#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'EOF'
Usage: scripts/build-runtime-debs.sh [smartdns|dns-vpp-proxy|xray|vpp|vpp-fdio|vpp-smart-qos|vpp-security-guard|vpp-pppoe-client|vpp-orchestrator|vpp-apply|all]

Builds source-backed runtime .deb packages for commercial Ly Route rootfs builds.

Environment:
  LY_ROUTE_RUNTIME_DEBS_DIR       Output directory for generated .deb packages.
  LY_ROUTE_RUNTIME_SRC_DIR        Source/cache directory for external repositories.
  LY_ROUTE_SMARTDNS_REPO          SmartDNS git repository URL.
  LY_ROUTE_SMARTDNS_REF           SmartDNS git ref to build.
  LY_ROUTE_SMARTDNS_SRC           Existing SmartDNS source directory to reuse.
  LY_ROUTE_XRAY_REPO              Xray-core git repository URL.
  LY_ROUTE_XRAY_REF               Xray-core git ref to build.
  LY_ROUTE_XRAY_SRC               Existing Xray-core source directory to reuse.
  LY_ROUTE_SOURCE_OFFLINE=1       Reuse existing source directories without fetching.
  LY_ROUTE_RUNTIME_DEB_ARCH       Debian package architecture. Defaults to the host architecture.
  LY_ROUTE_XRAY_GOARCH            Go architecture for Xray builds. Defaults from package architecture.
  LY_ROUTE_VPP_SRC                Existing VPP source directory.
  LY_ROUTE_VPP_INSTALL_DEPS=1     Run VPP make install-dep before make pkg-deb.
  LY_ROUTE_VPP_MAKE_ARGS          Extra arguments passed to VPP make.
  LY_ROUTE_FDIO_VPP_VERSION       FD.io VPP release package version. Defaults to 25.10-release.
  LY_ROUTE_FDIO_VPP_DISTRO        FD.io Debian distro path. Defaults to bookworm.
  LY_ROUTE_FDIO_VPP_DISTRO_ID     packagecloud distro version id. Defaults to 215.
  LY_ROUTE_FDIO_CACHE_DIR         Directory containing predownloaded FD.io .deb packages.
  LY_ROUTE_VPP_DEV_DEBS_DIR       Existing matching-architecture VPP development packages.
EOF
}

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
out_dir=${LY_ROUTE_RUNTIME_DEBS_DIR:-$repo_root/runtime-debs}
src_dir=${LY_ROUTE_RUNTIME_SRC_DIR:-$repo_root/tmp/runtime-src}
target=${1:-all}

smartdns_repo=${LY_ROUTE_SMARTDNS_REPO:-https://github.com/pymumu/smartdns.git}
smartdns_ref=${LY_ROUTE_SMARTDNS_REF:-master}
smartdns_src=${LY_ROUTE_SMARTDNS_SRC:-$src_dir/smartdns}
xray_repo=${LY_ROUTE_XRAY_REPO:-https://github.com/XTLS/Xray-core.git}
xray_ref=${LY_ROUTE_XRAY_REF:-main}
xray_src=${LY_ROUTE_XRAY_SRC:-$src_dir/xray-core}
vpp_src=${LY_ROUTE_VPP_SRC:-$repo_root/vpp-master}
fdio_vpp_version=${LY_ROUTE_FDIO_VPP_VERSION:-25.10-release}
fdio_vpp_distro=${LY_ROUTE_FDIO_VPP_DISTRO:-bookworm}
fdio_vpp_distro_id=${LY_ROUTE_FDIO_VPP_DISTRO_ID:-215}
fdio_cache_dir=${LY_ROUTE_FDIO_CACHE_DIR:-}
source_offline=${LY_ROUTE_SOURCE_OFFLINE:-${LY_ROUTE_RUNTIME_SRC_OFFLINE:-0}}

detect_deb_arch() {
  if command -v dpkg >/dev/null 2>&1; then
    dpkg --print-architecture
    return 0
  fi
  case "$(uname -m)" in
    x86_64) printf 'amd64\n' ;;
    aarch64|arm64) printf 'arm64\n' ;;
    *) uname -m ;;
  esac
}

goarch_for_deb_arch() {
  case "$1" in
    amd64) printf 'amd64\n' ;;
    arm64) printf 'arm64\n' ;;
    armhf) printf 'arm\n' ;;
    *) printf '%s\n' "$1" ;;
  esac
}

deb_arch=${LY_ROUTE_RUNTIME_DEB_ARCH:-$(detect_deb_arch)}
xray_goarch=${LY_ROUTE_XRAY_GOARCH:-$(goarch_for_deb_arch "$deb_arch")}

require_command() {
  if ! command -v "$1" >/dev/null 2>&1; then
    echo "required command missing: $1" >&2
    exit 1
  fi
}

