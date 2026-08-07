#!/usr/bin/env sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
debs=${LY_ROUTE_VPP_DEV_DEBS_DIR:-$repo_root/runtime-downloads}
out=${LY_ROUTE_VPP_PPPOE_CLIENT_BUILD_DIR:-$repo_root/build/vpp-pppoe-client}
sysroot=${LY_ROUTE_VPP_PPPOE_CLIENT_SYSROOT:-$out/sysroot}

for package in vpp-dev libvppinfra-dev; do
  set -- "$debs/${package}_25.10-release_amd64.deb"
  [ -f "$1" ] || { echo "missing VPP development package: $1" >&2; exit 1; }
done
command -v cmake >/dev/null
command -v dpkg-deb >/dev/null

mkdir -p "$out" "$sysroot"
dpkg-deb -x "$debs/vpp-dev_25.10-release_amd64.deb" "$sysroot"
dpkg-deb -x "$debs/libvppinfra-dev_25.10-release_amd64.deb" "$sysroot"

cmake -S "$repo_root/runtime/vpp-pppoe-client" -B "$out/cmake" \
  -DCMAKE_BUILD_TYPE=RelWithDebInfo \
  -DVPP_DIR="$sysroot/usr/lib/x86_64-linux-gnu/cmake/vpp" \
  -DVPP_INCLUDE_DIR="$sysroot/usr/include" \
  -DVPP_APIGEN="$sysroot/usr/bin/vppapigen" \
  -DVPP_VAPI_C_GEN="$sysroot/usr/bin/vapi_c_gen.py" \
  -DVPP_VAPI_CPP_GEN="$sysroot/usr/bin/vapi_cpp_gen.py"
cmake --build "$out/cmake" --parallel

plugin=$(find "$out/cmake" -type f -name ly_route_pppoe_client_plugin.so -print -quit)
[ -n "$plugin" ] || { echo "PPPoE client plugin was not produced" >&2; exit 1; }
printf '%s\n' "$plugin"
