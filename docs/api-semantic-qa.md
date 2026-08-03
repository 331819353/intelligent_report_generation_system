# Semantic QA API

主问答入口：`/api/v1/questions`
兼容入口：`/api/v1/semantic-qa`
鉴权：Bearer access token
权限对象：`DATASET`；读取接口要求 `READ`，治理接口要求 `MANAGE`
请求 JSON 使用严格字段校验，未知字段返回 `400 INVALID_REQUEST`。

## 统一 Question Orchestrator（主链）

新前端和新调用方只使用 `POST /api/v1/questions`。服务端在一个 Question 生命周期内
完成意图补全、三路路由、Semantic IR 构建、SQL Guard、执行、Result Verifier 和
确定性答案生成，不再要求浏览器先规划、并发执行计划后自行拼接答案。

生产环境会在规划前加载 PostgreSQL 中原子激活的 `semantic_release`，并要求该发布的
NebulaGraph 投影处于 `READY` 且 `semantic_version/content_hash` 完全一致。旧
PostgreSQL graph generation 只负责生成过渡候选和执行适配，不再是关系、权限或 Join
的最终裁决者。NebulaGraph 不可用且没有精确同版本认证缓存时，Question 进入
`BLOCKED`，不会降级到旧图执行。

```json
{
  "question": "华东区本月支付 GMV 是多少？",
  "conversationId": "会话 UUID",
  "timezone": "Asia/Shanghai",
  "locale": "zh-CN",
  "display": {"preferredChart": "AUTO"}
}
```

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| `POST` | `/api/v1/questions` | 创建并执行一个受治理 Question |
| `GET` | `/api/v1/questions/{id}` | 读取运行摘要、路由、版本和证据哈希 |
| `GET` | `/api/v1/questions/{id}/events` | 读取状态事件账本 |
| `POST` | `/api/v1/questions/{id}/clarifications` | 基于原问题哈希和受治理 ID 继续澄清 |
| `POST` | `/api/v1/questions/{id}/feedback` | 将准确/不准确反馈关联到本 Question 的全部计划 |
| `POST` | `/api/v1/questions/{id}/cancel` | 取消活动运行并写入 `CLIENT_CANCELLED` 阻断事件 |

状态机为：
`RECEIVED → AUTHORIZED → CONTEXT_READY → PLAN_READY → VALIDATING → COST_APPROVED → EXECUTING → RESULT_VERIFIED → ANSWERED`。
无法唯一证明语义时进入 `CLARIFICATION_REQUIRED`；门禁、执行、验证或取消失败进入
`BLOCKED`。事件包含安全的 `stage/status/code/summary`，不保存原始问句、提示词、SQL
或结果行。

路由严格限定为三类：

- A `SEMANTIC_IR`：默认路径，只接受已认证指标、维度和值，构建
  [`Semantic Query IR v1`](../api/schemas/semantic-query-ir-v1.schema.json) 后由确定性编译器执行。
- B `GOVERNED_TEXT_TO_SQL`：仅在目标方言存在可靠 AST 适配器时允许。当前仓库包含多种
  数据源方言，尚未配置统一可靠 AST 适配器，因此该路径明确返回能力关闭码
  `RELIABLE_DIALECT_AST_ADAPTER_NOT_CONFIGURED`，不会退化为执行模型自由 SQL。
- C `CLARIFY_OR_REFUSE`：证据不完整、存在歧义或治理条件不满足时最小澄清或拒绝。

成功响应同时返回 `understanding`、`graphPlan`、`executionRegistry`、`preflightProofs`、
`intent`、`semanticIr`、`executionGraph`、`sqlGuard`、
`resultVerification`、`answer`、`accuracyEvidence`、固定预算和 Host-owned Tool Registry
摘要。`understanding` 使用版本化 NFKC/case-fold Normalizer，返回规范化文本、原始
UTF-8 半开字节区间的 `AlignmentMap`、规则/认证 Alias Span、候选对象和认证示例 ID。
`graphPlan` 固定活动发布版本/内容哈希，并包含维度值复合身份、时间维度、认证数据集、
权限传播、最多四跳 Join 和图证据。冲突 Alias 最多取前三个候选 Bundle 做图闭包和权限
验证；只有多个合法 Bundle 仍会改变结果时才发起最小澄清。

