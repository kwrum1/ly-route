#!/usr/bin/env sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
tmp=$(mktemp -d)
name="ly-route-orchestrator-api-$$"
port=${LY_ROUTE_ORCHESTRATOR_TEST_PORT:-18081}
cleanup() {
  docker rm -f "$name" >/dev/null 2>&1 || true
  rm -rf "$tmp"
}
trap cleanup EXIT INT TERM

mkdir -p "$tmp/context" "$tmp/state" "$tmp/evidence"
(cd "$repo_root/backend" && CGO_ENABLED=0 go build -trimpath -ldflags '-s -w' -o "$tmp/context/ly-route-control" ./cmd/orchestrator-control)
base_image=${LY_ROUTE_ORCHESTRATOR_BASE_IMAGE:-debian:bookworm-slim}
if ! docker image inspect "$base_image" >/dev/null 2>&1; then
  if docker image inspect ly-route/vpp-test:25.10 >/dev/null 2>&1; then
    base_image=ly-route/vpp-test:25.10
  else
    docker pull "$base_image" > "$tmp/evidence/base-image-pull.txt"
  fi
fi
docker image inspect --format '{{.Id}}' "$base_image" > "$tmp/evidence/base-image-id.txt"
cat > "$tmp/context/ping" <<'EOF'
#!/bin/sh
exit 1
EOF
cat > "$tmp/context/vppctl" <<'EOF'
#!/bin/sh
set -eu
printf '%s\n' "$*" >> /state/vppctl.log
case "$*" in
  "set ly-route orchestrator candidate clear")
    : > /state/orchestrator-candidate
    ;;
  "set ly-route orchestrator candidate "*)
    value=${*#set ly-route orchestrator candidate }
    case "$value" in
      rule\ *) printf 'policy %s\n' "${value#rule }" >> /state/orchestrator-candidate ;;
      *) printf '%s\n' "$value" >> /state/orchestrator-candidate ;;
    esac
    ;;
  "set ly-route orchestrator commit generation "*)
    set -- $*
    printf '%s\n' "$6" > /state/orchestrator-generation
    printf 'running\n' > /state/orchestrator-state
    cp /state/orchestrator-candidate /state/orchestrator-active
    ;;
  "set ly-route orchestrator disable")
    printf 'locked\n' > /state/orchestrator-state
    rm -f /state/orchestrator-generation /state/orchestrator-active
    ;;
  "show ly-route orchestrator")
    orchestrator_state=locked
    [ ! -f /state/orchestrator-state ] || orchestrator_state=$(cat /state/orchestrator-state)
    printf 'state %s\n' "$orchestrator_state"
    if [ -f /state/orchestrator-generation ]; then
      printf 'generation %s\n' "$(cat /state/orchestrator-generation)"
    fi
    [ ! -f /state/orchestrator-active ] || cat /state/orchestrator-active
    ;;
  "create bond mode "*" id "*)
    set -- $*
    printf '%s\n' "$4" > /state/bond-mode
    printf '%s\n' "$6" > /state/bond-id
    : > /state/bond-members
    ;;
  "set interface name BondEthernet"*)
    set -- $*
    printf '%s\n' "$5" > /state/bond-name
    ;;
  "bond add "*)
    set -- $*
    printf '%s\n' "$4" >> /state/bond-members
    ;;
  "delete bond "*)
    rm -f /state/bond-id /state/bond-mode /state/bond-name /state/bond-members
    ;;
  "show bond details")
    if [ -f /state/bond-id ]; then
      bond_id=$(cat /state/bond-id)
      bond_mode=$(cat /state/bond-mode)
      member_count=$(wc -l < /state/bond-members | tr -d ' ')
      printf 'BondEthernet%s\n' "$bond_id"
      printf '  mode: %s\n' "$bond_mode"
      printf '  load balance: %s\n' "$bond_mode"
      printf '  number of active members: 0\n'
      printf '  number of members: %s\n' "$member_count"
      while IFS= read -r member; do
        [ -n "$member" ] && printf '    %s\n' "$member"
      done < /state/bond-members
      printf '  device instance: %s\n' "$bond_id"
      printf '  interface id: %s\n' "$bond_id"
      printf '  sw_if_index: 5\n'
      printf '  hw_if_index: 5\n'
    fi
    ;;
esac
EOF
capability_observed_at=$(date -u +%Y-%m-%dT%H:%M:%SZ)
capability_valid_until=$(date -u -d '+300 seconds' +%Y-%m-%dT%H:%M:%SZ)
capability_proofs=
for interface in eth1 eth2 eth3 eth4 eth5 eth6 eth7; do
  proof=$(printf '{"linux_interface":"%s","proof":{"hook":"af_xdp","mode":"zero_copy","source":"runtime_probe","runtime_verified":true,"native":true,"high_performance":true,"observed_at":"%s","valid_until":"%s"}}' \
    "$interface" "$capability_observed_at" "$capability_valid_until")
  if [ -n "$capability_proofs" ]; then
    capability_proofs="$capability_proofs,$proof"
  else
    capability_proofs=$proof
  fi
