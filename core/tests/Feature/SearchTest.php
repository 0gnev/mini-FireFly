<?php

declare(strict_types=1);

use App\Models\Provider;
use App\Services\FanoutClient;
use App\Services\Metrics;
use App\Services\OfferMerger;
use App\Services\ProviderRepository;
use App\Services\SearchService;
use Illuminate\Support\Facades\Queue;
use Tests\Support\FakeFanoutClient;

/**
 * Build a canned ok result for a provider with a single offer.
 *
 * @return array<string, mixed>
 */
function okResult(string $provider, string $price, string $flightNo): array
{
    return [
        'provider' => $provider,
        'status' => 'ok',
        'latency_ms' => 100,
        'attempts' => 1,
        'offers' => [[
            'offer_id' => $provider.':'.substr(md5($flightNo), 0, 8),
            'provider' => $provider,
            'price' => $price,
            'currency' => 'EUR',
            'depart_at' => '2026-07-01T08:40:00+02:00',
            'arrive_at' => '2026-07-01T11:05:00+02:00',
            'duration_minutes' => 145,
            'stops' => 0,
            'segments' => [[
                'flight_no' => $flightNo,
                'carrier' => substr($flightNo, 0, 2),
                'origin' => 'BEG',
                'destination' => 'AMS',
                'depart_at' => '2026-07-01T08:40:00+02:00',
                'arrive_at' => '2026-07-01T11:05:00+02:00',
            ]],
        ]],
    ];
}

function seedProviders(): void
{
    foreach (['a', 'b', 'c', 'd'] as $name) {
        Provider::query()->create([
            'name' => $name,
            'base_url' => "http://mock-{$name}:8080",
            'enabled' => true,
        ]);
    }
}

function bindFakeFanout(array $results): FakeFanoutClient
{
    $fake = new FakeFanoutClient($results);
    app()->instance(FanoutClient::class, $fake);
    // Rebuild SearchService so it picks up the fake (it was bound as a singleton).
    app()->forgetInstance(SearchService::class);
    app()->bind(SearchService::class, fn ($app) => new SearchService(
        $app->make(ProviderRepository::class),
        $fake,
        $app->make(OfferMerger::class),
        $app->make(Metrics::class),
        (int) config('firefly.search_deadline_ms', 2000),
        (int) config('firefly.merge_reserve_ms', 100),
        (int) config('firefly.search_cache_ttl_s', 90),
    ));

    return $fake;
}

beforeEach(function (): void {
    Queue::fake();
    seedProviders();
});

it('returns 200 with the §8.1 response shape and merged offers', function (): void {
    bindFakeFanout([
        okResult('a', '142.50', 'JU351'),
        okResult('d', '139.90', 'OS772'),
    ]);

    $response = $this->postJson('/api/v1/search', [
        'origin' => 'beg',
        'destination' => 'ams',
        'depart_date' => now()->addDays(10)->toDateString(),
        'return_date' => null,
        'passengers' => 1,
    ]);

    $response->assertStatus(200)
        ->assertJsonStructure([
            'request_id', 'cache', 'partial', 'offers',
            'providers' => ['a' => ['status', 'latency_ms', 'offers', 'attempts']],
            'meta' => ['deadline_ms', 'elapsed_ms', 'total_offers', 'deduplicated'],
        ]);

    expect($response->json('cache'))->toBe('miss');
    expect($response->json('request_id'))->toStartWith('req_');
    expect($response->json('offers.0.price'))->toBe('139.90'); // cheapest first
});

it('normalizes lowercase IATA codes to uppercase', function (): void {
    bindFakeFanout([okResult('a', '142.50', 'JU351')]);

    $response = $this->postJson('/api/v1/search', [
        'origin' => 'beg',
        'destination' => 'ams',
        'depart_date' => now()->addDays(5)->toDateString(),
        'passengers' => 2,
    ]);

    $response->assertStatus(200);
    expect($response->json('providers'))->toHaveKey('a');
});

