# 本地开发说明

## 环境要求

- Go 1.26 或兼容版本
- Node.js 25、npm 11 或兼容版本
- Make

## 安装前端依赖

```bash
npm --prefix web install
```

## 本地基础设施

本地依赖由 [compose.yaml](../compose.yaml) 管理：

| 服务 | 地址 | 用途 |
|---|---|---|
| PostgreSQL | `127.0.0.1:5432` | 控制库、配置、版本和审计 |
| Redis | `127.0.0.1:6379` | 查询缓存、分布式状态和短期任务协调 |
| MinIO API | `127.0.0.1:9000` | 报告 JSON、Excel、附件、快照和 PDF |
| MinIO Console | `http://127.0.0.1:9001` | 本地对象存储管理界面 |

PostgreSQL 使用两个不同账号：`report_admin` 仅用于迁移，`report_app` 是无超级用户、无建库和无建角色权限的运行账号。API 必须使用 `DATABASE_URL` 中的 `report_app`，否则 RLS 失去第二层隔离价值。

安装 Docker Desktop 或兼容的 Docker Engine/Compose 后执行：

```bash
make infra-config
make infra-up
make infra-status
make db-migrate
make db-verify
```

`infra-up` 会等待 PostgreSQL、Redis 和 MinIO 健康，并自动创建：

- `reports`：已发布报告 JSON 和 PDF；
- `uploads`：Excel、图片和附件；
- `snapshots`：报告运行及数据快照。

`make db-verify` 在事务中创建两个临时租户，验证 RLS 数据隔离、跨租户角色关联阻断和审计日志不可变，最后回滚全部验证数据。

### 数据库迁移策略

- 已在共享环境执行的迁移文件禁止修改、重命名或删除；
- Schema 变更采用只向前迁移，修复时新增更高版本的迁移文件；
- 高风险迁移先采用“扩展字段/双写/切换读取/清理旧结构”步骤，避免单次破坏性变更；
- 本地尚无业务数据时可使用 `make infra-reset` 重建，测试及生产环境禁止用重建替代修复迁移；
- 每次部署先备份控制库并记录已应用的 `platform_schema_migrations` 版本。

常用维护命令：

```bash
make infra-logs
make db-shell
make infra-down
make infra-reset
```

`make infra-reset` 会删除本地 PostgreSQL、Redis 和 MinIO 数据卷，仅应用于开发环境。仓库中的 `.env.example` 使用公开的本地开发默认值；生产环境必须通过密钥系统注入独立强凭证。

如果当前机器暂未安装 Docker，可先执行不依赖 Docker 的静态检查：

```bash
./scripts/check-compose.sh
sh -n scripts/migrate.sh
```

## 模型配置

模型配置只能通过运行环境注入。`AI_API_KEY` 不得写入仓库：

```bash
export AI_BASE_URL="https://mgallery.haier.net/v1/"
export AI_MODEL="deepseek-v3"
export AI_API_KEY="<从密钥管理系统或本地安全环境注入>"
export API_WRITE_TIMEOUT="120s"
export AI_REQUEST_TIMEOUT="100s"
export AI_ATTEMPT_TIMEOUT="90s"
export AI_MAX_ATTEMPTS="1"
export AI_CONFIDENCE_THRESHOLD="0.8"
```

上述超时用于本地 `deepseek-v3` 元数据验收；应按实际 Provider 延迟调整，并始终保持 `AI_REQUEST_TIMEOUT < API_WRITE_TIMEOUT`。批量加工仍应使用持久化异步任务，不能通过无限放大同步 HTTP 超时替代。未设置 `AI_API_KEY` 时元数据 AI 接口明确降级为不可用，非 AI 功能继续运行。元数据 AI API、结构化输出约束和审计字段见 `docs/api-metadata-ai.md`。

## 启动 API

```bash
set -a
. ./.env.example
. ./.env
set +a
make run-api
```

当前配置直接从进程环境变量读取；本地启动先加载示例默认值，再由忽略提交的 `.env` 覆盖模型密钥和 Provider 专属超时。生产环境应由密钥系统或容器编排注入变量。

API 默认监听 `:8080`：

```bash
curl http://localhost:8080/health/live
curl http://localhost:8080/health/ready
```

## 启动 Worker

```bash
make run-worker
```

## 初始化本地演示账号

```bash
make seed-dev
```

默认租户和账号来自 `.env.example`。认证接口参见 [身份认证 API](api-auth.md)。

## 验证

```bash
make fmt
make lint
make test
make build
make frontend-lint
make frontend-test
make frontend-build
```

集成测试将在本地基础设施任务完成后逐步增加：

```bash
make test-integration
```

## MySQL 与 Oracle 数据源验证环境

```bash
make source-infra-up
make source-infra-status
set -a; . ./.env.example; set +a
make test-source-integration
```

MySQL 8.4 暴露 `3306`，Oracle Free 23.26.2 的 `FREEPDB1` 暴露 `1521`。首次拉取 Oracle ARM64 镜像约 1.12 GB，解压后约 6 GB。示例账号只用于本地开发，生产环境必须由密钥管理系统注入。

数据库连接由独立 Python Connector Service 承担。Oracle 使用 `python-oracledb` Thin 模式，不安装 Oracle Client/Instant Client/OCI；MySQL 使用 PyMySQL。Go 核心服务通过 `CONNECTOR_SERVICE_URL` 和内部令牌访问该服务。

Connector 连接池空闲时间由 `CONNECTOR_POOL_IDLE_TTL_SECONDS` 控制。生产环境必须为 MySQL/Oracle 数据源使用只读账号；应用层 SQL 防护是额外保护，不替代数据库最小权限。
