<?php

declare(strict_types=1);

use App\Support\CanonicalJson;

it('sorts object keys recursively and omits whitespace', function (): void {
    $a = ['b' => 1, 'a' => ['z' => 2, 'y' => 3]];
    $b = ['a' => ['y' => 3, 'z' => 2], 'b' => 1];

    expect(CanonicalJson::encode($a))->toBe('{"a":{"y":3,"z":2},"b":1}');
    expect(CanonicalJson::encode($a))->toBe(CanonicalJson::encode($b));
});

it('keeps null values (does not drop them)', function (): void {
    $query = ['origin' => 'BEG', 'destination' => 'AMS', 'return_date' => null, 'passengers' => 1];

    $json = CanonicalJson::encode($query);

    expect($json)->toContain('"return_date":null');
});

it('preserves list order but normalizes nested objects', function (): void {
    $value = ['list' => [['k2' => 2, 'k1' => 1], ['k1' => 3]]];

    expect(CanonicalJson::encode($value))->toBe('{"list":[{"k1":1,"k2":2},{"k1":3}]}');
});

it('produces a stable search cache key regardless of key order', function (): void {
    $q1 = ['origin' => 'BEG', 'destination' => 'AMS', 'depart_date' => '2026-07-01', 'return_date' => null, 'passengers' => 1];
    $q2 = ['passengers' => 1, 'return_date' => null, 'depart_date' => '2026-07-01', 'destination' => 'AMS', 'origin' => 'BEG'];

    expect(CanonicalJson::searchCacheKey($q1))->toBe(CanonicalJson::searchCacheKey($q2));
    expect(CanonicalJson::searchCacheKey($q1))->toStartWith('search:');
});

it('changes the cache key when any field changes', function (): void {
    $base = ['origin' => 'BEG', 'destination' => 'AMS', 'depart_date' => '2026-07-01', 'return_date' => null, 'passengers' => 1];
    $changed = array_merge($base, ['passengers' => 2]);

    expect(CanonicalJson::searchCacheKey($base))->not->toBe(CanonicalJson::searchCacheKey($changed));
});

it('produces matching idempotency hashes for equal payloads with different key order', function (): void {
    $p1 = ['offer_id' => 'a:7f3c9b2e', 'passenger' => ['first_name' => 'Ilia', 'last_name' => 'K', 'email' => 'x@y.z']];
    $p2 = ['passenger' => ['email' => 'x@y.z', 'last_name' => 'K', 'first_name' => 'Ilia'], 'offer_id' => 'a:7f3c9b2e'];

    expect(CanonicalJson::sha256($p1))->toBe(CanonicalJson::sha256($p2));
});

it('produces differing idempotency hashes for differing payloads', function (): void {
    $p1 = ['offer_id' => 'a:7f3c9b2e', 'passenger' => ['email' => 'x@y.z']];
    $p2 = ['offer_id' => 'a:7f3c9b2e', 'passenger' => ['email' => 'other@y.z']];

    expect(CanonicalJson::sha256($p1))->not->toBe(CanonicalJson::sha256($p2));
});
