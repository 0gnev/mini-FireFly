<?php

declare(strict_types=1);

namespace App\Services;

use App\Jobs\LogProviderCalls;
use App\Support\CanonicalJson;
use Illuminate\Support\Facades\Cache;
use Illuminate\Support\Facades\Log;
use Illuminate\Support\Str;
use Throwable;

/**
 * Search orchestration (SPEC §2.3 / §7.1 / §7.2).
 *
 * Lifecycle:
 *   1. (caller normalizes/validates the query)
 *   2. cache_key = sha256(canonical_json(query)); on Redis hit → return cached + cache:"hit"
 *   3. load enabled providers from the registry
 *   4. call fanout with deadline budget = SEARCH_DEADLINE_MS − elapsed − merge reserve
 *   5. merge/dedup/sort offers; partial = any provider status ≠ ok
 *   6. cache the response (TTL) iff ≥ 1 provider returned ok
 *   7. dispatch the ClickHouse logging job; shape and return the response
 *
 * Redis fail-open (SPEC §10/§12.5): cache read/write failures are logged and the
 * request proceeds without caching, never erroring.
 */
class SearchService
{
    public function __construct(
        private readonly ProviderRepository $providers,
        private readonly FanoutClient $fanout,
        private readonly OfferMerger $merger,
        private readonly Metrics $metrics,
        private readonly int $deadlineMs,
        private readonly int $mergeReserveMs,
        private readonly int $cacheTtlS,
    ) {}

    /**
     * @param  array{origin: string, destination: string, depart_date: string, return_date: string|null, passengers: int}  $query
     * @return array<string, mixed> the public response body (SPEC §8.1)
     */
    public function search(array $query): array
    {
        $startedAt = microtime(true);
        $requestId = 'req_'.strtoupper((string) Str::ulid());

        $cacheKey = CanonicalJson::searchCacheKey($query);

        // --- Step 2: cache lookup -------------------------------------------
        $cached = $this->readCache($cacheKey);
        if ($cached !== null) {
            $this->metrics->increment('core_search_requests_total', ['cache' => 'hit']);
            Log::info('search cache hit', $this->logCtx($requestId, $query));
            $this->dispatchCacheHitLogging($requestId, $query, $cached);

            return $this->withRequestMeta($cached, $requestId, 'hit');
        }

        $this->metrics->increment('core_search_requests_total', ['cache' => 'miss']);

        // --- Step 3: enabled providers --------------------------------------
        $providers = $this->providers->enabledFanoutConfigs();

        // --- Step 4: deadline budget for fanout (SPEC §2.3 step 4) ----------
        $elapsedMs = (int) round((microtime(true) - $startedAt) * 1000);
        $budgetMs = max(1, $this->deadlineMs - $elapsedMs - $this->mergeReserveMs);

        $results = $this->fanout->fanout($requestId, $budgetMs, $query, $providers);

        // Transport failure or no enabled providers → synthesize an all-failed
        // status map so the response shape is still complete (SPEC §2.4).
        $results = $this->reconcileResults($results, $providers);

        // --- Step 5: merge/dedup/sort ---------------------------------------
        $merged = $this->merger->merge($results);

        $statuses = array_map(static fn (array $r): string => (string) ($r['status'] ?? 'error'), $results);
        $partial = $this->isPartial($statuses);
        $anyOk = in_array('ok', $statuses, true);

        $elapsedMs = (int) round((microtime(true) - $startedAt) * 1000);

        $providersMap = $this->buildProvidersMap($results);

        $body = [
            'partial' => $partial,
            'offers' => $merged['offers'],
            'providers' => $providersMap,
            'meta' => [
                'deadline_ms' => $this->deadlineMs,
                'elapsed_ms' => $elapsedMs,
                'total_offers' => $merged['total_offers'],
                'deduplicated' => $merged['deduplicated'],
            ],
        ];

        // --- Step 6: cache write (only when ≥ 1 ok) -------------------------
        if ($anyOk) {
            $this->writeCache($cacheKey, $body);
        }

        if ($partial) {
            $this->metrics->increment('core_partial_responses_total');
        }

        // --- Step 7: async ClickHouse logging -------------------------------
        $this->dispatchLogging($requestId, $query, $results, false, $partial, $merged['offers']);

        Log::info('search completed', $this->logCtx($requestId, $query) + [
            'partial' => $partial,
            'offers' => count($merged['offers']),
            'elapsed_ms' => $elapsedMs,
        ]);

        return $this->withRequestMeta($body, $requestId, 'miss');
    }

