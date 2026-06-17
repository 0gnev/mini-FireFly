# RUNBOOK — mini-FireFly

Operational procedures for the four rehearsed incident classes. Every command here is real
and runs against the compose stack from the repo root. The chaos rig (`scripts/chaos.sh`,
`make chaos-*`) drives the mock providers; `docker compose stop/start` drives the
infrastructure dependencies.

**Dashboards & consoles**
- Grafana: http://localhost:3000 (admin/admin) → dashboard **Provider Health**
  (uid `provider-health`): http://localhost:3000/d/provider-health/provider-health
- Prometheus alerts: http://localhost:9090/alerts · graph: http://localhost:9090/graph
- fanout metrics: http://localhost:8090/metrics · core/fanout health below

**Provider Health dashboard panels** (referenced throughout):
*Per-Provider Latency* row → `p50 latency (per provider)`, `p95 latency (per provider)`,
`p99 latency (per provider)`. *Per-Provider Health* row → `Status breakdown (stacked RPS by
status)`, `Breaker state timeline (0 closed / 1 half-open / 2 open)`, `Rate-limited count
(per provider)`, `In-flight calls (per provider)`. *Global* row → `Cache hit ratio
(ClickHouse)`, `Partial-response share (ClickHouse)`, `Total RPS (fan-out, Prometheus)`.

**Quick health probes** (used in several procedures):

```sh
curl -s http://localhost:8000/api/v1/healthz | jq          # core: mysql/redis/fanout
curl -s -o /dev/null -w '%{http_code}\n' http://localhost:8090/healthz   # fanout (Redis)
curl -s http://localhost:8000/api/v1/providers | jq        # per-provider breaker + last_5m
docker compose ps                                          # container health
```

A baseline search used as a probe throughout:

```sh
curl -s -X POST http://localhost:8000/api/v1/search -H 'Content-Type: application/json' \
  -d '{"origin":"BEG","destination":"AMS","depart_date":"2026-07-01","passengers":1}' \
  | jq '{cache, partial, providers}'
```

---

## Scenario 1 — Provider down / breaker open

A single provider is unreachable (process down, connection refused/reset). After repeated
failures its circuit breaker opens and calls short-circuit to `breaker_open`. **User impact
is bounded**: searches still return 200 with results from the other providers and
`"partial": true`.

### Detection

- **Alert `BreakerOpen`** fires (`fanout_breaker_state == 2 for 1m`) — Prometheus
  http://localhost:9090/alerts.
- Alert `ProviderErrorRateHigh` may fire first (status≠ok share > 30% over 5m) while the
  provider is failing but the breaker hasn't yet tripped.
- Grafana: **Breaker state timeline** for the provider climbs to **2 (open)**; **Status
  breakdown** shows `timeout`/`error`/`breaker_open` for that provider; its **p99** spikes
  then flatlines once the breaker short-circuits (latency 0).
- `GET /api/v1/providers` shows the provider with rising `last_5m.error_rate`.

### Diagnose

```sh
# Which provider, and is the breaker actually open in Redis (source of truth)?
docker compose exec -T redis redis-cli HGETALL breaker:d      # state/failures/opened_at

# Prometheus gauge (0 closed / 1 half-open / 2 open):
curl -s --get http://localhost:9090/api/v1/query \
  --data-urlencode 'query=fanout_breaker_state' | jq -r '.data.result[]|"\(.metric.provider)=\(.value[1])"'

# Status split for the suspect provider over the last 5m:
curl -s --get http://localhost:9090/api/v1/query \
  --data-urlencode 'query=sum by (status) (increase(fanout_provider_requests_total{provider="d"}[5m]))' \
  | jq -r '.data.result[]|"\(.metric.status)=\(.value[1])"'

# Is the provider process itself alive? /healthz answers even in 'down' profile:
curl -s http://localhost:8084/healthz; echo          # mock-d -> host port 8084
curl -s http://localhost:8084/admin/chaos; echo      # current chaos profile
docker compose logs --tail=50 mock-d
docker compose logs --tail=50 fanout | grep '"provider":"d"'
```

