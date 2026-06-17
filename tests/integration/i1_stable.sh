# I1 — all mocks stable: 200, partial=false, offers from 4 providers, dedup > 0.
# SPEC §14.2.
run_i1() {
  chaos_reset_all
  sleep 0.3

  local date; date="$(unique_date)"
  local resp; resp="$(search BEG AMS "$date" 1)"

  # Basic shape.
  local cache partial dedup total
  cache="$(printf '%s' "$resp"  | jq -r '.cache')"
  partial="$(printf '%s' "$resp" | jq -r '.partial')"
  dedup="$(printf '%s' "$resp"  | jq -r '.meta.deduplicated')"
  total="$(printf '%s' "$resp"  | jq -r '.meta.total_offers')"
  log "cache=$cache partial=$partial dedup=$dedup total_offers=$total"

  assert_eq "$cache" "miss" "cache (fresh query)"
  assert_eq "$partial" "false" "partial"

  # All four providers returned ok.
  local okcount
  okcount="$(printf '%s' "$resp" | jq '[.providers | to_entries[] | select(.value.status=="ok")] | length')"
  assert_eq "$okcount" "4" "providers with status=ok"

  # Offers present from all 4 providers (post-dedup some may be collapsed, but
  # with disjoint-ish private pools every provider should still place >=1 offer).
  local distinct
  distinct="$(printf '%s' "$resp" | jq -r '[.offers[].provider] | unique | length')"
  assert_eq "$distinct" "4" "distinct providers in merged offers"

  # Dedup must fire (the mock-overlap fix is the prerequisite for this).
  assert_gt "$dedup" "0" "deduplicated count"
}