`accuracyEvidence` 包含语义版本/内容哈希、Intent/Plan/Result 哈希、绑定证据、
GraphPlan/图证据、验证项和 Evidence Loop 新证据轨迹；答案文本只从已通过 Result
Verifier 的结果槽位生成。执行前后会重新确认活动发布指针和 NebulaGraph 投影 hash
没有漂移。

`executionRegistry` 证明活动发布的执行投影、原生指标/数据集当前发布指针、精确活动物化
和 DQ 规则一致；`preflightProofs` 只返回 PostgreSQL 方言、查询/参数 hash、引用字段 ID、
物化 ID、估算行数和成本，不返回生成 SQL 或参数值。路径 A 的查询必须先由 PostgreSQL
`EXPLAIN (FORMAT JSON)` 完成同方言解析，且成本在预算内。`EXPLAIN ANALYZE` 不会用于
预检。

Go Tool Host 的同步预算固定为最多 3 轮、12 次元数据工具、2 次 `EXPLAIN`、2 个主计划、
2 次验证和 60 秒。图闭包、编译、DQ、计划校验、预检、执行和结果验证均写入
`semantic_tool_calls` 追加式审计；表中只保存版本、PolicyScope/请求/结果 hash、证据、
预算和耗时，不保存原始问句、SQL、参数、结果行、提示词或模型思维链。

进度流仍使用 `Accept: application/x-ndjson`。Question 创建后，进度事件会带
`questionId`，客户端可用它调用取消接口；新增阶段包括 `ORCHESTRATION`、`SQL_GUARD`、
`EXECUTION`、`RESULT_VERIFICATION` 和 `ANSWER`。

下列 `/api/v1/semantic-qa/query-turns` 与 `/query-plans/{id}/execute` 仅保留给旧调用方
和治理调试页面。它们不是新产品问答主链。

## 兼容接口：多指标问答轮次

`POST /api/v1/semantic-qa/query-turns`

```json
{
  "question": "其中的关键人才有多少？",
  "timezone": "Asia/Shanghai",
  "priorQuestions": ["80后小微在职人员有多少人？"],
  "contextQueryPlanIds": ["上一轮全部 READY / EXECUTED 计划 ID"],
  "maximumPathHops": 8
}
```

歧义确认请求仍使用同一接口，只提交上一次响应中的受治理标识：

```json
{
  "question": "销售怎么样？",
  "timezone": "Asia/Shanghai",
  "confirmedMetricCodes": ["sales_amount"],
  "confirmedDecisions": [{
    "metricCode": "sales_amount",
    "decisionId": "维度 WHERE 决策图 ID"
  }],
  "maximumPathHops": 8
}
```

`confirmedMetricCodes` 最多 8 个，`confirmedDecisions` 最多 16 个。服务端会重新加载
当前 PUBLISHED 指标和决策图；请求不能提交表名、字段名、成员 SQL、WHERE 或物理
数据。

`timezone` 是 IANA 时区，缺省为 `UTC`，用于把分词阶段识别出的相对时间转换为
确定的半开区间。`query-turns` 会在服务端自动执行 Jieba/HMM，以及两个受控工具
循环：先检索指标语义并补全问题，再检索已发布指标清单；指标锁定后，再检索该指标
兼容的维度语义、成员和决策图。调用方不需要先请求独立测试页面或自行拼装
`semanticHints`。

指标锁定后，若问题中的维度和时间均可由已发布目录确定，分词补全会返回
`llmCompletion.model=DETERMINISTIC_SEMANTIC_CATALOG`，避免第二阶段不必要的补充
模型调用；这不会跳过第一阶段的指标语义与指标清单工具循环。已确认指标或已执行
上下文计划也可作为当前轮的可信指标锚点。相对时间、未知专名及未命中决策图的普通
维度仍进入受约束 LLM/向量链路，不会被快速路径猜测。
指标后缀、行政区划后缀、确定性剩余词和宽泛指标问法均来自平台基础 PostgreSQL
的 `semantic_parsing_rules`，按请求热加载；租户同类型同表达规则覆盖平台默认值。

