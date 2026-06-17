<?php

declare(strict_types=1);

namespace App\Http\Controllers;

use App\Http\Requests\SearchRequest;
use App\Services\SearchService;
use Illuminate\Http\JsonResponse;

/**
 * POST /api/v1/search (SPEC §8.1). Thin: validation lives in SearchRequest,
 * orchestration in SearchService.
 */
class SearchController extends Controller
{
    public function __invoke(SearchRequest $request, SearchService $search): JsonResponse
    {
        $body = $search->search($request->canonicalQuery());

        return response()->json($body, 200);
    }
}
