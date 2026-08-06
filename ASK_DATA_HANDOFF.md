# 智能问数项目实施 Handoff

> 实施依据：[ASK_DATA_CODEX_TODO.md](./ASK_DATA_CODEX_TODO.md)
> 架构依据：[ASK_DATA_TECHNICAL_DESIGN.md](./ASK_DATA_TECHNICAL_DESIGN.md)
> 页面门禁：任何新增页面、流程或显著视觉状态的 `WEB-*` 编码开始前，必须先提交页面设计稿并
> 取得用户确认；纯 API Client、类型和测试接线不触发页面门禁，但仍必须满足 TODO 依赖。

## 1. 当前状态

- 当前 Wave：Wave 0 已全部完成；Wave 1 注册表主链已完成；Wave 2A LLM
  认知协议已完成；Wave 2B 已完成画像合同、有界成员扫描 Worker、generation
  规范化/异常候选接线，以及分类文档、Embedding、混合检索和受约束 LLM 重排主链。
  Wave 2C 已完成 NebulaGraph 服务端/Go Client 兼容 POC、正式开发 Compose/初始化和
  GraphPlan 合同/Adapter；`SEARCH-005` 仍依赖未提供的人工黄金集，下一条 `GRAPH-004`
  因纸面依赖仍包含未勾选的 `DB-004` 而暂时阻塞。Wave 3A 已完成确定性问句规范化、原文
  span 回映、时间/比较/
  查询语法解析、安全会话上下文合并，以及受控 LLM 完整理解与取证计划。`NLU-005`
  仍依赖尚未完成的 `GRAPH-005`。Wave 4B 已提前完成无外部依赖的 `EVAL-001` 结果
  规范化与等价判定，以及 `EVAL-002` mention/binding 指标和校准训练/验证输入合同；
  `NLU-006` 的评测依赖现已满足，但任务本身仍等待 `NLU-005`。Wave 3C 的 `DB-005` 问数
  运行/审计控制面、`ORCH-002` Question 状态机/PostgreSQL Store，以及 Wave 4B 的
  `DB-006` 评测集、双人评审、追加式评测运行与结构化反馈迁移均已完成。Wave 4C 的
  `SEC-003` 敏感成员敏感度下限、数据库内 EXACT_ONLY、label-free evidence/LLM 遮罩和
  不披露授权边界也已完成；`ORCH-003` 仍需
  等待 `ORCH-001`，而后者尚受 GRAPH/QUERY 依赖约束。`DB-004` 中运行时依赖的 release/
  READY 基础已完成，ACTIVE 激活入口继续按门禁保持关闭；当前剩余任务的依赖/人工输入阻塞
  见“下一步”。用户已确认 `WEB-001` 方案 3「证据驾驶舱」，受保护 `/ask-data` React
  typed mock、ECharts 图表、关键交互和设计 QA 均已完成；`WEB-002` 仍等待 `ORCH-005`，
  尚未接入真实 Question API/SSE。
- 已完成：`CONTRACT-001`～`CONTRACT-004`、`BASE-001`、`BASE-002`、
  `DB-001`～`DB-003`、`REG-001`～`REG-004`、`AI-001`～`AI-004`、
  `DIM-001`～`DIM-003`、`SEARCH-001`～`SEARCH-004`、`GRAPH-001`～`GRAPH-003`、
  `NLU-001`～`NLU-004`、`DB-005`、`ORCH-002`、`DB-006`、`EVAL-001`、`EVAL-002`、
  `SEC-003`、`WEB-001`。
- 部分完成：`DB-004` 的 release manifest、四投影、lease、READY 收敛、
  `release_state` 和 GraphPlan cache 已完成；ACTIVE 激活函数必须等待 `DB-007`
  评测门禁和 `DB-008` 双人审批，当前故意不存在。
- 当前已有数仓盘点结果：本地控制库没有“当前 PUBLISHED + ACTIVE”的 DWS/ADS，
  因此正式导入结果为空；使用回滚事务中的历史 DWS 合成发布夹具已验证真实导入链路。
- 生产准确率状态：尚未评测，不得宣称达到 95%。
- 人工业务输入：用户已确认 `HUMAN-005` 的开发部分采用 v3.8.0、单副本共享 Space、持久卷、
  API GUEST/Worker USER 和环境变量开发凭据；生产容量、TLS、备份、多副本及
  `HUMAN-001`～`HUMAN-004`、`HUMAN-006` 仍未提供。
- 2026-08-05 本地运行环境已按用户要求全量重置：控制库、数仓、MinIO、历史
  NebulaGraph/Redis 卷均已删除并重新初始化；随后 seed 已创建 1 个 demo tenant、
  2 个用户，dataset/askdata domain 仍均为 0，没有导入业务语义资产。
- 当前 Compose 运行态健康：API `127.0.0.1:8080`、Web `127.0.0.1:5173`、
  Connector `127.0.0.1:8090`、初始化后 loopback proxy `127.0.0.1:9669`；API/Worker/
  Connection Test Worker、PostgreSQL、Warehouse PostgreSQL、MinIO、Nebula metad/
  storaged/graphd/proxy 均为 healthy。graphd 本身没有宿主端口，init 退出容器已移除。

## 2. 已落地产物

### 2.1 公共合同

- `internal/askdata/doc.go`：包依赖方向和根包边界。
- `internal/askdata/contracts.go`：稳定 ID、版本引用、发布引用、证据引用、权限范围和校准置信证据。
- `internal/askdata/strictjson.go`：拒绝未知字段、重复键、尾随 JSON 和非法 UTF-8 的统一解码入口。

关键决策：

- 内容哈希统一为 64 位小写 SHA-256。
- `PolicyScope` 采用规范排序后计算的不可变 hash，可进入缓存键和审计。
- LLM 输出只能引用 Evidence ID/hash，不保存隐藏思维链；候选重排另使用绑定 release 的
  `CANDIDATE_SET` EvidenceKind，避免把整个候选集与单条检索/图证据混用。

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

- 迁移：`000213`～`000218` 为 askdata 主体，`000221` 清理退役语义运行时遗留的
  tenant 初始化触发器，`000222` 增加有界画像运行时；均包含 `.up.sql` 与
  `.down.sql`，`000219`～`000220` 仍按 TODO 为后续发布门禁与审批预留。
- `askdata` 与已退役的 `platform.semantic_*` 完全隔离；没有恢复历史运行时表。
- 38 张控制面/投影/画像/问数/评测表全部启用并强制 RLS；所有数据库外键均包含 `tenant_id`。
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
  embedding/projection worker 状态，也不能直接更新 release 生命周期；可创建/推进自己
  的问数 run 并追加 event/artifact/tool outcome，但不能删除 run 或改写追加式审计；仅领域/
  平台管理员可管理 DRAFT evaluation set/case/review，原 actor 可创建/乐观更新自己的反馈。
- `report_worker`：可处理 embedding outbox、投影 artifact、GraphPlan cache 和有界
  画像 job；profile/member observation 只能追加写，不能更新，且不能写权威语义对象。
  问数运行与审计当前只读，不能替 API actor 追加或推进状态；SYSTEM 模式可读取密封黄金题，
  但对评测控制面只有 `evaluation_runs` INSERT，不能改写 set/case/review/feedback。
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
- UNKNOWN/NULL/N/A/测试等哨兵值不会成为成员候选，只记录 catalog version、hash 和计数；
  Profile 合同已升至 `1.1`，画像 input version 升至 `dimension-profile-v2`，避免旧 generation
  与新增 catalog/规范化证据合同复用同一幂等输入。
- `cognition.go`：DIM-002 的 Profile + MemberObservation 可确定性生成有界
  `DIMENSION_PROFILE` Prompt Fact；最多选择 64 个高频成员，并记录 omitted count、扫描
  完整性、profile/source hash。候选 ID 由 dimension-bound member key hash 稳定派生，
  不把观测值伪装成已认证 member version。
- 确定性 Unicode/大小写/空白等价只生成低风险别名候选；LLM 可提出别名、聚类、层级或
  哨兵异常，但每个成员、别名和 EvidenceRef 都必须回绑同一 generation。发明成员、跨
  generation/hash 引用、未观测别名、敏感成员和重复 proposal 均失败关闭。
- LLM proposal 永不自动应用；扫描截断或有成员因 Prompt 上限被省略时，确定性候选也保持
  review-only。该流程只返回可复核候选，不写入或认证 `dimension_members`。
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
- `reranker.go`：将最多 30 个受约束候选规范化为 `CANDIDATE_SET` Prompt Fact，绑定
  `PolicyScope`、release、检索 evidence、定义、反例、图兼容结论和 deterministic gate；
  caller 输入顺序不会改变 fact hash，CONFIDENTIAL/RESTRICTED、凭证形态和物理查询文本
  不会进入模型上下文。
- Reviewer 只能按稳定 object version ID 返回集合内可选候选，并且只能引用该候选自己的
  retrieval/graph/gate evidence。发明 ID、跨候选引用、错误 candidate-set hash 或覆盖
  graph/policy block 均失败关闭；全部候选被拦截时不调用模型，直接返回可审计 no-match。

### 2.13 NebulaGraph 服务端/Go Client 兼容 POC

- 已锁定 NebulaGraph metad/storaged/graphd `v3.8.0` 与官方
  `github.com/vesoft-inc/nebula-go/v3 v3.8.0`，均使用 release tag，不依赖 master/nightly。
  `nebula-go/v5` 是面向新 gRPC Nebula Service 的另一客户端，不适用于本设计选定的
  3.x graphd fbthrift 接口。
- `deployments/nebula-poc/compose.yaml` 是完全独立、全部数据使用 tmpfs 的临时验证拓扑：
  单 metad、单 storaged、两个普通 graphd、一个 TLS graphd 和一个 TCP blackhole；没有
  修改仓库根 `compose.yaml`。正式持久卷、账号和服务接线随后已由 2.27 的 `GRAPH-002` 落地。
