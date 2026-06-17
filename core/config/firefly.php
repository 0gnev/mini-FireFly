<?php

declare(strict_types=1);

/*
|--------------------------------------------------------------------------
| mini-FireFly core configuration (SPEC §13)
|--------------------------------------------------------------------------
|
| All values are env-driven (12-factor). Defaults match the spec so the
| service is correct even with an empty environment.
|
*/

return [

    // Optional explicit path to the airports fixture (SPEC §4.1). When unset the
    // AirportRepository probes the mounted fixtures dir then the baked-in copy.
    'airports_file' => env('AIRPORTS_FILE'),

    // Internal fan-out service base URL (SPEC §9).
    'fanout_url' => env('FANOUT_URL', 'http://fanout:8090'),

    // Total request budget in ms (SPEC §2.3 step 4).
    'search_deadline_ms' => (int) env('SEARCH_DEADLINE_MS', 2000),

    // Search-response cache TTL in seconds (SPEC §10.6).
    'search_cache_ttl_s' => (int) env('SEARCH_CACHE_TTL_S', 90),

    // Merge reserve subtracted from the deadline before handing the budget to
    // fanout (SPEC §2.3 step 4: "minus a 100 ms merge reserve").
    'merge_reserve_ms' => (int) env('SEARCH_MERGE_RESERVE_MS', 100),

    // Guzzle request timeout safety margin over the fanout deadline (SPEC §7.1:
    // "request timeout = deadline + 200 ms safety").
    'fanout_http_safety_ms' => (int) env('FANOUT_HTTP_SAFETY_MS', 200),

    // Provider registry cache TTL (SPEC §7.1 / §11.2 providers:config).
    'providers_config_ttl_s' => (int) env('PROVIDERS_CONFIG_TTL_S', 10),

    // last_5m ClickHouse query cache TTL (SPEC §8.4).
    'providers_last5m_ttl_s' => (int) env('PROVIDERS_LAST5M_TTL_S', 10),

    'clickhouse' => [
        'dsn' => env('CLICKHOUSE_DSN', 'http://clickhouse:8123'),
        'database' => env('CLICKHOUSE_DB', 'firefly'),
        'username' => env('CLICKHOUSE_USER', 'default'),
        'password' => env('CLICKHOUSE_PASSWORD', ''),
        // Wall-clock timeout for the async insert / read queries (seconds).
        'timeout' => (float) env('CLICKHOUSE_TIMEOUT_S', 2.0),
    ],
];
