# 智能问数项目实施 Handoff

> 实施依据：[ASK_DATA_CODEX_TODO.md](./ASK_DATA_CODEX_TODO.md)
> 架构依据：[ASK_DATA_TECHNICAL_DESIGN.md](./ASK_DATA_TECHNICAL_DESIGN.md)
> 页面门禁：任何 `WEB-*` 编码开始前，必须先提交页面设计稿并取得用户确认。

## 1. 当前状态

- 当前 Wave：Wave 0 已全部完成；Wave 1 注册表主链已完成；Wave 2A LLM
  认知协议已完成；Wave 2B 已完成画像合同、有界成员扫描 Worker 和检索主链，
  下一任务是把规范化/异常判断合同接入真实 generation（`DIM-003`）。
- 已完成：`CONTRACT-001`～`CONTRACT-004`、`BASE-001`、`BASE-002`、
  `DB-001`～`DB-003`、`REG-001`～`REG-004`、`AI-001`～`AI-004`、
  `DIM-001`～`DIM-002`、`SEARCH-001`～`SEARCH-003`。
- 部分完成：`DB-004` 的 release manifest、四投影、lease、READY 收敛、
  `release_state` 和 GraphPlan cache 已完成；ACTIVE 激活函数必须等待 `DB-007`
  评测门禁和 `DB-008` 双人审批，当前故意不存在。
- 当前已有数仓盘点结果：本地控制库没有“当前 PUBLISHED + ACTIVE”的 DWS/ADS，
  因此正式导入结果为空；使用回滚事务中的历史 DWS 合成发布夹具已验证真实导入链路。
- 生产准确率状态：尚未评测，不得宣称达到 95%。
- 人工业务输入：`HUMAN-001`～`HUMAN-006` 均尚未提供。
- 2026-08-05 本地运行环境已按用户要求全量重置：控制库、数仓、MinIO、历史
  NebulaGraph/Redis 卷均已删除并重新初始化；随后 seed 已创建 1 个 demo tenant、
  2 个用户，dataset/askdata domain 仍均为 0，没有导入业务语义资产。
- 当前 Compose 运行态健康：API `127.0.0.1:8080`、Web `127.0.0.1:5173`、
  Connector `127.0.0.1:8090`，API/Worker/Connection Test Worker、PostgreSQL、
  Warehouse PostgreSQL 和 MinIO 均为 healthy。

## 2. 已落地产物

### 2.1 公共合同

- `internal/askdata/doc.go`：包依赖方向和根包边界。
- `internal/askdata/contracts.go`：稳定 ID、版本引用、发布引用、证据引用、权限范围和校准置信证据。
- `internal/askdata/strictjson.go`：拒绝未知字段、重复键、尾随 JSON 和非法 UTF-8 的统一解码入口。

关键决策：

- 内容哈希统一为 64 位小写 SHA-256。
- `PolicyScope` 采用规范排序后计算的不可变 hash，可进入缓存键和审计。
- LLM 输出只能引用 Evidence ID/hash，不保存隐藏思维链。

### 2.2 QuestionUnderstanding

- Go 合同：`internal/askdata/understanding/model.go`。
- JSON Schema：`api/schemas/question-understanding-v1.schema.json`。
- 支持：domain hypothesis、指标/维度/成员 mention、Unicode span、时间、比较、排序、limit 和 unresolved span。
- 明确禁止：物理表名、列名、SQL 和临时语义对象定义。

### 2.3 Semantic IR

- Go 合同与规范化：`internal/askdata/ir/model.go`、`normalize.go`。
- JSON Schema：`api/schemas/semantic-ir-v1.schema.json`。
- IR 只包含发布版本 ID、成员版本 ID、受限操作符、日期范围、排序和 limit。
- 指标、分组、过滤成员采用确定性排序；相同语义输入生成稳定 JSON/hash。

### 2.4 Cognition Action 与 Tool Host

- Action 合同：`internal/askdata/cognition/model.go`。
- Tool 合同：`internal/askdata/toolhost/model.go`。
- JSON Schema：`api/schemas/cognition-action-v1.schema.json`。
- 已冻结 9 个认知阶段、8 个动作和 14 个工具名称。
- Tool 参数使用关闭的字段词汇表；未知工具、跨工具夹带参数、阶段不匹配和多个动作载荷全部失败关闭。
- 外层 Action Schema 校验后，Tool Host 仍强制执行第二次参数校验。

