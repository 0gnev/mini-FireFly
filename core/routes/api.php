<?php

declare(strict_types=1);

use App\Http\Controllers\BookingController;
use App\Http\Controllers\HealthController;
use App\Http\Controllers\ProviderController;
use App\Http\Controllers\SearchController;
use Illuminate\Support\Facades\Route;

/*
|--------------------------------------------------------------------------
| Public HTTP API (SPEC §8). Base path: /api/v1.
|--------------------------------------------------------------------------
*/

Route::prefix('v1')->group(function (): void {
    Route::post('/search', SearchController::class);

    Route::post('/bookings', [BookingController::class, 'store']);
    Route::get('/bookings/{id}', [BookingController::class, 'show']);

    Route::get('/providers', [ProviderController::class, 'index']);

    Route::get('/healthz', [HealthController::class, 'healthz']);
});
