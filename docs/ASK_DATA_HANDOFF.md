# 智能问数项目实施 Handoff

> 实施依据：[ASK_DATA_CODEX_TODO.md](./ASK_DATA_CODEX_TODO.md)
> 架构依据：[ASK_DATA_TECHNICAL_DESIGN.md](./ASK_DATA_TECHNICAL_DESIGN.md)
> 产品基线：[可信智能问数与智能报表一体化平台_最终产品设计方案.md](./可信智能问数与智能报表一体化平台_最终产品设计方案.md)
> 技术基线：[可信智能问数与智能报表一体化平台_最终技术设计文档.md](./可信智能问数与智能报表一体化平台_最终技术设计文档.md)
> 页面门禁：任何新增页面、流程或显著视觉状态的 `WEB-*`、`WEB-RPT-*` 编码开始前，必须先提交页面设计稿并
> 取得用户确认；纯 API Client、类型和测试接线不触发页面门禁，但仍必须满足 TODO 依赖。

## 0. 板块蓝图同步（2026-08-06）

TODO 已按**功能区（板块 B01～B13）**重新组织，见 `ASK_DATA_CODEX_TODO.md` §0 与 §22～§32。交接时必须先读本节。

### 0.1 Wave 与板块的关系

`Wave` 是时间轴，`板块` 是责任面，两者正交。原有 Wave 0～Wave 5 的任务编号、状态和门禁**全部保持不变**，只是额外标注了所属板块；新增任务统一挂在板块下，并追加了 Batch 7～Batch 12。

### 0.2 板块当前状态

| 板块 | 名称 | 状态 | 关键缺口 |
|---|---|---|---|
| B01 | 平台底座与权限 | 部分：`SEC-003` 完成 | `SEC-001`、`SEC-002`、`SEC-004` |
| B02 | 数据接入与元数据 | 仓库基线已有 | 明细取数申请 `DR-001`～`DR-003` |
| B03 | 数仓建模与物化 | 仓库基线已有 | 数据快照版本 `SNAP-001` |
| B04 | 语义资产治理 | 主链完成 | 时间合同、可加性、批量导入、词典、KPI Bundle 全部待建 |
| B05 | 语义发布与投影 | READY 完成，ACTIVE 门禁故意关闭 | `RETAIN-001`、`PROJ-002`、`DB-007/008`、`REL-001`～`006` |
| B06 | 检索与语义图谱 | 主链完成 | `GRAPH-006` 降级矩阵、`SEARCH-005/006` |
| B07 | 问句理解与联合绑定 | 主链完成 | `NLU-007`～`009` 单域约束、Release Pin、超范围分类 |
| B08 | 查询编译与执行 | 主链完成 | `QUERY-007`～`011` TopN/枚举拆分/Bundle/缓存/PARTIAL |
| B09 | 编排与问数 API | Loop 与 API 完成 | `ORCH-006`～`009`、`ANS-001`～`004` 叙述校验 |
| B10 | 问数工作台前端 | 仅 `WEB-001` mock 完成 | `WEB-002`～`013` |
| B11 | 报表引擎与融合 | **全部待建** | `RPT-*` 21 项、`FUSE-*` 5 项、`WEB-RPT-*` 6 项 |
| B12 | 评测、反馈与运营 | 基础完成 | `EVAL-003`～`012`、`FB-*`、`SQ-001` |
| B13 | 运维、可观测与成本 | 仅 `OPS-001` 完成 | `OPS-002`～`006`、`OBS-001`～`003` |

### 0.3 本次新增的任务数量

| 来源 | 新增任务 |
|---|---|
| 产品/技术设计文档第五部分的口径裁定与功能补全 | `TIME-001`～`004`、`ADD-001`～`004`、`IMPORT-001`～`005`、`TERM-001/002`、`KPI-001`、`RETAIN-001`、`SNAP-001`、`PROJ-002`、`GRAPH-006`、`SEARCH-006`、`NLU-007`～`009`、`QUERY-007`～`011`、`ORCH-007`～`009`、`ANS-001`～`004`、`WEB-008`～`013`、`EVAL-008`～`012`、`SQ-001`、`FB-001/002`、`DR-001`～`003`、`OBS-003`、`OPS-006` |
| 原计划完全缺失的报表板块 | `RPT-CONTRACT-001`～`004`、`RPT-DB-001`～`005`、`RPT-001`～`012`、`FUSE-001`～`005`、`WEB-RPT-001`～`006` |

合计新增 **17 个迁移编号（`000225`～`000241`）** 与 **9 个 JSON Schema 合同**，均已在 TODO §22 预留，不得重复占用。

### 0.4 新增的不可违反约束

在原有十条全局约束之外，本次同步追加：

11. 报告数据绑定必须声明 `bindingMode`，`SEMANTIC_IR` 与 `DATASET_FIELD` 二选一；只有前者能反哺问数。
12. 不可加指标（比率、去重）绝不得被 `SUM`/`AVG`；半可加指标缺时间聚合声明则编译失败。
13. 未结束周期默认 `MTD`，策略优先级为 指标级 > 业务域级 > 平台默认；实际区间必须对用户可见。
14. 任何叙述性文字必须通过事实校验；校验失败降级为结构化答案，**不得输出未校验文本**。
15. 报告分享不存在匿名类型，令牌只定位不授权。
16. 被引用的 Semantic Release 不得 `RETIRED`，必须进入 `RETAINED` 保留态。
17. 物化日常刷新只变更 `dataSnapshotVersion`，**不得**使 Release 进入 `STALE`。
18. 密封集按分片轮换，被查看的样本立即退役，绝不得用于修复和调参。

## 1. 当前状态

- 当前 Wave：Wave 0 已全部完成；Wave 1 注册表主链已完成；Wave 2A LLM
  认知协议已完成；Wave 2B 已完成画像合同、有界成员扫描 Worker、generation
  规范化/异常候选接线，以及分类文档、Embedding、混合检索和受约束 LLM 重排主链。
  Wave 2C 已完成 NebulaGraph 服务端/Go Client 兼容 POC、正式开发 Compose/初始化、
  GraphPlan 合同/Adapter、按 release/lease 幂等运行的正式 Projector Worker，以及
  Nebula/认证缓存/PostgreSQL 有界降级 Resolver；`SEARCH-005` 仍依赖未提供的人工黄金集，
  图主链已完成。Wave 3A 已完成确定性问句规范化、原文
  span 回映、时间/比较/
  查询语法解析、安全会话上下文合并、受控 LLM 完整理解与取证计划、联合候选 Binder/
  Bundle Beam Search，以及基于 held-out 验证集的置信度校准与定向澄清；Wave 3A 已完成，
  Wave 3B 已完成 Binding Bundle -> Semantic IR、pinned Semantic Contract Resolver、
  IR -> Dataset Query DSL Adapter、计划 Validator/安全 EXPLAIN、只读问数执行适配，以及规则优先的
  结果核验/异常分析；Wave 3B 已完成。
  Wave 4B 已提前完成无外部依赖的 `EVAL-001` 结果
  规范化与等价判定，以及 `EVAL-002` mention/binding 指标和校准训练/验证输入合同。Wave 3C 的 `DB-005` 问数
  运行/审计控制面、`ORCH-002` Question 状态机/PostgreSQL Store，以及 Wave 4B 的
  `DB-006` 评测集、双人评审、追加式评测运行与结构化反馈迁移均已完成。Wave 4C 的
  `SEC-003` 敏感成员敏感度下限、数据库内 EXACT_ONLY、label-free evidence/LLM 遮罩和
  不披露授权边界也已完成；`ORCH-001` Typed Tool Registry、`ORCH-003` LLM 中枢 Agent Loop、
  `ORCH-004` 审计/预算/幂等和 `ORCH-005` Question API/SSE 已完成，`ORCH-006` Conversation 与
  运行保留策略是下一条编排主线。用户已确认
  `DB-004` 以 release/
  READY 投影水位为完成边界，ACTIVE 原子切换归属 `REL-005` 并继续按门禁保持关闭；当前
  剩余任务的依赖/人工输入阻塞
  见“下一步”。用户已确认 `WEB-001` 方案 3「证据驾驶舱」，受保护 `/ask-data` React
  typed mock、ECharts 图表、关键交互和设计 QA 均已完成；`WEB-002` 的 `ORCH-005` 依赖已满足，
  尚未接入真实 Question API/SSE。
- 已完成：`CONTRACT-001`～`CONTRACT-004`、`BASE-001`、`BASE-002`、
  `DB-001`～`DB-004`、`REG-001`～`REG-004`、`AI-001`～`AI-004`、
  `DIM-001`～`DIM-003`、`SEARCH-001`～`SEARCH-004`、`GRAPH-001`～`GRAPH-003`、
  `NLU-001`～`NLU-004`、`DB-005`、`ORCH-002`、`DB-006`、`EVAL-001`、`EVAL-002`、
  `SEC-003`、`WEB-001`、`GRAPH-004`、`GRAPH-005`、`NLU-005`、`NLU-006`、`QUERY-001`、
  `QUERY-002`、`QUERY-003`、`QUERY-004`、`QUERY-005`、`QUERY-006`、`ORCH-001`、`ORCH-003`、
  `ORCH-004`、`ORCH-005`。
