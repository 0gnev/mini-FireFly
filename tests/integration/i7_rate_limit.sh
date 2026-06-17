# I7 — hammer one provider above rate_limit_rps (SPEC §14.2 / §10.3 / §9):
#   * `rate_limited` statuses appear (client-side token bucket drained)
#   * fanout_rate_limited_total{a} increments
#   * the mock's OWN 429 count stays ~0 — the client limiter (40 rps) sits BELOW
#     the mock's own cap (50 rps), so the provider is never poisoned by
#     self-inflicted 429s.
#
# We drive the fanout INTERNAL API (§9, POST :8090/v1/fanout) directly, each call
# requesting ONLY provider `a`. This isolates provider a's token bucket and —
# crucially — lets the burst actually exceed 40 rps: fanout (Go) absorbs dense
# concurrency that the core PHP-FPM front would otherwise throttle below the
# limit. The limiter exercised is exactly the per-provider Redis token bucket
# from §10.3. Hitting fanout directly is the documented internal contract and the
# host port (8090) is published for exactly this kind of probing.
FANOUT_URL="${FANOUT_URL:-http://localhost:8090}"

_i7_fanout_body() {
  cat <<JSON
{"request_id":"req_itest_i7_$1","deadline_ms":1850,
 "query":{"origin":"BEG","destination":"AMS","depart_date":"2026-07-01","passengers":1,"return_date":null},
 "providers":[{"name":"a","base_url":"http://mock-a:8080","timeout_ms":800,"rate_limit_rps":40,
   "breaker":{"failure_threshold":5,"window_s":30,"cooldown_s":15,"half_open_max":1}}]}
JSON
}

run_i7() {
  chaos_reset_all
  sleep 0.3

  local before; before="$(metric_counter fanout_rate_limited_total a)"
  local burst=120
  local since_marker="60s"

  info "firing $burst fully-concurrent fanout calls (provider a only) to overrun a's 40-token bucket ..."
  local tmp; tmp="$(mktemp -d)"
  local t0; t0="$(now_ms)"

  local i
  for i in $(seq 1 "$burst"); do
    curl -sS -X POST "${FANOUT_URL}/v1/fanout" -H 'Content-Type: application/json' \
      -d "$(_i7_fanout_body "$i")" >"${tmp}/r_${i}" 2>/dev/null &
  done
  wait
  local t1; t1="$(now_ms)"
  log "burst delivered in ~$((t1 - t0))ms"

  # Count fanout responses where provider a came back rate_limited.
  local rl_count=0 f st
  for f in "${tmp}"/r_*; do
    st="$(jq -r '.results[0].status // "?"' <"$f" 2>/dev/null)"
    [ "$st" = "rate_limited" ] && rl_count=$((rl_count + 1))
  done
  rm -rf "${tmp}"
  log "fanout responses with a:rate_limited = $rl_count"
  assert_gt "$rl_count" "0" "fanout responses where provider a is rate_limited"

  # fanout client-side rate-limit counter must have incremented.
  local after; after="$(metric_counter fanout_rate_limited_total a)"
  local delta=$((after - before))
  log "fanout_rate_limited_total{a} delta = $delta"
  assert_gt "$delta" "0" "fanout_rate_limited_total{a} increment"

  # The mock's OWN 429s should stay ~0 (client limiter fires first). Small
  # tolerance for burst-edge timing jitter.
  local mock429; mock429="$(mock_ratelimited_loglines a "${since_marker}")"
  log "mock-a own '429 rate limited' log lines during window = $mock429"
  assert_le "$mock429" "5" "mock-a own 429 count (~0, tolerance 5)"
}
