#!/usr/bin/env bash
# mini-FireFly core entrypoint.
#  - serve : wait for MySQL, run migrations + seed, start the dev HTTP server.
#  - queue : run the Redis-backed queue worker (ClickHouse logging jobs).
#  - migrate: run migrations + seed only, then exit.
set -euo pipefail

cd /app

wait_for_mysql() {
    echo '{"level":"info","service":"core","msg":"waiting for mysql"}'
    for _ in $(seq 1 60); do
        if php -r '
            $h=getenv("DB_HOST")?:"mysql";
            $p=(int)(getenv("DB_PORT")?:3306);
            $c=@fsockopen($h,$p,$e,$s,1);
            exit($c?0:1);
        '; then
            echo '{"level":"info","service":"core","msg":"mysql reachable"}'
            return 0
        fi
        sleep 2
    done
    echo '{"level":"error","service":"core","msg":"mysql never became reachable"}'
    return 1
}

case "${1:-serve}" in
    serve)
        wait_for_mysql
        php artisan migrate --force --no-interaction
        php artisan db:seed --class=ProviderSeeder --force --no-interaction || true
        php artisan config:cache || true
        echo '{"level":"info","service":"core","msg":"starting http server on 0.0.0.0:8000"}'
        exec php artisan serve --host=0.0.0.0 --port=8000
        ;;
    queue)
        wait_for_mysql
        echo '{"level":"info","service":"core","msg":"starting queue worker"}'
        exec php artisan queue:work redis --sleep=1 --tries=3 --max-time=3600
        ;;
    migrate)
        wait_for_mysql
        php artisan migrate --force --no-interaction
        exec php artisan db:seed --class=ProviderSeeder --force --no-interaction
        ;;
    *)
        exec "$@"
        ;;
esac
