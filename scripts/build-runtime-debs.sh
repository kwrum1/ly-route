#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'EOF'
Usage: scripts/build-runtime-debs.sh [smartdns|dns-vpp-proxy|xray|vpp|vpp-fdio|vpp-pppoe-client|vpp-smart-qos|vpp-security-guard|vpp-dns-intercept|vpp-pre-nat-route|vpp-orchestrator|vpp-apply|all]

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
  LY_ROUTE_VPP_SRC                Explicit VPP source directory (required for vpp).
  LY_ROUTE_VPP_INSTALL_DEPS=1     Run VPP make install-dep before make pkg-deb.
  LY_ROUTE_VPP_MAKE_ARGS          Extra arguments passed to VPP make.
  LY_ROUTE_FDIO_VPP_VERSION       FD.io VPP release package version. Defaults to 25.10-release.
  LY_ROUTE_FDIO_VPP_DISTRO        FD.io Debian distro path. Defaults to bookworm.
  LY_ROUTE_FDIO_VPP_DISTRO_ID     packagecloud distro version id. Defaults to 215.
  LY_ROUTE_FDIO_CACHE_DIR         Directory containing predownloaded FD.io .deb packages.
EOF
}

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
out_dir=${LY_ROUTE_RUNTIME_DEBS_DIR:-$repo_root/runtime-debs}
src_dir=${LY_ROUTE_RUNTIME_SRC_DIR:-$repo_root/tmp/runtime-src}
target=${1:-all}

mkdir -p "$out_dir"
source_fingerprint=$("$repo_root/scripts/source-fingerprint.sh" backend frontend packaging runtime scripts)
runtime_stamp="$out_dir/.ly-route-source-fingerprint"
runtime_in_progress="$out_dir/.ly-route-build-in-progress"
if [ ! -f "$runtime_stamp" ] || [ "$(cat "$runtime_stamp")" != "$source_fingerprint" ] || [ -e "$runtime_in_progress" ]; then
  # The directory may also contain pinned FD.io VPP base packages. Only remove
  # Ly Route-owned packages here; rootfs validation still requires every
  # package set to carry the current source stamp.
  for package_glob in \
    smartdns_*.deb \
    xray_*.deb \
    ly-route-dns-vpp-proxy_*.deb \
    ly-route-vpp-apply_*.deb \
    ly-route-vpp-pppoe-client_*.deb \
    ly-route-vpp-smart-qos_*.deb \
    ly-route-vpp-security-guard_*.deb \
    ly-route-vpp-dns-intercept_*.deb \
    ly-route-vpp-pre-nat-route_*.deb \
    ly-route-vpp-orchestrator_*.deb; do
    rm -f "$out_dir/$package_glob"
  done
  rm -f "$runtime_stamp" "$runtime_in_progress"
  printf '%s\n' "$source_fingerprint" >"$runtime_stamp"
fi
touch "$runtime_in_progress"

smartdns_repo=${LY_ROUTE_SMARTDNS_REPO:-https://github.com/pymumu/smartdns.git}
smartdns_ref=${LY_ROUTE_SMARTDNS_REF:-master}
smartdns_src=${LY_ROUTE_SMARTDNS_SRC:-$src_dir/smartdns}
xray_repo=${LY_ROUTE_XRAY_REPO:-https://github.com/XTLS/Xray-core.git}
xray_ref=${LY_ROUTE_XRAY_REF:-main}
xray_src=${LY_ROUTE_XRAY_SRC:-$src_dir/xray-core}
vpp_src=${LY_ROUTE_VPP_SRC:-}
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

vpp_plugin_multiarch() {
  case "$deb_arch" in
    amd64) printf 'x86_64-linux-gnu\n' ;;
    arm64) printf 'aarch64-linux-gnu\n' ;;
    armhf) printf 'arm-linux-gnueabihf\n' ;;
    *) echo "unsupported VPP plugin architecture: $deb_arch" >&2; exit 1 ;;
  esac
}

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
bind 127.0.0.1:1053 -no-speed-check
bind-tcp 127.0.0.1:1053 -no-speed-check
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
  mkdir -p "$package_root/DEBIAN" "$package_root/usr/lib/ly-route" "$package_root/lib/systemd/system" "$package_root/etc/ly-route" \
    "$package_root/etc/systemd/system/smartdns.service.d" "$package_root/etc/systemd/system/ly-route-policy-routing.service.d"
  cp "$binary" "$package_root/usr/lib/ly-route/ly-route-dns-vpp-proxy"
  ln -s ly-route-dns-vpp-proxy "$package_root/usr/lib/ly-route/ly-route-dns-vpp-proxy-v6"
  chmod 0755 "$package_root/usr/lib/ly-route/ly-route-dns-vpp-proxy"
  cat > "$package_root/DEBIAN/control" <<EOF
Package: ly-route-dns-vpp-proxy
Version: $version
Section: net
Priority: optional
Architecture: $deb_arch
Maintainer: Ly Route <root@ly-route.local>
Depends: vpp, smartdns
Description: Ly Route VPP native DNS service adapter
EOF
  cat > "$package_root/DEBIAN/postinst" <<'EOF'
#!/bin/sh
set -e
install -d -m 0755 /etc/ly-route
if [ ! -e /etc/ly-route/dns-source-routes.conf ]; then
  umask 027
  printf '%s\n' '# source-prefix match-kind domain smartdns-port' >/etc/ly-route/dns-source-routes.conf
fi
if command -v systemctl >/dev/null 2>&1; then
  systemctl daemon-reload >/dev/null 2>&1 || true
fi
exit 0
EOF
  chmod 0755 "$package_root/DEBIAN/postinst"
  cp "$repo_root/packaging/rootfs-overlay/etc/systemd/system/smartdns.service.d/10-ly-route-vpp-lifecycle.conf" \
    "$package_root/etc/systemd/system/smartdns.service.d/10-ly-route-vpp-lifecycle.conf"
  cp "$repo_root/packaging/rootfs-overlay/etc/systemd/system/ly-route-policy-routing.service.d/10-dns-vpp-lifecycle.conf" \
    "$package_root/etc/systemd/system/ly-route-policy-routing.service.d/10-dns-vpp-lifecycle.conf"
  cat > "$package_root/lib/systemd/system/ly-route-dns-vpp-proxy.service" <<'EOF'
