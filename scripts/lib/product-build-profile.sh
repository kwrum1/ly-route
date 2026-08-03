#!/usr/bin/env sh

product_build_fail() {
  echo "$*" >&2
  exit 1
}

product_build_require_file() {
  [ -f "$1" ] || product_build_fail "Required file missing: $1"
}

product_build_json_field() {
  node - "$PRODUCT_BUILD_PROFILE" "$1" <<'NODE'
const { readFileSync } = require("node:fs");
const [path, field] = process.argv.slice(2);
const value = JSON.parse(readFileSync(path, "utf8"))[field];
if (Array.isArray(value)) process.stdout.write(value.join(","));
else if (typeof value === "string") process.stdout.write(value);
else throw new Error(`unsupported build profile field ${field}`);
NODE
}

load_product_build_profile() {
  repo_root=$1
  product=$2
  selected_manifest=$3
  command -v node >/dev/null 2>&1 || product_build_fail "node is required to validate product build profiles"
  PRODUCT_BUILD_PROFILE="$repo_root/packaging/build-profiles/$product.json"
  PRODUCT_MANIFEST="$repo_root/packaging/product-profiles/$product.json"
  selected_manifest=${selected_manifest:-$PRODUCT_MANIFEST}
  product_build_require_file "$PRODUCT_BUILD_PROFILE"
  product_build_require_file "$PRODUCT_MANIFEST"
  product_build_require_file "$selected_manifest"

  node - "$PRODUCT_BUILD_PROFILE" "$PRODUCT_MANIFEST" "$selected_manifest" "$product" <<'NODE'
const { readFileSync } = require("node:fs");
const [buildPath, canonicalPath, selectedPath, product] = process.argv.slice(2);
const readJSON = (path, label) => {
  try { return JSON.parse(readFileSync(path, "utf8")); }
  catch { console.error(`${label} must be valid JSON: ${path}`); process.exit(1); }
};
const build = readJSON(buildPath, "Build profile");
const canonical = readJSON(canonicalPath, "Canonical product manifest");
const selected = readJSON(selectedPath, "Product manifest");
const requiredStrings = ["control_profile", "frontend_product", "default_config", "database_path", "config_path"];
const requiredArrays = ["services", "systemd_units", "required_packages", "required_commands", "required_units"];
if (build.schema_version !== 1 || build.product !== product || canonical.schema_version !== 1 || canonical.product !== product) {
  console.error(`Invalid ${product} build profile identity`);
  process.exit(1);
}
for (const field of requiredStrings) {
  if (typeof build[field] !== "string" || build[field].length === 0) {
    console.error(`Invalid ${product} build profile field: ${field}`);
    process.exit(1);
  }
}
for (const field of requiredArrays) {
  if (!Array.isArray(build[field]) || build[field].some((item) => typeof item !== "string" || item.length === 0)) {
    console.error(`Invalid ${product} build profile field: ${field}`);
    process.exit(1);
  }
}
if (build.control_profile !== `packaging/product-profiles/${product}.json` || build.frontend_product !== product ||
    JSON.stringify(build.services) !== JSON.stringify(canonical.services)) {
  console.error(`Build profile does not match canonical ${product} services`);
  process.exit(1);
}
if (JSON.stringify(selected) !== JSON.stringify(canonical)) {
  console.error(`Product manifest mismatch: expected ${product} canonical profile`);
  process.exit(1);
}
NODE

  PRODUCT_FRONTEND_PRODUCT=$(product_build_json_field frontend_product)
  PRODUCT_DEFAULT_CONFIG="$repo_root/$(product_build_json_field default_config)"
  PRODUCT_DATABASE_PATH=$(product_build_json_field database_path)
  PRODUCT_CONFIG_PATH=$(product_build_json_field config_path)
  PRODUCT_SERVICES=$(product_build_json_field services)
  PRODUCT_SYSTEMD_UNITS=$(product_build_json_field systemd_units)
  PRODUCT_REQUIRED_PACKAGES=$(product_build_json_field required_packages)
  PRODUCT_REQUIRED_COMMANDS=$(product_build_json_field required_commands)
  PRODUCT_REQUIRED_UNITS=$(product_build_json_field required_units)
  product_build_require_file "$PRODUCT_DEFAULT_CONFIG"
}

validate_frontend_bundle() {
  bundle=$1
  product=$2
  [ -d "$bundle" ] || product_build_fail "Frontend bundle is missing: $bundle"
  for name in index.html styles.css shell.js app.js capabilities.json; do
    product_build_require_file "$bundle/$name"
  done
  node - "$bundle" "$PRODUCT_MANIFEST" "$product" <<'NODE'
const { readFileSync, readdirSync } = require("node:fs");
const [bundlePath, canonicalPath, product] = process.argv.slice(2);
const bundleManifestPath = `${bundlePath}/capabilities.json`;
const appPath = `${bundlePath}/app.js`;
let bundle;
try { bundle = JSON.parse(readFileSync(bundleManifestPath, "utf8")); }
catch { console.error("Frontend bundle capabilities must be valid JSON"); process.exit(1); }
const canonical = JSON.parse(readFileSync(canonicalPath, "utf8"));
const marker = `window.LY_ROUTE_PRODUCT_ENTRYPOINT = "${product}";`;
const expectedFiles = ["app.js", "capabilities.json", "index.html", "shell.js", "styles.css"];
if (product === "orchestrator") expectedFiles.push("product.css");
expectedFiles.sort();
const actualFiles = readdirSync(bundlePath, { withFileTypes: true })
  .filter((entry) => entry.isFile()).map((entry) => entry.name).sort();
if (bundle.product !== product || JSON.stringify(bundle) !== JSON.stringify(canonical) ||
    !readFileSync(appPath, "utf8").includes(marker) || JSON.stringify(actualFiles) !== JSON.stringify(expectedFiles)) {
  console.error(`Frontend bundle product mismatch: expected ${product}`);
  process.exit(1);
}
NODE
}

