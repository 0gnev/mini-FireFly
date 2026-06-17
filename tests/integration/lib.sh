# tests/integration/lib.sh — shared helpers for the I1-I8 scenario suite.
#
# Sourced by scripts/itest.sh and by each tests/integration/iN_*.sh scenario.
# Pure bash + curl + jq. No state is kept here beyond a few env-configurable
# endpoints; every scenario is self-contained and resets chaos to `stable` on
# exit so the suite is order-insensitive (SPEC §14.2).
#
# Endpoints (host ports per the running stack):
#   core    8000   API
#   mock-a  8081 .. mock-d 8084   (PUT /admin/chaos, GET /healthz)
#   fanout  8090   /metrics
#   clickhouse 8123

# --- configuration ----------------------------------------------------------
API="${API:-http://localhost:8000/api/v1}"
FANOUT_METRICS="${FANOUT_METRICS:-http://localhost:8090/metrics}"
COMPOSE="${COMPOSE:-docker compose}"
COMPOSE_FILE="${COMPOSE_FILE:-docker-compose.yml}"
# Repo root: this file lives at <root>/tests/integration/lib.sh.
ITEST_LIB_DIR="$(CDPATH='' cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")" && pwd)"
REPO_ROOT="${REPO_ROOT:-$(CDPATH='' cd -- "${ITEST_LIB_DIR}/../.." && pwd)}"
CHAOS_SH="${CHAOS_SH:-${REPO_ROOT}/scripts/chaos.sh}"

# Per-provider published host port for the mock admin/health API.
mock_port() {
  case "$1" in
    a) echo 8081 ;; b) echo 8082 ;; c) echo 8083 ;; d) echo 8084 ;;
    *) echo "0" ;;
  esac
}

# --- output -----------------------------------------------------------------
_c_red=$'\033[31m'; _c_grn=$'\033[32m'; _c_yel=$'\033[33m'; _c_dim=$'\033[2m'; _c_rst=$'\033[0m'
[ -t 1 ] || { _c_red=''; _c_grn=''; _c_yel=''; _c_dim=''; _c_rst=''; }

log()  { printf '   %s%s%s\n' "$_c_dim" "$*" "$_c_rst"; }
info() { printf '   %s\n' "$*"; }
ok()   { printf '   %s✓%s %s\n' "$_c_grn" "$_c_rst" "$*"; }
bad()  { printf '   %s✗%s %s\n' "$_c_red" "$_c_rst" "$*"; }

# fail <msg> : record an assertion failure for the current scenario.
SCENARIO_FAILED=0
fail() { SCENARIO_FAILED=1; bad "$*"; }

# assert_eq <actual> <expected> <label>
assert_eq() {
  if [ "$1" = "$2" ]; then ok "$3 = $1"; else fail "$3: expected '$2', got '$1'"; fi
}
# assert_gt <actual> <threshold> <label>   (numeric, actual > threshold)
assert_gt() {
  if [ "$1" -gt "$2" ] 2>/dev/null; then ok "$3 = $1 (> $2)"; else fail "$3: expected > $2, got '$1'"; fi
}
# assert_ge <actual> <threshold> <label>
assert_ge() {
  if [ "$1" -ge "$2" ] 2>/dev/null; then ok "$3 = $1 (>= $2)"; else fail "$3: expected >= $2, got '$1'"; fi
}
# assert_le <actual> <threshold> <label>
assert_le() {
  if [ "$1" -le "$2" ] 2>/dev/null; then ok "$3 = $1 (<= $2)"; else fail "$3: expected <= $2, got '$1'"; fi
}
# assert_true <bashtest...> <label> — last arg is label, rest is a numeric test
# (kept simple; specific scenarios use assert_* above).

# --- HTTP -------------------------------------------------------------------
# search <origin> <dest> <date> [passengers] -> response JSON on stdout.
search() {
  local o="$1" d="$2" date="$3" pax="${4:-1}"
  curl -sS -X POST "${API}/search" \
    -H 'Content-Type: application/json' \
    -d "{\"origin\":\"${o}\",\"destination\":\"${d}\",\"depart_date\":\"${date}\",\"passengers\":${pax}}"
}