- POC 确认真实启动顺序必须是 metad → graphd → console `ADD HOSTS` → storaged ready；
  graphd 的 HTTP `/status` 会早于 thrift 查询端口可用，因此 Go 测试还执行有界 readiness
  retry，不能只依赖 HTTP 绿灯。
- `internal/askdata/graph/poc_test.go` 实测 meta/storage/graph 三类服务版本、Connection Pool、
  Space-bound Session Pool、TLS 1.2+、参数绑定、socket timeout、8 路并发和缺失 Space；
  主动停止 `graphd-a` 后同一 Session Pool 经 `graphd-b` 恢复查询，再恢复故障节点。
- 一键命令为 `scripts/verify-nebula-poc.sh`；证书在系统临时目录生成，脚本只控制固定的
  `askdata-nebula-poc` Compose project，默认退出时清理全部临时容器、网络和证书。

### 2.14 GraphPlan 合同与 Nebula Adapter

- `internal/askdata/graph/model.go` 冻结 `PlanRequest`、`GraphPlan`、对象版本引用、Join
  Step/Path 和风险代码。请求必须绑定规范 `PolicyScope`、一个已授权 domain、精确候选版本
  和 `maxJoinHops <= 4`、`maxPaths <= 32`；GraphPlan 的 request/plan/path hash 均规范且
  可复算，并可生成 `GRAPH_PATH` EvidenceRef。
- VID 严格采用技术设计中的
  `tenant_id:object_type:stable_object_id:version`，限制 256 bytes；release 不混入 VID，
  而是在每个 Tag/Edge 上保存并强制匹配 `release_hash`。同 version ID 冲突、不同引用映射
  到同一 VID、注入形态 ID 和超限候选均在访问图前失败关闭。
- `query_builder.go` 只生成四类固定模板：metric `MODELED_BY` model、model
  `HAS_DIMENSION` dimension、dimension `HAS_MEMBER` member，以及
  `JOINS_TO*1..N` 有界路径。所有点边都同时匹配 tenant/domain/release；路径端点必须是
  metric-bound model，中间点只能来自请求中已授权的 model，关系必须为 certified。
- `client.go` 对外只有 typed `Resolve`，不能提交任意查询或切换 Space。返回值只包含稳定
  版本 ID、模型绑定、兼容维度、成员归属/状态、Join 方向/类型/基数/fanout、Allowed 和
  确定性风险代码；Nebula 服务端错误被收敛为稳定类别，不回显生成的 nGQL。
- `poc_test.go` 在隔离的 NebulaGraph `v3.8.0` 中创建四类 Tag、四类 Edge 和版本化合成图，
  通过公开 Adapter 实测模型、维度、成员、Join 路径、风险和 EvidenceRef。实际联调还确认
  HTTP 健康可能早于 `SHOW HOSTS` 的 Version 元数据就绪，版本断言已改为有界重试；
  Nebula `ORDER BY` 只接受返回列名，路径长度和端点排序键因此先显式投影为内部列。

### 2.15 确定性问句规范化与 span 回映

- `internal/askdata/understanding/normalize.go` 新增版本化
  `question-normalization-v1` 合同。`NormalizedQuestion` 同时保留原问句与规范文本，并以
  零基 Unicode rune offset 在规范 span 和原文 span 之间双向映射；组合字符收缩和兼容/
  大小写展开采用覆盖完整原始 normalization segment 的保守映射。
- 文本投影固定为 NFKC → Unicode case fold → NFKC，统一全半角、兼容单位、中英文标点、
  Unicode 空白和安全数字单位间距。数值不会换算，“区/市”等行政后缀不会删除，`C++`、
  `A/B测试`、`华东-1区` 等实体核心字符保持可回到原文。
- 无害语气词处理只作用于句首/句尾明确的礼貌包装；核心文本中的相同词不会全局替换。
  完全被剥离的原文 span 返回 `ErrRemovedOffsetSpan`，无语义内容的问题失败关闭。
- 规范结果 `Validate` 会按原文重新计算，拒绝文本或映射篡改；非法 UTF-8、超过 4,096
  runes、非空白控制符、双向覆盖符和零宽不可见字符在进入后续词典/LLM 前被拒绝。
- `normalize_test.go` 覆盖全半角英文、中文/英文礼貌包装、标点、百分比/重量单位、emoji、
  组合音标、`ß -> ss`、实体后缀、幂等性、双向 offset 和 fuzz 往返不变量。

### 2.16 时间、比较与查询语法解析

- `internal/askdata/understanding/time.go` 以显式参考时间解析今天/昨日、自然周、本月/上月、
  今年/去年和财年；所有计算固定为 `Asia/Shanghai`，自然周从周一开始，结果统一保存为
  `start` 左闭、`endExclusive` 右开的 `YYYY-MM-DD` 日历范围。
- 显式日期支持带年份的日/月/年和同粒度范围。无年份日期、月日顺序不明的数字日期、裸
  “财年/自然周”、非法日期、倒序/跨粒度范围和多个时间表达均产生稳定 unresolved；财年
  起始月必须由调用方配置，缺失时不会按自然年猜测。
- `internal/askdata/understanding/grammar.go` 确定性识别同比、环比、较上期、Top/Bottom N、
  升降序及“按/每个”分组标记。规则事实不提前绑定指标/维度；多比较、多排名、Top 百分比
  和排序方向冲突以稳定 reason 交给后续认知阶段，不静默选边。
- `question-rules-v1` 结果绑定原问句 SHA-256、规范化版本、参考日、固定时区和可选财年配置，
  `Validate` 会重新执行解析以拒绝内容或配置篡改。时间、比较、排名、排序、分组和 unresolved
  的文本/span 全部通过 `NormalizedQuestion` 映回精确原文 Unicode rune 范围。
- `time_test.go`、`grammar_test.go` 覆盖上海跨日、周/月/年/财年边界、半开范围、全半角
  span、日期歧义/非法输入、语法冲突、排名上限、英文变体，以及“按揭/按钮”等误命中；
  fuzz 持续检查任意成功解析结果的全部 span 均与原文完全一致。

### 2.17 安全会话上下文合并

- `internal/askdata/understanding/context.go` 新增 `conversation-snapshot-v1`：只有合法且
  没有 unresolved 的最终 `QuestionUnderstanding` 能进入快照；构造时深拷贝调用方数据，
  并绑定 conversation、turn、完整 `PolicyScope`、release 与可复算 snapshot hash。
- `conversation-merge-v1` 将上一轮事实按 domain、metric、grouping、filter、time、
  comparison、ordering、limit 分槽处理。`CURRENT_TURN_WINS` 是固定优先级；“那按地区呢”
  只替换原分组，“换成去年”只替换原时间，上一轮事实继续保留其源问句和源 span，不会被
  搬到本轮文本上制造虚假位置。
- AUTO 模式只对明确的续问前缀、规则片段和清除指令继承；可信编排层也可显式指定
  FOLLOW_UP/INDEPENDENT。完整新问题、清空上下文和独立模式不会读取旧快照；取消分组、
  筛选、时间、比较、排序或排名等显式清除优先于本轮规则命中。
- 缺少上一轮的追问返回 `CONTEXT_PREVIOUS_TURN_REQUIRED` 和精确本轮原文 span。旧快照的
  tenant、actor、domain、role、policy hash、release 或 conversation 任一不一致时，结果
  为不含旧内容/旧 hash 的安全 reset；独立问题即使收到损坏旧快照也不会读取它。
- 合并结果绑定 current question hash、policy/release、precedence、previous snapshot hash
  和自身 content hash；`Validate` 确定性重放全部决策并拒绝篡改，可生成绑定当前 turn 的
  `CONVERSATION` EvidenceRef 供后续受控认知阶段引用。
- `context_test.go` 覆盖示例追问、槽位继承/覆盖/清除、歧义时间阻止旧时间继承、完整新问、
  缺失上下文、跨 actor/role/release/conversation、快照深拷贝、hash/replay 防篡改和 fuzz
  span 不变量。

### 2.18 本地启动阻塞修复

- `internal/dataset/mapped_dataset.go` 的系统映射刷新幂等键已加入数据集版本和草稿
  record version。同一事务重试仍得到稳定 key，但语义结构回退到历史 hash 时不会再与
  旧发布记录撞键。
- `internal/dataset/mapped_dataset_test.go` 已覆盖同事务稳定、历史结构再次发布必须隔离，
  以及数据库 128 字符 key 上限。

### 2.19 LLM 完整理解与取证计划

- `internal/askdata/understanding/service.go` 新增 `question-understanding-review-v1`：LLM
  返回当前轮 `QuestionUnderstanding`、冲突、分类型 Evidence Request 和引用证据；该合同
  独立于已冻结的 8 类 Cognition Action，不增加动作类型，也没有 SQL、nGQL、表、列、
  对象版本或任意工具参数字段。
- `BuildUnderstandingReviewInput` 只向 UNDERSTANDING 阶段暴露 AI-003 允许的 4 类事实：
  `CONVERSATION`、`EXACT_MATCHES`、`RULE_PARSE`、`POLICY_EVIDENCE`。Exact fact 同时携带
  被词典/规则/上下文触发器消费后剩余的原文 residual rune spans；事实、匹配和证据集合
  都按规范顺序构造，输入顺序不能改变 Prompt Fact。
- Reviewer 只能引用本轮输入的 conversation/rule/policy/exact EvidenceRef，顶层决策必须
  同时引用 conversation、rule、policy。授权域 hypothesis 必须属于固定 PolicyScope；
  发明证据、旧 hash、输入外域、嵌套引用未列入顶层证据均失败关闭。
