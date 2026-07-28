# Semantic QA API

Base path：`/api/v1/semantic-qa`
鉴权：Bearer access token
权限对象：`DATASET`；读取接口要求 `READ`，治理接口要求 `MANAGE`
请求 JSON 使用严格字段校验，未知字段返回 `400 INVALID_REQUEST`。

## 多指标问答轮次

`POST /api/v1/semantic-qa/query-turns`

```json
{
  "question": "其中的关键人才有多少？",
  "priorQuestions": ["80后小微在职人员有多少人？"],
  "contextQueryPlanIds": ["上一轮全部 READY / EXECUTED 计划 ID"],
  "maximumPathHops": 8
}
```

`priorQuestions` 最多两个，仅作为本次响应的三轮意图综合证据，不写入
QueryPlan；`contextQueryPlanIds` 才是条件继承的安全锚点。当前轮命中的同维度
值覆盖上下文，其余维度、时间和指标条件只从已验证计划继承，不能从原始问句
猜测。

响应中的 `plans` 为每个指标各自独立的 QueryPlan。客户端必须仅执行状态为
`READY` 的完整计划集合；任何计划为 `AMBIGUOUS / GAP / REJECTED` 时不得用
其余指标的部分结果回答整轮问题。

```json
{
  "questionHash": "64 位摘要",
  "intent": "METRIC",
  "metricCodes": ["sales_amount", "order_count"],
  "contextQueryPlanIds": [],
  "contextInherited": false,
  "trace": {
    "conversationQuestions": ["最近三轮问题，当前问题在最后"],
    "standaloneQuestion": "由最终治理条件形成的独立问题",
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

执行请求必须回传创建计划时的 `expectedGraphGenerationId` 和 `expectedPathHash`，并提供调用方生成的 UUID `queryId`。`maxRows` 范围为 0–500。

单个或复合成员查询在执行时从证据表重新取得仍有效的 `member_key`，由指标服务注入参数化 `EQUALS / IN` 过滤。时间短语及比较窗口在计划阶段冻结为精确范围，再由指标发布定义中的 `timeFieldId` 注入参数化 `GTE / LT` 过滤；趋势查询自动加入该时间维度，排名查询按指标排序并以 `topN` 收紧最大行数。运行时再次校验：

- current graph generation 与水位；
- PUBLISHED 指标及精确版本；
- PUBLISHED DWS 及精确版本；
- VERIFIED、非 UNSAFE 的维度兼容关系；
- 同版本、同 schema hash 的 ACTIVE 物化；
- 对象权限、行列策略和查询限额。

响应包含结果、`durationMs / rowCount` 和 `AnswerEvidence`。不会返回 SQL 或物理表名。执行期间 generation 变化会丢弃结果并返回冲突。

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
| `POST` | `/golden-questions` | 在 DRAFT set 中登记 hash、fixture 和预期路径 |
| `GET` | `/golden-questions?setId=...` | 列出问题 |
| `POST` | `/golden-questions/{id}/replay` | 在 current generation 重放规划 |

指定模板时模板必须 ACTIVE，且模板 intent 必须与 fixture intent 一致。原问题和原始结果行不进入回放表；回放保存 generation、plan、failure stage 和通过/失败状态。

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