- 发布边界：`DB-004` 已完成 release manifest、四投影、lease、READY 收敛、`release_state`
  和 GraphPlan cache；ACTIVE 激活属于 `REL-005`，必须等待 `DB-007` 评测门禁和 `DB-008`
  双人审批，当前故意不存在。
- 当前已有数仓盘点结果：本地控制库没有“当前 PUBLISHED + ACTIVE”的 DWS/ADS，
  因此正式导入结果为空；使用回滚事务中的历史 DWS 合成发布夹具已验证真实导入链路。
- 生产准确率状态：尚未评测，不得宣称达到 95%。
- 板块视图：2026-08-06 已按功能区把计划重组为 B01～B13 十三个板块（见 §0 与 TODO §0）。
  已完成任务全部集中在 B04～B09 的问数主链；**B11 报表引擎与问数报表融合板块 32 项任务全部未开工**，
  是当前最大的未开工面。口径类（时间合同、指标可加性）、资产建设产能（批量导入）与叙述层校验
  三条闭环也全部待建，已分别落为 `TIME-*`、`ADD-*`、`IMPORT-*`、`ANS-*` 任务。
- 人工业务输入：用户已确认 `HUMAN-005` 的开发部分采用 v3.8.0、单副本共享 Space、持久卷、
  API GUEST/Worker USER 和环境变量开发凭据；生产容量、TLS、备份、多副本及
  `HUMAN-001`～`HUMAN-004`、`HUMAN-006` 仍未提供。
- 2026-08-05 本地运行环境已按用户要求全量重置：控制库、数仓、MinIO、历史
  NebulaGraph/Redis 卷均已删除并重新初始化；随后 seed 已创建 1 个 demo tenant、
  2 个用户，dataset/askdata domain 仍均为 0，没有导入业务语义资产。
- 当前 Compose 运行态健康：API `127.0.0.1:8080`、Web `127.0.0.1:5173`、
  Connector `127.0.0.1:8090`、初始化后 loopback proxy `127.0.0.1:9669`；API/Worker/
  Connection Test Worker、PostgreSQL、Warehouse PostgreSQL、MinIO、Nebula metad/
  storaged/graphd/proxy 均为 healthy。GRAPH-004 共用后端镜像已重建，worker 已强制替换并
  健康启动；graphd 本身没有宿主端口，init 退出容器已移除。

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

### 2.28 GRAPH-004 Release Projector Worker（已完成）

- 新增迁移 `000224_askdata_graph_projection_worker_contract`。`report_worker` 只能调用
  target-scoped tenant listing/claim、lease heartbeat 和 `NEBULA_GRAPH` 快照函数；
  `report_app`、Connection Test Worker 与 PUBLIC 均无执行权。快照必须匹配 tenant、release、
  projection、worker、未过期 lease token 与 content hash，只返回发布内稳定对象/版本 ID、
  version number、成员 ACTIVE/EXPIRED 状态和认证 `MODEL_JOIN` 事实，不返回 label、alias、
  lookup hash、AST、contract JSON 或物理标识。
- `internal/askdata/graph/projector.go` 冻结 `askdata-nebula-projection-v1` 快照与证明合同，规范排序
  后按类型、每批最多 128 个点/边执行固定参数化 mutation；每批前后 heartbeat。`JOINS_TO`
  rank 由不可变 relationship version ID 稳定派生，允许同一模型端点存在多个认证关系版本。
  mutation 成功后必须与本地规范快照的 graph hash、vertex/edge/object count 精确一致才可完成；
  合同/证明错误不可重试，传输故障按 release projection lease 退避，失败不会推进 READY。
- `internal/askdata/graph/postgres_store.go` 在 tenant SYSTEM transaction 内再次核对 release
  manifest hash/object count、projection lease 和每类预期图事实计数。非 `MODEL_JOIN` 的
  relationship 可以合法存在于 release，但不会误计为 `JOINS_TO`；缺少发布端点、未认证对象、
  hash 漂移或实际计数不一致均失败关闭。
- `cmd/worker/main.go` 已接入独立图投影循环，使用 Compose 已限定的 Worker `USER` 账号和
  Space-bound SessionPool；缺少/非法 endpoint、Space、账号或 TLS 布尔配置时进程启动失败。
  新后端镜像已显式构建，worker 已强制替换并保持 healthy。
- 正式隔离 Compose 验收不再只验证持久化重启：首轮 Projector 通过后删除整个测试 Space，
  等待 Meta 收敛，用 init 重建空 schema/账号/角色，再由同一 Projector 重建全部点边并复验
  GraphPlan、tenant/domain/release、GUEST/USER 权限。该过程同时修复了 Nebula 服务重建后
  临时 Space 创建和同名 Space 角色元数据的异步收敛等待。
- 数据库验证新增真实调用：target 隔离 listing/claim、错误/正确 lease heartbeat、陈旧 token
  快照拒绝、包含过期成员的 label-free 快照以及 completion；因此发现并修复了
  `load_release_graph_projection` 输出列与 CTE 未限定列的 PL/pgSQL 歧义。
- 本任务没有新增页面、流程或显著视觉状态，页面确认门禁未触发。

### 2.29 GRAPH-005 Resolver、认证缓存与 PostgreSQL 降级（已完成）

- `internal/askdata/graph/resolver.go` 冻结三段式解析顺序：正常 Nebula、认证 GraphPlan cache、
  PostgreSQL registry fallback。每一段输出都重新执行 `GraphPlan.Validate`，并精确回绑规范化
  request hash、tenant、domain、actor、policy scope 和 release ID/content hash；Nebula 返回零
  metric-model binding 视为结果不足。取消/超时直接返回，不会借降级链绕过调用方预算。
- `PostgresCertifiedPlanCache` 只读取同 question-shape、policy、release 与 graph hash 的未过期
  cache，并要求 release 仍为 READY/ACTIVE/SUPERSEDED、对应 `NEBULA_GRAPH` projection 仍 READY
  且 applied hash 一致；严格 JSON、plan hash 或请求证明任一不一致即拒绝。缓存写权限继续只属于
  Worker，本任务未开放 API 任意写入。
- `PostgresFallback` 必须持有与请求 actor/domain 一致的真实 AccessContext，并在 tenant RLS
  transaction 中验证 release manifest hash/object count，以及 POSTGRES_REGISTRY、NEBULA_GRAPH
  双 READY 投影。它只读取当前 release 内认证指标、模型、维度、成员归属和 `MODEL_JOIN` 的
  label-free 稳定事实，不读取 member key/label、alias、AST、物理表列或 contract JSON。
- 关系遍历由 Go 实现确定性、有界的简单路径搜索：最多 32 条关系、4 跳、32 条路径、4096 次
  展开，只能使用请求内模型作为中间点；方向按冻结 VID 顺序与 Nebula Adapter 保持一致，并由
  `NewJoinPath`/`NewGraphPlan` 重算基数、fanout 风险和稳定 hash。超限、缺失证明或无可验证结果
  均失败关闭，绝不落到未验证 Top 1。
- 单元测试覆盖 Nebula 正常、真实 Client 传输故障、结果不足、精确缓存命中、缓存篡改、scope
  重绑、PostgreSQL 回退、全链失败与 context cancellation；真实 PostgreSQL/RLS 回滚夹具以
  `report_app` 读取完整发布，证明关系库输出与标准 `GraphPlan` 完全一致且不泄漏敏感成员材料，
  再将图投影置为 FAILED，验证缓存和关系回退同时拒绝。
- 本任务没有新增页面、流程或显著视觉状态，页面确认门禁未触发。

### 2.30 NLU-005 Joint Binder 与 Bundle Beam Search（已完成）

- 新增 `internal/askdata/binding/{binder,beam,score}.go`。绑定请求同时携带 NLU-004 的原始请求/
  结果和 GRAPH-005 的 PlanRequest/Resolution；执行前完整重放两条 hash 链，并要求 policy scope、
  domain 与 release 精确一致。当前和继承 mention 以 `CURRENT`/`INHERITED + kind + index` 分开，
  不会把上一轮 span 伪造成本轮证据。
- 每个 mention 的候选必须属于同一 release-pinned GraphRequest，并携带候选集、检索、规则、
  Semantic Contract 与 Data Quality 证据。规则 `BLOCK` 是硬门禁，优先于 reviewer rank；成员
  只能走确定性精确候选，不能进入 LLM label-bearing rerank，并且必须由 GraphPlan 证明 ACTIVE
  状态、父维度归属和与已选模型兼容。
- Beam 按指标 → 维度 → 成员顺序联合展开：指标同时选择 MetricVersion/Model；维度按 GROUP_BY/
  FILTER/TIME/SORT 角色保留；成员自动补入已证明的隐含 FILTER 维度；多模型组合必须被一条
  allowed 认证简单路径覆盖，fanout block 路径不会生成 bundle。默认 beam width 64、Top 10，
  硬上限分别为 256/30。
