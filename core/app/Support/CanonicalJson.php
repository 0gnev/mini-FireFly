<?php

declare(strict_types=1);

namespace App\Support;

/**
 * Canonical JSON encoding (SPEC §10.6).
 *
 * THE single place where canonicalization happens. Rules:
 *   - object keys sorted lexicographically, recursively,
 *   - no insignificant whitespace,
 *   - nulls included (never dropped),
 *   - slashes / unicode left unescaped so the bytes are stable & human-greppable.
 *
 * This is the only function that may produce the bytes hashed into a cache key,
 * so cache correctness depends on it being deterministic for equal logical input.
 */
final class CanonicalJson
{
    /**
     * Encode a value to canonical JSON.
     *
     * @param  mixed  $value
     */
    public static function encode($value): string
    {
        $normalized = self::normalize($value);

        $json = json_encode($normalized, JSON_UNESCAPED_SLASHES | JSON_UNESCAPED_UNICODE | JSON_THROW_ON_ERROR);

        return $json;
    }

    /**
     * Compute the search cache key (SPEC §10.6: search:{sha256(canonical_json(query))}).
     *
     * @param  mixed  $query
     */
    public static function searchCacheKey($query): string
    {
        return 'search:'.hash('sha256', self::encode($query));
    }

    /**
     * sha256 over the canonical encoding — used for the idempotency request hash
     * (SPEC §7.3). Stable regardless of incoming key order.
     *
     * @param  mixed  $value
     */
    public static function sha256($value): string
    {
        return hash('sha256', self::encode($value));
    }

    /**
     * Recursively sort associative-array keys; leave lists in order.
     *
     * @param  mixed  $value
     * @return mixed
     */
    private static function normalize($value)
    {
        if (! is_array($value)) {
            return $value;
        }

        if (self::isList($value)) {
            return array_map([self::class, 'normalize'], $value);
        }

        ksort($value, SORT_STRING);

        $out = [];
        foreach ($value as $k => $v) {
            $out[$k] = self::normalize($v);
        }

        return $out;
    }

    /** @param array<mixed> $value */
    private static function isList(array $value): bool
    {
        return array_is_list($value);
    }
}
