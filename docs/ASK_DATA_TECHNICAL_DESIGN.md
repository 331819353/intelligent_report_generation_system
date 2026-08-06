# 智能问数系统技术架构与实施方案

> 面向当前 `intelligent_report_generation_system` 仓库，技术栈为 Golang + React，图数据库采用 NebulaGraph，向量检索复用 PostgreSQL + pgvector。
> 文档版本：1.0；设计日期：2026-08-05；默认业务时区：Asia/Shanghai。
> Codex 原子任务计划：[ASK_DATA_CODEX_TODO.md](./ASK_DATA_CODEX_TODO.md)。

## 1. 技术结论

要把智能问数做到可验证的 95%，不能采用“把库表结构一次性塞给大模型，让模型直接写 SQL”的方案。建议建设一条受治理、可回放、能澄清、能自我纠错的链路：

1. 只允许查询已发布、已物化、粒度明确的 DWS/ADS 稳定视图，首期不开放 ODS/DWD 任意 Text-to-SQL。
2. PostgreSQL 中建设版本化语义层，保存指标、度量、维度、维度值、关系、时间口径、权限、质量规则和认证样例，并作为唯一事实源。
3. pgvector 负责指标、维度定义、低基数非敏感维度成员及认证问法的混合召回；精确词典、全文检索、向量召回、图约束和重排共同决定候选，不能只依赖向量相似度。
4. NebulaGraph 保存语义层的可重建图投影，负责指标—模型—维度—维度值—数据集—Join 路径的兼容性约束与路径搜索，不保存指标定义的唯一真相。
5. LLM 是全流程认知中枢：参与语义资产建设、问句理解、候选判断、消歧、计划选择、异常分析、结果核验、反馈归因和发布评审；它通过结构化判断与工具动作推动流程，但不能绕过权限、版本、图约束和编译器，也不能直接提交可执行 SQL。
6. 绑定后的意图进入版本化 `Semantic IR`；Go 编译器基于可信语义对象和现有 Dataset DSL 生成参数化 SQL，随后执行 AST、权限、成本、Join 基数和结果一致性校验。
7. Question Orchestrator 以状态机驱动 Agent Loop，通过工具多次检索、核对、编译、试探和纠错；普通问题通常 1～2 次模型调用，复杂问题最多 4 次模型调用、6～8 次工具调用。
8. 当系统不能证明唯一正确时，必须提出带候选含义的定向澄清问题；澄清是保障准确率的一部分，不是失败。
9. 95% 必须通过至少 2,000 条双人复核、密封、与待发布语义版本绑定的端到端黄金集证明：观测严格正确率至少 96%，95% Wilson 置信下界至少 95%，P0 和安全样本必须 100% 通过。

这套方案可以达到目标的原因，不是单纯依赖“大模型更强”，而是让 LLM 在每个关键节点基于新证据持续认知和决策，同时把可能出错的自由生成收缩为“召回候选—LLM 裁决—图验证—确定性编译—LLM/规则联合核验—低置信澄清”，并用可度量的发布门禁持续拦截回归。

## 2. 当前仓库评估与落地边界

### 2.1 可以直接复用的能力

| 现有能力 | 代码/部署位置 | 在智能问数中的复用方式 |
|---|---|---|
| 控制面 PostgreSQL + pgvector | `compose.yaml` | 保存语义注册表、词典、向量、问数运行状态、黄金集和反馈 |
| 独立 PostgreSQL 数仓 | `postgres-warehouse` | 查询已发布 ODS/DIM/DWD/DWS/ADS 物化视图；问数首期只开放 DWS/ADS |
| Dataset DSL V1 | `internal/dataset` | 复用字段稳定 ID、表达式 AST、粒度、时间字段、分析合同和版本哈希 |
| 参数化安全编译器 | `internal/querycompiler` | 从可信 Query DSL 编译 PostgreSQL SQL，继续执行标识符白名单和值绑定 |
| 查询执行与审计 | `internal/queryruntime` | 复用只读仓库账号、查询超时、行数限制、执行审计和取消机制 |
| 权限、租户、业务域 | `internal/access`、`internal/policy` | 在召回前、语义绑定前和执行前做三次权限裁剪 |
| Embedding Provider | `internal/embedding` | 复用 OpenAI-compatible `/embeddings`、维度检查和输入预算 |
| 混合召回样板 | `internal/assetembedding` | 复用确定性文档、outbox、RRF、表优先召回和词法降级思想 |
| 多模型与 AI 审计 | `internal/ai` | 复用 Provider Pool、严格 JSON Schema、重试、Token/成本审计；扩展问数 Purpose 和循环协议 |
| React 权限与 API 基础 | `web/src` | 新增问数工作台、澄清卡片、结果可视化、执行解释和反馈界面 |

### 2.2 必须重新建设的能力

历史迁移 `000026`～`000193` 曾设计指标、维度、语义 QA、NebulaGraph、黄金集和 Tool Host，但 `000195_remove_decommissioned_features.up.sql` 已明确删除这些运行时表，`scripts/verify-database.sh` 也要求它们不存在；当前 Go API、Worker 和 React 中没有可运行的智能问数服务。

因此不要回滚 `000195`，也不要假设历史表仍存在。建议从后续新迁移开始，在控制库中新建独立 `askdata` schema，避免与退役的 `platform.semantic_*` 名称和验证规则冲突。历史迁移可以作为设计素材，但必须用当前业务边界重新实现、测试和上线。

### 2.3 首期准确率适用范围

95% 只能对明确范围承诺：

- 已纳入语义层并发布的业务域；
- 已认证指标、维度和关系；
- 已建立索引策略的维度成员；
- 首期支持聚合、分组、筛选、排序、Top N、环比、同比和有限派生指标；
- 查询当前仓库的已发布 DWS/ADS 稳定视图；
- 中文为主，允许常见中英混输；
- 系统可对歧义问题澄清、对未覆盖问题拒答。

不应把未治理的任意表查询、跨域自由 Join、预测/归因、复杂窗口计算和任意 SQL 也混入同一 95% 口径。

## 3. 总体架构

```mermaid
flowchart LR
    U["用户 / React 问数工作台"] --> API["Go Question API / SSE"]
    API --> AUTH["租户、业务域与数据权限"]
    AUTH --> ORCH["Question Orchestrator 状态机"]

    ORCH --> NLU["结构化理解器"]
    ORCH --> TOOLS["Typed Tool Host"]
    NLU --> DICT["精确词典 / 规则 / 时间解析"]
    NLU --> LLM["LLM 认知中枢"]
    LLM --> ORCH
    LLM -. "资产识别/评审" .-> REG
    LLM -. "候选判断/消歧" .-> SEARCH
    LLM -. "路径与计划判断" .-> GRAPH
    LLM -. "异常分析/结果核验" .-> VERIFY
    LLM -. "反馈归因/发布评审" .-> EVAL

    TOOLS --> SEARCH["Hybrid Search: exact + lexical + vector + rerank"]
    SEARCH --> PGV["PostgreSQL + pgvector"]
    TOOLS --> GRAPH["Graph Resolver"]
    GRAPH --> NEB["NebulaGraph 语义图投影"]
    GRAPH -. "一致性降级" .-> REG["PostgreSQL 语义注册表"]

    TOOLS --> BIND["Joint Binder / 置信度校准"]
    BIND --> IR["Versioned Semantic IR"]
    IR --> COMP["确定性 Query DSL / SQL 编译器"]
    COMP --> VALID["权限、AST、成本、Join、质量校验"]
    VALID --> EXEC["只读 Query Runtime"]
    EXEC --> DWS["现有 DWS / ADS 发布视图"]
    EXEC --> VERIFY["结果等价与异常验证"]
    VERIFY --> ORCH
    ORCH --> API

    REG --> PROJ["Outbox / Semantic Projector"]
    PROJ --> PGV
    PROJ --> NEB
    REG --> EVAL["黄金集与发布门禁"]
```

### 3.1 分层职责

| 层 | 核心职责 | 不允许做的事 |
|---|---|---|
| 交互层 | 会话、澄清、结果展示、证据解释、反馈 | 在前端拼 SQL 或自行解释业务口径 |
| 编排层 | 状态机、预算、工具循环、重试、终止条件 | 无上限循环、保存模型思维链 |
| 理解与绑定层 | 提取 mention、生成候选、联合消歧、计算校准置信度 | 直接把模型输出名称当成数据库对象 |
| 语义层 | 定义业务概念、版本、来源、粒度、关系、权限和质量 | 保存未经认证的自由文本为生产指标 |
| 检索层 | 精确、词法、向量、样例和历史反馈召回 | 只用一个向量索引决定最终对象 |
| 图层 | 兼容性、Join 路径、血缘、派生和层级约束 | 作为指标公式的唯一事实源 |
| 规划与编译层 | Semantic IR、Query DSL、参数化 SQL、成本计划 | 让 LLM 直接生成或修补 SQL 字符串 |
| 执行与验证层 | 只读执行、超时、限行、质量检查、结果验证 | 使用高权限账号或绕过已发布视图 |
| 评测与治理层 | 黄金集、回归、发布门禁、反馈闭环、版本回滚 | 用用户点赞率替代严格正确率 |

