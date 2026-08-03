#!/usr/bin/env sh
set -eu
repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
debs=${LY_ROUTE_VPP_DEV_DEBS_DIR:-$repo_root/runtime-downloads}
out=${LY_ROUTE_VPP_SECURITY_GUARD_BUILD_DIR:-$repo_root/build/vpp-security-guard}
sysroot=${LY_ROUTE_VPP_SECURITY_GUARD_SYSROOT:-$out/sysroot}
arch=${LY_ROUTE_RUNTIME_DEB_ARCH:-$(dpkg --print-architecture)}
multiarch=$(dpkg-architecture -a"$arch" -qDEB_HOST_MULTIARCH)
for package in vpp-dev libvppinfra-dev; do
  set -- "$debs/${package}_"*"_${arch}.deb"
  [ -f "$1" ] || { echo "missing VPP development package for $arch: $package" >&2; exit 1; }
done
mkdir -p "$out" "$sysroot"
dpkg-deb -x "$debs"/vpp-dev_*_"$arch".deb "$sysroot"
dpkg-deb -x "$debs"/libvppinfra-dev_*_"$arch".deb "$sysroot"
cmake -S "$repo_root/runtime/vpp-security-guard" -B "$out/cmake" \
  -DCMAKE_BUILD_TYPE=RelWithDebInfo \
  -DVPP_DIR="$sysroot/usr/lib/$multiarch/cmake/vpp" \
  -DVPP_INCLUDE_DIR="$sysroot/usr/include" \
  -DVPP_APIGEN="$sysroot/usr/bin/vppapigen" \
  -DVPP_VAPI_C_GEN="$sysroot/usr/bin/vapi_c_gen.py" \
  -DVPP_VAPI_CPP_GEN="$sysroot/usr/bin/vapi_cpp_gen.py"
cmake --build "$out/cmake" --parallel
plugin=$(find "$out/cmake" -type f -name ly_route_security_guard_plugin.so -print -quit)
[ -n "$plugin" ] || { echo "security guard plugin was not produced" >&2; exit 1; }
printf '%s\n' "$plugin"
