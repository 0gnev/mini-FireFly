<?php

declare(strict_types=1);

namespace App\Support;

/**
 * IATA airport lookup used for request validation (SPEC §4.1: origin/destination
 * "must exist in fixtures/airports.json").
 *
 * Source resolution order:
 *   1. AIRPORTS_FILE env override (an explicit path),
 *   2. the repo's fixtures/airports.json (mounted into the container),
 *   3. a baked-in copy under resources/data so unit tests / standalone runs work.
 *
 * Loaded once per process and memoised.
 */
class AirportRepository
{
    /** @var array<string, array{iata: string, city: string, tz: string}>|null */
    private ?array $byIata = null;

    private ?string $explicitPath;

    public function __construct(?string $explicitPath = null)
    {
        if ($explicitPath !== null) {
            $this->explicitPath = $explicitPath;
        } else {
            $configured = config('firefly.airports_file');
            $this->explicitPath = is_string($configured) && $configured !== '' ? $configured : null;
        }
    }

    /** @return array<string, array{iata: string, city: string, tz: string}> */
    private function index(): array
    {
        if ($this->byIata !== null) {
            return $this->byIata;
        }

        $raw = $this->load();
        $index = [];
        foreach ($raw as $row) {
            if (! is_array($row) || ! isset($row['iata'])) {
                continue;
            }
            $iata = strtoupper((string) $row['iata']);
            $index[$iata] = [
                'iata' => $iata,
                'city' => (string) ($row['city'] ?? ''),
                'tz' => (string) ($row['tz'] ?? 'UTC'),
            ];
        }

        return $this->byIata = $index;
    }

    /** @return array<int, mixed> */
    private function load(): array
    {
        foreach ($this->candidatePaths() as $path) {
            if ($path !== null && is_file($path)) {
                $contents = file_get_contents($path);
                if ($contents !== false) {
                    /** @var array<int, mixed>|null $decoded */
                    $decoded = json_decode($contents, true);
                    if (is_array($decoded)) {
                        return $decoded;
                    }
                }
            }
        }

        return [];
    }

    /** @return array<int, string|null> */
    private function candidatePaths(): array
    {
        return [
            $this->explicitPath,
            base_path('../fixtures/airports.json'),
            resource_path('data/airports.json'),
        ];
    }

    public function exists(string $iata): bool
    {
        return isset($this->index()[strtoupper($iata)]);
    }

    public function timezone(string $iata): ?string
    {
        return $this->index()[strtoupper($iata)]['tz'] ?? null;
    }

    /** @return array<int, string> */
    public function all(): array
    {
        return array_keys($this->index());
    }
}
