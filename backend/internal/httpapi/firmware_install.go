package httpapi

import "strconv"

type firmwareInstallInvocation struct {
	Name string
	Args []string
}

const firmwareUpgradeInstallScript = `
set -eu
exec >>/var/log/ly-route-firmware-install.log 2>&1
package_path=$1
package_hash=$2
target_dir=$3
reboot_requested=$4
actual=$(sha256sum -- "$package_path" | cut -d' ' -f1)
test "$actual" = "$package_hash"
tmp=$(mktemp -d /var/lib/ly-route/firmware-update/install.XXXXXX)
trap 'rm -rf "$tmp"' EXIT
zstd -dc -- "$package_path" | tar -x -C "$tmp"
test -f "$tmp/manifest.json"
test -f "$tmp/checksums.sha256"
grep -q '"package_type": "ly-route-upgrade"' "$tmp/manifest.json"
(cd "$tmp" && sha256sum -c checksums.sha256)
test -x "$tmp/usr/lib/ly-route/ly-route-control"
test -x "$tmp/usr/lib/ly-route/vpp-apply"
install -d -m 0755 "$target_dir"
install -m 0755 "$tmp/usr/lib/ly-route/ly-route-control" "$target_dir/ly-route-control"
install -m 0755 "$tmp/usr/lib/ly-route/vpp-apply" "$target_dir/vpp-apply"
if [ -d "$tmp/opt/ly-route/admin" ]; then
  rm -rf /opt/ly-route/admin.new
  mkdir -p /opt/ly-route/admin.new
  cp -a "$tmp/opt/ly-route/admin/." /opt/ly-route/admin.new/
  rm -rf /opt/ly-route/admin
  mv /opt/ly-route/admin.new /opt/ly-route/admin
fi
if [ -f "$tmp/etc/nginx/conf.d/ly-route-admin.conf" ]; then
  install -D -m 0644 "$tmp/etc/nginx/conf.d/ly-route-admin.conf" /etc/nginx/conf.d/ly-route-admin.conf
fi
systemctl daemon-reload || true
systemctl restart ly-route-control-api.service
systemctl reload nginx.service || systemctl restart nginx.service || true
if [ "$reboot_requested" = true ]; then
  reboot -f
fi
`

func firmwareUpgradeInstallInvocation(packagePath, packageHash, targetDir string, reboot bool) firmwareInstallInvocation {
	return firmwareInstallInvocation{
		Name: "bash",
		Args: []string{"-c", firmwareUpgradeInstallScript, "ly-route-upgrade-installer", packagePath, packageHash, targetDir, strconv.FormatBool(reboot)},
	}
}
