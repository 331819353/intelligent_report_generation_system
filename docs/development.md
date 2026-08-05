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
| PostgreSQL（控制面） | `127.0.0.1:5432` | 权限、数据源、数据集、版本和审计 |
| PostgreSQL（数据面） | `127.0.0.1:5433` | DIM、DWD、DWS、ADS 物理表和发布视图；ODS 明细不复制入仓 |
| MinIO API | `127.0.0.1:9000` | Excel 上传和数据快照 |
| MinIO Console | `http://127.0.0.1:9001` | 本地对象存储管理界面 |

控制面 PostgreSQL 使用四个不同账号：`report_admin` 仅用于迁移；`report_app` 是 API
控制面账号；`report_worker` 是通用后台任务账号；`report_connection_tester` 只领取
并完成连接测试任务。数据面 PostgreSQL 另设 `warehouse_admin`、
`report_warehouse_reader` 和 `report_warehouse_worker`，其数据卷、端口和凭据均与
控制面隔离。三个运行进程必须分别
使用 `DATABASE_URL`、`WORKER_DATABASE_URL` 和 `CONNECTION_TEST_DATABASE_URL`，
不能复用角色。API 与通用 worker 均不能伪造连接测试证明，专用 tester 也不能自行
入队或直接写证明表。

生产编排必须从进程启动时就只向每个容器注入自己的一个运行 DSN，不能把三个 DSN
先全部注入再依赖应用删除环境变量。配置加载器会移除不属于当前进程的数据库密码和
DSN，这只是纵深防御。三个运行角色还必须全部不同于迁移管理员角色；即使管理员属性
被收窄，对表和函数的 ownership 也会扩大权限。迁移脚本会拒绝角色重名、继承关系、
角色成员关系以及
`SUPERUSER`、`CREATEDB`、`CREATEROLE`、`REPLICATION` 或 `BYPASSRLS` 等高权限；
不会静默修改已有生产角色。

安装 Docker Desktop 或兼容的 Docker Engine/Compose 后执行：

```bash
make infra-config
make infra-up
make infra-status
make db-migrate
make db-verify
```

`infra-up` 会等待两个 PostgreSQL 和 MinIO 健康，并自动创建：

- `uploads`：Excel 和 CSV 源文件；
- `snapshots`：数据集快照。

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

`make infra-reset` 会删除本地 PostgreSQL 和 MinIO 数据卷，仅应用于开发环境。仓库中的 `.env.example` 使用公开的本地开发默认值；生产环境必须通过密钥系统注入独立强凭证。

如果当前机器暂未安装 Docker，可先执行不依赖 Docker 的静态检查：

```bash
./scripts/check-compose.sh
sh -n scripts/migrate.sh
```

## 模型配置

模型配置只能通过运行环境注入。`AI_API_KEY` 不得写入仓库：

```bash
export AI_BASE_URL="https://mgallery.haier.net/v1/"
export AI_MODEL="MiniMax-M2"
export AI_MODELS="MiniMax-M2"
export AI_API_KEY="<MiniMax 网关密钥>"
export AI_PROVIDER_SELECTION_MODE="round_robin"
export AI_RESPONSE_FORMAT="prompt"
export AI_DEEPSEEK_BASE_URL="https://api.deepseek.com"
export AI_DEEPSEEK_MODELS="deepseek-v4-flash"
export AI_DEEPSEEK_API_KEY="<DeepSeek 密钥>"
export AI_DEEPSEEK_THINKING_ENABLED="true"
export AI_DEEPSEEK_REASONING_EFFORT="high"
export AI_DEEPSEEK_RESPONSE_FORMAT="json_object"
export AI_GLM_BASE_URL="https://open.bigmodel.cn/api/paas/v4"
export AI_GLM_MODELS="glm-5.2"
export AI_GLM_API_KEY="<GLM 密钥>"
export AI_GLM_THINKING_ENABLED="true"
export AI_GLM_RESPONSE_FORMAT="json_object"
export AI_GLM_MAX_OUTPUT_TOKENS="65536"
export API_WRITE_TIMEOUT="240s"
export AI_REQUEST_TIMEOUT="100s"
export AI_ATTEMPT_TIMEOUT="90s"
export AI_PRIMARY_FAILOVER_TIMEOUT="60s"
export AI_MAX_ATTEMPTS="1"
export AI_CONFIDENCE_THRESHOLD="0.8"
```

当前本地模型池由 `MiniMax-M2`、`deepseek-v4-flash` 和 `glm-5.2` 组成，未指定模型的调用按等权轮询顺序分发；同一审计请求内的网络重试固定在已选模型，避免重试改变模型语义。需要领域级失败转移时，从当前模型开始按环形顺序选择下一个模型，并创建新的独立审计请求。

