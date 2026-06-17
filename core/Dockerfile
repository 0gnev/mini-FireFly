# syntax=docker/dockerfile:1
# mini-FireFly core (Laravel 11, PHP 8.3) — built to run the public API,
# artisan migrate/seed, and the Redis-backed queue worker.
FROM php:8.3-cli AS base

# System deps for the PHP extensions we need.
RUN apt-get update && apt-get install -y --no-install-recommends \
        git unzip libzip-dev libicu-dev libonig-dev \
    && docker-php-ext-configure intl \
    && docker-php-ext-install -j"$(nproc)" pdo_mysql bcmath intl zip pcntl \
    && pecl install redis \
    && docker-php-ext-enable redis \
    && apt-get clean && rm -rf /var/lib/apt/lists/*

# Composer from the official image.
COPY --from=composer:2 /usr/bin/composer /usr/bin/composer

WORKDIR /app

# Install PHP deps first for better layer caching.
COPY composer.json composer.lock ./
RUN composer install --no-interaction --no-scripts --no-progress --prefer-dist

# App source.
COPY . .

RUN composer dump-autoload --optimize --no-interaction \
    && mkdir -p storage/framework/{cache/data,sessions,views} storage/logs bootstrap/cache \
    && chmod -R 777 storage bootstrap/cache

EXPOSE 8000

# Entrypoint waits for DB, migrates, seeds, then serves. (Idempotent: safe to re-run.)
COPY docker/entrypoint.sh /usr/local/bin/entrypoint.sh
RUN chmod +x /usr/local/bin/entrypoint.sh

ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
CMD ["serve"]
