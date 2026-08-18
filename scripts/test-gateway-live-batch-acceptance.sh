#!/usr/bin/env bash
#
# Non-destructive live Gateway acceptance matrix.
#
# The default mode only reads Gateway APIs and runs explicitly supplied client
# probes. It never installs an image, changes an ISO, or changes Gateway
# configuration. Tests that require a controlled mutation or a client fixture
# are reported as FIXTURE_FAIL until the operator explicitly supplies one.

set -u -o pipefail

repo_root=$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
run_id=$(date -u +%Y%m%dT%H%M%SZ)-$$

gateway_url=${LY_ROUTE_GATEWAY_URL:-https://10.1.18.125}
gateway_url=${gateway_url%/}
gateway_user=${LY_ROUTE_GATEWAY_USER:-admin}
gateway_password=${LY_ROUTE_GATEWAY_PASSWORD:-}
http_timeout=${LY_ROUTE_ACCEPTANCE_HTTP_TIMEOUT:-20}
telemetry_wait=${LY_ROUTE_TELEMETRY_WAIT_SECONDS:-20}
reboot_wait=${LY_ROUTE_REBOOT_WAIT_SECONDS:-180}
insecure_tls=${LY_ROUTE_ACCEPTANCE_INSECURE_TLS:-1}
evidence_dir=${LY_ROUTE_ACCEPTANCE_EVIDENCE_DIR:-"$repo_root/.acceptance/evidence/gateway-live-batch-$run_id"}

# These are deliberately opt-in. The script has no safe way to restore secrets
# excluded from a configuration export after an interrupted mutation.
allow_user_mutation=${LY_ROUTE_ALLOW_USER_MUTATION:-0}
allow_reboot=${LY_ROUTE_ALLOW_REBOOT:-0}
gateway_ssh=${LY_ROUTE_GATEWAY_SSH:-}
python_bin=${LY_ROUTE_ACCEPTANCE_PYTHON:-}

api_dir="$evidence_dir/api"
probe_dir="$evidence_dir/probes"
tmp_dir=
cookie_jar=
results_tsv="$evidence_dir/results.tsv"
summary_json="$evidence_dir/summary.json"
pass_count=0
product_fail_count=0
fixture_fail_count=0
temp_user=

usage() {
  cat <<'USAGE'
Usage:
  LY_ROUTE_GATEWAY_URL=https://gateway.example \
  LY_ROUTE_GATEWAY_USER=admin \
  LY_ROUTE_GATEWAY_PASSWORD='...' \
  bash scripts/test-gateway-live-batch-acceptance.sh

Optional independent client probes are shell commands. A probe exits 0 for
PASS, 125 for FIXTURE_FAIL, and any other non-zero status for PRODUCT_FAIL.

  LY_ROUTE_PROBE_IPV6_CLIENT
  LY_ROUTE_PROBE_WAN_PRIMARY_BACKUP
  LY_ROUTE_PROBE_WAN_WEIGHTED
  LY_ROUTE_PROBE_WAN_FIVE_TUPLE
  LY_ROUTE_PROBE_DHCP_CLIENT
  LY_ROUTE_PROBE_NAT_MODE_SWITCH
  LY_ROUTE_PROBE_DNS_UDP
  LY_ROUTE_PROBE_DNS_TCP
  LY_ROUTE_PROBE_DNS_DOMESTIC
  LY_ROUTE_PROBE_DNS_FOREIGN
  LY_ROUTE_PROBE_PROXY_NODE_FIXTURE
  LY_ROUTE_PROBE_PROXY_DOMESTIC
  LY_ROUTE_PROBE_PROXY_FOREIGN
  LY_ROUTE_PROBE_SMART_QOS
  LY_ROUTE_PROBE_TELEMETRY_TRAFFIC
  LY_ROUTE_PROBE_CONFIG_RESTORE
  LY_ROUTE_PROBE_USER_MANAGEMENT

Set LY_ROUTE_ALLOW_USER_MUTATION=1 to create, log in as, and delete one
temporary readonly user. Set LY_ROUTE_ALLOW_REBOOT=1 and
LY_ROUTE_GATEWAY_SSH=root@gateway.example to run a reboot persistence check.
Both opt-ins are disabled by default.

Exit status: 0 all PASS; 1 PRODUCT_FAIL only; 2 FIXTURE_FAIL only;
3 both PRODUCT_FAIL and FIXTURE_FAIL.
USAGE
}

if [[ ${1:-} == --help || ${1:-} == -h ]]; then
  usage
  exit 0
fi

mkdir -p "$api_dir" "$probe_dir"
tmp_dir=$(mktemp -d "${TMPDIR:-/tmp}/ly-route-gateway-batch.XXXXXX")
cookie_jar="$tmp_dir/cookies.txt"
printf 'result\tcheck\treason\n' >"$results_tsv"

cleanup() {
  # A failed user lifecycle check must not leave a new account behind.
  if [[ -n ${temp_user:-} && -f ${cookie_jar:-/nonexistent} ]]; then
    curl_request DELETE "/api/v1/auth/users/$temp_user" "$probe_dir/user-cleanup.json" '' "$cookie_jar" >/dev/null 2>&1 || true
  fi
  [[ -n ${tmp_dir:-} ]] && rm -rf "$tmp_dir"
}
trap cleanup EXIT INT TERM

curl_flags=(--silent --show-error --connect-timeout "$http_timeout" --max-time "$http_timeout")
if [[ $insecure_tls == 1 ]]; then
  curl_flags+=(--insecure)
fi

record() {
  local result=$1 check=$2 reason=${3:-}
  reason=${reason//$'\t'/ }
  reason=${reason//$'\r'/ }
  reason=${reason//$'\n'/ }
  printf '%s\t%s\t%s\n' "$result" "$check" "$reason"
  printf '%s\t%s\t%s\n' "$result" "$check" "$reason" >>"$results_tsv"
  case "$result" in
    PASS) pass_count=$((pass_count + 1)) ;;
    PRODUCT_FAIL) product_fail_count=$((product_fail_count + 1)) ;;
    FIXTURE_FAIL) fixture_fail_count=$((fixture_fail_count + 1)) ;;
    *) printf 'invalid result class: %s\n' "$result" >&2; exit 64 ;;
  esac
}

