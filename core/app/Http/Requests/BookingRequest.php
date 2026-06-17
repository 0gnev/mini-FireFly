<?php

declare(strict_types=1);

namespace App\Http\Requests;

use Illuminate\Foundation\Http\FormRequest;

/**
 * Validates a booking body (SPEC §8.2). Unknown offer_id format → 422
 * (offer_id must look like "{provider}:{8 hex}", §4.2). The Idempotency-Key
 * header is validated separately in the controller/service (it's a header, not a
 * body field).
 */
class BookingRequest extends FormRequest
{
    public function authorize(): bool
    {
        return true;
    }

    /**
     * @return array<string, mixed>
     */
    public function rules(): array
    {
        return [
            'offer_id' => ['required', 'string', 'max:64', 'regex:/^[a-z]:[0-9a-f]{8}$/'],
            'passenger' => ['required', 'array'],
            'passenger.first_name' => ['required', 'string', 'max:128'],
            'passenger.last_name' => ['required', 'string', 'max:128'],
            'passenger.email' => ['required', 'email', 'max:255'],
        ];
    }

    /**
     * @return array<string, string>
     */
    public function messages(): array
    {
        return [
            'offer_id.regex' => 'The offer_id is malformed; expected "{provider}:{8 hex chars}".',
        ];
    }

    /**
     * Normalized booking payload (the fields that define the idempotency hash).
     *
     * @return array{offer_id: string, passenger: array<string, mixed>}
     */
    public function bookingPayload(): array
    {
        /** @var array<string, mixed> $passenger */
        $passenger = (array) $this->input('passenger');

        return [
            'offer_id' => (string) $this->input('offer_id'),
            'passenger' => $passenger,
        ];
    }
}
