#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
artifact_dir=${LY_ROUTE_ORCHESTRATOR_DEMO_ARTIFACT_DIR:-"$repo_root/deploy/orchestrator-demo/artifacts"}

rm -rf "$artifact_dir"
mkdir -p "$artifact_dir"
"$repo_root/scripts/build-controller-shell.sh" --product orchestrator --out "$artifact_dir/admin"
(cd "$repo_root/backend" && CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o "$artifact_dir/ly-route-orchestrator" ./cmd/orchestrator-control)
chmod 0755 "$artifact_dir/ly-route-orchestrator"
docker compose -f "$repo_root/deploy/orchestrator-demo/docker-compose.yml" build