    /**
     * Decide partiality (SPEC §7.2.4: partial iff any status ∉ {ok}).
     *
     * @param  array<int, string>  $statuses
     */
    private function isPartial(array $statuses): bool
    {
        if ($statuses === []) {
            return true;
        }

        foreach ($statuses as $s) {
            if ($s !== 'ok') {
                return true;
            }
        }

        return false;
    }

    /**
     * Build the providers map (SPEC §8.1): name → {status, latency_ms, offers, attempts}.
     *
     * @param  array<int, array<string, mixed>>  $results
     * @return array<string, array<string, mixed>>
     */
    private function buildProvidersMap(array $results): array
    {
        $map = [];
        foreach ($results as $r) {
            $name = (string) ($r['provider'] ?? '');
            if ($name === '') {
                continue;
            }
            $map[$name] = [
                'status' => (string) ($r['status'] ?? 'error'),
                'latency_ms' => (int) ($r['latency_ms'] ?? 0),
                'offers' => count((array) ($r['offers'] ?? [])),
                'attempts' => (int) ($r['attempts'] ?? 0),
            ];
        }
        ksort($map);

        return $map;
    }

    /**
     * Ensure every enabled provider appears in the results. If fanout failed
     * entirely (null), mark all providers as errored so the status map is whole.
     *
     * @param  array<int, array<string, mixed>>|null  $results
     * @param  array<int, array<string, mixed>>  $providers
     * @return array<int, array<string, mixed>>
     */
    private function reconcileResults(?array $results, array $providers): array
    {
        if ($results === null) {
            return array_map(static fn (array $p): array => [
                'provider' => (string) ($p['name'] ?? ''),
                'status' => 'error',
                'offers' => [],
                'latency_ms' => 0,
                'attempts' => 0,
                'error' => 'fanout unavailable',
            ], $providers);
        }

        $seen = [];
        foreach ($results as $r) {
            $seen[(string) ($r['provider'] ?? '')] = true;
        }
        foreach ($providers as $p) {
            $name = (string) ($p['name'] ?? '');
            if ($name !== '' && ! isset($seen[$name])) {
                $results[] = [
                    'provider' => $name,
                    'status' => 'timeout',
                    'offers' => [],
                    'latency_ms' => 0,
                    'attempts' => 0,
                ];
            }
        }

        return $results;
    }

    /**
     * @param  array{origin: string, destination: string, depart_date: string, return_date: string|null, passengers: int}  $query
     * @param  array<int, array<string, mixed>>  $results
     * @param  array<int, array<string, mixed>>  $offers
     */
    private function dispatchLogging(
        string $requestId,
        array $query,
        array $results,
        bool $cacheHit,
        bool $partial,
        array $offers,
    ): void {
        try {
            $rows = [];
            foreach ($results as $r) {
                $rows[] = [
                    'request_id' => $requestId,
                    'provider' => (string) ($r['provider'] ?? ''),
                    'status' => (string) ($r['status'] ?? 'error'),
                    'latency_ms' => (int) ($r['latency_ms'] ?? 0),
                    'attempts' => (int) ($r['attempts'] ?? 0),
                    'breaker_state' => (string) ($r['breaker_state'] ?? ''),
                    'cache_hit' => $cacheHit ? 1 : 0,
                    'offers_count' => count((array) ($r['offers'] ?? [])),
                    'partial' => $partial ? 1 : 0,
                    'deadline_ms' => $this->deadlineMs,
                    'origin' => $query['origin'],
                    'destination' => $query['destination'],
                ];
            }

            LogProviderCalls::dispatch($rows);
        } catch (Throwable $e) {
            // Dispatch failures (e.g. Redis queue down) must not fail the request.
            Log::warning('failed to dispatch provider_calls logging job', [
                'request_id' => $requestId,
                'error' => $e->getMessage(),
            ]);
        }
    }

