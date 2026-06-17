# mini-FireFly — Flight Provider Aggregation Platform

**Engineering Specification v1.0**
**Status:** Ready for implementation
**Audience:** Autonomous coding agents. This document is the single source of truth. Every contract, schema, parameter, and acceptance criterion needed to build the system without further clarification is defined here. Where a decision is left open, it is explicitly marked `DECISION: implementer's choice` — everything else is normative.

---

## Table of Contents

1. [Purpose & Scope](#1-purpose--scope)
2. [System Overview](#2-system-overview)
3. [Repository Layout](#3-repository-layout)
4. [Canonical Data Model](#4-canonical-data-model)
5. [Service: Mock Providers (chaos rig)](#5-service-mock-providers-chaos-rig)
6. [Service: Go Fan-Out (`fanout`)](#6-service-go-fan-out-fanout)
7. [Service: Laravel Core (`core`)](#7-service-laravel-core-core)
8. [Public HTTP API](#8-public-http-api)
9. [Internal API: core → fanout](#9-internal-api-core--fanout)
10. [Resilience Patterns — Exact Semantics](#10-resilience-patterns--exact-semantics)
11. [Storage Schemas](#11-storage-schemas)
12. [Observability](#12-observability)
13. [Configuration Reference](#13-configuration-reference)
14. [Testing Strategy](#14-testing-strategy)
15. [CI/CD](#15-cicd)
16. [Local Development & Docker Compose](#16-local-development--docker-compose)
17. [Implementation Phases & Acceptance Criteria](#17-implementation-phases--acceptance-criteria)
18. [Work Decomposition for Agent Swarm](#18-work-decomposition-for-agent-swarm)
19. [Conventions & Non-Goals](#19-conventions--non-goals)

---

## 1. Purpose & Scope

### 1.1 What this system is

A **pluggable flight-data aggregator**: a single public API that fans a search request out to N heterogeneous flight-data providers in parallel, normalizes their incompatible response formats into one canonical model, and returns an aggregated result **within a hard deadline**, surviving partial failures, slow providers, rate limits, and malformed payloads.

The engineering value is **not** in flight business logic. It is in the **integration layer**: per-provider adapters, circuit breakers, retries with backoff, rate limiting, caching, idempotency, partial results, and live per-provider health monitoring. Adding a new provider must require exactly one adapter implementation plus one registry entry.

### 1.2 Why mock providers

Real airline/GDS APIs (Amadeus, Sabre, Travelport) are behind paid access and NDAs. The data source is therefore a fleet of **mock provider services** — deliberately hostile fakes that inject latency, errors, rate-limit responses, connection drops, and malformed JSON under configurable "chaos profiles." The aggregator must survive them. The mocks double as a chaos rig for rehearsing incidents (runbook + postmortem artifacts are deliverables).

### 1.3 Tech stack (fixed, non-negotiable)

| Concern | Technology |
|---|---|
| Core API / orchestration | PHP 8.3, Laravel 11 |
| Concurrent fan-out | Go ≥ 1.22 (goroutines + channels, stdlib `net/http`) |
| Mock providers | Go ≥ 1.22 (one binary, N instances via env) |
| Durable storage | MySQL 8 |
| Hot state / cache | Redis 7 |
| Analytics / call log | ClickHouse ≥ 24.x |
| Metrics | Prometheus + Grafana |
| Containerization | Docker, Docker Compose v2 |
| CI/CD | GitHub Actions |

### 1.4 Out of scope (do not build)

- Real provider integrations, payments, seat maps, ticketing.
- Authentication/authorization on the public API (single-tenant demo; a static API key middleware is optional, `DECISION: implementer's choice`).
- Kubernetes manifests (Compose only).
- Frontend UI (Grafana dashboards are the only UI).

---

## 2. System Overview

### 2.1 Component diagram

```
                 ┌─────────────────────────────────────────────────────────┐
                 │                      docker compose                      │
                 │                                                          │
 ┌────────┐      │  ┌────────────┐  HTTP   ┌─────────────┐   HTTP  ┌─────┐  │
 │ Client ├──────┼─►│  core      ├────────►│  fanout     ├────────►│mock-a│ │
 └────────┘ HTTP │  │ (Laravel)  │ /v1/    │  (Go)       │         ├─────┤  │
                 │  │            │ fanout  │             ├────────►│mock-b│ │
                 │  │ - REST API │         │ - goroutine │         ├─────┤  │
                 │  │ - validate │         │   per       ├────────►│mock-c│ │
                 │  │ - cache    │         │   provider  │         ├─────┤  │
                 │  │ - idempot. │         │ - breakers  ├────────►│mock-d│ │
                 │  │ - merge/   │         │ - retries   │         └─────┘  │
                 │  │   sort     │         │ - rate lim  │                  │
                 │  └─┬───┬───┬──┘         └──┬───────┬──┘                  │
                 │    │   │   │               │       │                     │
                 │    ▼   ▼   ▼               ▼       │ /metrics            │
                 │ ┌────┐┌─────┐┌──────────┐ ┌─────┐  ▼                     │
                 │ │MySQL││Redis││ClickHouse│ │Redis│ ┌──────────┐          │
                 │ └────┘└─────┘└──────────┘ └─────┘ │Prometheus│          │
                 │                  ▲                 └────┬─────┘          │
                 │                  │ provider_calls       ▼                │
                 │                  │ (written by core)  ┌───────┐          │
                 │                  └────────────────────│Grafana│          │
                 │                                       └───────┘          │
                 └─────────────────────────────────────────────────────────┘
```

### 2.2 Responsibilities

| Component | Owns | Must NOT do |
|---|---|---|
| **core** (Laravel) | Public API, request validation & normalization, cache lookup/write, idempotent bookings, merging/sorting offers, writing `provider_calls` rows to ClickHouse, provider config CRUD | Talk to mock providers directly; implement concurrency |
| **fanout** (Go) | Parallel provider calls, per-call deadline propagation, retries, circuit breakers, rate limiting, bulkheads, normalization via adapters, Prometheus metrics | Cache search responses; persist anything except breaker/limiter state in Redis |
| **mock-N** (Go) | Deterministic offer generation from seed, chaos injection, per-instance response format | Share code paths with adapters (formats must be defined independently to keep normalization honest) |
| **MySQL** | Provider registry, bookings, idempotency records | — |
| **Redis** | Search-response cache, token buckets, breaker state, idempotency locks | — |
| **ClickHouse** | Append-only `provider_calls` log | — |

### 2.3 Request lifecycle (`POST /api/v1/search`)

1. **core** validates and normalizes the request body (IATA uppercase, date formats, passenger bounds).
2. Computes `cache_key = sha256(canonical_json(normalized_query))`. On Redis hit → return cached response with `"cache": "hit"` and stop.
3. Loads enabled providers from MySQL (`providers.enabled = 1`).
4. Calls **fanout** `POST /v1/fanout` with the normalized query, provider list, and a deadline budget (`SEARCH_DEADLINE_MS`, default **2000 ms**, minus elapsed time, minus a 100 ms merge reserve).
5. **fanout** launches one goroutine per provider. Each goroutine: checks breaker → checks rate limit → acquires bulkhead slot → calls adapter with per-attempt timeout → retries transient failures with backoff → records breaker outcome → emits result into a buffered channel.
6. At the deadline, fanout returns whatever has arrived: normalized offers + a per-provider status map. **It never blocks past the deadline.**
7. **core** merges offers, deduplicates (§4.4), sorts by `price ASC, depart_at ASC`, writes the response to Redis cache (TTL `SEARCH_CACHE_TTL_S`, default **90 s**; only if at least one provider returned `ok`), asynchronously inserts one `provider_calls` row per provider attempt set into ClickHouse, and responds.

### 2.4 Failure philosophy

- One slow or dead provider must never delay or fail the whole request: **partial results are a success mode**, signaled with `"partial": true`.
- An empty result (all providers failed) is still HTTP 200 with `offers: []` and per-provider statuses — the aggregation itself did not fail. HTTP 5xx is reserved for the aggregator's own faults.
- All timeouts flow from a single request-scoped deadline; no component invents its own longer timeout.

---

## 3. Repository Layout

Monorepo. Top-level layout is normative; internal file naming is `DECISION: implementer's choice` within each service's idioms.

```
mini-firefly/
├── README.md                  # quickstart, architecture diagram, demo script
├── SPEC.md                    # this document
├── RUNBOOK.md                 # incident response procedures (Phase 3)
├── POSTMORTEM.md              # rehearsed-incident writeup (Phase 3)
├── docker-compose.yml         # full stack
├── docker-compose.ci.yml      # overrides for CI (no ports, healthcheck waits)
├── Makefile                   # make up / down / test / lint / seed / chaos-*
├── .github/
│   └── workflows/
│       └── ci.yml
├── core/                      # Laravel app
│   ├── Dockerfile
│   ├── app/ ... (standard Laravel 11 skeleton)
│   ├── database/migrations/
│   └── tests/{Unit,Feature}/
├── fanout/                    # Go fan-out service
│   ├── Dockerfile
│   ├── cmd/fanout/main.go
│   ├── internal/
│   │   ├── server/            # HTTP handlers
│   │   ├── fanout/            # orchestration core
│   │   ├── adapter/           # Adapter interface + registry
│   │   ├── providers/         # one package per provider adapter: a/, b/, c/, d/
│   │   ├── breaker/
│   │   ├── limiter/
│   │   ├── bulkhead/
│   │   ├── retry/
│   │   └── model/             # Query, Offer, Segment, statuses
│   └── go.mod
├── mockprovider/              # Go mock provider (single binary, N configs)
│   ├── Dockerfile
│   ├── cmd/mockprovider/main.go
│   ├── internal/
│   │   ├── gen/               # deterministic offer generation
│   │   ├── chaos/             # fault injection
│   │   └── formats/           # per-provider wire formats: a.go, b.go, c.go, d.go
│   └── go.mod
├── fixtures/
│   ├── airports.json          # seed airport list (see §5.4)
│   └── routes.json            # allowed route pairs
├── deploy/
│   ├── prometheus/prometheus.yml
│   ├── grafana/provisioning/  # datasources + dashboard JSON
│   └── clickhouse/init.sql    # schema bootstrap (§11.3)
└── scripts/
    ├── seed.sh                # seed MySQL providers table
    ├── chaos.sh               # toggle chaos profiles (wraps mock admin API)
    └── demo.sh                # scripted demo: healthy → incident → recovery
```

---

## 4. Canonical Data Model

All cross-service payloads are JSON, UTF-8, `snake_case` keys. Times are RFC 3339 with offset (`2026-07-01T08:40:00+02:00`). Money is a **string decimal** (`"142.50"`) plus ISO 4217 currency — never floats on the wire.

### 4.1 `Query` (canonical search query)

```json
{
  "origin": "BEG",
  "destination": "AMS",
  "depart_date": "2026-07-01",
  "return_date": null,
  "passengers": 1
}
```

| Field | Type | Constraints |
|---|---|---|
| `origin` / `destination` | string | exactly 3 chars, uppercase A–Z, must exist in `fixtures/airports.json`, must differ |
| `depart_date` | string | `YYYY-MM-DD`, today ≤ date ≤ today + 365d |
| `return_date` | string \| null | if present, ≥ `depart_date`; null = one-way |
| `passengers` | int | 1–9 |

### 4.2 `Offer` (canonical normalized offer)

```json
{
  "offer_id": "a:7f3c9b2e",
  "provider": "a",
  "price": "142.50",
  "currency": "EUR",
  "depart_at": "2026-07-01T08:40:00+02:00",
  "arrive_at": "2026-07-01T11:05:00+02:00",
  "duration_minutes": 145,
  "stops": 0,
  "segments": [
    {
      "flight_no": "JU351",
      "carrier": "JU",
      "origin": "BEG",
      "destination": "AMS",
      "depart_at": "2026-07-01T08:40:00+02:00",
      "arrive_at": "2026-07-01T11:05:00+02:00"
    }
  ]
}
```

- `offer_id` = `"{provider}:{first 8 hex chars of sha256 of provider's native offer id}"`.
- `stops` = `len(segments) - 1`.
- `duration_minutes` computed from first depart / last arrive.
- Currency: mocks may emit EUR, USD, or RSD. **Normalization converts everything to EUR** using a fixed static table baked into fanout config: `USD→EUR = 0.92`, `RSD→EUR = 0.0085`, `EUR→EUR = 1.0`. (Static on purpose: no external FX dependency. Document this clearly in README.)

### 4.3 Per-provider status enum

Used everywhere a provider outcome is reported (API response, ClickHouse, metrics):

```
ok | timeout | error | rate_limited | breaker_open | bad_payload | skipped_disabled
```

### 4.4 Offer deduplication (in core, after merge)

Two offers are duplicates iff identical `(carrier, flight_no list in order, depart_at list in order)`. Keep the **cheapest**; tie → keep the one from the provider that responded first. Dedup happens across providers (the mocks intentionally overlap routes).

---

## 5. Service: Mock Providers (chaos rig)

One Go binary; behavior is selected entirely by environment variables. Compose runs four instances: `mock-a`, `mock-b`, `mock-c`, `mock-d`.

### 5.1 Environment

| Var | Default | Meaning |
|---|---|---|
| `PROVIDER_ID` | — (required) | `a`/`b`/`c`/`d`; selects wire format |
| `PORT` | `8080` | listen port |
| `CHAOS_PROFILE` | `stable` | `stable` \| `flaky` \| `slow` \| `down` (see §5.3) |
| `CHAOS_SEED` | `42` | RNG seed for chaos decisions (deterministic chaos in tests) |
| `RATE_LIMIT_RPS` | `50` | above this, respond `429` |
| `BASE_LATENCY_MS` | `80` | mean response latency |

### 5.2 Endpoints

| Method | Path | Purpose |
|---|---|---|
| `POST` | `/search` (format varies per provider, see §5.5) | flight search |
| `GET` | `/healthz` | 200 `{"status":"ok","profile":"<current>"}` (returns 200 even in `down` profile — health of the *process*, not the simulated provider) |
| `PUT` | `/admin/chaos` | body `{"profile":"flaky"}` — switch profile at runtime (this is the chaos toggle) |
| `GET` | `/admin/chaos` | current profile |

### 5.3 Chaos profiles (exact semantics)

Probabilities are evaluated per request, in this order; first match wins.

| Profile | Behavior |
|---|---|
| `stable` | latency = `BASE_LATENCY_MS` ± 30% uniform; 1% chance of HTTP 500; otherwise valid response |
| `flaky` | 15% HTTP 500 · 10% truncated JSON body (cut at a random byte > 10, `Content-Type` still JSON, status 200) · 10% latency × 10 · 5% connection reset (close socket without response) · rest normal |
| `slow` | latency = 5 × `BASE_LATENCY_MS` ± 50%; 5% chance latency > 10 s (guaranteed to bust any sane deadline); responses otherwise valid |
| `down` | 100% connection refused — implemented as: `/search` handler closes the connection immediately (`hijack + close`); admin/health endpoints keep working |

Additional always-on hostility regardless of profile: requests beyond `RATE_LIMIT_RPS` (sliding 1 s window) → `429` with body `{"error":"rate limited","retry_after_ms":500}`.

### 5.4 Deterministic offer generation

- Inputs: `(PROVIDER_ID, origin, destination, depart_date, passengers)`.
- `seed = fnv64(provider_id | origin | destination | depart_date)` → seeded PRNG → generate 3–8 offers.
- Same query to the same provider **always yields identical offers** (prices, times, flight numbers). This makes integration tests reproducible and cache behavior verifiable.
- Routes are constrained to pairs in `fixtures/routes.json`; unknown route → 200 with empty offer list (a real provider behavior worth handling).
- `fixtures/airports.json`: ≥ 20 airports, format `[{"iata":"BEG","city":"Belgrade","tz":"Europe/Belgrade"}, ...]`. Include at minimum: BEG, AMS, CDG, FRA, IST, VIE, ZRH, LHR, JFK, MUC, BCN, MAD, FCO, ATH, PRG, BUD, WAW, CPH, ARN, DXB.
- Carrier/price flavor per provider: each provider has a disjoint-ish carrier pool and a price multiplier (a: ×1.0, b: ×0.95, c: ×1.10, d: ×1.02) so cross-provider dedup and "cheapest wins" actually exercise.

### 5.5 Wire formats (the whole point — they must be genuinely different)

**Provider A** — flat JSON, ISO times, price as float, EUR:

```json
{ "results": [ { "id": "A-981", "flight": "JU351", "from": "BEG", "to": "AMS",
  "dep": "2026-07-01T08:40:00+02:00", "arr": "2026-07-01T11:05:00+02:00",
  "price_eur": 142.5, "legs": [] } ] }
```

**Provider B** — nested, unix-epoch seconds, price in cents (int), USD, segments mandatory:

```json
{ "data": { "itineraries": [ { "token": "b81xx", "pricing": { "amount_cents": 15890, "ccy": "USD" },
  "segments": [ { "no": "KL1342", "carrier": "KL", "o": "BEG", "d": "AMS",
  "dep_ts": 1751352000, "arr_ts": 1751360700 } ] } ] } }
```

**Provider C** — XML-ish abomination encoded in JSON (string-typed numbers, `DD.MM.YYYY HH:mm` local times, RSD):

```json
{ "Flights": { "Flight": [ { "Id": "C-55", "Number": "JU351", "Org": "BEG", "Dst": "AMS",
  "Departure": "01.07.2026 08:40", "Arrival": "01.07.2026 11:05",
  "Fare": { "Total": "16690.00", "Currency": "RSD" } } ] } }
```

**Provider D** — newline-delimited JSON (one offer per line, streaming-style), EUR:

```
{"oid":"d-17","fn":"OS772","org":"BEG","dst":"AMS","dep":"2026-07-01T07:10:00+02:00","arr":"2026-07-01T09:20:00+02:00","p":"139.90","cur":"EUR"}
{"oid":"d-18", ...}
```

Provider C times are interpreted in the **origin airport's timezone** (from `airports.json`) — adapters must do real timezone math. Provider B epoch is UTC.

### 5.6 Mock acceptance criteria

- [ ] Same query twice with same env → byte-identical offer payloads (chaos aside).
- [ ] `PUT /admin/chaos {"profile":"down"}` makes `/search` connection-refuse within 1 s, while `/healthz` still answers.
- [ ] Under `flaky`, over 1000 requests, observed fault distribution within ±5 pp of spec.
- [ ] `429` fires at configured RPS with the documented body.

---

## 6. Service: Go Fan-Out (`fanout`)

### 6.1 Core types

```go
type Query struct {
    Origin      string     `json:"origin"`
    Destination string     `json:"destination"`
    DepartDate  string     `json:"depart_date"`            // YYYY-MM-DD
    ReturnDate  *string    `json:"return_date,omitempty"`
    Passengers  int        `json:"passengers"`
}

type Segment struct {
    FlightNo    string    `json:"flight_no"`
    Carrier     string    `json:"carrier"`
    Origin      string    `json:"origin"`
    Destination string    `json:"destination"`
    DepartAt    time.Time `json:"depart_at"`
    ArriveAt    time.Time `json:"arrive_at"`
}

type Offer struct {
    OfferID         string    `json:"offer_id"`
    Provider        string    `json:"provider"`
    Price           string    `json:"price"`     // decimal string, EUR after normalization
    Currency        string    `json:"currency"`  // always "EUR" post-normalization
    DepartAt        time.Time `json:"depart_at"`
    ArriveAt        time.Time `json:"arrive_at"`
    DurationMinutes int       `json:"duration_minutes"`
    Stops           int       `json:"stops"`
    Segments        []Segment `json:"segments"`
}

type ProviderStatus string // ok|timeout|error|rate_limited|breaker_open|bad_payload

type ProviderResult struct {
    Provider  string         `json:"provider"`
    Status    ProviderStatus `json:"status"`
    Offers    []Offer        `json:"offers,omitempty"`
    LatencyMS int64          `json:"latency_ms"`
    Attempts  int            `json:"attempts"`
    Error     string         `json:"error,omitempty"` // sanitized, no internal addrs
}
```

### 6.2 Adapter contract

```go
// Sentinel errors — the fan-out switches behavior on these.
var (
    ErrRateLimited = errors.New("provider rate limited") // no retry; report rate_limited
    ErrBadPayload  = errors.New("malformed payload")     // no retry; report bad_payload
    ErrTimeout     = errors.New("provider timeout")      // retryable
    ErrUpstream    = errors.New("provider 5xx")          // retryable
)

type Adapter interface {
    Name() string
    // Search performs ONE attempt. Retry/breaker/limiter live OUTSIDE the adapter.
    // Must respect ctx cancellation. Must wrap failures in the sentinel errors above
    // (errors.Is must work). Returns offers already normalized to the canonical model.
    Search(ctx context.Context, q Query) ([]Offer, error)
}

// Registry maps provider id → constructor; populated in an init or wire-up file.
// Adding a provider = implement Adapter + add one registry entry. Nothing else.
type Registry map[string]func(cfg ProviderConfig) Adapter
```

`ProviderConfig` (delivered per-request from core, §9): `{name, base_url, timeout_ms, rate_limit_rps, breaker: {...}}`.

### 6.3 Fan-out algorithm (normative)

```go
func (s *Service) FanOut(ctx context.Context, q Query, provs []ProviderConfig) []ProviderResult {
    // ctx already carries the request deadline set by the HTTP handler
    // from the deadline_ms field of the internal request (§9).

    results := make(chan ProviderResult, len(provs))
    var wg sync.WaitGroup

    for _, p := range provs {
        wg.Add(1)
        go func(p ProviderConfig) {
            defer wg.Done()
            start := time.Now()

            if !s.breakers.Allow(p.Name) {
                results <- ProviderResult{Provider: p.Name, Status: StatusBreakerOpen,
                    LatencyMS: 0, Attempts: 0}
                return
            }
            if !s.limiters.Allow(p.Name) { // local token check, Redis-backed
                results <- ProviderResult{Provider: p.Name, Status: StatusRateLimited}
                return
            }
            release, ok := s.bulkheads.Acquire(ctx, p.Name) // cap concurrent calls
            if !ok {
                results <- ProviderResult{Provider: p.Name, Status: StatusError,
                    Error: "bulkhead full"}
                return
            }
            defer release()

            adapter := s.registry.Build(p)
            offers, attempts, err := retry.Do(ctx, s.retryPolicy(p), func(attemptCtx context.Context) ([]Offer, error) {
                return adapter.Search(attemptCtx, q)
            })
            s.breakers.Record(p.Name, err)

            results <- toResult(p.Name, offers, attempts, err, time.Since(start))
        }(p)
    }

    go func() { wg.Wait(); close(results) }()

    var out []ProviderResult
    seen := map[string]bool{}
    for {
        select {
        case r, open := <-results:
            if !open { goto fill }       // all goroutines done before deadline
            out = append(out, r); seen[r.Provider] = true
        case <-ctx.Done():               // deadline: return what we have
            goto fill
        }
    }
fill:
    for _, p := range provs {            // providers that never reported → timeout
        if !seen[p.Name] {
            out = append(out, ProviderResult{Provider: p.Name, Status: StatusTimeout})
        }
    }
    return out
}
```

Normative properties (tests assert these):

1. **Never blocks past the context deadline** — late goroutines write into the buffered channel and are dropped; no goroutine leak (each in-flight HTTP call carries `ctx`, so they unwind promptly).
2. One goroutine per provider per request; bulkhead caps cross-request concurrency per provider.
3. Per-attempt timeout = `min(provider.timeout_ms, time remaining on ctx)`.
4. A provider that produced no result by the deadline is reported `timeout`, not omitted.

### 6.4 HTTP server

- `POST /v1/fanout` — internal API (§9).
- `GET /metrics` — Prometheus.
- `GET /healthz` — checks Redis connectivity; 200/503.
- Graceful shutdown on SIGTERM: stop accepting, drain ≤ 5 s.

### 6.5 fanout acceptance criteria

- [ ] With mock-b in `slow`, `POST /v1/fanout` with `deadline_ms=2000` returns in < 2100 ms with `b: timeout` and other providers `ok`.
- [ ] With mock-c emitting truncated JSON, result is `c: bad_payload`, zero retries for that error class.
- [ ] `go test -race ./...` clean; a leak test (uber-go/goleak) around 100 fan-outs against a `down` provider shows no leaked goroutines.
- [ ] Prices from B (USD cents) and C (RSD strings, local times) normalize to correct EUR decimals and UTC-offset times (golden-file tests).

---

## 7. Service: Laravel Core (`core`)

### 7.1 Responsibilities & internal flow

Controllers stay thin; logic lives in services:

- `SearchService` — normalize → cache check → load providers → call fanout (Guzzle, request timeout = deadline + 200 ms safety) → merge/dedup/sort → cache write → dispatch ClickHouse logging job → shape response.
- `BookingService` — idempotent booking creation (§7.3).
- `ProviderRepository` — provider registry CRUD, cached in Redis 10 s (so breaker-driven status displays stay fresh without hammering MySQL).
- `ClickHouseLogger` — buffered async insert of `provider_calls` rows (queued job; queue driver = Redis). Loss-tolerant: a dropped log row must never fail a user request.

### 7.2 Merging rules (normative)

1. Concatenate offers from all `ok` providers.
2. Deduplicate per §4.4.
3. Sort: `price ASC`, then `depart_at ASC`, then `provider ASC` (total order → deterministic responses → cacheable & testable).
4. `partial = true` iff at least one provider's status ∉ {`ok`}.
5. Cache only when ≥ 1 provider returned `ok`. Never cache an all-failure response.

### 7.3 Idempotent bookings

`POST /api/v1/bookings` requires header `Idempotency-Key` (8–128 chars, `[A-Za-z0-9_-]`). Algorithm:

1. `SET idem:{key} {request_hash} NX EX 86400` in Redis.
2. If set succeeded → fresh: create booking row (`status=confirmed` — no real ticketing), store `(key, request_hash, response_body)` in MySQL `idempotency_keys`, return 201.
3. If key exists → load from MySQL. Same `request_hash` → replay stored response, 200, header `Idempotency-Replayed: true`. Different hash → `409 {"error":"idempotency_key_reuse"}`.
4. Redis down → fall back to MySQL unique-constraint insert on the key (slower, still correct).

### 7.4 core acceptance criteria

- [ ] Identical `/search` twice within TTL → second response served from cache (`"cache":"hit"`), zero fanout calls (assert via fanout request counter in tests).
- [ ] Booking replay with same key+body → identical body, `Idempotency-Replayed: true`; different body → 409.
- [ ] ClickHouse outage degrades nothing user-visible (job retries, request path unaffected).
- [ ] Feature tests run against real Redis/MySQL containers (no Redis mocks for breaker/idempotency paths).

---

## 8. Public HTTP API

Base: `http://localhost:8000/api/v1`. JSON only. Errors: `{"error": "<machine_code>", "message": "<human text>"}`.

### 8.1 `POST /search`

Request: canonical `Query` (§4.1). Validation failure → 422 with Laravel-style field errors.

Response 200:

```json
{
  "request_id": "req_01J9XYZ...",
  "cache": "miss",
  "partial": true,
  "offers": [ { /* Offer, §4.2 */ } ],
  "providers": {
    "a": {"status": "ok",           "latency_ms": 162, "offers": 5, "attempts": 1},
    "b": {"status": "timeout",      "latency_ms": 1900, "offers": 0, "attempts": 2},
    "c": {"status": "breaker_open", "latency_ms": 0,   "offers": 0, "attempts": 0},
    "d": {"status": "ok",           "latency_ms": 95,  "offers": 4, "attempts": 1}
  },
  "meta": {"deadline_ms": 2000, "elapsed_ms": 1955, "total_offers": 9, "deduplicated": 2}
}
```

### 8.2 `POST /bookings`

Headers: `Idempotency-Key` (required). Body:

```json
{ "offer_id": "a:7f3c9b2e", "passenger": { "first_name": "Ilia", "last_name": "K", "email": "x@y.z" } }
```

201 → `{ "booking_id": "bk_...", "status": "confirmed", "offer_id": "...", "created_at": "..." }`.
Unknown `offer_id` format → 422. (Offers aren't persisted; the booking stores the offer_id and passenger snapshot — document this simplification in README.)

### 8.3 `GET /bookings/{id}` → 200 booking or 404

### 8.4 `GET /providers`

```json
{ "providers": [
  { "name": "a", "enabled": true, "breaker": "closed",
    "last_5m": {"error_rate": 0.02, "p99_ms": 310, "calls": 120} }
] }
```

`breaker` read from Redis; `last_5m` from ClickHouse (cache the CH query 10 s in Redis).

### 8.5 `GET /healthz` — aggregate: MySQL ping, Redis ping, fanout `/healthz`. 200 if all up; 503 with per-dependency detail otherwise.

---

## 9. Internal API: core → fanout

`POST /v1/fanout`

```json
{
  "request_id": "req_01J9XYZ...",
  "deadline_ms": 1850,
  "query": { /* canonical Query */ },
  "providers": [
    { "name": "a", "base_url": "http://mock-a:8080", "timeout_ms": 800,
      "rate_limit_rps": 40,
      "breaker": {"failure_threshold": 5, "window_s": 30, "cooldown_s": 15, "half_open_max": 1} }
  ]
}
```

Response 200: `{ "request_id": "...", "results": [ /* ProviderResult, §6.1 */ ] }`.

Provider configs flow **from core's MySQL registry on every request** — fanout holds no provider configuration of its own (stateless w.r.t. config; only breaker/limiter runtime state lives in Redis). This is what makes "add a provider = adapter + registry row" true.

---

## 10. Resilience Patterns — Exact Semantics

### 10.1 Circuit breaker (per provider, shared via Redis)

State machine: `closed → open → half_open → closed`.

- **closed**: count failures in a sliding window (`window_s=30`). Failures = `ErrTimeout`, `ErrUpstream`, connection errors. `ErrRateLimited` and `ErrBadPayload` do **not** trip the breaker (they're not provider-down signals). On reaching `failure_threshold=5` → open, record `opened_at`.
- **open**: all calls short-circuit to `breaker_open`. After `cooldown_s=15` → half_open.
- **half_open**: admit `half_open_max=1` probe call. Success → closed (reset counters). Failure → open, reset cooldown.
- Storage: Redis hash `breaker:{provider}` = `{state, failures, opened_at, probe_inflight}`; transitions via a Lua script for atomicity (two fanout replicas must not both send probes — the demo runs one replica, but correctness shouldn't depend on it).

### 10.2 Retry policy

- Max attempts: **2** (1 initial + 1 retry) — search is latency-sensitive; more retries belong to batch jobs, not interactive fan-outs.
- Retry only `ErrTimeout`, `ErrUpstream`, connection reset. Never `ErrRateLimited` (respect `retry_after_ms` is a non-goal; we just report) or `ErrBadPayload`.
- Backoff before retry: `base 100 ms × 2^(attempt-1)` + full jitter `[0, base)`. Skip the retry entirely if remaining ctx time < expected attempt budget (`timeout_ms`).

### 10.3 Rate limiter (per provider, Redis token bucket)

- Bucket: capacity = `rate_limit_rps`, refill `rate_limit_rps` tokens/s. Implemented as a Lua script (`EVALSHA`): keys `rl:{provider}:{tokens, ts}`.
- Purpose: protect the *provider* from us (client-side politeness), set slightly **below** the mock's own `RATE_LIMIT_RPS` (e.g., 40 vs 50) so the breaker isn't poisoned by self-inflicted 429s.
- On empty bucket: report `rate_limited` immediately; do not queue.

### 10.4 Bulkhead

- Per provider semaphore, `max_concurrent = 8`, in-process (`chan struct{}`). A provider melting down can't consume all fanout goroutine/socket capacity across overlapping requests.

### 10.5 Deadline propagation

- core computes `deadline_ms` budget → fanout sets `context.WithTimeout` → per-attempt context = min(remaining, provider timeout) → Go `http.Request.WithContext` → mock. There is exactly **one** deadline authority (the original request); everything narrows it, nothing extends it.

### 10.6 Cache

- Key: `search:{sha256(canonical_json(query))}`, value = final response body minus `request_id`/`cache` fields, TTL 90 s.
- Canonical JSON = sorted keys, no whitespace, nulls included — implemented identically in one place (core) since only core touches the cache.

---

## 11. Storage Schemas

### 11.1 MySQL (Laravel migrations)

```sql
CREATE TABLE providers (
  id              BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
  name            VARCHAR(16) NOT NULL UNIQUE,         -- 'a','b','c','d'
  base_url        VARCHAR(255) NOT NULL,
  enabled         TINYINT(1) NOT NULL DEFAULT 1,
  timeout_ms      INT NOT NULL DEFAULT 800,
  rate_limit_rps  INT NOT NULL DEFAULT 40,
  breaker_failure_threshold INT NOT NULL DEFAULT 5,
  breaker_window_s          INT NOT NULL DEFAULT 30,
  breaker_cooldown_s        INT NOT NULL DEFAULT 15,
  created_at TIMESTAMP NULL, updated_at TIMESTAMP NULL
);

CREATE TABLE bookings (
  id           BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
  booking_uid  CHAR(26) NOT NULL UNIQUE,               -- ULID, exposed as bk_{ulid}
  offer_id     VARCHAR(64) NOT NULL,
  passenger    JSON NOT NULL,
  status       ENUM('confirmed','cancelled') NOT NULL DEFAULT 'confirmed',
  created_at TIMESTAMP NULL, updated_at TIMESTAMP NULL
);

CREATE TABLE idempotency_keys (
  idem_key      VARCHAR(128) PRIMARY KEY,
  request_hash  CHAR(64) NOT NULL,
  response_body MEDIUMTEXT NOT NULL,
  status_code   SMALLINT NOT NULL,
  created_at    TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
```

Seeder inserts providers a–d pointing at compose hostnames.

### 11.2 Redis key map

| Key | Type | TTL | Writer |
|---|---|---|---|
| `search:{hash}` | string (JSON) | 90 s | core |
| `idem:{key}` | string | 24 h | core |
| `breaker:{provider}` | hash | none | fanout (Lua) |
| `rl:{provider}` | hash | 10 s rolling | fanout (Lua) |
| `providers:config` | string (JSON) | 10 s | core |
| `laravel_queue:*` | list | — | core queue |

### 11.3 ClickHouse (`deploy/clickhouse/init.sql`)

```sql
CREATE DATABASE IF NOT EXISTS firefly;

CREATE TABLE IF NOT EXISTS firefly.provider_calls (
  ts             DateTime64(3) DEFAULT now64(3),
  request_id     String,
  provider       LowCardinality(String),
  status         LowCardinality(String),
  latency_ms     UInt32,
  attempts       UInt8,
  breaker_state  LowCardinality(String),
  cache_hit      UInt8,            -- request-level flag, denormalized per row
  offers_count   UInt16,
  partial        UInt8,
  deadline_ms    UInt16,
  origin         FixedString(3),
  destination    FixedString(3)
) ENGINE = MergeTree
PARTITION BY toYYYYMMDD(ts)
ORDER BY (provider, ts)
TTL toDateTime(ts) + INTERVAL 30 DAY;
```

Canonical analytics queries (ship them in README and wire into Grafana):

```sql
-- p99 latency per provider, 5m buckets
SELECT provider, toStartOfFiveMinutes(ts) AS t, quantile(0.99)(latency_ms) AS p99
FROM firefly.provider_calls WHERE ts > now() - INTERVAL 6 HOUR
GROUP BY provider, t ORDER BY t;

-- error-rate breakdown
SELECT provider, status, count() AS c
FROM firefly.provider_calls WHERE ts > now() - INTERVAL 1 HOUR
GROUP BY provider, status;

-- cache hit ratio over time
SELECT toStartOfFiveMinutes(ts) AS t, avg(cache_hit) AS hit_ratio
FROM firefly.provider_calls GROUP BY t ORDER BY t;
```

---

## 12. Observability

### 12.1 Prometheus metrics (fanout)

| Metric | Type | Labels |
|---|---|---|
| `fanout_provider_requests_total` | counter | `provider`, `status` |
| `fanout_provider_latency_seconds` | histogram (buckets: .05 .1 .2 .4 .8 1.6 3.2) | `provider` |
| `fanout_breaker_state` | gauge (0 closed / 1 half_open / 2 open) | `provider` |
| `fanout_retries_total` | counter | `provider` |
| `fanout_rate_limited_total` | counter | `provider` |
| `fanout_inflight` | gauge | `provider` |
| `fanout_request_duration_seconds` | histogram | — (whole fan-out) |

core exposes (via a metrics endpoint or pushes into the same Prometheus through an exporter, `DECISION: implementer's choice`): `core_search_requests_total{cache}`, `core_partial_responses_total`, `core_bookings_total{replayed}`.

### 12.2 Grafana

Provisioned automatically (`deploy/grafana/provisioning`): one **"Provider Health"** dashboard — per provider row: p50/p95/p99, status breakdown stacked, breaker state timeline, rate-limited count, inflight; plus global row: cache hit ratio (CH datasource), partial-response share, total RPS. Both Prometheus and ClickHouse datasources provisioned.

### 12.3 Alert rules (Prometheus, demo-grade)

- `ProviderErrorRateHigh`: status≠ok share > 30% over 5 m.
- `BreakerOpen`: `fanout_breaker_state == 2` for > 1 m.
- `FanoutSlow`: p99 of `fanout_request_duration_seconds` > 1.8 s over 5 m.

### 12.4 Logging

Structured JSON to stdout everywhere. Mandatory fields: `ts`, `level`, `service`, `request_id`, `provider` (where applicable), `msg`. `request_id` is minted by core (ULID, `req_` prefix), passed to fanout in the body, and echoed by fanout in every log line — full request traceability across services with grep alone.

### 12.5 RUNBOOK.md & POSTMORTEM.md (Phase 3 deliverables)

RUNBOOK must cover, with exact commands and Grafana panel references: (1) provider down / breaker open, (2) all providers slow (deadline exhaustion), (3) Redis down (expected degradation: no cache, breakers fail-open — define and implement fail-open: if Redis is unreachable, allow calls and log loudly), (4) ClickHouse down (logging job backlog). POSTMORTEM documents one rehearsed incident end-to-end (trigger via `scripts/chaos.sh`, detection via alert, diagnosis via dashboard, mitigation, timeline, action items) in blameless format.

---

## 13. Configuration Reference

All services configured by env only (12-factor). Compose sets these; defaults below.

### core (Laravel `.env` extras)

| Var | Default | |
|---|---|---|
| `FANOUT_URL` | `http://fanout:8090` | |
| `SEARCH_DEADLINE_MS` | `2000` | total budget |
| `SEARCH_CACHE_TTL_S` | `90` | |
| `CLICKHOUSE_DSN` | `http://clickhouse:8123` | db `firefly` |
| `DB_*`, `REDIS_*` | standard Laravel | |

### fanout

| Var | Default | |
|---|---|---|
| `PORT` | `8090` | |
| `REDIS_ADDR` | `redis:6379` | |
| `BULKHEAD_MAX` | `8` | per provider |
| `RETRY_MAX_ATTEMPTS` | `2` | |
| `RETRY_BASE_MS` | `100` | |
| `FX_USD_EUR` | `0.92` | |
| `FX_RSD_EUR` | `0.0085` | |

### mockprovider — see §5.1.

---

## 14. Testing Strategy

### 14.1 Unit (fast, no containers)

- **fanout**: adapter format parsing (golden files per provider: valid, truncated, wrong-currency, empty), retry policy decision table, breaker state machine (with fake clock), token bucket math, dedup-irrelevant (dedup is core's).
- **core**: query normalization & cache-key canonicalization, merge/dedup/sort rules (table-driven), idempotency hash logic.
- **mockprovider**: determinism (same seed → same offers), chaos distribution (statistical, seeded).

### 14.2 Integration (docker compose, real Redis/MySQL/CH/mocks)

Driven by `docker-compose.ci.yml`. Scenarios (each is a named test, these are the **contract**):

| # | Scenario | Assert |
|---|---|---|
| I1 | all mocks `stable` | 200, `partial=false`, offers from 4 providers, dedup count > 0 |
| I2 | mock-b `slow` | < deadline+150 ms wall time, `b: timeout`, others ok, `partial=true` |
| I3 | mock-c `flaky`, force truncated JSON | `c: bad_payload`, attempts=1 |
| I4 | mock-d `down`, 6 consecutive searches | breaker for d transitions to open (status `breaker_open` by call ≤ 6); after cooldown + d→`stable`, recovers to ok |
| I5 | repeat identical query within TTL | second is `cache: hit`, fanout not called |
| I6 | booking idempotency | replay & conflict paths per §7.3 |
| I7 | hammer one provider above `rate_limit_rps` | `rate_limited` statuses appear; mock's 429 count stays ~0 (client-side limiter engaged first) |
| I8 | Redis stopped mid-run | searches still 200 (no cache, breakers fail-open), errors logged |

### 14.3 Race & leak

`go test -race` on every package; goleak in fanout integration-style tests (§6.5).

### 14.4 Load smoke (optional Phase 4 nicety)

`k6` or `hey` script: 50 RPS × 60 s against `/search` in mixed-chaos mode; assert p99 < deadline+200 ms and zero 5xx. `DECISION: implementer's choice` of tool.

---

## 15. CI/CD

`.github/workflows/ci.yml`, triggered on PR + push to `main`:

```
jobs:
  lint:        # parallel matrix
    - php: pint --test, phpstan analyse (level 6+)
    - go:  gofmt -l (fail if nonempty), go vet, golangci-lint run (fanout, mockprovider)
  unit:
    - php: pest/phpunit (core, sqlite-in-memory where possible)
    - go:  go test -race ./... (both modules)
  integration:
    needs: [lint, unit]
    - docker compose -f docker-compose.yml -f docker-compose.ci.yml up -d --wait
    - run scenario suite I1–I8 (a test runner container or host-side scripts)
    - docker compose logs on failure (artifact)
  build:
    needs: [integration]
    - docker build all images, tag {sha}, push to GHCR
  deploy:      # optional, main only, environment-gated
    - ssh to VPS, docker compose pull && up -d
```

Healthchecks in compose make `--wait` meaningful (every service defines one). CI must be green before merge (branch protection note in README).

---

## 16. Local Development & Docker Compose

### 16.1 Services in `docker-compose.yml`

`core` (8000), `fanout` (8090), `mock-a/b/c/d` (8081–8084), `mysql` (3306), `redis` (6379), `clickhouse` (8123/9000), `prometheus` (9090), `grafana` (3000). All with healthchecks; `core` `depends_on` with `condition: service_healthy`.

### 16.2 Makefile targets (normative)

```
make up           # compose up -d --wait, run migrations + seed
make down         # compose down -v
make test         # unit both stacks
make itest        # integration suite against running stack
make lint         # all linters
make seed         # reseed providers + fixtures
make chaos-flaky P=b   # scripts/chaos.sh b flaky
make chaos-down  P=d
make chaos-reset       # all → stable
make demo         # scripts/demo.sh: healthy → incident → recovery walkthrough
```

### 16.3 `scripts/demo.sh` (the interview prop)

Scripted sequence with narration echoed to terminal: (1) `make up`, baseline search, show response; (2) repeat → cache hit; (3) `chaos-down d`, loop searches, watch statuses go `error→…→breaker_open`, point to Grafana; (4) recover d, watch half-open probe close the breaker; (5) booking + idempotent replay. Total runtime target < 3 minutes.

---

## 17. Implementation Phases & Acceptance Criteria

Phases are sequential gates; a phase is done only when all its boxes check.

### Phase 1 — MVP fan-out

- [ ] Mock providers a–d serve deterministic offers in four distinct formats; chaos profiles + admin toggle work (§5.6).
- [ ] fanout: registry + 4 adapters + fan-out core; deadline & partial results semantics hold (§6.5 first two boxes).
- [ ] core: `POST /search` end-to-end (validation → fanout → merge/dedup/sort → response per §8.1), no cache/breakers yet.
- [ ] Compose brings the whole stack up with `make up`; README quickstart works on a clean machine.

### Phase 2 — Resilience

- [ ] Circuit breaker per §10.1 (Lua, Redis), retries per §10.2, rate limiting per §10.3, bulkhead per §10.4.
- [ ] Redis cache + idempotent bookings per §7.3/§10.6.
- [ ] Integration scenarios I1–I8 implemented and green in CI.

### Phase 3 — Observability & incident practice

- [ ] Prometheus metrics per §12.1, provisioned Grafana dashboard per §12.2, alert rules per §12.3.
- [ ] ClickHouse logging pipeline (async job) + canonical queries return sane data after a demo run.
- [ ] RUNBOOK.md (4 scenarios) + POSTMORTEM.md (1 rehearsed incident) per §12.5.
- [ ] `make demo` runs the full walkthrough.

### Phase 4 — Packaging

- [ ] CI pipeline per §15 fully green; images pushed to GHCR.
- [ ] README: architecture diagram, quickstart, design decisions (two languages, static FX, fail-open breakers), demo script, "how to add a provider" guide (must be ≤ 10 steps and true).
- [ ] (Optional) load smoke per §14.4; (optional) SSH deploy job.

---

## 18. Work Decomposition for Agent Swarm

Designed for parallel execution. Contracts in §§4–5, 6.1–6.2, 8–11 are **frozen interfaces** — agents build against them without coordinating. Suggested assignment:

| Agent | Workstream | Depends on | Deliverables |
|---|---|---|---|
| **A1** | `mockprovider` service | fixtures only | §5 complete incl. acceptance tests |
| **A2** | `fanout` skeleton: model, server, registry, fan-out core, retry/bulkhead | none (uses §6 contracts; tests against httptest fakes, not A1) | §6.3 properties under unit test |
| **A3** | `fanout` adapters a–d + normalization (FX, timezones) | format specs §5.5 (not A1's code) | golden-file tests |
| **A4** | `fanout` breaker + limiter (Redis Lua) | none | state-machine + bucket unit tests w/ miniredis or real Redis |
| **A5** | `core` Laravel: migrations, SearchService, merge/dedup, API | §8/§9 contracts | feature tests w/ fake fanout |
| **A6** | `core`: cache, idempotent bookings, CH logging job | A5 scaffolding | §7.4 boxes |
| **A7** | infra: compose, healthchecks, Makefile, seeds, prometheus/grafana/clickhouse provisioning | service Dockerfiles from A1/A2/A5 | `make up` green |
| **A8** | integration test suite I1–I8 + `demo.sh` + `chaos.sh` | A1–A7 assembled | CI integration job green |
| **A9** | CI workflow, GHCR, branch protection docs | A8 | §15 |
| **A10** | docs: README, RUNBOOK, POSTMORTEM (runs the rehearsed incident on the assembled stack) | A7/A8 | §12.5, Phase 4 README box |

Integration order: (A1‖A2‖A3‖A4‖A5) → merge A2+A3+A4 → (A6, A7) → A8 → A9/A10. Conflict surface is minimized because each agent owns disjoint directories; the only shared files are `docker-compose.yml` (owned by A7, others submit service blocks as patches) and this SPEC (read-only).

Rules for all agents:

1. **Do not change a frozen contract.** If a contract is unimplementable as written, stop and surface the conflict — do not silently adapt.
2. Every PR ships with tests for its acceptance boxes; CI green is the merge bar.
3. No new external dependencies beyond: Laravel ecosystem (guzzle, pest/phpunit, pint, phpstan, a ClickHouse client lib), Go stdlib + `golang.org/x/*` + prometheus client + goleak + (optionally) miniredis & a decimal lib (`shopspring/decimal`). Anything else needs justification in the PR description.
4. Structured JSON logs with `request_id` from day one (§12.4) — not retrofitted.

---

## 19. Conventions & Non-Goals

- **Style**: PHP — PSR-12 via Pint, PHPStan ≥ level 6. Go — gofmt, golangci-lint default set + `errcheck`, `gocritic`. Commit messages: Conventional Commits.
- **Versioning**: API is `/api/v1`; breaking changes bump the path (won't happen in this project's life, but the convention is stated).
- **Security posture (demo-grade, documented)**: no auth on public API, no TLS inside compose, secrets via env. README must list these explicitly as demo simplifications.
- **Determinism is a feature**: seeded mocks, total-order sorting, canonical JSON. Anywhere randomness is needed (jitter), it must be injectable for tests.
- **Non-goals recap**: real provider APIs, FX feeds, ticketing, auth, Kubernetes, multi-region, GraphQL.

---

*End of specification. Build it.*