- 排序分数只使用确定性系统特征：检索、exact、reviewer rank、rule、quality、graph、cost 与
  Join risk penalty；没有 LLM 自报 confidence。每个 Top Bundle 保存完整稳定 ID 绑定、时间、
  GraphPath/降级来源、分项分数、规范 evidence 与 bundle hash；零候选或无可行组合生成可审计
  `NoMatch`。严格 JSON 解码后还会对原始请求重算整个结果，适合作为 `BINDING_BUNDLE` 工件。
- 合成难例覆盖独立 Top 1 互不兼容、规则拦截 LLM 第一名、过期成员、成员父维度、fanout block/
  认证预聚合、空候选、续问 origin/span、输入顺序稳定、未知 JSON 字段与全链篡改。
- 本任务没有新增页面、流程或显著视觉状态，页面确认门禁未触发。

### 2.31 NLU-006 校准置信度与定向澄清（已完成）

- 新增 `internal/askdata/binding/{calibrator,clarification}.go`。生产拟合入口先以源 cases 重放
  EVAL-002 report，训练/验证样本按稳定 ID 归一化并拒绝交叉泄漏、重复、非法范围、未知角色/
  复杂度/歧义和缺少正负标签；固定参数 logistic 拟合后，只在 held-out 验证集上执行 PAV
  isotonic 校准，模型、数据集与结果均保存稳定 hash。
- 校准特征只来自确定性绑定产物：candidate total score、Top margin、exact、lexical、vector、
  graph、rule 和 rank；不读取或接受 LLM 自报 confidence。DIRECT 阈值必须同时满足验证集最小
  样本、precision、confidence 与 margin，找不到合格阈值时门禁默认关闭。
- 决策前完整重放 BindingResult，并重新计算每个 Top bundle 的特征和校准概率。通过 held-out
  门禁的高置信唯一候选进入 `DIRECT`；多候选低置信只生成 2～3 个与真实 bundle 一一对应的
  `CLARIFY` 选项；单候选证据不足返回 `EVIDENCE_REQUIRED`，NoMatch 返回 `NO_MATCH`，不会为了
  满足选项数量凭空构造候选。
- 澄清展示只接收经过净化的稳定业务标签、差异和该 bundle 已持有的 evidence，拒绝物理 SQL/
  表列文本、跨 bundle evidence 和伪造 bundle；问题、option ID、semantic version ID、模型证据、
  typed `ToolRequestClarification` 参数及 decision hash 均可确定性重放。
- 合成校准 fixture 覆盖输入顺序稳定、held-out gate、单调校准、训练/验证泄漏、直接执行、低
  margin、验证门禁关闭、单/零候选、伪造选项、敏感物理文本、跨候选证据、未知 JSON 字段与
  hash 篡改。任务没有数据库、外部模型或页面依赖，页面确认门禁未触发。

### 2.32 QUERY-001 Binding Bundle -> Semantic IR（已完成）

- 新增 `internal/askdata/ir/{builder,validation}.go` 和完整合成测试。`BuildRequest` 同时携带原始
  Binding Request/Result 与选中 bundle hash，构建前重放整个 NLU/Graph/候选绑定链；选中 bundle
  后再次校验 metric-model、dimension-model、ACTIVE member ownership 和 FILTER 维度归属，
  NoMatch、篡改或不在 Top bundles 内的 hash 均不能进入 IR。
- 生成的 Semantic IR 只含 pinned release/hash 和稳定版本 ID。指标 alias 由 metric version ID
  的 SHA-256 确定性生成，不引入未绑定标签；成员默认/集合与正/负操作符合并为 EQUALS、IN、
  NOT_EQUALS、NOT_IN，多个值自动使用集合操作。无成员的 FILTER 维度失败关闭；SORT 维度按自然
  粒度投影到 groupBy，以满足冻结 IR 的排序引用约束。
- 当前轮时间只能使用 Understanding Request 中已重放的 deterministic `ResolvedTime`；继承时间
  因 ContextSnapshot 有意不保存日期边界，必须由上一轮规则工件提供 `InheritedTimeResolution`，
  并同时匹配 previous snapshot hash、原 question span、TimeUnderstanding、RULE evidence 与
  resolution hash，禁止按当前日期重新解释“今年/上月”。time dimension 必须唯一，时间区间保持
  `[start,endExclusive)`；comparison 仅映射同比/环比/较上期且必须有时间，默认 limit 500、上限
  10000。
- `BuildArtifact` 保存 policy scope、domain、binding result、bundle、规范 IR、evidence 和 artifact
  hash；严格解码会从原请求重新构造后做全值比较。冻结的 Semantic IR v1 只有一个
  `modelVersionId`，多模型/GraphPath 当前明确失败关闭，不能静默丢失认证 join 语义；需要通过
  后续版本化 IR 合同升级后才能放开。
- 为使 `ir` 按设计消费 `binding` 且不形成包循环，纯 Semantic IR 合同下沉至
  `internal/askdata/ircontract`，`ir` 继续以类型别名和同名函数保持现有 Go/JSON API；共享校准
  feature/example 合同同样下沉到 `internal/askdata/calibration`，EVAL-002 类型保持别名兼容，
  `FitCalibratorFromEvaluation` 留在 evaluation 层先重放 report/cases 再调用 binding calibrator。
- 合成集成测试覆盖本轮时间、同比、成员过滤、分组、指标排序与 Top 5 的完整构建，以及 candidate
  set 顺序稳定、严格解码、binding/artifact 篡改；单元负测覆盖多模型、过期成员、无成员过滤、
  FILTER 集合归一、SORT 投影和继承时间 proof 篡改。本任务没有数据库、外部模型或页面依赖。

### 2.33 QUERY-002 Semantic Contract Resolver（已完成）

- 新增 `internal/askdata/compiler/resolver.go`：`ResolveRequest` 必须携带 QUERY-001 的原请求和
  Artifact，解析前完整重放 Binding -> IR；`ContractLookup` 固定 policy scope、domain、release
  ID/hash、IR hash 及精确 model/metric/dimension/member/relationship 版本集合，不读取或接受“当前
  release”指针。FILTER 的 member-dimension 边和 time dimension 也进入规范 `Resolution` 与 hash，
  防止多个维度存在时把合法成员重新绑定到错误过滤条件。
- `Resolution` 保存模型、字段、指标/measure AST、维度、label-free 成员、GraphPath、关系和物化
  快照合同。字段 ID/code/role/type、维度字段兼容、主时间字段、metric/measure 枚举、成员敏感度
  下限、对象精确集合和 canonical AST 均独立失败关闭；GraphPath 先重放 path ID、hop 连续性、
  方向和风险摘要，再逐步匹配 release 中的 join type/cardinality/fanout/model pair。
- 新增 `resolver_postgres.go`：生产 Store 在一个 `REPEATABLE READ + READ ONLY` USER/RLS 事务内读取
  pinned release，验证 manifest hash/count，以及 `POSTGRES_REGISTRY`、
  `EXECUTION_SEMANTIC_LAYER` 两个 READY 投影的 expected/applied hash 和 object count。模型、指标、
  measure、维度、成员、关系必须是该 release 的精确 CERTIFIED 对象，源 content hash 与 manifest
  一致；代码从不查询 `release_state` 或 ACTIVE release。
- 模型源必须仍是 current PUBLISHED DWS/ADS，且同一 dataset/version/materialization 的 layer、
  Dataset DSL hash、schema hash、published view、snapshot 和非空 row count 全部一致，物化必须
  ACTIVE。Store 重新执行 `dataset.Prepare`，只从通过完整 DSL 校验的稳定字段生成 field contract；
  stale、retired、缺失或不一致源统一拒绝。
- 敏感成员公开/持久化边界按技术设计保持 label-free：Resolution JSON 只含 member/dimension
  version ID、content hash 和 sensitivity，不包含 key、label、alias、lookup hash 或参数明文；
  PostgreSQL 读出的规范 member key 只作为 compiler 包内不可序列化、不参与 hash 的临时编译参数，
  且 Resolution 去除该参数后仍可重放校验。MEMBER manifest 还必须是四字段、label-free、alias
  version ID 有界排序的 v1 合同。
- Measure contract 同时固定逻辑 `measure_id` 与不可变 Measure Version ID；后续公式 AST 无论使用
  `measureId` 还是 `measureVersionId` 都必须归一到 release 中同一精确版本，不能按 code 或当前版本猜测。
- 新增小型、可重放的 `testfixture.SemanticMetricBuildRequest` 和 resolver 单元/集成测试。单元覆盖
  current release 切换后 pinned 结果/hash 不变、源顺序规范化、stale materialization、访问错配、
  member/time 归属、缺失对象、关系不一致、标签泄露和 AST/artifact 篡改；PostgreSQL 用例在显式
  `ASKDATA_INTEGRATION_DATABASE_URL`/admin URL 下复用现有 READY fixture，当前无 URL 时按仓库惯例
  跳过。本任务没有新增页面，未触发页面设计确认门禁。

### 2.34 QUERY-003 IR -> Dataset Query DSL Adapter（已完成）

