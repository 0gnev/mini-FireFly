# Runbook

Operational procedures for the mini-FireFly Docker Compose stack. Commands are intended to be run from the repository root.

This is a production-style simulation. The procedures describe realistic operational behavior, but the services run locally with mock providers and demo credentials.

## Service URLs

| Service | URL |
|---|---|
| Public API | `http://localhost:8000/api/v1` |
| Core health | `http://localhost:8000/api/v1/healthz` |
| Provider registry | `http://localhost:8000/api/v1/providers` |
| Fan-out health | `http://localhost:8090/healthz` |
| Fan-out metrics | `http://localhost:8090/metrics` |
| Prometheus | `http://localhost:9090` |
| Grafana | `http://localhost:3000` (`admin/admin`) |
| Grafana Provider Health dashboard | `http://localhost:3000/d/provider-health/provider-health` |
| ClickHouse HTTP | `http://localhost:8123` |
| Mock provider A | `http://localhost:8081` |
| Mock provider B | `http://localhost:8082` |
| Mock provider C | `http://localhost:8083` |
| Mock provider D | `http://localhost:8084` |

## Common commands

```sh
make up
make down
make test
make lint
make itest
make demo
make ps
make logs
make chaos-reset
```

Quick health probes:

```sh
curl -s http://localhost:8000/api/v1/healthz | jq
curl -s -o /dev/null -w '%{http_code}\n' http://localhost:8090/healthz
curl -s http://localhost:8000/api/v1/providers | jq
docker compose ps
```

Baseline search:

```sh
curl -s -X POST http://localhost:8000/api/v1/search \
  -H 'Content-Type: application/json' \
  -d '{"origin":"BEG","destination":"AMS","depart_date":"2026-07-20","passengers":1}' \
  | jq '{cache, partial, providers, meta}'
```

## How to start the stack

```sh
make up
```

`make up` runs Docker Compose, waits for services, applies Laravel migrations, and seeds providers `a` through `d`.

Verify:

```sh
curl -s http://localhost:8000/api/v1/healthz | jq '.status'
curl -s http://localhost:8090/healthz
```

## How to stop the stack

```sh
make down
```

This removes Compose volumes. Use it when you want a clean local state.

## How to reset data

Reset provider rows and fixtures:

```sh
make seed
```

Reset provider chaos profiles:

```sh
make chaos-reset
```

Reset all Compose data:

```sh
make down
make up
```

Clear Redis breaker state during a local rehearsal:

```sh
docker compose exec -T redis redis-cli DEL breaker:a breaker:b breaker:c breaker:d
```

## How to run tests

Unit and feature tests:

```sh
make test
```

Static checks:

```sh
make lint
```

Integration scenarios against a running stack:

```sh
make up
make itest
```

Guided operational demo:

```sh
make demo
```

## How to simulate provider incidents

Use the chaos rig:

```sh
make chaos-slow P=b
make chaos-down P=d
make chaos-flaky P=c
make chaos-reset
```

Direct form:

```sh
scripts/chaos.sh <a|b|c|d> <stable|flaky|slow|down>
```

Provider host ports:

| Provider | Port |
|---|---|
| a | `8081` |
| b | `8082` |
| c | `8083` |
| d | `8084` |

Inspect a provider chaos profile:

```sh
curl -s http://localhost:8084/admin/chaos; echo
```

## How to inspect API behavior

Search response:

```sh
curl -s -X POST http://localhost:8000/api/v1/search \
  -H 'Content-Type: application/json' \
  -d '{"origin":"BEG","destination":"AMS","depart_date":"2026-07-21","passengers":1}' \
  | jq
```

Booking creation:

```sh
curl -s -D - -X POST http://localhost:8000/api/v1/bookings \
  -H 'Content-Type: application/json' \
  -H 'Idempotency-Key: runbook-demo-0001' \
  -d '{"offer_id":"a:7f3c9b2e","passenger":{"first_name":"Ilia","last_name":"K","email":"x@y.z"}}'
```

Provider registry and recent health:

```sh
curl -s http://localhost:8000/api/v1/providers | jq
```

## How to inspect Redis

Ping:

```sh
docker compose exec -T redis redis-cli PING
```

Breaker state:

```sh
docker compose exec -T redis redis-cli HGETALL breaker:d
```

Queue depth:

```sh
docker compose exec -T redis redis-cli LLEN laravel_queue:default
docker compose exec -T redis redis-cli ZCARD laravel_queue:default:delayed
```

Cache keys:

```sh
docker compose exec -T redis redis-cli --scan --pattern '*search*'
```

## How to inspect ClickHouse

Ping:

```sh
curl -s http://localhost:8123/ping
```

Recent provider calls:

```sh
curl -s "http://localhost:8123/?query=SELECT%20provider%2C%20status%2C%20count()%20FROM%20firefly.provider_calls%20WHERE%20ts%20%3E%20now()%20-%20INTERVAL%201%20HOUR%20GROUP%20BY%20provider%2C%20status%20FORMAT%20PrettyCompact"
```

Queue worker logs:

```sh
docker compose logs --tail=80 core-queue
```

## How to inspect provider health

Grafana:

```text
http://localhost:3000/d/provider-health/provider-health
```

Prometheus breaker state:

```sh
curl -s --get http://localhost:9090/api/v1/query \
  --data-urlencode 'query=fanout_breaker_state' \
  | jq -r '.data.result[] | "\(.metric.provider)=\(.value[1])"'
```

Provider status split:

