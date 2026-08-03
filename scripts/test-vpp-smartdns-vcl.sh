#!/bin/sh
set -eu

image=${LY_ROUTE_VPP_TEST_IMAGE:-ly-route/vpp-test:25.10}
smartdns_deb=${LY_ROUTE_SMARTDNS_DEB:-/root/ly-route/runtime-debs/smartdns_0~48.1_amd64.deb}
name="lyroute-smartdns-vcl-$$"

[ -r "$smartdns_deb" ] || { echo "SmartDNS package is missing: $smartdns_deb" >&2; exit 1; }
cleanup() { docker rm -f "$name" >/dev/null 2>&1 || true; }
trap cleanup EXIT INT TERM

docker run -d --rm --name "$name" --network none -v "$smartdns_deb:/tmp/smartdns.deb:ro" --entrypoint sh "$image" -c '
  set -eu
  dpkg-deb -x /tmp/smartdns.deb /opt/smartdns
  printf "unix { nodaemon cli-listen /run/vpp/cli.sock runtime-dir /run/vpp }\nsession { enable rt-backend rule-table }\n" >/tmp/vpp.conf
  vpp -c /tmp/vpp.conf >/tmp/vpp.log 2>&1 &
  echo $! >/tmp/vpp.pid
  for i in $(seq 1 30); do test -S /run/vpp/cli.sock && break; sleep 1; done
  printf "vcl { app-socket-api /run/vpp/app_ns_sockets/default app-scope-local app-scope-global app_original_dst }\n" >/tmp/vcl.conf
  cat >/tmp/smartdns.conf <<EOF
bind 127.0.0.1:53
server 127.0.0.1:1053
EOF
  VCL_CONFIG=/tmp/vcl.conf VCL_VPP_API_SOCKET=/run/vpp/api.sock \
    LD_PRELOAD=/usr/lib/x86_64-linux-gnu/libvcl_ldpreload.so.25.10 \
    /opt/smartdns/usr/sbin/smartdns -f -x -p - -c /tmp/smartdns.conf >/tmp/smartdns.log 2>&1 &
  echo $! >/tmp/smartdns.pid
  sleep 3
  kill -0 "$(cat /tmp/smartdns.pid)"
  ! grep -q "bind service .* failed" /tmp/smartdns.log
  app_indexes=$(vppctl -s /run/vpp/cli.sock show app | awk "\$2 ~ /smartdns.*ldp/ { print \$1 }")
  [ -n "$app_indexes" ] || { cat /tmp/smartdns.log /tmp/vpp.log >&2; exit 1; }
  tcp_index=$(printf "%s\n" "$app_indexes" | sed -n 1p)
  udp_index=$(printf "%s\n" "$app_indexes" | sed -n 2p)
  case "$tcp_index:$udp_index" in
    ""|*:|:*|*[!0-9:]* ) cat /tmp/smartdns.log >&2; exit 1 ;;
  esac
  vppctl -s /run/vpp/cli.sock "session rule add scope global proto tcp 0.0.0.0/0 53 0.0.0.0/0 0 action $tcp_index tag lyroute-smartdns-tcp4"
  vppctl -s /run/vpp/cli.sock "session rule add scope global proto udp 0.0.0.0/0 53 0.0.0.0/0 0 action $udp_index tag lyroute-smartdns-udp4"
  vppctl -s /run/vpp/cli.sock "session rule add scope global proto tcp ::/0 53 ::/0 0 action $tcp_index tag lyroute-smartdns-tcp6"
  vppctl -s /run/vpp/cli.sock "session rule add scope global proto udp ::/0 53 ::/0 0 action $udp_index tag lyroute-smartdns-udp6"
  vppctl -s /run/vpp/cli.sock "session rule del scope global proto tcp 0.0.0.0/0 53 0.0.0.0/0 0 tag lyroute-smartdns-tcp4"
  vppctl -s /run/vpp/cli.sock "session rule del scope global proto udp 0.0.0.0/0 53 0.0.0.0/0 0 tag lyroute-smartdns-udp4"
  vppctl -s /run/vpp/cli.sock "session rule del scope global proto tcp ::/0 53 ::/0 0 tag lyroute-smartdns-tcp6"
  vppctl -s /run/vpp/cli.sock "session rule del scope global proto udp ::/0 53 ::/0 0 tag lyroute-smartdns-udp6"
  kill "$(cat /tmp/smartdns.pid)" "$(cat /tmp/vpp.pid)" 2>/dev/null || true
'

printf '%s\n' 'VPP SmartDNS VCL registration and session-rule verification passed'
