#!/bin/sh
set -eu

: "${LY_ROUTE_MANAGEMENT_FALLBACK:=yes}"
: "${LY_ROUTE_MANAGEMENT_FALLBACK_CIDR:=192.168.88.1/24}"
: "${LY_ROUTE_MANAGEMENT_FALLBACK_ROUTE_METRIC:=4096}"

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
    -addext 'subjectAltName=DNS:ly-route.local,IP:192.168.88.1,IP:127.0.0.1' \
    -keyout "$tls_dir/admin.key" \
    -out "$tls_dir/admin.crt"
  chmod 600 "$tls_dir/admin.key"
  chmod 644 "$tls_dir/admin.crt"
fi

lan_if=$(ip -o link show | awk -F': ' '$2 != "lo" {print $2; exit}' | cut -d'@' -f1)

if [ -n "${lan_if:-}" ] && [ "$LY_ROUTE_MANAGEMENT_FALLBACK" = "yes" ]; then
  mkdir -p /run/systemd/network
  cat > /run/systemd/network/05-ly-route-lan.network <<EOF
[Match]
Name=$lan_if

[Network]
Address=$LY_ROUTE_MANAGEMENT_FALLBACK_CIDR
DHCP=no
LinkLocalAddressing=ipv4
IPv6AcceptRA=yes
EOF
  ip link set "$lan_if" up 2>/dev/null || true
  if ! ip -4 addr show dev "$lan_if" | grep -q '192\.168\.88\.1/24'; then
    ip addr add "$LY_ROUTE_MANAGEMENT_FALLBACK_CIDR" dev "$lan_if" 2>/dev/null || true
  fi
  networkctl reload 2>/dev/null || true
  networkctl reconfigure "$lan_if" 2>/dev/null || true
  if [ -f /etc/kea/kea-dhcp4.conf ]; then
    sed -i "s/\"interfaces\": \[\"[^\"]*\"\]/\"interfaces\": [\"$lan_if\"]/" /etc/kea/kea-dhcp4.conf
  fi
  control_env=/etc/ly-route/control-api.env
  mkdir -p /etc/ly-route
  /usr/lib/ly-route/migrate-control-env.sh "$control_env" "$lan_if" "$LY_ROUTE_MANAGEMENT_FALLBACK_CIDR"
  systemctl reset-failed kea-dhcp4-server.service 2>/dev/null || true
fi

cat > /etc/issue <<EOF
Ly Route appliance
Default LAN: first Ethernet interface, 192.168.88.1/24 with DHCP enabled
Admin UI: http://192.168.88.1/
Admin UI HTTPS: https://192.168.88.1/
Configure admin credentials in /etc/ly-route/control-api.env
EOF

date -u +%Y-%m-%dT%H:%M:%SZ > "$state_dir/firstboot-ran-at"
