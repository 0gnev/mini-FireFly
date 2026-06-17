<?php

declare(strict_types=1);

namespace App\Models;

use Illuminate\Database\Eloquent\Model;
use Illuminate\Support\Carbon;

/**
 * Booking row (SPEC §11.1 / §8.2). Exposed to clients as bk_{booking_uid}.
 *
 * @property int $id
 * @property string $booking_uid
 * @property string $offer_id
 * @property array<string, mixed> $passenger
 * @property string $status
 * @property Carbon|null $created_at
 */
class Booking extends Model
{
    protected $table = 'bookings';

    protected $fillable = [
        'booking_uid',
        'offer_id',
        'passenger',
        'status',
    ];

    protected function casts(): array
    {
        return [
            'passenger' => 'array',
        ];
    }

    /** Public id form (SPEC §8.2: "bk_..."). */
    public function publicId(): string
    {
        return 'bk_'.$this->booking_uid;
    }

    /**
     * Public API representation (SPEC §8.2 / §8.3).
     *
     * @return array<string, mixed>
     */
    public function toPublicArray(): array
    {
        return [
            'booking_id' => $this->publicId(),
            'status' => $this->status,
            'offer_id' => $this->offer_id,
            'passenger' => $this->passenger,
            'created_at' => optional($this->created_at)->toRfc3339String(),
        ];
    }
}