- 新增 `internal/askdata/compiler/adapter.go`：`AdaptRequest` 携带 QUERY-002 原 `ResolveRequest` 与
  `Resolution`，先完整重放 Binding -> IR，再逐项匹配 scope/domain/release、model、IR/build/
  graph/resolution hash。Semantic IR v1 仍只允许单模型；存在非空 GraphPath/relationship 时明确失败，
  不会把认证 Join 路径静默丢弃。
- 生成临时、不可持久修改的 Dataset Document：维度输出复用模型稳定 field ID，指标输出 ID 从
  metric version ID 确定性派生；source node 只固定解析出的 Dataset Version，物理 schema/view 和
  column/type 白名单只来自 ACTIVE materialization。groupBy 日期粒度转换为受控 `DATE_TRUNC`/`CAST`，
  sort 只引用已投影稳定 ID，正式行数上限不超过 IR/DSL `resultLimit`。
- 新增 `adapter_ast.go` 严格解析 release 中的 Metric/Measure AST。Measure `FIELD_REF` 只能指向模型
  字段，非 COUNT 聚合必须可证明为数值表达式；Metric `MEASURE_REF` 支持逻辑 ID 和 version ID，但
  均归一到声明的精确 Measure Version，引用集合必须与 metric dependency 完全一致。有限算术、
  cast/round/coalesce、比较、集合与布尔节点映射为 Dataset Expression；未知字段、未知类型、额外
  JSON 字段、跨 metric 依赖和不受支持形状全部失败关闭。
- Metric default filter 不作为全局 WHERE 误伤其他指标，而是在各自 Measure 聚合参数内转换为
  `CASE WHEN`；IR 成员过滤只生成稳定 `PARAM_REF` 和参数类型/基数，规范 member key 仅存在于 live
  编译参数。时间使用 `[start,endExclusive)`；DATE/DATETIME 边界与 IANA timezone 固定，timezone
  留给 QUERY-005 只读执行会话使用。
- 普通查询生成一个 `CURRENT` 计划；同比、环比、较上期分别按日历年、日历月或等长前期生成
  `CURRENT`/`BASELINE` 两个同构计划。闰日/月末使用 clamp，不依赖机器当前日期；整体 QueryArtifact
  绑定比较合同和原 IR hash，未把比较请求静默降级为普通查询。
- `QueryArtifact` 只序列化 Dataset DSL、参数 shape/cardinality、受信物化白名单及 DSL、logical、
  compiled、aggregate plan hash；SQL、Args、member key 与时间参数值都在 JSON/hash/普通审计之外。
  公开 JSON 可用 dummy typed 参数重放 SQL shape/hash；仅 live plan 的 `CompiledQuery()` 能取得隔离
  拷贝，反序列化工件必须在执行前重新从 pinned registry 注入参数。
- `internal/querycompiler` 最小新增显式 `LimitKind`：既有调用默认 `PREVIEW`，AskData 正式计划显式
  使用 `RESULT` 且仍受 DSL `resultLimit` 限制；`CompiledQuery.PlanHash` 绑定方言、SQL placeholder
  shape、limit、参数合同和排序后的物理白名单，但排除所有运行值。
- Golden 测试固定 PostgreSQL SQL/Args，并覆盖逻辑/版本 Measure 引用、指标级默认过滤、月粒度、
  10000 行正式上限、CURRENT/BASELINE 参数移位、闰日 clamp、运行值不改变 compiled hash、公开 JSON
  无 SQL/Args/member/date 泄漏、物理 view 篡改、未知 AST/Measure 和 PREVIEW 越界失败。本任务没有
  新增页面，未触发页面设计确认门禁。

### 2.35 QUERY-004 计划 Validator 与安全 EXPLAIN（已完成）

- 新增 `internal/askdata/validator/plan.go`：只接受保留 in-process compiled query 的 live
  `QueryArtifact`，先验证 QUERY-003 全链 hash 和 compiled plan hash，再执行独立 SQL tokenizer/static
  gate。仅允许单条 SELECT/非递归 CTE，关系必须精确命中 plan 固定的 published schema/view；受控
  aggregate/date/cast 函数外的调用、DDL/DML/COPY、锁/会话语句、注释、分号、dollar quote、未知
  relation 和 `EXPLAIN`/`ANALYZE` 均以稳定 rejection code 失败关闭。
- 新增 `explain.go`：`PostgresExplainer` 校验 AccessContext 与 scope/domain，在数仓 reader pool 上开启
  `REPEATABLE READ + READ ONLY` USER/RLS 事务，事务内设置 tenant/user/domain、IANA timezone、
  `statement_timeout` 和 `lock_timeout`，且只拼接固定 `EXPLAIN (FORMAT JSON)` 前缀；不会运行
  `EXPLAIN ANALYZE`，也不会执行原 SELECT。
- 原始 EXPLAIN JSON 只在当前调用内解析并丢弃。公开 `ValidationArtifact` 仅保存 root node/cost/rows、
  plan node、Seq Scan、Join rows/fanout 数值摘要和 summary/validation hash，不包含 SQL、Args、成员/
  时间值或物理标识符。限制配置只能收紧，不能超过平台 ceiling；root/result/node rows、total cost、
  plan nodes/depth、Seq Scan rows、Join rows/fanout 都会在执行前拒绝。
- Artifact 重放会再次执行全部数值不变量，而不只检查 hash；因此篡改摘要后自行重算公开 hash 也不能
  把超阈值计划伪装为已验证。测试覆盖正常 SELECT/CTE、DATE_TRUNC/CAST/CASE、SQL 注入形态、未知 UDF/
  relation、高成本、大扫描、Join 行数/fanout、超上限配置、live 参数丢失和 JSON 无泄漏。本任务没有
  新增页面，未触发页面设计确认门禁。

### 2.36 QUERY-005 问数执行适配（已完成）

- 新增 `internal/askdata/validator/executor.go`：`ExecutionRequest` 必须同时携带同一 live
  `QueryArtifact` 与匹配的 `ValidationArtifact`，逐计划重查 role、query/compiled hash、MaxRows、
  static SQL gate 和 live Args；公开 JSON 重放后因没有参数值而明确不可执行。
- 新增 `queryruntime.SemanticMaterializationRevalidator`：控制库在 read-only repeatable-read USER/RLS
  快照内只按 compiler 固定的 materialization ID 复核 ACTIVE DWS/ADS、current PUBLISHED dataset
  version 和精确 published view，不跟随当前 semantic release。数仓执行则让普通/对比双计划共享一个
  `REPEATABLE READ + READ ONLY` reader 事务，设置 tenant/user/domain、IANA timezone、statement/
  lock timeout；运行时验证 reader 非 superuser/createrole/bypassRLS/继承角色，且发布 relation 是
  SELECT-only view，没有 INSERT/UPDATE/DELETE/TRUNCATE 权限。
- `ExecutionResult` 的行仅存在于进程内私有字段，`Rows(role)` 只返回深拷贝。公开
  `ExecutionArtifact` 不含行，只记录 scope、plan/validation hash、列名+PostgreSQL type OID、每计划
  MaxRows/row count/result hash 和总 result hash。typed canonical hash 区分 NULL/布尔/整数/浮点/
  DECIMAL/日期时间/字节/字符串；DECIMAL 与时间不经 float64 损失精度，NaN/Inf/未知值失败关闭。
- `queryruntime` 新增独立 `SEMANTIC_QUESTION` 审计合同和 Auditor 接口；Start 只含 plan/validation
  hash、EXPLAIN 最大成本、计划数、行/超时预算，Finish 只含 result hash、行数、耗时、状态和稳定
  error code。Executor 强制 Auditor 存在，终态审计失败会丢弃已经成功的结果，SQL、Args、成员/时间
  参数和结果行均没有审计字段可写。
- 行数与总字节均有硬上限；run ID 注册进程内 cancel handle，并校验发起取消的 AccessContext actor/
  domain。caller deadline、整体 25 秒 ceiling、DSL timeout、PostgreSQL statement timeout 和 pgx
  cancellation 共同收口；CURRENT/BASELINE 仍是同一一致性快照。本任务没有页面或数据库迁移，未触发
  页面设计确认门禁。

### 2.37 QUERY-006 规则 + LLM 结果核验与异常分析（已完成）

- 新增 `internal/askdata/validator/result.go`：`ResultRuleRequest` 必须同时持有匹配的 Semantic IR、
  live QueryArtifact、QUERY-005 进程内结果和可信质量证据；先重放 IR/query/result/scope/hash，再按
  Dataset 输出合同核对列名和 PostgreSQL OID。DECIMAL 只接受 exact NUMERIC，整数、布尔、日期、
  DATETIME 和字符串也只能接受相容 OID，不允许用 float8 冒充精确小数。
- 确定性规则检查 result limit、schema/type、重复行、输出 key 唯一性、key/非空字段 NULL、metric-only
  fanout、DIVIDE 的零分母证据、IR 时间覆盖、数据新鲜度、质量总状态/逐规则，以及 CURRENT/BASELINE
  同构 shape。所有规则和证据引用进入稳定 `RuleArtifact` hash，BLOCKING 失败不能被后续模型修改。