json_valid() {
  "$python_bin" -m json.tool "$1" >/dev/null 2>&1
}

classify_http_failure() {
  local check=$1 code=$2 detail=${3:-}
  case "$code" in
    000|'') record FIXTURE_FAIL "$check" "Gateway is unreachable: $detail" ;;
    401|403) record FIXTURE_FAIL "$check" "Gateway authentication or password-change precondition blocked the API (HTTP $code)" ;;
    *) record PRODUCT_FAIL "$check" "Gateway API returned HTTP $code: $detail" ;;
  esac
}

# curl_request writes its HTTP code to stdout. The request body is a file path
# or an empty string. The final argument is the cookie jar to use.
curl_request() {
  local method=$1 path=$2 output=$3 body=${4:-} jar=${5:-$cookie_jar}
  local error_file="${output}.stderr"
  local code rc
  mkdir -p "$(dirname -- "$output")"
  local args=("${curl_flags[@]}" -X "$method" -o "$output" -w '%{http_code}' -b "$jar" -c "$jar" -H 'Accept: application/json')
  if [[ -n $body ]]; then
    args+=(-H 'Content-Type: application/json' --data-binary "@$body")
  fi
  code=$(curl "${args[@]}" "$gateway_url$path" 2>"$error_file")
  rc=$?
  if [[ $rc -ne 0 ]]; then
    code=000
  fi
  printf '%s' "$code"
}

fetch() {
  local name=$1 path=$2
  local output="$api_dir/$name.json"
  local code
  code=$(curl_request GET "$path" "$output")
  printf '%s\n' "$code" >"$api_dir/$name.status"
  printf '%s\n' "$path" >"$api_dir/$name.path"
}

api_code() {
  local name=$1
  [[ -f $api_dir/$name.status ]] && tr -d '\r\n' <"$api_dir/$name.status" || printf '000'
}

need_api() {
  local check=$1 name=$2 code
  code=$(api_code "$name")
  if [[ $code != 200 ]]; then
    classify_http_failure "$check" "$code" "$(tr -d '\r\n' <"$api_dir/$name.stderr" 2>/dev/null || true)"
    return 1
  fi
  if ! json_valid "$api_dir/$name.json"; then
    record PRODUCT_FAIL "$check" "Gateway API returned invalid JSON for $(cat "$api_dir/$name.path")"
    return 1
  fi
  return 0
}

run_probe() {
  local check=$1 variable=$2 command=${!2:-}
  local log="$probe_dir/$check.log"
  local rc
  if [[ -z $command ]]; then
    record FIXTURE_FAIL "$check" "Missing independent client probe: set $variable"
    return 2
  fi
  bash -lc "$command" >"$log" 2>&1
  rc=$?
  case "$rc" in
    0) record PASS "$check" "Independent client probe succeeded"; return 0 ;;
    125|126|127) record FIXTURE_FAIL "$check" "Probe fixture is unavailable (exit $rc); see $log"; return 2 ;;
    *) record PRODUCT_FAIL "$check" "Independent client probe failed (exit $rc); see $log"; return 1 ;;
  esac
}

