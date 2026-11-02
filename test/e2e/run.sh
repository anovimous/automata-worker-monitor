#!/usr/bin/env bash
# End-to-end test of the monitor (plan/11 P7): a worker machine controller and the workers control plane in compose,
# fakeworkers created through the API, the scenarios of P4 and the two cases fixed in P5 checked with curl and jq.
# Needs Docker, curl, jq and the images of `make images` (E2E_VERSION, default dev). Everything it creates is removed
# at the end, also on a failure; its workers are w-e2e-…, so the workers of another stack on this machine are left alone.
#
#   test/e2e/run.sh            # or: make e2e
#   E2E_KEEP=1 test/e2e/run.sh # leave the stack up after the run, to look at it (http://127.0.0.1:19690)
set -uo pipefail

cd "$(dirname "$0")"
export E2E_VERSION=${E2E_VERSION:-dev}
CP=http://127.0.0.1:19690
NETWORK=amon-e2e-net
CONTROLLER=amon-e2e-worker-machine-controller
CONTROL_PLANE=amon-e2e-workers-control-plane
LOGS=$(mktemp -d -t amon-e2e-XXXXXX)
START=$(date +%s)
PASSED=0
FAILED=0

log() { printf '%5ss  %s\n' "$(($(date +%s) - START))" "$*"; }
pass() { PASSED=$((PASSED + 1)); log "PASS  $*"; }
fail() { FAILED=$((FAILED + 1)); log "FAIL  $*"; }

for tool in docker curl jq; do
  command -v "$tool" >/dev/null || { echo "e2e: $tool is required" >&2; exit 2; }
done
for image in automata-worker-machine-controller:$E2E_VERSION automata-workers-control-plane:$E2E_VERSION automata-fakeworker:dev; do
  docker image inspect "$image" >/dev/null 2>&1 || { echo "e2e: no image $image, run make images" >&2; exit 2; }
done

# --- the API ---------------------------------------------------------------------------------------------------------