- 空结果按每个计划角色核对成员是否存在、请求时段是否确无数据、是否由权限裁剪；只有“成员存在 +
  时段无数据 + 非权限裁剪”才设置 `NoDataConfirmed`。空结果、fanout/重复 key、覆盖/质量问题或异常
  对比趋势都要求先执行 `ANOMALY_ANALYSIS`，不会直接把空集表述成业务无数据。
- 新增 `anomaly.go`：模型仅接收原问题 fact、Semantic IR/plan hash、列级 count/distinct、指标
  non-null/null/min/max/sum、CURRENT/BASELINE 聚合趋势、规则检查、质量和 policy evidence；完整结果行、
  SQL、Args 和物理查询不进入 PromptFact。每个 Action 先通过 cognition schema，再校验证据必须精确
  来自本轮 prompt；发明、错 hash 或跨轮混用 evidence 均失败关闭。
- `RESULT_VERIFICATION` 必须包含 `RESULT_ANSWERS_QUESTION` 检查；模型给出 PASS 时全部模型检查也必须
  通过。最终 verdict 由治理层合并：确定性规则失败时只能 RETRY/BLOCK，模型 PASS 仅记录
  `RuleOverridePrevented=true`，不能覆盖规则。最终工件深拷贝认知结论，绑定 plan/result/rule/evidence
  hash，并交叉验证摘要、异常/核验引用和最终 verdict。本任务没有页面、迁移或真实外部模型调用，
  未触发页面设计确认门禁。

### 2.38 ORCH-001 Typed Tool Registry（已完成）

- 新增 `internal/askdata/toolhost/registry.go`、`tools_catalog.go`：Registry 必须一次注入架构文档全部
  14 个编译期 typed handler，缺少、重复或未知工具均不能构造；目录不存在通用 SQL/nGQL/数据库工具。
  `Definitions()` 返回按工具名稳定排序的隔离副本，每个 Definition 固定 argument/result JSON Schema、
  schema hash、Permission、BudgetCharge、timeout、最大结果字节和整体 definition hash，可做重放审计。
- `AuthorizationContext` 必须持有有效 PolicyScope、scope 内单一 domain 和无重复显式 Permission；Call
  在执行前再次通过封闭 `ToolArguments.ValidateFor`，且 release 必须与 scope 的 pinned release 完全一致，
  参数中的 domain 也只能等于当前 run domain。权限、release、domain 或预算失败返回稳定 REJECTED，
  不触达 handler、不扣调用预算。
- 每个工具固定一次 tool call 扣费；`execute_query_plan` 消耗一次 formal query，
  `compare_candidate_results` 消耗两次 formal query，`probe_join_cardinality` 与
  `execute_validation_query` 各消耗一次 validation query。`AvailableTools` 同时按 permission 和剩余
  8/2/3 预算过滤后给认知 Prompt 使用；每个工具还有 1～20 秒独立 timeout，caller cancel/timeout 与
  handler 稳定错误被规范化，原始内部错误不会回传模型。
- 14 个 Result 都有独立 Go 合同和 validator。Host 对结果做 evidence ID/ref 全量回绑、规范 JSON、
  result hash、大小限制和递归 unsafe-key 扫描；`rows/sql/ngql/args/parameters/prompt/messages/reasoning/
  credential` 等字段即使绕过 Registry 直接构造 Tool Message 也会被 `Response.Validate` 拒绝。正式查询
  结果只允许 hash、verdict、行/列/metric 聚合摘要，不可返回完整行；Semantic Contract formula 只暴露
  hash、操作符和稳定版本引用，不可返回物理表达式。
- 维度值 sanitizer 会对 `Sensitive=true` 的 member 清空 display label/aliases，保留稳定 ID、层级 ID 和
  label-free evidence；其他工具也只接收稳定对象 ID、Semantic IR、plan hash、质量/图/验证摘要。最终
  `Execution` 保存 definition hash、实际 BudgetCharge、耗时、timeout 状态和已验证 Response，供
  ORCH-003/004 驱动与审计。本任务没有页面、迁移或外部调用，未触发页面设计确认门禁。

### 2.39 ORCH-003 LLM 中枢 Agent Loop（已完成）

- 新增 `internal/askdata/orchestrator/loop.go`：外层 Loop 以一个 Question cognition stage 为边界，
  首轮由 `BuildMessages` 只装入已规范化的 PromptFact 和精确 EvidenceRef；即使规则已能走 fast path，
  仍必须完成至少一次 Cognition 裁决。模型只能返回封闭 Action 或选择当前阶段允许的 typed tool，不能
  直接执行 SQL、访问 Registry handler 或改写 Question 状态。
- Tool 列表先经 Registry 按 permission/剩余预算过滤，再与阶段白名单求交；每次调用前重新计算剩余
  8 tool/2 formal/3 validation 预算。Loop 对 action stage/hash、AI request/model/token 审计摘要、已见
  action/call、evidence allowlist、Tool response callId/tool 和实际收费二次复核，任何发明证据、重放、
  confused-deputy 响应或超预算收费都失败关闭。
- 成功工具结果必须已通过 Tool Host 的 typed/sanitized Response 合同；Loop 只把结构化 action 与工具
  脱敏结果追加到进程内 transcript，登记精确 evidence ref 后才允许下一轮引用，不保存隐藏思维链。
  Tool REJECTED/FAILED、无进展、冲突 evidence ID 和超过 512 KiB transcript 都立即停止。
- 使用 Question Run 的累计 BudgetUsage 执行最多 4 LLM、8 tools、2 formal、3 validation、16 step 和
  25 秒总时限；恢复调用可携带已见 action hash/call ID。caller cancel 原样传播，总时限触发稳定 timeout
  并标记预算 exhausted；复杂纠错在第 4 次模型调用后不会进入第 5 轮。本任务没有页面、迁移、数据库/
  数仓写入或真实外部模型调用，未触发页面设计确认门禁。

### 2.40 ORCH-004 审计、预算和幂等（已完成）

- 新增 `internal/askdata/orchestrator/audit.go`：`CheckpointLoop` 复用 ORCH-002 Store 和 `000217`
  既有表/触发器，以稳定 checkpoint ID/hash 执行一个 actor-scoped 原子事务。事务先锁定并验证完整
  ReplaySnapshot，再按认知顺序写入每个 `LLM_DECISION`、typed tool replay artifact、`tool_calls`、
  `TOOL_RESULT`、绝对 `BUDGET_UPDATED`、稳定 `ERROR`，最后更新 run 并追加状态事件；任一事实或
  外键失败时整批回滚，不存在仅扣预算或仅保存工具结果的半提交。
- 模型事件保存 action hash/type、AI request UUID、evidence IDs、provider model、attempt/token/cost/
  redaction、耗时，并由数据库外键再次约束 actor/tenant/`SEMANTIC_QUESTION` purpose。工具审计保存
  参数 hash、完整 sanitized outcome hash、call hash、definition hash、实际 charge、evidence IDs、
  duration/timeout/error code；tenant/domain/actor/policy scope/release/run version 均来自锁定 run，调用者
  无法另传覆盖。所有详情继续通过 audit forbidden-key 门禁，不保存问句、prompt、reasoning、SQL、Args
  或完整结果行。
- 每个完成工具额外保存 `tool-execution-replay-v1` EVIDENCE 工件，内容仅为 Tool Host 已验证的 typed
  output/evidence 和稳定执行摘要。Replay 会把工件与 `tool_calls` 的 request/result/call hash、预算、
  状态和证据做交叉校验；`ReplayToolExecution` 可重建成功的脱敏 typed response。精确 checkpoint 重试
  返回原 snapshot，checkpoint 内容碰撞或 stale version 失败；`BindReplayGuards` 将已见 action/call
  注入 Loop，恢复后重复 action 在 Tool Host 执行前即触发 no-progress。
- 新增 `budget.go`：只有实际达到 step/LLM/tool/formal/validation/time ceiling，或命中受控 transcript
  ceiling，才能构造 exhausted 终态。语义阶段在调用方明确选择时可生成证据绑定的 CLARIFICATION；
  context/硬时限等不能靠用户消歧的边界统一 BLOCK。completion artifact、预算/错误事件和终态迁移同
  事务提交，`budget_exhausted` 不会停留在非终态。本任务无页面或数据库迁移，未触发页面设计确认门禁。

### 2.41 ORCH-005 Question API 与 SSE（已完成）

- 新增 `internal/askdata/http/question.go`，正式提供 `POST /api/v1/questions`、
  `GET /api/v1/questions/{runId}` 和 `POST /api/v1/questions/{runId}/clarifications`；
  `cmd/api/main.go` 已装配同一个受 `auth.RequireAccessToken` 保护的 handler。认证层会实时复核 token、
  session 和选中业务域，HTTP 层再要求 claims actor 与数据库 access context 精确一致。
