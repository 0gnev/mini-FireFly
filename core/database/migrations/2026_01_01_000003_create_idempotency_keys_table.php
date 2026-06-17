<?php

declare(strict_types=1);

use Illuminate\Database\Migrations\Migration;
use Illuminate\Database\Schema\Blueprint;
use Illuminate\Support\Facades\Schema;

/**
 * Idempotency records (SPEC §11.1 / §7.3). idem_key is the PRIMARY KEY so the
 * MySQL unique-constraint fallback works when Redis is down (SPEC §7.3 step 4).
 */
return new class extends Migration
{
    public function up(): void
    {
        Schema::create('idempotency_keys', function (Blueprint $table) {
            $table->string('idem_key', 128)->primary();
            $table->char('request_hash', 64);
            $table->mediumText('response_body');
            $table->smallInteger('status_code');
            $table->timestamp('created_at')->useCurrent();
        });
    }

    public function down(): void
    {
        Schema::dropIfExists('idempotency_keys');
    }
};
