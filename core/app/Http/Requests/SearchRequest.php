<?php

declare(strict_types=1);

namespace App\Http\Requests;

use App\Support\AirportRepository;
use Illuminate\Foundation\Http\FormRequest;
use Illuminate\Validation\Validator;

/**
 * Validates & normalizes the canonical Query (SPEC §4.1).
 *
 * Normalization (uppercasing IATA, defaulting passengers) happens in
 * prepareForValidation so the validation rules and the downstream service see
 * the canonical form. Failure yields Laravel-style 422 field errors (SPEC §8.1).
 */
class SearchRequest extends FormRequest
{
    public function authorize(): bool
    {
        return true;
    }

    protected function prepareForValidation(): void
    {
        $origin = $this->input('origin');
        $destination = $this->input('destination');

        $this->merge([
            'origin' => is_string($origin) ? strtoupper(trim($origin)) : $origin,
            'destination' => is_string($destination) ? strtoupper(trim($destination)) : $destination,
        ]);
    }

    /**
     * @return array<string, mixed>
     */
    public function rules(): array
    {
        $today = now()->utc()->toDateString();
        $max = now()->utc()->addDays(365)->toDateString();

        return [
            'origin' => ['required', 'string', 'size:3', 'regex:/^[A-Z]{3}$/'],
            'destination' => ['required', 'string', 'size:3', 'regex:/^[A-Z]{3}$/', 'different:origin'],
            'depart_date' => ['required', 'date_format:Y-m-d', "after_or_equal:$today", "before_or_equal:$max"],
            'return_date' => ['nullable', 'date_format:Y-m-d', 'after_or_equal:depart_date'],
            'passengers' => ['required', 'integer', 'min:1', 'max:9'],
        ];
    }

    /**
     * Cross-field / fixture-backed checks that the rule array can't express.
     */
    public function withValidator(Validator $validator): void
    {
        $validator->after(function (Validator $v): void {
            /** @var AirportRepository $airports */
            $airports = app(AirportRepository::class);

            $origin = $this->input('origin');
            $destination = $this->input('destination');

            if (is_string($origin) && preg_match('/^[A-Z]{3}$/', $origin) && ! $airports->exists($origin)) {
                $v->errors()->add('origin', 'The selected origin is not a known airport.');
            }
            if (is_string($destination) && preg_match('/^[A-Z]{3}$/', $destination) && ! $airports->exists($destination)) {
                $v->errors()->add('destination', 'The selected destination is not a known airport.');
            }
        });
    }

    /**
     * The normalized canonical query (SPEC §4.1) — passengers cast to int,
     * return_date null when absent.
     *
     * @return array{origin: string, destination: string, depart_date: string, return_date: string|null, passengers: int}
     */
    public function canonicalQuery(): array
    {
        $returnDate = $this->input('return_date');

        return [
            'origin' => (string) $this->input('origin'),
            'destination' => (string) $this->input('destination'),
            'depart_date' => (string) $this->input('depart_date'),
            'return_date' => ($returnDate === null || $returnDate === '') ? null : (string) $returnDate,
            'passengers' => (int) $this->input('passengers'),
        ];
    }
}