[Unit]
Description=LY-Route VPP native DNS service adapter
Requires=vpp.service ly-route-vpp-session-enable.service smartdns.service ly-route-dns-vpp-v6-namespace.service ly-route-policy-routing.service
PartOf=vpp.service ly-route-dns-vpp-v6-namespace.service ly-route-policy-routing.service
After=vpp.service ly-route-vpp-session-enable.service smartdns.service ly-route-dns-vpp-v6-namespace.service ly-route-policy-routing.service
Before=ly-route-dns-vpp-session.service

[Service]
Type=simple
Environment=VCL_CONFIG=/etc/ly-route/vcl.conf
Environment=VCL_VPP_API_SOCKET=/run/vpp/api.sock
Environment=VCL_APP_NAMESPACE_ID=dns-v4
Environment=VCL_APP_NAMESPACE_SECRET=4242
Environment=LD_PRELOAD=/usr/lib/ly-route/libvcl_ldpreload.so
Environment=LY_ROUTE_DNS_FAMILY=ipv4
Environment=LY_ROUTE_DNS_SOURCE_ROUTES=/etc/ly-route/dns-source-routes.conf
ExecStart=/usr/lib/ly-route/ly-route-dns-vpp-proxy
Restart=on-failure
RestartSec=2s
TimeoutStopSec=5s

[Install]
WantedBy=multi-user.target
EOF
  cat > "$package_root/lib/systemd/system/ly-route-dns-vpp-proxy-v6.service" <<'EOF'
[Unit]
Description=LY-Route VPP native IPv6 DNS service adapter
Requires=vpp.service ly-route-vpp-session-enable.service smartdns.service ly-route-dns-vpp-v6-namespace.service ly-route-policy-routing.service
PartOf=vpp.service ly-route-dns-vpp-v6-namespace.service ly-route-policy-routing.service
After=vpp.service ly-route-vpp-session-enable.service smartdns.service ly-route-dns-vpp-v6-namespace.service ly-route-policy-routing.service
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
TimeoutStopSec=5s

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
import tempfile
import time

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

VPP_ROUTE_BATCH_BEGIN = "__ly-route-vpp-batch-begin__"
VPP_ROUTE_BATCH_END = "__ly-route-vpp-batch-end__"
VPP_ROUTE_BATCH_CHUNK_SIZE = 32

def collect_route_batch(commands, begin):
    batch = []
    for index in range(begin + 1, len(commands)):
        command = commands[index].strip()
        command = command[1:].strip() if command.startswith("?") else command
        if command == VPP_ROUTE_BATCH_END:
            return batch, index
        if command == VPP_ROUTE_BATCH_BEGIN:
            raise ValueError("nested VPP route command batch")
        if command:
            batch.append(command)
    raise ValueError("unterminated VPP route command batch")