- 规则解析仍是权威：确定性时间、比较、TopN/limit、排名方向、显式排序不得被 LLM
  改写或省略；“按/每个”必须得到相邻 `GROUP_BY` role，不能判断时必须保留 unresolved
  并请求维度证据。规则/上下文 unresolved 和稳定冲突码也不得被模型吞掉。
- 当前轮与继承理解继续分离。Evidence Request 使用 `CURRENT/INHERITED` 标记并在对应源
  问句上验证精确 rune span；当前或继承的 metric/dimension/value mention 分别必须覆盖
  `METRIC_CANDIDATES`、`DIMENSION_CANDIDATES`、`MEMBER_CANDIDATES`，每个 unresolved
  声明的 needed evidence 也必须有同 span 请求。
- `question-understanding-result-v1` 保存当前理解、原 ContextMergeResult、冲突、取证计划、
  proposal hash 和 result hash；可使用原请求重建事实、重放全部权威校验并拒绝篡改。
  模型生成的摘要、target/dimension hint 或 request reason 若含物理查询形态同样被拒绝。
- `service_test.go` 使用纯合成难例覆盖完整“时间 + 成员 + 指标 + 分组 + 同比 + TopN +
  排序”问句、未知 residual、续问继承、同比/环比冲突、事实规范顺序、结果防篡改，以及
  规则覆盖、越权域、伪造证据、缺失取证计划和物理查询注入负向路径。
- 本任务未调用外部模型、未读取 API key、未新增依赖/迁移/Compose/页面。`HUMAN-002`
  仍未提供，因此正式业务指标词典、Prompt 调优和生产准确率评测尚不能进行或宣称完成。

### 2.20 结果规范化与等价判定

- `internal/askdata/evaluation/equivalence.go` 新增 `result-equivalence-v1`。比较不读取或
  比较 SQL，而是同时校验规范 Semantic IR 和类型化逻辑结果；输入受 256 列、10,000 行、
  100 万 cell、单 cell 32,768 rune 和 64 MiB 规范 payload 上限约束。
- 调用方必须给出可信 `ResultSchema`：稳定列名、STRING/INTEGER/DECIMAL/FLOAT/BOOLEAN/
  DATE/DATETIME 类型、Key 标记和 DATETIME IANA 时区。输入列按名称规范重排，行按 Key
  后全行排序；FLOAT 禁止作为 Key，任一重复 Key 以 `ErrDuplicateResultKey` 失败关闭。
- DECIMAL 只接受精确十进制文本、`json.Number`、整数或数据库 `driver.Valuer`，使用
  `math/big.Rat` 规范为精确约分值；binary float 不能伪装成 Decimal。NULL 通过独立标记
  与空字符串/`"NULL"` 区分，INTEGER/BOOLEAN/STRING 均规范化并限制非法或超长值。
- DATE 保留 `YYYY-MM-DD` 日历语义；带 offset 的 DATETIME 和 `time.Time` 统一为 UTC，
  zone-less SQL datetime 只能按 Schema 中显式 IANA 时区解释，不读取机器本地时区。
  FLOAT 保留精确 float64 规范值，比较时使用调用方显式、有限的绝对/相对容差。
- `NormalizedResult` 的 SHA-256 可复算并拒绝内容/排序篡改；预期或实际若携带 declared
  result hash，必须分别匹配自己的规范结果。容差内浮点可判 `ResultEquivalent=true`，
  但 `ExactResultHashMatch` 仍为 false，避免容差隐藏工件差异。
- IR 复用 `ir.Canonicalize`，严格比较版本、release/hash、model、metrics、groupBy、filters、
  timeRange、comparison、sort、limit；最终 `Equivalent` 只有 IR 和结果均等价才成立。
  比较报告只包含 IR/result hash、行数和最多 64 个稳定 code/path，不保存结果行或参数。
- `equivalence_test.go` 直接复用 synthetic fixture，覆盖列/行乱序、精确 Decimal 大数和
  科学计数、浮点绝对/相对容差、NULL、日期、上海时区、全部基础标量、重复 Key、IR 差异、
  declared hash、结果 hash/规范顺序篡改、NaN/Inf、缺失时区和 FLOAT Key 等负向路径。
- 本任务未新增第三方依赖、数据库结构、Compose、页面或外部模型调用，也未读取 API key。

### 2.21 Mention/Binding 指标与校准输入

- `internal/askdata/evaluation/binding.go` 新增 `mention-binding-evaluation-v1`。评测 case
  直接复用 `QuestionUnderstanding`，要求 gold/prediction 指向完全相同的原问句，并以
  metric/dimension/value(member) 类型 + 原文 Unicode rune span 作为 mention 身份；重复
  span、越界 mention index 和不同问句全部失败关闭。
- Binding 只接受稳定对象版本 ID。Metric 比较 metric version；Dimension 同时比较
  dimension version 和 `GROUP_BY/FILTER/TIME/SORT` 角色；Member 同时比较 member version
  和所属 dimension version。错误对象、角色、父维度或 span 会各形成一个 FP 和一个 FN，
  不会被局部命中掩盖。
- `PRFScore` 保留 TP/FP/FN、gold/predicted 数量及派生 precision、recall、F1；零分母固定
  输出 0 而不是 NaN。报告按整体、domain、`SIMPLE/COMPOSITE/CONTEXTUAL/RELATIONAL`
  复杂度和六类歧义确定性 micro 聚合，分层总数必须分别与 overall 一致。
- 报告与所有分层/校准样本规范排序并绑定可复算 SHA-256；`ValidateAgainst` 可根据原 cases
  完整重放，识别即使重新计算 hash 的错误标签。持久报告不包含原问句，只保存稳定 case、
  对象版本、mention span 和分层元数据。
- TRAIN/VALIDATION 的每个已选择 binding 会生成校准样本：系统候选分、candidate margin、
  exact/lexical/vector/graph/rule 归一化特征、retrieval rank 和由 gold 派生的正确标签。
  合同不存在 LLM 自报 confidence；SEALED 和 PRODUCTION_REGRESSION case 只参与指标，
  不进入训练/验证输出，防止评测集泄漏。
- `binding_test.go` 复用 synthetic fixture 的同名指标/同名成员资产，覆盖正确直答、错绑父
  维度、错误 mention 边界、漏召回、维度角色错误、输入顺序稳定、分层守恒、校准正负样本、
  sealed 隔离、报告/hash/重放篡改、非法特征/绑定及零分母。
- 本任务未新增依赖、数据库结构、Compose、页面或外部模型调用，也未读取 API key。

### 2.22 问数运行、事件、工件与 Tool 审计控制面

- `000217_askdata_question_runtime_audit` 新增 `question_runs`、`question_run_events`、
  `question_artifacts`、`tool_calls` 四张表及对称 down migration。Run 只保存原问句 hash，
  从创建时固定 tenant/domain/actor、policy scope、ACTIVE release ID/content hash、trace 和
  幂等 key hash；新 run 不能绑定 READY/DRAFT、错误 content hash 或其他 domain release。
- 状态词汇与技术设计一致：RECEIVED → AUTHORIZED → CONTEXT_READY → UNDERSTANDING →
  RETRIEVING → BINDING → GRAPH_VALIDATING → IR_READY → PLAN_VALIDATING → EXECUTING →
  RESULT_VERIFYING，且仅允许设计中的纠错/澄清/阻断边。每次更新必须将 `record_version`
  精确加一；身份、release/policy pin 和预算上限不可变，消耗计数/耗时只能单调增加。
- 预算以显式列限制最多 4 次 LLM、8 次 Tool、2 次正式查询、3 次验证查询和 25 秒；实际
  elapsed 可记录超时清理成本，但动作计数不能突破上限。PLAN/RESULT 回到 BINDING 时必须
  清空旧 binding/graph/IR/plan/result hash，避免纠错后复用陈旧下游工件。
- CLARIFICATION_REQUIRED、ANSWERED、BLOCKED 都是不可回退终态，分别要求 CLARIFICATION、
  ANSWER、BLOCK completion artifact；ANSWERED 还必须具备 understanding/binding/graph/
  IR/plan/result 全链 hash。Run、artifact 和终态通过复合外键绑定，不能引用其他运行的 hash。
- Event、Artifact、Tool outcome 都是 append-only。每条事实必须回绑当前 run version；event
  与 Tool state 必须等于当前 run state。Event 引用 `platform.ai_requests` 时还必须同 actor/
  tenant 且 purpose 为 SEMANTIC_QUESTION。Tool 使用当前 14 个关闭名称词汇表，
  `(tenant,run,tool_call_id)` 唯一，request/result/call hash 支持幂等重放核验。
- ACTIVE release 校验通过不向 app 暴露的 `SECURITY DEFINER` helper 在 trigger 内持有
  transaction-scoped `FOR SHARE`，避免校验与 supersede 间的竞态，也不授予 app
  `releases.UPDATE` 或任意持锁入口。Event/Artifact/Tool stamp 会对 run 行取 `FOR SHARE`，
  与 Store 状态更新串行；EventType 形状必须匹配对应 AI request、Tool outcome 或 Artifact。
- understanding/binding/graph/IR/plan/result hash 只能在各自治理阶段首次出现并形成连续链，
  非纠错路径不得清空或覆盖；因此不能在 RECEIVED 提前注入 result，也不能在 ANSWERED 前
  一次补齐伪造链。Go 与 SQL 的 14×14 状态矩阵由真实数据库集成测试逐项对拍。
- 新的递归 `question_audit_json_is_safe` 在现有 SQL/credential/row guard 上继续拒绝 raw
  question、prompt/messages、reasoning/chain-of-thought、arguments/parameters、SQL/query、
  raw response/answer/result data 等键；JSON 有 16～256 KiB 分类型上限，审计表没有 SQL、
  参数明文、结果行或原问句列。
