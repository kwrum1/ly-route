#!/bin/sh
set -eu

: "${LY_ROUTE_MANAGEMENT_FALLBACK:=yes}"
: "${LY_ROUTE_MANAGEMENT_FALLBACK_CIDR:=192.168.88.254/24}"
: "${LY_ROUTE_MANAGEMENT_FALLBACK_ROUTE_METRIC:=4096}"
: "${LY_ROUTE_MANAGEMENT_FALLBACK_GATEWAY:=192.168.88.1}"

installed_network=/etc/ly-route/installed-network.json
management_ip=${LY_ROUTE_MANAGEMENT_FALLBACK_CIDR%/*}
if [ -s "$installed_network" ] && command -v python3 >/dev/null 2>&1; then
  installed_values=$(python3 - "$installed_network" <<'PY'
import json
import pathlib
import sys

def interface_identity(path):
    try:
        mac = (path / "address").read_text(encoding="ascii").strip().lower()
    except OSError:
        mac = ""
    try:
        pci = (path / "device").resolve(strict=True).name.lower()
    except OSError:
        pci = ""
    return mac, pci

def resolve(identity, inventory):
    expected_mac = str(identity.get("mac", "")).strip().lower()
    expected_pci = str(identity.get("pci", "")).strip().lower()
    for name, (mac, pci) in inventory.items():
        if expected_mac and mac == expected_mac:
            return name
        if expected_pci and expected_pci != "unknown" and pci == expected_pci:
            return name
    return ""

try:
    with open(sys.argv[1], encoding="utf-8") as source:
        document = json.load(source)
    management = document.get("management", {})
    inventory = {
        path.name: interface_identity(path)
        for path in pathlib.Path("/sys/class/net").iterdir()
        if path.name != "lo"
    }
    management_name = resolve(management, inventory)
    data_names = []
    for identity in document.get("data_interfaces", []):
        name = resolve(identity, inventory)
        if name and name != management_name and name not in data_names:
            data_names.append(name)
    print("|".join((management_name, str(management.get("mac", "")).strip().lower(),
                    str(management.get("cidr", "")).strip(),
                    str(management.get("gateway", "")).strip(), ",".join(data_names))))
except (OSError, ValueError, TypeError):
    raise SystemExit(1)
PY
  ) || installed_values=
  old_ifs=$IFS
  IFS='|'
  set -- $installed_values
  IFS=$old_ifs
  if [ "$#" -ge 4 ]; then
    installed_management_interface=$1
    installed_management_mac=$2
    [ -z "$3" ] || LY_ROUTE_MANAGEMENT_FALLBACK_CIDR=$3
    [ -z "$4" ] || LY_ROUTE_MANAGEMENT_FALLBACK_GATEWAY=$4
    installed_data_interfaces=${5:-}
  fi
fi
management_ip=${LY_ROUTE_MANAGEMENT_FALLBACK_CIDR%/*}

state_dir=/var/lib/ly-route
mkdir -p "$state_dir"

dns_sync_token=/etc/ly-route/dns-sync.token
if [ ! -s "$dns_sync_token" ]; then
  umask 077
  openssl rand -hex 32 > "$dns_sync_token"
fi
chmod 600 "$dns_sync_token"

if [ -n "${LY_ROUTE_SYSTEM_ROOT_PASSWORD:-}" ] && command -v chpasswd >/dev/null 2>&1; then
  printf 'root:%s\n' "$LY_ROUTE_SYSTEM_ROOT_PASSWORD" | chpasswd
  passwd -u root >/dev/null 2>&1 || true
fi

if [ -n "${LY_ROUTE_SYSTEM_ADMIN_PASSWORD:-}" ] && command -v useradd >/dev/null 2>&1 && command -v chpasswd >/dev/null 2>&1; then
  if ! id admin >/dev/null 2>&1; then
    if getent group sudo >/dev/null 2>&1; then
      useradd -m -s /bin/bash -G sudo admin
    else
      useradd -m -s /bin/bash admin
    fi
  fi
  if getent group vpp >/dev/null 2>&1; then
    usermod -a -G vpp admin
  fi
  printf 'admin:%s\n' "$LY_ROUTE_SYSTEM_ADMIN_PASSWORD" | chpasswd
fi

if [ -f /etc/ssh/sshd_config ]; then
  sed -i 's/^#\?PasswordAuthentication .*/PasswordAuthentication yes/' /etc/ssh/sshd_config
  sed -i 's/^#\?PermitRootLogin .*/PermitRootLogin yes/' /etc/ssh/sshd_config
  systemctl enable ssh.service >/dev/null 2>&1 || true
  systemctl try-restart ssh.service >/dev/null 2>&1 || true
fi

tls_dir=/etc/ly-route/tls
mkdir -p "$tls_dir"
if [ ! -s "$tls_dir/admin.key" ] || [ ! -s "$tls_dir/admin.crt" ]; then
  openssl req -x509 -nodes -newkey rsa:2048 -sha256 -days 3650 \
    -subj '/CN=ly-route.local/O=Ly Route Appliance' \
    -addext "subjectAltName=DNS:ly-route.local,IP:$management_ip,IP:192.168.88.254,IP:192.168.88.1,IP:127.0.0.1" \
    -keyout "$tls_dir/admin.key" \
    -out "$tls_dir/admin.crt"
  chmod 600 "$tls_dir/admin.key"
  chmod 644 "$tls_dir/admin.crt"
fi

lan_if=${installed_management_interface:-}
[ -n "${lan_if:-}" ] || lan_if=$(ip -o link show | awk -F': ' '$2 != "lo" {print $2; exit}' | cut -d'@' -f1)

if [ -n "${lan_if:-}" ] && [ "$LY_ROUTE_MANAGEMENT_FALLBACK" = "yes" ]; then
  mkdir -p /etc/systemd/network
  management_network=/etc/systemd/network/05-ly-route-management.network
  if [ -n "${installed_management_mac:-}" ]; then
    cat > "$management_network" <<EOF
[Match]
MACAddress=$installed_management_mac

[Network]
Address=$LY_ROUTE_MANAGEMENT_FALLBACK_CIDR
DHCP=no
LinkLocalAddressing=ipv4
IPv6AcceptRA=yes
Gateway=$LY_ROUTE_MANAGEMENT_FALLBACK_GATEWAY
EOF
  else
    cat > "$management_network" <<EOF
[Match]
Name=$lan_if

[Network]
Address=$LY_ROUTE_MANAGEMENT_FALLBACK_CIDR
DHCP=no
LinkLocalAddressing=ipv4
IPv6AcceptRA=yes
Gateway=$LY_ROUTE_MANAGEMENT_FALLBACK_GATEWAY
EOF
  fi
  rm -f /run/systemd/network/05-ly-route-lan.network
  ip link set "$lan_if" up 2>/dev/null || true
  if ! ip -o -4 addr show dev "$lan_if" | grep -Fq " $LY_ROUTE_MANAGEMENT_FALLBACK_CIDR "; then
    ip addr add "$LY_ROUTE_MANAGEMENT_FALLBACK_CIDR" dev "$lan_if" 2>/dev/null || true
  fi
  networkctl reload 2>/dev/null || true
  networkctl reconfigure "$lan_if" 2>/dev/null || true
  for attempt in $(seq 1 20); do
    if ip -o -4 addr show dev "$lan_if" | grep -Fq " $LY_ROUTE_MANAGEMENT_FALLBACK_CIDR "; then
      break
    fi
    ip addr add "$LY_ROUTE_MANAGEMENT_FALLBACK_CIDR" dev "$lan_if" 2>/dev/null || true
    sleep 1
  done
  # This applies only to the installer-selected VMXNET3 acceptance fallback.
  # Physical NICs remain on the native-first/DPDK path and are not modified.
  if [ "${LY_ROUTE_VMXNET3_AF_PACKET_ACCEPTANCE:-false}" = true ] &&
     [ "$(basename "$(readlink -f "/sys/class/net/$lan_if/device/driver" 2>/dev/null || true)")" = vmxnet3 ]; then
    ip link set dev "$lan_if" promisc on 2>/dev/null || true
  fi
  if [ -f /etc/kea/kea-dhcp4.conf ]; then
    # Render the DHCP subnet from the installed management CIDR. A plain
    # string replacement is unsafe here: replacing 192.168.88.1 inside the
    # default pool also turns 192.168.88.100 into 10.1.18.12500. Keep the
    # management address out of the dynamic pool as well.
    python3 - "$lan_if" "$management_ip" "$LY_ROUTE_MANAGEMENT_FALLBACK_CIDR" /etc/kea/kea-dhcp4.conf <<'PY'
import ipaddress
import json
import sys

interface, router_text, cidr, target = sys.argv[1:]
router = ipaddress.ip_address(router_text)
network = ipaddress.ip_interface(cidr).network
network_int = int(network.network_address)
first_host = network_int + 1
last_host = int(network.broadcast_address) - 1

# Keep the factory pool in .100-.199 for ordinary /24 LANs. For smaller
# networks use the first usable range, always excluding the router address.
host_count = max(0, last_host - first_host + 1)
if host_count >= 220:
    pool_first = network_int + 100
    pool_last = min(network_int + 199, last_host)
else:
    pool_first = first_host
    pool_last = min(network_int + 199, last_host)

pools = []
if pool_first <= pool_last:
    router_int = int(router)
    if pool_first <= router_int <= pool_last:
        if pool_first <= router_int - 1:
            pools.append({"pool": f"{ipaddress.ip_address(pool_first)} - {ipaddress.ip_address(router_int - 1)}"})
        if router_int + 1 <= pool_last:
            pools.append({"pool": f"{ipaddress.ip_address(router_int + 1)} - {ipaddress.ip_address(pool_last)}"})
    else:
        pools.append({"pool": f"{ipaddress.ip_address(pool_first)} - {ipaddress.ip_address(pool_last)}"})

document = {
    "Dhcp4": {
        "interfaces-config": {"interfaces": [interface]},
        "subnet4": [{
            "id": 1,
            "subnet": network.with_prefixlen,
            "pools": pools,
            "option-data": [
                {"name": "routers", "data": str(router)},
                {"name": "domain-name-servers", "data": str(router)},
            ],
        }],
    }
}
with open(target, "w", encoding="utf-8") as output:
    json.dump(document, output, indent=2)
    output.write("\n")
PY
  fi
  control_env=/etc/ly-route/control-api.env
  mkdir -p /etc/ly-route
  /usr/lib/ly-route/migrate-control-env.sh "$control_env" "$lan_if" "$LY_ROUTE_MANAGEMENT_FALLBACK_CIDR" "$LY_ROUTE_MANAGEMENT_FALLBACK_GATEWAY"
  if [ -n "${installed_management_interface:-}" ] || [ -n "${installed_data_interfaces:-}" ]; then
    runtime_env=/etc/ly-route/runtime.env
    runtime_env_tmp="$runtime_env.tmp.$$"
    grep -v \
      -e '^LY_ROUTE_VPP_DATA_INTERFACES=' \
      -e '^LY_ROUTE_MANAGEMENT_INTERFACE=' \
      -e '^LY_ROUTE_MANAGEMENT_SHARED=' \
      -e '^LY_ROUTE_VPP_PROOF_TTL_SECONDS=' \
      "$runtime_env" > "$runtime_env_tmp" || true
    printf 'LY_ROUTE_MANAGEMENT_INTERFACE=%s\n' "$lan_if" >> "$runtime_env_tmp"
    printf 'LY_ROUTE_MANAGEMENT_SHARED=false\n' >> "$runtime_env_tmp"
    printf 'LY_ROUTE_VPP_DATA_INTERFACES=%s\n' "$installed_data_interfaces" >> "$runtime_env_tmp"
    printf 'LY_ROUTE_VPP_PROOF_TTL_SECONDS=315360000\n' >> "$runtime_env_tmp"
    chmod 0644 "$runtime_env_tmp"
    mv -f "$runtime_env_tmp" "$runtime_env"
  fi
  systemctl reset-failed kea-dhcp4-server.service 2>/dev/null || true
  # Kea may wait for network-online.target, while firstboot is ordered before
  # that target. Do not hold the complete dataplane boot transaction here.
  systemctl restart --no-block kea-dhcp4-server.service 2>/dev/null || true

  # The installer records stable NIC identities before first boot. Render the
  # initial native attach operations once so VPP receives the selected data
  # interfaces even before the control API has produced a full plan.
  operations_file=/var/lib/ly-route/vpp/operations.json
  if [ -n "${installed_data_interfaces:-}" ] && {
    [ ! -s "$operations_file" ] || grep -Eq '"operations"[[:space:]]*:[[:space:]]*\[[[:space:]]*\][[:space:]]*}' "$operations_file";
  }; then
    mkdir -p "$(dirname "$operations_file")"
    python3 - "$installed_network" "$operations_file" <<'PY'
import json
import os
import sys

source_path, target_path = sys.argv[1:]
with open(source_path, encoding="utf-8") as source:
    document = json.load(source)

operations = []
for item in document.get("data_interfaces", []):
    name = str(item.get("name", "")).strip()
    selected = item.get("selected")
    if not name or not isinstance(selected, dict):
        continue
    hook = str(selected.get("hook", "")).strip()
    mode = str(selected.get("mode", "")).strip()
    vpp_name = "lyroute-" + name
    if hook == "af_packet" and mode == "linux_packet_socket":
        commands = [
            "?create host-interface name " + name,
            "?set interface name host-" + name + " " + vpp_name,
            "set interface state " + vpp_name + " up",
            "show hardware-interfaces " + vpp_name,
            "show interface " + vpp_name,
        ]
    elif hook == "af_xdp" and mode == "zero_copy":
        commands = [
            "?create interface af_xdp host-if " + name + " name " + vpp_name + " zero-copy",
            "set interface state " + vpp_name + " up",
            "show hardware-interfaces " + vpp_name,
            "show interface " + vpp_name,
        ]
    elif hook == "rdma" and mode == "rdma_dv":
        commands = [
            "?create interface rdma host-if " + name + " name " + vpp_name + " mode dv",
            "set interface state " + vpp_name + " up",
            "show hardware-interfaces " + vpp_name,
            "show interface " + vpp_name,
        ]
    else:
        continue
    operations.append({
        "Name": "vpp.dataplane.attach",
        "RequestID": "installer-firstboot",
        "Resource": name,
        "Payload": {
            "linux_interface": name,
            "vpp_interface": vpp_name,
            "hook": hook,
            "mode": mode,
            "tier": str(selected.get("tier", "vpp_native")),
            "acceptance_only": bool(selected.get("acceptance_only", False)),
            "high_performance": bool(selected.get("high_performance", True)),
        },
        "VPPCtlCommands": commands,
    })

with open(target_path, "w", encoding="utf-8") as target:
    json.dump({"request_id": "installer-firstboot", "operations": operations}, target, indent=2)
    target.write("\n")
os.chmod(target_path, 0o600)
PY
  fi
fi

cat > /etc/issue <<EOF
Ly Route appliance
Default LAN: selected management interface, $LY_ROUTE_MANAGEMENT_FALLBACK_CIDR with DHCP enabled
Admin UI: http://$management_ip/
Admin UI HTTPS: https://$management_ip/
Configure admin credentials in /etc/ly-route/control-api.env
EOF

date -u +%Y-%m-%dT%H:%M:%SZ > "$state_dir/firstboot-ran-at"
