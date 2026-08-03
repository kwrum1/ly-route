#!/usr/bin/env sh
set -eu

product=${1:?usage: test-router-lab-topology.sh gateway|orchestrator}
case "$product" in
  gateway|orchestrator) ;;
  *) exit 2 ;;
esac
repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
compose="$repo_root/deploy/$product-demo/docker-compose.yml"
container="ly-route-$product-demo"
docker compose -f "$compose" config >/dev/null
test "$(docker inspect -f '{{.State.Running}}' "$container" 2>/dev/null || true)" = true
interfaces=$(docker exec "$container" sh -c "ip -o link show | awk -F': ' '/^[0-9]+: eth[0-9]+/{count++} END{print count+0}'")
test "$interfaces" -ge 7
for network in mgmt lan wan chain1-lan chain1-wan chain2-lan chain2-wan; do
  docker inspect "$container" --format '{{json .NetworkSettings.Networks}}' | grep -F "\"$network\"" >/dev/null
done
docker exec "$container" sh -c 'curl -fsS http://127.0.0.1:8080/api/v1/health >/dev/null'
printf 'router lab topology passed: %s interfaces=%s networks=7\n' "$container" "$interfaces"