指标已确定、但维度值无法匹配该指标已验证兼容维度或持久化决策图时，响应状态为
`SEMANTIC_GAP`，且 `clarification.type=SEMANTIC_GAP`。此时 `plans` 为空，
客户端必须展示缺口说明，不能降级成没有 WHERE 条件的查询。

`priorQuestions` 最多两个，仅作为本次响应的三轮意图综合证据，不写入
QueryPlan；`contextQueryPlanIds` 才是条件继承的安全锚点。当前轮命中的同维度
值覆盖上下文，其余维度、时间和指标条件只从已验证计划继承，不能从原始问句
猜测。

调用方需要在等待期间展示真实处理阶段时，可为同一请求增加
`Accept: application/x-ndjson`。服务端依次返回 `progress` 帧和最终 `result` 帧；
失败则返回 `error` 帧。进度事件只包含服务端阶段、状态、时间和安全文案，不包含
原始问题、工具参数、候选标识、提示词、SQL 或模型供应商载荷。

```json
{"type":"progress","progress":{"timestamp":"2026-08-03T08:00:00Z","stage":"METRIC_CATALOG","status":"RUNNING","message":"正在使用补全后的问题检索已发布指标清单"}}
{"type":"progress","progress":{"timestamp":"2026-08-03T08:00:01Z","stage":"METRIC_CATALOG","status":"SUCCEEDED","message":"指标清单检索完成，召回 3 个可用候选"}}
{"type":"result","result":{"status":"PLANNED","plans":[]}}
```

未设置该 `Accept` 头时继续返回原有的单个 JSON 响应，保持兼容。智能问答页面默认
使用进度流，并在进入执行阶段后继续显示受控查询的完成情况；最终用响应中的真实
候选级审计轨迹替换等待日志。

响应中的 `plans` 为每个指标各自独立的 QueryPlan。客户端必须仅执行状态为
`READY` 的完整计划集合；任何计划为 `AMBIGUOUS / GAP / REJECTED` 时不得用
其余指标的部分结果回答整轮问题。

响应同时包含 `questionRunId`、当前 `state` 和 `lifecycle`。规划链使用
`RECEIVED → AUTHORIZED → CONTEXT_READY → VALIDATING → PLAN_READY`；无法唯一证明
指标或维度时终止于 `CLARIFICATION_REQUIRED`。只有所有叶子计划均为 `READY`，
turn 才会返回 `PLANNED / PLAN_READY`，存在非 READY 计划时不会再被包装成可执行结果。
状态机会写入 `semantic_question_runs / semantic_question_run_events`；账本只保存运行
UUID、操作者、会话关系、问题哈希、路由、语义版本、计划/结果摘要哈希、状态和安全
事件摘要，不保存原始问题、提示词、SQL 或结果行。活动发布 ID、内容哈希、理解/图计划
hash 也会写入运行记录；`semantic_question_artifacts` 保存删除原问句和命中文本后的
Understanding、GraphPlan 与 Semantic IR，用于同版本重放。迁移时使用期望前置状态和
`recordVersion` 串行化，失败链会收敛到 `BLOCKED`。

