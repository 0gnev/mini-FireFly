#!/bin/sh
# scripts/seed.sh — seed the MySQL `providers` table (SPEC §11.1 seeder).
#
# Delegates to Laravel's seeder inside the running `core` container, which
# inserts providers a-d pointing at the compose hostnames (mock-a..mock-d:8080).
# Run via `make seed` (which is also called by `make up`).
#
# Env (set by the Makefile, with sane defaults here too):
#   COMPOSE       docker compose invocation (default: "docker compose")
#   COMPOSE_FILE  compose file (default: docker-compose.yml)
set -eu

COMPOSE="${COMPOSE:-docker compose}"
COMPOSE_FILE="${COMPOSE_FILE:-docker-compose.yml}"

# Resolve repo root so the script works from any CWD.
SCRIPT_DIR=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
REPO_ROOT=$(CDPATH='' cd -- "${SCRIPT_DIR}/.." && pwd)
cd "${REPO_ROOT}"

echo ">> Seeding providers via core db:seed ..."
# -T disables pseudo-tty allocation (required in CI / non-interactive shells).
# shellcheck disable=SC2086
${COMPOSE} -f "${COMPOSE_FILE}" exec -T core \
  php artisan db:seed --class=ProviderSeeder --force

echo ">> Seed complete."
