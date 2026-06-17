<?php

declare(strict_types=1);

namespace App\Services;

/**
 * Offer merging, deduplication and sorting (SPEC §7.2 + §4.4).
 *
 * Pure & deterministic so it is unit-testable in isolation. Input is the list of
 * per-provider results from fanout (each: provider, status, offers, ...); the
 * provider order in that list defines "responded first" for dedup tie-breaks.
 */
class OfferMerger
{
    /**
     * @param  array<int, array<string, mixed>>  $providerResults  in response order
     * @return array{offers: array<int, array<string, mixed>>, total_offers: int, deduplicated: int}
     */
    public function merge(array $providerResults): array
    {
        // Rank each provider by its position in the results list = arrival order.
        $rank = [];
        foreach ($providerResults as $i => $r) {
            $name = (string) ($r['provider'] ?? '');
            if ($name !== '' && ! array_key_exists($name, $rank)) {
                $rank[$name] = $i;
            }
        }

        // 1. Concatenate offers from all `ok` providers (SPEC §7.2.1).
        $offers = [];
        foreach ($providerResults as $r) {
            if (($r['status'] ?? null) !== 'ok') {
                continue;
            }
            foreach ((array) ($r['offers'] ?? []) as $offer) {
                if (is_array($offer)) {
                    $offers[] = $offer;
                }
            }
        }

        $total = count($offers);

        // 2. Deduplicate per §4.4.
        $deduped = $this->deduplicate($offers, $rank);
        $removed = $total - count($deduped);

        // 3. Sort: price ASC, depart_at ASC, provider ASC (SPEC §7.2.3).
        usort($deduped, fn (array $a, array $b): int => $this->compare($a, $b));

        return [
            'offers' => $deduped,
            'total_offers' => $total,
            'deduplicated' => $removed,
        ];
    }

    /**
     * §4.4: duplicates iff identical (carrier, flight_no list in order,
     * depart_at list in order). Keep cheapest; tie → provider that responded
     * first (lower rank).
     *
     * @param  array<int, array<string, mixed>>  $offers
     * @param  array<string, int>  $rank
     * @return array<int, array<string, mixed>>
     */
    private function deduplicate(array $offers, array $rank): array
    {
        /** @var array<string, array<string, mixed>> $best */
        $best = [];

        foreach ($offers as $offer) {
            $key = $this->dedupKey($offer);

            if (! isset($best[$key])) {
                $best[$key] = $offer;

                continue;
            }

            $incumbent = $best[$key];
            $cmpPrice = $this->comparePrice($offer, $incumbent);

            if ($cmpPrice < 0) {
                $best[$key] = $offer;
            } elseif ($cmpPrice === 0) {
                // Tie on price → keep the provider that responded first.
                $challengerRank = $rank[(string) ($offer['provider'] ?? '')] ?? PHP_INT_MAX;
                $incumbentRank = $rank[(string) ($incumbent['provider'] ?? '')] ?? PHP_INT_MAX;
                if ($challengerRank < $incumbentRank) {
                    $best[$key] = $offer;
                }
            }
        }

        return array_values($best);
    }

    /**
     * Identity tuple for dedup (carrier list, flight_no list, depart_at list —
     * each taken from the segments, in order).
     *
     * @param  array<string, mixed>  $offer
     */
    private function dedupKey(array $offer): string
    {
        $carriers = [];
        $flightNos = [];
        $departs = [];

        foreach ((array) ($offer['segments'] ?? []) as $seg) {
            if (! is_array($seg)) {
                continue;
            }
            $carriers[] = (string) ($seg['carrier'] ?? '');
            $flightNos[] = (string) ($seg['flight_no'] ?? '');
            $departs[] = (string) ($seg['depart_at'] ?? '');
        }

        return implode('|', $carriers).'#'.implode('|', $flightNos).'#'.implode('|', $departs);
    }

    /**
     * Total order for sorting (SPEC §7.2.3): price ASC, depart_at ASC, provider ASC.
     *
     * @param  array<string, mixed>  $a
     * @param  array<string, mixed>  $b
     */
    private function compare(array $a, array $b): int
    {
        $cmp = $this->comparePrice($a, $b);
        if ($cmp !== 0) {
            return $cmp;
        }

        $cmp = strcmp((string) ($a['depart_at'] ?? ''), (string) ($b['depart_at'] ?? ''));
        if ($cmp !== 0) {
            return $cmp;
        }

        return strcmp((string) ($a['provider'] ?? ''), (string) ($b['provider'] ?? ''));
    }

    /**
     * Compare prices as decimals (money is a string on the wire, §4). Exact
     * decimal ordering via integer comparison of the whole and fractional parts
     * — no float arithmetic, no bcmath dependency.
     *
     * @param  array<string, mixed>  $a
     * @param  array<string, mixed>  $b
     */
    private function comparePrice(array $a, array $b): int
    {
        return self::compareDecimalStrings(
            (string) ($a['price'] ?? '0'),
            (string) ($b['price'] ?? '0'),
        );
    }

    /**
     * Compare two non-negative decimal strings (e.g. "9.00" vs "10.5").
     * Returns -1/0/1. Prices in this system are always non-negative EUR amounts.
     */
    public static function compareDecimalStrings(string $a, string $b): int
    {
        [$ai, $af] = self::splitDecimal($a);
        [$bi, $bf] = self::splitDecimal($b);

        // Compare integer parts numerically (strip leading zeros first).
        $ai = ltrim($ai, '0') ?: '0';
        $bi = ltrim($bi, '0') ?: '0';
        if (strlen($ai) !== strlen($bi)) {
            return strlen($ai) <=> strlen($bi);
        }
        if ($ai !== $bi) {
            return strcmp($ai, $bi);
        }

        // Equal integer parts → compare fractional parts right-padded to equal length.
        $len = max(strlen($af), strlen($bf));
        $af = str_pad($af, $len, '0');
        $bf = str_pad($bf, $len, '0');

        return strcmp($af, $bf) <=> 0;
    }

    /**
     * @return array{0: string, 1: string} integer part, fractional part
     */
    private static function splitDecimal(string $value): array
    {
        $value = trim($value);
        if ($value === '' || ! is_numeric($value)) {
            $value = '0';
        }
        // Drop a sign; system prices are non-negative.
        $value = ltrim($value, '+-');
        $parts = explode('.', $value, 2);

        return [$parts[0] === '' ? '0' : $parts[0], $parts[1] ?? ''];
    }
}