### 2.5 合成 fixture

- `internal/askdata/testfixture/fixture.go`。
- 包含合成租户、用户、DWS/ADS 模型、指标、维度、成员、关系、问题、IR 和结果。
- 已覆盖：同名指标、同名成员、越权、Join fanout、空结果、过期成员。
- fixture 必须带 `Synthetic=true` 才能通过校验。

### 2.6 只读资产盘点

- 命令：`cmd/askdata-inventory`。
- 服务与 PostgreSQL 适配：`internal/askdata/registry/inventory.go`。
- 只读取当前 ACTIVE 且 PUBLISHED 的 DWS/ADS dataset/materialization。
- 使用 PostgreSQL `READ ONLY + REPEATABLE READ` 事务，不连接 Warehouse 数据库。
- 默认隐藏 published schema/view，只输出不可逆物理引用 hash；显式参数才可展示物理标识。
- 从已发布 DSL 确定性提取字段、粒度和时间字段，并复核 DSL hash 与 materialization hash。

运行示例：

```sh
DATABASE_URL='postgres://...' go run ./cmd/askdata-inventory \
  --tenant-id '<tenant-uuid>'
```

### 2.7 `askdata` 数据库控制面

- 迁移：`000213`～`000216` 为 askdata 主体，`000221` 清理退役语义运行时遗留的
  tenant 初始化触发器，`000222` 增加有界画像运行时；均包含 `.up.sql` 与
  `.down.sql`，`000217`～`000220` 仍按 TODO 为后续问数运行时和评测门禁预留。
- `askdata` 与已退役的 `platform.semantic_*` 完全隔离；没有恢复历史运行时表。
- 29 张控制面/投影/画像表全部启用并强制 RLS；所有数据库外键均包含 `tenant_id`。
- 认证版本及其 metric-measure、hierarchy-level 子链接不可原地修改。
- 通用质量目标、别名目标、成员父子关系由触发器验证同 tenant/domain 和认证状态。
- 维度成员策略固定为 `FULL/EXACT_ONLY/ON_DEMAND/NONE`：高基数只能
  `ON_DEMAND/NONE`，敏感维度只能 `EXACT_ONLY/NONE`。
- 向量列为 `halfvec(2560)` + HNSW；敏感/高基数/非 FULL 成员无法进入 embedding outbox。
- release 只有四个投影 hash 全部一致才进入 READY；GraphPlan cache 必须绑定 READY
  的 NebulaGraph 投影和相同 release hash。
- 语义模型进入 release 时会再次验证其 DWS/ADS 仍是 current PUBLISHED + ACTIVE，
  不是只在导入时检查一次。

运行角色权限由 `scripts/migrate.sh` 每次部署先清空再显式授予：

- `report_app`：可编辑 registry DRAFT、写检索文档、创建密封 release；不能写
  embedding/projection worker 状态，也不能直接更新 release 生命周期。
- `report_worker`：可处理 embedding outbox、投影 artifact、GraphPlan cache 和有界
  画像 job；profile/member observation 只能追加写，不能更新，且不能写权威语义对象。
- `report_connection_tester` 与 PUBLIC：无 `askdata` schema 权限。

### 2.8 Go 语义注册表与发布包

- `model.go` / `validation.go`：Entity、SemanticModel、Measure、Metric、Dimension、
  Hierarchy、Relationship、QualityRule、BusinessTerm 领域合同。
- Validator 使用稳定 `code + path`；确定性校验可加性、依赖、维度策略、关系基数、
  fanout、质量状态和 AST 安全边界，AST 禁止 raw SQL/nGQL/query/secret 字段。
- `postgres_store.go`：所有操作均使用显式 tenant transaction；metric 更新使用
  record version 乐观锁；列表使用 `(updated_at,id)` keyset cursor；跨领域查询表现为 Not Found。
- `canonical.go` / `release.go` / `object_contract.go`：JSON 键排序、数字规范化、
  精确对象版本排序、稳定 content/release hash 和幂等 release ID。
- `importer.go` / `importer_store.go`：只读 current published + active DWS/ADS；复用
  Dataset DSL 稳定 field ID；确定性生成 model/measure/dimension DRAFT，绝不自动认证。