### 3.2 核心范式：LLM 是认知中枢，工具和规则是人的数据工具与制度边界

系统不采用“规则代替模型”，也不采用“模型绕过制度”。正确关系是：

```text
LLM：理解问题、形成假设、选择证据、比较候选、解释异常、作出下一步决策
Tools：检索真实对象、查询图关系、编译计划、执行查询、返回可验证证据
Rules/Policies：定义权限、版本、类型、成本、基数、隐私和发布门禁等不可绕过边界
Orchestrator：保存状态、控制预算、调度 LLM/Tools、判断是否继续、澄清、回答或阻断
```

LLM 的全流程参与是“每个阶段都参与认知、判断或评审”，不是“每个阶段都自由生成最终产物”。例如，LLM 可以判断两个销售额口径哪个更符合问句、分析 Join 探测为何异常、评审结果是否与问题一致；但指标版本必须来自注册表、Join 必须来自认证关系、SQL 必须由编译器生成、安全门禁不能由 LLM 投票关闭。

| 生命周期阶段 | LLM 的认知职责 | Tool/确定性事实 | 不可绕过边界 |
|---|---|---|---|
| 语义资产建设 | 识别候选指标/维度、补全描述、发现同义词和冲突、建议关系 | 扫描 Dataset DSL、字段、画像、血缘、历史问法 | 人工 Owner 审核、Schema 校验、版本发布 |
| 问句理解 | 还原完整意图、识别 mention/角色/省略和上下文 | 词典、时间解析、会话状态 | 严格 JSON Schema、权限前置 |
| 候选判断 | 比较精确/词法/向量候选及证据 | Hybrid Search 返回稳定 ID 和证据 | 不得选择未发布或未授权对象 |
| 消歧 | 结合上下文、定义、成员层级和反例作出选择或提问 | Semantic Contracts、Member Lookup、GraphPlan | 置信阈值和候选 margin 不足必须澄清 |
| 计划选择 | 选择指标组合、维度角色、模型和认证关系路径 | 图路径、粒度、基数、成本、质量状态 | Semantic IR 校验、fanout policy |
| 编译与修复 | 解释 IR/编译错误并决定回到哪一语义决策 | 确定性编译器、AST Validator、EXPLAIN | 禁止生成/提交任意 SQL |
| 异常分析 | 分析空结果、数据异常、候选差异和计划风险 | 验证查询、质量规则、基数探测 | 有界查询预算、敏感数据保护 |
| 结果核验 | 判断结果是否回答原问题、是否存在口径或趋势异常 | 结果摘要、结果 hash、对账和质量证据 | 规则校验失败不能被 LLM 覆盖 |
| 反馈归因 | 把反馈归因为指标、维度、成员、时间、关系、数据或表达问题 | 运行工件、绑定证据、用户选择 | 反馈不能直接修改生产语义 |
| 发布评审 | 汇总变更影响、失败聚类和残余风险，给出发布建议 | 密封黄金集、投影水位、安全集 | 数据库发布门禁不通过不能激活 |

因此系统应保留两类输出：一类是 LLM 的结构化认知结论和下一动作，另一类是工具返回的事实证据与确定性验证结果。审计记录结论、动作、证据 ID 和 hash，不保存隐藏思维链。

## 4. 语义层完整设计

### 4.1 核心概念

- **度量 Measure**：可以聚合的底层数值，例如订单金额、退款金额、订单行数。
- **指标 Metric**：有业务含义、计算公式、默认筛选、单位、粒度和负责人约束的度量或派生结果，例如“已支付销售额”“客单价”。
- **维度 Dimension**：用于分组或筛选的业务分析轴，例如地区、渠道、客户等级、统计月份。
- **维度成员 Member**：维度的规范值，例如地区维度中的“华东区”。
- **语义模型 Semantic Model**：绑定一个已发布数据集/物化视图，声明事实粒度、可用度量、可用维度、默认时间和关系。
- **关系 Relationship**：模型之间或实体之间经过认证的 Join 方向、条件、基数、时间有效性和扇出策略。
- **业务词 Term**：高频表达与语义对象的显式映射，例如“GMV”“成交额”映射到某指标版本。
- **认证样例 Certified Example**：经过人工批准的自然语言问法与 Semantic IR，不保存为可复制 SQL 模板。

### 4.2 语义层必须包含什么，以及为什么

| 对象 | 必备属性 | 建设原因 |
|---|---|---|
| Domain/Subject | 业务域、主题、Owner、适用人群 | 先缩小搜索空间，避免同名指标跨域误绑 |
| Entity | 订单、客户、商品等实体和主键 | 明确“客户数”统计的是客户实体而不是记录数 |
| Semantic Model | 数据集版本、物化版本、事实粒度、时间字段、时区 | 决定指标能否在正确粒度计算 |
| Measure | 字段 ID、聚合、可加性、单位、空值策略 | 避免金额求平均、库存跨时间求和等错误 |
| Metric Version | 公式 AST、依赖、默认过滤、单位、精度、时间语义 | 让“销售额”成为稳定业务合同，而非字段猜测 |
| Dimension | 字段 ID、类型、用途、层级、基数、敏感性 | 区分分组维度、过滤维度和不可检索维度 |
| Dimension Member | member_key、规范值、标签、别名、层级路径、有效期 | 把“华东”“华东区”绑定为同一受治理成员 |
| Time Semantics | 日历、财年、周起始日、时区、默认粒度 | 正确解释今年、上月、同比和自然周 |
| Relationship | 左右模型、Join AST、基数、fanout policy、SCD 有效期 | 选择可证明安全的 Join 路径并阻止重复聚合 |
| Cohort/Filter | 认证人群/筛选定义 AST | 支持“活跃客户”等不是单个维度值的概念 |
| Glossary/Term | 词、映射类型、目标 ID、范围、优先级、有效期 | 提供高频映射和租户/业务域覆盖能力 |
| Policy | 对象权限、行列策略、维度值敏感策略 | 保证检索结果和查询结果都不越权 |
| Quality Rule | 新鲜度、完整性、唯一性、非空、对账状态 | 数据质量不达标时阻止“语义正确但数据错误”的回答 |
| Certified Example | 问句 hash/脱敏文本、IR、结果 hash、适用版本 | 为检索、Few-shot 和回归提供高质量证据 |
| Release | semantic_version、content_hash、对象清单、投影状态 | 保证 PostgreSQL、向量和图在同一版本上回答 |

### 4.3 建议的控制库表

在新 `askdata` schema 中建设，所有业务表均包含 `tenant_id`、RLS、版本和审计字段：

```text
askdata.domains
askdata.business_terms
askdata.entities
askdata.semantic_models
askdata.measures
askdata.metrics
askdata.metric_versions
askdata.metric_dependencies
askdata.dimensions
askdata.dimension_hierarchies
askdata.dimension_members
askdata.dimension_member_aliases
askdata.relationships
askdata.cohorts
askdata.quality_rules
askdata.certified_examples
askdata.semantic_releases
askdata.semantic_release_objects
askdata.semantic_release_projections
askdata.search_documents
askdata.embedding_outbox
askdata.question_runs
askdata.question_run_events
askdata.question_artifacts
askdata.tool_calls
askdata.query_feedback
askdata.evaluation_sets
askdata.evaluation_cases
askdata.evaluation_case_reviews
askdata.evaluation_runs
```

指标公式、Join 条件、Filter 和 Semantic IR 必须存 AST/JSON 合同，不能存任意 SQL 片段。发布版本不可原地修改；变更后生成新版本、新 content hash，再做全量投影与评测。

### 4.4 与已有数仓的接入过程

1. 扫描 `platform.datasets`、`dataset_versions`、`dataset_fields`、`dataset_materializations` 的当前已发布版本。
2. 只导入 `layer IN ('DWS','ADS')` 且有 ACTIVE 发布视图、输出粒度和稳定 schema hash 的对象。
3. LLM 读取 Dataset DSL 的 `Field.Role`、`SemanticType`、`AnalysisContract`、`FactContract` 和业务描述，识别并解释候选度量、指标、维度、别名与潜在冲突；候选不能自动成为认证指标。
4. LLM 辅助数据管理员补齐指标名称、别名、公式意图、默认过滤、单位、Owner、可用维度和时间口径；确定性 Validator 检查 AST、类型和引用，最终由业务 Owner 审核。
5. Dimension Profiler 通过仓库只读连接扫描维度成员；LLM 对成员聚类、别名候选和层级异常给出判断，系统按索引策略生成规范成员、别名和层级路径，人工审批高风险合并。
6. LLM 基于现有 DSL Join、数据集血缘和基数探测识别关系候选并分析风险；只有通过 Join 基数、扇出和权限验证的关系才能认证。
7. 创建语义发布包；PostgreSQL 注册表、pgvector 文档、NebulaGraph 图均投影成功且 content hash 一致后才可激活。
8. 对该发布包跑黄金集；通过门禁后切换活动版本。已有运行继续固定旧版本，新问题使用新版本。

