# Observability

mini-FireFly exposes operational signals through Prometheus metrics, Grafana dashboards, JSON logs, and ClickHouse provider-call analytics. The observability goal is to answer integration-layer questions quickly: which provider is degraded, whether cache is reducing fan-out, whether the breaker is protecting the deadline, and whether logging failures affect the public API.

## Metrics

### API-level metrics

- request count by cache result;
- request latency;
- error count;
- cache hit ratio;
- partial response ratio;
- booking count by replay status.

Engineering questions:

- Is the public API returning too many partial responses?
- Are repeated searches being served from Redis?
- Are booking replays behaving as expected?

### Provider-level metrics

- provider call count;
- provider latency;
- provider status breakdown;
- provider timeout count;
- provider error count;
- provider rate-limited count;
- circuit breaker state;
- in-flight provider calls.

Engineering questions:

- Which provider is currently degraded?
- Is a provider slow, down, malformed, or rate-limited?
- Did the breaker open at the expected time?
- Did a recovered provider return through half-open to closed?

### Queue metrics

The current stack uses Redis-backed Laravel queues for asynchronous ClickHouse writes. Queue health should be inspected through Laravel worker logs and Redis queue keys.

Important signals:

- queued logging jobs;
- failed jobs;
- processing latency;
- worker process health;
- ClickHouse insert failures in logs.

Engineering questions:

- Are provider-call logs keeping up with traffic?
- Are logging failures isolated from public API responses?
- Is the queue worker down or backlogged?

## Dashboards

Grafana is provisioned from `deploy/grafana/provisioning/dashboards/provider-health.json`.

Primary dashboard:

- URL: `http://localhost:3000/d/provider-health/provider-health`
- Login: `admin/admin`
- Dashboard: `Provider Health`

Panel mapping:

| Panel | Question |
|---|---|
| p50/p95/p99 latency per provider | Which provider is slow? |
| Status breakdown | Are failures timeouts, errors, 429s, malformed payloads, or breaker short-circuits? |
| Breaker state timeline | Is the circuit breaker closed, half-open, or open? |
| Cache hit ratio | Is Redis reducing provider fan-out? |
| Partial-response share | Are users receiving degraded-but-successful responses? |
| Total fan-out RPS | How much provider traffic is the fan-out layer generating? |
| In-flight calls | Are calls accumulating near the deadline or bulkhead cap? |

## Logs

The Laravel core logs request-level behavior:

- search cache hits and misses;
- search completion with request id, route, partial flag, offer count, and elapsed time;
- booking creation and replay;
- ClickHouse logging dispatch failures;
- Redis cache or idempotency fallback behavior.

The Go fan-out service logs provider execution details and fail-open behavior when Redis-backed breaker or limiter state is unavailable.

Common commands:

```sh
docker compose logs --tail=100 core
docker compose logs --tail=100 core-queue
docker compose logs --tail=100 fanout
docker compose logs --tail=100 mock-d
```

## ClickHouse analytics queries

Provider call rows are written asynchronously to `firefly.provider_calls`. Cache hits are logged with `cache_hit = 1`, zero latency, and zero attempts so cache-hit-ratio queries can include both hits and misses.

Provider p99 latency:

```sql
SELECT
  provider,
  toStartOfFiveMinutes(ts) AS bucket,
  quantile(0.99)(latency_ms) AS p99_latency_ms
FROM firefly.provider_calls
WHERE ts > now() - INTERVAL 6 HOUR
  AND cache_hit = 0
GROUP BY provider, bucket
ORDER BY bucket, provider;
```

Provider status breakdown:

```sql
SELECT
  provider,
  status,
  count() AS calls
FROM firefly.provider_calls
WHERE ts > now() - INTERVAL 1 HOUR
  AND cache_hit = 0
GROUP BY provider, status
ORDER BY provider, calls DESC;
```

Cache hit ratio:

```sql
SELECT
  toStartOfFiveMinutes(ts) AS bucket,
  avg(cache_hit) AS cache_hit_ratio
FROM firefly.provider_calls
WHERE ts > now() - INTERVAL 6 HOUR
GROUP BY bucket
ORDER BY bucket;
```

Partial response ratio:

```sql
SELECT
  toStartOfFiveMinutes(ts) AS bucket,
  avg(partial) AS partial_ratio
FROM firefly.provider_calls
WHERE ts > now() - INTERVAL 6 HOUR
GROUP BY bucket
ORDER BY bucket;
```

Run a query from the host:

```sh
curl -s "http://localhost:8123/?query=SELECT%20provider%2C%20status%2C%20count()%20FROM%20firefly.provider_calls%20GROUP%20BY%20provider%2C%20status%20FORMAT%20PrettyCompact"
```

## Alerting ideas

Existing alert rules live in `deploy/prometheus/rules/alerts.yml`.

Recommended alerts:

- provider timeout ratio above a threshold for several minutes;
- provider error ratio above a threshold for several minutes;
- provider rate-limited ratio above a threshold;
- circuit breaker open for longer than expected cooldown;
- partial response ratio above a threshold;
- cache hit ratio drops sharply during normal repeated traffic;
- fan-out p99 approaches the hard deadline;
- queue backlog or failed job count increases;
- ClickHouse insert failures observed in `core-queue` logs;
- Redis unavailable, because breakers and limiters fail open.

## Known limitations

- There is no distributed tracing in the current simulation.
- Grafana dashboards are provisioned for local Docker Compose, not a production observability platform.
- Queue metrics are documented operationally but not exported as first-class Prometheus metrics yet.
- ClickHouse logging is loss-tolerant by design; logging failure must not fail public API requests.
- Alert thresholds are illustrative and should be tuned with real traffic volumes in a production system.
