# Architecture Decisions

## 1. Why Laravel + Go?

Laravel is used for the public API and business orchestration because it provides mature tools for validation, database access, queues, caching, HTTP responses, migrations, and testing.

Laravel owns validation, provider registry reads, persistence, Redis cache orchestration, idempotent booking behavior, queue dispatching, response shaping, and the public HTTP contract.

Go is used for the fan-out service because provider calls are concurrent, I/O-bound, and deadline-sensitive. A goroutine-per-provider model keeps the implementation simple while allowing the service to enforce a hard request deadline.

The split is intentional: PHP owns application-level behavior, while Go owns deadline-driven concurrency. The service boundary is a small internal HTTP contract: `POST /v1/fanout`.

## 2. Why use a separate fan-out service?

The fan-out service isolates provider integration logic from Laravel request handling. That keeps low-level concurrency, provider adapters, retries, bulkheads, rate limiting, and circuit breaker state in the service that owns those concerns.

The split also makes scaling clearer. API traffic and provider fan-out load are related, but not identical. A separate fan-out service could be scaled or tuned independently if the provider integration layer became the bottleneck.

The contract between `core` and `fanout` is intentionally narrow: Laravel sends a normalized query plus enabled provider config, and fanout returns provider results with statuses, attempts, latency, and offers.

## 3. Why ClickHouse for provider call logging?

Provider calls are append-only events. The common questions are analytical rather than transactional:

- What is p99 latency by provider over a time window?
- Which provider is returning timeouts, 429s, or malformed payloads?
- Is the cache reducing fan-out?
- What share of responses are partial?

ClickHouse is a better fit than OLTP tables for this workload because it is optimized for time-window scans, aggregations, and high-volume append-heavy event logs. MySQL remains the right place for provider registry rows, bookings, and idempotency records.

## 4. Why Redis for hot state?

Redis stores search cache entries, circuit breaker state, rate limiter state, and the Laravel queue transport. These are hot, short-lived, shared states that need to be fast and visible across service replicas.

Atomic Lua scripts are used for breaker and limiter operations where a read-modify-write sequence must behave consistently under concurrency.

Redis data is intentionally treated as ephemeral operational state. If Redis is reset, cache entries and breaker counters rebuild from live traffic.

## 5. Why partial results are treated as a success mode?

External provider failures are expected, not exceptional. The aggregator should not fail completely because one provider is slow, down, malformed, or rate-limited.

When the aggregator itself is functioning, HTTP 200 with `partial: true` is more useful than HTTP 500. The response still contains valid normalized offers from healthy providers and a provider status map that explains degraded providers.

HTTP 5xx is reserved for internal system failures: the API process, validation layer, persistence layer, or fan-out orchestration failing in a way that prevents the aggregator from forming a valid response.

## 6. Why circuit breakers fail open when Redis is unavailable?

Breaker and limiter state live in Redis. If Redis is unavailable, rejecting all search traffic only because protection state is unavailable would turn one infrastructure dependency outage into a full API outage.

The degraded mode is deliberate:

- no shared breaker protection;
- no shared rate limiting;
- cache reads and writes miss or fail;
- provider fan-out still runs;
- the public search API can still return useful responses.

This trade-off increases provider load risk while preserving API availability. In a real system, Redis-down fail-open behavior should be monitored aggressively with alerts, logs, and operational runbooks.

## 7. Why mock providers instead of real airline APIs?

Real airline or GDS APIs introduce legal, commercial, credential, cost, and availability constraints that distract from the project goal.

The project's value is integration resilience, not real flight inventory. Mock providers make resilience behavior deterministic: slow responses, malformed payloads, 429s, 500s, connection failures, timeouts, and recovery can be triggered on demand and verified in tests or demos.

## 8. Why static FX rates?

Currency normalization exists because provider payloads use different currencies and formats. A live FX feed would add another external dependency with its own latency, authentication, outages, and rate limits.

FX is not the focus of this project. Static rates make tests deterministic and cached responses reproducible. The rates are documented as a simplification rather than presented as production-grade pricing behavior.

## 9. What is intentionally simplified?

- no real airline inventory;
- no real ticketing;
- no authentication;
- no multi-tenancy;
- no TLS inside Docker Compose;
- secrets are passed through environment variables;
- Grafana uses demo credentials;
- bookings are simplified and immediately confirmed;
- provider registry is minimal;
- provider credentials and commercial constraints are not modeled;
- infrastructure is local Docker Compose rather than a managed production platform.

## 10. What would change in a real production system?

- authentication and authorization;
- tenant isolation;
- real secret management;
- TLS or mTLS between internal services;
- structured audit logs;
- distributed tracing;
- provider credential rotation;
- stronger booking consistency model;
- real payment and ticketing workflow;
- explicit provider SLAs and commercial error handling;
- deployment to Kubernetes or another orchestrator;
- alerting rules and incident response policies;
- capacity planning and load testing against production-like infrastructure;
- data retention, privacy, and compliance controls.
