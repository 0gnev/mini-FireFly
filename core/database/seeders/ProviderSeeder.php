<?php

declare(strict_types=1);

namespace Database\Seeders;

use App\Models\Provider;
use Illuminate\Database\Seeder;

/**
 * Seeds providers a–d pointing at the compose mock hostnames (SPEC §11.1 / §16.1).
 * Mocks listen on 8080 inside the container network. Idempotent: re-running
 * updates the rows in place.
 */
class ProviderSeeder extends Seeder
{
    public function run(): void
    {
        $defaults = [
            'enabled' => true,
            'timeout_ms' => 800,
            'rate_limit_rps' => 40,
            'breaker_failure_threshold' => 5,
            'breaker_window_s' => 30,
            'breaker_cooldown_s' => 15,
        ];

        $providers = [
            'a' => 'http://mock-a:8080',
            'b' => 'http://mock-b:8080',
            'c' => 'http://mock-c:8080',
            'd' => 'http://mock-d:8080',
        ];

        foreach ($providers as $name => $baseUrl) {
            Provider::query()->updateOrCreate(
                ['name' => $name],
                array_merge($defaults, ['base_url' => $baseUrl]),
            );
        }
    }
}
