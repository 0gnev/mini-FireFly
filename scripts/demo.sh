#!/bin/sh
# scripts/demo.sh - guided, self-checking resilience walkthrough.
#
# Requires a running stack (`make up`), curl, and jq.
set -eu

API="${API:-http://localhost:8000/api/v1}"
API="${API%/}"
SCRIPT_DIR=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
TMP_BASE="${TMPDIR:-/tmp}/mini-firefly-demo-$$"
mkdir -p "$TMP_BASE"

if [ -t 1 ]; then
  B=$(printf '\033[1m')
  C=$(printf '\033[36m')
  G=$(printf '\033[32m')
  R=$(printf '\033[0m')
else
  B=''
  C=''
  G=''
  R=''
fi

say() { printf '\n%s== %s ==%s\n' "$B" "$1" "$R"; }
note() { printf '%s%s%s\n' "$C" "$1" "$R"; }
pass() { printf '%s[ok]%s %s\n' "$G" "$R" "$1"; }
fail() { printf '\n[error] %s\n' "$1" >&2; exit 1; }

cleanup() {
  "${SCRIPT_DIR}/chaos.sh" a stable >/dev/null 2>&1 || true
  "${SCRIPT_DIR}/chaos.sh" b stable >/dev/null 2>&1 || true
  "${SCRIPT_DIR}/chaos.sh" c stable >/dev/null 2>&1 || true
  "${SCRIPT_DIR}/chaos.sh" d stable >/dev/null 2>&1 || true
  rm -rf "$TMP_BASE"
}
trap cleanup EXIT INT TERM

need() {
  command -v "$1" >/dev/null 2>&1 || fail "missing required command: $1"
}

future_date() {
  days="$1"
  if date -u -v+"${days}"d +%F >/dev/null 2>&1; then
    date -u -v+"${days}"d +%F
  else
    date -u -d "+${days} days" +%F
  fi
}

search_body() {
  date_value=$(future_date "$1")
  printf '{"origin":"BEG","destination":"AMS","depart_date":"%s","passengers":1}' "$date_value"
}

post_search() {
  body="$1"
  out="$2"
  headers="$3"
  curl -sS -D "$headers" -o "$out" -w '%{http_code}' \
    -X POST "${API}/search" \
    -H 'Content-Type: application/json' \
    -d "$body"
}

post_booking() {
  body="$1"
  key="$2"
  out="$3"
  headers="$4"
  curl -sS -D "$headers" -o "$out" -w '%{http_code}' \
    -X POST "${API}/bookings" \
    -H 'Content-Type: application/json' \
    -H "Idempotency-Key: ${key}" \
    -d "$body"
}

assert_code() {
  got="$1"
  want="$2"
  label="$3"
  [ "$got" = "$want" ] || fail "$label: expected HTTP $want, got $got"
  pass "$label"
}

assert_jq_eq() {
  file="$1"
  expr="$2"
  want="$3"
  label="$4"
  got=$(jq -r "$expr" "$file")
  [ "$got" = "$want" ] || fail "$label: expected $want, got $got"
  pass "$label"
}

assert_jq_true() {
  file="$1"
  expr="$2"
  label="$3"
  jq -e "$expr" "$file" >/dev/null || fail "$label"
  pass "$label"
}

provider_status() {
  file="$1"
  provider="$2"
  jq -r --arg provider "$provider" '.providers[$provider].status // ""' "$file"
}

assert_status_in() {
  status="$1"
  allowed="$2"
  label="$3"
  case " $allowed " in
    *" $status "*) pass "$label ($status)" ;;
    *) fail "$label: expected one of [$allowed], got [$status]" ;;
  esac
}

summary() {
  jq -r '"cache=\(.cache) partial=\(.partial) offers=\(.offers|length) providers={" +
    (.providers|to_entries|map("\(.key):\(.value.status)")|join(" ")) + "}"' "$1"
}

reset_chaos() {
  "${SCRIPT_DIR}/chaos.sh" a stable >/dev/null
  "${SCRIPT_DIR}/chaos.sh" b stable >/dev/null
  "${SCRIPT_DIR}/chaos.sh" c stable >/dev/null
  "${SCRIPT_DIR}/chaos.sh" d stable >/dev/null
}

clear_breakers_best_effort() {
  if command -v docker >/dev/null 2>&1; then
    docker compose exec -T redis redis-cli DEL breaker:a breaker:b breaker:c breaker:d >/dev/null 2>&1 || true
  fi
}

need curl
need jq

say "mini-FireFly demo"
note "Talking to core at ${API}"
note "Open Grafana Provider Health while this runs: http://localhost:3000/d/provider-health/provider-health"

