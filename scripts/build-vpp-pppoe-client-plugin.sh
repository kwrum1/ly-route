#!/usr/bin/env sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
debs=${LY_ROUTE_VPP_DEV_DEBS_DIR:-$repo_root/runtime-downloads}
out=${LY_ROUTE_VPP_PPPOE_CLIENT_BUILD_DIR:-$repo_root/build/vpp-pppoe-client}
sysroot=${LY_ROUTE_VPP_PPPOE_CLIENT_SYSROOT:-$out/sysroot}

required_version=${LY_ROUTE_VPP_VERSION:-}
if [ -n "$required_version" ]; then
  vpp_dev=$(find "$debs" -maxdepth 1 -type f -name "vpp-dev_${required_version}_*.deb" -print -quit)
  infra_dev=$(find "$debs" -maxdepth 1 -type f -name "libvppinfra-dev_${required_version}_*.deb" -print -quit)
else
  vpp_dev=$(find "$debs" -maxdepth 1 -type f -name 'vpp-dev_*.deb' -print -quit)
  infra_dev=$(find "$debs" -maxdepth 1 -type f -name 'libvppinfra-dev_*.deb' -print -quit)
fi
[ -f "$vpp_dev" ] || { echo "missing VPP development package in $debs: vpp-dev" >&2; exit 1; }
[ -f "$infra_dev" ] || { echo "missing VPP development package in $debs: libvppinfra-dev" >&2; exit 1; }
vpp_version=$(dpkg-deb -f "$vpp_dev" Version)
infra_version=$(dpkg-deb -f "$infra_dev" Version)
[ "$vpp_version" = "$infra_version" ] || {
  echo "VPP development package version mismatch: vpp-dev=$vpp_version libvppinfra-dev=$infra_version" >&2
  exit 1
}
command -v cmake >/dev/null
command -v dpkg-deb >/dev/null

mkdir -p "$out" "$sysroot"
dpkg-deb -x "$vpp_dev" "$sysroot"
dpkg-deb -x "$infra_dev" "$sysroot"

vpp_cmake_dir=$(find "$sysroot/usr/lib" -type d -path '*/cmake/vpp' -print -quit)
[ -d "$vpp_cmake_dir" ] || { echo "VPP CMake package directory not found in $vpp_dev" >&2; exit 1; }

cmake -S "$repo_root/runtime/vpp-pppoe-client" -B "$out/cmake" \
  -DCMAKE_BUILD_TYPE=RelWithDebInfo \
  -DVPP_DIR="$vpp_cmake_dir" \
  -DVPP_INCLUDE_DIR="$sysroot/usr/include" \
  -DVPP_APIGEN="$sysroot/usr/bin/vppapigen" \
  -DVPP_VAPI_C_GEN="$sysroot/usr/bin/vapi_c_gen.py" \
  -DVPP_VAPI_CPP_GEN="$sysroot/usr/bin/vapi_cpp_gen.py"
cmake --build "$out/cmake" --parallel

plugin=$(find "$out/cmake" -type f -name ly_route_pppoe_client_plugin.so -print -quit)
[ -n "$plugin" ] || { echo "PPPoE client plugin was not produced" >&2; exit 1; }
printf '%s\n' "$plugin"
