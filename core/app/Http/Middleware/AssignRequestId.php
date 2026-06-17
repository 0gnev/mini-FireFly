<?php

declare(strict_types=1);

namespace App\Http\Middleware;

use App\Logging\RequestId;
use Closure;
use Illuminate\Http\Request;
use Illuminate\Support\Str;
use Symfony\Component\HttpFoundation\Response;

/**
 * Assigns a per-request request_id (ULID, req_ prefix; SPEC §12.4) so every log
 * line for the request is correlatable. Honors an inbound X-Request-Id if the
 * caller supplied one; otherwise mints a fresh ULID. Echoed back in the response
 * header for client-side tracing.
 */
class AssignRequestId
{
    public function handle(Request $request, Closure $next): Response
    {
        $incoming = $request->header('X-Request-Id');
        $requestId = (is_string($incoming) && $incoming !== '')
            ? $incoming
            : 'req_'.strtoupper((string) Str::ulid());

        RequestId::set($requestId);
        $request->attributes->set('request_id', $requestId);

        /** @var Response $response */
        $response = $next($request);
        $response->headers->set('X-Request-Id', $requestId);

        return $response;
    }

    public function terminate(Request $request, Response $response): void
    {
        RequestId::set(null);
    }
}