- 四表全部 FORCE RLS，并额外限制普通 USER 只能访问自己的 run；同域其他 actor 不可读。
  `report_app` 只有 run INSERT/UPDATE 和三类事实 INSERT，worker 只有 SELECT，PUBLIC 和
  connection-test role 无 schema 权限；内部 release lock helper 对 app/worker/connection
  tester 均无 EXECUTE；append-only trigger 对管理员直接篡改同样生效。
- `scripts/verify-database.sh` 已加入关系/RLS/tenant FK、触发器、completion FK、Tool 唯一键、
  JSON guard 和权限静态断言，并在回滚事务中以真实 app 角色验证合法/非法状态迁移、错误
  release pin、current version/state、Tool 精确重试、终态完成形状、跨 actor RLS 和审计冻结。
- 本任务没有页面、Go 业务代码、Warehouse schema、外部模型或 Compose 改动，未读取 API key；
  ACTIVE 激活函数仍故意不存在。

### 2.23 Question 状态机与 PostgreSQL Store

- `internal/askdata/orchestrator/state.go` 是 `DB-005` 生命周期的 Go 可执行镜像：覆盖全部
  合法边、非终态同态 checkpoint、三个冻结终态、绝对且单调的预算消耗、completion shape
  和 PLAN/RESULT → BINDING 有界纠错。`Apply` 不修改输入，expected version 不匹配、非法
  跳转、空 checkpoint、预算回退/超限、hash 提前出现/断链/覆盖都返回稳定 sentinel。
- `store.go` 在 actor/domain 完全匹配的事务上下文内创建、推进和恢复运行；不存在 SYSTEM
  回退。创建先识别精确幂等重放与同 key 碰撞，再由数据库 ACTIVE trigger 原子固定 release；
  并发唯一键或 release 切换失败会在新事务中复查精确历史运行，避免把成功创建误报失败。
- Transition 先锁 run、完整验证已有 event/artifact/tool ledger，再按同一事务写 completion
  artifact、乐观更新 run 和追加 transition event。Event index 独立于 record version；事件以
  previous hash 形成规范链，Artifact/Tool 也按规范内容重算 hash，任何 pin/version/state/
  index/hash 或 EventType 引用篡改都会使 Resume 失败关闭。
- Resume 使用 Repeatable Read + Read Only 快照，终态必须由最后 transition event 与前一
  run version 的正确 completion artifact 同时证明；终态后事件、工件或 Tool outcome 均不
  能继续追加。`SeenActionHashes`/`SeenToolCallIDs` 为 ORCH-003/004 的 no-progress 与精确重放
  提供去重依据，但本任务没有提前实现 Agent Loop 或通用工具执行接口。
- `state_test.go`、`store_test.go` 和 `store_integration_test.go` 覆盖 14×14 Go/SQL 对拍、
  BLOCK/CLARIFY/ANSWER 完整持久链、两条纠错路径、乐观锁、错误 hash/引用、同 actor 幂等、
  跨 actor RLS、superseded release 的 resume/精确 replay 与新运行拒绝。真实 app/admin
  fixture 全部嵌在外层回滚事务；默认 Store 的 Resume 另实测事务隔离级别和只读属性。
- `.github/workflows/ci.yml` 已加入全仓 `go test`，基础设施迁移/数据库验收后再以真实 app/
  admin URL 执行 Orchestrator PostgreSQL 集成，避免数据库路径只在人工本地运行。
- 本任务未涉及页面、Warehouse schema、外部模型或 API key；没有实现 ORCH-003/004，也未
  开放 `activate_release`。

### 2.24 评测集、独立评审、评测运行与结构化反馈

- `000218_askdata_evaluation_feedback` 新增 `evaluation_sets`、`evaluation_cases`、
  `evaluation_case_reviews`、`evaluation_runs`、`query_feedback` 五表和对称 down migration，
  并为 release 增加 `(id,semantic_version,content_hash,domain_id,tenant_id)` 精确评测 pin。
- Set 固定 TRAIN/VALIDATION/SEALED/PRODUCTION_REGRESSION split 与 fixture/E2E mode；密封与
  生产回归必须固定 release。Seal 在数据库内按 set→case→review 锁序重算非空 case、每 case
  两条当前 APPROVED review、case/review count 与 C-collation manifest hash，SEALED/RETIRED
  后内容冻结；没有加入 release gate、审批或激活入口。
- Case 保存 question hash、经审批脱敏问句、redaction policy hash、answerability、预期
  disposition/security、复杂度/歧义、expected path/IR/result hash 与受限 opaque fixture ref，
  不保存 SQL、参数或结果行。数据库维护 `content_updated_by`；原作者和当前 content hash 的
  编辑者都不能评审，两个唯一 slot/不同 reviewer 与 review hash 共同证明独立性；内容变更会
  立即使旧 review 不再计数。
- Evaluation run 只能由 worker 追加，固定 set/case content hash、release ID/version/hash、
  expected/actual path/IR/result、warehouse snapshot/freshness、failure stage/code、显式 security
  与 leak 事实。裸 `*_equivalent=true` 只有同 hash 或存在 comparison report hash 才成立；
  leak 没有默认值，PASSED 禁止 leak，RETIRED 或未密封生产回归集拒绝新 run，既有 run 不可
  UPDATE/DELETE。
- Feedback 通过复合外键精确绑定终态 question run、原 actor、release/policy/domain/tenant；
  `ACCURATE` 只能配 `NONE`，`INACCURATE` 必须选择 metric/dimension/member/time/relationship/
  data/permission/expression/other，并使用 record version 与数据库重算 hash；不可删除，也不
  修改答案或权威语义。
- 五表全部 FORCE RLS。普通 USER 即使是领域管理员也只能管理 DRAFT 内容，Seal 后看不到问句/
  review；SYSTEM worker 只可读取密封内容并 INSERT evaluation run。`seal_evaluation_set` 另在
  SECURITY DEFINER 入口显式复核领域管理权限；PUBLIC/connection tester 无 schema 权限，
  manifest/lifecycle helper 不直接授权运行角色。
- `scripts/migrate.sh` 已同步最小权限；`scripts/verify-database.sh` 同时静态检查关系、RLS、
  复合 FK、trigger/function/search_path/constraint/grant，并以真实 app/worker/admin 回滚 fixture
  验证普通成员封存拒绝、当前内容作者自审拒绝、stale review、双审 Seal、密封隐藏/冻结、
  DRAFT production/错误 release/path/leak/遗漏 leak/RETIRED run 拒绝、run append-only，以及
  非终态/cross-actor/shape/乐观锁/删除 feedback 拒绝。
- 本任务没有页面、Go 业务代码、Warehouse schema、外部模型或 API key；页面确认门禁未触发。

### 2.25 敏感维度成员政策与数据库内 EXACT_ONLY

- `000223_askdata_sensitive_member_policy` 为 alias 增加数据库维护的维度绑定查找 hash，并在
  dimension/member 两侧强制 sensitivity floor：成员可以更严格，不能比所属维度宽松；维度
  也不能通过 DRAFT 更新越过已有成员。安装 trigger 前先锁表并审计历史数据，down migration
  对称移除本轮函数、trigger、index 和列。
- MEMBER release contract 只能有 `schemaVersion/type/dimensionVersionId/aliasVersionIds` 四个
  key；alias version ID 必须小写 UUID、排序、唯一、最多 64 个，并精确属于同一已认证成员。
  release object sensitivity 必须等于源 member；运行角色可读的 manifest 因此不含 label、
  member/alias lookup hash 或 alias content hash。DOMAIN manifest 另补 object/domain identity
  guard，避免既有 validator 改为 SECURITY DEFINER 后出现跨域入口。
- `askdata.lookup_exact_dimension_member(release_id,release_hash,dimension_version_id,lookup_hash)`
  是唯一 EXACT_ONLY 运行入口。它固定 USER access context、活动成员关系、READY PostgreSQL
  投影、DIMENSION/MEMBER manifest、CERTIFIED/有效期、候选唯一性和 release 状态；READY、
  ACTIVE 与已固定旧运行的 SUPERSEDED 均可重放。CONFIDENTIAL 与 RESTRICTED 分别检查
  `LOOKUP_CONFIDENTIAL_MEMBER`/`LOOKUP_RESTRICTED_MEMBER` 的 USER 或 ACTIVE ROLE 对象授权；
  missing、denied、ambiguous、expired、wrong hash 和 unpinned release 统一返回零行。
- `scripts/migrate.sh` 每次部署先清除历史表/列 ACL。app 仅可读取 member/alias 的非文本管理
  元数据并保留受治理 DML；worker 仅可读取画像差异所需的 `profile_id/member_key_hash`；raw
  canonical label、alias、normalized value、lookup hash 对 app/worker/connection tester 均不可读，
  lookup 函数只授权 app。
- `internal/askdata/security/member.go` 统一 FULL/EXACT_ONLY/ON_DEMAND/NONE 暴露矩阵。原始成员
  值只存在于构造 lookup 的问句 span，SQL 参数只有维度绑定 hash；lookup/match 的 fmt/JSON、
  error 和 evidence 均不输出 label、原问句或 lookup hash。匹配结果只携带成员/维度版本 ID、
  content hash 和 label-free EvidenceRef，并封装为私有 payload/proof；外部调用方只有只读
  accessor，复制合法结果后替换 ID、content hash 或 EvidenceRef 会失败关闭。
- `internal/askdata/understanding/service.go` 显式接收不可伪造的 sensitive match。当前与继承问句
  的敏感 span 会从 residual/rule/context/exact 等所有嵌套 Prompt fact 字符串递归移除，并以
  等 Unicode rune 长度遮罩 NFKC、case-fold、全角及 ASCII 邻接变体，保持后续 span 不漂移。
  Reviewer 必须基于遮罩问句返回；mention、summary、reason、conflict 或 evidence request 只要
  回显原值/规范变体或覆盖敏感 span，即失败关闭，验证完成后才在模型边界外恢复原问句。公开
  label grant/constructor 已删除，普通 `ExactMatch` 一律拒绝 MEMBER，不能由调用方自报 FULL
  policy 绕过数据库 label-free 路径。