def run_vpp_exec(vppctl, commands):
    temporary = tempfile.NamedTemporaryFile("w", encoding="utf-8", prefix="ly-route-vpp-batch-", suffix=".conf", delete=False)
    path = temporary.name
    try:
        for command in commands:
            temporary.write(command.strip() + "\n")
        temporary.close()
        result = subprocess.run([vppctl, "exec", path], text=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
        combined = (result.stdout + "\n" + result.stderr).lower()
        if result.returncode == 0 and not any(marker in combined for marker in ("cli line error", "unknown input", "parse error")):
            return result
        if result.returncode == 0:
            result.returncode = 1
        return result
    finally:
        try:
            os.unlink(path)
        except FileNotFoundError:
            pass

def command_without_optional_prefix(command):
    command = command.strip()
    return command[1:].strip() if command.startswith("?") else command

def operation_payload(operation):
    payload = operation.get("payload", operation.get("Payload", {}))
    return payload if isinstance(payload, dict) else {}

def route_policy_priority(operation):
    value = operation_payload(operation).get("priority", operation_payload(operation).get("Priority", 0))
    try:
        return int(value)
    except (TypeError, ValueError):
        return 0

def operation_with_name_and_commands(operation, name, commands):
    staged = dict(operation)
    if "name" in staged:
        staged["name"] = name
    elif "Name" in staged:
        staged["Name"] = name
    else:
        staged["name"] = name
    for field in ("vppctl_commands", "VPPCtlCommands", "commands", "Commands"):
        if field in staged:
            staged[field] = commands
            return staged
    staged["vppctl_commands"] = commands
    return staged

def stage_route_policy_operations(operations):
    route_indexes = [index for index, operation in enumerate(operations)
                     if operation_name(operation) == "vpp.route-policy"]
    if len(route_indexes) < 2:
        return operations
    first_route = route_indexes[0]
    routes = [operations[index] for index in route_indexes]
    routes.sort(key=lambda operation: (route_policy_priority(operation), operation_resource(operation)))

    def prepare_commands(commands):
        return [raw for raw in commands if command_without_optional_prefix(raw).startswith("ip table add ")]

    def populate_commands(commands):
        selected = []
        for raw in commands:
            command = command_without_optional_prefix(raw)
            if command in (VPP_ROUTE_BATCH_BEGIN, VPP_ROUTE_BATCH_END) or command.startswith("set ip flow-hash table ") or command.startswith("ip route add "):
                selected.append(raw)
        return selected

    def activation_commands(commands):
        selected = []
        for raw in commands:
            command = command_without_optional_prefix(raw)
            if (command.startswith("set acl-plugin acl ") or command.startswith("abf policy add ") or
                    command.startswith("abf attach ip4 policy ") or command.startswith("show acl-plugin acl index ") or
                    command.startswith("show abf policy ") or command == "show interface" or
                    command.startswith("show abf attach ") or command.startswith("show ip fib table ")):
                selected.append(raw)
        return selected

    staged = list(operations[:first_route])
    for operation in routes:
        staged.append(operation_with_name_and_commands(operation, "vpp.route-table.prepare", prepare_commands(commands_from_operation(operation))))
    for operation in routes:
        staged.append(operation_with_name_and_commands(operation, "vpp.route-table.populate", populate_commands(commands_from_operation(operation))))
    for operation in routes:
        staged.append(operation_with_name_and_commands(operation, "vpp.route-policy", activation_commands(commands_from_operation(operation))))
    route_index_set = set(route_indexes)
    for index, operation in enumerate(operations):
        if index < first_route or index in route_index_set:
            continue
        staged.append(operation)
    return staged

def expand_command(command):
    expanded = os.path.expandvars(command)
    missing = sorted(set(re.findall(r"\$\{?[A-Za-z_][A-Za-z0-9_]*\}?", expanded)))
    if missing:
        raise ValueError(f"missing environment variable for VPP command {command!r}: {', '.join(missing)}")
    return expanded

def active_abf_acl_ids_by_tag(vppctl, operations, command_map):
    """Resolve managed ACL tags through the ABF policies that currently own them."""
    active = {}
    for operation in operations:
        if not isinstance(operation, dict):
            continue
        commands = commands_from_operation(operation)
        if not commands:
            commands = commands_from_map(operation, command_map)
        tags_by_stable_id = {}
        for command in commands:
            match = re.fullmatch(r"\??set acl-plugin acl index ([0-9]+) (.+)", command.strip())
            if match is None:
                continue
            tag = re.search(r"(?:^|\s)tag\s+(\S+)\s*$", match.group(2))
            if tag is not None:
                tags_by_stable_id[int(match.group(1))] = tag.group(1)
        for command in commands:
            match = re.fullmatch(r"\??abf policy add id ([0-9]+) acl ([0-9]+) .+", command.strip())
            if match is None:
                continue
            tag = tags_by_stable_id.get(int(match.group(2)))
            if not tag:
                continue
            result = run_vppctl(vppctl, f"show abf policy {match.group(1)}")
            if result.returncode != 0:
                continue
            observed = re.search(r"\bpolicy:\s*" + re.escape(match.group(1)) + r"\s+acl:\s*([0-9]+)\b", result.stdout)
            if observed is not None:
                active[tag] = int(observed.group(1))
    return active

def existing_acl_ids_by_tag(vppctl, active_acl_ids=None):
    """Map managed ACL tags to their live VPP indexes for idempotent replay."""
    result = subprocess.run([vppctl, "show", "acl-plugin", "acl"], text=True,
                            stdout=subprocess.PIPE, stderr=subprocess.PIPE)
    if result.returncode != 0:
        fail_with_receipt(f"failed to inspect existing VPP ACLs: {result.stderr.strip()}")
    tagged = {}
    for match in re.finditer(r"^acl-index\s+(\d+)\s+count\s+\d+\s+tag\s+\{([^}]+)\}", result.stdout, re.MULTILINE):
        tagged.setdefault(match.group(2).strip(), []).append(int(match.group(1)))
    for tag, active_id in (active_acl_ids or {}).items():
        if active_id in tagged.get(tag, []):
            # A failed replacement can leave a stale ACL with the same tag.
            # The live ABF policy is the authoritative owner during replay.
            tagged[tag] = [active_id]
    return tagged

def prepare_replay_command(command, acl_ids, existing_acls):
    create = re.fullmatch(r"set acl-plugin acl index ([0-9]+) (.+)", command)
    if create:
        stable_id = int(create.group(1))
        tag = re.search(r"(?:^|\s)tag\s+(\S+)\s*$", create.group(2))
        if tag is not None:
            existing = existing_acls.get(tag.group(1), [])
            if len(existing) > 1:
                raise ValueError(f"VPP ACL tag {tag.group(1)!r} is ambiguous: {existing}")
            if len(existing) == 1:
                allocated_id = existing[0]
                acl_ids[stable_id] = allocated_id
                return f"set acl-plugin acl index {allocated_id} {create.group(2)}", None
        return f"set acl-plugin acl {create.group(2)}", stable_id
    for stable_id, allocated_id in acl_ids.items():
        command = re.sub(
            rf"\bacl\s+index\s+{stable_id}\b",
            f"acl index {allocated_id}",
            command,
        )
        command = re.sub(
            rf"\bacl\s+{stable_id}\b",
            f"acl {allocated_id}",
            command,
        )
    return command, None

def replay_abf_policy_already_present(vppctl, command):
    """Avoid appending the same path every time persisted state is replayed.

    The live transaction applies and verifies an ABF policy before its
    operation is persisted.  A subsequent service reload therefore only has
    to create a missing policy (as it does after a cold VPP start).  VPP treats
    another `abf policy add` for an existing ID as an additional path, so
    blindly replaying it grows the path list and can make later removal crash
    the ABF plugin.
    """
    match = re.fullmatch(r"abf policy add id ([0-9]+) acl ([0-9]+) via .+", command)
    if match is None:
        return False
    policy_id = match.group(1)
    wanted_acl = int(match.group(2))
    result = run_vppctl(vppctl, f"show abf policy {policy_id}")
    if result.returncode != 0:
        return False
    observed = re.search(r"\bpolicy:\s*" + re.escape(policy_id) + r"\s+acl:\s*([0-9]+)\b", result.stdout)
    if observed is None:
        return False
    actual_acl = int(observed.group(1))
    if actual_acl != wanted_acl:
        raise ValueError(
            f"VPP ABF policy {policy_id} owns ACL {actual_acl}, want {wanted_acl}; refusing non-idempotent replay"
        )
    return True

def allocated_acl_id(stdout, stderr):
    match = re.search(r"\bACL index:\s*([0-9]+)\b", f"{stdout}\n{stderr}", re.IGNORECASE)
    if match is None:
        raise ValueError("VPP did not report the allocated ACL index")
    return int(match.group(1))

def write_receipt(path, receipt):
    os.makedirs(os.path.dirname(path), exist_ok=True)
    temporary = path + ".tmp"
    with open(temporary, "w", encoding="utf-8") as handle:
        json.dump(receipt, handle, indent=2, sort_keys=True)
        handle.write("\n")
    os.replace(temporary, path)

def desired_tap_names(operations, command_map):
    """Find TAP names owned by the DNS/proxy networks being replayed."""
    names = set()
    for operation in operations:
        name = operation_name(operation)
        if name not in {"vpp.dns-service.network", "vpp.proxy-service.network"}:
            continue
        payload = operation.get("Payload") or operation.get("payload") or {}
        def collect(value):
            if isinstance(value, dict):
                for key in ("vpp_interface", "host_interface", "ingress_vpp_interface",
                            "ingress_host_interface", "egress_vpp_interface",
                            "egress_host_interface"):
                    item = value.get(key)
                    if isinstance(item, str) and item.strip():
                        names.add(item.strip())
                for child in value.values():
                    collect(child)
            elif isinstance(value, list):
                for child in value:
                    collect(child)
        collect(payload)
        commands = commands_from_operation(operation) or commands_from_map(operation, command_map)
        for command in commands:
            match = re.search(r"host-if-name\s+(\S+)", command)
            if match:
                names.add(match.group(1))
            match = re.search(r"set interface name\s+\S+\s+(\S+)", command)
            if match:
                names.add(match.group(1))
    return names

def clear_stale_taps(vppctl, operations, command_map, receipt):
    """Remove stale TAP objects before replaying deterministic names.

    A stopped transaction may leave the old object in VPP. Replaying a
    create/rename then produces an ambiguous interface and makes the next
    Apply fail even though the desired configuration is valid.
    """
    targets = desired_tap_names(operations, command_map)
    managed_prefixes = ("lydns", "lydnsh", "lypxin", "lypxhin", "lypxout", "lypxhout")

    def managed_name(name):
        return any(name.startswith(prefix) for prefix in managed_prefixes)
    result = subprocess.run([vppctl, "show", "tap"], text=True,
                            stdout=subprocess.PIPE, stderr=subprocess.PIPE)
    if result.returncode != 0:
        fail_with_receipt(f"failed to inspect VPP TAP interfaces: {result.stderr.strip()}")
    records = []
    for block in re.split(r"(?=^Interface:\s)", result.stdout, flags=re.MULTILINE):
        header = re.search(r"^Interface:\s+\S+\s+\(ifindex\s+(\d+)\)", block, re.MULTILINE)
        if header is None:
            continue
        names = set(re.findall(r'^Interface:\s+(\S+)', block, re.MULTILINE))
        names.update(re.findall(r'^\s*name\s+"([^"]+)"', block, re.MULTILINE))
        matching = {name for name in names if managed_name(name)}
        if not matching:
            continue
        index = header.group(1)
        records.append((int(index), matching))
    keep = set()
    for target in targets:
        candidates = sorted(index for index, matching in records if target in matching)
        if candidates:
            keep.add(candidates[0])
    removed = []
    for index, matching in sorted(records, reverse=True):
        if index in keep:
            continue
        delete_result = run_vppctl(vppctl, f"delete tap sw_if_index {index}")
        if delete_result.returncode != 0:
            fail_with_receipt(f"failed to remove stale VPP TAP {index}: {delete_result.stderr.strip()}")
        removed.append({"sw_if_index": index, "names": sorted(matching)})
    if removed:
        receipt["stale_taps_removed"] = removed

def stable_id(value, minimum, span):
    import hashlib
    digest = hashlib.sha256(value.strip().encode("utf-8")).digest()
    return minimum + (int.from_bytes(digest[:4], "big") % span)

def payload_value(payload, key, default=None):
    if not isinstance(payload, dict):
        return default
    return payload.get(key, default)

def selector_prefixes(values):
    import ipaddress
    if not isinstance(values, list) or not values:
        values = ["any"]
    result = []
    seen = set()
    for raw in values:
        raw = str(raw).strip()
        if not raw or raw.lower() == "any":
            raw = "0.0.0.0/0"
        try:
            network = ipaddress.ip_network(raw, strict=False)
        except ValueError:
            continue
        if network.version != 4:
            continue
        value = str(network)
        if value not in seen:
            seen.add(value)
            result.append(value)
    return result or ["0.0.0.0/0"]

def selector_protocols(values):
    if not isinstance(values, list) or not values:
        return ["any"]
    result = []
    seen = set()
    for raw in values:
        value = str(raw).strip().lower() or "any"
        if value not in {"any", "tcp", "udp", "icmp"} or value in seen:
            continue
        seen.add(value)
        result.append(value)
    return result or ["any"]

def selector_ports(values):
    if not isinstance(values, list) or not values:
        values = ["any"]
    result = []
    seen = set()
    for raw in values:
        value = str(raw).strip().lower()
        if not value or value == "any":
            value = "0-65535"
        parts = value.split("-", 1)
        try:
            first = int(parts[0])
            last = int(parts[1]) if len(parts) == 2 else first
        except ValueError:
            continue
        if first < 0 or last < first or last > 65535:
            continue
        item = (first, last)
        if item not in seen:
            seen.add(item)
            result.append(item)
    return result or [(0, 65535)]

def operation_lan_interface(operations):
    for operation in operations:
        for command in commands_from_operation(operation):
            match = re.search(r"abf attach ip4 policy \d+ priority \d+ (\S+)", command)
            if match:
                return match.group(1)
    return ""

def install_pre_nat_routes(vppctl, operations, receipt):
    """Rebuild policy classification after route tables exist.

    The control API persists declarative operations and the standalone replay
    service is also used after a cold VPP restart. Keeping this step here is
    what makes native route/NAT and transparent proxy paths identical across
    both entry points. Proxy TAP paths skip NAT so Xray receives the original
    client source for TPROXY; ordinary WAN paths retain NAT.
    """
    ingress = operation_lan_interface(operations)
    if not ingress:
        return
    rules = []
    for operation in operations:
        if operation_name(operation) != "vpp.route-policy":
            continue
        payload = operation.get("Payload") or operation.get("payload") or {}
        if str(payload_value(payload, "action", "")).strip().lower() == "deny":
            continue
        path = payload_value(payload, "path", {})
        if not isinstance(path, dict) or not str(path.get("vpp_interface", "")).strip():
            continue
        resource = operation_resource(operation)
        if not resource:
            continue
        policy_id = stable_id("route-abf:" + resource, 10000, 8999)
        table_id = stable_id("route-table:" + resource, 50000, 49999)
        priority = int(payload_value(payload, "priority", 0) or 0)
        match = payload_value(payload, "match", {})
        sources = selector_prefixes(payload_value(match, "sources", []))
        destinations = selector_prefixes(payload_value(match, "destinations", []))
        protocols = selector_protocols(payload_value(match, "protocols", []))
        source_ports = selector_ports(payload_value(match, "source_ports", []))
        dest_ports = selector_ports(payload_value(match, "dest_ports", []))
        skip_nat = str(path.get("vpp_interface", "")).lower().startswith("lypxin")
        for source in sources:
            for destination in destinations:
                for protocol in protocols:
                    for source_first, source_last in source_ports:
                        for dest_first, dest_last in dest_ports:
                            suffix = " skip-nat" if skip_nat else ""
                            rules.append(
                                f"set ly-route pre-nat-route add id {policy_id} priority {priority} "
                                f"source {source} destination {destination} protocol {protocol} "
                                f"sport {source_first}-{source_last} dport {dest_first}-{dest_last} "
                                f"table {table_id}{suffix}"
                            )
    clear = run_vppctl(vppctl, "set ly-route pre-nat-route clear")
    if clear.returncode != 0:
        fail_with_receipt(f"failed to clear VPP pre-NAT routes: {clear.stderr.strip()}")
    if not rules:
        return
    address = run_vppctl(vppctl, f"show interface address {ingress}")
    if address.returncode != 0:
        fail_with_receipt(f"failed to read VPP LAN address for pre-NAT routes: {address.stderr.strip()}")
    lan_prefix = None
    for field in address.stdout.split():
        if re.fullmatch(r"\d+\.\d+\.\d+\.\d+/\d+", field.rstrip(",")):
            import ipaddress
            lan_prefix = str(ipaddress.ip_interface(field.rstrip(",")).network)
            break
    if lan_prefix is None:
        fail_with_receipt(f"VPP LAN interface {ingress} has no IPv4 address")
    configured = run_vppctl(vppctl, f"set ly-route pre-nat-route interface {ingress} lan-prefix {lan_prefix}")
    if configured.returncode != 0:
        fail_with_receipt(f"failed to configure VPP pre-NAT interface: {configured.stderr.strip()}")
    for command in rules:
        result = run_vppctl(vppctl, command)
        if result.returncode != 0:
            fail_with_receipt(f"failed to install VPP pre-NAT route: {result.stderr.strip()}")
    receipt["pre_nat_routes"] = len(rules)

if len(sys.argv) < 2 or len(sys.argv) > 4:
    fail("usage: vpp-apply [--dry-run] [--underlay-only] /path/to/operations.json")

dry_run = truthy(os.environ.get("LY_ROUTE_VPP_APPLY_DRY_RUN", "false"))
underlay_only = truthy(os.environ.get("LY_ROUTE_VPP_UNDERLAY_ONLY", "false"))
arguments = sys.argv[1:]
while arguments and arguments[0] in ("--dry-run", "--underlay-only"):
    if arguments[0] == "--dry-run":
        dry_run = True
    else:
        underlay_only = True
    arguments = arguments[1:]
if len(arguments) != 1:
    fail("usage: vpp-apply [--dry-run] [--underlay-only] /path/to/operations.json")

operations_path = arguments[0]
command_map_path = os.environ.get("LY_ROUTE_VPP_COMMAND_MAP", "/etc/ly-route/vpp-command-map.json")
receipt_path = os.environ.get("LY_ROUTE_VPP_RECEIPT", "/var/lib/ly-route/vpp-apply-receipt.json")
receipt = {"status": "ready", "dry_run": dry_run, "underlay_only": underlay_only, "operations": []}

def fail_with_receipt(message):
    receipt["status"] = "failed"
    receipt["error"] = message
    write_receipt(receipt_path, receipt)
    fail(message)

def pppoe_underlay_interfaces(operations, command_map):
    """Return native VPP PPPoE interfaces referenced by replay commands."""
    names = set()
    for operation in operations:
        commands = commands_from_operation(operation)
        if not commands:
            commands = commands_from_map(operation, command_map)
        for command in commands:
            names.update(re.findall(r"\bpppoe_session[0-9]+\b", command))
    return sorted(names)

def wait_for_pppoe_underlay(vppctl, operations, command_map):
    """Do not replay routes until every native PPPoE session interface exists."""
    interfaces = pppoe_underlay_interfaces(operations, command_map)
    if not interfaces:
        return
    try:
        attempts = int(os.environ.get("LY_ROUTE_PPPOE_READY_ATTEMPTS", "60"))
        interval = float(os.environ.get("LY_ROUTE_PPPOE_READY_INTERVAL", "1"))
    except ValueError:
        fail_with_receipt("PPPoE readiness retry settings must be positive numbers")
    if attempts <= 0 or interval <= 0:
        fail_with_receipt("PPPoE readiness retry settings must be positive numbers")
    for attempt in range(attempts):
        session = subprocess.run([vppctl, "show", "pppoe", "session"], text=True,
                                 stdout=subprocess.PIPE, stderr=subprocess.PIPE)
        session_ready = session.returncode == 0 and bool(session.stdout.strip())
        interfaces_ready = True
        for interface in interfaces:
            result = subprocess.run([vppctl, "show", "interface", interface], text=True,
                                    stdout=subprocess.PIPE, stderr=subprocess.PIPE)
            if result.returncode != 0 or interface not in result.stdout:
                interfaces_ready = False
                break
        if session_ready and interfaces_ready:
            return
        if attempt + 1 < attempts:
            time.sleep(interval)
    fail_with_receipt("native PPPoE underlay did not become ready before VPP operation replay: " + ", ".join(interfaces))

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

if underlay_only:
    operations = [operation for operation in operations if operation_name(operation) == "vpp.dataplane.attach"]

operations = stage_route_policy_operations(operations)

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
    try:
        ready_attempts = int(os.environ.get("LY_ROUTE_VPP_READY_ATTEMPTS", "60"))
        ready_interval = float(os.environ.get("LY_ROUTE_VPP_READY_INTERVAL", "1"))
    except ValueError:
        fail_with_receipt("VPP readiness retry settings must be positive numbers")
    if ready_attempts <= 0 or ready_interval <= 0:
        fail_with_receipt("VPP readiness retry settings must be positive numbers")
    for attempt in range(ready_attempts):
        version_result = subprocess.run([vppctl, "show", "version"], text=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
        if version_result.returncode == 0:
            break
        if attempt + 1 < ready_attempts:
            time.sleep(ready_interval)
    else:
        fail_with_receipt("VPP API did not become ready before operation replay")

if not underlay_only:
    clear_stale_taps(vppctl, operations, command_map, receipt)

acl_ids = {}
active_acl_ids = active_abf_acl_ids_by_tag(vppctl, operations, command_map)
existing_acls = existing_acl_ids_by_tag(vppctl, active_acl_ids)
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
    # Replay foundational LAN/WAN attachment operations before waiting for a
    # PPPoE session. A cold VPP restart can have no PPP session yet, but the
    # PPPoE client itself needs the restored WAN and LAN objects to negotiate
    # and program the session. Wait only when the next operation actually
    # references the native PPPoE underlay.
    if pppoe_underlay_interfaces([operation], command_map):
        wait_for_pppoe_underlay(vppctl, [operation], command_map)
    entry = {"name": name, "resource": resource, "commands": commands, "results": []}
    route_batch_indexes = set()
    for command_index, command in enumerate(commands):
        if command_index in route_batch_indexes:
            continue
        ignore_failure = command.startswith("?")
        if ignore_failure:
            command = command[1:].strip()
        receipt_command = command
        if command == VPP_ROUTE_BATCH_BEGIN:
            try:
                route_batch, batch_end = collect_route_batch(commands, command_index)
                expanded_batch = [expand_command(route_command) for route_command in route_batch]
            except ValueError as err:
                entry["results"].append({"command": receipt_command, "status": "failed", "stderr": receipt_text(str(err))})
                receipt["operations"].append(entry)
                fail_with_receipt(str(err))
            route_batch_indexes.update(range(command_index + 1, batch_end + 1))
            for batch_begin in range(0, len(expanded_batch), VPP_ROUTE_BATCH_CHUNK_SIZE):
                batch_end_index = min(batch_begin + VPP_ROUTE_BATCH_CHUNK_SIZE, len(expanded_batch))
                label = f"vpp route command batch {batch_begin + 1}-{batch_end_index}/{len(expanded_batch)}"
                receipt["in_progress"] = {"operation": name, "resource": resource, "command": label}
                write_receipt(receipt_path, receipt)
                if dry_run:
                    entry["results"].append({"command": label, "status": "dry-run"})
                    continue
                result = run_vpp_exec(vppctl, expanded_batch[batch_begin:batch_end_index])
                stdout = result.stdout.strip()
                stderr = result.stderr.strip()
                if result.returncode != 0:
                    entry["results"].append({"command": label, "status": "failed", "stdout": receipt_text(stdout), "stderr": receipt_text(stderr)})
                    receipt["operations"].append(entry)
                    fail_with_receipt(f"vppctl route batch failed for operation {name}: {label}")
                entry["results"].append({"command": label, "status": "applied", "stdout": receipt_text(stdout), "stderr": receipt_text(stderr)})
            continue
        if command == VPP_ROUTE_BATCH_END:
            entry["results"].append({"command": receipt_command, "status": "failed", "stderr": "unexpected VPP route command batch terminator"})
            receipt["operations"].append(entry)
            fail_with_receipt(f"unexpected VPP route command batch terminator for operation {name}")
        try:
            command = expand_command(command)
        except ValueError as err:
            entry["results"].append({"command": receipt_command, "status": "failed", "stderr": receipt_text(str(err))})
            receipt["operations"].append(entry)
            fail_with_receipt(str(err))
        try:
            effective_command, stable_acl_id = prepare_replay_command(command, acl_ids, existing_acls)
        except ValueError as err:
            entry["results"].append({"command": receipt_command, "status": "failed", "stderr": receipt_text(str(err))})
            receipt["operations"].append(entry)
            fail_with_receipt(str(err))
        if not dry_run:
            try:
                if replay_abf_policy_already_present(vppctl, effective_command):
                    result_entry = {"command": receipt_command, "status": "already-applied"}
                    if effective_command != command:
                        result_entry["effective_command"] = effective_command
                    entry["results"].append(result_entry)
                    continue
            except ValueError as err:
                entry["results"].append({"command": receipt_command, "status": "failed", "stderr": receipt_text(str(err))})
                receipt["operations"].append(entry)
                fail_with_receipt(str(err))
        argv = shlex.split(effective_command)
        if not argv:
            entry["results"].append({"command": receipt_command, "status": "failed", "stderr": "empty VPP command"})
            receipt["operations"].append(entry)
            fail_with_receipt(f"empty VPP command for operation {name}")
        if dry_run:
            result_entry = {"command": receipt_command, "status": "dry-run"}
            if effective_command != command:
                result_entry["effective_command"] = effective_command
            entry["results"].append(result_entry)
            if stable_acl_id is not None:
                acl_ids[stable_acl_id] = stable_acl_id
            continue
        receipt["in_progress"] = {"operation": name, "resource": resource, "command": receipt_command}
        write_receipt(receipt_path, receipt)
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
        if stable_acl_id is not None:
            try:
                acl_ids[stable_acl_id] = allocated_acl_id(stdout, stderr)
            except ValueError as err:
                entry["results"].append({"command": receipt_command, "status": "failed", "stdout": receipt_text(stdout), "stderr": receipt_text(str(err))})
                receipt["operations"].append(entry)
                fail_with_receipt(f"failed to recover dynamic ACL for operation {name}: {err}")
        result_entry = {"command": receipt_command, "status": "applied", "stdout": receipt_text(stdout), "stderr": receipt_text(stderr)}
        if effective_command != command:
            result_entry["effective_command"] = effective_command
        entry["results"].append(result_entry)
    receipt["operations"].append(entry)

if not underlay_only:
    install_pre_nat_routes(vppctl, operations, receipt)

receipt["status"] = "applied"
receipt.pop("in_progress", None)
write_receipt(receipt_path, receipt)
EOF
  chmod 0755 "$package_root/usr/lib/ly-route/vpp-apply"
  rm -f "$out_dir/ly-route-vpp-apply_"*"_${deb_arch}.deb"
  dpkg-deb --build --root-owner-group "$package_root" "$out_dir/ly-route-vpp-apply_${version}_${deb_arch}.deb" >/dev/null
  rm -rf "$work_dir"
}

build_vpp() {
  require_command make
  if [ -z "$vpp_src" ]; then
    echo "LY_ROUTE_VPP_SRC is required for the vpp target; refusing to use a stale vpp-master directory" >&2
    exit 2
  fi
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
  package_list=$(mktemp "${TMPDIR:-/tmp}/ly-route-vpp-packages.XXXXXX")
  find "$vpp_src" -path '*/build-root/*.deb' -o -path '*/build-root/packages/*.deb' -o -path '*/build-root/install-vpp-native/*.deb' > "$package_list"
  while IFS= read -r package; do
    cp "$package" "$out_dir/"
    found=1
  done < "$package_list"
  rm -f "$package_list"
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
  build_debs=$(mktemp -d "${TMPDIR:-/tmp}/ly-route-vpp-smart-qos-deps.XXXXXX")
  package_root=$(mktemp -d "${TMPDIR:-/tmp}/ly-route-vpp-smart-qos-package.XXXXXX")
  trap 'rm -rf "$build_debs" "$package_root"' RETURN
  vpp_dev_dir=${LY_ROUTE_VPP_DEV_DEBS_DIR:-}
  [ -n "$vpp_dev_dir" ] || vpp_dev_dir="$build_debs"
  if [ ! -f "$vpp_dev_dir/vpp-dev_"*.deb ] || [ ! -f "$vpp_dev_dir/libvppinfra-dev_"*.deb ]; then
    fetch_fdio_package vpp-dev "$build_debs"
    fetch_fdio_package libvppinfra-dev "$build_debs"
    vpp_dev_dir="$build_debs"
  fi
  plugin=$(LY_ROUTE_VPP_DEV_DEBS_DIR="$vpp_dev_dir" "$repo_root/scripts/build-vpp-smart-qos-plugin.sh" | tail -1)
  [ -f "$plugin" ] || { echo "smart QoS plugin build did not produce a library" >&2; exit 1; }
  plugin_dir="$package_root/usr/lib/$(vpp_plugin_multiarch)/vpp_plugins"
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
Depends: vpp (= ${LY_ROUTE_VPP_PACKAGE_VERSION:-$fdio_vpp_version})
Description: Ly Route VPP FQ-CoDel smart QoS plugin
EOF
  mkdir -p "$out_dir"
  rm -f "$out_dir/ly-route-vpp-smart-qos_"*"_${deb_arch}.deb"
  dpkg-deb --build --root-owner-group "$package_root" "$out_dir/ly-route-vpp-smart-qos_25.10.0+lyroute1_${deb_arch}.deb" >/dev/null
}

build_vpp_pppoe_client() {
  require_command cmake
  build_debs=$(mktemp -d "${TMPDIR:-/tmp}/ly-route-vpp-pppoe-client-deps.XXXXXX")
  package_root=$(mktemp -d "${TMPDIR:-/tmp}/ly-route-vpp-pppoe-client-package.XXXXXX")
  trap 'rm -rf "$build_debs" "$package_root"' RETURN
  vpp_dev_dir=${LY_ROUTE_VPP_DEV_DEBS_DIR:-}
  [ -n "$vpp_dev_dir" ] || vpp_dev_dir="$build_debs"
  if ! find "$vpp_dev_dir" -maxdepth 1 -type f -name 'vpp-dev_*.deb' -print -quit | grep -q . ||
     ! find "$vpp_dev_dir" -maxdepth 1 -type f -name 'libvppinfra-dev_*.deb' -print -quit | grep -q .; then
    fetch_fdio_package vpp-dev "$build_debs"
    fetch_fdio_package libvppinfra-dev "$build_debs"
    vpp_dev_dir="$build_debs"
  fi
  plugin=$(LY_ROUTE_VPP_DEV_DEBS_DIR="$vpp_dev_dir" "$repo_root/scripts/build-vpp-pppoe-client-plugin.sh" | tail -1)
  [ -f "$plugin" ] || { echo "PPPoE client plugin build did not produce a library" >&2; exit 1; }
  case "$deb_arch" in
    amd64) multiarch=x86_64-linux-gnu ;;
    arm64) multiarch=aarch64-linux-gnu ;;
    armhf) multiarch=arm-linux-gnueabihf ;;
    *) echo "unsupported plugin architecture: $deb_arch" >&2; exit 1 ;;
  esac
  plugin_dir="$package_root/usr/lib/$multiarch/vpp_plugins"
  mkdir -p "$package_root/DEBIAN" "$plugin_dir"
  install -m 0644 "$plugin" "$plugin_dir/ly_route_pppoe_client_plugin.so"
  cat > "$package_root/DEBIAN/control" <<EOF
