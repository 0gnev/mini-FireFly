<?php

declare(strict_types=1);

namespace App\Logging;

/**
 * Holds the current request_id (ULID with req_ prefix, minted by core, SPEC
 * §12.4) so the log formatter can stamp every line without threading it through
 * every call. Process-local; reset per request by middleware.
 */
final class RequestId
{
    private static ?string $current = null;

    public static function set(?string $id): void
    {
        self::$current = $id;
    }

    public static function current(): ?string
    {
        return self::$current;
    }
}