curl -fsS "${API}/healthz" >/dev/null || fail "core health check failed; run make up first"
pass "core health endpoint responds"

reset_chaos
clear_breakers_best_effort
pass "providers reset to stable"

# 1. Healthy baseline search -------------------------------------------------
say "Step 1/10: Healthy baseline search"
note "Expected result: HTTP 200, cache miss, partial=false, all providers ok"
BODY_BASE=$(search_body 30)
BASE_JSON="$TMP_BASE/search-baseline.json"
BASE_HDR="$TMP_BASE/search-baseline.hdr"
code=$(post_search "$BODY_BASE" "$BASE_JSON" "$BASE_HDR")
assert_code "$code" "200" "baseline search status"
assert_jq_eq "$BASE_JSON" '.cache' 'miss' "baseline cache miss"
assert_jq_eq "$BASE_JSON" '.partial' 'false' "baseline is not partial"
assert_jq_true "$BASE_JSON" '[.providers[].status] | all(. == "ok")' "all providers ok"
summary "$BASE_JSON"

# 2. Cache hit ---------------------------------------------------------------
say "Step 2/10: Cache hit on repeated query"
note "Expected result: same query returns HTTP 200 and cache=hit"
CACHE_JSON="$TMP_BASE/search-cache.json"
CACHE_HDR="$TMP_BASE/search-cache.hdr"
code=$(post_search "$BODY_BASE" "$CACHE_JSON" "$CACHE_HDR")
assert_code "$code" "200" "repeated search status"
assert_jq_eq "$CACHE_JSON" '.cache' 'hit' "repeated query cache hit"
summary "$CACHE_JSON"

# 3. Slow provider -----------------------------------------------------------
say "Step 3/10: Slow provider scenario"
note "Expected result: provider b is degraded, but the public API remains bounded"
"${SCRIPT_DIR}/chaos.sh" b slow >/dev/null
SLOW_JSON="$TMP_BASE/search-slow.json"
SLOW_HDR="$TMP_BASE/search-slow.hdr"
code=$(post_search "$(search_body 31)" "$SLOW_JSON" "$SLOW_HDR")
assert_code "$code" "200" "slow-provider search status"
status_b=$(provider_status "$SLOW_JSON" b)
[ -n "$status_b" ] || fail "provider b missing from response"
assert_status_in "$status_b" "ok timeout error" "provider b status is represented"
elapsed=$(jq -r '.meta.elapsed_ms' "$SLOW_JSON")
deadline=$(jq -r '.meta.deadline_ms' "$SLOW_JSON")
[ "$elapsed" -le $((deadline + 750)) ] || fail "slow-provider response exceeded expected bound: elapsed=${elapsed} deadline=${deadline}"
pass "slow-provider response stayed near the hard deadline"
summary "$SLOW_JSON"
"${SCRIPT_DIR}/chaos.sh" b stable >/dev/null

# 4. Down provider -----------------------------------------------------------
say "Step 4/10: Down provider scenario"
note "Expected result: provider d reports timeout/error/breaker_open and response.partial=true"
"${SCRIPT_DIR}/chaos.sh" d down >/dev/null
DOWN_JSON="$TMP_BASE/search-down.json"
DOWN_HDR="$TMP_BASE/search-down.hdr"
code=$(post_search "$(search_body 32)" "$DOWN_JSON" "$DOWN_HDR")
assert_code "$code" "200" "down-provider search status"
status_d=$(provider_status "$DOWN_JSON" d)
assert_status_in "$status_d" "timeout error breaker_open" "provider d degraded status"
assert_jq_eq "$DOWN_JSON" '.partial' 'true' "down-provider response is partial"
summary "$DOWN_JSON"

# 5. Breaker opening ---------------------------------------------------------
say "Step 5/10: Circuit breaker opening"
note "Expected result: repeated provider d failures eventually return breaker_open"
breaker_seen=0
i=1
while [ "$i" -le 10 ]; do
  out="$TMP_BASE/search-breaker-${i}.json"
  hdr="$TMP_BASE/search-breaker-${i}.hdr"
  code=$(post_search "$(search_body $((40 + i)))" "$out" "$hdr")
  assert_code "$code" "200" "breaker loop call ${i} status"
  status_d=$(provider_status "$out" d)
  printf '  call %s: provider d=%s\n' "$i" "$status_d"
  if [ "$status_d" = "breaker_open" ]; then
    breaker_seen=1
    break
  fi
  i=$((i + 1))
  sleep 1
done
[ "$breaker_seen" -eq 1 ] || fail "provider d breaker did not open within 10 calls"
pass "provider d breaker opened"