Package: ly-route-vpp-pppoe-client
Version: 25.10.0+lyroute1
Section: net
Priority: optional
Architecture: $deb_arch
Maintainer: Ly Route <root@ly-route.local>
Depends: vpp (= ${LY_ROUTE_VPP_PACKAGE_VERSION:-$fdio_vpp_version})
Description: Ly Route native VPP PPPoE client binding
EOF
  mkdir -p "$out_dir"
  rm -f "$out_dir/ly-route-vpp-pppoe-client_"*"_${deb_arch}.deb"
  dpkg-deb --build --root-owner-group "$package_root" "$out_dir/ly-route-vpp-pppoe-client_25.10.0+lyroute1_${deb_arch}.deb" >/dev/null
}

build_vpp_security_guard() {
  require_command cmake
  build_debs=$(mktemp -d "${TMPDIR:-/tmp}/ly-route-vpp-security-guard-deps.XXXXXX")
  package_root=$(mktemp -d "${TMPDIR:-/tmp}/ly-route-vpp-security-guard-package.XXXXXX")
  trap 'rm -rf "$build_debs" "$package_root"' RETURN
  vpp_dev_dir=${LY_ROUTE_VPP_DEV_DEBS_DIR:-}
  [ -n "$vpp_dev_dir" ] || vpp_dev_dir="$build_debs"
  if [ ! -f "$vpp_dev_dir/vpp-dev_"*.deb ] || [ ! -f "$vpp_dev_dir/libvppinfra-dev_"*.deb ]; then
    fetch_fdio_package vpp-dev "$build_debs"
    fetch_fdio_package libvppinfra-dev "$build_debs"
    vpp_dev_dir="$build_debs"
  fi
  plugin=$(LY_ROUTE_VPP_DEV_DEBS_DIR="$vpp_dev_dir" "$repo_root/scripts/build-vpp-security-guard-plugin.sh" | tail -1)
  [ -f "$plugin" ] || { echo "security guard plugin build did not produce a library" >&2; exit 1; }
  plugin_dir="$package_root/usr/lib/$(vpp_plugin_multiarch)/vpp_plugins"
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
Depends: vpp (= ${LY_ROUTE_VPP_PACKAGE_VERSION:-$fdio_vpp_version})
Description: Ly Route VPP protocol-aware security rate guard plugin
EOF
  mkdir -p "$out_dir"
  rm -f "$out_dir/ly-route-vpp-security-guard_"*"_${deb_arch}.deb"
  dpkg-deb --build --root-owner-group "$package_root" "$out_dir/ly-route-vpp-security-guard_25.10.0+lyroute1_${deb_arch}.deb" >/dev/null
}

