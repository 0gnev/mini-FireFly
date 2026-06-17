<?php

declare(strict_types=1);

namespace App\Services;

use ClickHouseDB\Client as SmiClient;
use Illuminate\Support\Facades\Log;
use Throwable;

/**
 * Thin wrapper around the smi2/phpclickhouse HTTP client (SPEC §11.3).
 *
 * Used for:
 *   - buffered async inserts of provider_calls rows (write path, SPEC §7.1),
 *   - the last_5m read query for GET /providers (read path, SPEC §8.4).
 *
 * Every method is loss-tolerant: ClickHouse being down must never surface to the
 * user (SPEC §7.4). Inserts return bool; reads return null on failure and the
 * caller degrades gracefully.
 */
class ClickHouseClient
{
    private ?SmiClient $client = null;

    /**
     * @param  array{dsn: string, database: string, username: string, password: string, timeout: float}  $config
     */
    public function __construct(private readonly array $config) {}

    private function client(): SmiClient
    {
        if ($this->client !== null) {
            return $this->client;
        }

        $parts = parse_url($this->config['dsn']);
        $host = $parts['host'] ?? 'clickhouse';
        $port = $parts['port'] ?? 8123;
        $https = ($parts['scheme'] ?? 'http') === 'https';

        $client = new SmiClient([
            'host' => $host,
            'port' => (string) $port,
            'username' => $this->config['username'],
            'password' => $this->config['password'],
            'https' => $https,
        ]);
        $client->database($this->config['database']);
        $client->setTimeout($this->config['timeout']);
        $client->setConnectTimeOut(1);

        return $this->client = $client;
    }

    /**
     * Bulk-insert provider_calls rows. Returns false on any failure (caller logs
     * & lets the queue retry; a permanently dropped row never fails a request).
     *
     * @param  array<int, array<int, mixed>>  $rows  positional rows matching $columns
     * @param  array<int, string>  $columns
     */
    public function insertProviderCalls(array $rows, array $columns): bool
    {
        if ($rows === []) {
            return true;
        }

        try {
            $this->client()->insert('firefly.provider_calls', $rows, $columns);

            return true;
        } catch (Throwable $e) {
            Log::warning('clickhouse insert failed', ['error' => $e->getMessage()]);

            return false;
        }
    }

    /**
     * Run a read query and return rows, or null on failure.
     *
     * @param  array<string, mixed>  $bindings
     * @return array<int, array<string, mixed>>|null
     */
    public function select(string $sql, array $bindings = []): ?array
    {
        try {
            /** @var array<int, array<string, mixed>> $rows */
            $rows = $this->client()->select($sql, $bindings)->rows();

            return $rows;
        } catch (Throwable $e) {
            Log::warning('clickhouse select failed', ['error' => $e->getMessage()]);

            return null;
        }
    }
}
