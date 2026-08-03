#!/bin/sh
set -eu

mkdir -p /etc/ppp
cat >/etc/ppp/server-options <<'EOF'
noauth
mtu 1492
mru 1492
lcp-echo-interval 5
lcp-echo-failure 3
+ipv6
ipv6 ::1,::2
EOF
cat >/etc/ppp/pap-secrets <<'EOF'
e2e-user * e2e-password
EOF
cp /etc/ppp/pap-secrets /etc/ppp/chap-secrets
ip link set eth0 up

(
  ppp_if=''
  until ppp_if=$(ip -o link show 2>/dev/null | awk -F': ' '$2 ~ /^ppp[0-9]+$/ {print $2; exit}'); [ -n "$ppp_if" ]; do sleep 0.2; done
  until ip link show "$ppp_if" 2>/dev/null | grep -q 'UP'; do sleep 0.2; done
  until ip -6 addr replace 2001:db8:ffff::1/64 dev "$ppp_if" 2>/dev/null; do sleep 0.2; done
  until ip -6 route replace 2001:db8:100::/56 dev "$ppp_if" 2>/dev/null; do sleep 0.2; done
  exec dhcp6-pd-fixture
) &
exec pppoe-server -F -I eth0 -L 10.67.0.1 -R 10.67.0.10 -N 4 -C LY-ROUTE-E2E -O /etc/ppp/server-options
