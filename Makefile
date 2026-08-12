.PHONY: fmt lint build ci-check run-api run-worker run-connection-test-worker dev-up dev-stop dev-restart dev-status dev-logs seed-dev frontend-lint frontend-build infra-config infra-up connector-up connector-status mysql-e2e-up mysql-e2e-reset mysql-e2e-shell mysql-e2e-verify infra-down infra-reset infra-status infra-logs db-migrate db-seed-report-components db-verify warehouse-verify db-shell warehouse-shell clean

export GOCACHE ?= $(CURDIR)/.cache/go-build

# Go 后端的格式、静态检查与构建入口。
fmt:
	@gofmt -w $$(find cmd internal -name '*.go' -type f)

lint:
	@go vet ./cmd/... ./internal/...

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

frontend-build:
	@npm --prefix web run build

# 本地基础设施。常规运行不启动验收源；真实 MySQL 只在 verification profile 中启用。
infra-config:
	@docker compose --env-file .env.example config --quiet

infra-up:
	@docker compose --env-file .env.example up -d --wait postgres postgres-warehouse minio
	@docker compose --env-file .env.example run --rm minio-init

connector-up:
	@docker compose --env-file .env.example up -d --wait connector-service

connector-status:
	@docker compose --env-file .env.example ps connector-service

# 真实 MySQL 非 PG 方言验收源；只在显式 verification profile 中运行。
mysql-e2e-up:
	@compose_env='--env-file .env.example'; if [ -f .env ]; then compose_env="$$compose_env --env-file .env"; fi; docker compose $$compose_env --profile verification up -d --wait mysql-e2e connector-service

mysql-e2e-reset:
	@compose_env='--env-file .env.example'; if [ -f .env ]; then compose_env="$$compose_env --env-file .env"; fi; docker compose $$compose_env --profile verification rm -sfv mysql-e2e
	@docker volume rm intelligent-report-system_mysql_e2e_data 2>/dev/null || true
	@$(MAKE) mysql-e2e-up

mysql-e2e-shell:
	@compose_env='--env-file .env.example'; if [ -f .env ]; then compose_env="$$compose_env --env-file .env"; fi; docker compose $$compose_env --profile verification exec mysql-e2e sh -lc 'MYSQL_PWD="$$MYSQL_PASSWORD" exec mysql -u"$$MYSQL_USER" "$$MYSQL_DATABASE"'

mysql-e2e-verify:
	@compose_env='--env-file .env.example'; if [ -f .env ]; then compose_env="$$compose_env --env-file .env"; fi; \
	result="$$(docker compose $$compose_env --profile verification exec -T mysql-e2e sh -lc 'MYSQL_PWD="$$MYSQL_PASSWORD" mysql -u"$$MYSQL_USER" "$$MYSQL_DATABASE" --batch --skip-column-names -e "SELECT COUNT(*),COALESCE(SUM(sales_amount),0),COALESCE(SUM(cost_amount),0) FROM sales_order_lines"')"; \
	test "$$result" = '5	50291.00	38760.00' || { echo "unexpected MySQL verification fixture receipt"; exit 1; }; \
	echo "MySQL verification fixture is intact (5 rows, trusted aggregates match)."

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

db-seed-report-components:
	@explicit_database_url="$${DATABASE_URL:-}"; \
	set -a; \
	. ./.env.example; \
	if [ -f ./.env ]; then . ./.env; fi; \
	set +a; \
	if [ -n "$$explicit_database_url" ]; then export DATABASE_URL="$$explicit_database_url"; fi; \
	go run ./cmd/report-component-seed

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
	@set -a; . ./.env.example; if [ -f ./.env ]; then . ./.env; fi; set +a; env -u WORKER_DATABASE_URL -u CONNECTION_TEST_DATABASE_URL -u POSTGRES_USER -u POSTGRES_PASSWORD -u POSTGRES_APP_PASSWORD -u POSTGRES_WORKER_USER -u POSTGRES_WORKER_PASSWORD -u POSTGRES_CONNECTION_TEST_USER -u POSTGRES_CONNECTION_TEST_PASSWORD -u CONNECTOR_CONNECTION_TEST_TOKEN -u CONNECTION_TEST_MINIO_ACCESS_KEY -u CONNECTION_TEST_MINIO_SECRET_KEY ./scripts/run-with-nebula-role.sh api go run ./cmd/api

run-worker:
	@set -a; . ./.env.example; if [ -f ./.env ]; then . ./.env; fi; set +a; env -u DATABASE_URL -u CONNECTION_TEST_DATABASE_URL -u POSTGRES_USER -u POSTGRES_PASSWORD -u POSTGRES_APP_USER -u POSTGRES_APP_PASSWORD -u POSTGRES_WORKER_PASSWORD -u POSTGRES_CONNECTION_TEST_USER -u POSTGRES_CONNECTION_TEST_PASSWORD -u CONNECTOR_CONNECTION_TEST_TOKEN -u CONNECTION_TEST_MINIO_ACCESS_KEY -u CONNECTION_TEST_MINIO_SECRET_KEY ./scripts/run-with-nebula-role.sh worker go run ./cmd/worker

run-connection-test-worker:
	@set -a; . ./.env.example; if [ -f ./.env ]; then . ./.env; fi; set +a; env -u DATABASE_URL -u WORKER_DATABASE_URL -u POSTGRES_USER -u POSTGRES_PASSWORD -u POSTGRES_APP_USER -u POSTGRES_APP_PASSWORD -u POSTGRES_WORKER_USER -u POSTGRES_WORKER_PASSWORD -u POSTGRES_CONNECTION_TEST_PASSWORD -u CONNECTOR_INTERNAL_TOKEN -u MINIO_ACCESS_KEY -u MINIO_SECRET_KEY ./scripts/run-with-nebula-role.sh none go run ./cmd/connection-test-worker

# 由 Docker Compose 持久化应用与基础设施进程，不随调用终端退出。
dev-up:
	@./scripts/dev-services.sh start

dev-stop:
	@./scripts/dev-services.sh stop

dev-restart:
	@./scripts/dev-services.sh restart

dev-status:
	@./scripts/dev-services.sh status

dev-logs:
	@./scripts/dev-services.sh logs

seed-dev:
	@set -a; . ./.env.example; if [ -f ./.env ]; then . ./.env; fi; set +a; ./scripts/run-with-nebula-role.sh none go run ./cmd/seed

clean:
	@rm -rf bin .cache coverage.out web/dist .vite