package_version() {
  local version
  if git -C "$1" describe --tags --always --dirty >/dev/null 2>&1; then
    version=$(git -C "$1" describe --tags --always --dirty | sed 's/^v//; s/[^0-9A-Za-z.+~:-]/~/g')
  else
    version=$(date -u +%Y%m%d%H%M%S)
  fi
  case "$version" in
    [0-9]*) printf '%s\n' "$version" ;;
    *) printf '0~%s\n' "$version" ;;
  esac
}

install_smartdns_defaults() {
  package_root=$1
  mkdir -p "$package_root/etc/smartdns/conf.d"
  cat > "$package_root/etc/smartdns/smartdns.conf" <<'EOF'
server-name ly-route-smartdns
bind 127.0.0.1:1053
bind-tcp 127.0.0.1:1053
server 1.1.1.1
server 8.8.8.8
conf-file /etc/smartdns/conf.d/ly-route-active.conf
EOF
  cat > "$package_root/etc/smartdns/conf.d/ly-route-active.conf" <<'EOF'
# Ly Route writes active DNS policy fragments in this directory.
EOF
}

build_dns_vpp_proxy() {
  require_command cc
  require_command dpkg-deb
  mkdir -p "$out_dir"
  version=$(date -u +%Y%m%d%H%M%S)
  work_dir=$(mktemp -d "${TMPDIR:-/tmp}/ly-route-dns-vpp-proxy.XXXXXX")
  package_root=$work_dir/root
  binary=$work_dir/ly-route-dns-vpp-proxy
  cc -O2 -Wall -Wextra -Werror -std=c11 -o "$binary" "$repo_root/packaging/runtime/dns-vpp-proxy.c" -ldl
  mkdir -p "$package_root/DEBIAN" "$package_root/usr/lib/ly-route" "$package_root/lib/systemd/system" "$package_root/etc/ly-route"
  cp "$binary" "$package_root/usr/lib/ly-route/ly-route-dns-vpp-proxy"
  ln -s ly-route-dns-vpp-proxy "$package_root/usr/lib/ly-route/ly-route-dns-vpp-proxy-v6"
  chmod 0755 "$package_root/usr/lib/ly-route/ly-route-dns-vpp-proxy"
  printf '%s\n' '# source-prefix match-kind domain smartdns-port' >"$package_root/etc/ly-route/dns-source-routes.conf"
  cat > "$package_root/DEBIAN/control" <<EOF
Package: ly-route-dns-vpp-proxy
Version: $version
Section: net
Priority: optional
Architecture: $deb_arch
Maintainer: Ly Route <root@ly-route.local>
Depends: vpp
Description: Ly Route VPP native DNS service adapter
EOF
  cat > "$package_root/lib/systemd/system/ly-route-dns-vpp-proxy.service" <<'EOF'
[Unit]
Description=LY-Route VPP native DNS service adapter
Requires=vpp.service smartdns.service
After=vpp.service smartdns.service
Before=ly-route-dns-vpp-session.service

[Service]
Type=simple
Environment=VCL_CONFIG=/etc/ly-route/vcl.conf
Environment=VCL_VPP_API_SOCKET=/run/vpp/api.sock
Environment=LD_PRELOAD=/usr/lib/ly-route/libvcl_ldpreload.so
Environment=LY_ROUTE_DNS_FAMILY=ipv4
Environment=LY_ROUTE_DNS_SOURCE_ROUTES=/etc/ly-route/dns-source-routes.conf
ExecStart=/usr/lib/ly-route/ly-route-dns-vpp-proxy
Restart=on-failure
RestartSec=2s

[Install]
WantedBy=multi-user.target
EOF
  cat > "$package_root/lib/systemd/system/ly-route-dns-vpp-proxy-v6.service" <<'EOF'
[Unit]
Description=LY-Route VPP native IPv6 DNS service adapter
Requires=vpp.service smartdns.service ly-route-dns-vpp-v6-namespace.service
After=vpp.service smartdns.service ly-route-dns-vpp-v6-namespace.service
Before=ly-route-dns-vpp-session.service

[Service]
Type=simple
Environment=VCL_CONFIG=/etc/ly-route/vcl-v6.conf
Environment=VCL_VPP_API_SOCKET=/run/vpp/api.sock
Environment=VCL_APP_NAMESPACE_ID=dns-v6
Environment=VCL_APP_NAMESPACE_SECRET=4242
Environment=LD_PRELOAD=/usr/lib/ly-route/libvcl_ldpreload.so
Environment=LY_ROUTE_DNS_FAMILY=ipv6
Environment=LY_ROUTE_DNS_SOURCE_ROUTES=/etc/ly-route/dns-source-routes.conf
ExecStart=/usr/lib/ly-route/ly-route-dns-vpp-proxy-v6
Restart=on-failure
RestartSec=2s

[Install]
WantedBy=multi-user.target
EOF
  dpkg-deb --build --root-owner-group "$package_root" "$out_dir/ly-route-dns-vpp-proxy_${version}_${deb_arch}.deb" >/dev/null
  rm -rf "$work_dir"
}