Confirm the failures are real provider-down signals (`timeout`/`error`), not `bad_payload`
or `rate_limited` (those don't trip the breaker by design).

### Mitigate

- **If this is a real outage**, the breaker is doing its job — it protects the deadline
  budget by short-circuiting. Restore the provider; the breaker auto-recovers via a
  half-open probe after the 15 s cooldown.

```sh
# Rehearsal: recover the mock (clears the 'down' profile)
scripts/chaos.sh d stable          #   or:  make chaos-reset

# Real container crash: restart it
docker compose restart mock-d

# Watch the half-open probe close the breaker (run a few searches after cooldown):
sleep 16
for i in 1 2 3; do
  curl -s -X POST http://localhost:8000/api/v1/search -H 'Content-Type: application/json' \
    -d "{\"origin\":\"BEG\",\"destination\":\"AMS\",\"depart_date\":\"2026-10-0$i\",\"passengers\":1}" \
    | jq -r '.providers.d.status'
done
# Expect: ok  (breaker reset to closed)
docker compose exec -T redis redis-cli HGET breaker:d state    # -> closed
```

- **Optional, to remove a provider from rotation entirely** (e.g. a confirmed long outage),
  disable it in the registry so it's reported `skipped_disabled` instead of consuming a
  goroutine each request:

```sh
docker compose exec -T mysql mysql -uroot -proot firefly \
  -e "UPDATE providers SET enabled=0 WHERE name='d';"
# Re-enable later: UPDATE providers SET enabled=1 WHERE name='d';
```

The provider-config cache (`providers:config` in Redis, TTL 10 s) refreshes within 10 s.

### Verify resolved

`fanout_breaker_state{provider=d}` back to `0`; `BreakerOpen` clears; baseline search shows
`d: ok` and `partial: false`. Reset chaos: `make chaos-reset`.

---

## Scenario 2 — All providers slow / deadline exhaustion

Providers respond, but slowly (the `slow` profile: ~5× base latency, occasional > 10 s). The
fan-out deadline (2000 ms total budget) is exhausted before some/all providers reply; those
are reported `timeout`. Searches stay 200 but increasingly `partial`, and the **whole**
fan-out p99 approaches the deadline.

### Detection

- **Alert `FanoutSlow`** fires (p99 of `fanout_request_duration_seconds` > 1.8s over 5m).
- Grafana: **p95 / p99 latency (per provider)** elevated across providers; **Status
  breakdown** shows growing `timeout` share; **Partial-response share (ClickHouse)** climbs;
  **In-flight calls** elevated (goroutines waiting on slow providers up to the bulkhead cap
  of 8).

### Diagnose

```sh
# Whole-fanout p99 (what FanoutSlow watches):
curl -s --get http://localhost:9090/api/v1/query --data-urlencode \
  'query=histogram_quantile(0.99, sum by (le) (rate(fanout_request_duration_seconds_bucket[5m])))' \
  | jq -r '.data.result[0].value[1]'

# Per-provider p99 — find the slow one(s):
curl -s --get http://localhost:9090/api/v1/query --data-urlencode \
  'query=histogram_quantile(0.99, sum by (provider,le) (rate(fanout_provider_latency_seconds_bucket[5m])))' \
  | jq -r '.data.result[]|"\(.metric.provider)=\(.value[1])"'

# Confirm via the live response which providers are timing out:
curl -s -X POST http://localhost:8000/api/v1/search -H 'Content-Type: application/json' \
  -d '{"origin":"BEG","destination":"AMS","depart_date":"2026-07-15","passengers":1}' \
  | jq '.partial, (.providers|to_entries|map({(.key):.value.status})|add), .meta'

docker compose logs --tail=50 mock-b mock-c     # check current profiles / latencies
```

`meta.elapsed_ms` near `deadline_ms` (2000) confirms deadline exhaustion. A single slow
provider should *not* exhaust the deadline (others return well within budget); broad
slowness points at shared pressure or many providers in `slow`.

### Mitigate

```sh
# Rehearsal recovery: clear the slow profiles
make chaos-reset
# or per provider:
scripts/chaos.sh b stable
scripts/chaos.sh c stable

# Real chronic-slow provider: shed it so it stops eating the deadline budget.
# Either let its breaker trip (slow -> timeouts -> failures -> open) or disable it:
docker compose exec -T mysql mysql -uroot -proot firefly \
  -e "UPDATE providers SET enabled=0 WHERE name='b';"
```

The deadline is the single authority — no per-provider tuning can extend it, by design. The
correct lever is to remove slow providers from the fan-out, not to raise the budget. If a
genuine business need exists, `SEARCH_DEADLINE_MS` on `core` is the only knob; raising it
trades user latency for completeness and must be a deliberate decision.

### Verify resolved

`FanoutSlow` clears; per-provider p99 back under budget; baseline search `partial: false`,
all `ok`. Reset chaos.

---

## Scenario 3 — Redis down (degraded, fail-open)

Redis backs the search cache, breaker state, limiter token buckets, idempotency locks, and
the Laravel queue. **This is an expected-degradation scenario, not an outage of the API.**
Implemented behavior:

- **Cache:** every search misses → full fan-out every time (higher load, higher latency).
- **Breakers fail OPEN:** `Breaker.Allow` returns `true`, `Breaker.Record` is a no-op — calls
  reach providers with **no breaker protection**, logged loudly via the fanout `FailLogger`.
- **Limiter fails OPEN:** `Limiter.Allow` returns `true` — **no client-side rate limiting**;
  the mock's own 429s may now appear as `rate_limited`.
- **Idempotency:** booking creation falls back to the MySQL unique-constraint path (still
  correct, slower).
- **Queue:** ClickHouse logging jobs can't enqueue; logging is degraded (loss-tolerant).

Searches still return **200** the whole time.

### Detection

- core `GET /api/v1/healthz` → 503 with `dependencies.redis.status: down`.
- fanout `GET /healthz` → 503 (it checks Redis connectivity).
- fanout logs: loud fail-open lines from the breaker/limiter `FailLogger`.
- Grafana: **Cache hit ratio (ClickHouse)** drops toward 0; **Total RPS / In-flight** rise
  (no cache absorbing repeats); breaker timeline may go flat/stale (no state being written).

### Diagnose

```sh
docker compose ps redis
docker compose exec -T redis redis-cli PING 2>&1 || echo "redis unreachable"
curl -s http://localhost:8000/api/v1/healthz | jq '.status, .dependencies.redis'
curl -s -o /dev/null -w 'fanout healthz: %{http_code}\n' http://localhost:8090/healthz
docker compose logs --tail=80 fanout | grep -iE 'redis|fail-open|fail open'

# Confirm searches still succeed (degraded, every call a cache miss):
curl -s -X POST http://localhost:8000/api/v1/search -H 'Content-Type: application/json' \
  -d '{"origin":"BEG","destination":"AMS","depart_date":"2026-07-01","passengers":1}' \
  | jq '{cache, partial, n: (.offers|length)}'      # cache will be "miss" repeatedly
```

### Mitigate

```sh
# Rehearsal / restart:
docker compose start redis            # if stopped
docker compose restart redis          # if wedged
docker compose exec -T redis redis-cli PING        # -> PONG

# Confirm recovery:
curl -s http://localhost:8000/api/v1/healthz | jq '.dependencies.redis'   # -> up
# Two identical searches: the second should now be a cache hit again:
for n in 1 2; do curl -s -X POST http://localhost:8000/api/v1/search \
  -H 'Content-Type: application/json' \
  -d '{"origin":"BEG","destination":"AMS","depart_date":"2026-07-02","passengers":1}' \
  | jq -r '.cache'; done            # expect: miss then hit
```

Breaker/limiter state is rebuilt lazily from a clean slate once Redis returns (the Lua
scripts re-load on first use); no manual reset needed. Watch for a brief burst toward
providers immediately after recovery as buckets refill.

### Verify resolved

Both health endpoints 200; cache hits return; fail-open log lines stop. No data fix required
(Redis state is ephemeral by design).

---

## Scenario 4 — ClickHouse down (logging backlog, loss-tolerant)

ClickHouse stores the append-only `provider_calls` analytics log, written **asynchronously**
by the `core-queue` worker via the Redis-backed Laravel queue. **A ClickHouse outage must
degrade nothing user-visible** (SPEC §7.4): the request path never touches ClickHouse
synchronously.

### Detection

- Analytics blind spots: Grafana **Cache hit ratio (ClickHouse)** and **Partial-response
  share (ClickHouse)** panels (and the ClickHouse-backed parts of `GET /providers`'
  `last_5m`) stop updating / go empty.
- `core-queue` logs show failing/retrying `LogProviderCalls` jobs; the Redis queue list
  grows (`laravel_queue:*`).
- **Crucially:** `/search` latency and success are unaffected; `/healthz` does **not**
  include ClickHouse, so it stays 200 (core/redis/fanout still up).

### Diagnose

```sh
docker compose ps clickhouse
curl -s -o /dev/null -w 'clickhouse ping: %{http_code}\n' 'http://localhost:8123/ping'
docker compose logs --tail=80 core-queue | grep -iE 'clickhouse|LogProviderCalls|exception'

# Backlog depth (queued + delayed logging jobs):
docker compose exec -T redis redis-cli LLEN laravel_queue:default
docker compose exec -T redis redis-cli ZCARD laravel_queue:default:delayed

# Confirm user path is healthy despite CH down:
curl -s -X POST http://localhost:8000/api/v1/search -H 'Content-Type: application/json' \
  -d '{"origin":"BEG","destination":"AMS","depart_date":"2026-07-01","passengers":1}' \
  | jq '{cache, partial}'         # still 200, unaffected
```

### Mitigate

```sh
# Bring ClickHouse back:
docker compose start clickhouse       # if stopped
docker compose restart clickhouse     # if wedged
curl -s 'http://localhost:8123/ping'  # -> Ok.

# The queue worker drains the backlog automatically once CH answers. Watch it drain:
docker compose exec -T redis redis-cli LLEN laravel_queue:default
docker compose logs -f --tail=20 core-queue      # jobs succeeding again
```

If the backlog is huge or jobs have exhausted retries, this is **loss-tolerant**: dropping
analytics rows is acceptable and must never block user requests. Clear dead jobs rather than
let them pile up:

```sh
docker compose exec -T core php artisan queue:flush     # clear failed jobs (analytics only)
```

Confirm fresh rows land after recovery:

```sh
curl -s "http://localhost:8123/?query=SELECT%20max(ts)%20FROM%20firefly.provider_calls"
```

### Verify resolved

ClickHouse `/ping` → `Ok.`; queue length returns to ~0; ClickHouse-backed Grafana panels and
`/providers last_5m` repopulate. A gap in `provider_calls` for the outage window is expected
and acceptable.

---

## Appendix — chaos rig cheat sheet

```sh
make chaos-flaky P=b      # 15% 500s, truncated JSON, latency spikes, conn resets
make chaos-slow  P=b      # ~5x latency, occasional >10s
make chaos-down  P=d      # connection refused on /search (health/admin still up)
make chaos-reset          # ALL providers -> stable   (always end an incident with this)
scripts/chaos.sh <a|b|c|d> <stable|flaky|slow|down>   # direct form
```

Provider → host port map: `a`→8081, `b`→8082, `c`→8083, `d`→8084. Always finish an incident
or rehearsal with `make chaos-reset` and confirm `make ps` shows all services healthy.
