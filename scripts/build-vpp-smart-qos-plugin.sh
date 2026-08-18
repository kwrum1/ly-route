#!/usr/bin/env sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
debs=${LY_ROUTE_VPP_DEV_DEBS_DIR:-$repo_root/runtime-downloads}
out=${LY_ROUTE_VPP_SMART_QOS_BUILD_DIR:-$repo_root/build/vpp-smart-qos}
sysroot=${LY_ROUTE_VPP_SMART_QOS_SYSROOT:-$out/sysroot}

vpp_dev=$(find "$debs" -maxdepth 1 -type f -name 'vpp-dev_*.deb' -print -quit)
infra_dev=$(find "$debs" -maxdepth 1 -type f -name 'libvppinfra-dev_*.deb' -print -quit)
[ -f "$vpp_dev" ] || { echo "missing VPP development package in $debs: vpp-dev" >&2; exit 1; }
[ -f "$infra_dev" ] || { echo "missing VPP development package in $debs: libvppinfra-dev" >&2; exit 1; }
command -v cmake >/dev/null
command -v dpkg-deb >/dev/null

mkdir -p "$out" "$sysroot"
dpkg-deb -x "$vpp_dev" "$sysroot"
dpkg-deb -x "$infra_dev" "$sysroot"

cmake -S "$repo_root/runtime/vpp-smart-qos" -B "$out/cmake" \
  -DCMAKE_BUILD_TYPE=RelWithDebInfo \
  -DVPP_DIR="$sysroot/usr/lib/x86_64-linux-gnu/cmake/vpp" \
  -DVPP_INCLUDE_DIR="$sysroot/usr/include" \
  -DVPP_APIGEN="$sysroot/usr/bin/vppapigen" \
  -DVPP_VAPI_C_GEN="$sysroot/usr/bin/vapi_c_gen.py" \
  -DVPP_VAPI_CPP_GEN="$sysroot/usr/bin/vapi_cpp_gen.py"
cmake --build "$out/cmake" --parallel

plugin=$(find "$out/cmake" -type f -name ly_route_smart_qos_plugin.so -print -quit)
[ -n "$plugin" ] || { echo "smart QoS plugin was not produced" >&2; exit 1; }
printf '%s\n' "$plugin"