MiniMax 官方目前只为 `MiniMax-Text-01` 声明 `response_format` 支持，`MiniMax-M2` 使用 `prompt` 模式：不发送该参数，而是把规范化的 JSON Schema 作为系统输出合同并执行本地严格校验。DeepSeek 和 GLM 使用 `json_object` 加相同的本地校验。GLM 的 65536 Token 上限是端点级覆盖，用于避免思考过程耗尽短任务的输出预算。认证、租户策略、配额、取消、非法请求、拒答和响应过大不会换模型重试。未配置任何模型密钥时，元数据补全和数据集 DAG 提案接口明确降级为不可用，非 AI 功能继续运行。

## 一键启动本地服务

推荐使用持久化开发服务入口。它会启动并等待双 PostgreSQL、MinIO、
Connector、API、通用 Worker、连接测试 Worker 和 Web，自动执行数据库迁移及应用
镜像构建。所有服务均由 Docker Compose 托管并使用 `restart: unless-stopped`，不依赖
当前终端会话存活：

```bash
make dev-up
make dev-status
```

访问地址：

- Web：`http://127.0.0.1:5173`
- API：`http://127.0.0.1:8080`
- Connector：`http://127.0.0.1:8090`

维护命令：

```bash
make dev-restart
make dev-logs
make dev-stop
```

`dev-stop` 只停止 API、两个 Worker 和 Web，不删除数据，也不停止基础设施容器；
需要停止全部容器时另行执行 `make infra-down`。重复执行 `make dev-up` 是幂等的，
Compose 会复用健康容器，并只重建发生变化的应用镜像。

## 单独启动 API

```bash
make run-api
```

单独调试时可使用前台命令。当前配置直接从进程环境变量读取；本地 `run-api`、
`run-worker`、`dev-up` 和 `seed-dev` 目标先加载示例默认值，再由忽略提交的 `.env`
覆盖模型密钥和 Provider 专属超时。生产环境不使用这些本地目标，应由密钥系统或
容器编排注入变量。

API 默认监听 `:8080`：

```bash
curl http://localhost:8080/health/live
curl http://localhost:8080/health/ready
```

## 单独启动 Worker

```bash
make run-worker
```

连接测试使用独立进程和数据库身份：

```bash
make run-connection-test-worker
```

本地默认单次测试超时为 `CONNECTION_TEST_TIMEOUT=30s`，租约为
`CONNECTION_TEST_LEASE=60s`。租约必须至少比超时长 10 秒。任务状态和安全错误文案
保存在 PostgreSQL；连接凭据、租约 token 和驱动原始错误不会进入 API 响应。

生产环境还必须隔离连接测试进程的外部资源：

- `CONNECTOR_CONNECTION_TEST_TOKEN` 必须与 `CONNECTOR_INTERNAL_TOKEN` 不同；
  前者只能调用 `/v1/connections/test`，元数据和查询接口只接受后者。
- `CONNECTOR_SERVICE_URL` 使用 HTTPS；只有同一主机的 loopback HTTP 代理可例外。
- Excel 测试只注入 `CONNECTION_TEST_MINIO_*`，必须启用 TLS，并使用独立的
  MinIO 身份。该身份只授予 uploads 桶所需对象前缀的 `GetObject`，不授予
  `ListBucket`、`PutObject`、`DeleteObject` 或 snapshots 桶权限。
- API、通用 worker 不注入连接测试 token 或其 MinIO 密钥；连接测试进程不注入
  通用 Connector token、通用 MinIO 密钥、API DSN 或通用 worker DSN。

连接测试进程目前仍需读取 `DATA_SOURCE_CREDENTIAL_KEY`，用于解密数据库已经冻结并
通过 claim 函数交付的精确配置密文。因此专用 tester 是一个明确的可信边界：数据库
权限、专用 Connector token 和只读 MinIO 凭据可以缩小影响面，但不是硬件或密码学
级密钥隔离。进一步收紧时应由独立凭据代理/KMS 在短时授权下完成解密和连接。

## 初始化本地演示账号

```bash
make seed-dev
```

默认租户和账号来自 `.env.example`。这个仅限开发环境的 Seed 会显式启用租户 AI，并合并 `METADATA_COMPLETION` 和 `DATASET_DAG_GENERATION` 用途。生产新租户默认禁用 AI，模型密钥不等于启用 AI，仍必须由受信管理流程配置总开关与配额。认证接口参见 [身份认证 API](api-auth.md)。

## 质量检查与构建

```bash
make fmt
make lint
make build
make frontend-lint
make frontend-build
make db-verify
make warehouse-verify
```

## 外部数据库连接

