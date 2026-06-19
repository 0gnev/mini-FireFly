# Postmortem: Provider D outage simulation

**Status:** Resolved  
**Severity:** SEV-3 simulation, degraded responses only  
**Date:** 2026-06-11  
**Scope:** Local Docker Compose rehearsal using the mock provider chaos rig

This is not a real production outage. It is a rehearsed incident report showing how the system behaves when one provider becomes unavailable.

## Summary

Provider D became unavailable during a controlled chaos simulation. The aggregator continued returning HTTP 200 responses with offers from the healthy providers and marked responses as `partial: true`.

After repeated failures, the circuit breaker for provider D opened. Grafana showed the provider degradation through status breakdown and breaker state panels. Provider D was then restored, the breaker moved through half-open, a probe succeeded, and the breaker closed again.

## Impact

- Public search remained available.
- Responses from `POST /api/v1/search` stayed HTTP 200.
- Provider D offers were missing while the provider was unavailable.
- Responses correctly included `partial: true`.
- Providers A, B, and C continued returning offers.
- No booking behavior was affected.
- ClickHouse logging and Grafana dashboards recorded the incident path.

The impact was degraded completeness, not a full API outage.

## Timeline

| Time (UTC) | Event |
|---|---|
| 02:20:47 | Baseline confirmed: all mock providers stable, breakers closed, no active alerts. |
| 02:20:57 | Simulation triggered with `make chaos-down P=d`; provider D started refusing `/search` calls. |
| 02:20:57-02:20:58 | Repeated unique searches returned HTTP 200 with provider D reporting `timeout` or `error`; `partial` became `true`. |
| 02:20:58 | Provider D reached the breaker failure threshold; the next call reported `breaker_open`. |
| 02:21:11 | Prometheus observed `fanout_breaker_state{provider="d"} == 2`; Grafana breaker timeline showed D open. |
| 02:22:16 | `BreakerOpen` alert reached firing state after its `for` window. |
| 02:22:30 | Recovery started with `make chaos-reset`; provider D returned to `stable`. |
| 02:22:46 | After cooldown, a half-open probe succeeded; provider D returned `ok` and the breaker closed. |
| 02:23:13 | Breaker alert cleared; full search responses returned with `partial: false`. |

## Root cause

The immediate cause was the mock provider D `down` chaos profile. Provider D refused search calls, which the fan-out service classified as provider failure.

The circuit breaker opening was not the root problem; it was the mitigation working as designed. The breaker protected the request deadline by short-circuiting calls to a provider that had repeatedly failed.

## Detection

The incident was visible through multiple signals:

- search response provider map: `providers.d.status` changed from `ok` to `timeout`/`error` and then `breaker_open`;
- response body: `partial` changed to `true`;
- Prometheus: `fanout_breaker_state{provider="d"}` changed to `2`;
- Grafana: status breakdown showed provider D failures and breaker timeline showed open state;
- Redis: `breaker:d` contained the breaker state;
- fanout logs showed provider D failure and breaker behavior.

The fastest detection path was the API response itself. The alert was intentionally slower because it uses a `for` window to avoid flapping.

## Response

The response was:

1. Confirm public API behavior stayed HTTP 200.
2. Confirm only provider D was degraded.
3. Confirm the breaker opened after repeated failures.
4. Restore provider D with `make chaos-reset`.
5. Wait for the breaker cooldown.
6. Send new searches to trigger the half-open probe.
7. Confirm provider D returned `ok`.
8. Confirm `partial` returned to `false`.
9. Confirm Grafana and Prometheus showed breaker closure.

No manual breaker reset was required. Recovery happened through the normal breaker state machine.

## What went well

- Graceful degradation worked: one provider outage did not fail the entire search request.
- The public API returned useful partial results with a clear provider status map.
- Providers A, B, and C were isolated from provider D's failure.
- The circuit breaker opened at the configured threshold and closed after recovery.
- The incident was observable from responses, Redis, Prometheus, Grafana, and logs.
- The chaos rig made the incident reproducible with a small set of commands.

## What went wrong

- The alert fired later than the first visible response-level degradation because it had a one-minute `for` window.
- A connection-refused failure and a slow timeout can both surface as provider failure statuses, so logs are needed to distinguish them precisely.
- `GET /api/v1/providers` can lag briefly because parts of provider/config state are cached.
- There is no dedicated dashboard annotation when a chaos event starts or ends.
- There is no automated alert today for overall partial response ratio.

## Action items

- Add alert for provider timeout ratio > X% for Y minutes.
- Add alert for partial response ratio > X%.
- Add dashboard annotation support for chaos events.
- Add structured incident IDs to demo scripts.
- Add integration test for provider recovery after breaker cooldown.
- Add a lower-severity immediate breaker-open notification while keeping the current delayed warning alert.
- Add an error-class label or structured log field to distinguish connection refused from deadline timeout.

## Lessons learned

Provider failure is a normal operating mode for an aggregator. The important behavior is not to hide the failure, but to isolate it, return useful partial data, expose provider status clearly, and recover without manual state edits once the provider is healthy again.

The simulation also shows why response-level status maps, breaker metrics, and dashboard panels should be designed together. A reviewer can see the same incident from the API contract, the service internals, and the operational dashboard.