- 创建请求采用 16 KiB 严格 JSON、4096 rune 问句和必填 `Idempotency-Key`。HTTP 层规范化问句后只把
  域分离 SHA-256 交给 Backend；原问句、幂等明文不会进入 Store、响应或日志。未传 conversation 时用
  tenant/actor/domain/幂等 hash 确定性生成 UUID，使丢失响应后的重试仍命中同一 CreateRun 事实。
- `PostgresService` 在 USER/RLS 事务内解析当前 ACTIVE release 和用户真实 ACTIVE role IDs，构造规范
  `PolicyScope` 后复用 ORCH-002 `CreateRun/Resume`。读取历史 run 时先由 actor/domain RLS 取得其 pinned
  release，而不是错误跟随新 ACTIVE release；当前 role 集变化仍会在 Store 的 policy hash 核对中失败。
- 澄清 API 只接收 completion artifact 中公开 allowlist 的稳定 `optionId`；自由文本/未知字段被严格 JSON
  拒绝。合法选项创建相同 conversation、parent run、actor/domain/policy/release 的 child run；过期选项、
  非澄清终态、scope 改变或 release 已无法继续时失败关闭。
- 新增 `sse.go`：数据库 `event_index` 直接作为 SSE `id`，严格支持 `Last-Event-ID`、连续事件检查、轮询
  去重、15 秒 heartbeat、终态关闭和重连。每个公开 event 最大 16 KiB，只包含 event UUID/index、run
  version、state/type/stage/status/code、action/artifact hash、evidence IDs、duration 和 createdAt；不投影
  `Details`、AI request、Tool 原始结果、prompt、SQL、参数、result rows 或隐藏 reasoning。
- HTTP 测试覆盖鉴权前置、严格 body/header、确定性 hash/conversation、幂等冲突映射、completion 白名单、
  澄清自由文本拒绝、SSE Last-Event-ID/游标越界/断线续传/去重/超限 payload 和恶意审计详情泄漏。
  真实 app/admin PostgreSQL 回滚 fixture 另验证 ACTIVE release、实际 role scope 和同域跨 actor RLS。
  本任务未新增页面、迁移或真实模型调用，未触发页面设计确认门禁。

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

2026-08-06 `GRAPH-004` 验证：

```sh
gofmt -w cmd/worker/main.go internal/askdata/graph/*.go
bash -n deployments/nebula/init.sh deployments/nebula/verify.sh \
  scripts/migrate.sh scripts/verify-database.sh scripts/verify-nebula-compose.sh
ENV_FILE=.env.example ./scripts/migrate.sh
ENV_FILE=.env.example ./scripts/verify-database.sh
./scripts/verify-nebula-compose.sh
go test ./internal/askdata/... -count=1
go test ./... -count=1
go vet ./...
./scripts/check-compose.sh
./scripts/ci-check.sh
ENV_FILE=.env.example ./scripts/verify-warehouse.sh
docker compose --env-file .env.example --env-file .env build api
docker compose --env-file .env.example --env-file .env \
  up -d --no-deps --force-recreate --wait worker
./scripts/dev-services.sh status
git diff --check
```

- 全部通过。正式 Nebula 验收真实删除并重建 Space；第二轮 Projector 在空图上恢复 6 个点、
  5 条边，证明与规范快照一致，随后 drift 负测仍通过。临时项目、网络和卷由 trap 清理。
- 数据库验证真实执行 `NEBULA_GRAPH` target listing/claim、heartbeat、快照和 completion；错误
  lease token 失败关闭，快照只含稳定 ID/版本/状态/关系事实。静态权限检查确认只有 Worker
  可执行新增函数。
- 全量 Go、vet、Compose、CI 静态检查、数据库和数仓验证通过；新 worker 容器健康，日志只有
  正常启动记录。本轮没有新增页面，也未读取或输出 `.env` 中的 secret。

2026-08-06 `GRAPH-005` 验证：

```sh
gofmt -w internal/askdata/graph
go test ./internal/askdata/graph -count=1
ASKDATA_INTEGRATION_ADMIN_DATABASE_URL='postgres://<admin>@127.0.0.1:<port>/<db>?sslmode=disable' \
ASKDATA_INTEGRATION_APP_ROLE='<app-role>' \
  go test ./internal/askdata/graph \
  -run '^TestPostgresFallbackAndCertifiedCacheAgainstRuntimeRole$' -count=1 -v
go test ./... -count=1
go vet ./...
ENV_FILE=.env.example ./scripts/verify-database.sh
ENV_FILE=.env.example ./scripts/verify-warehouse.sh
./scripts/check-compose.sh
./scripts/ci-check.sh
git diff --check
```

- 全部通过。故障注入覆盖公开 Nebula Client 的传输失败与结构完整但缺少指标绑定的结果；精确
  认证缓存命中后保留降级原因，缓存篡改、scope/release/request 重绑与全链失败均关闭。
- PostgreSQL 集成夹具在同一回滚事务内构造 READY release、四投影和认证关系，以真实
  `report_app` RLS 权限读取；回退结果与标准构造器生成的 `GraphPlan` 深度一致，序列化输出不含
  成员 key/label。将 NEBULA_GRAPH 投影置为 FAILED 后，缓存和关系回退都拒绝重放。
- 全仓 Go test/vet、数据库/数仓动态检查、Compose/CI 静态门禁通过。本任务没有新增页面，也未
  读取或输出 `.env` 中的 secret。

2026-08-06 `NLU-005` 验证：

```sh
gofmt -w internal/askdata/binding
go test ./internal/askdata/binding -count=1 -v
go test -race ./internal/askdata/binding -count=1
go test ./internal/askdata/... -count=1
go vet ./internal/askdata/...
go test ./... -count=1
go vet ./...
./scripts/ci-check.sh
git diff --check
```

- 全部通过，race 未发现共享状态问题。合成测试证明独立候选 Top 1 不兼容时只保留整体可行
  bundle；规则 block、过期成员、错误父维度、无 allowed GraphPath、跨 release/graph request
  候选以及 understanding/GraphPlan/result hash 篡改均不能形成可执行组合。
- 续问夹具证明继承指标仍引用上一轮 mention origin/span，本轮分组维度保持 CURRENT；候选顺序
  变化不改变 Top bundle 或结果 hash。空候选生成稳定 NoMatch，严格解码拒绝未知物理查询字段。
- 本任务没有数据库、Compose 或页面变更，未调用外部模型，也未读取或输出 API key。

2026-08-06 `NLU-006` 验证：

```sh
gofmt -w internal/askdata/binding
go test ./internal/askdata/binding -count=1 -v
go test -race ./internal/askdata/binding -count=1
go test ./internal/askdata/... -count=1
go vet ./internal/askdata/...
go test ./... -count=1
go vet ./...
./scripts/ci-check.sh
git diff --check
```

- 全部通过，race 未发现共享状态问题。校准模型在输入顺序变化时保持同一 hash，并证明更强
  特征的样本校准概率不低于弱特征样本；训练/验证身份泄漏、单标签或越界输入均失败关闭。
- held-out 验证满足 precision/confidence/margin 后才允许 DIRECT；低 margin 或验证门禁关闭时
  输出证据绑定的真实候选，单/零候选进入 EVIDENCE_REQUIRED/NO_MATCH，不制造虚假歧义。
- 严格 JSON 重放拒绝 LLM confidence 字段、物理 SQL 展示、伪造 bundle、跨 bundle evidence、
  未知字段和 hash 篡改。本任务没有数据库、Compose 或页面变更，未调用外部模型。

2026-08-06 `QUERY-001` 验证：

```sh
gofmt -w internal/askdata/{binding,calibration,cognition,evaluation,ir,ircontract,toolhost}
go test ./internal/askdata/ir -count=1 -v
go test -race ./internal/askdata/ir ./internal/askdata/binding \
  ./internal/askdata/evaluation -count=1
go test ./internal/askdata/... -count=1
go vet ./internal/askdata/...
go test ./... -count=1
go vet ./...
./scripts/ci-check.sh
git diff --check
```

- 全部通过，race 未发现共享状态问题。完整 fixture 从规则解析、Understanding、GraphPlan、Binding
  一直重放到 IR，验证 2026 年半开区间、同比、华东稳定 member ID、地区 groupBy、指标降序与
  Top 5；candidate set 反序后 BuildArtifact 与全部 hash 保持一致。
- 过期 member ownership、多模型 bundle、无成员 FILTER、错误继承 snapshot/hash/date、未知物理
  SQL 字段和 binding/IR/artifact 篡改全部失败关闭。包合同下沉后 cognition/toolhost schema、
  evaluation alias 和原 `ir` API 的全仓调用均通过编译与既有回归。
- 本任务没有数据库、Compose 或页面变更，未调用外部模型，也未读取或输出 API key。

2026-08-06 `QUERY-002` 验证：

```sh
gofmt -w internal/askdata/compiler internal/askdata/testfixture/ir_build.go
go test ./internal/askdata/compiler -count=1 -v
go test -race ./internal/askdata/compiler ./internal/askdata/testfixture -count=1
go test ./internal/askdata/... -count=1
go test ./... -count=1
go vet ./...
./scripts/ci-check.sh
git diff --check
```