# 6. Partial response --------------------------------------------------------
say "Step 6/10: Partial result response"
note "Expected result: API still returns HTTP 200 while provider d is unavailable"
PARTIAL_JSON="$TMP_BASE/search-partial.json"
PARTIAL_HDR="$TMP_BASE/search-partial.hdr"
code=$(post_search "$(search_body 60)" "$PARTIAL_JSON" "$PARTIAL_HDR")
assert_code "$code" "200" "partial-response search status"
assert_jq_eq "$PARTIAL_JSON" '.partial' 'true' "partial flag is true"
status_d=$(provider_status "$PARTIAL_JSON" d)
assert_status_in "$status_d" "timeout error breaker_open" "provider d remains degraded"
summary "$PARTIAL_JSON"

# 7. Provider recovery -------------------------------------------------------
say "Step 7/10: Provider recovery"
note "Expected result: provider d returns to stable; breaker still needs cooldown/probe"
"${SCRIPT_DIR}/chaos.sh" d stable >/dev/null
pass "provider d set to stable"

# 8. Breaker closing ---------------------------------------------------------
say "Step 8/10: Circuit breaker closing"
note "Expected result: after cooldown, a successful half-open probe closes the breaker"
sleep 17
closed_seen=0
i=1
RECOVERED_JSON="$TMP_BASE/search-recovered.json"
while [ "$i" -le 5 ]; do
  out="$TMP_BASE/search-recovery-${i}.json"
  hdr="$TMP_BASE/search-recovery-${i}.hdr"
  code=$(post_search "$(search_body $((70 + i)))" "$out" "$hdr")
  assert_code "$code" "200" "recovery call ${i} status"
  status_d=$(provider_status "$out" d)
  partial=$(jq -r '.partial' "$out")
  printf '  call %s: provider d=%s partial=%s\n' "$i" "$status_d" "$partial"
  if [ "$status_d" = "ok" ] && [ "$partial" = "false" ]; then
    cp "$out" "$RECOVERED_JSON"
    closed_seen=1
    break
  fi
  i=$((i + 1))
  sleep 2
done
[ "$closed_seen" -eq 1 ] || fail "provider d did not recover to ok/partial=false"
pass "provider d breaker closed"

# 9. Booking creation --------------------------------------------------------
say "Step 9/10: Booking creation"
note "Expected result: first booking request returns HTTP 201 and a bk_ id"
OFFER_ID=$(jq -r '.offers[0].offer_id // empty' "$RECOVERED_JSON")
[ -n "$OFFER_ID" ] || fail "no offer available for booking"
KEY="demo-$(date +%s)-$$"
BOOK_BODY=$(jq -cn --arg offer "$OFFER_ID" '{offer_id:$offer, passenger:{first_name:"Ilia", last_name:"K", email:"x@y.z"}}')
BOOK1_JSON="$TMP_BASE/book-create.json"
BOOK1_HDR="$TMP_BASE/book-create.hdr"
code=$(post_booking "$BOOK_BODY" "$KEY" "$BOOK1_JSON" "$BOOK1_HDR")
assert_code "$code" "201" "fresh booking status"
assert_jq_true "$BOOK1_JSON" '.booking_id | startswith("bk_")' "fresh booking has public id"
BOOKING_ID=$(jq -r '.booking_id' "$BOOK1_JSON")
printf '  booking_id=%s offer_id=%s\n' "$BOOKING_ID" "$OFFER_ID"

# 10. Idempotent replay ------------------------------------------------------
say "Step 10/10: Idempotent booking replay"
note "Expected result: same key/body returns HTTP 200 and Idempotency-Replayed: true"
BOOK2_JSON="$TMP_BASE/book-replay.json"
BOOK2_HDR="$TMP_BASE/book-replay.hdr"
code=$(post_booking "$BOOK_BODY" "$KEY" "$BOOK2_JSON" "$BOOK2_HDR")
assert_code "$code" "200" "booking replay status"
replayed=$(grep -i '^Idempotency-Replayed:' "$BOOK2_HDR" 2>/dev/null | tr -d '\r' | awk '{print tolower($2)}' | tail -n 1 || true)
[ "$replayed" = "true" ] || fail "Idempotency-Replayed header missing or not true"
pass "Idempotency-Replayed header is true"
jq -S . "$BOOK1_JSON" > "$TMP_BASE/book-create.sorted.json"
jq -S . "$BOOK2_JSON" > "$TMP_BASE/book-replay.sorted.json"
if cmp -s "$TMP_BASE/book-create.sorted.json" "$TMP_BASE/book-replay.sorted.json"; then
  pass "replay body matches original booking"
else
  fail "replay body differs from original booking"
fi

say "Demo complete"
note "All checks passed. Providers are reset to stable by the cleanup trap."
