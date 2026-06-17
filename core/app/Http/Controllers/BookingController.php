<?php

declare(strict_types=1);

namespace App\Http\Controllers;

use App\Http\Requests\BookingRequest;
use App\Models\Booking;
use App\Services\BookingService;
use Illuminate\Http\JsonResponse;

/**
 * Bookings API (SPEC §8.2 / §8.3). The Idempotency-Key header is validated here
 * (8–128 chars, [A-Za-z0-9_-]); body validation lives in BookingRequest.
 */
class BookingController extends Controller
{
    public function store(BookingRequest $request, BookingService $bookings): JsonResponse
    {
        $key = (string) $request->header('Idempotency-Key', '');

        if (! $this->validIdempotencyKey($key)) {
            return response()->json([
                'error' => 'invalid_idempotency_key',
                'message' => 'Idempotency-Key header must be 8–128 chars matching [A-Za-z0-9_-].',
            ], 422);
        }

        $result = $bookings->book($key, $request->bookingPayload());

        $response = response()->json($result->body, $result->statusCode);
        if ($result->replayed) {
            $response->header('Idempotency-Replayed', 'true');
        }

        return $response;
    }

    public function show(string $id): JsonResponse
    {
        $uid = str_starts_with($id, 'bk_') ? substr($id, 3) : $id;

        $booking = Booking::query()->where('booking_uid', $uid)->first();

        if ($booking === null) {
            return response()->json([
                'error' => 'not_found',
                'message' => 'Booking not found.',
            ], 404);
        }

        return response()->json($booking->toPublicArray(), 200);
    }

    private function validIdempotencyKey(string $key): bool
    {
        return preg_match('/^[A-Za-z0-9_-]{8,128}$/', $key) === 1;
    }
}
