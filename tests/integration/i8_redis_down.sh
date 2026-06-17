# I8 — Redis stopped mid-run (SPEC §14.2 / §12.5 fail-open): searches still 200
#   (no cache, breakers fail-open), errors logged; then restart Redis + wait
#   healthy to restore the baseline.
#
# NOTE: stops/starts the redis container; takes ~15-30s.
run_i8() {
  chaos_reset_all
  sleep 0.3

  # Baseline search works before stopping redis.
  local date; date="$(unique_date)"
  local pre_code
  pre_code="$(curl -sS -o /dev/null -w '%{http_code}' -X POST "${API}/search" \
    -H 'Content-Type: application/json' \
    -d "{\"origin\":\"BEG\",\"destination\":\"AMS\",\"depart_date\":\"${date}\",\"passengers\":1}")"
  assert_eq "$pre_code" "200" "baseline search before redis stop"

  info "stopping redis ..."
  ${COMPOSE} -f "${COMPOSE_FILE}" stop redis >/dev/null 2>&1

  # With redis down: cache lookups fail, breaker/limiter fail-open (allow calls).
  # Searches must still return 200 with offers (the aggregation itself does not
  # fail on a redis outage).
  local ok200=0 tries=4 k
  for k in $(seq 1 "$tries"); do
    local d2; d2="$(unique_date)"
    local resp code
    resp="$(curl -sS -w '\n%{http_code}' -X POST "${API}/search" \
      -H 'Content-Type: application/json' \
      -d "{\"origin\":\"BEG\",\"destination\":\"AMS\",\"depart_date\":\"${d2}\",\"passengers\":1}")"
    code="$(printf '%s' "$resp" | tail -n1)"
    local jbody; jbody="$(printf '%s' "$resp" | sed '$d')"
    local okprov
    okprov="$(printf '%s' "$jbody" | jq '[.providers | to_entries[] | select(.value.status=="ok")] | length' 2>/dev/null || echo 0)"
    local cache; cache="$(printf '%s' "$jbody" | jq -r '.cache' 2>/dev/null || echo '?')"
    log "redis-down search $k: http=$code cache=$cache ok_providers=$okprov"
    if [ "$code" = "200" ] && [ "${okprov:-0}" -ge 1 ]; then ok200=$((ok200 + 1)); fi
  done

  assert_ge "$ok200" "$tries" "searches returning 200 with offers while redis down"

  # Restore redis and wait until healthy so subsequent scenarios / baseline are clean.
  info "starting redis + waiting healthy ..."
  ${COMPOSE} -f "${COMPOSE_FILE}" start redis >/dev/null 2>&1
  if wait_service_healthy redis 60; then
    ok "redis restored and healthy"
  else
    fail "redis did not return to healthy within 60s"
  fi

  # A post-recovery search should cache again (cache:miss then hit).
  local d3; d3="$(unique_date)"
  local m1 m2
  m1="$(search BEG AMS "$d3" 1 | jq -r '.cache')"
  m2="$(search BEG AMS "$d3" 1 | jq -r '.cache')"
  log "post-recovery cache: $m1 then $m2"
  if [ "$m1" = "miss" ] && [ "$m2" = "hit" ]; then
    ok "cache works again after redis recovery"
  else
    fail "cache did not recover after redis restart ($m1/$m2)"
  fi
}