build_vpp_dns_intercept() {
  require_command cmake
  build_debs=$(mktemp -d "${TMPDIR:-/tmp}/ly-route-vpp-dns-intercept-deps.XXXXXX")
  package_root=$(mktemp -d "${TMPDIR:-/tmp}/ly-route-vpp-dns-intercept-package.XXXXXX")
  trap 'rm -rf "$build_debs" "$package_root"' RETURN
  vpp_dev_dir=${LY_ROUTE_VPP_DEV_DEBS_DIR:-}
  [ -n "$vpp_dev_dir" ] || vpp_dev_dir="$build_debs"
  if [ ! -f "$vpp_dev_dir/vpp-dev_"*.deb ] || [ ! -f "$vpp_dev_dir/libvppinfra-dev_"*.deb ]; then
    fetch_fdio_package vpp-dev "$build_debs"
    fetch_fdio_package libvppinfra-dev "$build_debs"
    vpp_dev_dir="$build_debs"
  fi
  plugin=$(LY_ROUTE_VPP_DEV_DEBS_DIR="$vpp_dev_dir" "$repo_root/scripts/build-vpp-dns-intercept-plugin.sh" | tail -1)
  [ -f "$plugin" ] || { echo "DNS intercept plugin build did not produce a library" >&2; exit 1; }
  plugin_dir="$package_root/usr/lib/$(vpp_plugin_multiarch)/vpp_plugins"
  mkdir -p "$package_root/DEBIAN" "$plugin_dir"
  cp "$plugin" "$plugin_dir/ly_route_dns_intercept_plugin.so"
  chmod 0644 "$plugin_dir/ly_route_dns_intercept_plugin.so"
  cat > "$package_root/DEBIAN/control" <<EOF
Package: ly-route-vpp-dns-intercept
Version: 25.10.0+lyroute1
Section: net
Priority: optional
Architecture: $deb_arch
Maintainer: Ly Route <root@ly-route.local>
Depends: vpp (= ${LY_ROUTE_VPP_PACKAGE_VERSION:-$fdio_vpp_version})
Description: Ly Route VPP pre-NAT transparent DNS interception plugin
EOF
  mkdir -p "$out_dir"
  rm -f "$out_dir/ly-route-vpp-dns-intercept_"*"_${deb_arch}.deb"
  dpkg-deb --build --root-owner-group "$package_root" "$out_dir/ly-route-vpp-dns-intercept_25.10.0+lyroute1_${deb_arch}.deb" >/dev/null
}