# unique_date — an in-range depart_date that is (with very high probability) a
# CACHE MISS: a fresh value every call, even across command-substitution
# boundaries and across re-runs within the 90s cache TTL.
#
# Validation requires today <= date <= today+365 (today is 2026-06-11 in the
# rig). We pick a random offset in [1, 360] days from a fixed base inside that
# window, seeded by $RANDOM + nanoseconds so successive calls (and separate
# suite runs) almost never collide. Callers that need many guaranteed-distinct
# misses in a tight loop should keep the loop bounded (the I-scenarios do).
unique_date() {
  local nonce off ns
  # Nanoseconds may carry a leading zero (octal trap) and may be unsupported
  # (BSD date prints "N"); normalize to a base-10 integer, defaulting to 0.
  ns="$(date +%N 2>/dev/null)"
  case "$ns" in (*[!0-9]*|'') ns=0 ;; esac
  # $RANDOM is 0..32767; mix with low-order nanoseconds for extra spread.
  nonce=$(( RANDOM * 131 + 10#${ns:-0} ))
  off=$(( (nonce % 360) + 1 ))   # 1..360 days ahead of base
  if command -v python3 >/dev/null 2>&1; then
    python3 - "$off" <<'PY'
import sys, datetime
off = int(sys.argv[1])
print((datetime.date(2026,6,15) + datetime.timedelta(days=off)).isoformat())
PY
  else
    # Fallback without python: spread across a ~330-day window in 2026 H2/2027.
    local doy=$(( (off % 330) ))
    local m=$(( 7 + (doy / 28) ))
    local dd=$(( (doy % 28) + 1 ))
    if [ "$m" -gt 12 ]; then
      printf '2027-%02d-%02d\n' $(( m - 12 )) "$dd"
    else
      printf '2026-%02d-%02d\n' "$m" "$dd"
    fi
  fi
}

# --- chaos ------------------------------------------------------------------
chaos_set() { "${CHAOS_SH}" "$1" "$2" >/dev/null; }
chaos_reset_all() {
  for p in a b c d; do "${CHAOS_SH}" "$p" stable >/dev/null 2>&1 || true; done
}
# chaos_profile <provider> -> current profile string.
chaos_profile() {
  local port; port="$(mock_port "$1")"
  curl -sS "http://localhost:${port}/admin/chaos" | jq -r '.profile'
}

# --- fanout metrics ---------------------------------------------------------
# metric_counter <metric_name> <provider> [status] -> integer counter value
# (sum across matching label sets). Missing metric -> 0.
metric_counter() {
  local name="$1" provider="$2" status="${3:-}"
  local body; body="$(curl -sS "${FANOUT_METRICS}")"
  local pat="^${name}\{"
  printf '%s\n' "$body" \
    | grep -E "$pat" \
    | { if [ -n "$provider" ]; then grep "provider=\"${provider}\""; else cat; fi; } \
    | { if [ -n "$status" ]; then grep "status=\"${status}\""; else cat; fi; } \
    | awk '{s+=$NF} END{printf "%d", s+0}'
}

# breaker_state <provider> -> 0 closed | 1 half_open | 2 open (float trimmed).
breaker_state() {
  curl -sS "${FANOUT_METRICS}" \
    | grep -E "^fanout_breaker_state\{provider=\"$1\"\}" \
    | awk '{printf "%d", $NF+0}'
}

# fanout_requests_total <provider> — sum across all statuses for a provider.
fanout_requests_total() {
  local body; body="$(curl -sS "${FANOUT_METRICS}")"
  printf '%s\n' "$body" \
    | grep -E "^fanout_provider_requests_total\{provider=\"$1\"" \
    | awk '{s+=$NF} END{printf "%d", s+0}'
}

# --- docker logs ------------------------------------------------------------
# mock_ratelimited_loglines <provider> <since> -> count of "rate limited" log
# lines emitted by mock-<provider> since the given timestamp (RFC3339 or
# relative like 30s). The mock has no /metrics; its own 429s are observed via
# its structured stdout log (msg="rate limited").
mock_ratelimited_loglines() {
  local provider="$1" since="$2"
  ${COMPOSE} -f "${COMPOSE_FILE}" logs --since "${since}" "mock-${provider}" 2>/dev/null \
    | grep -c '"rate limited"' || true
}

# --- waiting ----------------------------------------------------------------
# wait_service_healthy <service> <timeout_s>
wait_service_healthy() {
  local svc="$1" timeout="${2:-60}" i=0
  while [ "$i" -lt "$timeout" ]; do
    local h
    h="$(${COMPOSE} -f "${COMPOSE_FILE}" ps --format '{{.Service}} {{.Health}}' 2>/dev/null \
        | awk -v s="$svc" '$1==s{print $2}')"
    [ "$h" = "healthy" ] && return 0
    i=$((i+1)); sleep 1
  done
  return 1
}

# now_ms — current epoch milliseconds (for wall-time assertions).
now_ms() {
  if command -v python3 >/dev/null 2>&1; then
    python3 -c 'import time; print(int(time.time()*1000))'
  else
    echo $(( $(date +%s) * 1000 ))
  fi
}