install_xray_defaults() {
  package_root=$1
  mkdir -p "$package_root/etc/xray"
  cat > "$package_root/etc/xray/config.json" <<'EOF'
{"log":{"loglevel":"warning"},"inbounds":[],"outbounds":[{"tag":"direct","protocol":"freedom"}]}
EOF
}

prepare_git_source() {
  destination=$1
  repository=$2
  ref=$3
  if [ -d "$destination/.git" ]; then
    if [ "$source_offline" = "1" ]; then
      if git -C "$destination" rev-parse --verify --quiet "$ref" >/dev/null; then
        git -C "$destination" checkout --force "$ref"
      elif git -C "$destination" rev-parse --verify --quiet "origin/$ref" >/dev/null; then
        git -C "$destination" checkout --force "origin/$ref"
      else
        echo "offline source ref not found locally for $destination: $ref; using current HEAD" >&2
      fi
      return 0
    fi
    git_retry -C "$destination" fetch --tags origin "$ref"
    git -C "$destination" checkout FETCH_HEAD
    return 0
  fi
  if [ "$source_offline" = "1" ]; then
    echo "offline source cache missing: $destination" >&2
    exit 1
  fi
  if [ -e "$destination" ]; then
    echo "source path exists but is not a git checkout: $destination" >&2
    exit 1
  fi
  mkdir -p "$(dirname -- "$destination")"
  git_retry clone --depth 1 --branch "$ref" "$repository" "$destination"
}

git_retry() {
  local attempt=1
  while true; do
    if git "$@"; then
      return 0
    fi
    if [ "$attempt" -ge 5 ]; then
      return 1
    fi
    sleep $((attempt * 5))
    attempt=$((attempt + 1))
  done
}

build_binary_deb() {
  package_name=$1
  version=$2
  binary_path=$3
  install_path=$4
  service_name=$5
  service_exec=$6
  description=$7
  work_dir=$(mktemp -d "${TMPDIR:-/tmp}/ly-route-${package_name}.XXXXXX")
  package_root=$work_dir/root
  mkdir -p "$package_root/DEBIAN" "$package_root$(dirname -- "$install_path")" "$package_root/lib/systemd/system"
  cp "$binary_path" "$package_root$install_path"
  chmod 0755 "$package_root$install_path"
  cat > "$package_root/DEBIAN/control" <<EOF
Package: $package_name
Version: $version
Section: net
Priority: optional
Architecture: $deb_arch
Maintainer: Ly Route <root@ly-route.local>
Description: $description
EOF
  cat > "$package_root/lib/systemd/system/$service_name" <<EOF
[Unit]
Description=$description
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=$service_exec
Restart=on-failure
RestartSec=2s

[Install]
WantedBy=multi-user.target
EOF
  case "$package_name" in
    smartdns) install_smartdns_defaults "$package_root" ;;
    xray) install_xray_defaults "$package_root" ;;
  esac
  rm -f "$out_dir/${package_name}_"*"_${deb_arch}.deb"
  dpkg-deb --build --root-owner-group "$package_root" "$out_dir/${package_name}_${version}_${deb_arch}.deb" >/dev/null
  rm -rf "$work_dir"
}

build_smartdns() {
  require_command git
  require_command mmdebstrap
  require_command chroot
  require_command dpkg-deb
  require_command tar
  mkdir -p "$out_dir"
  prepare_git_source "$smartdns_src" "$smartdns_repo" "$smartdns_ref"
  version=$(package_version "$smartdns_src")
  build_root=$(mktemp -d "${TMPDIR:-/tmp}/ly-route-smartdns-build.XXXXXX")
  trap 'rm -rf "$build_root"' RETURN
  build_tar=$build_root/smartdns-src.tar
  tar -C "$smartdns_src" --exclude .git -cf "$build_tar" .
  chroot_dir=$build_root/chroot
  mmdebstrap --architectures="$deb_arch" --variant=minbase --components=main --include=ca-certificates,build-essential,make,gcc,pkg-config,libssl-dev,zlib1g-dev,git bookworm "$chroot_dir" "${LY_ROUTE_MIRROR:-http://deb.debian.org/debian}"
  mkdir -p "$chroot_dir/src/smartdns"
  tar -C "$chroot_dir/src/smartdns" -xf "$build_tar"
  chroot "$chroot_dir" /bin/sh -lc 'cd /src/smartdns && make'
  binary=$chroot_dir/src/smartdns/src/smartdns
  if [ ! -x "$binary" ]; then
    binary=$chroot_dir/src/smartdns/src/smartdns/smartdns
  fi
  if [ ! -x "$binary" ]; then
    echo "SmartDNS build did not produce an executable binary under the bookworm build chroot" >&2
    exit 1
  fi
  build_binary_deb smartdns "$version" "$binary" /usr/sbin/smartdns smartdns.service '/usr/sbin/smartdns -f -x -p - -c /etc/smartdns/smartdns.conf' 'SmartDNS local DNS accelerator'
  rm -rf "$build_root"
  trap - RETURN
}

