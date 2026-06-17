<?php

declare(strict_types=1);

namespace App\Jobs;

use App\Services\ClickHouseClient;
use Illuminate\Bus\Queueable;
use Illuminate\Contracts\Queue\ShouldQueue;
use Illuminate\Foundation\Bus\Dispatchable;
use Illuminate\Queue\InteractsWithQueue;
use Illuminate\Queue\SerializesModels;
use Illuminate\Support\Facades\Log;
use Throwable;

/**
 * Async ClickHouse logging of provider_calls rows (SPEC §7.1 / §11.3).
 *
 * Queued on the Redis driver. LOSS-TOLERANT: this job runs entirely off the
 * request path, and a permanently dropped log row must never affect a user
 * request (SPEC §7.4). We retry a few times then give up quietly.
 *
 * One row per provider attempt-set, with request-level fields (cache_hit,
 * partial, deadline_ms, origin, destination) denormalized onto every row.
 */
class LogProviderCalls implements ShouldQueue
{
    use Dispatchable;
    use InteractsWithQueue;
    use Queueable;
    use SerializesModels;

    /** Bounded retries; logging is best-effort. */
    public int $tries = 3;

    public int $backoff = 5;

    /**
     * @param  array<int, array<string, mixed>>  $rows  pre-built associative rows
     */
    public function __construct(public array $rows) {}

    public function handle(ClickHouseClient $clickhouse): void
    {
        if ($this->rows === []) {
            return;
        }

        $columns = [
            'request_id', 'provider', 'status', 'latency_ms', 'attempts',
            'breaker_state', 'cache_hit', 'offers_count', 'partial',
            'deadline_ms', 'origin', 'destination',
        ];

        $positional = [];
        foreach ($this->rows as $row) {
            $positional[] = array_map(static fn (string $c) => $row[$c] ?? null, $columns);
        }

        $ok = $clickhouse->insertProviderCalls($positional, $columns);

        if (! $ok) {
            // Throw so the queue retries; once $tries is exhausted the row is
            // dropped (see failed()). The user request already returned long ago.
            throw new \RuntimeException('clickhouse insert returned false');
        }
    }

    public function failed(?Throwable $e): void
    {
        Log::warning('provider_calls logging permanently dropped after retries', [
            'error' => $e?->getMessage(),
            'rows' => count($this->rows),
        ]);
    }
}