it('serves the second identical query from cache with zero fanout calls', function (): void {
    $fake = bindFakeFanout([okResult('a', '142.50', 'JU351')]);

    $payload = [
        'origin' => 'BEG',
        'destination' => 'AMS',
        'depart_date' => now()->addDays(7)->toDateString(),
        'return_date' => null,
        'passengers' => 1,
    ];

    $first = $this->postJson('/api/v1/search', $payload);
    $first->assertStatus(200);
    expect($first->json('cache'))->toBe('miss');
    expect($fake->calls)->toBe(1);

    $second = $this->postJson('/api/v1/search', $payload);
    $second->assertStatus(200);
    expect($second->json('cache'))->toBe('hit');
    // Critical acceptance criterion (SPEC §7.4): no second fanout call.
    expect($fake->calls)->toBe(1);

    // Cached body matches except for request_id/cache fields.
    expect($second->json('offers'))->toEqual($first->json('offers'));
    expect($second->json('providers'))->toEqual($first->json('providers'));
});

it('marks partial=true and never caches an all-failure response', function (): void {
    $fake = bindFakeFanout([
        ['provider' => 'a', 'status' => 'timeout', 'offers' => [], 'latency_ms' => 1900, 'attempts' => 2],
        ['provider' => 'b', 'status' => 'breaker_open', 'offers' => [], 'latency_ms' => 0, 'attempts' => 0],
    ]);

    $payload = [
        'origin' => 'BEG',
        'destination' => 'AMS',
        'depart_date' => now()->addDays(3)->toDateString(),
        'passengers' => 1,
    ];

    $first = $this->postJson('/api/v1/search', $payload);
    $first->assertStatus(200);
    expect($first->json('partial'))->toBeTrue();
    expect($first->json('offers'))->toBe([]);

    // All-failure → not cached → fanout called again.
    $this->postJson('/api/v1/search', $payload)->assertStatus(200);
    expect($fake->calls)->toBe(2);
});

it('reports partial=true when one provider is ok and another failed', function (): void {
    bindFakeFanout([
        okResult('a', '142.50', 'JU351'),
        ['provider' => 'b', 'status' => 'timeout', 'offers' => [], 'latency_ms' => 1900, 'attempts' => 2],
    ]);

    $response = $this->postJson('/api/v1/search', [
        'origin' => 'BEG',
        'destination' => 'AMS',
        'depart_date' => now()->addDays(3)->toDateString(),
        'passengers' => 1,
    ]);

    $response->assertStatus(200);
    expect($response->json('partial'))->toBeTrue();
    expect($response->json('providers.b.status'))->toBe('timeout');
});

it('passes a deadline budget below the full deadline to fanout', function (): void {
    $fake = bindFakeFanout([okResult('a', '142.50', 'JU351')]);

    $this->postJson('/api/v1/search', [
        'origin' => 'BEG',
        'destination' => 'AMS',
        'depart_date' => now()->addDays(3)->toDateString(),
        'passengers' => 1,
    ])->assertStatus(200);

    // 2000 deadline − elapsed − 100 reserve ≤ 1900.
    expect($fake->lastDeadlineMs)->toBeLessThanOrEqual(1900);
    expect($fake->lastDeadlineMs)->toBeGreaterThan(0);
});

it('only sends enabled providers to fanout', function (): void {
    Provider::query()->where('name', 'c')->update(['enabled' => false]);
    $fake = bindFakeFanout([okResult('a', '142.50', 'JU351')]);

    $this->postJson('/api/v1/search', [
        'origin' => 'BEG',
        'destination' => 'AMS',
        'depart_date' => now()->addDays(3)->toDateString(),
        'passengers' => 1,
    ])->assertStatus(200);

    $names = array_map(static fn (array $p): string => $p['name'], $fake->lastProviders);
    expect($names)->not->toContain('c');
    expect($names)->toContain('a');
});
