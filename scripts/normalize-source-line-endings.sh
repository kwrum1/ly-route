#!/usr/bin/env bash
set -euo pipefail

root=${1:-.}
root=$(CDPATH= cd -- "$root" && pwd)

# Only source/config/documentation extensions are touched. Binary artifacts,
# archives, databases, images, and downloaded third-party trees are excluded.
find "$root" \
  \( -path '*/.git' -o -path '*/.git/*' -o -path '*/dist' -o -path '*/dist/*' \
     -o -path '*/runtime-debs' -o -path '*/runtime-debs/*' -o -path '*/tmp' -o -path '*/tmp/*' \
     -o -path '*/vpp-master' -o -path '*/vpp-master/*' \) -prune -o \
  -type f \( -name '*.c' -o -name '*.cc' -o -name '*.cpp' -o -name '*.h' \
     -o -name '*.go' -o -name '*.mod' -o -name '*.sum' -o -name '*.js' -o -name '*.mjs' \
     -o -name '*.css' -o -name '*.html' -o -name '*.json' -o -name '*.md' -o -name '*.py' \
     -o -name '*.sh' -o -name '*.service' -o -name '*.conf' -o -name '*.env' \
     -o -name '*.yaml' -o -name '*.yml' -o -name '.gitignore' \
     -o -name '.gitattributes' -o -name 'Makefile' -o -name 'Dockerfile' \) -print0 |
while IFS= read -r -d '' file; do
  # sed removes the CR from CRLF while preserving file contents and mode.
  sed -i 's/\r$//' "$file"
done
