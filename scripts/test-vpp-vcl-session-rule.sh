#!/bin/sh
set -eu

image=${LY_ROUTE_VPP_TEST_IMAGE:-ly-route/vpp-test:25.10}
name="lyroute-vcl-session-$$"

cleanup() {
  docker rm -f "$name" >/dev/null 2>&1 || true
}
trap cleanup EXIT INT TERM

docker run -d --rm --name "$name" --network none --entrypoint sh "$image" -c '
  printf "unix { nodaemon cli-listen /run/vpp/cli.sock runtime-dir /run/vpp }\nsession { enable rt-backend rule-table use-app-socket-api }\n" >/tmp/vpp.conf
  exec vpp -c /tmp/vpp.conf
' >/dev/null

ready=0
for _ in $(seq 1 30); do
  if docker exec "$name" test -S /run/vpp/cli.sock; then
    ready=1
    break
  fi
  sleep 1
done
[ "$ready" = 1 ] || { echo "VPP CLI socket did not become ready" >&2; exit 1; }

docker exec "$name" sh -c '
  printf "vcl { app_original_dst use-mq-eventfd }\n" >/tmp/vcl.conf
  start_listener() {
    label=$1
    shift
    VCL_CONFIG=/tmp/vcl.conf VCL_VPP_API_SOCKET=/run/vpp/api.sock \
      LD_PRELOAD=/usr/lib/x86_64-linux-gnu/libvcl_ldpreload.so.25.10 \
      timeout 10 vcl_test_server "$@" 53 >"/tmp/vcl-test-server-$label.log" 2>&1 &
    pid=$!
    echo "$pid" >"/tmp/vcl-test-server-$label.pid"
    sleep 1
    index=$(vppctl -s /run/vpp/cli.sock show app | awk -v pid="$pid" "\$2 ~ (\"ldp-\" pid \"-\") { print \$1; exit }")
    case "$index" in
      ""|*[!0-9]*) cat "/tmp/vcl-test-server-$label.log" >&2; exit 1 ;;
    esac
    printf "%s" "$index"
  }
  tcp4_index=$(start_listener tcp4 -p tcp)
  udp4_index=$(start_listener udp4 -D)
  tcp6_index=$(start_listener tcp6 -6 -p tcp)
  udp6_index=$(start_listener udp6 -6 -D)
  vppctl -s /run/vpp/cli.sock "session rule add scope global proto tcp 0.0.0.0/0 53 0.0.0.0/0 0 action $tcp4_index tag lyroute-dns-tcp4"
  vppctl -s /run/vpp/cli.sock "session rule add scope global proto udp 0.0.0.0/0 53 0.0.0.0/0 0 action $udp4_index tag lyroute-dns-udp4"
  vppctl -s /run/vpp/cli.sock "session rule add scope global proto tcp ::/0 53 ::/0 0 action $tcp6_index tag lyroute-dns-tcp6"
  vppctl -s /run/vpp/cli.sock "session rule add scope global proto udp ::/0 53 ::/0 0 action $udp6_index tag lyroute-dns-udp6"
  vppctl -s /run/vpp/cli.sock "session rule del scope global proto tcp 0.0.0.0/0 53 0.0.0.0/0 0 tag lyroute-dns-tcp4"
  vppctl -s /run/vpp/cli.sock "session rule del scope global proto udp 0.0.0.0/0 53 0.0.0.0/0 0 tag lyroute-dns-udp4"
  vppctl -s /run/vpp/cli.sock "session rule del scope global proto tcp ::/0 53 ::/0 0 tag lyroute-dns-tcp6"
  vppctl -s /run/vpp/cli.sock "session rule del scope global proto udp ::/0 53 ::/0 0 tag lyroute-dns-udp6"
  kill $(cat /tmp/vcl-test-server-*.pid) 2>/dev/null || true
'

printf '%s\n' 'VPP VCL session-rule lifecycle verification passed'
