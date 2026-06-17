<?php

declare(strict_types=1);

namespace App\Services;

use App\Models\Provider;
use Illuminate\Support\Facades\Cache;
use Illuminate\Support\Facades\Log;
use Throwable;

/**
 * Provider registry access (SPEC §7.1 / §11.2).
 *
 * The enabled-provider list is cached in Redis under `providers:config` for 10 s
 * so breaker-driven status displays stay fresh without hammering MySQL. The
 * cache is read-through; any write invalidates it (CRUD support for /providers
 * admin needs, kept minimal for the demo).
 *
 * Redis fail-open (SPEC §10/§12.5): if the cache layer is unreachable we fall
 * straight through to MySQL and log loudly rather than failing the request.
 */
class ProviderRepository
{
    private const CACHE_KEY = 'providers:config';

    public function __construct(private readonly int $ttlSeconds) {}

    /**
     * All providers (enabled and disabled), as plain config arrays. Cached.
     *
     * @return array<int, array<string, mixed>>
     */
    public function all(): array
    {
        try {
            return Cache::remember(self::CACHE_KEY, $this->ttlSeconds, fn (): array => $this->loadFromDatabase());
        } catch (Throwable $e) {
            Log::warning('providers:config cache unavailable, reading MySQL directly', [
                'error' => $e->getMessage(),
            ]);

            return $this->loadFromDatabase();
        }
    }

    /**
     * Enabled providers shaped as fanout config blocks (SPEC §9).
     *
     * @return array<int, array<string, mixed>>
     */
    public function enabledFanoutConfigs(): array
    {
        return array_values(array_filter(
            $this->all(),
            static fn (array $p): bool => (bool) ($p['enabled'] ?? false),
        ));
    }

    /**
     * @return array<int, array<string, mixed>>
     */
    private function loadFromDatabase(): array
    {
        return Provider::query()
            ->orderBy('name')
            ->get()
            ->map(static function (Provider $p): array {
                $cfg = $p->toFanoutConfig();
                $cfg['enabled'] = (bool) $p->enabled;

                return $cfg;
            })
            ->all();
    }

    public function forget(): void
    {
        try {
            Cache::forget(self::CACHE_KEY);
        } catch (Throwable $e) {
            Log::warning('failed to invalidate providers:config cache', ['error' => $e->getMessage()]);
        }
    }
}
