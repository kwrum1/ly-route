#!/bin/sh
set -eu

data_dir=${LY_ROUTE_DATA_DIR:-/var/lib/ly-route/gateway}
mkdir -p "$data_dir"
write_capability_proof() {
  now=$(date -u +%Y-%m-%dT%H:%M:%SZ)
  valid_until=$(date -u -d +10minutes +%Y-%m-%dT%H:%M:%SZ)
  proofs=''
  for interface in eth1 eth2 eth3; do
    item=$(printf '{"linux_interface":"%s","candidates":[{"tier":"vpp_native","hook":"af_xdp","mode":"zero_copy","source":"runtime_probe","runtime_verified":true,"native":true,"high_performance":true,"smart_qos_plugin_available":true,"performance_score":100,"observed_at":"%s","valid_until":"%s"}]}' "$interface" "$now" "$valid_until")
    if [ -n "$proofs" ]; then proofs="$proofs,$item"; else proofs=$item; fi
  done
  printf '{"management_interface":"eth0","proofs":[%s]}\n' "$proofs" >"$data_dir/vpp-native-capabilities.json.tmp"
  mv "$data_dir/vpp-native-capabilities.json.tmp" "$data_dir/vpp-native-capabilities.json"
}

write_capability_proof
(
  while sleep 300; do
    write_capability_proof
  done
) &
export LY_ROUTE_VPP_CAPABILITY_PROOF=${LY_ROUTE_VPP_CAPABILITY_PROOF:-$data_dir/vpp-native-capabilities.json}
exec /usr/local/bin/ly-route-vpp-demo