```json
{
  "questionRunId": "本轮 UUID",
  "state": "PLAN_READY",
  "lifecycle": [
    {"state": "RECEIVED", "timestamp": "2026-08-03T08:00:00Z"},
    {"state": "PLAN_READY", "timestamp": "2026-08-03T08:00:01Z"}
  ],
  "questionHash": "64 位摘要",
  "status": "PLANNED",
  "intent": "METRIC",
  "metricCodes": ["sales_amount", "order_count"],
  "contextQueryPlanIds": [],
  "contextInherited": false,
  "tokenization": {
    "strategy": "JIEBA_HMM_POS_SEMANTIC_CATALOG_V1",
    "tokens": [],
    "questionMetricTop5": [],
    "semanticRetrievals": [],
    "llmCompletion": {"status": "SUCCEEDED"}
  },
  "trace": {
    "conversationQuestions": ["最近三轮问题，当前问题在最后"],
    "standaloneQuestion": "由最终治理条件形成的独立问题",
    "metricToolLoop": {
      "auditRequestId": "AI 审计 ID",
      "model": "MiniMax-M2",
      "rounds": 3,
      "toolCalls": 3,
      "steps": [
        {"round": 1, "toolName": "search_metric_semantics", "terminal": false},
        {"round": 2, "toolName": "search_metrics", "terminal": false},
        {"round": 3, "toolName": "submit_metric_selection", "terminal": true}
      ]
    },
    "dimensionToolLoops": [{
      "metricCode": "sales_amount",
      "auditRequestId": "AI 审计 ID",
      "model": "MiniMax-M2",
      "rounds": 3,
      "toolCalls": 3,
      "steps": [
        {"round": 1, "toolName": "search_dimension_semantics", "terminal": false},
        {"round": 2, "toolName": "search_dimension_decisions", "terminal": false},
        {"round": 3, "toolName": "submit_dimension_selection", "terminal": true}
      ]
    }],
    "extraction": {
      "intent": "METRIC",
      "metricTerms": ["员工总人数"],
      "dimensionValueTerms": ["80后", "在职", "关键人才"]
    },
    "metricCandidates": [{
      "code": "已发布指标编码",
      "label": "员工总人数",
      "matchMethod": "CONTEXT_PLAN",
      "selected": true,
      "source": "CONTEXT_PLAN"
    }],
    "dimensionValueLookups": [{
      "term": "关键人才",
      "metricFieldId": "field_employee_total_count",
      "dimensionCode": "key_talent",
      "dimensionName": "关键人才",
      "dimensionFieldId": "field_key_talent",
      "dimensionFieldName": "key_talent",
      "dimensionFieldDescription": "标识员工是否被评定为关键人才",
      "vectorQuery": "标识员工是否被评定为关键人才；值：关键人才",
      "vectorSearchStatus": "SUCCEEDED",
      "vectorCandidateCount": 54,
      "whereDesignStatus": "SUCCEEDED",
      "whereDesignOperator": "CONTAINS",
      "whereDesignReason": "该字段是标签组合，用户按关键人才标签筛选",
      "whereDesignModel": "已配置模型",
      "matchMethod": "SEMANTIC_TAG",
      "candidateCount": 54,
      "whereCondition": "key_talent LIKE '%关键人才%'",
      "compiledCondition": "field_key_talent IN (:key_talent_1 … :key_talent_54)",
      "candidateFilter": {
        "inputCount": 54,
        "acceptedCount": 54,
        "rejectedCount": 0,
        "status": "PASS"
      },
      "selected": true,
      "source": "CURRENT_TURN"
    }],
    "finalSelections": [{
      "metricCode": "已发布指标编码",
      "metricName": "员工总人数",
      "metricFieldId": "field_employee_total_count",
      "dimensions": [{"dimensionCode": "key_talent", "memberKeys": ["受控成员键"]}],
      "whereCondition": "key_talent LIKE '%关键人才%'",
      "compiledCondition": "field_key_talent IN (:key_talent_1 … :key_talent_54)",
      "planStatus": "READY"
    }],
    "assessments": [
      {"step": "CONTEXT_SYNTHESIS", "status": "PASS"},
      {"step": "INTENT_EXTRACTION", "status": "PASS"},
      {"step": "METRIC_RETRIEVAL", "status": "PASS"},
      {"step": "DIMENSION_VALUE_RETRIEVAL", "status": "PASS"},
      {"step": "FINAL_PLAN", "status": "PASS"}
    ]
  },
  "plans": [
    {"id": "销售额计划 ID", "status": "READY"},
    {"id": "订单量计划 ID", "status": "READY"}
  ]
}
```