run_fixture_probe() {
  local check=$1 variable=$2 command=${!2:-}
  local log="$probe_dir/$check.log"
  local rc
  if [[ -z $command ]]; then
    record FIXTURE_FAIL "$check" "Missing fixture preflight: set $variable"
    return 2
  fi
  bash -lc "$command" >"$log" 2>&1
  rc=$?
  if [[ $rc -eq 0 ]]; then
    record PASS "$check" 'Acceptance fixture completed an independent end-to-end request'
    return 0
  fi
  record FIXTURE_FAIL "$check" "Acceptance fixture failed independently of the Gateway (exit $rc); see $log"
  return 2
}

record_python_result() {
  local check=$1 rc=$2 detail=${3:-}
  case "$rc" in
    0) record PASS "$check" "$detail" ;;
    2) record FIXTURE_FAIL "$check" "$detail" ;;
    *) record PRODUCT_FAIL "$check" "$detail" ;;
  esac
}

capture_baseline() {
  fetch health /api/v1/health
  fetch runtime /api/v1/runtime/status
  fetch pppoe /api/v1/gateway/pppoe/status
  fetch interfaces /api/v1/interfaces
  fetch wan_links /api/v1/gateway/wan-links
  fetch wan_groups /api/v1/gateway/wan-groups
  fetch routes /api/v1/gateway/policies/routes
  fetch port_maps /api/v1/gateway/nat/port-maps
  fetch dns_policies /api/v1/dns/policies
  fetch dns_upstreams /api/v1/dns/upstreams
  fetch proxy_egresses /api/v1/proxy/egresses
  fetch smart_qos /api/v1/flow-control/smart-qos
  fetch traffic_control /api/v1/gateway/traffic-control
  fetch top_sessions /api/v1/telemetry/top-sessions
  fetch online_users /api/v1/telemetry/online-users
  fetch traffic_trend '/api/v1/telemetry/traffic-trend?window=5m&points=12'
  fetch config_export /api/v1/config/export
  fetch snapshots /api/v1/config/snapshots
  fetch users /api/v1/auth/users
}

login() {
  local body="$tmp_dir/login.json" output="$api_dir/login.json" code rc
  if [[ -z $gateway_password ]]; then
    record FIXTURE_FAIL gateway_login 'LY_ROUTE_GATEWAY_PASSWORD is required'
    return 1
  fi
  "$python_bin" - "$gateway_user" "$gateway_password" >"$body" <<'PY'
import json
import sys
json.dump({"username": sys.argv[1], "password": sys.argv[2]}, sys.stdout)
PY
  code=$(curl "${curl_flags[@]}" -X POST -o "$output" -w '%{http_code}' -c "$cookie_jar" -H 'Accept: application/json' -H 'Content-Type: application/json' --data-binary "@$body" "$gateway_url/api/v1/auth/login" 2>"$output.stderr")
  rc=$?
  [[ $rc -eq 0 ]] || code=000
  printf '%s\n' "$code" >"$api_dir/login.status"
  if [[ $code == 200 ]] && json_valid "$output"; then
    record PASS gateway_login 'Authenticated Gateway API session established'
    return 0
  fi
  classify_http_failure gateway_login "$code" "$(tr -d '\r\n' <"$output.stderr" 2>/dev/null || true)"
  return 1
}

check_runtime() {
  need_api runtime_components runtime || return
  local detail rc
  detail=$("$python_bin" - "$api_dir/runtime.json" <<'PY'
import json
import sys
data = json.load(open(sys.argv[1], encoding="utf-8"))
components = {item.get("name"): item for item in data.get("components", [])}
required = ("vpp", "pppoe", "smartdns", "kea", "xray", "persistence")
missing = [name for name in required if name not in components]
bad = [name for name in required if name in components and (components[name].get("state") != "running" or not components[name].get("available"))]
last = data.get("last_apply", {})
if missing or bad or last.get("status") != "committed" or last.get("runtime_state") != "running":
    print("missing=%s degraded=%s last_apply=%s/%s" % (missing, bad, last.get("status"), last.get("runtime_state")))
    raise SystemExit(1)
print("VPP, PPPoE, SmartDNS, Kea, Xray, and persistence are running")
PY
  )
  rc=$?
  record_python_result runtime_components "$rc" "$detail"
}

