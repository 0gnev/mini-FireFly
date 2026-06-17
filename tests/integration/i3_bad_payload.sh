# I3 — mock-c flaky forcing truncated JSON: c: bad_payload, attempts=1 (no retry).
# SPEC §14.2 / §6.5. Under `flaky`, ~10% of requests return a truncated JSON body
# (status 200, Content-Type JSON, cut at a random byte). The adapter must classify
# that as ErrBadPayload — reported `bad_payload`, with NO retry (attempts=1).
#
# Truncation is probabilistic, so we loop fresh (uncached) searches until a
# c:bad_payload is observed, with a bounded attempt budget.
run_i3() {
  chaos_reset_all
  chaos_set c flaky
  sleep 0.5

  local max_tries=120 i=0 seen=0 attempts=""
  while [ "$i" -lt "$max_tries" ]; do
    i=$((i + 1))
    local date; date="$(unique_date)"
    local resp; resp="$(search BEG AMS "$date" 1)"
    local cs; cs="$(printf '%s' "$resp" | jq -r '.providers.c.status')"
    if [ "$cs" = "bad_payload" ]; then
      attempts="$(printf '%s' "$resp" | jq -r '.providers.c.attempts')"
      seen=1
      log "observed c:bad_payload on try $i (attempts=$attempts)"
      break
    fi
  done

  if [ "$seen" -ne 1 ]; then
    fail "did not observe c:bad_payload within $max_tries flaky searches"
    chaos_set c stable
    return
  fi
  ok "c status = bad_payload"

  # No retry for the bad_payload class: exactly one attempt.
  assert_eq "$attempts" "1" "provider c attempts (no retry for bad_payload)"

  chaos_set c stable
}
