# I5 — repeat identical query within TTL: second is cache:hit, fanout NOT called.
# SPEC §14.2 / §7.4. We snapshot the fanout per-request counter total before and
# after the SECOND (cached) search and assert zero delta — proving core served
# from Redis without fanning out.
run_i5() {
  chaos_reset_all
  sleep 0.3

  local date; date="$(unique_date)"

  # First search: primes the cache (must be a miss and have >=1 ok provider so
  # core actually caches it).
  local r1 c1
  r1="$(search VIE ZRH "$date" 1)"
  c1="$(printf '%s' "$r1" | jq -r '.cache')"
  assert_eq "$c1" "miss" "first search cache"

  # Snapshot total fanout requests across all four providers.
  local before_a before_b before_c before_d before
  before_a="$(fanout_requests_total a)"; before_b="$(fanout_requests_total b)"
  before_c="$(fanout_requests_total c)"; before_d="$(fanout_requests_total d)"
  before=$((before_a + before_b + before_c + before_d))

  # Second identical search: must hit cache.
  local r2 c2
  r2="$(search VIE ZRH "$date" 1)"
  c2="$(printf '%s' "$r2" | jq -r '.cache')"
  assert_eq "$c2" "hit" "second search cache"

  # Snapshot again — no fanout calls should have happened.
  local after_a after_b after_c after_d after
  after_a="$(fanout_requests_total a)"; after_b="$(fanout_requests_total b)"
  after_c="$(fanout_requests_total c)"; after_d="$(fanout_requests_total d)"
  after=$((after_a + after_b + after_c + after_d))

  local delta=$((after - before))
  log "fanout_provider_requests_total delta across cached call = $delta"
  assert_eq "$delta" "0" "fanout request delta on cache hit"

  # Cached body must be byte-equal modulo the request_id/cache fields.
  local b1 b2
  b1="$(printf '%s' "$r1" | jq -S 'del(.request_id,.cache)')"
  b2="$(printf '%s' "$r2" | jq -S 'del(.request_id,.cache)')"
  if [ "$b1" = "$b2" ]; then ok "cached body matches original (modulo request_id/cache)"; \
    else fail "cached body differs from original"; fi
}