```sh
curl -s --get http://localhost:9090/api/v1/query \
  --data-urlencode 'query=sum by (provider,status) (increase(fanout_provider_requests_total[5m]))' \
  | jq -r '.data.result[] | "\(.metric.provider) \(.metric.status)=\(.value[1])"'
```

Per-provider logs:

```sh
docker compose logs --tail=80 fanout
docker compose logs --tail=80 mock-d
```

## Known failure scenarios

### Provider is slow

Simulate:

```sh
make chaos-slow P=b
```

Expected API behavior:

- `POST /api/v1/search` still returns HTTP 200.
- Provider `b` may show elevated `latency_ms` or `timeout`.
- `partial` becomes `true` if at least one provider times out.
- `meta.elapsed_ms` should remain bounded by the hard deadline plus small overhead.

Expected Grafana behavior:

- provider `b` latency rises;
- timeout share may increase;
- partial-response share may rise;
- fan-out p99 approaches the deadline if enough providers are slow.

Recover:

```sh
scripts/chaos.sh b stable
make chaos-reset
```

Verify:

```sh
curl -s -X POST http://localhost:8000/api/v1/search \
  -H 'Content-Type: application/json' \
  -d '{"origin":"BEG","destination":"AMS","depart_date":"2026-07-22","passengers":1}' \
  | jq '.partial, .providers.b'
```

### Provider is down

Simulate:

```sh
make chaos-down P=d
```

Expected provider status:

- initial calls report `timeout` or `error`;
- repeated failures open the circuit breaker;
- later calls report `breaker_open`;
- the public response remains HTTP 200 with `partial: true`.

Expected breaker behavior:

- breaker opens after the configured failure threshold;
- calls short-circuit while open;
- after cooldown, one half-open probe is allowed;
- a successful probe closes the breaker.

Diagnose:

```sh
docker compose exec -T redis redis-cli HGETALL breaker:d
curl -s --get http://localhost:9090/api/v1/query \
  --data-urlencode 'query=fanout_breaker_state{provider="d"}' | jq
docker compose logs --tail=50 fanout | grep '"provider":"d"'
```

Recover:

```sh
scripts/chaos.sh d stable
sleep 16
for i in 1 2 3; do
  curl -s -X POST http://localhost:8000/api/v1/search \
    -H 'Content-Type: application/json' \
    -d "{\"origin\":\"BEG\",\"destination\":\"AMS\",\"depart_date\":\"2026-08-0$i\",\"passengers\":1}" \
    | jq -r '.providers.d.status'
done
```

Expected result after recovery: `d` returns `ok`, `partial` returns to `false`, and `fanout_breaker_state{provider="d"}` returns to `0`.

### Redis is down

Simulate:

```sh
docker compose stop redis
```

Expected cache behavior:

- search cache reads and writes fail open;
- repeated searches become `cache: miss`;
- fan-out traffic increases because Redis is not absorbing repeated queries.

Expected breaker/rate limiter degraded behavior:

- circuit breakers fail open;
- rate limiter state is unavailable and fails open;
- provider traffic is allowed without shared protection;
- fanout logs should make fail-open behavior visible.

Risks:

- provider load may increase;
- breaker protection is temporarily unavailable;
- queue-backed ClickHouse logging cannot enqueue normally;
- booking idempotency uses the MySQL fallback path.

Recover:

```sh
docker compose start redis
docker compose exec -T redis redis-cli PING
```

Verify cache recovery:

```sh
for n in 1 2; do
  curl -s -X POST http://localhost:8000/api/v1/search \
    -H 'Content-Type: application/json' \
    -d '{"origin":"BEG","destination":"AMS","depart_date":"2026-07-23","passengers":1}' \
    | jq -r '.cache'
done
```

Expected result: `miss` then `hit`.

### ClickHouse is down

Simulate:

```sh
docker compose stop clickhouse
```

Expected API behavior:

- search and booking APIs continue working;
- `/healthz` does not fail solely because ClickHouse is down;
- ClickHouse-backed analytics panels stop updating.

Expected logging behavior:

- `core-queue` logs insert failures and retries;
- provider-call analytics may have a gap;
- logging failure must not fail the public API.

Recover:

```sh
docker compose start clickhouse
curl -s http://localhost:8123/ping
docker compose logs --tail=50 core-queue
```

If failed jobs are only analytics rows and retries are exhausted:

```sh
docker compose exec -T core php artisan queue:flush
```

### Queue worker is down

Simulate:

```sh
docker compose stop core-queue
```

How to detect:

```sh
docker compose ps core-queue
docker compose exec -T redis redis-cli LLEN laravel_queue:default
docker compose logs --tail=80 core-queue
```

Expected side effects:

- public search API continues working;
- provider-call rows stop reaching ClickHouse;
- Redis queue depth grows;
- Grafana panels backed by ClickHouse become stale.

Recover:

```sh
docker compose start core-queue
docker compose logs -f --tail=20 core-queue
docker compose exec -T redis redis-cli LLEN laravel_queue:default
```

Expected result: the queue drains and ClickHouse receives fresh provider-call rows.

## Recovery procedures

Use this order during a local incident rehearsal:

1. Confirm public API behavior with a search request.
2. Identify degraded dependency with `docker compose ps`, `/healthz`, Grafana, Prometheus, and logs.
3. Restore the dependency or reset the relevant chaos profile.
4. Verify the API response shape and provider status map.
5. Verify operational state: breaker closed, cache hit restored, queue drained, or ClickHouse receiving rows.
6. Run `make chaos-reset` after provider incidents.
7. Record any behavior gap in an issue using `.github/ISSUE_TEMPLATE/engineering_task.md`.