### 2.9 AI 环境参数处理

- 用户提供的 DeepSeek、GLM、Embedding 变量名和非密钥参数已确认；`.env.example`
  已同步长耗时配置：API write 650s、request 300s、attempt 270s、failover 75s、2 次尝试。
- API key 没有写入仓库、测试输出或本 handoff；部署时通过本地 `.env`/Secret Manager 注入。
- 密钥曾出现在会话正文中，建议在正式环境启用前轮换。
- 2026-08-05 已将三组 key 仅注入 Git 忽略的本地 `.env`，并通过非回显检查确认
  API 与 Worker 均能读取；仓库受版本控制文件和本文仍不包含 key。

### 2.10 LLM 认知执行协议

- `internal/ai` 新增 `SEMANTIC_QUESTION` Purpose；数据库策略白名单支持该值，
  但迁移不为任何租户自动授权，默认失败关闭。显式授权后复用统一配额、模型选择和摘要审计。
- `internal/askdata/cognition/executor.go`：每次只执行一轮受审计模型调用；空动作、
  未知动作、阶段错配、`length` 截断、重复 action hash 和重复 tool_call ID 全部失败关闭。
- assistant 决策与 typed tool result 可进入下一轮消息；Tool Message 必须带 call ID/name，
  且先通过 `toolhost.Response.Validate`。Provider reasoning content 不进入执行结果或审计合同。
- `prompts.go` / `schemas.go`：九个阶段分别限制可见 FactKind 和动作枚举；事实 payload
  规范化并绑定 evidence hash，SQL/nGQL/凭证键禁止进入；元数据中的指令被明确视为不可信数据。
- DeepSeek/GLM fixture 覆盖 `json_schema`、`json_object`、thinking、refusal、length、
  preferred-model 路由和 tool no-progress，不依赖真实 API key。
- Provider 的 `MaxOutputTokens` 配置被定义为硬上限，不再覆盖调用已预留的更小 token 预算。

### 2.11 维度画像、成员规范化与索引策略

- `internal/askdata/dimension/model.go` / `policy.go`：记录基数、NULL、保留值 hash/计数、
  成员变化率、敏感性、扫描预算和实际使用量；画像与决策都有可复核 content hash。
- 确定性决策输出 `FULL/EXACT_ONLY/ON_DEMAND/NONE`：低基数稳定且非敏感才可 FULL；
  高基数走 ON_DEMAND；敏感+高基数或 RESTRICTED 走 NONE；不完整扫描不会误判为 FULL。
- `normalize.go`：仅做 NFKC、空白和大小写等无损规范化；canonical value 与 aliases 分开，
  成员 key 为 `dimension_version_id + normalized_value`，避免跨维度同值误绑。
- UNKNOWN/NULL/N/A/测试等哨兵值不会成为成员候选，只记录 catalog version、hash 和计数。
- `cognition.go`：LLM 只能基于稳定 member ID/evidence 提出聚类、别名、层级或哨兵异常；
  LLM proposal、敏感成员及中高风险合并都不能自动应用。合同已存在，待 `DIM-003`
  把它接到真实 generation 观测证据并补齐流程测试后勾选。
- `worker.go` / `postgres_store.go`：只同步当前 PUBLISHED + ACTIVE DWS/ADS 的精确
  materialization snapshot；任务以 input hash 和递增 generation 幂等，具备租约、
  `FOR UPDATE SKIP LOCKED`、最多 3 次指数退避、配置/源版本过期检测。
- Warehouse 扫描限定 `warehouse_published`，使用 `READ ONLY + REPEATABLE READ`、
  `statement_timeout`、最大行数/成员数/样本字节，并通过服务端引用安全转义列名。
  RESTRICTED 或 `NONE` 维度直接记为 SKIPPED，不读取任何业务行。
- `dimension_profile_jobs`、`dimension_profiles`、`dimension_profile_members` 保存可审计的
  扫描预算、实际用量、profile/policy hash 和规范化成员观测；这些是候选证据，绝不因
  Worker 运行而自动升级为 `dimension_members` 认证资产。

### 2.12 分类文档、Embedding Worker 与混合检索

