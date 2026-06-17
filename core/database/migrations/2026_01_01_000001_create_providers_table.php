<?php

declare(strict_types=1);

use Illuminate\Database\Migrations\Migration;
use Illuminate\Database\Schema\Blueprint;
use Illuminate\Support\Facades\Schema;

/**
 * Provider registry (SPEC §11.1). Column names/types/defaults match the spec
 * DDL exactly; fanout reads these per-request via core's /v1/fanout call.
 */
return new class extends Migration
{
    public function up(): void
    {
        Schema::create('providers', function (Blueprint $table) {
            $table->bigIncrements('id');
            $table->string('name', 16)->unique();          // 'a','b','c','d'
            $table->string('base_url', 255);
            $table->boolean('enabled')->default(true);      // TINYINT(1) DEFAULT 1
            $table->integer('timeout_ms')->default(800);
            $table->integer('rate_limit_rps')->default(40);
            $table->integer('breaker_failure_threshold')->default(5);
            $table->integer('breaker_window_s')->default(30);
            $table->integer('breaker_cooldown_s')->default(15);
            $table->timestamp('created_at')->nullable();
            $table->timestamp('updated_at')->nullable();
        });
    }

    public function down(): void
    {
        Schema::dropIfExists('providers');
    }
};
