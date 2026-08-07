#!/usr/bin/env sh
set -eu

# Pinned data release.  The control plane never downloads rule data at
# runtime; CI and image builders fetch it once, verify it, and package it.
tag=${LY_ROUTE_GEODATA_TAG:-202608052252}
out=${LY_ROUTE_GEODATA_DIR:-$(CDPATH= cd -- "$(dirname -- "$0")/../packaging/geodata" && pwd)}
source_dir=${LY_ROUTE_GEODATA_SOURCE_DIR:-}
mkdir -p "$out"

case "$tag" in
  202608052252)
    geoip_sha=6ba63d75f307d16a81ae09406ddcf2779fa75cb642d4aae59613370d62d33509
    geosite_sha=857227f9dcedbfda5c067ba740ca8a461a06a6ac12aeeb99dcbf82c0e1bdb125
    china_sha=a74d47f63d557ba6760d05c726d491f9b8a71ec4a360dd5e288e09ab480c17e8
    ;;
  *) echo "Unsupported v2ray-rules-dat release tag: $tag" >&2; exit 2 ;;
esac

fetch_one() {
  name=$1
  expected=$2
  if [ -n "$source_dir" ] && [ -f "$source_dir/$name" ]; then
    cp "$source_dir/$name" "$out/$name"
  else
    command -v curl >/dev/null 2>&1 || { echo "curl is required to fetch geodata" >&2; exit 1; }
    curl --fail --location --retry 4 --retry-delay 2 --silent --show-error \
      "https://github.com/Loyalsoldier/v2ray-rules-dat/releases/download/$tag/$name" \
      -o "$out/$name"
  fi
  actual=$(sha256sum "$out/$name" | awk '{print $1}')
  [ "$actual" = "$expected" ] || { echo "$name sha256 mismatch: $actual != $expected" >&2; exit 1; }
}

fetch_one geoip.dat "$geoip_sha"
fetch_one geosite.dat "$geosite_sha"
fetch_one china-list.txt "$china_sha"
cat >"$out/manifest.json" <<EOF
{
  "provider": "Loyalsoldier/v2ray-rules-dat",
  "tag": "$tag",
  "files": {
    "geoip.dat": "$geoip_sha",
    "geosite.dat": "$geosite_sha",
    "china-list.txt": "$china_sha"
  }
}
EOF
printf 'Verified geodata in %s\n' "$out"