build_xray() {
  require_command git
  require_command go
  require_command dpkg-deb
  mkdir -p "$out_dir"
  prepare_git_source "$xray_src" "$xray_repo" "$xray_ref"
  version=$(package_version "$xray_src")
  binary_dir=$src_dir/bin
  mkdir -p "$binary_dir"
  (cd "$xray_src" && GOOS=linux GOARCH="$xray_goarch" CGO_ENABLED=0 go build -trimpath -ldflags '-s -w' -o "$binary_dir/xray" ./main)
  build_binary_deb xray "$version" "$binary_dir/xray" /usr/bin/xray xray.service '/usr/bin/xray run -config /etc/xray/config.json' 'Xray proxy runtime'
}

build_vpp_apply() {
  require_command dpkg-deb
  mkdir -p "$out_dir"
  version=$(date -u +%Y%m%d%H%M%S)
  work_dir=$(mktemp -d "${TMPDIR:-/tmp}/ly-route-vpp-apply.XXXXXX")
  package_root=$work_dir/root
  mkdir -p "$package_root/DEBIAN" "$package_root/usr/lib/ly-route"
  cat > "$package_root/DEBIAN/control" <<EOF
Package: ly-route-vpp-apply
Version: $version
Section: net
Priority: optional
Architecture: $deb_arch
Maintainer: Ly Route <root@ly-route.local>
Depends: python3
Description: Ly Route VPP operation apply adapter
EOF
  cat > "$package_root/usr/lib/ly-route/vpp-apply" <<'EOF'
#!/usr/bin/env python3
import json
import os
import re
import shutil
import shlex
import subprocess
import sys

def fail(message):
    print(message, file=sys.stderr)
    sys.exit(1)

def receipt_text(value):
    if not value:
        return ""
    return "captured output omitted"

def run_vppctl(vppctl, command):
    argv = shlex.split(command)
    if not argv:
        return subprocess.CompletedProcess([vppctl], 2, "", "empty VPP command")
    return subprocess.run([vppctl] + argv, text=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE)

def truthy(value):
    return str(value).lower() in {"1", "true", "yes", "on"}

def load_json(path):
    with open(path, "r", encoding="utf-8") as handle:
        return json.load(handle)

def operation_name(operation):
    return str(operation.get("Name") or operation.get("name") or "").strip()

def operation_resource(operation):
    return str(operation.get("Resource") or operation.get("resource") or "").strip()

def commands_from_operation(operation):
    for field in ("commands", "Commands", "vppctl_commands", "VPPCtlCommands"):
        commands = operation.get(field)
        if isinstance(commands, list) and all(isinstance(command, str) for command in commands):
            return [command.strip() for command in commands if command.strip()]
    return []

def commands_from_map(operation, command_map):
    operations = command_map.get("operations", {})
    if not isinstance(operations, dict):
        fail("vpp command map field 'operations' must be an object")
    name = operation_name(operation)
    resource = operation_resource(operation)
    keys = []
    if name and resource:
        keys.append(f"{name}:{resource}")
    if name:
        keys.append(name)
    for key in keys:
        commands = operations.get(key)
        if isinstance(commands, list) and all(isinstance(command, str) for command in commands):
            return [command.strip() for command in commands if command.strip()]
    return []

def expand_command(command):
    expanded = os.path.expandvars(command)
    missing = sorted(set(re.findall(r"\$\{?[A-Za-z_][A-Za-z0-9_]*\}?", expanded)))
    if missing:
        raise ValueError(f"missing environment variable for VPP command {command!r}: {', '.join(missing)}")
    return expanded

def write_receipt(path, receipt):
    os.makedirs(os.path.dirname(path), exist_ok=True)
    temporary = path + ".tmp"
    with open(temporary, "w", encoding="utf-8") as handle:
        json.dump(receipt, handle, indent=2, sort_keys=True)
        handle.write("\n")
    os.replace(temporary, path)

if len(sys.argv) not in (2, 3):
    fail("usage: vpp-apply [--dry-run] /path/to/operations.json")

dry_run = truthy(os.environ.get("LY_ROUTE_VPP_APPLY_DRY_RUN", "false"))
arguments = sys.argv[1:]
if arguments[0] == "--dry-run":
    dry_run = True
    arguments = arguments[1:]
if len(arguments) != 1:
    fail("usage: vpp-apply [--dry-run] /path/to/operations.json")

operations_path = arguments[0]
command_map_path = os.environ.get("LY_ROUTE_VPP_COMMAND_MAP", "/etc/ly-route/vpp-command-map.json")
receipt_path = os.environ.get("LY_ROUTE_VPP_RECEIPT", "/var/lib/ly-route/vpp-apply-receipt.json")
receipt = {"status": "ready", "dry_run": dry_run, "operations": []}

def fail_with_receipt(message):
    receipt["status"] = "failed"
    receipt["error"] = message
    write_receipt(receipt_path, receipt)
    fail(message)

vppctl = shutil.which("vppctl")
if vppctl is None and not dry_run:
    fail_with_receipt("vppctl is required for ly-route-vpp-apply")

try:
    document = load_json(operations_path)
except Exception as err:
    fail_with_receipt(f"failed to load VPP operations JSON: {err}")

operations = document.get("operations")
if not isinstance(operations, list):
    fail_with_receipt("operations.json must contain an operations array")

command_map = {"operations": {}}
if os.path.exists(command_map_path):
    try:
        command_map = load_json(command_map_path)
    except Exception as err:
        fail_with_receipt(f"failed to load VPP command map JSON: {err}")
    if not isinstance(command_map, dict):
        fail_with_receipt("vpp command map must be a JSON object")

if not operations:
    write_receipt(receipt_path, receipt)
    sys.exit(0)

if not dry_run:
    version_result = subprocess.run([vppctl, "show", "version"], text=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
    if version_result.returncode != 0:
        fail_with_receipt("vppctl show version failed")

for index, operation in enumerate(operations):
    if not isinstance(operation, dict):
        fail_with_receipt(f"operation {index} must be an object")
    name = operation_name(operation)
    resource = operation_resource(operation)
    if not name:
        fail_with_receipt(f"operation {index} is missing a name")
    commands = commands_from_operation(operation)
    if not commands:
        commands = commands_from_map(operation, command_map)
    if not commands:
        key = f"{name}:{resource}" if resource else name
        fail_with_receipt(f"missing VPP command mapping for operation {key}")
    entry = {"name": name, "resource": resource, "commands": commands, "results": []}
    for command in commands:
        ignore_failure = command.startswith("?")
        if ignore_failure:
            command = command[1:].strip()
        receipt_command = command
        try:
            command = expand_command(command)
        except ValueError as err:
            entry["results"].append({"command": receipt_command, "status": "failed", "stderr": receipt_text(str(err))})
            receipt["operations"].append(entry)
            fail_with_receipt(str(err))
        argv = shlex.split(command)
        if not argv:
            entry["results"].append({"command": receipt_command, "status": "failed", "stderr": "empty VPP command"})
            receipt["operations"].append(entry)
            fail_with_receipt(f"empty VPP command for operation {name}")
        if dry_run:
            entry["results"].append({"command": receipt_command, "status": "dry-run"})
            continue
        result = subprocess.run([vppctl] + argv, text=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
        stdout = result.stdout.strip()
        stderr = result.stderr.strip()
        if result.returncode != 0:
            if ignore_failure:
                entry["results"].append({"command": receipt_command, "status": "ignored-failure", "stdout": receipt_text(stdout), "stderr": receipt_text(stderr)})
                continue
            entry["results"].append({"command": receipt_command, "status": "failed", "stdout": receipt_text(stdout), "stderr": receipt_text(stderr)})
            receipt["operations"].append(entry)
            fail_with_receipt(f"vppctl command failed for operation {name}: {receipt_command}")
        entry["results"].append({"command": receipt_command, "status": "applied", "stdout": receipt_text(stdout), "stderr": receipt_text(stderr)})
    receipt["operations"].append(entry)

receipt["status"] = "applied"
write_receipt(receipt_path, receipt)
EOF
  chmod 0755 "$package_root/usr/lib/ly-route/vpp-apply"
  rm -f "$out_dir/ly-route-vpp-apply_"*"_${deb_arch}.deb"
  dpkg-deb --build --root-owner-group "$package_root" "$out_dir/ly-route-vpp-apply_${version}_${deb_arch}.deb" >/dev/null
  rm -rf "$work_dir"
}

build_vpp() {
  require_command make
  if [ ! -f "$vpp_src/Makefile" ]; then
    echo "VPP source tree not found: $vpp_src" >&2
    exit 1
  fi
  if [ "${LY_ROUTE_VPP_INSTALL_DEPS:-0}" = "1" ]; then
    make -C "$vpp_src" install-dep
  fi
  make -C "$vpp_src" ${LY_ROUTE_VPP_MAKE_ARGS:-} pkg-deb
  mkdir -p "$out_dir"
  found=0
  while IFS= read -r package; do
    cp "$package" "$out_dir/"
    found=1
  done < <(find "$vpp_src" -path '*/build-root/*.deb' -o -path '*/build-root/packages/*.deb' -o -path '*/build-root/install-vpp-native/*.deb')
  if [ "$found" -ne 1 ]; then
    echo "VPP pkg-deb completed but no .deb packages were found under $vpp_src/build-root" >&2
    exit 1
  fi
}

fdio_vpp_sha256() {
  case "$1:$fdio_vpp_version" in
    libvppinfra:25.10-release) printf '1f6e9b7a4018b02ab062104891453392a4cfb1f81708c68e9d953d61db9ff0a8\n' ;;
    vpp:25.10-release) printf 'c355ee5c9c80e012ce5f02e81014eaa136a984f2b11287d757682d98de26277a\n' ;;
    vpp-plugin-core:25.10-release) printf 'de71aaf0cc2cc952c76524a4ec294179d161911f6c6dcb127431cbb2527458ce\n' ;;
    vpp-plugin-dpdk:25.10-release) printf 'f22c6ae649c36ae59d4053cc40f6f6b81c4ea66a86c66cef3ac4c2407a77533c\n' ;;
    vpp-dev:25.10-release) printf '3e4c4106955ebd569d126e29f97dca621d1c8adaa370cd006ae48e8554cab94d\n' ;;
    libvppinfra-dev:25.10-release) printf '2e03781cb364411e677227a538cfb15770d0a79fa21a3057f266d0707f5dba0e\n' ;;
    *) printf '' ;;
  esac
}

