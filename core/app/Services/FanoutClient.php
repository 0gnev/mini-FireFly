<?php

declare(strict_types=1);

namespace App\Services;

use GuzzleHttp\Client;
use GuzzleHttp\Exception\GuzzleException;
use Illuminate\Support\Facades\Log;

/**
 * HTTP client for the internal core → fanout API (SPEC §9).
 *
 * The Guzzle request timeout is the search deadline plus a safety margin
 * (SPEC §7.1: "request timeout = deadline + 200 ms safety"), so the HTTP call
 * itself never out-waits fanout's own deadline handling.
 *
 * On any transport-level failure this returns null; the caller (SearchService)
 * treats that as "no results from fanout" and still produces a 200 with every
 * provider marked as failed — the aggregator's contract is that provider faults
 * are a success mode (SPEC §2.4).
 */
class FanoutClient
{
    public function __construct(
        private readonly Client $http,
        private readonly string $fanoutUrl,
        private readonly int $httpSafetyMs,
    ) {}

    /**
     * Call fanout. Returns the decoded "results" array, or null on transport error.
     *
     * @param  array<string, mixed>  $query
     * @param  array<int, array<string, mixed>>  $providers
     * @return array<int, array<string, mixed>>|null
     */
    public function fanout(string $requestId, int $deadlineMs, array $query, array $providers): ?array
    {
        $body = [
            'request_id' => $requestId,
            'deadline_ms' => $deadlineMs,
            'query' => $query,
            'providers' => $providers,
        ];

        // Total HTTP budget = fanout deadline + safety margin, in seconds.
        $timeoutSeconds = ($deadlineMs + $this->httpSafetyMs) / 1000.0;

        try {
            $response = $this->http->post(rtrim($this->fanoutUrl, '/').'/v1/fanout', [
                'json' => $body,
                'timeout' => $timeoutSeconds,
                'connect_timeout' => min(1.0, $timeoutSeconds),
                'headers' => [
                    'Content-Type' => 'application/json',
                    'X-Request-Id' => $requestId,
                ],
            ]);

            /** @var array<string, mixed>|null $decoded */
            $decoded = json_decode((string) $response->getBody(), true);

            if (! is_array($decoded) || ! isset($decoded['results']) || ! is_array($decoded['results'])) {
                Log::warning('fanout returned an unexpected payload', ['request_id' => $requestId]);

                return null;
            }

            /** @var array<int, array<string, mixed>> $results */
            $results = array_values(array_filter($decoded['results'], 'is_array'));

            return $results;
        } catch (GuzzleException $e) {
            Log::warning('fanout call failed', [
                'request_id' => $requestId,
                'error' => $e->getMessage(),
            ]);

            return null;
        }
    }
}