## 5. 用户输入如何变成完整意图

### 5.1 标准理解合同

模型第一次输出的不是 SQL，而是带原文 span 的结构化合同：

```json
{
  "domainHypotheses": [{"domainId": "sales", "score": 0.96}],
  "metricMentions": [
    {"text": "销售额", "start": 11, "end": 14, "aggregationHint": "DEFAULT"}
  ],
  "dimensionMentions": [
    {"text": "月", "start": 8, "end": 9, "role": "GROUP_BY"}
  ],
  "valueMentions": [
    {"text": "华东区", "start": 4, "end": 7, "dimensionHint": "地区"}
  ],
  "time": {
    "expression": "今年",
    "grain": "MONTH",
    "comparison": "YEAR_OVER_YEAR",
    "timezone": "Asia/Shanghai"
  },
  "ordering": [],
  "limit": null,
  "unresolvedSpans": []
}
```

该合同只描述用户说了什么，不允许填物理表名、列名或未检索到的对象 ID。随后由 Go Binder 将 mention 绑定到语义对象的稳定版本 ID。

### 5.2 逐步处理流程

1. **会话上下文重写**：将“那按地区呢”“换成去年”等追问与上一轮已确认的指标/维度合并，但保留本轮覆盖关系。
2. **确定性规范化**：全半角、大小写、数字单位、日期、停用语气词、常见比较词、行政区后缀和中英文符号规范化；保留原始字符位置。
3. **最长词典匹配**：使用 Trie/Aho-Corasick 对业务词、指标别名、维度别名、成员别名做领域内最长匹配；这些命中作为 LLM 判断的高可信证据，租户规则覆盖平台规则。
4. **规则解析**：确定性识别同比、环比、Top N、升降序、总计、平均、按/每个、时间范围和粒度，并把规范结果与冲突一并交给 LLM。
5. **LLM 完整理解**：LLM 综合会话、词典命中、规则结果和残余文本，输出严格 JSON span、角色、假设、冲突和下一步证据需求。
6. **分类型候选召回**：LLM 决定需要调用哪些检索工具；指标、维度定义、维度成员、时间/人群分别走独立索引，不能在同一向量池里竞争。
7. **候选认知判断**：LLM 阅读候选定义、正反证据和认证样例，比较业务含义；系统仍只允许它选择真实、已发布、已授权的稳定 ID。
8. **图约束与计划判断**：Graph Tool 返回可用模型、兼容维度、成员归属和 Join 路径，LLM 分析路径是否符合用户意图；不可行组合由规则删除。
9. **联合绑定**：算法对“指标 + 分组维度 + 过滤维度 + 成员 + 时间 + 模型”做全局 Beam Search，LLM 对 Top Bundle 作语义裁决，而不是逐词独立取 Top 1。
10. **重排与校准**：Cross-encoder/结构化 LLM 对 10～30 个受约束候选重排；最终置信度由校准器基于 LLM 判断、检索、图和规则特征计算，不采用模型自报 confidence。
11. **缺口判断**：LLM 分析残余 span、候选差距、权限、数据质量、关系、公式和执行成本，提出继续取证或澄清建议；不可绕过的门禁最终决定是否允许直接回答。

### 5.3 指标识别如何达到高准确率

指标候选得分建议由以下证据组合：

```text
metric_score =
  exact_alias_score
  + lexical_score
  + vector_score
  + domain_prior
  + certified_example_score
  + dimension_compatibility_score
  + conversation_context_score
  - ambiguity_penalty
  - stale_or_quality_penalty
```

关键机制：

- 指标别名必须按业务域生效；“收入”在财务域和电商域不能共享无范围映射。
- 指标检索文档必须包含业务定义、公式摘要、单位、默认筛选、适用实体、粒度、时间字段、可用维度和反例，不只包含指标名称。
- “客户数”“订单数”必须通过实体和去重键区分 `COUNT(*)`、`COUNT(order_id)` 和 `COUNT(DISTINCT customer_id)`。
- 派生指标必须展开已发布依赖图；不允许模型临时编造比率公式。
- 对“经营情况”“销售怎么样”等宽泛问题，只能映射到已认证 KPI Bundle，或要求用户选择指标。
- 认证样例参与召回，但最终仍需版本、权限和图约束验证，不能直接复制历史 SQL。

### 5.4 维度和维度值识别如何达到高准确率

用户提出的“维度描述 + 维度值作为 key”是正确方向，但需要扩展为多路证据：

```text
member_vector_key =
  业务域 + " | 维度:" + 维度名称 + " | 描述:" + 维度描述
  + " | 层级:" + hierarchy_path + " | 值:" + canonical_label
  + " | 别名:" + aliases
```

同时维护确定性键：

```text
exact_member_key = tenant_id + dimension_id + normalize(canonical_value_or_alias)
```

识别过程必须先确定候选维度，再在候选维度内查成员。不能把“苹果”直接在全库维度值中做一次 ANN，因为它可能是品牌、商品、公司或水果类别。

增强方式包括：

1. **唯一精确别名优先**：在同一候选维度内唯一命中时直接绑定；跨维度同名时仍需上下文消歧。
2. **别名与规范值分离**：规范值用于执行，别名用于检索；别名变更不改历史成员身份。
3. **层级路径**：保存“华东/上海/浦东”路径，使“上海”可与省、市、区域角色共同判断。
4. **领域和指标兼容性**：指标只能使用语义模型声明的维度；不兼容候选即使向量高也淘汰。
5. **Span 角色判断**：区分“按华东大区”中的分组意图和“华东区的销售额”中的筛选意图。
6. **拼音、简称和编码索引**：对中文地名、组织缩写、商品码建立可治理的词法别名，不依赖 embedding 猜测。
7. **成员有效期**：组织、区域等变化时按查询时间选择当时有效成员；不把已失效成员静默映射到当前成员。
8. **负样本与反例**：对容易混淆的成员对维护 hard negatives，训练或校准重排器。
9. **高基数策略**：客户、订单号等高基数维度不全量向量化，使用精确/前缀/受控模糊查询并要求维度上下文。
10. **敏感值策略**：个人信息、受限组织值不得发送给 embedding 或 LLM；只允许数据库内精确匹配或禁止问数。
11. **空值和默认值治理**：UNKNOWN、测试值、哨兵日期不能成为正常成员候选。
12. **主动学习**：将澄清选择、纠错和未命中聚类成候选别名，经管理员审批后进入下一语义发布。

### 5.5 联合绑定比独立分类更重要

假设“华东地区的客户销售额”同时召回两个指标和三个维度。如果逐项独立取最高分，可能得到不兼容组合。Joint Binder 应构造候选束：

```text
Bundle = MetricVersion + SemanticModel + GroupDimensions
       + FilterDimensions/Members + TimeSpec + RelationshipPath
```

对每个 Bundle 计算：检索分、精确证据、图兼容、粒度兼容、关系风险、权限、数据质量和执行成本，再保留 Top N。只要最优与次优差距不足、或任何关键绑定未经证明，就进入澄清。

## 6. 高频映射词体系

高频词不是一张随意维护的同义词表，而是版本化语义资产：

| 字段 | 说明 |
|---|---|
| `term` | 用户常用表达，如“GMV”“华东”“本财年” |
| `term_type` | METRIC、DIMENSION、MEMBER、TIME、COHORT、OPERATOR |
| `target_object_id/version` | 稳定语义目标；时间/操作符可指规范代码 |
| `domain_id` | 生效业务域，允许平台默认与租户覆盖 |
| `match_mode` | EXACT、PREFIX、SUFFIX、REGEX_SAFE、VECTOR |
| `priority` | 确定性冲突排序；不能用它掩盖同级歧义 |
| `negative_contexts` | 明确不适用的上下文 |
| `valid_from/to` | 生效期 |
| `source` | 人工、黄金集、反馈候选、导入 |
| `review_status` | DRAFT、APPROVED、DEPRECATED |

精确规则和映射变更后可立即参与草稿评测，但只有随语义发布包通过回归后才能进入生产。系统可以自动挖掘词频和映射建议，不能自动发布业务映射。

## 7. 向量化检索设计

### 7.1 索引分区

至少分为五类文档，分别设置 Top K、阈值和重排逻辑：