无法唯一判断指标时返回 `status=NEEDS_METRIC_CONFIRMATION` 和
`clarification.metricCandidates`；每项包含指标名称、编码、领域、精确数据集版本和
来源 DWS 发布视图。无法唯一判断维度和值时返回
`status=NEEDS_DIMENSION_CONFIRMATION` 和
`clarification.dimensionCandidates`，每项同时返回原始 `term` 和确认键 `decisionId`。
智能问答页面允许一次确认多个指标，并按 `metricCode + term` 对维度歧义分组，每组
只能选择一个决策；服务端重新检索候选并执行同样的单词单决策约束。这两种响应都不会
执行未确认结果，确认后会在同一问答会话中自动继续规划和查询。

指标 Evidence Loop 强制执行 `search_metric_semantics → search_metrics →
submit_metric_selection`；维度工具循环强制执行
`search_dimension_semantics → search_dimension_decisions →
submit_dimension_selection`。两个循环均可在预算内重复检索，且终止值必须来自工具
候选。每个非终止调用必须产生新的 `evidenceId`，轨迹提供参数哈希、状态哈希与
新增证据数；重复检索无新证据会稳定失败。协议、预算和降级规则见
[多模型受控工具循环](ai-tool-loop.md)。

## 查询结果反馈

`POST /api/v1/semantic-qa/query-plans/{id}/feedback`

```json
{"rating":"INACCURATE","issueType":"FILTER","comment":"区域筛选应包含华北"}
```

只接受已经执行完成的计划，`rating` 为 `ACCURATE / INACCURATE`。准确时服务端固定
`issueType=NONE`；不准确时必须从 `METRIC_DEFINITION / FILTER / RESULT_VALUE /
PERMISSION / FRESHNESS / EXPRESSION / OTHER` 中选择错误类型。同一用户对同一计划再次
提交时更新为最新评价。智能问答页面可同时填写最多 2000 字的点评；多指标答案会把同一
评价写入本轮每个已执行计划。反馈不会直接修改语义资产、解锁发布或进入正式准确率。

多轮追问把上一轮全部 `READY / EXECUTED` 计划 ID 放入
`contextQueryPlanIds`，同时把前两次用户问句放入 `priorQuestions`。页面的
检索过程必须直接渲染 `trace`；旧计划没有 `trace` 时应明确标记为不可审计，
不得用最终条件反推伪造候选过程。详细继承和资产规则见
[多轮、多指标智能问答落地说明](semantic-chat-multi-turn-multi-metric.md)。

## 1. 租户开关与状态

| 方法 | 路径 | 用途 |
| --- | --- | --- |
| `GET` | `/settings` | 读取语义问答、图投影和问题变更开关 |
| `PUT` | `/settings` | 更新开关、最小路径置信度和最大跳数 |
| `GET` | `/graph/status` | 读取 current generation、水位、节点/边数和错误码 |
| `GET` | `/warehouse-dag?datasetVersionId=...` | 获取精确发布版本的构建 DAG 和拓扑序 |
| `GET` | `/analysis-templates` | 获取市场通用分析模板及自动生成策略 |

只有 `status=READY` 且 `appliedEventVersion=requestedEventVersion` 的图可用于新计划。

## 2. DAG ChangeSet

| 方法 | 路径 | 用途 |
| --- | --- | --- |
| `POST` | `/change-sets` | 创建结构化 patch ChangeSet |
| `POST` | `/change-sets/from-candidate` | 将旧设计器/问题流程的完整候选转换为有界 patch |
| `GET` | `/change-sets/{id}` | 读取操作、验证结果和状态 |
| `POST` | `/change-sets/{id}/validate` | 对冻结基线应用 patch 并运行本地合同校验 |
| `POST` | `/change-sets/{id}/apply` | 以乐观锁写入数据集草稿 |
| `POST` | `/change-sets/{id}/reject` | 拒绝提案并取消关联的未完成 run |

`triggerType` 为 `AUTOMATION / QUESTION / MANUAL`；`changeKind` 为 `CREATE_DATASET / MODIFY_DATASET / REPAIR_DAG`。Patch 根路径只允许 DSL 白名单组件，最多 256 个操作。接口不接受 SQL、DDL 或物理对象名。

修改现有草稿必须携带当前 `baselineDatasetVersion` 和 `baselineDslHash`。所有状态变化使用 `expectedRecordVersion`。

