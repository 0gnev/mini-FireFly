# POSTMORTEM — Provider D outage, circuit breaker open

**Status:** Resolved · **Severity:** SEV-3 (degraded, partial results; no full outage)
**Date of incident:** 2026-06-11 · **Author:** A10 (docs/incident rehearsal)
**Type:** Rehearsed incident on the live stack (chaos rig). All values below are observed,
not invented.

This is a **blameless** postmortem. The focus is on the system and our response, not on any
individual. The chaos rig is designed to make these failures happen on demand; reproducing
one and watching the resilience machinery work is the point.

---

## Summary

Provider **d** became unreachable (connection refused on every `/search`). The fan-out
retried each call, recorded the failures against d's circuit breaker, and after **5 failed
calls** the breaker **opened**, short-circuiting further calls to d for the cooldown period.
Throughout the incident, `POST /api/v1/search` continued returning **HTTP 200** with results
from providers a, b, and c and `"partial": true` — d simply dropped out of the results.
After provider d was restored, the breaker's half-open probe succeeded on the first call
past the 15 s cooldown and the breaker closed; full service resumed automatically.

The incident was triggered with `make chaos-down P=d` and resolved with `make chaos-reset`.

## Impact

- **User-facing:** Searches remained available (HTTP 200) the entire time. Results from
  provider d were missing → responses flagged `"partial": true`. Users saw a smaller (but
  valid, sorted, deduplicated) set of offers — no errors, no elevated latency on the surviving
  providers (a/b/c stayed `ok` with normal p99).
- **No 5xx** from the aggregator. No bookings affected. No data loss.
- **Blast radius:** one provider out of four. By design the per-provider bulkhead and the
  shared deadline kept d's failure from affecting the other providers' goroutines.
- **Duration of degradation:** ≈ 1m33s from first failure (02:20:57Z) to breaker closed /
  d:ok (02:22:46Z). The `BreakerOpen` alert was in firing state for ≈ 57s (02:22:16Z →
  02:23:13Z).

## Timeline (UTC, 2026-06-11)

| Time | Event |
|---|---|
| 02:20:47 | Pre-incident baseline confirmed: all breakers `0` (closed), no active alerts, all mocks `stable`. |
| 02:20:57 | **Trigger:** `make chaos-down P=d` — mock-d set to `down` (connection refused on `/search`). |
| 02:20:57–58 | Searches loop (unique dates to bypass cache). Calls 1–5: `d: timeout`, 2 attempts each (initial + 1 retry), latency 118–157 ms. Provider d's failure counter climbs 1→5. |
| 02:20:58 | **Call 6: `d: breaker_open`** (latency 0 ms, 0 attempts) — breaker tripped at `failure_threshold=5`. Calls 7–8 also `breaker_open`. Searches still 200, `partial: true`, a/b/c `ok`. |
| 02:21:11 | `BreakerOpen` alert becomes **pending** (`activeAt=02:21:11.234Z`); `fanout_breaker_state{provider="d"}` scraped as `2`. |
| 02:21:25 | Confirmed in Prometheus (`fanout_breaker_state{d}=2`) and Redis (`breaker:d` → `state=open, failures=5, opened_at=1781144458814`). |
| 02:22:16 | `BreakerOpen` alert transitions to **firing** (`for: 1m` satisfied, ≈65s after activeAt). |
| 02:22:30 | **Mitigation:** `make chaos-reset` — mock-d returned to `stable`. |
| 02:22:46 | After the 15 s cooldown, the next search drives the **half-open probe**; it succeeds → breaker **closed**. `d: ok`, 5 offers, `partial: false`. Probes 2–5 all `ok`. |
| 02:23:13 | `BreakerOpen` alert returns to **inactive**; `fanout_breaker_state{d}=0`, Redis `breaker:d state=closed`. Incident resolved. |

## Root cause

Provider d's `/search` endpoint refused all connections (chaos `down` profile; in
production this maps to a crashed/unreachable provider process or network partition). The
connection failures classify as retryable provider-down signals (`ErrTimeout`/connection
error). The fan-out retried each (max 2 attempts), recorded the outcome against the breaker,
and on the 5th failing call within the 30 s window the breaker hit its `failure_threshold=5`
and opened — exactly as designed. **The breaker opening is not the bug; it is the mitigation
working.** The root cause was the provider being down.

## Detection

Detection worked as designed, in layers:

- **Live response signal (fastest):** the per-provider status in the search response went
  `ok → timeout → breaker_open` within ~1 second of the trigger.
- **Metric:** `fanout_breaker_state{provider="d"}` rose to `2` (open) and was visible on the
  Grafana **Breaker state timeline (0 closed / 1 half-open / 2 open)** panel; **Status
  breakdown** showed d's `timeout`/`breaker_open` slices.
- **Alert:** `BreakerOpen` (`fanout_breaker_state == 2 for 1m`) went pending at 02:21:11 and
  fired at 02:22:16. The 1-minute `for` clause intentionally suppresses flapping — the
  trade-off is ~1 min of detection latency on the *alert* (the dashboard and response
  surfaced it immediately).