- `internal/askdata/search/document.go`：METRIC、DIMENSION、MEMBER、BUSINESS_TERM、
  CERTIFIED_EXAMPLE 各自使用独立模板/version/hash。MEMBER 文档同时写入维度名称、
  维度描述、canonical value 和 aliases，解决无维度上下文的同值竞争。
- 凭证形态、物理 SQL/nGQL、RESTRICTED 对象、敏感/高基数/非 FULL member 无法进入向量文档。
- `worker.go` / `postgres_store.go`：batch、租约 token、指数退避、input hash + model 幂等；
  文档或 lease 改变后旧结果更新 0 行并回滚，2,560 维不匹配失败关闭。
- `cmd/worker` 已启动 AskData embedding loop，复用既有 embedding Provider；Provider 未配置时安全空转。
- `retriever.go` / `retrieval_postgres.go` / `rank.go`：exact、pg_trgm/全文 lexical、
  halfvec cosine vector 三路召回；SQL 内先固定 tenant、USER RLS、domain、release hash、
  READY SEARCH_INDEX 投影和对象类型，再按类型 RRF。向量失败明确降级 exact+lexical。

### 2.13 本地启动阻塞修复

- `internal/dataset/mapped_dataset.go` 的系统映射刷新幂等键已加入数据集版本和草稿
  record version。同一事务重试仍得到稳定 key，但语义结构回退到历史 hash 时不会再与
  旧发布记录撞键。
- `internal/dataset/mapped_dataset_test.go` 已覆盖同事务稳定、历史结构再次发布必须隔离，
  以及数据库 128 字符 key 上限。

## 3. 验证记录

在仓库根目录执行：

```sh
gofmt -w $(rg --files internal/askdata -g '*.go')
go test ./...
ENV_FILE=.env.example ./scripts/check-compose.sh
./scripts/ci-check.sh
```

结果：全部通过。

2026-08-05 全新环境启动验证：

```sh
go test ./...
./scripts/dev-services.sh start
./scripts/dev-services.sh status
```

- 217 个迁移版本已执行完成，范围为 `000001`～`000222`；`000192` 不存在，
  `000217`～`000220` 按 TODO 保留给后续问数运行时和评测迁移。
- API live/ready、Connector live、Web 首页均返回 HTTP 200，四个应用容器均 healthy。
- `000221` 已移除 `000195` 遗留、仍引用已删除表的 tenant trigger；`make seed-dev`
  已在空库成功创建 demo tenant 与管理员。
- 映射数据集刷新幂等键已加入数据集版本和草稿修订版本；相同 transition 可重放，
  回退到历史 schema 时不会与旧发布撞键。
- `verify-database.sh` 改用事务内自建跨领域夹具，不再依赖预置 membership；
  `verify-warehouse.sh` 已移除退役画像运行时的旧函数断言，两者均通过。

数据库验证还执行了：

```sh
ENV_FILE=.env.example ./scripts/migrate.sh
ENV_FILE=.env.example ./scripts/verify-database.sh
ENV_FILE=.env.example ./scripts/verify-warehouse.sh
```

- `000213`～`000216` 完成一次完整空 schema down→up 往返；`000221`、`000222`
  已在现有库完成向前迁移；画像三表的强制 RLS、tenant 复合外键和 app/worker
  最小权限均由数据库验收脚本断言。
- PostgreSQL 实测角色权限、跨领域 RLS、全部 tenant 复合外键、HNSW、四投影函数和
  “激活函数不存在”安全断言。
- Registry integration test 实测 app role tenant transaction、乐观锁、稳定分页、
  跨领域不可见。
- Search integration test 实测 worker 角色 embedding claim SQL，以及 app 角色下
  exact/lexical/vector 三类查询的 USER RLS、release pin 和空 release 失败关闭路径。
- Dimension integration test 实测 worker 同步 SQL 的数值参数类型，以及 Warehouse
  reader 的只读有界扫描、NULL/基数统计、成员截断和 snapshot 行数漂移检测。
- 回滚事务中的真实 DWS fixture 实测：DRAFT 导入、认证依赖、release object、四投影
  claim/complete→READY、GraphPlan cache，以及源 DWS 失效后 release 失败关闭。

已验证的负向边界：

