<?php

declare(strict_types=1);

namespace App\Services;

use App\Models\Booking;
use App\Models\IdempotencyKey;
use App\Support\CanonicalJson;
use Illuminate\Database\QueryException;
use Illuminate\Support\Facades\Log;
use Illuminate\Support\Facades\Redis;
use Illuminate\Support\Str;
use Throwable;

/**
 * Idempotent booking creation (SPEC §7.3).
 *
 * Algorithm:
 *   1. SET idem:{key} {request_hash} NX EX 86400
 *   2. set succeeded → fresh: create booking (status=confirmed), persist
 *      (key, hash, response) in MySQL, return 201
 *   3. key exists → load from MySQL: same hash → replay 200 + Idempotency-Replayed,
 *      different hash → 409 idempotency_key_reuse
 *   4. Redis down → fall back to MySQL unique-constraint insert on the key
 *
 * The returned BookingResult carries the body, HTTP status, and a "replayed"
 * flag so the controller can set the Idempotency-Replayed header.
 */
class BookingService
{
    private const IDEM_TTL_S = 86400; // 24h (SPEC §11.2)

    public function __construct(private readonly Metrics $metrics) {}

    /**
     * @param  array{offer_id: string, passenger: array<string, mixed>}  $payload
     */
    public function book(string $idempotencyKey, array $payload): BookingResult
    {
        $requestHash = CanonicalJson::sha256($payload);

        $lock = $this->tryAcquireRedisLock($idempotencyKey, $requestHash);

        if ($lock === 'acquired') {
            return $this->createFresh($idempotencyKey, $requestHash, $payload);
        }

        if ($lock === 'exists') {
            return $this->replayOrConflict($idempotencyKey, $requestHash);
        }

        // $lock === 'unavailable' → Redis down: fall back to MySQL uniqueness.
        return $this->createWithDbFallback($idempotencyKey, $requestHash, $payload);
    }

    /**
     * @return 'acquired'|'exists'|'unavailable'
     */
    private function tryAcquireRedisLock(string $key, string $requestHash): string
    {
        try {
            // SET idem:{key} {hash} EX 86400 NX. NOTE: Laravel's Redis::set takes
            // FLAT args set(key, value, 'EX', ttl, 'NX') — the raw phpredis
            // array-options form (['NX','EX'=>ttl]) throws "access offset of array
            // on array" and would silently force the MySQL fallback on every call.
            // Returns true on first set, false (null) when the key already exists.
            // larastan's Redis facade stub types set() as 2-3 args and doesn't model
            // this (correct, runtime-verified) variadic form, so ignore its counts.
            // @phpstan-ignore arguments.count, argument.type
            $ok = Redis::set('idem:'.$key, $requestHash, 'EX', self::IDEM_TTL_S, 'NX');

            return $ok ? 'acquired' : 'exists';
        } catch (Throwable $e) {
            Log::warning('idempotency Redis lock unavailable, falling back to MySQL', [
                'error' => $e->getMessage(),
            ]);

            return 'unavailable';
        }
    }

    /**
     * @param  array{offer_id: string, passenger: array<string, mixed>}  $payload
     */
    private function createFresh(string $key, string $requestHash, array $payload): BookingResult
    {
        // Redis claimed the key; but a prior fresh request may have crashed after
        // claiming and before persisting. If a row already exists, defer to replay.
        $existing = IdempotencyKey::query()->find($key);
        if ($existing !== null) {
            return $this->replayFromRecord($existing, $requestHash);
        }

        return $this->persistBooking($key, $requestHash, $payload);
    }

    /**
     * Redis-down path: create the booking, then claim the key via a MySQL unique
     * insert. A duplicate-key error means someone already booked under this key →
     * replay/conflict from the stored record.
     *
     * @param  array{offer_id: string, passenger: array<string, mixed>}  $payload
     */
    private function createWithDbFallback(string $key, string $requestHash, array $payload): BookingResult
    {
        $existing = IdempotencyKey::query()->find($key);
        if ($existing !== null) {
            return $this->replayFromRecord($existing, $requestHash);
        }

        try {
            return $this->persistBooking($key, $requestHash, $payload);
        } catch (QueryException $e) {
            // Unique-constraint race: another writer won. Re-read and replay.
            $record = IdempotencyKey::query()->find($key);
            if ($record !== null) {
                return $this->replayFromRecord($record, $requestHash);
            }
            throw $e;
        }
    }

    /**
     * @param  array{offer_id: string, passenger: array<string, mixed>}  $payload
     */
    private function persistBooking(string $key, string $requestHash, array $payload): BookingResult
    {
        $booking = new Booking([
            'booking_uid' => (string) Str::ulid(),
            'offer_id' => $payload['offer_id'],
            'passenger' => $payload['passenger'],
            'status' => 'confirmed',
        ]);
        $booking->save();

        $body = $booking->toPublicArray();

        // The unique PK insert here is what enforces idempotency when Redis is down.
        IdempotencyKey::query()->create([
            'idem_key' => $key,
            'request_hash' => $requestHash,
            'response_body' => json_encode($body, JSON_UNESCAPED_SLASHES | JSON_UNESCAPED_UNICODE),
            'status_code' => 201,
        ]);

        $this->metrics->increment('core_bookings_total', ['replayed' => 'false']);
        Log::info('booking created', ['booking_id' => $body['booking_id'], 'idem_key' => $key]);

        return new BookingResult($body, 201, false);
    }

    private function replayOrConflict(string $key, string $requestHash): BookingResult
    {
        $record = IdempotencyKey::query()->find($key);

        if ($record === null) {
            // Redis says the key exists but MySQL has no record yet — the original
            // request is still in flight or crashed mid-write. Treat as conflict
            // would be wrong; report a benign retryable error code.
            Log::warning('idempotency key locked in Redis but absent in MySQL', ['idem_key' => $key]);

            return new BookingResult(
                ['error' => 'idempotency_in_progress', 'message' => 'A request with this key is being processed.'],
                409,
                false,
            );
        }

        return $this->replayFromRecord($record, $requestHash);
    }

    private function replayFromRecord(IdempotencyKey $record, string $requestHash): BookingResult
    {
        if (! hash_equals($record->request_hash, $requestHash)) {
            return new BookingResult(['error' => 'idempotency_key_reuse'], 409, false);
        }

        /** @var array<string, mixed> $body */
        $body = json_decode($record->response_body, true) ?: [];

        $this->metrics->increment('core_bookings_total', ['replayed' => 'true']);
        Log::info('booking replayed', ['idem_key' => $record->idem_key]);

        // SPEC §7.3 step 3: a replay is always 200 (the original creation was 201),
        // with the stored response body and the Idempotency-Replayed header.
        return new BookingResult($body, 200, true);
    }
}