check_pppoe_ipv6() {
  need_api pppoe_ipv6_pd_ra_runtime pppoe || return
  need_api pppoe_ipv6_pd_ra_runtime interfaces || return
  local detail rc
  detail=$("$python_bin" - "$api_dir/pppoe.json" "$api_dir/interfaces.json" <<'PY'
import json
import sys
pppoe = json.load(open(sys.argv[1], encoding="utf-8"))
interfaces = json.load(open(sys.argv[2], encoding="utf-8"))
items = pppoe.get("items", pppoe if isinstance(pppoe, list) else [])
connected = [item for item in items if item.get("state") == "connected"]
if not connected:
    print("no connected PPPoE acceptance WAN")
    raise SystemExit(2)
if not any(str(item.get("assigned_ipv6", "")).strip() for item in connected):
    print("connected PPPoE has no IPv6 address or delegated prefix")
    raise SystemExit(1)
lan_items = interfaces.get("items", interfaces if isinstance(interfaces, list) else [])
delegated = []
for item in lan_items:
    if item.get("gateway_role") != "lan":
        continue
    ipv6 = item.get("ipv6") or {}
    if str(ipv6.get("mode", "")).lower() == "delegated_prefix" and ipv6.get("source_wan_id"):
        delegated.append(item.get("id") or item.get("system_name"))
if not delegated:
    print("no LAN is configured for delegated-prefix RA")
    raise SystemExit(2)
print("connected IPv6 PPPoE and delegated-prefix LAN=%s" % ",".join(map(str, delegated)))
PY
  )
  rc=$?
  record_python_result pppoe_ipv6_pd_ra_runtime "$rc" "$detail"
  run_probe pppoe_ipv6_client_access LY_ROUTE_PROBE_IPV6_CLIENT || true
}

check_wan_mode() {
  local check=$1 wanted=$2 probe=$3
  need_api "$check" wan_links || return
  need_api "$check" wan_groups || return
  local detail rc
  detail=$("$python_bin" - "$api_dir/wan_links.json" "$api_dir/wan_groups.json" "$wanted" <<'PY'
import json
import sys
links = json.load(open(sys.argv[1], encoding="utf-8"))
groups = json.load(open(sys.argv[2], encoding="utf-8"))
wanted = sys.argv[3]
link_items = links.get("items", links if isinstance(links, list) else [])
group_items = groups.get("items", groups if isinstance(groups, list) else [])
enabled = [item for item in link_items if item.get("enabled", True)]
if len(enabled) < 2:
    print("need two enabled WAN links; found %d" % len(enabled))
    raise SystemExit(2)
def mode(item):
    value = item.get("mode") or (item.get("load_balance") or {}).get("mode") or ""
    value = str(value).lower().replace("-", "_")
    aliases = {"active_backup": "primary_backup", "failover": "primary_backup", "per_connection_weighted": "weighted", "weighted_load": "weighted", "ecmp": "five_tuple", "per_connection": "five_tuple"}
    return aliases.get(value, value)
matches = [item for item in group_items if mode(item) == wanted]
if not matches:
    print("no WAN group configured for %s" % wanted)
    raise SystemExit(2)
print("WAN group(s) configured for %s: %s" % (wanted, ",".join(str(item.get("id")) for item in matches)))
PY
  )
  rc=$?
  record_python_result "$check" "$rc" "$detail"
  run_probe "${check}_client_flow" "$probe" || true
}

check_nat() {
  need_api nat_ed_full_cone_intent routes || return
  need_api nat_ed_full_cone_intent wan_links || return
  local detail rc
  detail=$("$python_bin" - "$api_dir/routes.json" "$api_dir/wan_links.json" <<'PY'
import json
import sys
routes = json.load(open(sys.argv[1], encoding="utf-8"))
links = json.load(open(sys.argv[2], encoding="utf-8"))
routes = routes.get("items", routes if isinstance(routes, list) else [])
links = links.get("items", links if isinstance(links, list) else [])
values = []
for item in list(routes) + list(links):
    if str(item.get("action", "nat")).lower() == "nat" or "nat_behavior" in item:
        values.append(str(item.get("nat_behavior") or item.get("nat_mode") or "endpoint_dependent").lower())
full = any(value in ("full_cone", "fullcone", "endpoint_independent", "cone") for value in values)
ed = any(value in ("", "endpoint_dependent", "nat44_ed", "ed", "default") for value in values)
if not full or not ed:
    print("acceptance intent needs both endpoint-dependent and full-cone entries; ed=%s full_cone=%s" % (ed, full))
    raise SystemExit(2)
print("both endpoint-dependent and full-cone intent are present")
PY
  )
  rc=$?
  record_python_result nat_ed_full_cone_intent "$rc" "$detail"
  run_probe nat_ed_full_cone_mode_switch LY_ROUTE_PROBE_NAT_MODE_SWITCH || true
}