fetch_fdio_package() {
  local package=$1 destination=$2 package_file url expected actual cached_package
  package_file="${package}_${fdio_vpp_version}_${deb_arch}.deb"
  url="https://packagecloud.io/fdio/release/packages/debian/${fdio_vpp_distro}/${package_file}/download.deb?distro_version_id=${fdio_vpp_distro_id}"
  cached_package=""
  if [ -n "$fdio_cache_dir" ] && [ -f "$fdio_cache_dir/$package_file" ]; then
    cached_package="$fdio_cache_dir/$package_file"
  elif [ -f "$destination/$package_file" ]; then
    cached_package="$destination/$package_file"
  fi
  mkdir -p "$destination"
  if [ -n "$cached_package" ]; then
    if [ "$cached_package" != "$destination/$package_file" ]; then
      cp "$cached_package" "$destination/$package_file"
    fi
  else
    curl -fL --retry 8 --retry-all-errors --retry-delay 5 --connect-timeout 20 --max-time 300 -C - -o "$destination/$package_file" "$url"
  fi
  expected=$(fdio_vpp_sha256 "$package")
  [ -n "$expected" ] || { echo "no locked checksum for $package_file" >&2; exit 1; }
  actual=$(sha256sum "$destination/$package_file" | awk '{print $1}')
  if [ "$actual" != "$expected" ]; then
    echo "checksum mismatch for $package_file: expected $expected, got $actual" >&2
    exit 1
  fi
  if [ "$(dpkg-deb -f "$destination/$package_file" Package)" != "$package" ]; then
    echo "downloaded package does not identify as $package: $package_file" >&2
    exit 1
  fi
}

