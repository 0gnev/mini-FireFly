<?php

declare(strict_types=1);

use Illuminate\Support\Facades\Queue;

beforeEach(function (): void {
    Queue::fake();
});

/**
 * @return array<string, mixed>
 */
function validSearchPayload(array $overrides = []): array
{
    return array_merge([
        'origin' => 'BEG',
        'destination' => 'AMS',
        'depart_date' => now()->addDays(10)->toDateString(),
        'return_date' => null,
        'passengers' => 1,
    ], $overrides);
}

it('rejects unknown origin airport with 422', function (): void {
    $this->postJson('/api/v1/search', validSearchPayload(['origin' => 'ZZZ']))
        ->assertStatus(422)
        ->assertJsonValidationErrors(['origin']);
});

it('rejects unknown destination airport with 422', function (): void {
    $this->postJson('/api/v1/search', validSearchPayload(['destination' => 'QQQ']))
        ->assertStatus(422)
        ->assertJsonValidationErrors(['destination']);
});

it('rejects identical origin and destination with 422', function (): void {
    $this->postJson('/api/v1/search', validSearchPayload(['destination' => 'BEG']))
        ->assertStatus(422)
        ->assertJsonValidationErrors(['destination']);
});

it('rejects a depart_date in the past', function (): void {
    $this->postJson('/api/v1/search', validSearchPayload(['depart_date' => now()->subDay()->toDateString()]))
        ->assertStatus(422)
        ->assertJsonValidationErrors(['depart_date']);
});

it('rejects a depart_date beyond today + 365 days', function (): void {
    $this->postJson('/api/v1/search', validSearchPayload(['depart_date' => now()->addDays(400)->toDateString()]))
        ->assertStatus(422)
        ->assertJsonValidationErrors(['depart_date']);
});

it('rejects a return_date earlier than depart_date', function (): void {
    $depart = now()->addDays(20)->toDateString();
    $this->postJson('/api/v1/search', validSearchPayload([
        'depart_date' => $depart,
        'return_date' => now()->addDays(10)->toDateString(),
    ]))->assertStatus(422)->assertJsonValidationErrors(['return_date']);
});

it('rejects passengers below 1 and above 9', function (): void {
    $this->postJson('/api/v1/search', validSearchPayload(['passengers' => 0]))
        ->assertStatus(422)->assertJsonValidationErrors(['passengers']);

    $this->postJson('/api/v1/search', validSearchPayload(['passengers' => 10]))
        ->assertStatus(422)->assertJsonValidationErrors(['passengers']);
});

it('rejects a malformed IATA code (wrong length)', function (): void {
    $this->postJson('/api/v1/search', validSearchPayload(['origin' => 'BE']))
        ->assertStatus(422)->assertJsonValidationErrors(['origin']);
});