## 3. 消费合同与 ADS

| 方法 | 路径 | 用途 |
| --- | --- | --- |
| `POST` | `/consumer-contracts` | 创建 DRAFT 消费合同和精确 DWS 输入 |
| `GET` | `/consumer-contracts/{id}` | 读取合同 |
| `POST` | `/consumer-contracts/{id}/publish` | 以 `expectedVersion` 发布合同 |

ADS 的 `dataset.consumerContractId` 必须指向 PUBLISHED 合同。数据库发布触发器验证全部输入都是合同声明的精确 PUBLISHED DWS 版本，并检查所有 required 输入均存在。

## 4. 查询计划与执行

创建计划：

```http
POST /api/v1/semantic-qa/query-plans
Content-Type: application/json

{
  "question": "华东地区的销售额",
  "intent": "METRIC",
  "metricCode": "sales_amount",
  "dimensionCode": "region",
  "memberValue": "华东",
  "memberFilters": [
    {"dimensionCode": "channel", "memberValue": "APP"},
    {
      "dimensionCode": "birth_cohort",
      "memberValues": ["80-85", "85-90"]
    }
  ],
  "timeRange": {
    "start": "2026-07-01T00:00:00Z",
    "endExclusive": "2026-08-01T00:00:00Z"
  },
  "topN": 10,
  "sortDirection": "DESC",
  "maximumPathHops": 8
}
```

多轮追问可以携带上一轮成功计划：

```json
{
  "question": "那上个月呢？",
  "intent": "UNKNOWN",
  "metricCode": "",
  "contextQueryPlanId": "上一轮 READY 或 EXECUTED 计划 UUID"
}
```

服务端从该计划的持久化条件文档继承已证明的指标、维度编码、规范
`memberKey / memberKeys` 和时间范围；不会继承结果行、原始问句、权限结论
或旧图路径。当前追问新增的同维度条件替换旧条件，其他旧条件继续生效，并在
current generation 上重新规划和校验，因此多轮上下文不能绕过语义图门禁。

`question` 原文只在本次解释过程中使用，持久化时仅保存 SHA-256。对象槽位
为空时，解释器先从当前已发布指标清单召回至多 24 个候选；精确名称/编码优先，
先按 ODS 继承的有效领域分区，文本相似度和指标名称向量只用于排序，LLM 只能
复制候选指标编码。像“有多少人”这种已明确指标类型、但未说出目录指标名的问法，
只有在问题命中的已验证维度值决策边反向收敛到唯一已发布人数指标时，才允许以
`DECISION_GRAPH` 选中指标；零个或多个指标都会继续阻断，不能默认猜测。

指标版本确定后，服务端才会在其精确数据集版本及安全兼容维度范围内提取真实
维度值候选。同一文本值出现在多个维度时，LLM 只能结合完整问题、候选维度名、
逻辑字段名和字段描述复制其中一个维度编码；无法证明时保留歧义。维度检索向量
严格由 `字段描述:规范值` 组成，首先检索持久化的
`维度字段:维度值 → 指标字段 → 表/WHERE` 决策图。成员身份、集合大小和指标兼容
关系全部一致时直接返回 `REUSED_DECISION_GRAPH`；没有可复用决策时才把字段名、
描述、匹配方式和已选择的非敏感治理值交给 LLM 选择
`EQUALS / IN / CONTAINS`。LLM 不得输出 SQL、发明值、选择数据集或物理表；
敏感维度不会外发。模型决策还要经过字段和值白名单复核，最后由服务端以不可变
`fieldId` 参数化编译。详细规则见
[指标定位链路](./semantic-qa-resolution-chain.md)。

`memberFilters` 最多 8 个，只允许不同维度各一个过滤组。单值使用
`memberValue`；受治理集合使用 `memberValues`（2–128 个不同成员）。每个成员
都要唯一命中有效成员/别名，并分别证明其维度与指标版本为 `VERIFIED` 且非
`UNSAFE`。集合执行为一个参数化 `IN`，不会被错误编译为多个 `EQUALS AND`。
持久化计划只保存成员证据 ID 和过滤组数量；执行时重新取得仍有效的
`member_key` 并参数绑定，原始问句不会写入计划或日志。