- 画像 observation 的 `eligible_for_llm` 每次从 sensitivity、member index policy 与实际高基数
  状态重算，仅 `PUBLIC/INTERNAL + FULL + low-cardinality` 可为 true；伪造 persisted bool 会被
  拒绝。本地确定性 alias 检查仍可使用这些 observation，但 `DIMENSION_PROFILE` PromptFact 是
  独立聚合载荷，不含 label、alias、normalized value、member hash 或派生 ID；成员型 LLM reviewer
  在实现按 `profile_generation` 从 PostgreSQL 权威重载前始终不调用。外部包用自定义
  `ProfileStore + WarehouseScanner` 注入 FULL/INTERNAL 任意标签的攻击回归已覆盖该边界。
- MEMBER search document 统一复用安全暴露矩阵；敏感/受限或非 FULL 成员不能进入 lexical/
  embedding 文档。Reranker 在 request 规范化与 evidence 校验两层均拒绝显式 MEMBER，调用方
  即使伪造 INTERNAL/FULL/ALLOW 形状也不会触发 reviewer。当前 Go `internal/` request 只允许
  SQL/RLS/release 过滤后的可信 typed producer 构造，不能暴露为网络/插件合同；未来若改变该
  信任边界，必须先增加 authoritative candidate/coverage provenance。CI 已加入 security
  PostgreSQL integration；本任务没有页面、外部模型调用、Warehouse schema 或 ACTIVE 激活入口，
  页面确认门禁未触发。

### 2.26 WEB-001 问数工作台设计确认与落地（已完成）

- 已用应用内浏览器检查真实登录页、平台管理页和数据集配置页，并以现有 Haier 品牌、248px
  固定侧栏、`#2864DC` 主蓝、`#F7F8FA` 画布、白色细边框卡片、9～11px 圆角和现有字体/图标
  体系作为强制视觉约束。参考截图：
  `/Users/susanmartinez/.codex/visualizations/2026/08/05/019fd157-a519-7c12-afb3-234454839fae/existing-dataset-reference.png`。
- 方案 1「对话中枢」：
  `/Users/susanmartinez/.codex/generated_images/019fd157-a519-7c12-afb3-234454839fae/exec-4564e2b6-050a-4a0f-a01e-2d2e92ce803e.png`。
- 方案 2「分析画布」：
  `/Users/susanmartinez/.codex/generated_images/019fd157-a519-7c12-afb3-234454839fae/exec-1f52ab9e-76c7-4a08-ba9d-965404d7cc81.png`。
- 方案 3「证据驾驶舱」：
  `/Users/susanmartinez/.codex/generated_images/019fd157-a519-7c12-afb3-234454839fae/exec-66f629ed-773e-48f9-ac86-25084f7a53ec.png`。
- 确认状态：用户于 2026-08-06 选择 **方案 3「证据驾驶舱」**。
- 已新增 `/ask-data` 受保护路由和“智能问数”导航，严格使用
  `RequireAuth + RequireBusinessDomain`；实现文件为 `web/src/pages/AskDataPage.tsx`、
  `web/src/styles/ask-data.css`，并在 `AppShell.tsx` 增加兼容旧页面的 `titleMeta`。
- 页面使用 typed mock 覆盖会话筛选、常用/智能建议、开始新问题、idle/loading/complete、
  三阶段受控状态、ECharts 双向渠道贡献图、结果展开、证据折叠和反馈；未接入真实 API/SSE，
  未持久化 mock，也不展示 prompt、SQL 参数或思维链。
- `web/package.json`/lock 新增 ECharts，采用按组件注册方式控制增量；现有 Haier Logo 和
  Phosphor 图标直接复用，没有新增伪造图片、手工 SVG 或 CSS 图形资产。
- 设计 QA：`design-qa.md`；最终实现截图：
  `design-qa-artifacts/ask-data-implementation-final.png`；参考/实现同画面对照：
  `design-qa-artifacts/comparison-final.png`；最终结果为 `passed`，没有遗留 P0/P1/P2。
- 已执行 `docker compose up -d --build web`，默认 Compose 预览
  `http://127.0.0.1:5173/ask-data` 已加载本次源码；API、Web、Connector、控制库、数仓和
  MinIO 均恢复 healthy。

2026-08-06 `WEB-001` 验证：

```sh
cd web
npm run lint
npm run build
git diff --check -- package.json package-lock.json src/app/App.tsx \
  src/components/AppShell.tsx src/main.tsx src/pages/AskDataPage.tsx src/styles/ask-data.css
cd ..
docker compose up -d --build web
docker compose ps
```

结果：全部通过。Vite 仅保留“大于 500 kB chunk”性能提示，不影响构建退出码；ECharts
已由全量 import 改为模块化注册。应用内浏览器使用真实本地种子管理员登录和 ACTIVE“企业经营”
领域验证默认 Compose `/ask-data`，路由门禁、导航 active、提问/加载/完成、会话筛选、结果展开、
证据折叠和反馈均通过；重建后核心提问流程再次通过。

### 2.27 GRAPH-002 正式开发 Compose、初始化与最小权限（已完成）

- 用户于 2026-08-06 确认 `HUMAN-005` 的开发部分：NebulaGraph Server/官方 Go Client
  锁定 `v3.8.0`，采用单副本、持久卷、每环境共享 Space、tenant/release typed 隔离、
  API `GUEST`、Worker `USER` 和环境变量开发凭据。生产容量、TLS、Secrets、备份和多副本
  仍属 `HUMAN-005/OPS-004`，不得从本机结果推断。
- 根 `compose.yaml` 新增 metad/storaged/graphd、一次性 init、verification job 与
  `nebula-loopback-proxy`；Meta/Storage 只加入 internal cluster network，graphd 再加入
  internal client network，API/Worker 只能访问客户端面。graphd 不发布宿主端口；无凭据 proxy
  仅在 init 成功后显式启动并绑定 `127.0.0.1:${ASKDATA_NEBULA_PORT}`。Connection Test Worker、
  Connector、Web 不持有图凭据且不加入图网络。
- metad/storaged 数据与日志使用命名卷。init 在首次登录后立即把厂商 root 密码轮换为配置值，
  等待新密码生效且旧密码失效后才执行 `ADD HOSTS`；随后精确核验目标 storaged 的
  `host/port/ONLINE/v3.8.0`、单 partition/replica、`FIXED_STRING(256)` Space、当前 Adapter
  冻结的 `semantic_model/metric/dimension/member` 和 `MODELED_BY/HAS_DIMENSION/HAS_MEMBER/
  JOINS_TO` 字段、nullable/default/comment/TTL 合同，以及 API/Worker 的唯一 Space 角色。
- production 使用 `local_*`、未知 `APP_ENV`、错误/默认密码、Space 参数漂移、Schema 漂移、
  API/Worker 在其他 Space 的陈旧授权均失败关闭。API 的 GUEST 实测只读，Worker 的 USER
  实测可写/删投影但不能 DDL/授权；两者均不能访问未授权 Space。共享 Space 不提供数据库
  RLS，租户边界仍由 typed Graph Service 强制 tenant/domain/release 条件。
- `scripts/run-with-nebula-role.sh` 为 `make run-api/run-worker` 只映射本角色到 generic runtime
  凭据，再删除 root/bootstrap/canonical secret；Connection Test Worker 和 seed 删除全部图
  配置。Compose 也显式清空共享 `env_file` 带入的越界凭据。`check-compose.sh` 同时验证服务级
  端口/网络和本地进程凭据矩阵。
- `scripts/verify-nebula-compose.sh` 不读取用户 `.env`：
  `deployments/nebula/verification.override.yaml` 会重置 API、Worker、Connection Test Worker 的
  service `env_file`，配置检查同时禁用 env file 解析。每次由同一 nonce 创建唯一
  `askdata-graph002-verify-*` project 与 `g002_verify_*` Space，以及独立账号、随机 loopback
  端口和独立卷。Go integration 在图写入前反查该 project 的 proxy published endpoint 与
  Compose project/service 标签；随后对 4 类 Tag、4 类 Edge 分别执行 tenant/release 排除，
  只有 Resolve 成功、目标关系消失且无关对照关系保留才通过。测试还覆盖真实 GraphPlan、
  参数/并发、错误密码、未授权 Space、GUEST/USER 正负权限、强制重建持久化及 partition/Schema
  drift 失败关闭。退出 trap 仅 `down -v` 该精确格式项目，已实测无容器、网络或卷残留；
  CI 新增独立 `graph-compose` job。
- `dev-services.sh start` 已按 metad/storaged/graphd → init → storaged ready → proxy → migration →
  apps 顺序启动，并在应用健康后删除带 bootstrap 环境的已退出 init 容器。当前 canonical 栈已
  切换到新网络；早期实现遗留且容器数为 0 的旧 graph network 已删除，没有删除图数据卷。
- 本任务没有新增页面、流程或显著视觉状态，页面确认门禁未触发。

## 3. 验证记录

在仓库根目录执行：

```sh
gofmt -w $(rg --files internal/askdata -g '*.go')
go test ./...
ENV_FILE=.env.example ./scripts/check-compose.sh
./scripts/ci-check.sh
```

结果：全部通过。

2026-08-05 `DIM-003` 验证：

```sh
go test ./internal/askdata/dimension ./internal/askdata/cognition
go test ./internal/askdata/...
go vet ./internal/askdata/...
test "$(gofmt -l cmd internal | wc -l | tr -d ' ')" = "0"
go test ./...
./scripts/ci-check.sh
```

