<?php

declare(strict_types=1);

use App\Models\Provider;
use App\Services\ClickHouseClient;
use App\Services\FanoutClient;
use App\Services\Metrics;
use App\Services\OfferMerger;
use App\Services\ProviderRepository;
use App\Services\SearchService;
use Illuminate\Support\Facades\Queue;
use Tests\Support\FakeFanoutClient;

/**
 * SPEC §7.4: "ClickHouse outage degrades nothing user-visible."
 *
 * We bind a ClickHouseClient whose every method behaves as if CH is down, run a
 * real search (with the logging job processed synchronously), and assert the
 * user still gets a clean 200 with offers.
 */
class DownClickHouseClient extends ClickHouseClient
{
    public function __construct()
    {
        parent::__construct([
            'dsn' => 'http://clickhouse.invalid:8123',
            'database' => 'firefly',
            'username' => 'default',
            'password' => '',
            'timeout' => 0.5,
        ]);
    }

    public function insertProviderCalls(array $rows, array $columns): bool
    {
        return false; // simulate CH down on the write path
    }

    public function select(string $sql, array $bindings = []): ?array
    {
        return null; // simulate CH down on the read path
    }
}

beforeEach(function (): void {
    // Process the logging job synchronously so any CH failure would surface here.
    config(['queue.default' => 'sync']);

    app()->instance(ClickHouseClient::class, new DownClickHouseClient);

    foreach (['a', 'b'] as $name) {
        Provider::query()->create(['name' => $name, 'base_url' => "http://mock-{$name}:8080", 'enabled' => true]);
    }
});

it('returns a clean 200 search even when ClickHouse is down', function (): void {
    $fake = new FakeFanoutClient([
        [
            'provider' => 'a', 'status' => 'ok', 'latency_ms' => 90, 'attempts' => 1,
            'offers' => [[
                'offer_id' => 'a:7f3c9b2e', 'provider' => 'a', 'price' => '142.50', 'currency' => 'EUR',
                'depart_at' => '2026-07-01T08:40:00+02:00', 'arrive_at' => '2026-07-01T11:05:00+02:00',
                'duration_minutes' => 145, 'stops' => 0,
                'segments' => [[
                    'flight_no' => 'JU351', 'carrier' => 'JU', 'origin' => 'BEG', 'destination' => 'AMS',
                    'depart_at' => '2026-07-01T08:40:00+02:00', 'arrive_at' => '2026-07-01T11:05:00+02:00',
                ]],
            ]],
        ],
    ]);
    app()->instance(FanoutClient::class, $fake);
    app()->bind(SearchService::class, fn ($app) => new SearchService(
        $app->make(ProviderRepository::class),
        $fake,
        $app->make(OfferMerger::class),
        $app->make(Metrics::class),
        2000,
        100,
        90,
    ));

    $response = $this->postJson('/api/v1/search', [
        'origin' => 'BEG',
        'destination' => 'AMS',
        'depart_date' => now()->addDays(10)->toDateString(),
        'passengers' => 1,
    ]);

    $response->assertStatus(200);
    expect($response->json('offers'))->toHaveCount(1);
    expect($response->json('offers.0.price'))->toBe('142.50');
});

it('returns the providers listing even when the ClickHouse last_5m query fails', function (): void {
    Queue::fake();

    $response = $this->getJson('/api/v1/providers');

    $response->assertStatus(200)->assertJsonStructure([
        'providers' => [['name', 'enabled', 'breaker', 'last_5m' => ['error_rate', 'p99_ms', 'calls']]],
    ]);

    // CH down → zeroed last_5m, not an error.
    expect($response->json('providers.0.last_5m.calls'))->toBe(0);
});