多标签组合字段可由语义资产声明 `mappingValue=TAG:<规范标签>`，且
`knowledgeType` 必须等于目标维度名称/编码或通用维值类型。该映射只会按完整
逗号标签边界展开当前有效成员，不会生成自由 `LIKE / CONTAINS` 表达式。

`timeRange` 是左闭右开的受控边界，只接受同精度的 `YYYY-MM-DD`
或带时区 RFC3339；字段类型必须分别匹配 `DATE` 或 `DATETIME`。
`topN` 范围为 1–500，必须有明确维度；`RANKING` 未指定时默认
`topN=10, sortDirection=DESC`。时间边界始终转为参数绑定，Top N
始终收紧执行行数，排序只能使用服务端派生的指标字段，均不接受 SQL。

常见自然语言时间短语会先收敛为受控 `timePreset`。也可以由调用方显式提交：

```json
{
  "question": "最近 7 天各区域销售排行",
  "intent": "RANKING",
  "metricCode": "sales_amount",
  "dimensionCode": "region",
  "timePreset": "LAST_7_DAYS",
  "timezone": "Asia/Shanghai"
}
```

允许的 preset 为 `TODAY / YESTERDAY / LAST_7_DAYS / LAST_30_DAYS /
THIS_MONTH / LAST_MONTH / THIS_YEAR / LAST_YEAR`。未提供时区时使用
`UTC`；计划创建时会依据指标时间字段的 `DATE / DATETIME` 类型和指定
IANA 时区冻结为精确边界，因此执行重试不会因跨日或跨月而漂移。

`COMPARISON` 支持 `PREVIOUS_PERIOD / YEAR_OVER_YEAR / CUSTOM` 三种
模式。中文“环比/较上期”和“同比”会确定性映射到前两种；当前窗口必须
来自 `timeRange` 或 `timePreset`。`CUSTOM` 还必须显式提供
`comparisonRange`。计划会冻结当前与基准两个半开窗口，执行时使用两个
独立查询 ID、同一指标版本和同一证据路径，响应的 `result` 是当前窗口，
`comparison.baseline` 是基准窗口。

计划状态：

- `READY`：完整路径已由同一 generation 证明；
- `AMBIGUOUS`：同名指标、维度或成员无法唯一确定；
- `GAP`：对象不存在或能力缺失；
- `REJECTED`：关系、物化或源血缘无法证明；
- `EXECUTED / FAILED`：执行终态。

读取与执行：

| 方法 | 路径 |
| --- | --- |
| `GET` | `/query-plans/{id}` |
| `POST` | `/query-plans/{id}/execute` |
| `POST` | `/query-plans/{id}/feedback` |

执行请求必须回传创建计划时的 `expectedGraphGenerationId` 和 `expectedPathHash`，并提供调用方生成的 UUID `queryId`。`maxRows` 范围为 0–500。

单个或复合成员查询在执行时从证据表重新取得仍有效的 `member_key`，由指标服务注入参数化 `EQUALS / IN` 过滤。时间短语及比较窗口在计划阶段冻结为精确范围，再由指标发布定义中的 `timeFieldId` 注入参数化 `GTE / LT` 过滤；趋势查询自动加入该时间维度，排名查询按指标排序并以 `topN` 收紧最大行数。运行时再次校验：

- current graph generation 与水位；
- PUBLISHED 指标及精确版本；
- PUBLISHED DWS 及精确版本；
- VERIFIED、非 UNSAFE 的维度兼容关系；
- 同版本、同 schema hash 的 ACTIVE 物化；
- 对象权限、行列策略和查询限额。