    /**
     * On a cache hit, log one provider_calls row per provider with cache_hit=1 so
     * the cache-hit-ratio analytics (§11.3) is meaningful. No provider was actually
     * called, so latency/attempts are 0; these rows are excluded from the
     * provider-performance queries (which filter cache_hit = 0). Loss-tolerant.
     *
     * @param  array{origin: string, destination: string, depart_date: string, return_date: string|null, passengers: int}  $query
     * @param  array<string, mixed>  $cached  the cached response body
     */
    private function dispatchCacheHitLogging(string $requestId, array $query, array $cached): void
    {
        try {
            /** @var array<string, mixed> $providers */
            $providers = is_array($cached['providers'] ?? null) ? $cached['providers'] : [];
            $partial = (bool) ($cached['partial'] ?? false);
            /** @var array<string, mixed> $meta */
            $meta = is_array($cached['meta'] ?? null) ? $cached['meta'] : [];
            $deadlineMs = (int) ($meta['deadline_ms'] ?? $this->deadlineMs);

            $rows = [];
            foreach ($providers as $name => $p) {
                $p = is_array($p) ? $p : [];
                $rows[] = [
                    'request_id' => $requestId,
                    'provider' => (string) $name,
                    'status' => (string) ($p['status'] ?? 'ok'),
                    'latency_ms' => 0,
                    'attempts' => 0,
                    'breaker_state' => '',
                    'cache_hit' => 1,
                    'offers_count' => (int) ($p['offers'] ?? 0),
                    'partial' => $partial ? 1 : 0,
                    'deadline_ms' => $deadlineMs,
                    'origin' => $query['origin'],
                    'destination' => $query['destination'],
                ];
            }

            if ($rows !== []) {
                LogProviderCalls::dispatch($rows);
            }
        } catch (Throwable $e) {
            Log::warning('failed to dispatch cache-hit provider_calls logging job', [
                'request_id' => $requestId,
                'error' => $e->getMessage(),
            ]);
        }
    }

    /**
     * @return array<string, mixed>|null
     */
    private function readCache(string $key): ?array
    {
        try {
            $value = Cache::get($key);

            return is_array($value) ? $value : null;
        } catch (Throwable $e) {
            Log::warning('search cache read failed; proceeding without cache', ['error' => $e->getMessage()]);

            return null;
        }
    }

    /**
     * @param  array<string, mixed>  $body
     */
    private function writeCache(string $key, array $body): void
    {
        try {
            Cache::put($key, $body, $this->cacheTtlS);
        } catch (Throwable $e) {
            Log::warning('search cache write failed', ['error' => $e->getMessage()]);
        }
    }

    /**
     * Attach request_id + cache fields (which are excluded from the cached body,
     * SPEC §10.6) and return the final response.
     *
     * @param  array<string, mixed>  $body
     * @return array<string, mixed>
     */
    private function withRequestMeta(array $body, string $requestId, string $cache): array
    {
        return ['request_id' => $requestId, 'cache' => $cache] + $body;
    }

    /**
     * @param  array{origin: string, destination: string, depart_date: string, return_date: string|null, passengers: int}  $query
     * @return array<string, mixed>
     */
    private function logCtx(string $requestId, array $query): array
    {
        return [
            'request_id' => $requestId,
            'origin' => $query['origin'],
            'destination' => $query['destination'],
            'depart_date' => $query['depart_date'],
        ];
    }
}
