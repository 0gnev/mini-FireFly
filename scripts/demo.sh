#!/bin/sh
# scripts/demo.sh — the interview prop (SPEC §16.3).
#
# Scripted sequence with narration:
#   1. baseline search (all providers healthy)       -> show offers + statuses
#   2. repeat identical search                        -> cache: hit
#   3. chaos-down d, loop searches                    -> d: error -> breaker_open
#   4. recover d -> stable, keep searching            -> half-open probe closes breaker
#   5. booking + idempotent replay                    -> Idempotency-Replayed: true
#
# Target runtime < 3 minutes. Assumes the stack is already up (`make up`).
# Talks to core on http://localhost:8000 and toggles chaos via scripts/chaos.sh.
set -eu

API="${API:-http://localhost:8000/api/v1}"
SCRIPT_DIR=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)

# --- pretty helpers -------------------------------------------------------
if [ -t 1 ]; then
  B=$(printf '\033[1m'); C=$(printf '\033[36m'); R=$(printf '\033[0m')
else
  B=''; C=''; R=''
fi
say()   { printf '\n%s== %s ==%s\n' "$B" "$1" "$R"; }
note()  { printf '%s%s%s\n' "$C" "$1" "$R"; }
pause() { sleep "${1:-1}"; }

# Pretty-print JSON if jq is present, else raw.
pp() { if command -v jq >/dev/null 2>&1; then jq "$@"; else cat; fi; }

SEARCH_BODY='{"origin":"BEG","destination":"AMS","depart_date":"2026-07-01","passengers":1}'

search() {
  curl -fsS -X POST "${API}/search" \
    -H 'Content-Type: application/json' \
    -d "${SEARCH_BODY}"
}

# Extract a field for terse status lines (best-effort, jq-gated).
status_line() {
  if command -v jq >/dev/null 2>&1; then
    jq -r '"cache=\(.cache) partial=\(.partial) offers=\(.offers|length) " +
           "providers={" + (.providers|to_entries|map("\(.key):\(.value.status)")|join(" ")) + "}"'
  else
    cat
  fi
}

# --------------------------------------------------------------------------
say "mini-FireFly demo  (Grafana: http://localhost:3000  Prometheus: :9090)"
note "Talking to core at ${API}"
note "Open the 'Provider Health' dashboard in Grafana to watch this live."
pause 2

# 1) Baseline -------------------------------------------------------------
say "1/5  Baseline search — all providers healthy"
search | tee /tmp/demo_search.json | pp '.'
note "Summary:"; cat /tmp/demo_search.json | status_line
pause 2

# 2) Cache hit ------------------------------------------------------------
say "2/5  Repeat the identical search — expect cache: hit (no fan-out)"
search | status_line
note "Same query within TTL is served from Redis (SPEC §10.6)."
pause 2

# 3) Take provider d down -> breaker opens --------------------------------
say "3/5  Chaos: take provider d DOWN, then loop searches"
"${SCRIPT_DIR}/chaos.sh" d down
note "Looping searches — watch d go error -> ... -> breaker_open (threshold 5)."
i=1
while [ "$i" -le 7 ]; do
  # Vary the date each iteration so we bypass the cache and actually fan out.
  body=$(printf '{"origin":"BEG","destination":"AMS","depart_date":"2026-07-%02d","passengers":1}' "$((i + 1))")
  printf '  call %s: ' "$i"
  curl -fsS -X POST "${API}/search" -H 'Content-Type: application/json' -d "${body}" \
    | { if command -v jq >/dev/null 2>&1; then jq -r '.providers.d.status'; else cat; fi; }
  i=$((i + 1))
  pause 1
done
note "Provider d should now report breaker_open — check the breaker timeline in Grafana."
note "Alert BreakerOpen fires after 1m of fanout_breaker_state==2 (SPEC §12.3)."
pause 2

# 4) Recover d -> half-open probe closes breaker --------------------------
say "4/5  Recover provider d -> stable; wait out cooldown; probe closes breaker"
"${SCRIPT_DIR}/chaos.sh" d stable
note "Cooldown is 15s (SPEC §10.1); waiting, then searching to trigger the half-open probe..."
pause 17
i=1
while [ "$i" -le 4 ]; do
  body=$(printf '{"origin":"BEG","destination":"AMS","depart_date":"2026-08-%02d","passengers":1}' "$((i + 1))")
  printf '  call %s: d=' "$i"
  curl -fsS -X POST "${API}/search" -H 'Content-Type: application/json' -d "${body}" \
    | { if command -v jq >/dev/null 2>&1; then jq -r '.providers.d.status'; else cat; fi; }
  i=$((i + 1))
  pause 2
done
note "A successful half-open probe resets the breaker to closed -> d: ok again."
pause 2

# 5) Booking + idempotent replay ------------------------------------------
say "5/5  Booking + idempotent replay"
OFFER_ID=$(cat /tmp/demo_search.json \
  | { if command -v jq >/dev/null 2>&1; then jq -r '.offers[0].offer_id // "a:00000000"'; else echo "a:00000000"; fi; })
KEY="demo-$(date +%s)"
BOOK_BODY=$(printf '{"offer_id":"%s","passenger":{"first_name":"Ilia","last_name":"K","email":"x@y.z"}}' "${OFFER_ID}")

note "First booking (Idempotency-Key: ${KEY}) -> expect 201:"
curl -fsS -D - -o /tmp/demo_book1.json -X POST "${API}/bookings" \
  -H 'Content-Type: application/json' \
  -H "Idempotency-Key: ${KEY}" \
  -d "${BOOK_BODY}" | grep -iE '^HTTP/|^Idempotency-Replayed' || true
cat /tmp/demo_book1.json | pp '.'
pause 1

note "Replay with the SAME key + body -> expect 200 + Idempotency-Replayed: true:"
curl -fsS -D - -o /tmp/demo_book2.json -X POST "${API}/bookings" \
  -H 'Content-Type: application/json' \
  -H "Idempotency-Key: ${KEY}" \
  -d "${BOOK_BODY}" | grep -iE '^HTTP/|^Idempotency-Replayed' || true
cat /tmp/demo_book2.json | pp '.'

say "Demo complete"
note "All providers back to stable. Review the dashboard for the full incident arc."