## Resolution / recovery

Restoring the provider (`make chaos-reset`, i.e. d → `stable`; in production a
`docker compose restart mock-d` or fixing the upstream) was sufficient. **No manual breaker
intervention was needed** — the state machine recovered on its own:

1. `open` → after `cooldown_s=15` → `half_open`.
2. The next search admitted **one** probe call (`half_open_max=1`); it succeeded against the
   now-healthy provider.
3. Success → breaker `closed`, counters reset → d back in rotation.

Observed: first post-cooldown search at 02:22:46 returned `d: ok`; the alert cleared at
02:23:13.

## What went well

- **Graceful degradation held.** Zero user-facing errors; searches stayed 200 with
  `partial: true`. One dead provider never threatened the whole request.
- **Blast radius contained.** Providers a/b/c were unaffected — normal latency, `ok` status.
  The per-provider goroutine + bulkhead isolation did its job.
- **Breaker tripped at exactly the configured threshold** (5 failures) and **recovered
  automatically** via a single half-open probe — no human action on the breaker itself.
- **Full observability.** The transition was legible end-to-end: live response field → Redis
  `breaker:d` hash → Prometheus gauge → Grafana timeline → `BreakerOpen` alert. Diagnosis
  took seconds.
- **The chaos rig made this a one-command rehearsal** (`make chaos-down P=d` /
  `make chaos-reset`), which is exactly what it's for.

## What didn't go well / risks

- **Alert latency.** `BreakerOpen` only fires after `for: 1m`. For a single non-critical
  provider that's a reasonable anti-flap trade-off, but an operator relying solely on the
  alert (not the dashboard) is blind for ~1 minute after the breaker opens.
- **`down` surfaces as `timeout`, not a distinct status.** Connection-refused/reset is
  classified and reported as `timeout` (retryable). It's correct for breaker/retry behavior,
  but the status alone doesn't distinguish "slow" from "refused" — operators must check logs
  to tell them apart.
- **`GET /api/v1/providers` briefly lagged.** During the incident one read of `/providers`
  reported d's breaker as `closed` while Prometheus and Redis both showed `open` — the
  endpoint's `last_5m`/config read path is cached (~10 s in Redis), so it can momentarily
  trail the authoritative breaker state. Not a correctness bug, but a confusing signal if
  used as the primary source of truth during a fast-moving incident.
- **No automated page on `partial`-rate spikes.** A creeping rise in partial responses
  (several providers each just below their breaker threshold) wouldn't trip `BreakerOpen` and
  would only be caught by `ProviderErrorRateHigh` per provider.

## Action items

| # | Action | Owner | Notes |
|---|---|---|---|
| 1 | Add a faster-firing companion alert (e.g. `BreakerOpen` with `for: 0s` at `info` severity, or a `breaker_open`-rate alert) so the dashboard-equivalent signal reaches paging. | observability owner (A7/A8) | Keep the 1m `warning` for anti-flap; add a low-noise immediate `info`. |
| 2 | Surface connection-refused distinctly from deadline-timeout in logs/metrics (e.g. an error-class label on `fanout_provider_requests_total` or a structured `error` reason), so operators don't have to grep logs to tell "refused" from "slow". | fanout owner (A2/A4) | Status enum stays as-is; this is an additional diagnostic dimension. |
| 3 | Document (RUNBOOK Scenario 1, done) that `breaker:d` in Redis + `fanout_breaker_state` are the source of truth, and `/providers` may lag up to ~10 s. Consider shortening or bypassing the cache for the breaker field specifically. | core owner (A6) + docs (A10) | Doc note shipped; cache-TTL change is optional. |
| 4 | Add a dashboard panel / alert on **partial-response share** crossing a threshold, to catch many-providers-slightly-degraded before it becomes a multi-breaker event. | observability owner | `Partial-response share (ClickHouse)` panel exists; add an alert on it. |

## Appendix — how to reproduce

```sh
make chaos-reset                                  # clean baseline (all mocks stable)
make chaos-down P=d                               # trigger
# loop unique-date searches until d -> breaker_open (≈6 calls):
for i in $(seq 1 8); do curl -s -X POST http://localhost:8000/api/v1/search \
  -H 'Content-Type: application/json' \
  -d "{\"origin\":\"BEG\",\"destination\":\"AMS\",\"depart_date\":\"2026-09-0$i\",\"passengers\":1}" \
  | jq -r '.providers.d.status'; done
curl -s --get http://localhost:9090/api/v1/query \
  --data-urlencode 'query=fanout_breaker_state' \
  | jq -r '.data.result[]|"\(.metric.provider)=\(.value[1])"'   # d=2
# watch http://localhost:9090/alerts for BreakerOpen (pending -> firing after 1m)
make chaos-reset                                  # recover; probe closes the breaker after 15s cooldown
```
