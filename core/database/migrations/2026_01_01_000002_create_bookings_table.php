<?php

declare(strict_types=1);

use Illuminate\Database\Migrations\Migration;
use Illuminate\Database\Schema\Blueprint;
use Illuminate\Support\Facades\Schema;

/**
 * Bookings (SPEC §11.1). booking_uid is a CHAR(26) ULID, exposed to clients as
 * bk_{ulid}. Offers aren't persisted — we store the offer_id and a passenger
 * snapshot (documented simplification, SPEC §8.2).
 */
return new class extends Migration
{
    public function up(): void
    {
        Schema::create('bookings', function (Blueprint $table) {
            $table->bigIncrements('id');
            $table->char('booking_uid', 26)->unique();      // ULID
            $table->string('offer_id', 64);
            $table->json('passenger');
            $table->enum('status', ['confirmed', 'cancelled'])->default('confirmed');
            $table->timestamp('created_at')->nullable();
            $table->timestamp('updated_at')->nullable();
        });
    }

    public function down(): void
    {
        Schema::dropIfExists('bookings');
    }
};
