#!/usr/bin/env bash
# scripts/itest.sh — integration scenario suite runner (I1-I8, SPEC §14.2).
#
# Drives the LIVE stack (assumed already up via `docker compose up -d --wait`)
# through the eight integration scenarios that form the testing contract. Each
# scenario is a named, independently-runnable check; chaos is reset to `stable`
# around every scenario so the suite is order-insensitive.
#
# Invoked by `make itest`. Exits non-zero if any scenario fails.
#
# Usage:
#   scripts/itest.sh                 # run all scenarios (I1..I8)
#   scripts/itest.sh I2 I5           # run only the named scenarios
#
# Env overrides (see tests/integration/lib.sh): API, FANOUT_METRICS, COMPOSE,
# COMPOSE_FILE.
#
# Portable to bash 3.2 (macOS) and bash 4+/5 (CI Linux) — no associative arrays.
#
# NOTE: I4 (breaker cooldown) and I8 (redis restart) each take ~15-30s; the full
# suite runs in roughly 1-2 minutes.
set -uo pipefail

SCRIPT_DIR="$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)"
REPO_ROOT="$(CDPATH='' cd -- "${SCRIPT_DIR}/.." && pwd)"
ITEST_DIR="${REPO_ROOT}/tests/integration"
export REPO_ROOT

# shellcheck source=tests/integration/lib.sh
. "${ITEST_DIR}/lib.sh"

# Preflight: jq and a reachable core API are hard requirements for the real suite.
command -v jq >/dev/null 2>&1 || { echo "FATAL: jq is required but not installed" >&2; exit 3; }
if ! curl -fsS "${API}/healthz" >/dev/null 2>&1; then
  echo "FATAL: core API not reachable at ${API}/healthz — is the stack up (docker compose up -d --wait)?" >&2
  exit 3
fi

# Load every scenario definition.
for f in i1_stable i2_slow i3_bad_payload i4_breaker i5_cache i6_idempotency i7_rate_limit i8_redis_down; do
  # shellcheck disable=SC1090
  . "${ITEST_DIR}/${f}.sh"
done

ALL_IDS="I1 I2 I3 I4 I5 I6 I7 I8"

# scn_fn <id> -> echoes the runner function name (or empty for unknown).
scn_fn() {
  case "$1" in
    I1) echo run_i1 ;; I2) echo run_i2 ;; I3) echo run_i3 ;; I4) echo run_i4 ;;
    I5) echo run_i5 ;; I6) echo run_i6 ;; I7) echo run_i7 ;; I8) echo run_i8 ;;
    *) echo "" ;;
  esac
}
# scn_desc <id> -> echoes the one-line description.
scn_desc() {
  case "$1" in
    I1) echo "all mocks stable -> 200, partial=false, 4 providers, dedup>0" ;;
    I2) echo "mock-b slow -> b:timeout, others ok, partial=true, < deadline+150ms" ;;
    I3) echo "mock-c flaky truncated JSON -> c:bad_payload, attempts=1" ;;
    I4) echo "mock-d down -> breaker opens <=6 calls; recovers after cooldown" ;;
    I5) echo "repeat query -> cache:hit, fanout not called" ;;
    I6) echo "booking idempotency -> 201 / replay 200 / conflict 409" ;;
    I7) echo "hammer provider a -> client rate_limited fires, mock 429 ~0" ;;
    I8) echo "redis stopped -> searches still 200; then restored" ;;
    *) echo "" ;;
  esac
}

# Selection: args (e.g. I2 I5) or all.
if [ "$#" -gt 0 ]; then
  SELECTED="$*"
else
  SELECTED="$ALL_IDS"
fi

# Results accumulate as "ID=PASS" / "ID=FAIL" lines in RESULTS.
RESULTS=""
overall=0
suite_t0="$(now_ms)"

printf '\n==> mini-FireFly integration suite (I1-I8) — SPEC §14.2\n'
printf '    API=%s\n\n' "${API}"

for id in $SELECTED; do
  fn="$(scn_fn "$id")"
  if [ -z "$fn" ]; then
    printf '!! unknown scenario %s (valid: %s)\n' "$id" "$ALL_IDS" >&2
    overall=1
    continue
  fi
  desc="$(scn_desc "$id")"
  printf '%s── %s — %s%s\n' "$_c_yel" "$id" "$desc" "$_c_rst"
  SCENARIO_FAILED=0
  local_t0="$(now_ms)"
  if ! "$fn"; then SCENARIO_FAILED=1; fi
  local_t1="$(now_ms)"
  dur=$(( local_t1 - local_t0 ))
  # Always leave chaos clean between scenarios.
  chaos_reset_all >/dev/null 2>&1 || true

  if [ "$SCENARIO_FAILED" -eq 0 ]; then
    RESULTS="${RESULTS}${id}=PASS "
    printf '   %s%s PASS%s (%dms)\n\n' "$_c_grn" "$id" "$_c_rst" "$dur"
  else
    RESULTS="${RESULTS}${id}=FAIL "
    overall=1
    printf '   %s%s FAIL%s (%dms)\n\n' "$_c_red" "$id" "$_c_rst" "$dur"
  fi
done

suite_t1="$(now_ms)"

# Final cleanup: restore baseline (all mocks stable, redis running & healthy).
printf '==> cleanup: restoring baseline (mocks stable, redis up) ...\n'
chaos_reset_all >/dev/null 2>&1 || true
${COMPOSE} -f "${COMPOSE_FILE}" start redis >/dev/null 2>&1 || true
wait_service_healthy redis 30 >/dev/null 2>&1 || true

# result_of <id> -> PASS|FAIL|SKIP from the RESULTS string.
result_of() {
  for kv in $RESULTS; do
    case "$kv" in "$1="*) echo "${kv#*=}"; return ;; esac
  done
  echo "SKIP"
}

# Summary table.
printf '\n==> SUMMARY\n'
pass=0; fail=0
for id in $SELECTED; do
  r="$(result_of "$id")"
  desc="$(scn_desc "$id")"
  if [ "$r" = "PASS" ]; then
    printf '   %s%-3s PASS%s  %s\n' "$_c_grn" "$id" "$_c_rst" "$desc"
    pass=$((pass+1))
  else
    printf '   %s%-3s %s%s  %s\n' "$_c_red" "$id" "$r" "$_c_rst" "$desc"
    fail=$((fail+1))
  fi
done
printf '\n   %d passed, %d failed  (suite wall: %dms)\n\n' "$pass" "$fail" "$(( suite_t1 - suite_t0 ))"

exit "$overall"
