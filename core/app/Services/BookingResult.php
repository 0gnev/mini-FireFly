<?php

declare(strict_types=1);

namespace App\Services;

/**
 * Outcome of a booking attempt (SPEC §7.3 / §8.2).
 *
 * - $body: the JSON response body,
 * - $statusCode: 201 fresh / 200 replay / 409 conflict,
 * - $replayed: whether to set the Idempotency-Replayed: true response header.
 */
final class BookingResult
{
    /**
     * @param  array<string, mixed>  $body
     */
    public function __construct(
        public readonly array $body,
        public readonly int $statusCode,
        public readonly bool $replayed,
    ) {}
}
