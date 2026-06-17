<?php

declare(strict_types=1);

namespace App\Services;

use Illuminate\Support\Facades\Redis;
use Throwable;

/**
 * Minimal Prometheus-style counter store for core metrics (SPEC §12.1):
 *   core_search_requests_total{cache}
 *   core_partial_responses_total
 *   core_bookings_total{replayed}
 *
 * Counters live in a single Redis hash so the /metrics endpoint can render them
 * without a heavyweight client library (allowed-deps constraint, SPEC §18.3).
 * Redis being down must never break the request path, so increments are
 * fire-and-forget (failures swallowed).
 */
class Metrics
{
    private const HASH = 'core:metrics';

    /**
     * @param  array<string, string>  $labels
     */
    public function increment(string $name, array $labels = [], int $by = 1): void
    {
        try {
            Redis::hincrby(self::HASH, $this->field($name, $labels), $by);
        } catch (Throwable) {
            // Metrics are best-effort; never fail a request over them.
        }
    }

    /**
     * Render all counters in Prometheus text exposition format.
     */
    public function render(): string
    {
        try {
            /** @var array<string, string> $raw */
            $raw = Redis::hgetall(self::HASH) ?: [];
        } catch (Throwable) {
            $raw = [];
        }

        // Group samples by metric name for clean # TYPE headers.
        $byName = [];
        foreach ($raw as $field => $value) {
            $name = strstr($field, '{', true) ?: $field;
            $byName[$name][] = $field.' '.(int) $value;
        }

        $out = [];
        foreach ($byName as $name => $samples) {
            $out[] = "# TYPE {$name} counter";
            foreach ($samples as $sample) {
                $out[] = $sample;
            }
        }

        return $out === [] ? "# no core metrics yet\n" : implode("\n", $out)."\n";
    }

    /**
     * @param  array<string, string>  $labels
     */
    private function field(string $name, array $labels): string
    {
        if ($labels === []) {
            return $name;
        }

        ksort($labels);
        $parts = [];
        foreach ($labels as $k => $v) {
            $parts[] = $k.'="'.str_replace('"', '', $v).'"';
        }

        return $name.'{'.implode(',', $parts).'}';
    }
}
