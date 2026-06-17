<?php

declare(strict_types=1);

use App\Http\Controllers\HealthController;
use App\Http\Middleware\AssignRequestId;
use Illuminate\Foundation\Application;
use Illuminate\Foundation\Configuration\Exceptions;
use Illuminate\Foundation\Configuration\Middleware;
use Illuminate\Http\Request;
use Illuminate\Support\Facades\Route;

return Application::configure(basePath: dirname(__DIR__))
    ->withRouting(
        web: __DIR__.'/../routes/web.php',
        api: __DIR__.'/../routes/api.php',
        commands: __DIR__.'/../routes/console.php',
        health: '/up',
        then: function (): void {
            // Prometheus scrape endpoint at the root (SPEC §12.1).
            Route::get('/metrics', [HealthController::class, 'metrics']);
        },
    )
    ->withMiddleware(function (Middleware $middleware): void {
        // Public JSON API; no auth (demo-grade, SPEC §1.4/§19).
        // request_id correlation on every request (SPEC §12.4).
        $middleware->append(AssignRequestId::class);
    })
    ->withExceptions(function (Exceptions $exceptions): void {
        // Force JSON error envelopes for the API (SPEC §8: {"error","message"}).
        $exceptions->shouldRenderJsonWhen(
            fn (Request $request): bool => $request->is('api/*') || $request->is('metrics') || $request->expectsJson(),
        );
    })->create();
