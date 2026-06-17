#!/bin/sh
# scripts/chaos.sh — toggle a mock provider's chaos profile at runtime.
#
# Usage:   scripts/chaos.sh <provider> <profile>
#   provider : a | b | c | d
#   profile  : stable | flaky | slow | down   (SPEC §5.3)
#
# Wraps the mock admin API (SPEC §5.2): PUT /admin/chaos  {"profile":"<p>"}.
# Mocks publish host ports 8081-8084 (mock-a -> 8081 ... mock-d -> 8084), so
# this resolves the provider letter to its host port and PUTs to localhost.
#
# Override the host with CHAOS_HOST (e.g. when the rig runs on a remote VM).
set -eu

CHAOS_HOST="${CHAOS_HOST:-localhost}"

usage() {
  echo "usage: $0 <a|b|c|d> <stable|flaky|slow|down>" >&2
  exit 2
}

[ "$#" -eq 2 ] || usage
PROVIDER="$1"
PROFILE="$2"

# Resolve provider letter -> published host port.
case "${PROVIDER}" in
  a) PORT=8081 ;;
  b) PORT=8082 ;;
  c) PORT=8083 ;;
  d) PORT=8084 ;;
  *) echo "error: unknown provider '${PROVIDER}' (expected a|b|c|d)" >&2; usage ;;
esac

case "${PROFILE}" in
  stable|flaky|slow|down) ;;
  *) echo "error: unknown profile '${PROFILE}' (expected stable|flaky|slow|down)" >&2; usage ;;
esac

URL="http://${CHAOS_HOST}:${PORT}/admin/chaos"
BODY="{\"profile\":\"${PROFILE}\"}"

echo ">> mock-${PROVIDER} (:${PORT}) -> chaos profile '${PROFILE}'"
# -f: fail on HTTP errors; -S: show errors even when silent; -s: no progress.
curl -fsS -X PUT \
  -H 'Content-Type: application/json' \
  -d "${BODY}" \
  "${URL}"
echo ""
