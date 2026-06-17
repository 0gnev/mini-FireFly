<?php

declare(strict_types=1);

namespace App\Http\Controllers;

use App\Services\ProviderStatusService;
use Illuminate\Http\JsonResponse;

/**
 * GET /api/v1/providers (SPEC §8.4).
 */
class ProviderController extends Controller
{
    public function index(ProviderStatusService $status): JsonResponse
    {
        return response()->json($status->snapshot(), 200);
    }
}