check_dns() {
  need_api dns_geosite_doh_configuration dns_policies || return
  need_api dns_geosite_doh_configuration dns_upstreams || return
  local detail rc
  detail=$("$python_bin" - "$api_dir/dns_policies.json" "$api_dir/dns_upstreams.json" <<'PY'
import json
import sys
policies = json.load(open(sys.argv[1], encoding="utf-8"))
upstreams = json.load(open(sys.argv[2], encoding="utf-8"))
policies = policies.get("items", policies if isinstance(policies, list) else [])
upstreams = upstreams.get("items", upstreams if isinstance(upstreams, list) else [])
def strings(value):
    if isinstance(value, dict):
        for key, child in value.items():
            yield str(key)
            yield from strings(child)
    elif isinstance(value, list):
        for child in value:
            yield from strings(child)
    elif value is not None:
        yield str(value)
haystack = " ".join(value.lower() for value in strings(policies))
has_geosite = "geosite" in haystack or "obj-geosite-" in haystack
doh_ids = set()
for item in upstreams:
    servers = item.get("servers") or []
    if any(str(server).lower().startswith("https://") for server in servers):
        doh_ids.add(str(item.get("id", "")))
if not has_geosite:
    print("no configured DNS policy references a GeoSite selector")
    raise SystemExit(2)
if not doh_ids:
    print("no configured DNS upstream uses DoH")
    raise SystemExit(2)
if not any(identifier and identifier in haystack for identifier in doh_ids):
    print("configured GeoSite/DNS policy does not reference a DoH upstream")
    raise SystemExit(2)
print("GeoSite policy and DoH upstream are configured")
PY
  )
  rc=$?
  record_python_result dns_geosite_doh_configuration "$rc" "$detail"
  run_probe dns_udp_transparent_hijack LY_ROUTE_PROBE_DNS_UDP || true
  run_probe dns_tcp_transparent_hijack LY_ROUTE_PROBE_DNS_TCP || true
  run_probe dns_geosite_domestic_doh LY_ROUTE_PROBE_DNS_DOMESTIC || true
  run_probe dns_default_foreign_doh LY_ROUTE_PROBE_DNS_FOREIGN || true
}

check_proxy_split() {
  need_api proxy_domestic_foreign_configuration routes || return
  need_api proxy_domestic_foreign_configuration proxy_egresses || return
  local detail rc
  detail=$("$python_bin" - "$api_dir/routes.json" "$api_dir/proxy_egresses.json" <<'PY'
import json
import sys
routes = json.load(open(sys.argv[1], encoding="utf-8"))
egresses = json.load(open(sys.argv[2], encoding="utf-8"))
routes = routes.get("items", routes if isinstance(routes, list) else [])
egresses = egresses.get("items", egresses if isinstance(egresses, list) else [])
proxy_ids = {str(item.get("id")) for item in egresses if item.get("enabled", True)}
proxy_routes = [item for item in routes if str(item.get("egress", "")) in proxy_ids]
direct_routes = [item for item in routes if str(item.get("egress", "")) not in proxy_ids and str(item.get("action", "")).lower() in ("nat", "route", "direct")]
if not proxy_routes or not direct_routes:
    print("need both direct and proxy route policies; direct=%d proxy=%d" % (len(direct_routes), len(proxy_routes)))
    raise SystemExit(2)
print("direct route policies=%d proxy route policies=%d" % (len(direct_routes), len(proxy_routes)))
PY
  )
  rc=$?
  record_python_result proxy_domestic_foreign_configuration "$rc" "$detail"
  if ! run_fixture_probe proxy_node_fixture LY_ROUTE_PROBE_PROXY_NODE_FIXTURE; then
    record FIXTURE_FAIL proxy_foreign_via_xray 'Proxy node fixture did not pass its independent preflight; Gateway proxy behavior was not judged'
    return
  fi
  run_probe proxy_domestic_direct LY_ROUTE_PROBE_PROXY_DOMESTIC || true
  run_probe proxy_foreign_via_xray LY_ROUTE_PROBE_PROXY_FOREIGN || true
}

check_smart_qos() {
  need_api smart_qos_runtime smart_qos || return
  need_api smart_qos_runtime traffic_control || return
  local detail rc
  detail=$("$python_bin" - "$api_dir/smart_qos.json" "$api_dir/traffic_control.json" <<'PY'
import json
import sys
qos = json.load(open(sys.argv[1], encoding="utf-8"))
traffic = json.load(open(sys.argv[2], encoding="utf-8"))
item = qos.get("item", qos)
if item.get("runtime_state") != "running" or not item.get("enabled"):
    print("Smart QoS is not running: %s" % item.get("runtime_state"))
    raise SystemExit(1)
items = traffic.get("items", traffic if isinstance(traffic, list) else [])
if not items:
    print("no traffic-control policy is configured for a loaded-flow acceptance probe")
    raise SystemExit(2)
print("Smart QoS is running with %d traffic-control policy object(s)" % len(items))
PY
  )
  rc=$?
  record_python_result smart_qos_runtime "$rc" "$detail"
  run_probe smart_qos_loaded_flow LY_ROUTE_PROBE_SMART_QOS || true
}

check_monitoring_api() {
  local names=(top_sessions online_users traffic_trend)
  local name
  for name in "${names[@]}"; do
    need_api monitoring_api_contract "$name" || return
  done
  record PASS monitoring_api_contract 'Top connections, online users, and traffic trend APIs returned valid JSON'
}