响应包含结果、`durationMs / rowCount` 和 `AnswerEvidence`。执行响应同样带有
`questionRunId/state/lifecycle`，完整成功路径为 `RECEIVED → AUTHORIZED →
CONTEXT_READY → PLAN_READY → VALIDATING → COST_APPROVED → EXECUTING →
RESULT_VERIFIED → ANSWERED`。`AnswerEvidence` 额外提供 `semanticVersion`、
`queryPlanHash`、`resultHash`、`queryTraceId`、`verifiedAt` 与服务端命名的
`validatorChecks`，用于证明本次回答绑定的语义版本、计划和结果快照。不会返回
SQL、隐藏推理或物理表名。执行期间 generation 变化会丢弃结果并返回冲突。

计划中的 `resolution` 会按顺序返回 `INTENT_RECOGNITION /
DOMAIN_CATALOG / METRIC_CATALOG / DIMENSION_MEMBER / DATASET_LOCK` 五个阶段的状态、
候选数量和稳定决策码，便于解释本轮为什么选中该指标和数据集；其中不包含
问题原文或维度成员原值。

计划还会返回服务端生成的 `conditions`：包含领域、指标版本、精确数据集版本、
规范化 `dimensionCode / dimensionId / memberKey`；集合条件使用
`memberKeys`。该 JSON 还可包含受控时间窗口，
是受信查询编译器的输入，不包含 SQL 或物理表名。

## 5. 问题模板与黄金问题

| 方法 | 路径 | 用途 |
| --- | --- | --- |
| `POST/GET` | `/question-templates` | 创建/列出意图与必需槽位模板 |
| `POST/GET` | `/golden-question-sets` | 创建/列出版本化问题集 |
| `POST` | `/golden-question-sets/{id}/activate` | 原子激活版本 |
| `GET` | `/golden-question-sets/{id}/evaluation-gate` | 服务端复算正式发布门禁、Wilson 下界和首次错误阶段 |
| `POST` | `/golden-questions` | 在 DRAFT set 中登记 hash、fixture 和预期路径 |
| `GET` | `/golden-questions?setId=...` | 列出问题 |
| `POST` | `/golden-questions/{id}/replay` | 规划集重放路径；E2E 集走正式 Question Orchestrator 并比较结果 hash |

问题集显式声明 `datasetSplit=DEVELOPMENT / VALIDATION / SEALED /
PRODUCTION_REGRESSION` 和 `evaluationMode=FIXTURE_REGRESSION /
END_TO_END_RESULT_EQUIVALENCE`。规划回归只保存 question hash，不计入正式准确率；E2E
问题保存经脱敏和批准的问句，READY 样本必须有专家批准的 `expectedResultHash`。SEALED
集激活要求至少 2,000 条、每条 `independentReviewCount=2`，并生成不可变集合指纹。

`evaluation-gate` 只使用黄金运行，不使用点赞。它同时返回严格准确率点估计和 95%
Wilson 下界、P0 正确率、越权阻断率、敏感泄漏数、直接回答覆盖率、拒答精确率，以及
首次失败阶段分布。任一硬条件缺失时 `decision=BLOCKED`；这意味着工程能力已经存在，
但在真实业务 sealed 数据未录入前不会宣称达到 95%。运行记录不保存 SQL、参数或结果行，
只保存版本、路径和结果 hash、布尔安全事实与错误归因。

## 6. 物化建议

`GET /materialization-recommendations?lookbackDays=30&minimumHits=10`

返回 DWS 精确版本的计划命中数、不同问题数、平均/最大执行耗时和 ACTIVE 物化状态。策略始终是 `SUGGESTION_ONLY`：

- `KEEP`
- `PROPOSE_MATERIALIZATION_CHANGE_SET`
- `LOGICAL_ONLY`

该接口不会创建、发布、激活或退役物化。

## 7. 通用错误

| HTTP | code | 含义 |
| --- | --- | --- |
| 400 | `INVALID_REQUEST` | 输入违反语义合同 |
| 404 | `NOT_FOUND` | 对象不存在或租户不可见 |
| 409 | `CONFLICT` | 基线、状态、generation 或版本已变化 |
| 409 | `SEMANTIC_QA_DISABLED` | 租户能力未启用 |
| 503 | `GRAPH_NOT_READY` | 图尚未追平当前控制面 |
| 422 | `UNPROVEN_PATH` | 无法用权威事实证明可执行路径 |
