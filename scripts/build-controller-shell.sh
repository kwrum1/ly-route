#!/usr/bin/env sh
set -eu

usage() {
  cat <<'EOF'
Usage: scripts/build-controller-shell.sh --product gateway|orchestrator --out DIRECTORY [--manifest PATH]
EOF
}

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
product=""
out_dir=""
manifest=""

while [ "$#" -gt 0 ]; do
  case "$1" in
    --product) product=${2:-}; shift 2 ;;
    --out) out_dir=${2:-}; shift 2 ;;
    --manifest) manifest=${2:-}; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) echo "Unknown argument: $1" >&2; usage >&2; exit 2 ;;
  esac
done

case "$product" in
  gateway|orchestrator) ;;
  *) echo "Unsupported product: $product" >&2; exit 2 ;;
esac
if [ -z "$out_dir" ]; then
  echo "--out is required" >&2
  exit 2
fi

canonical_manifest="$repo_root/packaging/product-profiles/$product.json"
manifest=${manifest:-$canonical_manifest}
if [ ! -f "$manifest" ]; then
  echo "Capability manifest is missing: $manifest" >&2
  exit 1
fi
node - "$manifest" "$canonical_manifest" "$product" <<'NODE'
const { readFileSync } = require("node:fs");
const [manifestPath, canonicalPath, product] = process.argv.slice(2);
let manifest;
try {
  manifest = JSON.parse(readFileSync(manifestPath, "utf8"));
} catch {
  console.error("invalid capability manifest JSON");
  process.exit(1);
}
if (manifest.product !== product) {
  console.error(`manifest product mismatch: expected ${product}, got ${manifest.product}`);
  process.exit(1);
}
if (manifestPath !== canonicalPath && readFileSync(manifestPath, "utf8") !== readFileSync(canonicalPath, "utf8")) {
  console.error(`capability manifest contents mismatch canonical ${product} profile`);
  process.exit(1);
}
NODE

source_dir="$repo_root/frontend/$product"
if [ ! -f "$source_dir/shell.js" ] || [ ! -f "$source_dir/index.html" ] || [ ! -f "$source_dir/styles.css" ]; then
  echo "$product UI source is incomplete" >&2
  exit 1
fi
rm -rf "$out_dir"
mkdir -p "$out_dir"
cp "$source_dir/index.html" "$source_dir/styles.css" "$source_dir/shell.js" "$manifest" "$out_dir/"
mv "$out_dir/$(basename "$manifest")" "$out_dir/capabilities.json"
node - "$out_dir/index.html" <<'NODE'
const { readFileSync, writeFileSync } = require("node:fs");
const path = process.argv[2];
const source = readFileSync(path, "utf8");
writeFileSync(path, source.replace(/\s*<!-- gateway-modules:start -->[\s\S]*?<!-- gateway-modules:end -->/, ""));
NODE
case "$product" in
  gateway)
    cat \
      "$source_dir/bootstrap.js" \
      "$source_dir/modules/routing.js" \
      "$source_dir/modules/modal.js" \
      "$source_dir/modules/overview.js" \
      "$source_dir/app.js" > "$out_dir/app.js"
    ;;
  orchestrator)
    cat \
      "$source_dir/modules/api.js" \
      "$source_dir/modules/model.js" \
      "$source_dir/modules/modal.js" \
      "$source_dir/modules/nic-settings.js" \
      "$source_dir/modules/group-settings.js" \
      "$source_dir/modules/policy.js" \
      "$source_dir/app.js" > "$out_dir/app.js"
    cat \
      "$source_dir/product.css" \
      "$source_dir/forms.css" \
      "$source_dir/responsive.css" > "$out_dir/product.css"
    node - "$out_dir/index.html" <<'NODE'
const { readFileSync, writeFileSync } = require("node:fs");
const path = process.argv[2];
const source = readFileSync(path, "utf8");
writeFileSync(path, source.replace("</head>", "  <link rel=\"stylesheet\" href=\"./product.css\">\n</head>"));
NODE
    ;;
esac
printf 'Built %s controller-shell bundle at %s\n' "$product" "$out_dir"
