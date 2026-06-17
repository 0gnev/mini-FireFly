<?php

declare(strict_types=1);

use App\Models\Booking;
use App\Models\IdempotencyKey;

/**
 * Booking idempotency (SPEC §7.3 / §8.2).
 *
 * These tests drive the MySQL unique-constraint fallback path by forcing the
 * Redis lock to report "unavailable" (Redis facade throws under the array/test
 * setup). That exercises the replay + conflict logic deterministically without a
 * live Redis. A separate suite (BookingRedisTest) covers the SET NX path against
 * real Redis when the compose stack is up.
 */

/**
 * @return array<string, mixed>
 */
function bookingBody(array $overrides = []): array
{
    return array_merge([
        'offer_id' => 'a:7f3c9b2e',
        'passenger' => [
            'first_name' => 'Ilia',
            'last_name' => 'K',
            'email' => 'x@y.z',
        ],
    ], $overrides);
}

/**
 * @param  array<string, mixed>  $body
 */
function postBooking(string $key, array $body)
{
    return test()->postJson('/api/v1/bookings', $body, ['Idempotency-Key' => $key]);
}

it('creates a confirmed booking and returns 201 with bk_ id', function (): void {
    $response = postBooking('order-key-0001', bookingBody());

    $response->assertStatus(201)
        ->assertJsonStructure(['booking_id', 'status', 'offer_id', 'created_at']);

    expect($response->json('status'))->toBe('confirmed');
    expect($response->json('booking_id'))->toStartWith('bk_');
    expect($response->json('offer_id'))->toBe('a:7f3c9b2e');

    $this->assertDatabaseCount('bookings', 1);
    $this->assertDatabaseHas('idempotency_keys', ['idem_key' => 'order-key-0001', 'status_code' => 201]);
});

it('replays the same body for a repeated key and sets Idempotency-Replayed', function (): void {
    $first = postBooking('order-key-0002', bookingBody());
    $first->assertStatus(201);

    $second = postBooking('order-key-0002', bookingBody());

    $second->assertStatus(200);
    $second->assertHeader('Idempotency-Replayed', 'true');
    expect($second->json())->toEqual($first->json());

    // No second booking row created.
    $this->assertDatabaseCount('bookings', 1);
});

it('returns 409 idempotency_key_reuse for the same key with a different body', function (): void {
    postBooking('order-key-0003', bookingBody())->assertStatus(201);

    $conflict = postBooking('order-key-0003', bookingBody([
        'passenger' => ['first_name' => 'Other', 'last_name' => 'Person', 'email' => 'other@y.z'],
    ]));

    $conflict->assertStatus(409)->assertJson(['error' => 'idempotency_key_reuse']);
    $this->assertDatabaseCount('bookings', 1);
});

it('rejects a missing Idempotency-Key header with 422', function (): void {
    $this->postJson('/api/v1/bookings', bookingBody())
        ->assertStatus(422)
        ->assertJson(['error' => 'invalid_idempotency_key']);
});

it('rejects an Idempotency-Key that is too short', function (): void {
    postBooking('short', bookingBody())
        ->assertStatus(422)
        ->assertJson(['error' => 'invalid_idempotency_key']);
});

it('rejects an Idempotency-Key with illegal characters', function (): void {
    postBooking('has spaces here!!', bookingBody())
        ->assertStatus(422)
        ->assertJson(['error' => 'invalid_idempotency_key']);
});

it('rejects a malformed offer_id with 422', function (): void {
    postBooking('order-key-0004', bookingBody(['offer_id' => 'not-an-offer']))
        ->assertStatus(422)
        ->assertJsonValidationErrors(['offer_id']);
});

it('fetches a booking by id and 404s for unknown ids', function (): void {
    $created = postBooking('order-key-0005', bookingBody());
    $id = $created->json('booking_id');

    $this->getJson("/api/v1/bookings/{$id}")
        ->assertStatus(200)
        ->assertJson(['booking_id' => $id, 'status' => 'confirmed']);

    $this->getJson('/api/v1/bookings/bk_NONEXISTENT0000000000000000')
        ->assertStatus(404)
        ->assertJson(['error' => 'not_found']);
});

it('persists exactly the spec idempotency_keys columns', function (): void {
    postBooking('order-key-0006', bookingBody())->assertStatus(201);

    $record = IdempotencyKey::query()->find('order-key-0006');
    expect($record)->not->toBeNull();
    expect($record->request_hash)->toHaveLength(64);
    expect($record->status_code)->toBe(201);
    expect(json_decode($record->response_body, true))->toHaveKey('booking_id');

    $booking = Booking::query()->first();
    expect($booking->booking_uid)->toHaveLength(26);
});