require_fdio_amd64() {
  require_command curl
  require_command sha256sum
  require_command dpkg-deb
  if [ "$deb_arch" != "amd64" ]; then
    echo "FD.io VPP package fetch currently supports amd64 only, got: $deb_arch" >&2
    exit 1
  fi
}

build_vpp_fdio() {
  require_fdio_amd64
  mkdir -p "$out_dir"
  local package
  for package in libvppinfra vpp vpp-plugin-core vpp-plugin-dpdk; do
    fetch_fdio_package "$package" "$out_dir"
  done
}

build_vpp_smart_qos() {
  require_command cmake
  build_debs=${LY_ROUTE_VPP_DEV_DEBS_DIR:-}
  owned_build_debs=false
  if [ -z "$build_debs" ]; then
    require_fdio_amd64
    build_debs=$(mktemp -d "${TMPDIR:-/tmp}/ly-route-vpp-smart-qos-deps.XXXXXX")
    owned_build_debs=true
    fetch_fdio_package vpp-dev "$build_debs"
    fetch_fdio_package libvppinfra-dev "$build_debs"
  fi
  package_root=$(mktemp -d "${TMPDIR:-/tmp}/ly-route-vpp-smart-qos-package.XXXXXX")
  trap 'if [ "$owned_build_debs" = true ]; then rm -rf "$build_debs"; fi; rm -rf "$package_root"' RETURN
  plugin=$(LY_ROUTE_RUNTIME_DEB_ARCH="$deb_arch" LY_ROUTE_VPP_DEV_DEBS_DIR="$build_debs" "$repo_root/scripts/build-vpp-smart-qos-plugin.sh" | tail -1)
  [ -f "$plugin" ] || { echo "smart QoS plugin build did not produce a library" >&2; exit 1; }
  multiarch=$(dpkg-architecture -a"$deb_arch" -qDEB_HOST_MULTIARCH)
  plugin_dir="$package_root/usr/lib/$multiarch/vpp_plugins"
  mkdir -p "$package_root/DEBIAN" "$plugin_dir"
  cp "$plugin" "$plugin_dir/ly_route_smart_qos_plugin.so"
  chmod 0644 "$plugin_dir/ly_route_smart_qos_plugin.so"
  cat > "$package_root/DEBIAN/control" <<EOF
Package: ly-route-vpp-smart-qos
Version: 25.10.0+lyroute1
Section: net
Priority: optional
Architecture: $deb_arch
Maintainer: Ly Route <root@ly-route.local>
Depends: vpp
Description: Ly Route VPP FQ-CoDel smart QoS plugin
EOF
  mkdir -p "$out_dir"
  rm -f "$out_dir/ly-route-vpp-smart-qos_"*"_${deb_arch}.deb"
  dpkg-deb --build --root-owner-group "$package_root" "$out_dir/ly-route-vpp-smart-qos_25.10.0+lyroute1_${deb_arch}.deb" >/dev/null
}