check_monitoring_data() {
  local probe_rc
  run_probe telemetry_generate_client_flow LY_ROUTE_PROBE_TELEMETRY_TRAFFIC
  probe_rc=$?
  if [[ $probe_rc -ne 0 ]]; then
    if [[ $probe_rc -eq 2 ]]; then
      record FIXTURE_FAIL monitoring_live_samples 'No traffic generator is available to populate online users, Top connections, and charts'
    else
      record PRODUCT_FAIL monitoring_live_samples 'Traffic generator failed before telemetry could be observed'
    fi
    return
  fi

  local deadline=$((SECONDS + telemetry_wait))
  local ready=0
  while (( SECONDS < deadline )); do
    fetch top_sessions /api/v1/telemetry/top-sessions
    fetch online_users /api/v1/telemetry/online-users
    fetch traffic_trend '/api/v1/telemetry/traffic-trend?window=5m&points=12'
    if [[ $(api_code top_sessions) == 200 && $(api_code online_users) == 200 && $(api_code traffic_trend) == 200 ]]; then
      if "$python_bin" - "$api_dir/top_sessions.json" "$api_dir/online_users.json" "$api_dir/traffic_trend.json" <<'PY'
import json
import sys
top = json.load(open(sys.argv[1], encoding="utf-8"))
online = json.load(open(sys.argv[2], encoding="utf-8"))
trend = json.load(open(sys.argv[3], encoding="utf-8"))
def items(value):
    value = value.get("data", value) if isinstance(value, dict) else value
    return value.get("items", []) if isinstance(value, dict) else value if isinstance(value, list) else []
series = ((trend.get("series") or {}).get("logical_egresses") or []) if isinstance(trend, dict) else []
samples = [sample for entry in series for sample in entry.get("samples", [])]
activity = any(float(sample.get("download_bps") or 0) > 0 or float(sample.get("upload_bps") or 0) > 0 for sample in samples)
raise SystemExit(0 if items(top) and items(online) and len(samples) >= 2 and activity else 1)
PY
      then
        ready=1
        break
      fi
    fi
    sleep 1
  done
  if [[ $ready == 1 ]]; then
    record PASS monitoring_live_samples 'Independent traffic populated online users, Top connections, and a nonzero traffic trend'
  else
    record PRODUCT_FAIL monitoring_live_samples "Telemetry did not expose active users, sessions, and nonzero trend data within ${telemetry_wait}s"
  fi
}

check_config() {
  need_api config_export config_export || return
  local detail rc request="$tmp_dir/config-import-dry-run.json" output="$api_dir/config-import-dry-run.json" code
  detail=$("$python_bin" - "$api_dir/config_export.json" "$request" <<'PY'
import json
import sys
source = json.load(open(sys.argv[1], encoding="utf-8"))
if not isinstance(source.get("package_manifest"), dict) or not isinstance(source.get("payload"), dict):
    print("export is missing package_manifest or payload")
    raise SystemExit(1)
payload = source["payload"]
manifest = source["package_manifest"]
if payload.get("product") != "gateway" or manifest.get("product") != "gateway":
    print("exported package is not a Gateway package")
    raise SystemExit(1)
json.dump({"confirm": False, "dry_run": True, "package_manifest": manifest, "payload": payload}, open(sys.argv[2], "w", encoding="utf-8"))
print("Gateway desired configuration export is structurally valid")
PY
  )
  rc=$?
  record_python_result config_export "$rc" "$detail"
  [[ $rc -eq 0 ]] || return

  code=$(curl_request POST /api/v1/config/import "$output" "$request")
  printf '%s\n' "$code" >"$api_dir/config-import-dry-run.status"
  if [[ $code != 200 ]]; then
    classify_http_failure config_import_dry_run "$code" "$(tr -d '\r\n' <"$output.stderr" 2>/dev/null || true)"
  else
    detail=$("$python_bin" - "$output" <<'PY'
import json
import sys
data = json.load(open(sys.argv[1], encoding="utf-8"))
if data.get("status") != "dry_run" or not data.get("safe_to_apply"):
    print("unexpected dry-run response: status=%s safe_to_apply=%s" % (data.get("status"), data.get("safe_to_apply")))
    raise SystemExit(1)
print("import dry-run accepted without persisting configuration")
PY
    )
    rc=$?
    record_python_result config_import_dry_run "$rc" "$detail"
  fi

  if need_api config_snapshot_inventory snapshots; then
    detail=$("$python_bin" - "$api_dir/snapshots.json" <<'PY'
import json
import sys
data = json.load(open(sys.argv[1], encoding="utf-8"))
if not isinstance(data.get("items"), list):
    print("snapshot response has no items list")
    raise SystemExit(1)
print("snapshot inventory is readable")
PY
    )
    rc=$?
    record_python_result config_snapshot_inventory "$rc" "$detail"
  fi
  run_probe config_restore_controlled LY_ROUTE_PROBE_CONFIG_RESTORE || true
}

