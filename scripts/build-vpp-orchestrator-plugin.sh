#!/usr/bin/env sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
debs=${LY_ROUTE_VPP_DEV_DEBS_DIR:-$repo_root/runtime-downloads}
out=${LY_ROUTE_VPP_ORCHESTRATOR_BUILD_DIR:-$repo_root/build/vpp-orchestrator}
sysroot=${LY_ROUTE_VPP_ORCHESTRATOR_SYSROOT:-$out/sysroot}
arch=${LY_ROUTE_RUNTIME_DEB_ARCH:-amd64}

[ "$arch" = amd64 ] || { echo "VPP orchestrator plugin build currently requires amd64 FD.io development packages" >&2; exit 1; }
for package in vpp-dev libvppinfra-dev; do
  file="$debs/${package}_25.10-release_${arch}.deb"
  [ -f "$file" ] || { echo "missing VPP development package: $file" >&2; exit 1; }
done
command -v cmake >/dev/null
command -v dpkg-deb >/dev/null

mkdir -p "$out" "$sysroot"
dpkg-deb -x "$debs/vpp-dev_25.10-release_${arch}.deb" "$sysroot"
dpkg-deb -x "$debs/libvppinfra-dev_25.10-release_${arch}.deb" "$sysroot"

cmake -S "$repo_root/runtime/vpp-orchestrator" -B "$out/cmake" \
  -DCMAKE_BUILD_TYPE=RelWithDebInfo \
  -DVPP_DIR="$sysroot/usr/lib/x86_64-linux-gnu/cmake/vpp" \
  -DVPP_INCLUDE_DIR="$sysroot/usr/include" \
  -DVPP_APIGEN="$sysroot/usr/bin/vppapigen" \
  -DVPP_VAPI_C_GEN="$sysroot/usr/bin/vapi_c_gen.py" \
  -DVPP_VAPI_CPP_GEN="$sysroot/usr/bin/vapi_cpp_gen.py"
cmake --build "$out/cmake" --parallel

plugin=$(find "$out/cmake" -type f -name ly_route_orchestrator_plugin.so -print -quit)
[ -n "$plugin" ] || { echo "orchestrator plugin was not produced" >&2; exit 1; }
printf '%s\n' "$plugin"