- 全部通过；流程测试覆盖 generation 输入顺序稳定性、确定性别名候选、LLM 聚类/层级/
  哨兵建议、未知成员、错误 sensitivity、截断扫描和 EvidenceRef/hash 回绑。
- 本任务未改数据库结构或页面；未运行外部真实模型，不需要也未读取 API key。

2026-08-05 `SEARCH-004` 验证：

```sh
go test ./internal/askdata/search ./internal/askdata/cognition ./internal/askdata
go test ./internal/askdata/...
go vet ./internal/askdata/...
git diff --check
go test ./...
./scripts/ci-check.sh
```

- 全部通过；覆盖候选顺序/hash 稳定性、Prompt 指令边界、可选稳定 ID 重排、全量拦截时
  跳过 LLM、block override、发明 ID、跨候选 EvidenceRef、错误集合 hash、敏感/物理查询
  上下文，以及 unknown/duplicate JSON 字段失败关闭。
- 本任务未改数据库结构或页面，未调用外部真实模型，也未读取 API key。

2026-08-05 `GRAPH-001` 验证：

```sh
go mod verify
go test ./internal/askdata/graph -count=1
./scripts/verify-nebula-poc.sh
go test ./internal/askdata/...
go vet ./internal/askdata/...
go test ./...
./scripts/ci-check.sh
git diff --check
```

- 全部通过；真实 POC 在 Linux/ARM64 Docker 上运行 NebulaGraph Server `v3.8.0`，并使用
  锁定的 `nebula-go/v3 v3.8.0` 完成 plain/TLS 查询、Space、参数、超时、并发和 graphd
  故障切换验证；结束后未残留 `askdata-nebula-poc` 容器或网络。
- 本任务未修改生产 Compose、数据库结构或页面，未读取 API key。

2026-08-05 `GRAPH-003` 验证：

```sh
gofmt -w internal/askdata/graph
go test ./internal/askdata/graph -count=1
go vet ./internal/askdata/graph
./scripts/verify-nebula-poc.sh
go test ./internal/askdata/... -count=1
go vet ./internal/askdata/...
test "$(gofmt -l cmd internal | wc -l | tr -d ' ')" = "0"
go test ./...
./scripts/ci-check.sh
git diff --check
```

- 全部通过；真实 POC 使用公开 Adapter 写入并解析合成 GraphPlan，覆盖固定查询模板、
  Tag/Edge scope、认证 Join、风险和稳定 EvidenceRef；结束后没有残留
  `askdata-nebula-poc` 容器或网络。
- 本任务未修改生产 Compose、数据库结构或页面，未调用外部模型，也未读取 API key。

2026-08-05 `NLU-001` 验证：

```sh
gofmt -w internal/askdata/understanding/normalize.go \
  internal/askdata/understanding/normalize_test.go
go test -race ./internal/askdata/understanding -count=1
go test ./internal/askdata/understanding -run '^$' \
  -fuzz '^FuzzNormalizeQuestionOffsetRoundTrip$' -fuzztime=5s
go test ./internal/askdata/... -count=1
go vet ./internal/askdata/...
test "$(gofmt -l cmd internal | wc -l | tr -d ' ')" = "0"
go test ./...
./scripts/ci-check.sh
git diff --check
```

- 全部通过；fuzz 约执行 21 万次，验证任一规范 rune 映回原文后再投影时仍覆盖原 rune，
  race 未发现共享 Unicode case folder 的并发问题。
- 本任务未新增依赖、数据库结构或页面，未调用外部模型，也未读取 API key。

2026-08-05 `NLU-002` 验证：

```sh
go test ./internal/askdata/understanding -count=1
go vet ./internal/askdata/understanding
go test -race ./internal/askdata/understanding -count=1
go test ./internal/askdata/understanding -run '^$' \
  -fuzz '^FuzzRuleParserPreservesOriginalSpans$' -fuzztime=5s
go test ./internal/askdata/... -count=1
go vet ./internal/askdata/...
test "$(gofmt -l cmd internal | wc -l | tr -d ' ')" = "0"
go test ./...
./scripts/ci-check.sh
git diff --check
```

- 全部通过；fuzz 约执行 16 万次，race 未发现问题。测试验证固定上海时区、自然周周一边界、
  财年配置、显式半开日期范围、歧义失败关闭、查询语法冲突，以及所有命中到原文 span 的
  精确回映和解析结果重放防篡改。
- 本任务未新增依赖、数据库结构、Compose 或页面，未调用外部模型，也未读取 API key。

2026-08-05 `NLU-003` 验证：

```sh
go test ./internal/askdata/understanding -count=1
go vet ./internal/askdata/understanding
go test -race ./internal/askdata/understanding -count=1
go test ./internal/askdata/understanding -run '^$' \
  -fuzz '^FuzzMergeContextReplayAndSpanSafety$' -fuzztime=5s
go test ./internal/askdata/... -count=1
go vet ./internal/askdata/...
test "$(gofmt -l cmd internal | wc -l | tr -d ' ')" = "0"
go test ./...
./scripts/ci-check.sh
git diff --check
```

- 全部通过；最终 fuzz 约执行 9 万次，race 未发现问题。测试覆盖续问识别、槽位优先级、
  显式清除、缺失上下文、独立问题不读取旧快照、权限/release/conversation 变化安全 reset、
  snapshot/result hash 防篡改、确定性重放和所有本轮触发 span 的精确原文映射。
- 本任务未新增依赖、数据库结构、Compose 或页面，未调用外部模型，也未读取 API key。

2026-08-05 `NLU-004` 验证：

```sh
go test ./internal/askdata/understanding -count=1
go test ./internal/askdata/understanding -coverprofile=/tmp/askdata-nlu004.cover
go test ./internal/askdata/... -count=1
go test -race ./internal/askdata/...
go vet ./...
test "$(gofmt -l cmd internal | wc -l | tr -d ' ')" = "0"
go test ./...
./scripts/ci-check.sh
```

- 全部通过；`understanding` 包覆盖率为 77.8%，askdata race 未发现问题。合成测试覆盖
  conversation/exact/residual/rule/policy 四类事实、完整意图、当前/继承源 span、规则冲突、
  授权域和证据白名单、取证覆盖、物理查询拒绝、稳定 hash 与结果重放防篡改。
- 本任务未运行数据库/数仓/Nebula integration test，因为没有迁移、存储或外部服务改动；
  未调用真实 LLM，也未读取或输出本地密钥。

2026-08-06 `EVAL-001` 验证：

```sh
go test ./internal/askdata/evaluation -count=1 \
  -coverprofile=/tmp/askdata-eval001.cover
go test ./internal/askdata/... -count=1
go test -race ./internal/askdata/...
go vet ./...
test "$(gofmt -l cmd internal | wc -l | tr -d ' ')" = "0"
go test ./...
./scripts/ci-check.sh
git diff --check
```

- 全部通过；`evaluation` 包覆盖率为 71.0%，askdata race 未发现问题。测试覆盖 synthetic
  golden IR/result、列/行规范顺序、Decimal 精度、FLOAT 容差与 exact hash 差异、NULL、
  日期/时区、重复 Key、全部标量、IR/result hash 防篡改和有界失败关闭。
- 本任务没有迁移、存储、外部服务或页面改动，因此未运行数据库/数仓/Nebula integration；
  未调用真实模型，也未读取或输出本地密钥。

2026-08-06 `EVAL-002` 验证：

```sh
go test ./internal/askdata/evaluation -count=1 \
  -coverprofile=/tmp/askdata-eval002.cover
go test ./internal/askdata/... -count=1
go test -race ./internal/askdata/... -count=1
go vet ./...
test "$(gofmt -l cmd internal | wc -l | tr -d ' ')" = "0"
go test ./... -count=1
./scripts/ci-check.sh
git diff --check
```

- 全部通过；`evaluation` 包覆盖率为 77.1%，askdata race 未发现问题。测试覆盖三类
  mention/binding PRF、domain/复杂度/歧义分层守恒、训练/验证校准正负标签、sealed 隔离、
  错误 span/角色/成员父维度、规范顺序、零分母和报告重放防篡改。
- 本任务没有迁移、存储、外部服务或页面改动，因此未运行数据库/数仓/Nebula integration；
  未调用真实模型，也未读取或输出本地密钥。

2026-08-06 `DB-005` 验证：

```sh
ENV_FILE=.env.example ./scripts/migrate.sh
ENV_FILE=.env.example ./scripts/verify-database.sh
ENV_FILE=.env.example ./scripts/verify-warehouse.sh
sh -n scripts/migrate.sh scripts/verify-database.sh
go test ./internal/askdata/... -count=1
go test ./... -count=1
go vet ./...
ENV_FILE=.env.example ./scripts/check-compose.sh
./scripts/ci-check.sh
git diff --check
```

- 全部通过。`000217` 在本地无业务运行数据时完成精确 down→up 往返；最终向前迁移幂等，
  数据库验收 fixture 全部位于回滚事务，未留下 synthetic tenant/release/run。
- 实测 app 角色 ACTIVE release pin、合法状态链、错误 release hash/跨级迁移拒绝、current
  version/state 回绑、Tool 重试单行幂等、BLOCK completion artifact、终态冻结、同域跨 actor
  RLS；管理员 UPDATE/DELETE event/artifact/tool outcome 也被 append-only trigger 拒绝。
- 本任务没有页面、Go 业务代码、Warehouse schema、外部模型或 Compose 改动；未读取或输出
  本地密钥，ACTIVE 激活函数仍不存在。

2026-08-06 `ORCH-002` 验证：