done
printf '{"management_interface":"eth0","proofs":[%s]}\n' "$capability_proofs" \
  > "$tmp/state/vpp-native-capabilities.json"
cat > "$tmp/context/Dockerfile" <<'EOF'
ARG BASE_IMAGE
FROM ${BASE_IMAGE}
COPY ly-route-control /ly-route-control
COPY ping vppctl /usr/local/bin/
RUN chmod 0755 /usr/local/bin/ping /usr/local/bin/vppctl
ENTRYPOINT ["/ly-route-control"]
EOF
image="ly-route/orchestrator-api-test:$(git -C "$repo_root" rev-parse --short HEAD)"
docker build --quiet --build-arg "BASE_IMAGE=$base_image" --tag "$image" "$tmp/context" > "$tmp/evidence/image-id.txt"

start_dut() {
  docker run --detach --name "$name" --network host \
    -v "$tmp/state:/state" \
    -e LY_ROUTE_API_HOST=127.0.0.1 -e LY_ROUTE_API_PORT="$port" \
    -e LY_ROUTE_DB_PATH=/state/orchestrator.db \
    -e LY_ROUTE_ADMIN_USERNAME=admin -e LY_ROUTE_ADMIN_PASSWORD=container-secret \
    -e LY_ROUTE_FORCE_PASSWORD_CHANGE=false -e LY_ROUTE_ENABLE_VPP_INTERFACE_TELEMETRY=false \
    -e LY_ROUTE_PING=/usr/local/bin/ping -e LY_ROUTE_VPPCTL=/usr/local/bin/vppctl \
    -e LY_ROUTE_MANAGEMENT_INTERFACE=eth0 \
    -e LY_ROUTE_VPP_CAPABILITY_PROOF=/state/vpp-native-capabilities.json \
    -e LY_ROUTE_ORCHESTRATOR_RECONCILE_INTERVAL=200ms \
    "$image" > "$tmp/evidence/container-id.txt"
  attempts=0
  until curl --fail --silent "http://127.0.0.1:$port/api/v1/health" > "$tmp/evidence/health.json"; do
    attempts=$((attempts + 1))
    if [ "$attempts" -ge 50 ]; then docker logs "$name" >&2; return 1; fi
    sleep 0.1
  done
}

login() {
  curl --fail --silent --show-error -c "$tmp/cookies" -H 'Content-Type: application/json' \
    -d '{"username":"admin","password":"container-secret"}' \
    "http://127.0.0.1:$port/api/v1/auth/login" > "$tmp/login.json"
  python3 - "$tmp/login.json" "$tmp/evidence/login.json" <<'PY'
import json,sys
x=json.load(open(sys.argv[1]))
safe={'password_change_required':x['password_change_required'],'session':{'username':x['session']['username'],'role':x['session']['role']}}
json.dump(safe,open(sys.argv[2],'w'),sort_keys=True,separators=(',',':'))
PY
}

start_dut
login
curl --fail-with-body --silent --show-error -b "$tmp/cookies" -H 'Content-Type: application/json' -X PUT \
  --data-binary @"$repo_root/backend/internal/orchestratorapi/testdata/topology-v1.json" \
  "http://127.0.0.1:$port/api/v1/orchestrator/topology" > "$tmp/evidence/topology-put.json" || {
    cat "$tmp/evidence/topology-put.json" >&2
    exit 1
  }
curl --fail --silent --show-error -b "$tmp/cookies" -H 'Content-Type: application/json' -X POST \
  --data '{"id":"office-runtime","name":"Office runtime","kind":"ip","entries":["192.0.2.0/24","198.51.100.10-198.51.100.20"]}' \
  "http://127.0.0.1:$port/api/v1/objects/ip-groups" > "$tmp/evidence/ip-group-post.json"
curl --fail --silent --show-error -b "$tmp/cookies" \
  "http://127.0.0.1:$port/api/v1/objects/ip-groups" > "$tmp/evidence/ip-groups.json"
python3 - "$tmp/evidence/ip-groups.json" <<'PY'
import json,sys
x=json.load(open(sys.argv[1]))
item=next(item for item in x['items'] if item['id']=='office-runtime')
assert item['kind']=='ip'
assert item['entries']==['192.0.2.0/24','198.51.100.10-198.51.100.20']
PY
cat > "$tmp/policy.json" <<'EOF'
{"schema_version":1,"ip_objects":[{"id":"office","prefixes":["192.0.2.0/24"]}],"policy_groups":[{"id":"route","position":10,"rules":[{"id":"via-east","sequence":10,"match":{"sources":["office"],"destinations":["any"],"protocol":"tcp","destination_ports":[{"start":443,"end":443}]},"action":{"kind":"via","group":"inline-east"}}]}],"default":{"kind":"direct"}}
EOF
curl --fail --silent --show-error -b "$tmp/cookies" -H 'Content-Type: application/json' -X PUT --data-binary @"$tmp/policy.json" \
  "http://127.0.0.1:$port/api/v1/orchestrator/policy" > "$tmp/evidence/policy-put.json"