validate_prebuilt_control() {
  binary=$1
  product=$2
  [ -n "$binary" ] || return 0
  product_build_require_file "$binary"
  [ "${LY_ROUTE_CONTROL_PRODUCT:-}" = "$product" ] || \
    product_build_fail "Prebuilt control product mismatch: expected $product"
}

validate_prebuilt_vpp_apply() {
  binary=$1
  [ -n "$binary" ] || return 0
  product_build_require_file "$binary"
}

validate_upgrade_base_manifest() {
  manifest=$1
  product=$2
  [ -n "$manifest" ] || return 0
  product_build_require_file "$manifest"
  node - "$manifest" "$PRODUCT_MANIFEST" "$product" <<'NODE'
const { readFileSync } = require("node:fs");
const [manifestPath, canonicalPath, product] = process.argv.slice(2);
let manifest;
try { manifest = JSON.parse(readFileSync(manifestPath, "utf8")); }
catch { console.error(`Upgrade base manifest must be valid JSON: ${manifestPath}`); process.exit(1); }
if (manifest.product !== product) {
  console.error(`Upgrade base product mismatch: expected ${product}, got ${String(manifest.product)}`);
  process.exit(1);
}
const canonical = JSON.parse(readFileSync(canonicalPath, "utf8"));
if (manifest.services && JSON.stringify(manifest.services) !== JSON.stringify(canonical.services)) {
  console.error(`Upgrade base manifest services mismatch: expected ${product} canonical profile`);
  process.exit(1);
}
if (manifest.resources && JSON.stringify(manifest.resources) !== JSON.stringify(canonical.resources)) {
  console.error(`Upgrade base manifest resources mismatch: expected ${product} canonical profile`);
  process.exit(1);
}
NODE
}

copy_product_frontend() (
  frontend_target=$1
  frontend_bundle=$2
  frontend_product=$3
  if [ -n "$frontend_bundle" ]; then
    rm -rf "$frontend_target"
    mkdir -p "$frontend_target"
    cp -a "$frontend_bundle/." "$frontend_target/"
  else
    "$repo_root/scripts/build-controller-shell.sh" --product "$frontend_product" --out "$frontend_target"
  fi
)

product_source_commit() {
  if [ -n "${GITHUB_SHA:-}" ]; then
    printf '%s' "$GITHUB_SHA"
  else
    git -C "$repo_root" rev-parse --short=12 HEAD 2>/dev/null || printf unknown
  fi
}

product_source_date_epoch() {
  if [ -n "${SOURCE_DATE_EPOCH:-}" ]; then
    case "$SOURCE_DATE_EPOCH" in *[!0-9]*) product_build_fail "SOURCE_DATE_EPOCH must be a non-negative integer" ;; esac
    printf '%s' "$SOURCE_DATE_EPOCH"
  else
    git -C "$repo_root" log -1 --format=%ct 2>/dev/null || printf 0
  fi
}

write_product_artifact_manifest() (
  output=$1
  artifact_type=$2
  artifact_name=$3
  product=$4
  suite=$5
  arch=$6
  source_commit=$7
  mkdir -p "$(dirname "$output")"
  node - "$PRODUCT_BUILD_PROFILE" "$output" "$artifact_type" "$artifact_name" "$product" "$suite" "$arch" "$source_commit" <<'NODE'
const { readFileSync, writeFileSync } = require("node:fs");
const [profilePath, outputPath, artifactType, artifactName, product, suite, arch, sourceCommit] = process.argv.slice(2);
const profile = JSON.parse(readFileSync(profilePath, "utf8"));
const manifest = {
  schema_version: 1,
  artifact_type: artifactType,
  artifact_name: artifactName,
  product,
  suite,
  arch,
  source_commit: sourceCommit,
  control_profile: "/etc/ly-route/product-manifest.json",
  frontend_bundle: "/opt/ly-route/admin",
  frontend_product: profile.frontend_product,
  database_path: profile.database_path,
  config_path: profile.config_path,
  services: profile.services,
  systemd_units: profile.systemd_units,
  packages: profile.required_packages,
};
writeFileSync(outputPath, `${JSON.stringify(manifest, null, 2)}\n`);
NODE
)

create_deterministic_tar() (
  root=$1
  artifact=$2
  epoch=$3
  (cd "$root" && tar --sort=name --mtime="@$epoch" --owner=0 --group=0 --numeric-owner --format=gnu -cpf "$artifact" .)
)

write_artifact_checksum() (
  artifact=$1
  artifact_dir=$(dirname "$artifact")
  artifact_name=$(basename "$artifact")
  (cd "$artifact_dir" && sha256sum "$artifact_name" >"$artifact_name.sha256")
)