```sh
gofmt -w internal/askdata/orchestrator/*.go
ENV_FILE=.env.example ./scripts/migrate.sh
ENV_FILE=.env.example ./scripts/verify-database.sh
ASKDATA_INTEGRATION_DATABASE_URL='postgres://report_app:...@127.0.0.1:5432/...' \
ASKDATA_INTEGRATION_ADMIN_DATABASE_URL='postgres://report_admin:...@127.0.0.1:5432/...' \
  go test ./internal/askdata/orchestrator -count=1
ASKDATA_INTEGRATION_DATABASE_URL='postgres://report_app:...@127.0.0.1:5432/...' \
ASKDATA_INTEGRATION_ADMIN_DATABASE_URL='postgres://report_admin:...@127.0.0.1:5432/...' \
  go test -race ./internal/askdata/orchestrator -count=1
go test ./internal/askdata/... -count=1
go test ./... -count=1
go vet ./...
bash -n scripts/migrate.sh scripts/verify-database.sh
git diff --check
```

- 全部通过。真实数据库测试逐项对拍 Go/SQL 14×14 状态矩阵，并覆盖 RECEIVED 到
  ANSWERED、CLARIFICATION_REQUIRED、BLOCKED 以及 PLAN/RESULT 两条 correction 的 Store +
  trigger + replay 组合；缺失/错误 EventType 引用、提前 hash、非法跳转、过期 version、跨
  actor 读取、错误/superseded release 新建均失败关闭。
- `000217` 在确认四张运行时表均为 0 后再次完成精确 down→up，最终 migrate 幂等；数据库
  verify 与 Go 集成 fixture 均回滚，检查后没有 `orch_*` / `askdata_runtime_*` 合成数据。
- ACTIVE release lock helper 只供 lifecycle trigger 内部使用；app、worker、connection tester
  均无 EXECUTE，app 仍无 releases UPDATE。`FOR SHARE` release/fact 锁、hash 阶段链和事件
  引用约束已纳入数据库静态与真实角色验收。
- 本任务没有页面、Warehouse schema、外部模型或 API key 调用，ACTIVE 激活入口仍不存在。

2026-08-06 `DB-006` 验证：

```sh
ENV_FILE=.env.example ./scripts/migrate.sh
ENV_FILE=.env.example ./scripts/verify-database.sh
go test ./internal/askdata/... -count=1
go test ./... -count=1
go vet ./...
bash -n scripts/migrate.sh scripts/verify-database.sh
git diff --check
```

- 全部通过。`000218` 在确认五张评测/反馈表没有业务数据后完成精确 down→up；真实 app/
  worker/admin fixture 全部回滚，验证后没有 evaluation/query feedback synthetic residue。
- 实测当前内容作者自审、同 reviewer 双 slot、stale review、少于两条独立 APPROVED review、
  SEALED 内容可见/可改、错误 release/path/result equivalence、leak 事实遗漏/矛盾、run 更新删除，
  以及非终态或跨 actor feedback 均失败关闭；本任务没有页面或激活入口。

2026-08-06 `SEC-003` 验证：

```sh
ENV_FILE=.env.example ./scripts/migrate.sh
ENV_FILE=.env.example ./scripts/verify-database.sh
ASKDATA_INTEGRATION_DATABASE_URL='postgres://report_app:...@127.0.0.1:5432/...' \
ASKDATA_INTEGRATION_ADMIN_DATABASE_URL='postgres://report_admin:...@127.0.0.1:5432/...' \
  go test ./internal/askdata/security -count=1
go test ./internal/askdata/... -count=1
go test -race ./internal/askdata/security ./internal/askdata/dimension ./internal/askdata/understanding -count=1
go test ./... -count=1
go vet ./...
./scripts/ci-check.sh
bash -n scripts/migrate.sh scripts/verify-database.sh
git diff --check
```

- 全部通过。`000223` 在确认 member/alias/MEMBER release object 均为 0 的前提下精确
  down→up，并重新执行正式 `migrate.sh` 补齐新建函数的运行角色 ACL；最终迁移为 220 个、
  最高版本 `000223`，随后完整数据库验证再次通过。
- 真实 `report_app` fixture 覆盖 confidential canonical/alias 成功、restricted ACTIVE ROLE
  成功，以及 missing/unauthorized 同为零行、ambiguous/expired/wrong hash/random release 为零行；
  sensitivity-only manifest 更新、低于维度的 member、敏感 MEMBER search document 均被拒绝。
  SUPERSEDED release 的固定回放成功，fixture 回滚后 tenant/member/alias/MEMBER release residue
  均为 0。
- Go/race 覆盖 lookup/match 格式化与 JSON 不泄漏、相邻 ASCII/全角/NFKC/case-fold 变体、当前/
  继承问句递归 fact 遮罩、敏感 span 冲突及 Reviewer 在 mention/summary/reason/conflict/evidence
  request 中回显失败关闭；另覆盖合法 match 复制后篡改、调用方构造普通 MEMBER hit、
  EXACT_ONLY/ON_DEMAND/NONE/高基数伪造 eligible，以及外部包通过自定义 Store/Scanner 注入标签。
  画像 PromptFact 仅含聚合统计且 member reviewer 调用次数为 0；Reranker 外部包攻击 fixture
  进一步验证显式 MEMBER 即使携带伪造 INTERNAL/FULL/ALLOW 证据也不能生成 PromptFact 或触发
  reviewer。本任务没有页面、Warehouse、外部模型或 API key 调用。

2026-08-05 全新环境启动验证：

```sh
go test ./...
./scripts/dev-services.sh start
./scripts/dev-services.sh status
```

- 该次全新环境启动时有 218 个迁移版本；加入 `000218` 后为 219 个，本轮 `000223` 后当前为
  220 个，范围为 `000001`～`000223`，`000192` 不存在，`000219`～`000220` 仍按 TODO 保留给
  发布门禁与审批迁移。
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
- member sensitivity 低于维度、仅修改 MEMBER manifest sensitivity、contract 夹带 label/hash、
  跨 member/未认证/未排序 alias version ID，以及 DOMAIN release object 跨域 identity；
- 敏感成员 missing/denied 结果分叉、错误 release/hash/投影、未发布或未固定对象、过期成员、
  同 hash 多成员歧义、非活动 ROLE/错误动作，以及 raw member/alias/profile label 列权限；
- EXACT_ONLY 原值、question/lookup hash 进入 fmt/JSON/evidence，当前或继承问句敏感 span 进入
  Prompt fact，Reviewer 在 mention/summary/reason/conflict/evidence request 回显规范化变体；
- profile generation 输入顺序漂移、伪造成员 ID、跨 generation/evidence hash、未观测别名、
  敏感成员进入 LLM、扫描不完整时自动应用；
- MEMBER 文档缺少维度上下文、文档凭证/物理查询、Embedding 维数错误、旧租约覆盖；
- Hybrid Retriever 无权限 scope、错误 release、水位过滤和向量降级。
- Reranker 候选集合篡改、错误 release-bound candidate-set hash、发明/blocked candidate、
  跨候选 evidence、非规范反例、敏感/凭证/物理查询上下文，以及全候选 block 后误调用 LLM；
- Nebula Client 缺失 Space、TLS 证书/握手错误、注入形态参数、无响应 endpoint 超时、
  单 graphd 故障和非 POC Compose target；版本锁禁止 master/nightly 及错误使用 v5 gRPC Client；
- GraphPlan 注入形态/冲突稳定 ID、重复 VID、候选和跳数上限、跨 tenant/domain/release、
  篡改 PolicyScope、发明模型/维度/成员、未授权中间模型、未认证 Join、风险/path/plan hash
  篡改、任意 nGQL 泄露；
- 问句非法 UTF-8、超长输入、空/纯标点内容、控制字符、双向覆盖/零宽不可见字符、规范
  span 越界、已删除语气词 span 伪映射、规范文本同长度篡改，以及 NFKC 组合/展开导致的
  offset 漂移；
- 无年份/顺序不明/非法日期、裸财年/自然周、财年配置缺失、多个时间表达、倒序或跨粒度
  日期范围，以及固定上海时区的 UTC 跨日边界；
- 多个比较/排名、Top 百分比、越界 limit、显式排序相互冲突或与排名方向冲突，以及
  “按揭/按摩/按钮/按键/按压”被错误识别为分组标记；
- 未完成/含 unresolved 的理解进入会话快照、缺少上一轮的片段追问、旧快照或合并结果
  hash 篡改、同 turn 重放、完整新问题误继承旧条件，以及清除指令被规则命中反向覆盖；
- 跨 tenant/actor/domain/role/policy/release/conversation 继承，或在独立/清空问题中读取、
  暴露损坏旧快照；
- LLM 完整理解改写确定性时间/比较/TopN/排序、吞掉规则或上下文冲突、错误分组 role、
  当前/继承 span 混用、越权 domain hypothesis、发明/跨输入 EvidenceRef、嵌套证据未列入
  顶层引用、mention 或 unresolved 缺少分类型取证请求，以及模型文本夹带物理查询；
- 结果列缺失/重复、行列数不一致、超出行/cell/字节预算、Decimal 经 binary float 损失精度、
  NaN/Inf、FLOAT Key、重复业务 Key、NULL 与字符串混同、非法日期/时区、zone-less 时间读取
  本机时区、规范行/hash 篡改、declared result hash 不匹配，以及仅结果相同但 IR 关键字段错误；
- gold/prediction 问句不一致、同类型 mention span 重复、binding index 越界/重复、对象版本 ID
  非法、dimension role 与 mention 不一致、member 缺少所属 dimension、校准特征 NaN/Inf/越界、
  retrieval rank 非法、分层总数漂移、校准顺序/shape/hash 篡改，以及 SEALED/生产回归样本泄漏
  到训练或验证输入；
