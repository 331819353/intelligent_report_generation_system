# Semantic QA API

Base path：`/api/v1/semantic-qa`
鉴权：Bearer access token
权限对象：`DATASET`；读取接口要求 `READ`，治理接口要求 `MANAGE`
请求 JSON 使用严格字段校验，未知字段返回 `400 INVALID_REQUEST`。

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
    {"dimensionCode": "channel", "memberValue": "APP"}
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

服务端只从该计划继承已证明的指标和维度编码；不会继承成员原值、结果行、
权限结论或旧图路径。当前追问仍会重新解析时间、成员和意图，并在 current
generation 上重新规划和校验，因此多轮上下文不能绕过语义图门禁。

`question` 原文只在本次解释过程中使用，持久化时仅保存 SHA-256。对象槽位为空时，解释器可以从租户内精确/文本/向量候选中选择；向量结果不会直接成为图关系。

`memberFilters` 最多 8 个，只允许不同维度各一个等值成员。每个成员都要
唯一命中有效成员/别名，并分别证明其维度与指标版本为 `VERIFIED` 且非
`UNSAFE`。持久化计划只保存成员证据 ID 和过滤数量；执行时重新取得仍
有效的 `member_key` 并参数绑定，原始成员值不会写入计划或日志。

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

单个或复合成员查询在执行时从证据表重新取得仍有效的 `member_key`，由指标服务注入参数化等值过滤。时间短语及比较窗口在计划阶段冻结为精确范围，再由指标发布定义中的 `timeFieldId` 注入参数化 `GTE / LT` 过滤；趋势查询自动加入该时间维度，排名查询按指标排序并以 `topN` 收紧最大行数。运行时再次校验：

- current graph generation 与水位；
- PUBLISHED 指标及精确版本；
- PUBLISHED DWS 及精确版本；
- VERIFIED、非 UNSAFE 的维度兼容关系；
- 同版本、同 schema hash 的 ACTIVE 物化；
- 对象权限、行列策略和查询限额。

响应包含结果、`durationMs / rowCount` 和 `AnswerEvidence`。不会返回 SQL 或物理表名。执行期间 generation 变化会丢弃结果并返回冲突。

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
