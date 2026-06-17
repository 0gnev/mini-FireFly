# mini-FireFly — developer entrypoints (SPEC §16.2).
# Normative targets: up down test itest lint seed chaos-flaky chaos-down
#                    chaos-reset demo
#
# `make chaos-flaky P=b` -> scripts/chaos.sh b flaky
# `make chaos-down  P=d` -> scripts/chaos.sh d down

# Docker Compose v2 plugin. Override with `make COMPOSE="docker-compose" ...`
# if you only have the legacy standalone binary.
COMPOSE        ?= docker compose
COMPOSE_FILE   ?= docker-compose.yml
CI_COMPOSE     ?= -f docker-compose.yml -f docker-compose.ci.yml

# Default chaos target provider for the chaos-* targets.
P ?= a

.DEFAULT_GOAL := help

.PHONY: help up down migrate seed test itest lint load \
        chaos-flaky chaos-slow chaos-down chaos-reset demo logs ps config

help: ## Show this help.
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) \
		| awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-16s\033[0m %s\n", $$1, $$2}'

up: ## Bring the full stack up (--wait), run migrations + seed.
	$(COMPOSE) -f $(COMPOSE_FILE) up -d --wait
	$(MAKE) migrate
	$(MAKE) seed
	@echo ""
	@echo "Stack is up:"
	@echo "  core        http://localhost:8000/api/v1"
	@echo "  fanout      http://localhost:8090/metrics"
	@echo "  prometheus  http://localhost:9090"
	@echo "  grafana     http://localhost:3000  (admin/admin)"

migrate: ## Run core database migrations (idempotent).
	$(COMPOSE) -f $(COMPOSE_FILE) exec -T core php artisan migrate --force

down: ## Tear the stack down and remove volumes.
	$(COMPOSE) -f $(COMPOSE_FILE) down -v

seed: ## Reseed providers + fixtures (delegates to scripts/seed.sh).
	COMPOSE="$(COMPOSE)" COMPOSE_FILE="$(COMPOSE_FILE)" ./scripts/seed.sh

test: ## Run unit tests for both stacks (no containers needed).
	@echo ">> PHP unit/feature tests (core)"
	cd core && composer install --no-interaction --prefer-dist --no-progress && \
		php artisan test
	@echo ">> Go tests (fanout)"
	cd fanout && go test -race ./...
	@echo ">> Go tests (mockprovider)"
	cd mockprovider && go test -race ./...

itest: ## Run the integration scenario suite (I1-I8) against a running stack.
	@echo ">> Integration suite (I1-I8) against the running stack."
	./scripts/itest.sh

lint: ## Run all linters (PHP pint+phpstan, Go gofmt+vet+golangci-lint).
	@echo ">> PHP lint (core)"
	cd core && composer install --no-interaction --prefer-dist --no-progress && \
		./vendor/bin/pint --test && ./vendor/bin/phpstan analyse --no-progress --memory-limit=512M
	@echo ">> Go lint (fanout)"
	cd fanout && test -z "$$(gofmt -l .)" && go vet ./...
	@echo ">> Go lint (mockprovider)"
	cd mockprovider && test -z "$$(gofmt -l .)" && go vet ./...

chaos-flaky: ## Set provider P to the 'flaky' chaos profile (e.g. P=b).
	./scripts/chaos.sh $(P) flaky

chaos-slow: ## Set provider P to the 'slow' chaos profile.
	./scripts/chaos.sh $(P) slow

chaos-down: ## Set provider P to the 'down' chaos profile (e.g. P=d).
	./scripts/chaos.sh $(P) down

chaos-reset: ## Reset all providers back to 'stable'.
	./scripts/chaos.sh a stable
	./scripts/chaos.sh b stable
	./scripts/chaos.sh c stable
	./scripts/chaos.sh d stable

demo: ## Scripted healthy -> incident -> recovery walkthrough (< 3 min).
	./scripts/demo.sh

load: ## k6 load smoke (50 RPS x 60s vs /search, SPEC §14.4). Set mixed chaos first.
	@echo ">> k6 load smoke vs http://localhost:8000 (override RPS/DURATION via env)"
	docker run --rm --add-host=host.docker.internal:host-gateway \
		-e BASE_URL=http://host.docker.internal:8000 \
		-e RPS="$(RPS)" -e DURATION="$(DURATION)" \
		-v "$(PWD)/tests/load:/scripts:ro" grafana/k6 run /scripts/smoke.js

logs: ## Tail logs for all services.
	$(COMPOSE) -f $(COMPOSE_FILE) logs -f --tail=100

ps: ## Show service status.
	$(COMPOSE) -f $(COMPOSE_FILE) ps

config: ## Validate the compose config parses (and the CI override merge).
	$(COMPOSE) -f $(COMPOSE_FILE) config -q && echo "docker-compose.yml OK"
	$(COMPOSE) $(CI_COMPOSE) config -q && echo "CI override merge OK"