cat > "$tmp/compile.json" <<'EOF'
{"flow":{"source_ip":"192.0.2.10","destination_ip":"198.51.100.20","protocol":"tcp","source_port":41000,"destination_port":443},"prelude":{}}
EOF
curl --fail-with-body --silent --show-error -b "$tmp/cookies" -H 'Content-Type: application/json' --data-binary @"$tmp/compile.json" \
  "http://127.0.0.1:$port/api/v1/orchestrator/policy/compile" > "$tmp/evidence/policy-compile.json" || {
    cat "$tmp/evidence/policy-compile.json" >&2
    exit 1
  }
python3 - "$tmp/evidence/policy-compile.json" <<'PY'
import json,sys
x=json.load(open(sys.argv[1]))
assert x['path']['traversal']==['inline-east']
PY

curl --fail --silent --show-error -b "$tmp/cookies" \
  "http://127.0.0.1:$port/api/v1/config/export" > "$tmp/evidence/config-export.json"
python3 - "$tmp/evidence/config-export.json" <<'PY'
import json,sys
x=json.load(open(sys.argv[1]))
assert x['package_manifest']['product']=='orchestrator'
assert x['payload']['product']=='orchestrator'
resources=x['payload']['resources']
assert any(item['id']=='office-runtime' for item in resources['object_group'])
for forbidden in ('wan_link','wan_group','nat_static','port_map','dns_policy','dns_upstream','dhcp_server','proxy_node','proxy_subscription'):
    assert forbidden not in resources
PY
curl --fail --silent --show-error -b "$tmp/cookies" -H 'Content-Type: application/json' -X POST \
  --data '{"name":"runtime-before-restart"}' \
  "http://127.0.0.1:$port/api/v1/config/snapshots" > "$tmp/evidence/snapshot-create.json"
snapshot_id=$(python3 - "$tmp/evidence/snapshot-create.json" <<'PY'
import json,sys
print(json.load(open(sys.argv[1]))['snapshot']['id'])
PY
)
curl --fail --silent --show-error -b "$tmp/cookies" -H 'Content-Type: application/json' -X POST --data '{}' \
  "http://127.0.0.1:$port/api/v1/config/snapshots/$snapshot_id/restore" > "$tmp/evidence/snapshot-restore.json"
python3 - "$tmp/evidence/snapshot-restore.json" "$snapshot_id" <<'PY'
import json,sys
x=json.load(open(sys.argv[1]))
assert x['status']=='restored'
assert x['snapshot']['id']==sys.argv[2]
PY

docker rm -f "$name" >/dev/null
start_dut
login
curl --fail --silent --show-error -b "$tmp/cookies" \
  "http://127.0.0.1:$port/api/v1/orchestrator/policy" > "$tmp/evidence/policy-after-restart.json"
python3 - "$tmp/evidence/policy-put.json" "$tmp/evidence/policy-after-restart.json" <<'PY'
import json,sys
before=json.load(open(sys.argv[1])); after=json.load(open(sys.argv[2]))
assert before['checksum']==after['checksum']
assert after['item']['policy_groups'][0]['rules'][0]['action']=={'kind':'via','group':'inline-east'}
PY
curl --fail --silent --show-error -b "$tmp/cookies" \
  "http://127.0.0.1:$port/api/v1/objects/ip-groups" > "$tmp/evidence/ip-groups-after-restart.json"
python3 - "$tmp/evidence/ip-groups-after-restart.json" <<'PY'
import json,sys
x=json.load(open(sys.argv[1]))
assert any(item['id']=='office-runtime' and item['kind']=='ip' for item in x['items'])
PY
attempts=0
evidence_dir=${LY_ROUTE_ACCEPTANCE_EVIDENCE_DIR:-$repo_root/.sisyphus/full-acceptance/evidence/o-runtime-container}
rm -rf "$evidence_dir"
mkdir -p "$evidence_dir"
cp "$tmp/evidence/"* "$evidence_dir/"
cp "$tmp/state/vppctl.log" "$evidence_dir/vppctl.log"
git -C "$repo_root" rev-parse HEAD > "$evidence_dir/commit.txt"
sha256sum "$tmp/context/ly-route-control" > "$evidence_dir/binary.sha256"
printf '%s\n' "orchestrator container API/persistence/restart verification passed: $evidence_dir"
