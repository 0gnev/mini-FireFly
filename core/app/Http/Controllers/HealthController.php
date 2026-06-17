<?php

declare(strict_types=1);

namespace App\Http\Controllers;

use App\Services\HealthService;
use App\Services\Metrics;
use Illuminate\Http\JsonResponse;
use Illuminate\Http\Response;

/**
 * GET /api/v1/healthz (SPEC §8.5) and GET /metrics (SPEC §12.1).
 */
class HealthController extends Controller
{
    public function healthz(HealthService $health): JsonResponse
    {
        $result = $health->check();

        return response()->json([
            'status' => $result['ok'] ? 'ok' : 'degraded',
            'dependencies' => $result['deps'],
        ], $result['ok'] ? 200 : 503);
    }

    public function metrics(Metrics $metrics): Response
    {
        return response($metrics->render(), 200)
            ->header('Content-Type', 'text/plain; version=0.0.4; charset=utf-8');
    }
}