user_lifecycle() {
  local username="batch-accept-${run_id//[^a-zA-Z0-9]/}" password="BatchAccept2026!" body="$tmp_dir/temp-user.json" output="$probe_dir/user-create.json" code rc detail temp_cookie="$tmp_dir/temp-user.cookies"
  temp_user=$username
  "$python_bin" - "$username" "$password" >"$body" <<'PY'
import json
import sys
json.dump({"username": sys.argv[1], "role": "readonly", "password": sys.argv[2], "enabled": True}, sys.stdout)
PY
  code=$(curl_request POST /api/v1/auth/users "$output" "$body")
  if [[ $code != 200 ]]; then
    classify_http_failure user_management_lifecycle "$code" "temporary user creation failed"
    return
  fi
  code=$(curl "${curl_flags[@]}" -X POST -o "$probe_dir/user-login.json" -w '%{http_code}' -c "$temp_cookie" -H 'Accept: application/json' -H 'Content-Type: application/json' --data-binary "@$body" "$gateway_url/api/v1/auth/login" 2>"$probe_dir/user-login.json.stderr")
  rc=$?
  [[ $rc -eq 0 ]] || code=000
  if [[ $code != 200 ]]; then
    classify_http_failure user_management_lifecycle "$code" 'temporary user could not authenticate'
    return
  fi
  code=$(curl_request DELETE "/api/v1/auth/users/$username" "$probe_dir/user-delete.json")
  if [[ $code != 200 ]]; then
    classify_http_failure user_management_lifecycle "$code" 'temporary user was created but could not be deleted'
    return
  fi
  temp_user=
  record PASS user_management_lifecycle 'Temporary readonly user creation, login, and deletion succeeded'
}

check_users() {
  need_api user_management_read users || return
  local detail rc
  detail=$("$python_bin" - "$api_dir/users.json" <<'PY'
import json
import sys
data = json.load(open(sys.argv[1], encoding="utf-8"))
items = data.get("items")
if not isinstance(items, list) or not any(item.get("username") == "admin" and item.get("role") == "admin" for item in items):
    print("admin user is missing from the user inventory")
    raise SystemExit(1)
print("user inventory contains the built-in admin account")
PY
  )
  rc=$?
  record_python_result user_management_read "$rc" "$detail"
  if [[ -n ${LY_ROUTE_PROBE_USER_MANAGEMENT:-} ]]; then
    run_probe user_management_lifecycle LY_ROUTE_PROBE_USER_MANAGEMENT || true
  elif [[ $allow_user_mutation == 1 ]]; then
    user_lifecycle
  else
    record FIXTURE_FAIL user_management_lifecycle 'Set LY_ROUTE_ALLOW_USER_MUTATION=1 or provide LY_ROUTE_PROBE_USER_MANAGEMENT'
  fi
}

relogin_quietly() {
  local body="$tmp_dir/relogin.json" output="$probe_dir/relogin.json" code rc
  "$python_bin" - "$gateway_user" "$gateway_password" >"$body" <<'PY'
import json
import sys
json.dump({"username": sys.argv[1], "password": sys.argv[2]}, sys.stdout)
PY
  code=$(curl "${curl_flags[@]}" -X POST -o "$output" -w '%{http_code}' -c "$cookie_jar" -H 'Accept: application/json' -H 'Content-Type: application/json' --data-binary "@$body" "$gateway_url/api/v1/auth/login" 2>"$output.stderr")
  rc=$?
  [[ $rc -eq 0 && $code == 200 ]]
}

