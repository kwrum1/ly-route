#!/bin/sh
set -eu

control_binary=${LY_ROUTE_CONTROL_BINARY:?LY_ROUTE_CONTROL_BINARY is required}
vpp_socket=${LY_ROUTE_VPP_SOCKET:-/run/vpp/cli.sock}
data_dir=${LY_ROUTE_DATA_DIR:-/var/lib/ly-route}
mkdir -p /run/vpp "$data_dir" "${LY_ROUTE_DATAPLANE_STATE_DIR:-$data_dir/dataplane}"

cat >/tmp/ly-route-vpp.conf <<EOF
unix {
  nodaemon
  runtime-dir /run/vpp
  cli-listen $vpp_socket
}
api-segment {
  prefix ${LY_ROUTE_VPP_API_PREFIX:-lyroute-demo}
}
EOF

/usr/bin/vpp -c /tmp/ly-route-vpp.conf >/tmp/ly-route-vpp.log 2>&1 &
vpp_pid=$!

cleanup() {
  [ -z "${control_pid:-}" ] || kill "$control_pid" 2>/dev/null || true
  kill "$vpp_pid" 2>/dev/null || true
}
trap cleanup EXIT INT TERM

attempt=0
until /usr/bin/vppctl -s "$vpp_socket" show version >/dev/null 2>&1; do
  attempt=$((attempt + 1))
  if [ "$attempt" -ge 80 ]; then
    cat /tmp/ly-route-vpp.log >&2
    exit 1
  fi
  sleep 0.1
done

for path in /sys/class/net/eth*; do
  [ -e "$path" ] || continue
  interface=${path##*/}
  [ "$interface" != "${LY_ROUTE_MANAGEMENT_INTERFACE:-eth0}" ] || continue
  vpp_interface="lyroute-$interface"
  /usr/bin/vppctl -s "$vpp_socket" create interface af_xdp host-if "$interface" name "$vpp_interface" >/dev/null
  /usr/bin/vppctl -s "$vpp_socket" set interface state "$vpp_interface" up
done

cat >/usr/local/bin/ly-route-vppctl <<EOF
#!/bin/sh
exec /usr/bin/vppctl -s $vpp_socket "\$@"
EOF
chmod 0755 /usr/local/bin/ly-route-vppctl

"$control_binary" &
control_pid=$!
wait "$control_pid"