build_vpp_pre_nat_route() {
  require_command cmake
  build_debs=$(mktemp -d "${TMPDIR:-/tmp}/ly-route-vpp-pre-nat-route-deps.XXXXXX")
  package_root=$(mktemp -d "${TMPDIR:-/tmp}/ly-route-vpp-pre-nat-route-package.XXXXXX")
  trap 'rm -rf "$build_debs" "$package_root"' RETURN
  vpp_dev_dir=${LY_ROUTE_VPP_DEV_DEBS_DIR:-}
  [ -n "$vpp_dev_dir" ] || vpp_dev_dir="$build_debs"
  if [ ! -f "$vpp_dev_dir/vpp-dev_"*.deb ] || [ ! -f "$vpp_dev_dir/libvppinfra-dev_"*.deb ]; then
    fetch_fdio_package vpp-dev "$build_debs"
    fetch_fdio_package libvppinfra-dev "$build_debs"
    vpp_dev_dir="$build_debs"
  fi
  plugin=$(LY_ROUTE_VPP_DEV_DEBS_DIR="$vpp_dev_dir" sh "$repo_root/scripts/build-vpp-pre-nat-route-plugin.sh" | tail -1)
  [ -f "$plugin" ] || { echo "pre-NAT route plugin build did not produce a library" >&2; exit 1; }
  plugin_dir="$package_root/usr/lib/$(vpp_plugin_multiarch)/vpp_plugins"
  mkdir -p "$package_root/DEBIAN" "$plugin_dir"
  cp "$plugin" "$plugin_dir/ly_route_pre_nat_route_plugin.so"
  chmod 0644 "$plugin_dir/ly_route_pre_nat_route_plugin.so"
  cat > "$package_root/DEBIAN/control" <<EOF
Package: ly-route-vpp-pre-nat-route
Version: 25.10.0+lyroute1
Section: net
Priority: optional
Architecture: $deb_arch
Maintainer: Ly Route <root@ly-route.local>
Depends: vpp (= ${LY_ROUTE_VPP_PACKAGE_VERSION:-$fdio_vpp_version})
Description: Ly Route VPP pre-NAT policy routing plugin
EOF
  mkdir -p "$out_dir"
  rm -f "$out_dir/ly-route-vpp-pre-nat-route_"*"_${deb_arch}.deb"
  dpkg-deb --build --root-owner-group "$package_root" "$out_dir/ly-route-vpp-pre-nat-route_25.10.0+lyroute1_${deb_arch}.deb" >/dev/null
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
  plugin_dir="$package_root/usr/lib/$(vpp_plugin_multiarch)/vpp_plugins"
  mkdir -p "$package_root/DEBIAN" "$plugin_dir"
  cp "$plugin" "$plugin_dir/ly_route_orchestrator_plugin.so"
  chmod 0644 "$plugin_dir/ly_route_orchestrator_plugin.so"
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
  vpp-pppoe-client) build_vpp_pppoe_client ;;
  vpp-smart-qos) build_vpp_smart_qos ;;
  vpp-security-guard) build_vpp_security_guard ;;
  vpp-dns-intercept) build_vpp_dns_intercept ;;
  vpp-pre-nat-route) build_vpp_pre_nat_route ;;
  vpp-orchestrator) build_vpp_orchestrator ;;
  vpp-apply) build_vpp_apply ;;
  all)
    build_smartdns
    build_dns_vpp_proxy
    build_xray
    build_vpp_apply
    build_vpp_fdio
    build_vpp_pppoe_client
    build_vpp_smart_qos
    build_vpp_security_guard
    build_vpp_dns_intercept
    build_vpp_pre_nat_route
    build_vpp_orchestrator
    ;;
  -h|--help) usage ; exit 0 ;;
  *) echo "unknown target: $target" >&2; usage >&2; exit 2 ;;
esac

rm -f "$runtime_in_progress"

printf 'Runtime packages written to %s\n' "$out_dir"