check_reboot_persistence() {
  if [[ $allow_reboot != 1 || -z $gateway_ssh ]]; then
    record FIXTURE_FAIL reboot_persistence 'Set LY_ROUTE_ALLOW_REBOOT=1 and LY_ROUTE_GATEWAY_SSH=root@gateway to enable reboot acceptance'
    return
  fi
  need_api reboot_persistence config_export || return
  local before_hash ssh_rc deadline detail rc
  before_hash=$("$python_bin" - "$api_dir/config_export.json" <<'PY'
import json
import sys
print(json.load(open(sys.argv[1], encoding="utf-8")).get("package_manifest", {}).get("package_hash", ""))
PY
)
  if [[ -z $before_hash ]]; then
    record PRODUCT_FAIL reboot_persistence 'Cannot establish pre-reboot configuration hash'
    return
  fi
  ssh -o BatchMode=yes -o ConnectTimeout="$http_timeout" -o StrictHostKeyChecking=accept-new "$gateway_ssh" 'systemctl reboot' >"$probe_dir/reboot-ssh.log" 2>&1
  ssh_rc=$?
  # systemctl reboot can close SSH before its command status is returned.
  if [[ $ssh_rc -ne 0 ]] && ! grep -Eqi 'closed|reset|broken pipe' "$probe_dir/reboot-ssh.log"; then
    record FIXTURE_FAIL reboot_persistence "Cannot request reboot through $gateway_ssh; see $probe_dir/reboot-ssh.log"
    return
  fi
  rm -f "$cookie_jar"
  deadline=$((SECONDS + reboot_wait))
  while (( SECONDS < deadline )); do
    if relogin_quietly; then
      fetch config_export_after_reboot /api/v1/config/export
      fetch runtime_after_reboot /api/v1/runtime/status
      if [[ $(api_code config_export_after_reboot) == 200 && $(api_code runtime_after_reboot) == 200 ]]; then
        break
      fi
    fi
    sleep 2
  done
  if [[ $(api_code config_export_after_reboot) != 200 || $(api_code runtime_after_reboot) != 200 ]]; then
    record PRODUCT_FAIL reboot_persistence "Gateway did not return authenticated API service within ${reboot_wait}s"
    return
  fi
  detail=$("$python_bin" - "$api_dir/config_export_after_reboot.json" "$api_dir/runtime_after_reboot.json" "$before_hash" <<'PY'
import json
import sys
after = json.load(open(sys.argv[1], encoding="utf-8"))
runtime = json.load(open(sys.argv[2], encoding="utf-8"))
before_hash = sys.argv[3]
after_hash = after.get("package_manifest", {}).get("package_hash", "")
components = {item.get("name"): item for item in runtime.get("components", [])}
required = ("vpp", "pppoe", "smartdns", "kea", "xray", "persistence")
bad = [name for name in required if components.get(name, {}).get("state") != "running"]
if after_hash != before_hash or bad:
    print("before_hash=%s after_hash=%s degraded=%s" % (before_hash, after_hash, bad))
    raise SystemExit(1)
print("configuration hash persisted and all Gateway components returned after reboot")
PY
  )
  rc=$?
  record_python_result reboot_persistence "$rc" "$detail"
}

write_summary() {
  if [[ -n $python_bin ]] && command -v "$python_bin" >/dev/null 2>&1; then
    "$python_bin" - "$results_tsv" "$summary_json" "$gateway_url" <<'PY'
import csv
import json
import sys
rows = []
with open(sys.argv[1], encoding="utf-8", newline="") as source:
    for row in csv.DictReader(source, delimiter="\t"):
        rows.append(row)
counts = {"PASS": 0, "PRODUCT_FAIL": 0, "FIXTURE_FAIL": 0}
for row in rows:
    counts[row["result"]] = counts.get(row["result"], 0) + 1
json.dump({"gateway_url": sys.argv[3], "counts": counts, "results": rows}, open(sys.argv[2], "w", encoding="utf-8"), indent=2)
PY
  fi
  local summary_line="Acceptance summary: PASS=$pass_count PRODUCT_FAIL=$product_fail_count FIXTURE_FAIL=$fixture_fail_count"
  printf '%s\nEvidence: %s\n' "$summary_line" "$evidence_dir" >"$evidence_dir/summary.txt"
  printf '\n%s\nEvidence: %s\n' "$summary_line" "$evidence_dir"
}

main() {
  if [[ -z $python_bin ]]; then
    if command -v python3 >/dev/null 2>&1; then
      python_bin=python3
    elif command -v python >/dev/null 2>&1; then
      python_bin=python
    fi
  fi
  if ! command -v curl >/dev/null 2>&1; then
    record FIXTURE_FAIL prerequisites 'curl is required'
    write_summary
    exit 2
  fi
  if [[ -z $python_bin ]] || ! command -v "$python_bin" >/dev/null 2>&1; then
    record FIXTURE_FAIL prerequisites 'python3 or python is required for JSON validation'
    write_summary
    exit 2
  fi
  if ! login; then
    write_summary
    return
  fi

  capture_baseline
  check_runtime
  run_probe lan_dhcp_client_lease LY_ROUTE_PROBE_DHCP_CLIENT || true
  check_pppoe_ipv6
  check_wan_mode wan_primary_backup_configuration primary_backup LY_ROUTE_PROBE_WAN_PRIMARY_BACKUP
  check_wan_mode wan_weighted_configuration weighted LY_ROUTE_PROBE_WAN_WEIGHTED
  check_wan_mode wan_five_tuple_configuration five_tuple LY_ROUTE_PROBE_WAN_FIVE_TUPLE
  check_nat
  check_dns
  check_proxy_split
  check_smart_qos
  check_monitoring_api
  check_monitoring_data
  check_config
  check_users
  check_reboot_persistence
  write_summary
}

main "$@"
if (( product_fail_count > 0 && fixture_fail_count > 0 )); then
  exit 3
elif (( product_fail_count > 0 )); then
  exit 1
elif (( fixture_fail_count > 0 )); then
  exit 2
fi