项目不再内置 MySQL 或 Oracle 测试镜像、样例账号和样例数据。用户配置的外部数据库
连接仍由独立 Python Connector Service 承担；Oracle 使用 `python-oracledb` Thin
模式，MySQL 使用 PyMySQL。Go 核心服务通过 `CONNECTOR_SERVICE_URL` 和内部令牌访问
该服务。

Connector 连接池空闲时间由 `CONNECTOR_POOL_IDLE_TTL_SECONDS` 控制。
`CONNECTOR_MAX_POOLS` 限制进程内池注册表数量，
`CONNECTOR_MAX_TOTAL_CONNECTIONS` 限制全部池化连接和 one-shot 连接合计的物理
socket 数。池数量达到上限时只按 LRU 淘汰没有活动连接或等待引用的完全空闲池；
物理连接达到上限时也只回收 LRU 空闲连接，绝不关闭活动查询，无法回收时等待
`connectTimeout` 后失败。连接测试使用受同一全局物理连接上限保护的 one-shot
连接，成功或失败后都关闭，不进入普通池。生产环境必须为 MySQL/Oracle 数据源使用
只读账号；应用层 SQL 防护是额外保护，不替代数据库最小权限。
生产 Connector 启动时要求同时配置不同的 `CONNECTOR_INTERNAL_TOKEN` 和
`CONNECTOR_CONNECTION_TEST_TOKEN`；缺少通用 token、沿用开发默认值或两个 token
相同都会启动失败。对外暴露时还应通过网络策略限制来源；Compose 的开发端口默认只
绑定 `127.0.0.1`。

本地 Compose 通过 `CONNECTOR_EGRESS_ALLOWLIST=mysql:3306,oracle:1521` 允许两个
开发服务名。production 会拒绝 hostname-only 规则，必须显式配置批准的
`IP/CIDR:port`，并用非空 `CONNECTOR_EGRESS_DENYLIST` 覆盖平台控制面 CIDR。
全部 DNS 结果都通过 allow/deny 后，驱动才会 pin 到其中一个已验证 IP；网络层仍须
默认拒绝出站并只开放相同数据库 CIDR/port，Oracle listener/SCAN redirect 也必须由
网络层负测约束。

以下资源变量在 production 必须逐项显式注入；括号内是开发默认值：

- `CONNECTOR_MAX_POOLS`（1,000 个池）、
  `CONNECTOR_MAX_TOTAL_CONNECTIONS`（100 条物理连接）；
- `CONNECTOR_HTTP_MAX_REQUEST_BYTES`（1 MiB）、
  `CONNECTOR_JSON_MAX_RESPONSE_BYTES`（64 MiB）、
  `CONNECTOR_METADATA_SYNC_MAX_ROWS`（200,000）；
- `CONNECTOR_METADATA_SAMPLE_MAX_CELL_BYTES`（16 KiB）、
  `CONNECTOR_METADATA_SAMPLE_MAX_ROW_BYTES`（64 KiB）、
  `CONNECTOR_METADATA_SAMPLE_MAX_RESPONSE_BYTES`（512 KiB）；
- `CONNECTOR_STREAM_MAX_CELL_BYTES`（1 MiB）、
  `CONNECTOR_STREAM_MAX_ROW_BYTES`（4 MiB）、
  `CONNECTOR_STREAM_MAX_BYTES`（1 GiB）；
- 通用 worker 的 `WAREHOUSE_STAGE_MAX_BYTES`（1 GiB，每租户每物化任务，同时
  覆盖数据库和 Excel/CSV 的逻辑 staging）。

文件对象读取仍受租户 `max_excel_file_bytes` 限制。ODS/DWD 设计预览只解析所需
worksheet 的表头和前 100 行。DWD/DIM 正式回源通过 Excelize 行迭代
和 1,000 行批次 COPY，不在内存中构造完整二维表；worksheet XML 超过 16 MiB 时
落到临时文件。展开总量同时受“压缩文件大小的 8 倍”和 2 GiB 绝对上限约束，
逻辑投影行则独立累计到 `WAREHOUSE_STAGE_MAX_BYTES`。调高这些边界前应同时评估
worker 内存、临时空间、仓储容量和单任务隔离。

MySQL 普通查询、元数据同步和 DWD/DIM 正式回源流都使用 `SSCursor`。预算终止、取消和 HTTP
客户端断流会先关闭 socket，再关闭游标，避免游标清理继续排空未读大结果；成功完整
消费才允许连接回池。单个源字段仍必须由数据库驱动先物化后才能执行 cell 上限检查，
因此生产还应限制 Connector 容器内存、源账号可见字段和数据库最大包/LOB 策略；不得
把应用层 cell 预算描述为源端单值峰值内存的绝对上限。
