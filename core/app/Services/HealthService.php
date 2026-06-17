<?php

declare(strict_types=1);

namespace App\Services;

use GuzzleHttp\Client;
use Illuminate\Support\Facades\DB;
use Illuminate\Support\Facades\Redis;
use Throwable;

/**
 * Aggregate health (SPEC §8.5): MySQL ping, Redis ping, fanout /healthz.
 * 200 if all up; 503 with per-dependency detail otherwise.
 */
class HealthService
{
    public function __construct(
        private readonly Client $http,
        private readonly string $fanoutUrl,
    ) {}

    /**
     * @return array{ok: bool, deps: array<string, array{status: string, detail?: string}>}
     */
    public function check(): array
    {
        $deps = [
            'mysql' => $this->checkMysql(),
            'redis' => $this->checkRedis(),
            'fanout' => $this->checkFanout(),
        ];

        $ok = true;
        foreach ($deps as $d) {
            if ($d['status'] !== 'up') {
                $ok = false;
            }
        }

        return ['ok' => $ok, 'deps' => $deps];
    }

    /**
     * @return array{status: string, detail?: string}
     */
    private function checkMysql(): array
    {
        try {
            DB::connection()->getPdo()->query('SELECT 1');

            return ['status' => 'up'];
        } catch (Throwable $e) {
            return ['status' => 'down', 'detail' => $e->getMessage()];
        }
    }

    /**
     * @return array{status: string, detail?: string}
     */
    private function checkRedis(): array
    {
        try {
            Redis::ping();

            return ['status' => 'up'];
        } catch (Throwable $e) {
            return ['status' => 'down', 'detail' => $e->getMessage()];
        }
    }

    /**
     * @return array{status: string, detail?: string}
     */
    private function checkFanout(): array
    {
        try {
            $response = $this->http->get(rtrim($this->fanoutUrl, '/').'/healthz', [
                'timeout' => 1.5,
                'connect_timeout' => 1.0,
            ]);
            $code = $response->getStatusCode();

            return $code >= 200 && $code < 300
                ? ['status' => 'up']
                : ['status' => 'down', 'detail' => "fanout returned HTTP {$code}"];
        } catch (Throwable $e) {
            return ['status' => 'down', 'detail' => $e->getMessage()];
        }
    }
}
