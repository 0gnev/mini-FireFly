<?php

declare(strict_types=1);

namespace App\Models;

use Illuminate\Database\Eloquent\Model;

/**
 * Provider registry row (SPEC §11.1).
 *
 * @property int $id
 * @property string $name
 * @property string $base_url
 * @property bool $enabled
 * @property int $timeout_ms
 * @property int $rate_limit_rps
 * @property int $breaker_failure_threshold
 * @property int $breaker_window_s
 * @property int $breaker_cooldown_s
 */
class Provider extends Model
{
    protected $table = 'providers';

    protected $fillable = [
        'name',
        'base_url',
        'enabled',
        'timeout_ms',
        'rate_limit_rps',
        'breaker_failure_threshold',
        'breaker_window_s',
        'breaker_cooldown_s',
    ];

    protected function casts(): array
    {
        return [
            'enabled' => 'boolean',
            'timeout_ms' => 'integer',
            'rate_limit_rps' => 'integer',
            'breaker_failure_threshold' => 'integer',
            'breaker_window_s' => 'integer',
            'breaker_cooldown_s' => 'integer',
        ];
    }

    /**
     * Shape this provider as the per-provider config block the fanout /v1/fanout
     * endpoint expects (SPEC §9).
     *
     * @return array<string, mixed>
     */
    public function toFanoutConfig(): array
    {
        return [
            'name' => $this->name,
            'base_url' => $this->base_url,
            'timeout_ms' => $this->timeout_ms,
            'rate_limit_rps' => $this->rate_limit_rps,
            'breaker' => [
                'failure_threshold' => $this->breaker_failure_threshold,
                'window_s' => $this->breaker_window_s,
                'cooldown_s' => $this->breaker_cooldown_s,
                'half_open_max' => 1,
            ],
        ];
    }
}
