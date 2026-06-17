<?php

declare(strict_types=1);

namespace App\Models;

use Illuminate\Database\Eloquent\Model;

/**
 * Idempotency record (SPEC §11.1 / §7.3).
 *
 * @property string $idem_key
 * @property string $request_hash
 * @property string $response_body
 * @property int $status_code
 */
class IdempotencyKey extends Model
{
    protected $table = 'idempotency_keys';

    protected $primaryKey = 'idem_key';

    public $incrementing = false;

    protected $keyType = 'string';

    // Spec table has only created_at (no updated_at).
    public const UPDATED_AT = null;

    protected $fillable = [
        'idem_key',
        'request_hash',
        'response_body',
        'status_code',
    ];

    protected function casts(): array
    {
        return [
            'status_code' => 'integer',
        ];
    }
}
