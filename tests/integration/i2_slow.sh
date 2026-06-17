# I2 — mock-b slow: wall time < deadline+150ms, b: timeout, others ok, partial=true.
# SPEC §14.2 / §5.3 / §6.5. Deadline is 2000ms (SEARCH_DEADLINE_MS).
#
# RIG CONFIG: mock-b runs with BASE_LATENCY_MS=400 (the "naturally slower"
# provider — see docker-compose.yml). In `stable` that is ~280-520ms, well under
# the 800ms per-attempt timeout, so b reports `ok` normally. In `slow` the
# latency is 5×400 ≈ 1000-3000ms, which exceeds the 800ms per-attempt timeout on
# BOTH the initial attempt and the retry — so fanout exhausts its 2 attempts
# against a provider that never answers in time and reports `b: timeout`, while
# the request as a whole still returns within the deadline (the late goroutine is
# abandoned, never blocking past ctx). This makes the literal §14.2 assertion
# hold through the public /search API.
run_i2() {
  chaos_reset_all

  # mock-b slow → every attempt exceeds the per-attempt timeout → b:timeout,
  # but the aggregation still returns bounded (< deadline+150ms) with a/c/d ok.
  chaos_set b slow
  sleep 0.5

  local budget_ms=2150
  local t0 t1 elapsed resp
  t0="$(now_ms)"
  resp="$(search BEG AMS "$(unique_date)" 1)"
  t1="$(now_ms)"
  elapsed=$((t1 - t0))

  local b_status partial a_s c_s d_s b_attempts
  b_status="$(printf '%s' "$resp"   | jq -r '.providers.b.status')"
  b_attempts="$(printf '%s' "$resp" | jq -r '.providers.b.attempts')"
  partial="$(printf '%s' "$resp"    | jq -r '.partial')"
  a_s="$(printf '%s' "$resp" | jq -r '.providers.a.status')"
  c_s="$(printf '%s' "$resp" | jq -r '.providers.c.status')"
  d_s="$(printf '%s' "$resp" | jq -r '.providers.d.status')"
  log "slow path: wall=${elapsed}ms b=$b_status(attempts=$b_attempts) a=$a_s c=$c_s d=$d_s partial=$partial"

  assert_le "$elapsed" "$budget_ms" "wall time (ms) < deadline+150"
  assert_eq "$b_status" "timeout" "provider b status (slow busts per-attempt timeout)"
  assert_eq "$a_s" "ok" "provider a status"
  assert_eq "$c_s" "ok" "provider c status"
  assert_eq "$d_s" "ok" "provider d status"
  assert_eq "$partial" "true" "partial"

  chaos_set b stable
}