build_vpp_security_guard() {
  require_command cmake
  build_debs=${LY_ROUTE_VPP_DEV_DEBS_DIR:-}
  owned_build_debs=false
  if [ -z "$build_debs" ]; then
    require_fdio_amd64
    build_debs=$(mktemp -d "${TMPDIR:-/tmp}/ly-route-vpp-security-guard-deps.XXXXXX")
    owned_build_debs=true
    fetch_fdio_package vpp-dev "$build_debs"
    fetch_fdio_package libvppinfra-dev "$build_debs"
  fi
  package_root=$(mktemp -d "${TMPDIR:-/tmp}/ly-route-vpp-security-guard-package.XXXXXX")
  trap 'if [ "$owned_build_debs" = true ]; then rm -rf "$build_debs"; fi; rm -rf "$package_root"' RETURN
  plugin=$(LY_ROUTE_RUNTIME_DEB_ARCH="$deb_arch" LY_ROUTE_VPP_DEV_DEBS_DIR="$build_debs" "$repo_root/scripts/build-vpp-security-guard-plugin.sh" | tail -1)
  [ -f "$plugin" ] || { echo "security guard plugin build did not produce a library" >&2; exit 1; }
  multiarch=$(dpkg-architecture -a"$deb_arch" -qDEB_HOST_MULTIARCH)
  plugin_dir="$package_root/usr/lib/$multiarch/vpp_plugins"
  mkdir -p "$package_root/DEBIAN" "$plugin_dir"
  cp "$plugin" "$plugin_dir/ly_route_security_guard_plugin.so"
  chmod 0644 "$plugin_dir/ly_route_security_guard_plugin.so"
  cat > "$package_root/DEBIAN/control" <<EOF
Package: ly-route-vpp-security-guard
Version: 25.10.0+lyroute1
Section: net
Priority: optional
Architecture: $deb_arch
Maintainer: Ly Route <root@ly-route.local>
Depends: vpp
Description: Ly Route VPP protocol-aware security rate guard plugin
EOF
  mkdir -p "$out_dir"
  rm -f "$out_dir/ly-route-vpp-security-guard_"*"_${deb_arch}.deb"
  dpkg-deb --build --root-owner-group "$package_root" "$out_dir/ly-route-vpp-security-guard_25.10.0+lyroute1_${deb_arch}.deb" >/dev/null
}