get() { curl -s --max-time 10 "$CP/api/v1$1"; }
code() { curl -s -o /dev/null -w '%{http_code}' --max-time 10 "$CP/api/v1$1"; }
post() { # path [body, default {}]
  local body='{}'
  [ $# -ge 2 ] && body=$2
  curl -s --max-time 120 -X POST "$CP/api/v1$1" -H 'Content-Type: application/json' -d "$body"
}
worker() { get "/workers/$1" | jq -c '.worker // empty'; }
field() { worker "$1" | jq -r "$2"; }

# until waits until a command succeeds, every 2 s, at most $1 seconds.
until_ok() {
  local timeout=$1
  shift
  local deadline=$(($(date +%s) + timeout))
  while ! "$@" >/dev/null 2>&1; do
    [ "$(date +%s)" -ge "$deadline" ] && return 1
    sleep 2
  done
}

is() { [ "$(field "$1" "$2")" = "$3" ]; }
gone() { [ "$(code "/workers/$1")" = 404 ]; }
container_gone() { ! docker container inspect "automata-worker-$1" >/dev/null 2>&1; }
has_event() { get "/workers/$1/events?limit=100" | jq -e --arg k "$2" 'any(.[]; .kind == $k)' >/dev/null; }
healthz() { [ "$(curl -s -o /dev/null -w '%{http_code}' --max-time 5 "$CP/healthz")" = 200 ]; }

template() { # name scenario [removeAfterDrain]
  local answer
  answer=$(post /templates "$(jq -nc --arg name "$1" --arg sc "$2" --argjson rad "${3:-false}" '{
    name: $name, description: ("e2e, scenario " + $sc), image: "automata-fakeworker:dev", pullPolicy: "never",
    envFile: "default", env: {FAKE_SCENARIO: $sc, FAKE_JOB_SECONDS: "20"}, network: "amon-e2e-net", extraHosts: [],
    cpus: 0.25, memoryBytes: 268435456, pidsLimit: 64, wordlistCacheMaxBytes: 67108864, tmpfsSocketsBytes: 16777216,
    restartMaxRetries: 5, stopTimeoutSeconds: 45, drainTimeoutSeconds: 0, escalateAfterDrainTimeout: false,
    removeAfterDrain: $rad, extraLabels: {}}')")
  jq -er .id <<<"$answer" 2>/dev/null || { echo "e2e: template $1 not created: $answer" >&2; exit 1; }
}

create() { # template id, count: prints the new worker ids
  post /workers "$(jq -nc --argjson t "$1" --argjson n "$2" '{host: "e2e", templateId: $t, count: $n}')" | jq -r '.workers[] | select(.ok) | .id'
}

# --- setup and teardown ----------------------------------------------------------------------------------------------

teardown() {
  docker compose logs --no-color >"$LOGS/compose.log" 2>&1
  if [ -n "${E2E_KEEP:-}" ]; then
    log "the stack is left up (E2E_KEEP): $CP; logs in $LOGS"
    return
  fi
  docker compose down -v --remove-orphans >/dev/null 2>&1
  # the test's workers and their volumes: only w-e2e-…
  docker ps -aq --filter name=automata-worker-w-e2e- | xargs -r docker rm -f >/dev/null
  docker volume ls -q | grep -E '^automata-(wl|tmp)-w-e2e-' | xargs -r docker volume rm >/dev/null
  docker network rm "$NETWORK" >/dev/null 2>&1
}
trap teardown EXIT

docker network create "$NETWORK" >/dev/null
docker compose up -d --quiet-pull >"$LOGS/up.log" 2>&1 || { cat "$LOGS/up.log"; exit 1; }
until_ok 60 healthz || { echo "e2e: the workers control plane did not become healthy" >&2; docker compose logs; exit 1; }
log "stack up (images $E2E_VERSION), logs in $LOGS"

# a failed template create ends the run (exit in a command substitution only ends the subshell)
T_NORMAL=$(template e2e-normal normal) || exit 1
T_DRAIN_REMOVE=$(template e2e-drain-remove normal true) || exit 1
T_STUCK=$(template e2e-stuck stuck) || exit 1
T_NO_STATUS=$(template e2e-no-status no-status) || exit 1
T_CRASH=$(template e2e-crash crash) || exit 1
T_SLOW=$(template e2e-slow-status slow-status) || exit 1

mapfile -t NORMAL < <(create "$T_NORMAL" 3)
CREATED=$(date +%s)
DRAIN_REMOVE=$(create "$T_DRAIN_REMOVE" 1)
STUCK=$(create "$T_STUCK" 1)
NO_STATUS=$(create "$T_NO_STATUS" 1)
CRASH=$(create "$T_CRASH" 1)
SLOW=$(create "$T_SLOW" 1)
log "created ${NORMAL[*]} $DRAIN_REMOVE $STUCK $NO_STATUS $CRASH $SLOW"
[ "${#NORMAL[@]}" = 3 ] && [ -n "$DRAIN_REMOVE$STUCK$NO_STATUS$CRASH$SLOW" ] || { echo "e2e: the workers were not created" >&2; exit 1; }

# --- the scenarios ---------------------------------------------------------------------------------------------------

# 1. three workers ACTIVE and HEALTHY within 15 s of their creation
healthy_all() { for id in "${NORMAL[@]}"; do is "$id" '.lifecycle + "/" + .health' ACTIVE/HEALTHY || return 1; done; }
if until_ok 15 healthy_all; then pass "3 workers ACTIVE/HEALTHY in $(($(date +%s) - CREATED)) s"; else fail "3 workers ACTIVE/HEALTHY within 15 s"; fi

# the drains run while the other scenarios are checked
post "/workers/${NORMAL[0]}/actions/drain" >/dev/null
until_ok 15 is "$DRAIN_REMOVE" .health HEALTHY && post "/workers/$DRAIN_REMOVE/actions/drain" >/dev/null

# 2. a container removed by hand is DISAPPEARED, then gone from the fleet
docker rm -f "automata-worker-${NORMAL[1]}" >/dev/null
if until_ok 20 gone "${NORMAL[1]}" && has_event "${NORMAL[1]}" DISAPPEARED; then pass "a container removed by hand: DISAPPEARED, REMOVED"; else fail "a container removed by hand: $(worker "${NORMAL[1]}")"; fi

# 3. no telemetry: UNRESPONSIVE once the starting grace (20 s) is over, before the provision timeout (40 s)
if until_ok 45 is "$NO_STATUS" .health UNRESPONSIVE; then pass "no-status: UNRESPONSIVE ($(field "$NO_STATUS" '.healthReasons | join(",")'))"; else fail "no-status: $(worker "$NO_STATUS" | jq -c '{lifecycle, health, healthReasons}')"; fi

# 4. a stuck loop: STUCK within its phase budget (30 s) + 10 s after the job started
if until_ok 90 is "$STUCK" .health STUCK && [ "$(field "$STUCK" '.healthReasons[0]')" = NO_PROGRESS ]; then pass "stuck: STUCK (NO_PROGRESS)"; else fail "stuck: $(worker "$STUCK" | jq -c '{health, healthReasons, healthMessage}')"; fi

# 5. drain: STOPPED with the reason DRAINED; with removeAfterDrain the worker is removed
if until_ok 90 is "${NORMAL[0]}" '.lifecycle + "/" + .lifecycleReason' STOPPED/DRAINED; then pass "drain: STOPPED (DRAINED)"; else fail "drain: $(worker "${NORMAL[0]}" | jq -c '{lifecycle, lifecycleReason}')"; fi
if until_ok 90 gone "$DRAIN_REMOVE" && until_ok 20 container_gone "$DRAIN_REMOVE"; then pass "drain with removeAfterDrain: REMOVED, container gone"; else fail "drain with removeAfterDrain: $(worker "$DRAIN_REMOVE" | jq -c '{lifecycle, lifecycleReason}')"; fi

# 6. (P5) a failed provision whose container still runs is removed: the remove stops it first
if until_ok 60 is "$SLOW" .lifecycle PROVISION_FAILED && [ "$(docker inspect -f '{{.State.Running}}' "automata-worker-$SLOW")" = true ] &&
  [ "$(field "$SLOW" '.allowedActions | join(",")')" = remove ]; then
  answer=$(post "/workers/$SLOW/actions/remove" '{"removeVolumes": true}')
  if until_ok 20 gone "$SLOW" && container_gone "$SLOW"; then pass "(P5) a running PROVISION_FAILED worker is removed"; else fail "(P5) remove of a failed provision: $answer"; fi
else
  fail "(P5) slow-status: $(worker "$SLOW" | jq -c '{lifecycle, health, allowedActions}')"
fi

# 7. the worker machine controller stops: its workers are UNKNOWN after 15 s (never DOWN), and back when it returns
docker stop "$CONTROLLER" >/dev/null
stopped_at=$(date +%s)
if until_ok 30 is "${NORMAL[2]}" .health UNKNOWN; then pass "controller stopped: UNKNOWN after $(($(date +%s) - stopped_at)) s"; else fail "controller stopped: $(field "${NORMAL[2]}" .health)"; fi
docker start "$CONTROLLER" >/dev/null
if until_ok 30 is "${NORMAL[2]}" .health HEALTHY; then pass "controller back: HEALTHY"; else fail "controller back: $(field "${NORMAL[2]}" .health)"; fi

# 8. a crash: Docker restarts it (CONTAINER_RESTARTED, RESTARTED_RECENTLY), and DOWN once it stops restarting it
if until_ok 120 has_event "$CRASH" CONTAINER_RESTARTED && until_ok 20 is "$CRASH" '.healthReasons | index("RESTARTED_RECENTLY") != null' true; then
  pass "crash: CONTAINER_RESTARTED, RESTARTED_RECENTLY"
else
  fail "crash restarted: $(worker "$CRASH" | jq -c '{health, healthReasons, restartCount}')"
fi
if until_ok 360 is "$CRASH" '.health + "/" + (.healthReasons | join(","))' DOWN/CONTAINER_EXITED; then pass "crash: DOWN (CONTAINER_EXITED) after $(field "$CRASH" .restartCount) restarts"; else fail "crash down: $(worker "$CRASH" | jq -c '{health, healthReasons, restartCount}')"; fi

# 9. (P5) the crashed worker is stopped or restarted, not killed or drained, then removed
actions=$(field "$CRASH" '.allowedActions | join(",")')
refused=$(post "/workers/$CRASH/actions/kill" | jq -r '.error.code // "accepted"')
if [ "$actions" = stop,restart ] && [ "$refused" = INVALID_TRANSITION ]; then pass "(P5) a crashed worker offers stop,restart; kill is refused"; else fail "(P5) crashed worker: actions $actions, kill $refused"; fi
post "/workers/$CRASH/actions/stop" >/dev/null
if until_ok 20 is "$CRASH" .lifecycle STOPPED; then
  post "/workers/$CRASH/actions/remove" '{"removeVolumes": true}' >/dev/null
  if until_ok 20 gone "$CRASH" && container_gone "$CRASH"; then pass "(P5) the crashed worker is stopped, then removed"; else fail "(P5) remove of the crashed worker"; fi
else
  fail "(P5) stop of the crashed worker: $(field "$CRASH" .lifecycle)"
fi

# 10. the workers control plane restarts: the same picture after one poll
picture() { get "/workers" | jq -c '[.items[] | {id, lifecycle, health}] | sort_by(.id)'; }
before=$(picture)
docker restart "$CONTROL_PLANE" >/dev/null
until_ok 60 healthz
sleep 6
after=$(picture)
if [ "$before" = "$after" ] && [ "$(jq length <<<"$after")" -gt 0 ]; then pass "control plane restarted: the same picture ($(jq length <<<"$after") workers)"; else fail "control plane restarted: before $before after $after"; fi

# 11. a request from another site is refused
origin=$(curl -s -o /dev/null -w '%{http_code}' -X POST -H 'Origin: http://evil.example' -H 'Content-Type: application/json' -d '{}' "$CP/api/v1/workers/${NORMAL[2]}/actions/drain")
if [ "$origin" = 403 ] && is "${NORMAL[2]}" .lifecycle ACTIVE; then pass "a foreign Origin is refused (403)"; else fail "foreign Origin: $origin"; fi

log "$PASSED passed, $FAILED failed"
[ "$FAILED" = 0 ]
