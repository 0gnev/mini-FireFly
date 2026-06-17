<?php

declare(strict_types=1);

namespace Tests\Support;

use App\Services\FanoutClient;
use GuzzleHttp\Client;

/**
 * Test double for FanoutClient that records call counts and returns a canned
 * results payload, so SearchService feature tests can assert "zero fanout calls"
 * on a cache hit (SPEC §7.4) without a real fanout service.
 */
class FakeFanoutClient extends FanoutClient
{
    public int $calls = 0;

    /** @var array<int, array<string, mixed>> */
    public array $lastProviders = [];

    public ?int $lastDeadlineMs = null;

    /**
     * @param  array<int, array<string, mixed>>  $cannedResults
     */
    public function __construct(private array $cannedResults = [])
    {
        // Bypass real HTTP wiring; parent ctor only stores deps we won't use.
        parent::__construct(new Client, 'http://fanout.test', 200);
    }

    /**
     * @param  array<string, mixed>  $query
     * @param  array<int, array<string, mixed>>  $providers
     * @return array<int, array<string, mixed>>|null
     */
    public function fanout(string $requestId, int $deadlineMs, array $query, array $providers): ?array
    {
        $this->calls++;
        $this->lastProviders = $providers;
        $this->lastDeadlineMs = $deadlineMs;

        return $this->cannedResults;
    }

    /**
     * @param  array<int, array<string, mixed>>  $results
     */
    public function setResults(array $results): void
    {
        $this->cannedResults = $results;
    }
}