- 问数 run 绑定非 ACTIVE/错误 hash release、跨级或终态后状态迁移、`record_version` 非精确加一、
  身份/release/policy/预算上限改写、预算用量回退、阶段前注入或终态前补齐 hash、hash 断链/
  覆盖、纠错保留陈旧下游 hash、终态缺少对应 artifact、EventType 缺少/伪造 AI/Tool/Artifact
  引用、事实缺少对应审计 event、child fact 伪造 run version/state/actor/release/policy、跨 actor
  读取、Tool Call 重复、event/artifact/tool outcome 更新或删除，以及审计 JSON 夹带 question/
  prompt/reasoning/SQL/参数/response/result rows；
- 普通成员管理/封存评测集、case 原作者或当前内容编辑者自审、同 reviewer 占用两个 slot、
  stale review 继续计数、少于两条当前 APPROVED review Seal、SEALED case/review 改写、USER
  读取密封问句、DRAFT production regression 或 RETIRED set 新增 run、错误 set/case/release/path
  pin、未固定 warehouse freshness、等价布尔缺少 hash/report 证据、遗漏 leak 字段、leak 与
  security 状态矛盾、evaluation run 更新/删除，以及非终态/cross-actor/shape 错误/过期 version/
  删除 query feedback；
- AVG/COUNT_DISTINCT 可加性错误、MANY_TO_MANY SAFE fanout、敏感/高基数成员策略；
- canonical JSON 重复键、对象顺序变化、contract hash 篡改、release 对象重复；
- repository stale record version、空/篡改 cursor、跨领域读取；
- 非数值 measure 导入、跨 domain asset 过滤、重复导入稳定 ID/hash。

2026-08-06 `GRAPH-002` 验证：

```sh
./scripts/check-compose.sh
./scripts/verify-nebula-env-isolation.sh
./scripts/verify-nebula-compose.sh
./scripts/verify-nebula-poc.sh
docker compose --env-file .env.example --env-file .env \
  --profile verification run --rm --no-deps nebula-verify
go test ./... -count=1
go vet ./...
ENV_FILE=.env ./scripts/verify-database.sh
ENV_FILE=.env ./scripts/verify-warehouse.sh
./scripts/ci-check.sh
npm --prefix web run lint
npm --prefix web run build
./scripts/dev-services.sh start
./scripts/dev-services.sh status
git diff --check
```

- 正式隔离 Compose 验收通过并自动清理容器、网络、命名卷；canonical Compose 已切换到
  cluster/client/host-proxy 三网结构，所有基础设施和应用服务 healthy，graphd 无宿主端口，
  proxy 仅为 `127.0.0.1:9669`。
- 全量 Go test/vet、数据库/数仓验证、Compose 静态检查、本地凭据隔离、CI 静态检查及前端
  lint/build 通过。Vite 仅保留既有大 chunk 提示。
- GRAPH-001 POC 在与其他重型回归并行时出现过 2 秒 SessionPool 并发认证超时；其独立的
  600ms blackhole 超时负测保持不变，普通 POC socket timeout 调整为 10 秒并在独立重跑通过。
- 本轮没有调用外部模型、没有读取或输出 API key，没有新增页面。

## 4. 工作区注意事项

- 用户在本次实施前已有多项 `docs/*` 删除状态；这些文件未被恢复或修改。
- `ASK_DATA_TECHNICAL_DESIGN.md`、`ASK_DATA_CODEX_TODO.md` 为当前项目设计和执行事实源。
- 不要恢复历史 `platform.semantic_*` 运行时；新控制面使用 `askdata` schema。
- `WEB-001` 的方案 3 已取得用户确认并完成。后续新增页面、流程或显著视觉状态仍须先出设计稿
  并取得用户确认；`WEB-002` 纯 API Client/类型接线不触发页面门禁，但必须等待 `ORCH-005`。
- 本地开发验证使用 `.env.example`；不要把用户给出的 API key 补写到 `.env.example`。
- 当前机器存在 Git 忽略的 `.env` 以供运行；交接、日志和提交时不得回显其内容。
- Nebula 兼容 POC 使用 `./scripts/verify-nebula-poc.sh`；默认会创建并清理独立临时栈，
  不要把 `deployments/nebula-poc/compose.yaml` 当作正式部署配置。
- 正式开发图验收使用 `./scripts/verify-nebula-compose.sh`；脚本拒绝 canonical project/Space，
  通过 `deployments/nebula/verification.override.yaml` 删除应用 service `env_file`，不读取
  `.env`，只控制 project/Space nonce 严格对应的 `askdata-graph002-verify-*` 隔离项目并在退出时
  删除其独立卷。Go integration 还会核对 proxy endpoint 与 Compose ownership；不要把测试
  环境变量手工指向开发共享 Space。
- `nebula-init` 的环境变量凭据只获准用于本地开发；生产必须补齐 HUMAN-005 的 TLS、Secrets、
  副本、容量和备份方案。graphd 不应直接增加 `ports`，本地端口只由 init 后 proxy 暴露。
- 数据库 integration test 使用 `ASKDATA_INTEGRATION_DATABASE_URL`、
  `ASKDATA_INTEGRATION_ADMIN_DATABASE_URL` 和 `ASKDATA_INTEGRATION_WORKER_DATABASE_URL`
  显式开启；画像扫描另使用 `ASKDATA_INTEGRATION_WAREHOUSE_DATABASE_URL` 和
  `ASKDATA_INTEGRATION_WAREHOUSE_ADMIN_DATABASE_URL`，默认单元测试会跳过外部数据库。

## 5. 下一步

1. `GRAPH-002` 已完成。当前再次没有“不需要用户确认或外部人工输入、且可按现有定义完整
   勾选”的后端 TODO；不得以部分实现冒充完成，也不得为了推进清单提前开放 ACTIVE 激活入口。
2. `WEB-001` 已按方案 3 完成并通过设计 QA。`WEB-002` 的 Question API Client/SSE 仍依赖
   `ORCH-005`，当前不得用假 API 或与最终合同不一致的事件模型提前勾选。
3. 下一条 P0 `GRAPH-004` 已满足 GRAPH-002/003，但纸面仍依赖未勾选的 `DB-004`。继续前
   需要用户确认将 `DB-004` 拆成已完成的 READY/投影基础与后置的 ACTIVE 原子切换，或明确
   `GRAPH-004` 只依赖前者；确认后即可恢复 GRAPH-004→005 主链。
4. `REG-006` 的清单边界存在冲突：其完成标准同时要求 DRAFT CRUD 与 publish/activate endpoint，
   但真实 activate 归属 `REL-005`，依赖 DB-007/EVAL-005/DB-008 等门禁；只做 CRUD 或返回
   501 的占位 endpoint 均不能勾选。继续前应把它拆成 DRAFT 管理 API 与后续发布/激活 API，
   当前 `askdata.activate_release` 必须继续不存在。
5. `DB-004` 的 release/READY 基础已完成且足以支撑 Projector，但最终 ACTIVE 原子切换仍等待
   DB-007 + DB-008；在用户确认拆分前不能自行把整个 DB-004 勾选，也不能绕过它启动 GRAPH-004。
6. `ORCH-003` 已满足 ORCH-002，但仍等待 `ORCH-001`；后者依赖尚未完成的 GRAPH-005 和
   QUERY-006，因此当前不能先落一个绕过 typed Tool Registry 的自由 Agent Loop。
7. `NLU-005` 已满足 NLU/SEARCH 依赖，但仍等待 `GRAPH-005`；不得在缺少 GraphPlan
   约束时先做一个退化成逐 mention Top1 的 Binder。
8. `NLU-006` 的 `EVAL-002` 依赖已满足，但仍必须等待 `NLU-005`；校准器应直接消费本次
   TRAIN/VALIDATION 特征与标签，不得重新加入 LLM 自报 confidence。
9. `REG-005` 仍需要 `HUMAN-001`～`HUMAN-003`；`SEARCH-005` 仍需要
   `HUMAN-002`～`HUMAN-004`，当前不能生成正式认证资产或宣称 Recall@K。
10. 2026-08-06 续作审计已逐项复核剩余 51 个未勾选 TODO 及当前代码实物。只有
    `DB-004` 和 `REG-006` 的纸面显式依赖全部完成，但前者的 ACTIVE 原子激活仍
    必须等待 `DB-007` + `DB-008`，后者将 DRAFT CRUD 与属于 `REL-005` 的发布/激活
    API 混在同一验收项；不拆分清单就只能部分实现，不能安全勾选。仓库中仍无
    `000219`/`000220`、`internal/askdata/http/admin.go`、Question/SSE handler、
    Binding/Compiler/Validator 主链实现，未发现“已完整实现但漏勾选”的任务。
11. TODO 还有一个任务 ID 冲突：已完成的“空库启动与历史映射对账修复”和未完成的
    “完整配置模型与生产失败关闭”都使用 `OPS-001`。下一位接手者不应自行改号；
    应与 `DB-004`/`REG-006` 的拆分一并取得用户确认后，再同步修正 TODO 与交接文档。

## 6. 接手验证命令

```sh
git status --short
go test ./internal/askdata/...
./scripts/check-compose.sh
./scripts/verify-nebula-compose.sh
./scripts/verify-nebula-poc.sh
./scripts/dev-services.sh status
ASKDATA_INTEGRATION_DATABASE_URL='postgres://report_app:...@127.0.0.1:5432/...' \
ASKDATA_INTEGRATION_ADMIN_DATABASE_URL='postgres://report_admin:...@127.0.0.1:5432/...' \
ASKDATA_INTEGRATION_WORKER_DATABASE_URL='postgres://report_worker:...@127.0.0.1:5432/...' \
  go test ./internal/askdata/... -count=1
ENV_FILE=.env.example ./scripts/verify-database.sh
ENV_FILE=.env.example ./scripts/verify-warehouse.sh
go test ./...
go vet ./...
./scripts/ci-check.sh
npm --prefix web run lint
npm --prefix web run build
```
