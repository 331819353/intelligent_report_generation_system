.PHONY: fmt lint test test-integration connector-test build ci-check run-api run-worker run-connection-test-worker seed-dev verify-asset-retrieval frontend-lint frontend-test frontend-build infra-config infra-up connector-up connector-status infra-down infra-reset infra-status infra-logs db-migrate db-verify warehouse-verify db-shell warehouse-shell clean

export GOCACHE ?= $(CURDIR)/.cache/go-build

# Go 后端的格式、静态检查、测试与构建入口。
fmt:
	@gofmt -w $$(find cmd internal -name '*.go' -type f)

lint:
	@go vet ./cmd/... ./internal/...

test:
	@go test -race -cover ./cmd/... ./internal/...

test-integration:
	@go test -race -tags=integration ./test/integration/...

connector-test:
	@python3 -m pytest connector_service/tests

build:
	@mkdir -p bin
	@go build -trimpath -o bin/api ./cmd/api
	@go build -trimpath -o bin/worker ./cmd/worker
	@go build -trimpath -o bin/connection-test-worker ./cmd/connection-test-worker

ci-check:
	@sh scripts/ci-check.sh

# Web 前端的质量检查与生产构建入口。
frontend-lint:
	@npm --prefix web run lint

frontend-test:
	@npm --prefix web run test

frontend-build:
	@npm --prefix web run build

# 本地基础设施。外部 MySQL/Oracle 由用户配置，不在项目内启动测试实例。
infra-config:
	@docker compose --env-file .env.example config --quiet

infra-up:
	@docker compose --env-file .env.example up -d --wait postgres postgres-warehouse redis minio
	@docker compose --env-file .env.example run --rm minio-init

connector-up:
	@docker compose --env-file .env.example up -d --wait connector-service

connector-status:
	@docker compose --env-file .env.example ps connector-service

infra-down:
	@docker compose --env-file .env.example down

infra-reset:
	@docker compose --env-file .env.example down --volumes --remove-orphans
	@$(MAKE) infra-up

infra-status:
	@docker compose --env-file .env.example ps

infra-logs:
	@docker compose --env-file .env.example logs --tail=200

# 数据库迁移、约束验证和交互式终端。
db-migrate:
	@./scripts/migrate.sh

db-verify:
	@./scripts/verify-database.sh

warehouse-verify:
	@./scripts/verify-warehouse.sh

db-shell:
	@docker compose --env-file .env.example exec postgres psql -U report_admin -d intelligent_report_control

warehouse-shell:
	@docker compose --env-file .env.example exec postgres-warehouse psql -U warehouse_admin -d intelligent_report_warehouse

# 应用进程与开发种子数据。
run-api:
	@set -a; . ./.env.example; if [ -f ./.env ]; then . ./.env; fi; set +a; env -u WORKER_DATABASE_URL -u CONNECTION_TEST_DATABASE_URL -u POSTGRES_USER -u POSTGRES_PASSWORD -u POSTGRES_APP_PASSWORD -u POSTGRES_WORKER_USER -u POSTGRES_WORKER_PASSWORD -u POSTGRES_CONNECTION_TEST_USER -u POSTGRES_CONNECTION_TEST_PASSWORD -u CONNECTOR_CONNECTION_TEST_TOKEN -u CONNECTION_TEST_MINIO_ACCESS_KEY -u CONNECTION_TEST_MINIO_SECRET_KEY go run ./cmd/api

run-worker:
	@set -a; . ./.env.example; if [ -f ./.env ]; then . ./.env; fi; set +a; env -u DATABASE_URL -u CONNECTION_TEST_DATABASE_URL -u POSTGRES_USER -u POSTGRES_PASSWORD -u POSTGRES_APP_USER -u POSTGRES_APP_PASSWORD -u POSTGRES_WORKER_PASSWORD -u POSTGRES_CONNECTION_TEST_USER -u POSTGRES_CONNECTION_TEST_PASSWORD -u CONNECTOR_CONNECTION_TEST_TOKEN -u CONNECTION_TEST_MINIO_ACCESS_KEY -u CONNECTION_TEST_MINIO_SECRET_KEY go run ./cmd/worker

run-connection-test-worker:
	@set -a; . ./.env.example; if [ -f ./.env ]; then . ./.env; fi; set +a; env -u DATABASE_URL -u WORKER_DATABASE_URL -u POSTGRES_USER -u POSTGRES_PASSWORD -u POSTGRES_APP_USER -u POSTGRES_APP_PASSWORD -u POSTGRES_WORKER_USER -u POSTGRES_WORKER_PASSWORD -u POSTGRES_CONNECTION_TEST_PASSWORD -u CONNECTOR_INTERNAL_TOKEN -u MINIO_ACCESS_KEY -u MINIO_SECRET_KEY go run ./cmd/connection-test-worker

seed-dev:
	@set -a; . ./.env.example; if [ -f ./.env ]; then . ./.env; fi; set +a; go run ./cmd/seed

verify-asset-retrieval:
	@set -a; . ./.env.example; if [ -f ./.env ]; then . ./.env; fi; set +a; go run ./cmd/asset-retrieval-eval

clean:
	@rm -rf bin .cache coverage.out
