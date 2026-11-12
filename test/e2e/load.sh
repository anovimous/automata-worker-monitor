#!/usr/bin/env bash
# Load check of the monitor: 50 fakeworkers on one host, then the CPU and the memory
# of the worker machine controller (and of the workers control plane) sampled with docker stats. The stack is the one
# of the end-to-end test (compose.yml); everything it creates is removed at the end.
#
#   test/e2e/load.sh                         # or: make load
#   LOAD_WORKERS=50 LOAD_MINUTES=5 test/e2e/load.sh
set -uo pipefail

cd "$(dirname "$0")"
export E2E_VERSION=${E2E_VERSION:-dev}
CP=http://127.0.0.1:19690
NETWORK=amon-e2e-net
CONTROLLER=amon-e2e-worker-machine-controller
CONTROL_PLANE=amon-e2e-workers-control-plane
WORKERS=${LOAD_WORKERS:-50}
MINUTES=${LOAD_MINUTES:-5}
START=$(date +%s)

log() { printf '%5ss  %s\n' "$(($(date +%s) - START))" "$*"; }

for tool in docker curl jq awk; do
  command -v "$tool" >/dev/null || { echo "load: $tool is required" >&2; exit 2; }
done

teardown() {
  docker compose down -v --remove-orphans >/dev/null 2>&1
  docker ps -aq --filter name=automata-worker-w-e2e- | xargs -r docker rm -f >/dev/null
  docker volume ls -q | grep -E '^automata-(wl|tmp)-w-e2e-' | xargs -r docker volume rm >/dev/null
  docker network rm "$NETWORK" >/dev/null 2>&1
}
trap teardown EXIT

docker network create "$NETWORK" >/dev/null
docker compose up -d --quiet-pull >/dev/null 2>&1 || exit 1
for _ in $(seq 30); do curl -sf -o /dev/null "$CP/healthz" && break; sleep 2; done

template=$(curl -s -X POST "$CP/api/v1/templates" -H 'Content-Type: application/json' -d '{
  "name": "load-normal", "description": "load check", "image": "automata-fakeworker:dev", "pullPolicy": "never",
  "envFile": "default", "env": {"FAKE_SCENARIO": "normal", "FAKE_JOB_SECONDS": "60"}, "network": "amon-e2e-net",
  "extraHosts": [], "cpus": 0.2, "memoryBytes": 268435456, "pidsLimit": 64, "wordlistCacheMaxBytes": 67108864,
  "tmpfsSocketsBytes": 16777216, "restartMaxRetries": 5, "stopTimeoutSeconds": 45, "drainTimeoutSeconds": 0,
  "escalateAfterDrainTimeout": false, "removeAfterDrain": false, "extraLabels": {}}' | jq -r .id)
left=$WORKERS
while [ "$left" -gt 0 ]; do
  n=$((left > 20 ? 20 : left))
  curl -s -X POST "$CP/api/v1/workers" -H 'Content-Type: application/json' \
    -d "{\"host\": \"e2e\", \"templateId\": $template, \"count\": $n}" | jq -e '[.workers[] | select(.ok)] | length' >/dev/null
  left=$((left - n))
done
log "created $WORKERS workers"

healthy() { curl -s "$CP/api/v1/workers" | jq '[.items[] | select(.health == "HEALTHY")] | length'; }
for _ in $(seq 90); do [ "$(healthy)" -ge "$WORKERS" ] && break; sleep 2; done
log "$(healthy) of $WORKERS workers HEALTHY; sampling for $MINUTES min"

# docker stats: CPU in % of one core, memory as "12.3MiB / 15.5GiB"
samples=$(mktemp)
end=$(($(date +%s) + MINUTES * 60))
while [ "$(date +%s)" -lt "$end" ]; do
  docker stats --no-stream --format '{{.Name}} {{.CPUPerc}} {{.MemUsage}}' "$CONTROLLER" "$CONTROL_PLANE" >>"$samples"
  sleep 5
done
log "$(healthy) of $WORKERS workers HEALTHY at the end"

report() { # container
  grep "^$1 " "$samples" | awk -v name="$2" '
    function mib(s,   v) { v = s + 0; if (s ~ /GiB/) v *= 1024; else if (s ~ /KiB/) v /= 1024; else if (s ~ /[0-9]B$/ && s !~ /iB/) v /= 1048576; return v }
    { cpu = $2 + 0; mem = mib($3); n++; sc += cpu; sm += mem; if (cpu > mc) mc = cpu; if (mem > mm) mm = mem }
    END { printf "%-28s CPU avg %.2f %% max %.2f %% of one core; memory avg %.1f MiB max %.1f MiB (%d samples)\n", name, sc / n, mc, sm / n, mm, n }'
}
report "$CONTROLLER" "worker machine controller"
report "$CONTROL_PLANE" "workers control plane"
echo "budget: worker machine controller < 2 % of one core and < 100 MB for 50 workers"
rm -f "$samples"
