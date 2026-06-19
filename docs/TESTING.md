# Testing Strategy

The tests are organized around resilience behavior, not only happy-path request handling. The goal is to prove that the aggregator still returns a useful response when providers are slow, malformed, rate-limited, down, or recovering.

## Unit tests

PHP unit and feature tests live under `core/tests`.

- API validation is covered by `core/tests/Feature/SearchValidationTest.php`: invalid IATA codes, unknown airports, invalid dates, identical origin/destination, and invalid passenger counts.
- Search orchestration is covered by `core/tests/Feature/SearchTest.php`: response shape, provider filtering, deadline budget, cache miss/hit, partial responses, and all-provider failure not being cached.
- Booking idempotency is covered by `core/tests/Feature/BookingTest.php` and `core/tests/Feature/BookingRedisTest.php`: first booking, same-key replay, conflicting replay body, missing or invalid idempotency key, malformed `offer_id`, and booking lookup.
- Offer merging, canonical JSON, and idempotency hashes are covered by `core/tests/Unit`.

Go unit tests live under `fanout/internal` and `mockprovider/internal`.

- Fan-out behavior is covered by `fanout/internal/fanout/fanout_test.go`: all providers ok, deadline partials, breaker short-circuit, rate limiting, bulkhead full, malformed payload, and upstream errors.
- Deadline leak safety is covered by `fanout/internal/fanout/leak_test.go`.
- Circuit breaker behavior is covered by `fanout/internal/breaker/breaker_test.go`: opens after failures, rejects while open, half-open after cooldown, success closes, failure reopens, sliding window reset, and Redis fail-open behavior.
- Retry, rate limiter, bulkhead, and provider adapter normalization are covered by their package tests.
- Mock provider chaos and payload formats are covered by `mockprovider/internal/*_test.go`.

## Integration tests

The integration suite lives in `tests/integration` and runs against the Docker Compose stack via `make itest`.

- `i1_stable.sh`: healthy baseline search.
- `i2_slow.sh`: slow provider behavior under the hard deadline.
- `i3_bad_payload.sh`: malformed provider payload handling.
- `i4_breaker.sh`: provider outage, breaker opening, recovery, and breaker closing.
- `i5_cache.sh`: first request cache miss, repeated request cache hit, and no fan-out on cache hit.
- `i6_idempotency.sh`: booking creation, idempotent replay, and conflicting replay body.
- `i7_rate_limit.sh`: provider-side rate limiting behavior.
- `i8_redis_down.sh`: Redis degraded mode and documented fail-open behavior.

## Contract tests

The public API contract is documented in `docs/openapi.yaml`. The Laravel feature tests assert the same response fields for search and bookings:

- `POST /api/v1/search` returns `request_id`, `cache`, `partial`, `offers`, `providers`, and `meta`.
- `POST /api/v1/bookings` returns a `bk_` booking id and supports idempotent replay via `Idempotency-Key`.
- `GET /api/v1/bookings/{id}` returns the public booking representation or `404`.

The internal fan-out contract is validated through Go server tests and the integration suite rather than exposed as public OpenAPI.

## Chaos tests

The chaos rig is controlled by `scripts/chaos.sh` and the Make targets:

- `make chaos-slow P=b`;
- `make chaos-down P=d`;
- `make chaos-flaky P=c`;
- `make chaos-reset`.

The integration suite and `make demo` use these controls to verify:

- one provider timing out does not block the response past the deadline;
- one provider returning malformed JSON is isolated to that provider;
- one provider returning 429 is reported as `rate_limited`;
- one provider returning 500 is reported as `error`;
- a down provider opens the circuit breaker after the configured threshold;
- provider recovery drives the breaker through half-open and back to closed.

## What is not tested yet

- End-to-end visual assertions for Grafana dashboard panels.
- Production-like load tests against PHP-FPM or another concurrent HTTP front end.
- Distributed tracing, because tracing is not implemented in this simulation.
- Real provider credential rotation, because mock providers do not use credentials.
- Real payment, ticketing, cancellation, or inventory consistency workflows.
- Long-running retention behavior for ClickHouse analytics data.

## How to run tests locally

Unit and feature tests:

```sh
make test
```

Integration tests against the live stack:

```sh
make up
make itest
make down
```

Linters and static checks:

```sh
make lint
```

Guided resilience demo with assertions:

```sh
make demo
```

## How tests run in CI

GitHub Actions runs `.github/workflows/ci.yml` on pull requests and pushes to `main`.

The pipeline enforces:

- `composer validate`;
- `php artisan test`;
- Laravel Pint;
- PHPStan;
- `go test -race ./...` for both Go modules;
- `gofmt` checks;
- `go vet ./...`;
- Docker Compose integration tests through `make itest`;
- Docker image builds after tests pass.

The Trivy filesystem scan is report-only in the current configuration, so dependency and secret findings are visible without blocking merges by default.
