<?php

declare(strict_types=1);

use App\Support\CanonicalJson;

/**
 * The booking idempotency request hash (SPEC §7.3) is sha256 over the canonical
 * JSON of the booking payload {offer_id, passenger}. These assert the exact
 * properties the replay/conflict decision relies on.
 */

/**
 * Mirror of BookingService::book hash computation.
 *
 * @param  array{offer_id: string, passenger: array<string, mixed>}  $payload
 */
function idemHash(array $payload): string
{
    return CanonicalJson::sha256($payload);
}

it('is identical for byte-identical payloads', function (): void {
    $p = ['offer_id' => 'a:7f3c9b2e', 'passenger' => ['first_name' => 'Ilia', 'last_name' => 'K', 'email' => 'x@y.z']];

    expect(idemHash($p))->toBe(idemHash($p));
});

it('is identical regardless of passenger field order', function (): void {
    $p1 = ['offer_id' => 'a:7f3c9b2e', 'passenger' => ['first_name' => 'Ilia', 'last_name' => 'K', 'email' => 'x@y.z']];
    $p2 = ['offer_id' => 'a:7f3c9b2e', 'passenger' => ['email' => 'x@y.z', 'last_name' => 'K', 'first_name' => 'Ilia']];

    expect(idemHash($p1))->toBe(idemHash($p2));
});

it('differs when the offer_id differs', function (): void {
    $p1 = ['offer_id' => 'a:7f3c9b2e', 'passenger' => ['email' => 'x@y.z']];
    $p2 = ['offer_id' => 'b:11111111', 'passenger' => ['email' => 'x@y.z']];

    expect(idemHash($p1))->not->toBe(idemHash($p2));
});

it('differs when any passenger field differs', function (): void {
    $p1 = ['offer_id' => 'a:7f3c9b2e', 'passenger' => ['first_name' => 'Ilia', 'email' => 'x@y.z']];
    $p2 = ['offer_id' => 'a:7f3c9b2e', 'passenger' => ['first_name' => 'Other', 'email' => 'x@y.z']];

    expect(idemHash($p1))->not->toBe(idemHash($p2));
});

it('produces a 64-char hex digest', function (): void {
    $hash = idemHash(['offer_id' => 'a:7f3c9b2e', 'passenger' => ['email' => 'x@y.z']]);

    expect($hash)->toHaveLength(64);
    expect($hash)->toMatch('/^[0-9a-f]{64}$/');
});
