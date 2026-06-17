<?php

declare(strict_types=1);

namespace App\Providers;

use App\Services\ClickHouseClient;
use App\Services\FanoutClient;
use App\Services\HealthService;
use App\Services\Metrics;
use App\Services\OfferMerger;
use App\Services\ProviderRepository;
use App\Services\ProviderStatusService;
use App\Services\SearchService;
use App\Support\AirportRepository;
use GuzzleHttp\Client;
use Illuminate\Support\ServiceProvider;

class AppServiceProvider extends ServiceProvider
{
    public function register(): void
    {
        $this->app->singleton(AirportRepository::class, fn (): AirportRepository => new AirportRepository);

        $this->app->singleton(OfferMerger::class, fn (): OfferMerger => new OfferMerger);

        $this->app->singleton(ProviderRepository::class, fn ($app): ProviderRepository => new ProviderRepository(
            (int) config('firefly.providers_config_ttl_s', 10),
        ));

        $this->app->singleton(ClickHouseClient::class, fn ($app): ClickHouseClient => new ClickHouseClient([
            'dsn' => (string) config('firefly.clickhouse.dsn'),
            'database' => (string) config('firefly.clickhouse.database'),
            'username' => (string) config('firefly.clickhouse.username'),
            'password' => (string) config('firefly.clickhouse.password'),
            'timeout' => (float) config('firefly.clickhouse.timeout'),
        ]));

        $this->app->singleton(FanoutClient::class, fn ($app): FanoutClient => new FanoutClient(
            new Client,
            (string) config('firefly.fanout_url'),
            (int) config('firefly.fanout_http_safety_ms', 200),
        ));

        $this->app->singleton(SearchService::class, fn ($app): SearchService => new SearchService(
            $app->make(ProviderRepository::class),
            $app->make(FanoutClient::class),
            $app->make(OfferMerger::class),
            $app->make(Metrics::class),
            (int) config('firefly.search_deadline_ms', 2000),
            (int) config('firefly.merge_reserve_ms', 100),
            (int) config('firefly.search_cache_ttl_s', 90),
        ));

        $this->app->singleton(ProviderStatusService::class, fn ($app): ProviderStatusService => new ProviderStatusService(
            $app->make(ProviderRepository::class),
            $app->make(ClickHouseClient::class),
            (int) config('firefly.providers_last5m_ttl_s', 10),
        ));

        $this->app->singleton(HealthService::class, fn ($app): HealthService => new HealthService(
            new Client,
            (string) config('firefly.fanout_url'),
        ));
    }

    public function boot(): void
    {
        //
    }
}