- 全部必需门禁通过，race 未发现共享状态问题。PostgreSQL 集成测试已编译并接入显式
  `ASKDATA_INTEGRATION_DATABASE_URL`/admin URL 门禁；当前 shell 未提供 URL，因此该外部用例按既有
  约定跳过，未读取或回显 `.env`。
- 可重放 fixture 证明 Resolver 只向 Store 提交 run 固定 release；即使 Store 的 current release
  改变，Resolution 全值与 hash 仍不变。负测覆盖 retired materialization、错误 actor/domain、
  member 跨 FILTER 维度、time dimension 错绑、manifest 对象缺失、GraphPath 关系不一致、带 label
  MEMBER contract 和 resolved AST 篡改。
- 全仓 test/vet、CI 静态检查和格式/diff 检查通过。本任务未新增页面、数据库迁移或外部模型调用。

2026-08-06 `QUERY-003` 验证：

```sh
gofmt -w internal/askdata/compiler internal/querycompiler
go test ./internal/askdata/compiler ./internal/querycompiler -count=1
go test -race ./internal/askdata/compiler ./internal/askdata/testfixture \
  ./internal/querycompiler -count=1
test "$(gofmt -l cmd internal | wc -l | tr -d ' ')" = "0"
go test ./... -count=1
go vet ./...
./scripts/ci-check.sh
git diff --check
```

- 全部通过，race 未发现共享状态问题。Golden 用例固定 PostgreSQL 参数化 SQL 与 Args；正式查询
  `RESULT` 上限 10000 可编译，同一 SQL shape 更换成员/时间参数后 compiled plan hash 不变，默认
  `PREVIEW` 仍拒绝超过 previewLimit 的请求。
- 复杂合成用例覆盖 Metric 逻辑/版本 Measure 引用、Measure 字段 AST、指标级 `PAID` default
  filter、华东成员、月粒度、2026 当前期/2025 基线期、指标排序及闰日同比 clamp。公开 Artifact
  JSON 不含 SQL、Args、`EAST` 或日期边界，反序列化后仍能重放 shape/hash 但不能取得执行参数。
- 负测覆盖未知 AST 字段、未知 Measure、非 exact dependency、物理 view 篡改、非法 limit kind；
  Resolution JSON 去除临时 member key 后仍可验证。本任务未新增页面、数据库迁移或外部模型调用。

2026-08-06 `QUERY-004` 验证：

```sh
gofmt -w internal/askdata/validator
go test ./internal/askdata/validator -count=1 -v
go test ./internal/askdata/... ./internal/querycompiler -count=1
test "$(gofmt -l cmd internal | wc -l | tr -d ' ')" = "0"
go test ./... -count=1
go vet ./...
go test -race ./internal/askdata/validator ./internal/askdata/compiler \
  ./internal/askdata/testfixture ./internal/querycompiler -count=1
./scripts/ci-check.sh
git diff --check
```

- 全部通过，race 未发现共享状态问题。危险 DDL/DML/锁/多语句/注释/递归 CTE、未知 relation/UDF、
  高成本、超行数、超大 Seq Scan、Join rows/fanout 和可放宽 ceiling 均被负测拒绝；正常 CTE、
  DATE_TRUNC、CASE、CAST NUMERIC 及真实 QUERY-003 live plan 均通过。
- 验证 Artifact JSON 不含 SQL、Args、发布物理名或 metric/field 名；公开工件丢失 live 参数后不能再次
  EXPLAIN，必须回到 pinned compiler 重新注入。PostgreSQL adapter 已编译通过；实际数仓连接验证留给
  明确要求 warehouse integration 的 `QUERY-005` 一并覆盖。本任务无页面、迁移、外部模型调用。

2026-08-06 `QUERY-005` 验证：

```sh
gofmt -w internal/askdata/validator internal/queryruntime
go test ./internal/askdata/validator ./internal/queryruntime -count=1
ASKDATA_INTEGRATION_WAREHOUSE_DATABASE_URL='postgres://<reader>@127.0.0.1:5433/<warehouse>' \
ASKDATA_INTEGRATION_WAREHOUSE_ADMIN_DATABASE_URL='postgres://<admin>@127.0.0.1:5433/<warehouse>' \
  go test ./internal/askdata/validator \
  -run TestPostgresExecutorUsesReaderRoleReadOnlySnapshotAndSupportsCancellation -count=1 -v
test "$(gofmt -l cmd internal | wc -l | tr -d ' ')" = "0"
go test ./... -count=1
go vet ./...
go test -race ./internal/askdata/validator ./internal/askdata/compiler \
  ./internal/askdata/testfixture ./internal/queryruntime ./internal/querycompiler -count=1
./scripts/ci-check.sh
git diff --check
```

- 全部通过，race 未发现共享状态问题。真实本地数仓 integration 创建隔离 DWS table + published view，
  确认 warehouse reader 不能 INSERT，真实 EXPLAIN/执行得到精确 `30.7500000000` DECIMAL；普通审计
  不出现任一输入/结果值。随后以 ACCESS EXCLUSIVE lock 阻塞同一查询，actor/domain 合法的 cancel 在
  2 秒内终止 pgx/PostgreSQL 调用并写入 CANCELED，fixture 已完整清理。
- 单元负测覆盖序列化计划无 live Args、validation/plan 不匹配、超 MaxRows、NaN、跨 actor 取消、caller
  deadline、Start/Finish audit 故障；成功 result hash 不受 run ID 影响，Artifact/Audit JSON 不含 SQL、
  Args 或结果行。本任务无页面、迁移、外部模型调用。

2026-08-06 `QUERY-006` 验证：

```sh
gofmt -w internal/askdata/validator
go test ./internal/askdata/validator -count=1
go test ./internal/askdata/... -count=1
go vet ./internal/askdata/...
go test -race ./internal/askdata/validator ./internal/askdata/cognition \
  ./internal/askdata/evaluation ./internal/askdata/compiler \
  ./internal/askdata/testfixture -count=1
go test ./... -count=1
go vet ./...
./scripts/ci-check.sh
git diff --check
```

- validator、askdata module test/vet/race 已通过。fixture 覆盖正常精确 Decimal、错误 OID、NULL key、
  重复行/key、metric-only fanout、除零、过期数据、质量失败、完整/不完整时间覆盖、确认空结果、权限
  裁剪空结果，以及 CURRENT/BASELINE 900% 异常趋势。
- 认知路径使用受控 reviewer fixture 验证 ANOMALY_ANALYSIS → RESULT_VERIFICATION 顺序、Prompt 不含
  SQL/Args/完整 rows、发明 evidence 失败关闭，以及规则失败而模型 PASS 时最终仍为 RETRY 并记录覆盖
  阻止。本任务无页面、迁移、数据库/数仓写入或真实外部模型调用。

2026-08-06 `ORCH-001` 验证：

```sh
gofmt -w internal/askdata/toolhost internal/askdata/cognition/executor_test.go
go test ./internal/askdata/toolhost -coverprofile=/tmp/askdata-orch001.cover -count=1
go test ./internal/askdata/... -count=1
go vet ./internal/askdata/...
go test -race ./internal/askdata/toolhost ./internal/askdata/cognition \
  ./internal/askdata/orchestrator -count=1
test "$(gofmt -l cmd internal | wc -l | tr -d ' ')" = "0"
go test ./... -count=1
go vet ./...
./scripts/ci-check.sh
git diff --check
```

- 全部通过，toolhost 覆盖率 68.8%，race 未发现共享状态问题。测试枚举并重放全部 14 个 Definition/
  schema/hash 和全部 14 个 typed Result validator；覆盖稳定 AvailableTools、search typed dispatch、
  permission/release/domain/budget preflight、候选对比两次正式查询预算、timeout/cancel、错误遮罩和
  Definition schema 深拷贝。
- 负测验证 NaN、发明/不完整 evidence、`rows` 等禁用字段无法形成 Tool Message；敏感 member handler
  即使返回 label/alias，也会在 Registry 结果中被清空。既有 Cognition ToolMessage fixture 已补充强制
  EvidenceRef，未放宽动作或参数合同。本任务无页面、迁移、数据库/数仓写入或真实外部模型调用。

2026-08-06 `ORCH-003` 验证：

```sh
gofmt -w internal/askdata/orchestrator/loop.go internal/askdata/orchestrator/loop_test.go
go test ./internal/askdata/orchestrator -count=1
go test ./internal/askdata/... -count=1
go vet ./internal/askdata/orchestrator
test "$(gofmt -l cmd internal | wc -l | tr -d ' ')" = "0"
go test ./... -count=1
go vet ./...
go test -race ./internal/askdata/orchestrator ./internal/askdata/cognition \
  ./internal/askdata/toolhost -count=1
./scripts/ci-check.sh
git diff --check
```

- 全部通过，race 未发现共享状态问题。测试覆盖单次 LLM fast path、typed search tool → 新证据 → 第二轮
  binding 决策、4 次模型复杂纠错上限、无进展、总 timeout、caller cancel、初始/运行中预算耗尽。