- `METRIC`：指标名称、别名、定义、单位、实体、粒度、默认过滤、可用维度。
- `DIMENSION`：维度名称、别名、描述、类型、层级、适用模型。
- `DIMENSION_MEMBER`：维度上下文 + 规范值 + 别名 + 层级路径；只收录允许向量化的成员。
- `BUSINESS_TERM`：高频术语本身；映射结果不混入向量文本，避免相互污染。
- `CERTIFIED_EXAMPLE`：脱敏问法摘要 + 规范意图特征，用于相似问法召回。

### 7.2 混合召回流程

1. 先按 tenant、domain、权限、对象状态、semantic release 做数据库级过滤。
2. 精确别名/编码命中 Top N。
3. 中文二字片段、trigram/全文词法检索 Top 30。
4. HNSW cosine 向量检索 Top 30。
5. 用 RRF 合并，精确唯一命中给予受控加权，但不绕过图验证。
6. 取 Top 10～30 做 Cross-encoder/LLM 重排。
7. 用图约束和数据质量做最终裁剪。

当前仓库的 Qwen3-Embedding-4B 配置为 2,560 维，适合继续使用 `halfvec(2560)`；pgvector 官方说明 `halfvec` 支持最高 4,000 维、HNSW 支持 cosine，并提示多租户共享近似索引会互相影响召回，因此规模上来后应按大租户或业务域分区，而不是仅靠查询后的 tenant 过滤。参考：[pgvector 官方仓库](https://github.com/pgvector/pgvector)。

### 7.3 召回质量要求

- 每个索引维护 exact-search 对照集，定期比较 ANN recall。
- `metric recall@10 >= 99%`、`dimension recall@10 >= 99%`、`member recall@20 >= 99%` 才能进入绑定阶段。
- 小数据集或强 tenant/domain 过滤后优先精确 KNN；HNSW 是性能优化，不是正确性事实源。
- embedding 模型、维度、文档模板版本进入 `input_hash`；不同模型的向量不得混用同一索引。
- embedding 服务失败时降级到精确 + 词法检索；降级后的低置信问题必须澄清，不能假装召回质量不变。

### 7.4 维度成员索引策略

| 策略 | 适用范围 | 实现 |
|---|---|---|
| FULL | 低基数、非敏感、稳定成员 | 精确索引 + 词法 + 向量 |
| EXACT_ONLY | 敏感或代码型值 | 仅数据库内规范值/别名精确匹配，不进模型 |
| ON_DEMAND | 高基数但允许查找 | 在已确定维度内做前缀/受控模糊/Top N 查询 |
| NONE | 禁止检索或无业务意义 | 不能作为自然语言维度值绑定 |

阈值不应全局写死。首批可以用“成员数、变化率、敏感性、平均文本长度、查询频率”决定策略，经过压测后再调整。

`EXACT_ONLY` 的落地边界固定如下：调用侧只在内存中规范化原始 span，并提交
`SHA256(dimension_version_id + NUL + normalized_value)`；数据库函数同时固定当前
tenant/actor/domain、release ID/hash、`POSTGRES_REGISTRY=READY` 水位、精确 DIMENSION/MEMBER
manifest、成员有效期和唯一候选。MEMBER manifest 只保存 `dimensionVersionId` 与最多 64 个
排序且唯一的 opaque `aliasVersionIds`，不得保存 label、lookup hash 或可反查的 alias content
hash。CONFIDENTIAL/RESTRICTED 分别要求 `LOOKUP_CONFIDENTIAL_MEMBER`/
`LOOKUP_RESTRICTED_MEMBER` 的真实 USER 或 ACTIVE ROLE 对象授权；不存在、无权限、歧义、过期、
错误 hash 和未固定发布统一为零行，历史 run 允许继续读取其固定的 `SUPERSEDED` release。

数据库命中返回的 Go 合同仅包含成员/维度版本 ID、content hash 和 label-free evidence，并以
私有 payload/proof 绑定这些字段；调用方只能使用只读 accessor，复制后替换 ID、hash 或 evidence
会失败。原问句敏感 span 不进入 SQL、日志或 evidence；进入 LLM 前，对当前问句、继承上下文
以及所有嵌套 fact 字符串执行等长 Unicode 遮罩，覆盖 NFKC、case-fold、全角和邻接变体。
Reviewer 回显该 span 或其规范化变体时失败关闭。Understanding 的普通 `ExactMatch` 一律拒绝
MEMBER，当前不存在公开的 label-bearing grant，因此 EXACT_ONLY 即使是 PUBLIC/INTERNAL 也不把
命中 label 重新注入模型。

画像本地 observation 仍按 `PUBLIC/INTERNAL + FULL + low-cardinality` 重算 label 资格，并可做
确定性 alias 检查；但给模型的 `DIMENSION_PROFILE` PromptFact 是独立的聚合载荷，不含 label、
normalized value、member hash 或派生 member ID。成员型 LLM reviewer 在实现从 PostgreSQL
`profile_generation` 权威重载并绑定证据前必须保持禁用，不能由公开 Worker、自定义 Store 或
Scanner 间接铸造模型可见的成员标签。

Reranker 对显式 MEMBER candidate 在 request 规范化和反序列化 evidence 校验两层都失败关闭；
在建立 release-pinned authoritative candidate loader/capability 前，不把 MEMBER definition 或
negative examples 送入 reviewer。这里的 Go `internal/` 请求结构只允许由经过 SQL/RLS/release
过滤的可信 typed producer 构造，不是网络或插件输入合同；未来 ORCH 接线不得把这些结构直接
暴露给不可信调用方。若该信任边界改变，必须先为 candidate provenance 和敏感成员扫描结果增加
不可伪造的持久化证明，否则所有 caller-supplied label-bearing LLM 路径都应保持禁用。

## 8. NebulaGraph 语义图设计

### 8.1 定位

NebulaGraph 用来快速回答：

- 指标属于哪个业务域和语义模型；
- 指标支持哪些维度；
- 维度值属于哪个维度和层级；
- 派生指标依赖哪些指标/度量；
- 多模型查询允许走哪条 Join 路径；
- 哪条关系会产生 fanout 或需要预聚合；
- 哪些认证样例支持当前组合。

PostgreSQL 语义注册表仍是事实源。NebulaGraph 是按 semantic release 生成的可重建投影；图写入失败不能让未完成版本被激活。

### 8.2 图模型

建议 Tag：

```text
domain, term, entity, semantic_model, measure, metric,
dimension, member, time_dimension, cohort, dataset, field,
quality_rule, policy, certified_example
```

建议 Edge：

```text
BELONGS_TO_DOMAIN
MODELED_BY
EXPOSES_MEASURE
HAS_METRIC
HAS_DIMENSION
HAS_MEMBER
ROLLS_UP_TO
DERIVED_FROM
USES_MEASURE
COMPATIBLE_WITH
JOINS_TO
FILTERED_BY
GOVERNED_BY
CERTIFIED_BY
SOURCED_FROM
```

`JOINS_TO` 必须保存关系版本、左右键的逻辑字段 ID、基数、方向、fanout policy、有效时间语义和认证状态，但不保存任意 SQL。

### 8.3 多租户与 Space 方案

建议默认使用“每环境一个共享 Space + 服务端强制租户前缀 VID”：

```text
VID = tenant_id + ":" + object_type + ":" + stable_object_id + ":" + version
```

每个 Tag 和 Edge 同时保存 `tenant_id` 与 `release_hash`；只有 Graph Service 能访问 NebulaGraph，浏览器、LLM 和普通 API 用户都不能提交 nGQL。Graph Service 只接受稳定对象 ID 的 typed request，并由服务端生成参数化/转义后的有界 nGQL。

对极高隔离要求的大租户可使用独立 Space 或独立集群。官方 `nebula-go` 的 Session Pool 与单个用户、单个 Space 绑定，因此如果采用每租户 Space，需要维护多 Session Pool 和生命周期管理；服务端与 Go Client 必须锁定兼容版本，不能依赖 master/nightly。参考：[NebulaGraph 官方 Go Client](https://github.com/vesoft-inc/nebula-go)。

#### 8.3.1 已落地的开发基线

截至 2026-08-06，开发环境锁定 NebulaGraph metad/storaged/graphd 与
`github.com/vesoft-inc/nebula-go/v3` `v3.8.0`。共享 Space 使用
`partition_num=1`、`replica_factor=1`、`FIXED_STRING(256)` VID；这只是开发拓扑，不能外推
生产 partition、副本或容量。

当前 init 只创建 GraphPlan Adapter 已冻结并实际使用的 Schema 子集：

```text
Tags: semantic_model, metric, dimension, member
Edges: MODELED_BY, HAS_DIMENSION, HAS_MEMBER, JOINS_TO
```

每个对象均保存 tenant/domain/release 属性，`JOINS_TO` 保存关系版本、Join 类型、基数、fanout
policy 与认证状态。8.2 的完整目标模型仍由后续 Projector/Resolver 合同逐步扩展；未被 typed
Adapter 消费的 Tag/Edge 不得提前创建成看似稳定的生产合同。

运行权限按进程分离：API 只持有目标 Space 的 `GUEST`，Worker 只持有 `USER`，bootstrap root
仅用于 init。首次空卷时先轮换厂商默认 root，再注册 Storage 和创建 Schema/角色；已有 Space、
Schema 或角色与冻结合同不一致时失败关闭。metad/storaged 只在 internal 集群网，graphd 同时加入
internal 客户端网；API/Worker 不能解析 Meta/Storage。graphd 本身不发布端口，本地调试与隔离
验收仅在 init 成功后启动无凭据 TCP proxy，并只绑定宿主 loopback。

正式验收的 project 与 Space 必须由同一随机 nonce 严格关联。验收 Compose 通过专用 override
重置 API、Worker 和 Connection Test Worker 的 service `env_file`，配置展开禁用 env file
解析，因此不会间接读取开发者 `.env`。Go integration 在任何写入前必须反查目标 project 的
proxy published endpoint 和 Compose ownership label，并对四类 Tag、四类 Edge 分别执行
tenant/release 真实排除；只有 Resolve 成功、目标关系消失且无关对照关系仍存在才算隔离通过。

### 8.4 发布投影与降级

```text
DRAFT -> VALIDATING -> PROJECTING -> READY -> ACTIVE -> SUPERSEDED
```

每个 release 必须有以下投影水位：

- `POSTGRES_REGISTRY`
- `SEARCH_INDEX`
- `NEBULA_GRAPH`
- `EXECUTION_SEMANTIC_LAYER`

四者的 `applied_content_hash` 全部等于 release `content_hash` 才能 READY。问数运行开始时固定 release ID/hash，运行中不得自动切换。

NebulaGraph 故障时，优先使用同 release/hash 的认证 GraphPlan 缓存；缓存未命中时可在 PostgreSQL 关系注册表上对少量候选做有界递归查询，得到相同合同但性能下降。绝不能在图不可用时绕过关系验证直接执行向量 Top 1。

## 9. Semantic IR 与 SQL 生成

### 9.1 Semantic IR

最终意图必须变成只引用稳定 ID 的规范合同：

```json
{
  "irVersion": "1.0",
  "semanticReleaseId": "release-uuid",
  "semanticContentHash": "sha256...",
  "modelId": "sales_order_model@v4",
  "metrics": [
    {"metricVersionId": "net_sales@v3", "alias": "net_sales"}
  ],
  "groupBy": [
    {"dimensionVersionId": "stat_month@v2", "grain": "MONTH"}
  ],
  "filters": [
    {
      "dimensionVersionId": "sales_region@v5",
      "operator": "IN",
      "memberIds": ["east_china@v2"]
    }
  ],
  "timeRange": {
    "dimensionVersionId": "order_date@v2",
    "start": "2026-01-01",
    "endExclusive": "2026-08-06",
    "timezone": "Asia/Shanghai"
  },
  "comparison": {"type": "YEAR_OVER_YEAR"},
  "sort": [],
  "limit": 500
}
```

IR 校验包括：版本固定、对象存在、权限、指标和模型兼容、维度可用、成员归属、时间范围、比较口径、结果粒度、最大行数和查询复杂度。

### 9.2 确定性编译

1. 从 Metric Version 读取公式 AST 和默认过滤。
2. 从 Semantic Model 读取已发布 Dataset Version、Materialization 和物理视图白名单。
3. 从 Dimension/Member 读取逻辑字段 ID 与规范绑定值。
4. 从 GraphPlan 读取唯一认证关系路径。
5. 将 IR 展开为临时、不可持久修改的 Query DSL。
6. 复用 `dataset.Validate` 和 `querycompiler.Compile` 生成 PostgreSQL 参数化 SQL。
7. 编译结果保存 plan hash，不在普通审计表保存原始问题、参数明文或结果行。

LLM 在编译阶段继续参与：它分析错误属于指标、维度、成员、时间、关系还是计划选择，并决定重新取证、换候选、修改 Semantic IR 或请求澄清；但不能对编译错误返回一段“修正 SQL”。所有修复必须回到 IR 或语义合同层，再由编译器重新生成 SQL。

### 9.3 SQL 安全和正确性门禁

- 只允许 `SELECT`/CTE；禁止 DDL、DML、COPY、外部函数和不在白名单的 UDF。
- 标识符来自发布视图白名单，用户值全部绑定参数。
- 用数据库只读角色和只读事务执行；PostgreSQL 官方文档说明只读事务会阻止常见 DML/DDL。参考：[PostgreSQL SET TRANSACTION](https://www.postgresql.org/docs/16/sql-set-transaction.html)。
- 设置 `statement_timeout`、`lock_timeout`、最大扫描/结果行数和每租户并发预算。
- 执行前使用 `EXPLAIN (FORMAT JSON)` 做计划成本、全表扫描、Join 行数和聚合风险检查；不要把 `EXPLAIN ANALYZE` 当作安全预检，因为它会实际执行可执行部分。
- 对 MANY_TO_MANY、ONE_TO_MANY 后聚合、非可加指标、SCD2 和跨事实 Join 执行专项验证。
- 执行后检查输出 key 唯一性、重复行、数值类型、空值、除零、结果行数、时间覆盖、数据新鲜度和质量规则。
- 对空结果，先验证成员存在、时间有数据和权限裁剪，再决定回答“无数据”还是修正候选。

## 10. Agent Loop 与 Tools

### 10.1 为什么需要以 LLM 为中枢的应用层 Loop

当前 `internal/ai` 已支持严格 JSON Schema、Tool 角色和多模型，但 Provider Request 仍以结构化补全为主，没有完整的原生 tool-call 参数合同。首期建议实现供应商无关的应用层动作协议：LLM 在每个阶段读取当前状态和新证据，输出 `CALL_TOOL`、`PROPOSE_BINDING`、`PROPOSE_PLAN`、`ANALYZE_ANOMALY`、`VERIFY_RESULT`、`FINALIZE`、`CLARIFY` 或 `BLOCK`；Tool Host 执行后把脱敏事实作为 Tool Message 回传。这样 LLM 始终是认知和决策中心，DeepSeek、GLM 或其他 OpenAI-compatible 模型又可共用同一状态机，不依赖某家原生 Function Calling 细节。

### 10.2 工具清单

| Tool | 输入 | 输出 | 纠错用途 |
|---|---|---|---|
| `search_semantic_objects` | mention、类型、domain、release | 候选 ID、分数、证据 | 扩大或收缩指标/维度候选 |
| `get_semantic_contracts` | 稳定对象 ID | 公式、粒度、单位、Owner、状态 | 比较同名指标真实含义 |
| `lookup_dimension_values` | dimension ID、value mention | member ID、别名、路径 | 在正确维度内消歧成员 |
| `get_certified_examples` | 理解摘要 | 相似认证 IR | 补充规范问法证据 |
| `resolve_graph_plan` | 候选 bundle | 兼容模型、Join 路径、风险 | 删除不可能组合 |
| `validate_semantic_bundle` | 完整 bundle | 缺口、冲突、置信特征 | 决定继续、澄清或编译 |
| `get_data_quality_status` | model/metric/time | 新鲜度和质量门禁 | 阻止错误数据回答 |
| `compile_semantic_query` | Semantic IR | plan hash、参数合同 | 确定性生成查询 |
| `validate_query_plan` | plan hash | AST/权限/成本/风险 | 在执行前发现错误 |
| `probe_join_cardinality` | path、时间范围 | 有界基数证据 | 验证 fanout 假设 |
| `execute_query_plan` | 认证 plan | 结果摘要/引用 | 正式只读执行 |
| `execute_validation_query` | 预定义验证类型 | count/distinct/coverage | 校验空结果和聚合 |
| `compare_candidate_results` | 两个认证候选计划 | 差异摘要 | 难例下比较候选，不暴露原始行 |
| `request_clarification` | 冲突点、候选 | 可读选项 | 定向澄清 |

### 10.3 状态机

```mermaid
stateDiagram-v2
    [*] --> RECEIVED
    RECEIVED --> AUTHORIZED
    AUTHORIZED --> CONTEXT_READY
    CONTEXT_READY --> UNDERSTANDING
    UNDERSTANDING --> RETRIEVING
    RETRIEVING --> BINDING
    BINDING --> GRAPH_VALIDATING
    GRAPH_VALIDATING --> IR_READY
    IR_READY --> PLAN_VALIDATING
    PLAN_VALIDATING --> EXECUTING
    EXECUTING --> RESULT_VERIFYING
    RESULT_VERIFYING --> ANSWERED

    RECEIVED --> BLOCKED
    AUTHORIZED --> BLOCKED
    CONTEXT_READY --> BLOCKED
    UNDERSTANDING --> CLARIFICATION_REQUIRED
    BINDING --> CLARIFICATION_REQUIRED
    GRAPH_VALIDATING --> CLARIFICATION_REQUIRED
    RETRIEVING --> CLARIFICATION_REQUIRED
    PLAN_VALIDATING --> CLARIFICATION_REQUIRED
    RESULT_VERIFYING --> CLARIFICATION_REQUIRED
    RESULT_VERIFYING --> BINDING: bounded correction
    PLAN_VALIDATING --> BINDING: semantic repair
    UNDERSTANDING --> BLOCKED
    RETRIEVING --> BLOCKED
    BINDING --> BLOCKED
    GRAPH_VALIDATING --> BLOCKED
    IR_READY --> BLOCKED
    PLAN_VALIDATING --> BLOCKED
    EXECUTING --> BLOCKED
    RESULT_VERIFYING --> BLOCKED
    CLARIFICATION_REQUIRED --> [*]
    ANSWERED --> [*]
    BLOCKED --> [*]
```

图中省略非终态的同态 checkpoint。可执行规范以数据库
`askdata.valid_question_run_transition` 与 Go `orchestrator.CanTransition` 的一致矩阵为准；
所有 BLOCK/CLARIFY 边都必须保存稳定原因和可重放事件，三个终态本身不可再更新。
Understanding、Binding Bundle、GraphPlan、Semantic IR、QueryPlan、Result hash 只能在各自
治理阶段首次出现并按上游顺序形成连续链；除 PLAN/RESULT → BINDING 的显式纠错会清空
Binding 之后的陈旧链外，已有 hash 不得清空或覆盖，ANSWERED 前也不得一次性补写历史阶段。

### 10.4 循环预算和终止条件

```go
for step := 0; step < budget.MaxSteps; step++ {
    action := planner.Next(state.SanitizedSnapshot())
    result := toolHost.Execute(ctx, action, pinnedRelease, policyScope)
    state.Apply(action, result)

    if state.ReadyToAnswer() || state.NeedsClarification() || state.Blocked() {
        break
    }
    if !state.MadeProgress() {
        return ErrToolNoProgress
    }
}
```

建议预算：最多 4 次模型调用、8 次工具调用、2 次正式查询、3 次验证查询、25 秒总时限。普通精确命中走“单次 LLM 理解与裁决 + 确定性工具验证”的 fast path，不进入多轮模型循环；复杂问题才启动多轮取证和自我纠错。每个 LLM 决策和 Tool 调用记录 action、参数 hash、结果 hash、证据 ID、release hash、权限范围、耗时和错误码；不记录模型隐藏思维链。

## 11. 95% 正确率的定义、证明与持续保障

### 11.1 不能只看一个“SQL 能运行率”

生产主指标定义为：

```text
Strict E2E Correctness =
  结果等价且语义计划全部正确的直接回答数
  + 对歧义/不可回答问题做出正确澄清或拒绝的数量
  ----------------------------------------------------
  全部评测问题数量
```

直接回答必须同时满足：

- 指标版本正确；
- 分组维度正确；
- 过滤维度及成员正确；
- 时间范围、时区和粒度正确；
- 聚合、默认过滤和比较口径正确；
- 模型和 Join 路径正确；
- 权限和数据质量正确；
- 结果与黄金结果等价。

任何一个关键项错误，该问题都算错。结果等价时对行排序、Decimal 精度、时区、NULL 和浮点容差做规范化，不能只比较 SQL 字符串。

### 11.2 同时约束准确率和覆盖率

只靠大量拒答可以虚假提高准确率，因此必须同时发布：

- `strict_e2e_accuracy >= 96%`；
- 95% Wilson lower bound `>= 95%`；
- 对明确可回答问题的直接回答覆盖率 `>= 85%`；
- 正确澄清/拒答率 `>= 95%`；
- P0 核心问题 `= 100%`；
- 越权、提示注入、敏感值和缓存隔离安全集 `= 100%`；
- 敏感数据泄漏 `= 0`。

2,000 条以上样本、观测正确率 96% 左右，才能让 95% Wilson 下界接近或超过 95%；小样本的“95%”不应作为生产承诺。

### 11.3 分阶段质量指标

| 阶段 | 质量指标 | 建议发布线 |
|---|---|---|
| 词典/规则 | 时间、比较、Top N 解析准确率 | >= 99% |
| 指标召回 | Gold metric recall@10 | >= 99% |
| 维度召回 | Gold dimension recall@10 | >= 99% |
| 成员召回 | Gold member recall@20 | >= 99% |
| 指标绑定 | Top-1 precision/F1 | >= 97% |
| 维度绑定 | mention-level F1 | >= 97% |
| 成员绑定 | mention-level F1 | >= 97% |
| GraphPlan | 正确模型/关系路径率 | >= 99% |
| IR | 全字段 strict match | >= 97% |
| SQL/结果 | 结果等价率 | >= 96% |
| 安全 | 关键安全样本通过率 | 100% |

这些阶段指标用于定位故障，不能相乘替代端到端评测。最终是否达到 95% 只看密封 E2E 集和线上审计抽样。

### 11.4 黄金集设计

至少覆盖：

- 高频简单问题 35%；
- 时间、同比、环比 15%；
- 多维分组、Top N 15%；
- 维度同名和成员歧义 10%；
- 指标同名、别名、派生指标 10%；
- 关系/Join/扇出 5%；
- 空结果、数据质量、过期成员 5%；
- 越权、注入、敏感数据和不可回答 5%。

按业务域、用户角色、语言变体、问题复杂度和线上频率分层抽样。开发集用于调优，Validation 用于阈值，Sealed 集在发布前不可查看，Production Regression 来自经审批的线上问题。每条 Sealed Case 需两名独立评审确认问句、预期 IR、结果 hash 和安全期望。

数据库实现必须把“独立评审”和“当前内容”作为可重算事实，而不是信任调用方汇总值：case
保存脱敏策略 hash、expected path/IR/result hash 和当前内容编辑者；原作者及当前内容编辑者均
不得评审同一 content hash，每条 case 只有两个唯一 review slot。Seal 在同一事务锁定 set、
case、review，重新确认两条当前 APPROVED 事实后才计算稳定 manifest hash；USER 模式随后不再
读取密封问句和评审，只有 SYSTEM 评测 worker 可读。评测运行只追加写，必须固定 set/case
content hash、release ID/semantic version/content hash、expected/actual path/IR/result 和数仓
snapshot/freshness；等价结论在 hash 不同时必须绑定 comparison report hash，敏感泄漏必须由
worker 显式写入且不能使用默认 false。Production Regression 未密封或 set 已 RETIRED 时不得
新增运行。用户反馈只绑定原 actor 的终态 question run，使用结构化 issue type，不能直接修改
答案或生产语义。

### 11.5 为什么这套设计能达到 95%

| 主要错误来源 | 架构控制 | 结果 |
|---|---|---|
| 同名指标/维度 | 业务域路由 + 词典 + 多路召回 + 图兼容 | 把模糊全库选择收缩成可证明候选 |
| 维度值识别错 | 先维度后成员 + 描述/值复合 key + 层级/别名 | 避免全局值碰撞 |
| 模型幻觉对象 | 稳定 ID 绑定 + 注册表校验 | 未发布对象无法进入 IR |
| Join/粒度错误 | NebulaGraph 路径 + 基数/fanout 验证 | 在 SQL 前阻断重复聚合 |
| SQL 生成错误 | AST/IR 确定性编译 | 把自由生成转为可测试代码 |
| 隐含歧义 | 校准置信度 + 候选 margin + 定向澄清 | 不强答无法唯一确定的问题 |
| 一次推理失误 | Tool Loop + 编译/执行/结果验证 | 用外部证据迭代纠错 |
| 语义变更回归 | Release hash + 密封黄金集门禁 | 未通过评测的版本无法激活 |
| 反馈污染 | 反馈只生成候选，人工审批后发布 | 防止错误点击直接修改生产语义 |

必须明确：架构只能提供达到 95% 的工程条件，不能在没有语义覆盖、黄金集和实际评测结果时提前宣称已经达到 95%。

## 12. 技术选型

| 领域 | 选型 | 原因 |
|---|---|---|
| Backend | 当前 Go 版本 + `net/http` + `pgx` | 与仓库一致；typed tools、状态机和并发控制实现简单 |
| Frontend | React + TypeScript + React Router | 与现有项目一致 |
| Server State | TanStack Query | 管理问数运行、重试、缓存和失效 |
| Streaming | HTTP POST + SSE/fetch stream | 服务器单向推送状态足够，复杂度低于 WebSocket |
| Table | TanStack Table | 大结果表的列、排序和虚拟化控制 |
| Chart | Apache ECharts | 折线、柱状、饼图和组合图覆盖问数结果 |
| Control Plane | PostgreSQL 17 | 已部署；事务、RLS、版本、outbox 和审计适合语义事实源 |
| Vector | pgvector `halfvec` + HNSW + lexical/RRF | 复用现有能力；高维向量和混合检索可控 |
| Graph | NebulaGraph `v3.8.0` + 官方 `nebula-go/v3 v3.8.0` | 兼容 POC 已锁定 3.x fbthrift 客户端；禁止 master/nightly 和不兼容的 v5 gRPC 客户端 |
| Warehouse | 现有独立 PostgreSQL DWS/ADS | 首期减少 SQL 方言和跨源不确定性，提高正确率 |
| Object Storage | 现有 MinIO | 保存大评测结果、离线报告和可重放非敏感工件 |
| Cache | 首期 PostgreSQL 缓存；规模后可选 Redis | 减少首期组件，缓存不作为语义事实源 |
| Async | 现有 PostgreSQL outbox + Worker | 语义投影、embedding、画像和评测可幂等重试 |
| Observability | OpenTelemetry + Prometheus/Grafana | 统一 Trace、Metric、Log，观察每个语义阶段 |

NebulaGraph 开发环境可以单副本部署 graphd/metad/storaged；生产建议按官方兼容矩阵和容量 POC 设计多副本。禁止在 `go.mod` 使用 master/nightly，必须锁定与服务端兼容的 release tag。

## 13. Golang 工程落地

### 13.1 新增 package

```text
internal/askdata/registry        语义对象、版本、发布与仓库映射
internal/askdata/search          精确/词法/向量召回、RRF、重排
internal/askdata/dimension       维度画像、成员、别名和索引策略
internal/askdata/graph           NebulaGraph adapter、GraphPlan、投影
internal/askdata/understanding   规则、时间解析、LLM 理解合同
internal/askdata/binding         Joint Binder、Beam Search、置信度校准
internal/askdata/ir              Semantic IR schema 与规范化 hash
internal/askdata/compiler        IR -> Dataset Query DSL -> SQL
internal/askdata/validator       权限、AST、成本、Join、结果校验
internal/askdata/toolhost        typed tools、预算、脱敏审计
internal/askdata/orchestrator    状态机、循环、澄清和终止条件
internal/askdata/evaluation      黄金集、结果等价、发布门禁
internal/askdata/feedback        反馈、错误分类、候选词挖掘
internal/askdata/http            Question/Admin/Evaluation API
```

### 13.2 对现有模块的最小改造

- `internal/ai`：增加 `PurposeSemanticQuestion`、动作 JSON Schema、完整 Tool Message 回传和模型级循环预算；仍由外层 Orchestrator 决定工具。
- `internal/querycompiler`：增加 Semantic Query 输入适配器；保留原编译器白名单，不让问数路径接受用户表名。
- `internal/queryruntime`：增加 `QUERY_TYPE=SEMANTIC_QUESTION`、结果 hash、EXPLAIN 成本和验证查询审计。
- `internal/policy`：提供按对象 ID 批量过滤语义候选的接口，保证召回前权限裁剪。
- `cmd/worker`：加入 embedding、dimension profile、semantic release projector 和 evaluation job；规模增大后再拆进独立 worker process。
- `cmd/api`：装配 Question API、Tool Host、Graph Adapter、Retriever 和 Orchestrator。
- `internal/config`：增加 NebulaGraph、问数预算、阈值、评测和 release 配置；生产必须显式设置。

### 13.3 API

```text
POST   /api/v1/questions
GET    /api/v1/questions/{runId}
GET    /api/v1/questions/{runId}/events
POST   /api/v1/questions/{runId}/clarifications
POST   /api/v1/questions/{runId}/feedback

GET    /api/v1/askdata/metrics
POST   /api/v1/askdata/metrics
GET    /api/v1/askdata/dimensions
POST   /api/v1/askdata/terms
POST   /api/v1/askdata/releases
POST   /api/v1/askdata/releases/{id}/validate
POST   /api/v1/askdata/releases/{id}/activate
POST   /api/v1/askdata/evaluations
GET    /api/v1/askdata/evaluations/{id}
```

`POST /questions` 返回 `runId` 并开始 SSE；事件只包含可展示状态、候选解释和证据摘要，不输出 prompt、SQL 参数明文或思维链。

### 13.4 ORCH-005 已实现 API 合同（2026-08-06）

- 正式路径固定为 `POST /api/v1/questions`、`GET /api/v1/questions/{runId}`、
  `GET /api/v1/questions/{runId}/events`、`POST /api/v1/questions/{runId}/clarifications`；
  全部复用现有 Bearer token、实时 session 和业务域门禁。
- 创建请求要求 `Idempotency-Key`，只把规范问句和幂等键转换成域分离 SHA-256；未显式传入
  `conversationId` 时，由 actor/domain/幂等 hash 确定性生成 UUID，精确重试返回原 run。
- Get/SSE 每次按当前 ACTIVE roles 重算 PolicyScope；创建时固定 ACTIVE release，读取历史 run 时使用
  该 run 的 pinned release/hash，因此 release 被 supersede 后仍可重放，但角色或业务域变化会失败关闭。
- SSE 的 `id` 是数据库 append-only `event_index`；`Last-Event-ID` 只接受 `0..1000000` 十进制整数，
  只发送更大的连续事件。公开事件最大 16 KiB，不包含内部 `Details`、AI request、Tool 原始 payload、
  prompt、SQL、参数、结果行或敏感成员值。
- 澄清请求只接收 completion artifact 已公布的稳定 `optionId`，并创建同 actor/domain/conversation/
  policy/release 的 child run；不接收自由文本。原问句短期加密保留、工件 TTL 和会话删除属于
  `ORCH-006`，不由 HTTP 层提前定义。

## 14. React 页面设计

新增 `/ask-data` 工作台：

- 左侧会话历史和常用问法；
- 中间对话、状态进度、定向澄清卡片、结果表/图；
- 右侧“理解与证据”面板，展示已绑定指标定义、维度、筛选、时间、数据新鲜度和来源版本；
- 结果支持表格、折线、柱状和 KPI 卡；LLM 判断最符合用户意图的表达方式，确定性规则验证图表与结果形状、字段类型和数据量兼容后执行；
- 用户反馈必须选择错误类型：指标错、维度错、维度值错、时间错、结果错、权限/数据问题或其他；
- 管理端增加指标、维度成员、术语、关系、发布和黄金集页面。

澄清卡片应问具体冲突，例如：

> “销售额”存在两个可用口径：① 已支付订单销售额（扣除取消，不扣退款）；② 净销售额（已支付且扣除退款）。请选择本次口径。

不要只提示“问题不清楚，请重新输入”。

### 14.1 WEB-001 已确认实现基线（2026-08-06）

- 用户已确认方案 3「证据驾驶舱」，实现继续复用现有 248px `AppShell`、Haier Logo、
  `#2864DC` 主色、白色细边框卡片、Phosphor 图标和全局字体，不建立第二套设计系统。
- `/ask-data` 已接入 `RequireAuth + RequireBusinessDomain`；左栏承载会话筛选和常用问题，
  中栏承载业务语言提问、受控阶段、结论、ECharts 渠道贡献图和语义化表格，右栏承载
  问题理解、口径权限、数据链路、质量新鲜度和反馈。
- `WEB-001` 只包含页面内 typed mock 和确定性 650ms 状态演示；不创建 run、不连接 SSE、
  不持久化会话/反馈，也不把模拟状态解释为真实后台结果。真实合同继续由 `WEB-002` 接入。
- 1280px 使用三栏；1180px 以下改为“会话 + 问答”两栏，证据面板移到下一行；继续遵循
  全站 1100px 桌面最小宽度。设计对照、差异修正和可访问性检查记录在 `design-qa.md`。

## 15. 安全、隐私与治理

1. 召回、GraphPlan、编译和执行均固定 `tenant_id + domain_id + actor_id + release_hash`。
2. 语义描述和认证样例按不可信数据处理，进入模型前去除可执行指令特征，防止元数据提示注入。
3. LLM 不获得数据库凭据、任意 SQL 工具、完整结果行或未授权对象描述。
4. 高敏维度成员不向量化、不进入模型上下文、日志或 evidence label；EXACT_ONLY 只走上述
   release-pinned 数据库函数，未授权与不存在保持同一零行结果；结果继续使用现有列掩码和行策略。
5. NebulaGraph 无 RLS，因此只能位于服务网络内，由 Graph Service 统一加 tenant/release 条件。
6. Query Runtime 使用独立只读账号、稳定视图白名单、只读事务、参数绑定和超时。
7. 缓存键必须包含 tenant、actor policy scope、semantic release、IR hash 和 warehouse snapshot/freshness；禁止跨租户复用 ANN/结果缓存。
8. 审计保存状态、对象 ID、hash、证据和错误码；原始问句按隐私政策加密短期保存或只保存 hash，不保存模型思维链。
9. 线上反馈不能自动修改生产映射，只能生成待审核语义变更。

## 16. 可观测性与 SLO

### 16.1 Trace

每个问题一条 Trace：

```text
authorize -> normalize -> understand -> retrieve_metric
-> retrieve_dimension -> lookup_member -> graph_resolve
-> bind -> compile -> explain -> execute -> verify -> render
```

Span 只记录对象 ID、候选数量、分数区间、版本和耗时，不记录敏感值。

### 16.2 核心指标

- `askdata_e2e_strict_accuracy`
- `askdata_direct_answer_coverage`
- `askdata_clarification_rate`
- `askdata_metric_binding_accuracy`
- `askdata_dimension_binding_f1`
- `askdata_member_binding_f1`
- `askdata_retrieval_recall_at_k`
- `askdata_graph_plan_failure_rate`
- `askdata_sql_validation_failure_rate`
- `askdata_result_validation_retry_rate`
- `askdata_release_projection_lag_seconds`
- `askdata_question_latency_seconds`
- `askdata_llm_calls_per_question`
- `askdata_tool_calls_per_question`
- `askdata_cost_per_answer`

建议体验目标：精确 fast path p95 小于 3 秒；普通未缓存问题 p95 小于 12 秒；复杂循环问题最大 25 秒。准确率门禁优先于延迟，超预算时应澄清或阻断，不应跳过校验。

## 17. 部署架构

### 17.1 开发环境

现有 `compose.yaml` 已落地：

```text
nebula-metad
nebula-storaged
nebula-graphd
nebula-init
nebula-verify
nebula-loopback-proxy  # 仅显式本地访问/验收，且必须等待 init 成功
```

使用单副本、固定命名卷和幂等初始化 Job 创建并核验 Space/Tag/Edge。API 仅持有图只读账号，
Worker 持有投影写账号；Connection Test Worker、Connector 和 Web 不持有图凭据，也不加入图
客户端网络。本地非 Compose 进程通过角色 wrapper 删除 root、bootstrap 和另一运行角色的
canonical secret。正式 Graph Compose 验收必须使用同一 nonce 派生的唯一 project/Space、随机
loopback 端口和独立卷，并用专用 override 禁止所有应用 service `env_file`；退出只删除该隔离
project 的容器/网络/卷，不操作开发共享 Space 或其持久卷。
CI 使用相同隔离流程。开发明文连接、单副本和环境变量凭据不能直接提升为生产配置。

### 17.2 生产环境

- API 无状态多副本；Question 状态写 PostgreSQL，SSE 可由 run ID 重连。
- Worker 按 `embedding/indexer/projector/evaluator` 队列独立扩缩。
- PostgreSQL 控制库高可用，pgvector 索引按租户/业务域规模分区。
- NebulaGraph 使用多 metad、storaged 和 graphd 节点；replica、partition 和容量由数据量 POC 决定。
- 数仓继续独立只读账号；问数不通过 Connector 直查外部 MySQL/Oracle，以减少数据版本和方言不确定性。
- MinIO 保存离线评测明细和可审计工件；在线状态和语义事实仍在 PostgreSQL。

## 18. 分阶段实施计划

### Phase 0：范围与基线（2 周）

交付：

- 选择 1 个业务域、20～50 个核心指标、20～40 个维度；
- 从真实问句抽取 500 条开发集、200 条验证集，定义 2,000 条密封集建设计划；
- 完成当前 DWS/ADS 视图、粒度、字段和质量盘点；
- 固化准确率、覆盖率、安全和延迟口径。

退出条件：指标 Owner、黄金结果和首期边界均明确。

### Phase 1：语义注册表与数仓接入（4～6 周）

交付：

- `askdata` schema、RLS、版本、发布和 outbox；
- Dataset DSL/DWS 资产导入；
- 指标、维度、成员、关系管理 API；
- 维度画像与成员索引策略；
- 管理端基础页面。

退出条件：核心指标和维度 100% 有 Owner、定义、粒度、来源和质量状态。

### Phase 2：混合检索与联合绑定（4～6 周）

交付：

- 精确词典、规则、pgvector 分类索引、RRF、重排；
- NebulaGraph schema、投影 Worker 和 Graph Resolver；
- QuestionUnderstanding、Joint Binder、置信度校准和澄清；
- mention-level 评测。

退出条件：召回指标达到 99%，指标/维度/成员绑定达到 97%。

### Phase 3：IR、SQL 与 Tool Loop（4～6 周）

交付：

- Semantic IR 和确定性编译；
- Tool Host、Question Orchestrator、SSE；
- EXPLAIN、Join/fanout、结果验证；
- React 问数工作台；
- 全链路审计和可观测性。

退出条件：开发集端到端严格正确率达到 96%，P0 无错误。

### Phase 4：密封评测、Shadow 和 Canary（4 周）

交付：

- 2,000+ 双人复核密封集；
- Release Gate 和版本回滚；
- 线上流量 Shadow，不影响用户结果；
- 5%/20%/50% Canary，逐步开放直接回答。

退出条件：密封集严格正确率 >=96%、Wilson 下界 >=95%、直接回答覆盖率 >=85%、安全集 100%。

### Phase 5：扩大业务域（持续）

每增加一个业务域，都要重复“语义覆盖—成员治理—黄金集—发布门禁”，不能因为第一个业务域通过就默认全公司达到 95%。

## 19. 人员建议

首期 18～24 周的最小团队：

- 2 名 Go 后端；
- 1 名前端；
- 1 名数据工程/数仓工程师；
- 1 名算法/LLM 工程师；
- 1 名 QA/数据分析评测负责人；
- 0.5 名 DevOps；
- 每个业务域至少 1 名指标 Owner/数据 Steward 持续参与。

没有业务指标 Owner，仅由研发根据字段名自动生成指标，95% 目标不可实现。

## 20. 主要风险与应对

| 风险 | 后果 | 应对 |
|---|---|---|
| 指标定义本身冲突 | 系统无法判断哪个“销售额”正确 | Owner、版本、适用范围、澄清、发布治理 |
| 维度成员规模过大 | 向量成本高、召回污染 | FULL/EXACT_ONLY/ON_DEMAND/NONE 策略 |
| ANN 过滤导致漏召回 | Top K 缺少正确候选 | 分区、iterative scan、exact 对照和 recall 监控 |
| 图投影与注册表不一致 | 关系路径错误 | release hash、水位门禁、固定版本 |
| LLM 供应商差异 | JSON/动作协议不稳定 | 严格 Schema、应用层 Tool Loop、Provider 契约测试 |
| Join 扇出 | 指标被重复累计 | 基数合同、预聚合、probe、结果唯一性检查 |
| 大量澄清降低体验 | 准确但不可用 | 提升词典/样例覆盖，同时约束直接回答覆盖率 |
| 反馈直接污染语义 | 错误快速扩散 | 反馈只生成候选，双人审核后进入 release |
| 历史退役 schema 被误恢复 | 与当前验证和代码冲突 | 新建 `askdata` schema、新迁移、禁止回滚 000195 |

## 21. 最终验收清单

### 语义治理

- [ ] 每个生产指标有唯一 Owner、版本、定义、单位、粒度、默认过滤和来源。
- [ ] 每个可问维度有成员索引策略、敏感性、层级和更新时间。
- [ ] 每条生产 Join 有基数、fanout 和时间有效性证明。
- [ ] 活动 release 的四个投影 content hash 完全一致。

### 检索与理解

- [ ] 指标/维度 recall@10 >=99%，成员 recall@20 >=99%。
- [ ] 词典覆盖高频问法，跨域同名词不会直接唯一绑定。
- [ ] LLM 在资产、理解、候选、消歧、计划、异常、核验、反馈和发布阶段均有结构化认知输出；输出只包含阶段合同允许的字段、动作、稳定 ID 和证据引用。
- [ ] 低置信和残余 span 会触发澄清而不是强答。

### 编译与执行

- [ ] 所有 SQL 均来自 Semantic IR 确定性编译并参数化。
- [ ] 只查询已发布 DWS/ADS 视图，使用只读账号和只读事务。
- [ ] EXPLAIN、Join、超时、行数、质量和结果验证均生效。
- [ ] 问题、语义版本、plan hash、result hash 可回放审计。

### 95% 门禁

- [ ] 密封集 >=2,000 条且每条双人复核。
- [ ] strict accuracy >=96%，95% Wilson lower bound >=95%。
- [ ] answerable direct coverage >=85%。
- [ ] P0 和安全样本 100%，敏感泄漏为 0。
- [ ] Shadow 和 Canary 无显著线上回归。

## 22. 下一步建议

第一步不要先选更大的模型，而应先选定一个业务域，导出以下四份清单：

1. 当前发布的 DWS/ADS 数据集、物化视图、字段和粒度；
2. 该业务域前 20～50 个指标及业务定义；
3. 这些指标允许使用的维度和维度成员规模；
4. 最近 3～6 个月真实问数问题的脱敏样本及标准答案。

拿到这四份清单后，可以把本文继续拆成数据库迁移、Go 接口、NebulaGraph DDL、Semantic IR JSON Schema、React 页面和首批黄金集的具体研发任务。