- 空 ID、非法 hash、篡改 PolicyScope hash；
- JSON 未知字段、重复键、尾随值；
- 中文 Unicode span 与原问句不一致；
- Semantic IR 夹带物理字段、未绑定成员和无效排序引用；
- 未知 Tool、跨 Tool 参数、未执行二次校验；
- Action 阶段不匹配、多个 payload 和夹带 SQL 字段。
- Semantic Question 默认未授权、Tool Message 顺序/identity、截断、拒答、模型切换、
  reasoning content 丢弃、重复 action/tool no-progress；
- 画像预算/计数矛盾、profile/decision hash 篡改、敏感/高基数索引策略；
- canonical/alias 混写、跨维度同值 key、UNKNOWN/NULL/测试值、LLM 高风险自动合并；
- MEMBER 文档缺少维度上下文、文档凭证/物理查询、Embedding 维数错误、旧租约覆盖；
- Hybrid Retriever 无权限 scope、错误 release、水位过滤和向量降级。
- AVG/COUNT_DISTINCT 可加性错误、MANY_TO_MANY SAFE fanout、敏感/高基数成员策略；
- canonical JSON 重复键、对象顺序变化、contract hash 篡改、release 对象重复；
- repository stale record version、空/篡改 cursor、跨领域读取；
- 非数值 measure 导入、跨 domain asset 过滤、重复导入稳定 ID/hash。

## 4. 工作区注意事项

- 用户在本次实施前已有多项 `docs/*` 删除状态；这些文件未被恢复或修改。
- `ASK_DATA_TECHNICAL_DESIGN.md`、`ASK_DATA_CODEX_TODO.md` 为当前项目设计和执行事实源。
- 不要恢复历史 `platform.semantic_*` 运行时；新控制面使用 `askdata` schema。
- 未经用户页面设计确认，不得开始 `WEB-001`～`WEB-007` 的 React 编码。
- 本地开发验证使用 `.env.example`；不要把用户给出的 API key 补写到 `.env.example`。
- 当前机器存在 Git 忽略的 `.env` 以供运行；交接、日志和提交时不得回显其内容。
- 数据库 integration test 使用 `ASKDATA_INTEGRATION_DATABASE_URL`、
  `ASKDATA_INTEGRATION_ADMIN_DATABASE_URL` 和 `ASKDATA_INTEGRATION_WORKER_DATABASE_URL`
  显式开启；画像扫描另使用 `ASKDATA_INTEGRATION_WAREHOUSE_DATABASE_URL` 和
  `ASKDATA_INTEGRATION_WAREHOUSE_ADMIN_DATABASE_URL`，默认单元测试会跳过外部数据库。

## 5. 下一步

1. `DIM-003`：把已实现的 normalize/anomaly 合同接入 `DIM-002` 的 profile generation，
   形成可复核的 alias/层级/哨兵异常候选；高风险和敏感成员仍禁止自动合并。
2. `SEARCH-004`：让 LLM 只在 SQL/RLS/图约束后的候选集合内重排；deterministic block 不可覆盖。
3. `REG-005` 仍需要 `HUMAN-001`～`HUMAN-003` 才能形成首批业务语义资产；当前不能生成正式认证资产。
4. `GRAPH-001` 先做 NebulaGraph 服务端/Go Client 兼容 POC，通过后才修改 Compose。
5. `DB-004` 最终勾选必须等待 `DB-007` + `DB-008`，不得提前创建 activate 函数。
6. 到达任何 `WEB-*` 前，先制作并提交问数工作台与语义管理页面设计稿供用户确认。

## 6. 接手验证命令

```sh
git status --short
go test ./internal/askdata/...
ASKDATA_INTEGRATION_DATABASE_URL='postgres://report_app:...@127.0.0.1:5432/...' \
ASKDATA_INTEGRATION_ADMIN_DATABASE_URL='postgres://report_admin:...@127.0.0.1:5432/...' \
ASKDATA_INTEGRATION_WORKER_DATABASE_URL='postgres://report_worker:...@127.0.0.1:5432/...' \
  go test ./internal/askdata/... -count=1
ENV_FILE=.env.example ./scripts/verify-database.sh
ENV_FILE=.env.example ./scripts/verify-warehouse.sh
go test ./...
npm --prefix web run lint
npm --prefix web run build
```