- 负测覆盖发明/冲突 EvidenceRef、重复 action、预算过滤后的不可用 formal tool、模型返回错误 stage、
  Tool response callId 错配和 adapter 超剩余预算收费；模型只接收 sanitized prompt/tool result，transcript
  不含 rows/SQL/Args/reasoning。本任务未连接数据库、数仓或真实模型，也没有页面变更。

2026-08-06 `ORCH-004` 验证：

```sh
gofmt -w internal/askdata/orchestrator
go test ./internal/askdata/orchestrator -count=1
go test -race ./internal/askdata/orchestrator -count=1
go vet ./internal/askdata/orchestrator
set -a && source .env.example && set +a
ASKDATA_INTEGRATION_DATABASE_URL="$DATABASE_URL" \
ASKDATA_INTEGRATION_ADMIN_DATABASE_URL="postgres://${POSTGRES_USER}:${POSTGRES_PASSWORD}@${POSTGRES_HOST}:${POSTGRES_PORT}/${POSTGRES_DB}?sslmode=disable" \
  go test ./internal/askdata/orchestrator -count=1 -v
go test ./internal/askdata/... -count=1
go vet ./internal/askdata/...
test "$(gofmt -l cmd internal | wc -l | tr -d ' ')" = "0"
go test ./... -count=1
go vet ./...
go test -race ./internal/askdata/orchestrator ./internal/askdata/cognition \
  ./internal/askdata/toolhost -count=1
./scripts/ci-check.sh
git diff --check
```

- 全部通过，race 未发现共享状态问题。真实 app/admin PostgreSQL integration 在回滚 fixture 内验证
  2 个 `SEMANTIC_QUESTION` AI request、2 次 LLM decision、1 次 typed tool、replay artifact、预算事件
  和 RETRIEVING → BINDING 状态事件原子落库；Resume 后可重建同 result/definition/charge 的脱敏执行，
  重复 action 未触达 fake Tool Host。
- 第二条真实数据库链验证 4 次 LLM 预算已用尽后，预算/错误事件、CLARIFICATION 工件和终态迁移同
  事务提交；相同 checkpoint 返回同一版本/事件数量，内容碰撞返回幂等冲突。完整 ORCH-002 状态矩阵、
  生命周期/RLS/乐观锁/pinned release integration 同次回归通过，fixture 全部回滚。负测另覆盖收费与
  usage 不符、决策/终态矛盾、无效 ceiling、重放 hash 碰撞及审计敏感键；本任务无页面或新迁移。

2026-08-06 `ORCH-005` 验证：

```sh
gofmt -w internal/askdata/http cmd/api/main.go
go test ./internal/askdata/http -count=1 -v
go vet ./internal/askdata/http
set -a && source .env.example && set +a
ASKDATA_INTEGRATION_DATABASE_URL="$DATABASE_URL" \
ASKDATA_INTEGRATION_ADMIN_DATABASE_URL="postgres://${POSTGRES_USER}:${POSTGRES_PASSWORD}@${POSTGRES_HOST}:${POSTGRES_PORT}/${POSTGRES_DB}?sslmode=disable" \
  go test ./internal/askdata/http \
  -run TestPostgresQuestionScopeResolutionUsesActiveReleaseRolesAndActorRLS -count=1 -v
go test ./internal/askdata/... -count=1
go vet ./internal/askdata/...
go test -race ./internal/askdata/http ./internal/askdata/orchestrator -count=1
test "$(gofmt -l cmd internal | wc -l | tr -d ' ')" = "0"
go test ./... -count=1
go vet ./...
./scripts/ci-check.sh
git diff --check
```

- 全部通过，race 未发现共享状态问题。真实数据库用例在回滚事务中以实际 app role 执行 scope query，
  确认 actor 可取得 ACTIVE release/role policy 和自身 pinned run，同域 observer 因 RLS 只能得到 not found。
  单元/HTTP 用例确认响应和 SSE 不含注入到 audit Details/payload 中的 prompt、SQL、result rows 或内部错误。

## 4. 工作区注意事项

- 用户在本次实施前已有多项 `docs/*` 删除状态；这些文件未被恢复或修改。
- `ASK_DATA_TECHNICAL_DESIGN.md`、`ASK_DATA_CODEX_TODO.md` 为当前项目设计和执行事实源；
  `可信智能问数与智能报表一体化平台_*.md` 为产品与技术基线，其第五部分与前四部分冲突时以第五部分为准。
- TODO 已按功能区划分为 B01～B13 个板块（见 TODO §0）。领任务时先确认所属板块，再确认 Wave 与 Batch；
  不要把新任务放到任何板块之外。
- 新增迁移编号 `000225`～`000241` 已在 TODO §22.1 预留到具体任务，新 Schema 已在 §22.2 预留，不得重复占用。
- 报表板块（B11）新建独立 Report V2 bounded context：不修改历史迁移，不假定旧报告表仍存在，
  不恢复 `000195` 删除的旧运行时。
- 不要恢复历史 `platform.semantic_*` 运行时；新控制面使用 `askdata` schema。
- `WEB-001` 的方案 3 已取得用户确认并完成。后续新增页面、流程或显著视觉状态仍须先出设计稿
  并取得用户确认；`WEB-002` 的 `ORCH-005` 依赖已满足，纯 API Client/类型接线不触发页面门禁。
  `WEB-010`～`013` 与全部 `WEB-RPT-*` 均需先过设计稿门禁。
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

### 5.1 立即可执行（不依赖人工业务输入）

1. `ORCH-006` Conversation 与运行保留策略；`ORCH-005` 已完成。原问句必须按明确策略选择
   加密短期保留或仅 hash，运行工件 TTL/删除不能破坏不可变统计，会话继承必须继续固定 tenant/actor/
   release。该任务是后端策略与配置，不涉及新增页面。
2. `WEB-002` 的依赖已满足，可按 ORCH-005 已冻结合同接真实 API Client/SSE；纯 client/types/hooks 接线
   不触发页面门禁，但若扩展页面流程或显著视觉状态仍须先提交设计稿确认。
3. **Batch 7 口径闭环**可与上述并行启动：`TIME-001` → `TIME-002` → `TIME-003` → `TIME-004`，
   以及 `ADD-001` → `ADD-003`。这两条链路是 95% 正确率的最大单点风险，且不依赖真实业务数据即可
   完成合同、编译器与校验器（业务取值由 `HUMAN-007`/`HUMAN-008` 后续填入）。
4. **Batch 8 资产建设产能**：`IMPORT-001` → `IMPORT-002` → `IMPORT-003` → `IMPORT-004`。
   没有批量导入，一个业务域 20～50 指标 + 20～40 维度 + 数千成员的建设周期无法满足 4～8 周目标，
   该能力属于 Wave 1 必需项，**不可后置**。
5. **Batch 10 报表合同层**：`RPT-CONTRACT-001`～`004` 可立即冻结。报表板块在原计划中完全缺失，
   合同不冻结就开始前端会导致编辑器、AI 与运行时结构分裂。

### 5.2 治理边界（已确认，不得改动）

6. `DB-004` 以 READY/投影基础完成，ACTIVE 原子切换归属 `REL-005`；`REG-006` 只负责 DRAFT 管理 API，
   发布生命周期 endpoint 分属 `REL-001`～`REL-005`；Wave 5 未完成的配置任务已改号为 `OPS-005`。
   不要恢复旧边界或重复编号。
7. `askdata.activate_release` 必须继续不存在，直到 `DB-007` 评测门禁、`DB-008` 双人审批和
   `REL-005` 同时完成；Projector/Resolver 的完成不构成放宽激活门禁的理由。
8. 产品决策 D01～D04 已确认并写入设计文档：未结束周期默认 `MTD`；报表资产认证**单人审批**
   （语义 Release 激活仍为双人审批，两者不得混同）；报告分享**不允许匿名**；明细取数申请入口
   **在本平台内**。相关任务为 `TIME-001`、`FUSE-002`、`RPT-DB-005`、`DR-001`。

### 5.3 人工输入阻塞

9. `REG-005` 仍需要 `HUMAN-001`～`HUMAN-003`；`SEARCH-005` 仍需要 `HUMAN-002`～`HUMAN-004`，
   当前不能生成正式认证资产或宣称 Recall@K / 95% 准确率。
10. 新增人工门禁 `HUMAN-007`～`HUMAN-013`（业务日历与时间策略、指标可加性、报告模板与叙述规范、
    报表资产认证责任人、明细取数审批链、配额与成本策略、容量与压测目标）。未提供前，Codex 可以
    建设合同、校验器、导入工具和测试夹具，**但不得编造业务答案**。

### 5.4 排期风险提示

11. B11 报表板块共 32 项任务且全部待建，是当前最大的未开工面；其中 6 项前端任务需逐个通过设计稿
    门禁，排期时必须为设计评审预留时间，不能按纯编码估算。
12. `EVAL-011` 密封集分片轮换必须在首次运行密封集**之前**完成。一旦按旧方式反复使用整套密封集，
    密封性即不可恢复，95% 论证将失效。

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
