<?php

declare(strict_types=1);

use Illuminate\Support\Facades\Redis;

/**
 * Idempotency against REAL Redis — exercises the SET NX EX 86400 lock path
 * (SPEC §7.3 steps 1–3), which the MySQL-fallback suite (BookingTest) cannot.
 *
 * Requires a live Redis (the compose stack: REDIS_HOST reachable). Skipped
 * automatically when Redis is not connectable so the unit/feature suite stays
 * green on a bare machine. In CI (docker-compose.ci.yml) Redis is up and these
 * run for real.
 */
beforeEach(function (): void {
    try {
        Redis::ping();
    } catch (Throwable $e) {
        $this->markTestSkipped('real Redis not available: '.$e->getMessage());
    }

    // Clean any idem keys from prior runs of these fixed keys.
    foreach (['redis-key-aaaaaaaa', 'redis-key-bbbbbbbb'] as $k) {
        try {
            Redis::del('idem:'.$k);
        } catch (Throwable) {
            // ignore
        }
    }
});

/**
 * @return array<string, mixed>
 */
function redisBookingBody(array $overrides = []): array
{
    return array_merge([
        'offer_id' => 'a:7f3c9b2e',
        'passenger' => ['first_name' => 'Ilia', 'last_name' => 'K', 'email' => 'x@y.z'],
    ], $overrides);
}

it('locks the key via Redis SET NX and replays on repeat (real Redis)', function (): void {
    $key = 'redis-key-aaaaaaaa';

    $first = $this->postJson('/api/v1/bookings', redisBookingBody(), ['Idempotency-Key' => $key]);
    $first->assertStatus(201);

    // The Redis lock must now hold the request hash.
    expect(Redis::get('idem:'.$key))->not->toBeNull();

    $second = $this->postJson('/api/v1/bookings', redisBookingBody(), ['Idempotency-Key' => $key]);
    $second->assertStatus(200)->assertHeader('Idempotency-Replayed', 'true');
    expect($second->json())->toEqual($first->json());
});

it('returns 409 on key reuse with a different body (real Redis)', function (): void {
    $key = 'redis-key-bbbbbbbb';

    $this->postJson('/api/v1/bookings', redisBookingBody(), ['Idempotency-Key' => $key])->assertStatus(201);

    $this->postJson('/api/v1/bookings', redisBookingBody([
        'passenger' => ['first_name' => 'Z', 'last_name' => 'Q', 'email' => 'z@q.r'],
    ]), ['Idempotency-Key' => $key])
        ->assertStatus(409)
        ->assertJson(['error' => 'idempotency_key_reuse']);
});