build_vpp_pppoe_client() {
  require_command cmake
  require_command dpkg-deb
  require_command go
  build_debs=${LY_ROUTE_VPP_DEV_DEBS_DIR:-}
  owned_build_debs=false
  if [ -z "$build_debs" ]; then
    require_fdio_amd64
    build_debs=$(mktemp -d "${TMPDIR:-/tmp}/ly-route-vpp-pppoe-deps.XXXXXX")
    owned_build_debs=true
    fetch_fdio_package vpp-dev "$build_debs"
    fetch_fdio_package libvppinfra-dev "$build_debs"
  fi
  package_root=$(mktemp -d "${TMPDIR:-/tmp}/ly-route-vpp-pppoe-package.XXXXXX")
  trap 'if [ "$owned_build_debs" = true ]; then rm -rf "$build_debs"; fi; rm -rf "$package_root"' RETURN
  plugin=$(LY_ROUTE_RUNTIME_DEB_ARCH="$deb_arch" LY_ROUTE_VPP_DEV_DEBS_DIR="$build_debs" "$repo_root/scripts/build-vpp-pppoe-client-plugin.sh" | tail -1)
  [ -f "$plugin" ] || { echo "PPPoE client plugin build did not produce a library" >&2; exit 1; }
  multiarch=$(dpkg-architecture -a"$deb_arch" -qDEB_HOST_MULTIARCH)
  plugin_dir="$package_root/usr/lib/$multiarch/vpp_plugins"
  mkdir -p "$package_root/DEBIAN" "$package_root/usr/lib/ly-route" "$plugin_dir"
  (cd "$repo_root/backend" && GOOS=linux GOARCH="$(goarch_for_deb_arch "$deb_arch")" CGO_ENABLED=0 \
    go build -trimpath -ldflags '-s -w' -o "$package_root/usr/lib/ly-route/ly-route-pppoe-client" ./cmd/ly-route-pppoe-client)
  cp "$plugin" "$plugin_dir/ly_route_pppoe_client_plugin.so"
  chmod 0755 "$package_root/usr/lib/ly-route/ly-route-pppoe-client"
  chmod 0644 "$plugin_dir/ly_route_pppoe_client_plugin.so"
  cat > "$package_root/DEBIAN/control" <<EOF
Package: ly-route-vpp-pppoe-client
Version: 25.10.0+lyroute1
Section: net
Priority: optional
Architecture: $deb_arch
Maintainer: Ly Route <root@ly-route.local>
Depends: vpp
Description: Ly Route native VPP PPPoE client and control-frame plugin
EOF
  mkdir -p "$out_dir"
  rm -f "$out_dir/ly-route-vpp-pppoe-client_"*"_${deb_arch}.deb"
  dpkg-deb --build --root-owner-group "$package_root" "$out_dir/ly-route-vpp-pppoe-client_25.10.0+lyroute1_${deb_arch}.deb" >/dev/null
}

build_vpp_orchestrator() {
  require_fdio_amd64
  require_command cmake
  build_debs=$(mktemp -d "${TMPDIR:-/tmp}/ly-route-vpp-orchestrator-deps.XXXXXX")
  package_root=$(mktemp -d "${TMPDIR:-/tmp}/ly-route-vpp-orchestrator-package.XXXXXX")
  trap 'rm -rf "$build_debs" "$package_root"' RETURN
  fetch_fdio_package vpp-dev "$build_debs"
  fetch_fdio_package libvppinfra-dev "$build_debs"
  plugin=$(LY_ROUTE_RUNTIME_DEB_ARCH="$deb_arch" LY_ROUTE_VPP_DEV_DEBS_DIR="$build_debs" "$repo_root/scripts/build-vpp-orchestrator-plugin.sh" | tail -1)
  [ -f "$plugin" ] || { echo "orchestrator plugin build did not produce a library" >&2; exit 1; }
  mkdir -p "$package_root/DEBIAN" "$package_root/usr/lib/x86_64-linux-gnu/vpp_plugins"
  cp "$plugin" "$package_root/usr/lib/x86_64-linux-gnu/vpp_plugins/ly_route_orchestrator_plugin.so"
  chmod 0644 "$package_root/usr/lib/x86_64-linux-gnu/vpp_plugins/ly_route_orchestrator_plugin.so"
  cat > "$package_root/DEBIAN/control" <<EOF
Package: ly-route-vpp-orchestrator
Version: 25.10.0+lyroute1
Section: net
Priority: optional
Architecture: $deb_arch
Maintainer: Ly Route <root@ly-route.local>
Depends: vpp (= $fdio_vpp_version)
Description: Ly Route VPP native transparent traffic orchestrator plugin
EOF
  mkdir -p "$out_dir"
  rm -f "$out_dir/ly-route-vpp-orchestrator_"*"_${deb_arch}.deb"
  dpkg-deb --build --root-owner-group "$package_root" "$out_dir/ly-route-vpp-orchestrator_25.10.0+lyroute1_${deb_arch}.deb" >/dev/null
}

case "$target" in
  smartdns) build_smartdns ;;
  dns-vpp-proxy) build_dns_vpp_proxy ;;
  xray) build_xray ;;
  vpp) build_vpp ;;
  vpp-fdio) build_vpp_fdio ;;
  vpp-smart-qos) build_vpp_smart_qos ;;
  vpp-security-guard) build_vpp_security_guard ;;
  vpp-pppoe-client) build_vpp_pppoe_client ;;
  vpp-orchestrator) build_vpp_orchestrator ;;
  vpp-apply) build_vpp_apply ;;
  all)
    build_smartdns
    build_dns_vpp_proxy
    build_xray
    build_vpp_apply
    build_vpp_fdio
    build_vpp_smart_qos
    build_vpp_security_guard
    build_vpp_pppoe_client
    build_vpp_orchestrator
    ;;
  -h|--help) usage ; exit 0 ;;
  *) echo "unknown target: $target" >&2; usage >&2; exit 2 ;;
esac

printf 'Runtime packages written to %s\n' "$out_dir"
