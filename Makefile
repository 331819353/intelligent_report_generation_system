.PHONY: fmt lint test test-integration test-source-integration connector-test build ci-check run-api run-worker seed-dev verify-asset-retrieval frontend-lint frontend-test frontend-build infra-config infra-up source-infra-up source-infra-status infra-down infra-reset infra-status infra-logs db-migrate db-verify db-shell clean

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

test-source-integration:
	@go test -count=1 -race -tags=sourceintegration ./test/sourceintegration/...

connector-test:
	@python3 -m pytest connector_service/tests

build:
	@mkdir -p bin
	@go build -trimpath -o bin/api ./cmd/api
	@go build -trimpath -o bin/worker ./cmd/worker

ci-check:
	@sh scripts/ci-check.sh

# Web 前端的质量检查与生产构建入口。
frontend-lint:
	@npm --prefix web run lint

frontend-test:
	@npm --prefix web run test

frontend-build:
	@npm --prefix web run build

# 本地基础设施与外部数据源模拟环境。
infra-config:
	@docker compose --env-file .env.example config --quiet

infra-up:
	@docker compose --env-file .env.example up -d --wait postgres redis minio
	@docker compose --env-file .env.example run --rm minio-init

source-infra-up:
	@docker compose --env-file .env.example up -d --wait mysql oracle connector-service

source-infra-status:
	@docker compose --env-file .env.example ps mysql oracle connector-service

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

db-shell:
	@docker compose --env-file .env.example exec postgres psql -U report_admin -d intelligent_report

# 应用进程与开发种子数据。
run-api:
	@set -a; . ./.env.example; if [ -f ./.env ]; then . ./.env; fi; set +a; go run ./cmd/api

run-worker:
	@set -a; . ./.env.example; if [ -f ./.env ]; then . ./.env; fi; set +a; go run ./cmd/worker

seed-dev:
	@set -a; . ./.env.example; if [ -f ./.env ]; then . ./.env; fi; set +a; go run ./cmd/seed

verify-asset-retrieval:
	@set -a; . ./.env.example; if [ -f ./.env ]; then . ./.env; fi; set +a; go run ./cmd/asset-retrieval-eval

clean:
	@rm -rf bin .cache coverage.out
