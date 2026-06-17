<?php

declare(strict_types=1);

namespace App\Services;

use Illuminate\Support\Facades\Cache;
use Illuminate\Support\Facades\Redis;
use Throwable;

/**
 * Builds the GET /providers response (SPEC §8.4):
 *   - enabled/config from the registry,
 *   - breaker state from Redis (hash breaker:{provider}, written by fanout, §10.1),
 *   - last_5m metrics from ClickHouse, with the CH query cached 10 s in Redis.
 *
 * Every external read is fail-soft: Redis/ClickHouse being down yields sane
 * defaults ("closed" / zeroed metrics) rather than an error.
 */
class ProviderStatusService
{
    public function __construct(
        private readonly ProviderRepository $providers,
        private readonly ClickHouseClient $clickhouse,
        private readonly int $last5mTtlS,
    ) {}

    /**
     * @return array{providers: array<int, array<string, mixed>>}
     */
    public function snapshot(): array
    {
        $configs = $this->providers->all();
        $last5m = $this->last5mByProvider();

        $out = [];
        foreach ($configs as $cfg) {
            $name = (string) ($cfg['name'] ?? '');
            if ($name === '') {
                continue;
            }
            $out[] = [
                'name' => $name,
                'enabled' => (bool) ($cfg['enabled'] ?? false),
                'breaker' => $this->breakerState($name),
                'last_5m' => $last5m[$name] ?? ['error_rate' => 0.0, 'p99_ms' => 0, 'calls' => 0],
            ];
        }

        return ['providers' => $out];
    }

    /**
     * Read breaker:{provider}.state from Redis (SPEC §10.1). Defaults to "closed"
     * when the hash is missing or Redis is down (fail-open display).
     *
     * Uses the 'fanout' connection (empty key prefix): the breaker hash is written
     * by the Go fanout under the RAW key `breaker:{provider}`, so reading it through
     * the default (prefixed) connection would always miss and report "closed".
     */
    private function breakerState(string $provider): string
    {
        try {
            $state = Redis::connection('fanout')->hget('breaker:'.$provider, 'state');

            return is_string($state) && $state !== '' ? $state : 'closed';
        } catch (Throwable) {
            return 'closed';
        }
    }

    /**
     * last_5m metrics per provider from ClickHouse, cached 10 s (SPEC §8.4).
     *
     * @return array<string, array{error_rate: float, p99_ms: int, calls: int}>
     */
    private function last5mByProvider(): array
    {
        try {
            /** @var array<string, array{error_rate: float, p99_ms: int, calls: int}> $cached */
            $cached = Cache::remember('providers:last5m', $this->last5mTtlS, fn (): array => $this->queryLast5m());

            return $cached;
        } catch (Throwable) {
            return $this->queryLast5m();
        }
    }

    /**
     * @return array<string, array{error_rate: float, p99_ms: int, calls: int}>
     */
    private function queryLast5m(): array
    {
        $sql = <<<'SQL'
            SELECT
                provider,
                count() AS calls,
                quantile(0.99)(latency_ms) AS p99_ms,
                avgIf(1, status != 'ok') AS error_rate
            FROM firefly.provider_calls
            WHERE ts > now() - INTERVAL 5 MINUTE AND cache_hit = 0
            GROUP BY provider
        SQL;

        $rows = $this->clickhouse->select($sql);
        if ($rows === null) {
            return [];
        }

        $out = [];
        foreach ($rows as $row) {
            $provider = (string) ($row['provider'] ?? '');
            if ($provider === '') {
                continue;
            }
            $out[$provider] = [
                'error_rate' => round((float) ($row['error_rate'] ?? 0), 4),
                'p99_ms' => (int) round((float) ($row['p99_ms'] ?? 0)),
                'calls' => (int) ($row['calls'] ?? 0),
            ];
        }

        return $out;
    }
}
