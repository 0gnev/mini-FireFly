<?php

declare(strict_types=1);

use App\Services\OfferMerger;

/**
 * Helper to build a minimal offer with one segment.
 *
 * @return array<string, mixed>
 */
function offer(string $provider, string $price, string $carrier, string $flightNo, string $departAt): array
{
    return [
        'offer_id' => $provider.':'.substr(md5($flightNo.$departAt), 0, 8),
        'provider' => $provider,
        'price' => $price,
        'currency' => 'EUR',
        'depart_at' => $departAt,
        'arrive_at' => $departAt,
        'duration_minutes' => 100,
        'stops' => 0,
        'segments' => [[
            'flight_no' => $flightNo,
            'carrier' => $carrier,
            'origin' => 'BEG',
            'destination' => 'AMS',
            'depart_at' => $departAt,
            'arrive_at' => $departAt,
        ]],
    ];
}

/**
 * @param  array<int, array<string, mixed>>  $offers
 * @return array<string, mixed>
 */
function result(string $provider, string $status, array $offers = []): array
{
    return ['provider' => $provider, 'status' => $status, 'offers' => $offers, 'latency_ms' => 10, 'attempts' => 1];
}

it('only includes offers from ok providers', function (): void {
    $merger = new OfferMerger;

    $out = $merger->merge([
        result('a', 'ok', [offer('a', '100.00', 'JU', 'JU100', '2026-07-01T08:00:00+02:00')]),
        result('b', 'timeout', [offer('b', '50.00', 'KL', 'KL999', '2026-07-01T07:00:00+02:00')]),
    ]);

    expect($out['offers'])->toHaveCount(1);
    expect($out['offers'][0]['provider'])->toBe('a');
});

it('dedups identical offers and keeps the cheapest', function (): void {
    $merger = new OfferMerger;

    $out = $merger->merge([
        result('a', 'ok', [offer('a', '142.50', 'JU', 'JU351', '2026-07-01T08:40:00+02:00')]),
        result('b', 'ok', [offer('b', '135.00', 'JU', 'JU351', '2026-07-01T08:40:00+02:00')]),
    ]);

    expect($out['total_offers'])->toBe(2);
    expect($out['deduplicated'])->toBe(1);
    expect($out['offers'])->toHaveCount(1);
    expect($out['offers'][0]['provider'])->toBe('b');
    expect($out['offers'][0]['price'])->toBe('135.00');
});

it('breaks dedup price ties by provider that responded first', function (): void {
    $merger = new OfferMerger;

    // d appears first in results (responded first), a second; identical price.
    $out = $merger->merge([
        result('d', 'ok', [offer('d', '120.00', 'OS', 'OS772', '2026-07-01T07:10:00+02:00')]),
        result('a', 'ok', [offer('a', '120.00', 'OS', 'OS772', '2026-07-01T07:10:00+02:00')]),
    ]);

    expect($out['offers'])->toHaveCount(1);
    expect($out['offers'][0]['provider'])->toBe('d');
});

it('sorts by price asc, then depart_at asc, then provider asc', function (): void {
    $merger = new OfferMerger;

    $out = $merger->merge([
        result('c', 'ok', [
            offer('c', '200.00', 'AA', 'AA1', '2026-07-01T10:00:00+02:00'),
            offer('c', '100.00', 'BB', 'BB2', '2026-07-01T12:00:00+02:00'),
        ]),
        result('a', 'ok', [
            offer('a', '100.00', 'CC', 'CC3', '2026-07-01T09:00:00+02:00'),
        ]),
        result('b', 'ok', [
            // same price & depart as a's first → provider tie-break (a before b)
            offer('b', '100.00', 'DD', 'DD4', '2026-07-01T09:00:00+02:00'),
        ]),
    ]);

    $order = array_map(static fn (array $o): string => $o['provider'].'/'.$o['price'].'/'.$o['depart_at'], $out['offers']);

    expect($order)->toBe([
        'a/100.00/2026-07-01T09:00:00+02:00', // cheapest, earliest, provider a
        'b/100.00/2026-07-01T09:00:00+02:00', // same price+depart, provider b
        'c/100.00/2026-07-01T12:00:00+02:00', // same price, later depart
        'c/200.00/2026-07-01T10:00:00+02:00', // most expensive
    ]);
});

it('compares prices as decimals, not strings', function (): void {
    $merger = new OfferMerger;

    // "9.00" < "10.00" numerically, but "10.00" < "9.00" as plain strings.
    $out = $merger->merge([
        result('a', 'ok', [offer('a', '10.00', 'AA', 'AA1', '2026-07-01T08:00:00+02:00')]),
        result('b', 'ok', [offer('b', '9.00', 'BB', 'BB2', '2026-07-01T08:00:00+02:00')]),
    ]);

    expect($out['offers'][0]['price'])->toBe('9.00');
    expect($out['offers'][1]['price'])->toBe('10.00');
});

it('returns empty offers when no provider is ok', function (): void {
    $merger = new OfferMerger;

    $out = $merger->merge([
        result('a', 'timeout'),
        result('b', 'breaker_open'),
    ]);

    expect($out['offers'])->toBe([]);
    expect($out['total_offers'])->toBe(0);
    expect($out['deduplicated'])->toBe(0);
});
