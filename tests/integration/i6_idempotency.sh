# I6 — booking idempotency (SPEC §7.3 / §14.2):
#   * fresh key+body            -> 201 Created
#   * replay same key+body      -> 200, header Idempotency-Replayed: true, identical body
#   * same key, different body  -> 409 idempotency_key_reuse
run_i6() {
  # Unique key per run so reruns don't collide with prior 24h Redis records.
  local key="itest-i6-$(date +%s)-$$"
  local body1='{"offer_id":"a:7f3c9b2e","passenger":{"first_name":"Ilia","last_name":"K","email":"x@y.z"}}'
  local body2='{"offer_id":"b:1234abcd","passenger":{"first_name":"Other","last_name":"Person","email":"q@w.e"}}'

  # --- fresh booking -> 201 ---
  local out code rbody
  out="$(curl -sS -o /tmp/i6_1.body -w '%{http_code}' -X POST "${API}/bookings" \
        -H 'Content-Type: application/json' -H "Idempotency-Key: ${key}" -d "$body1")"
  code="$out"
  assert_eq "$code" "201" "fresh booking HTTP status"
  local bid1; bid1="$(jq -r '.booking_id' </tmp/i6_1.body 2>/dev/null)"
  log "booking_id=$bid1"

  # --- replay same key+body -> 200 + Idempotency-Replayed: true + identical body ---
  local hdrs code2
  code2="$(curl -sS -D /tmp/i6_2.hdr -o /tmp/i6_2.body -w '%{http_code}' -X POST "${API}/bookings" \
        -H 'Content-Type: application/json' -H "Idempotency-Key: ${key}" -d "$body1")"
  assert_eq "$code2" "200" "replay HTTP status"

  local replayed
  replayed="$(grep -i '^Idempotency-Replayed:' /tmp/i6_2.hdr | tr -d '\r' | awk '{print tolower($2)}')"
  assert_eq "$replayed" "true" "Idempotency-Replayed header"

  local b_orig b_replay
  b_orig="$(jq -S . </tmp/i6_1.body)"
  b_replay="$(jq -S . </tmp/i6_2.body)"
  if [ "$b_orig" = "$b_replay" ]; then ok "replay body identical to original"; \
    else fail "replay body differs from original"; fi

  # --- same key, different body -> 409 ---
  local code3 err3
  code3="$(curl -sS -o /tmp/i6_3.body -w '%{http_code}' -X POST "${API}/bookings" \
        -H 'Content-Type: application/json' -H "Idempotency-Key: ${key}" -d "$body2")"
  assert_eq "$code3" "409" "conflicting body HTTP status"
  err3="$(jq -r '.error' </tmp/i6_3.body 2>/dev/null)"
  assert_eq "$err3" "idempotency_key_reuse" "conflict error code"

  rm -f /tmp/i6_1.body /tmp/i6_2.hdr /tmp/i6_2.body /tmp/i6_3.body
}
