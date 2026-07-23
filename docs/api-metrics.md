# 指标 API v1

指标接口基路径为 `/api/v1`，使用 Bearer 访问令牌，并由服务端从令牌上下文确定租户。请求体必须是单个 JSON 文档，拒绝未知字段，最大 2 MiB。指标定义合同见
[`docs/metric-definition-v1.md`](metric-definition-v1.md)。

## 1. 权限与接口目录

| 方法与路径 | 权限动作 | 说明 |
| --- | --- | --- |
| `POST /metrics` | `METRIC:MANAGE` + `DATASET:READ` | 创建指标和首个可变草稿。 |
| `GET /metrics?limit=&offset=` | `METRIC:READ` | 列出指标；默认 `limit=50`，范围 1–200，`offset` 不小于 0。 |
| `GET /metrics/{id}` | `METRIC:READ` | 读取指标主对象和当前草稿。 |
| `PUT /metrics/{id}/draft` | `METRIC:MANAGE` + `DATASET:READ` | 使用三重乐观锁更新草稿。 |
| `POST /metrics/{id}/validate` | `METRIC:MANAGE` + `DATASET:READ` | 校验当前草稿，请求体必须是 `{}`。 |
| `POST /metrics/{id}/preview` | `METRIC:READ` + `DATASET:READ` | 试算当前草稿。 |
| `POST /metrics/{id}/publish` | `METRIC:PUBLISH` + `DATASET:READ` | 校验试算后发布不可变版本。 |
| `GET /metrics/{id}/versions?limit=&offset=` | `METRIC:READ` | 列出发布版本摘要。 |
| `GET /metrics/{id}/versions/{versionId}` | `METRIC:READ` | 读取精确不可变版本。 |
| `POST /metrics/{id}/versions/{versionId}/preview` | `METRIC:READ` + `DATASET:READ` | 试算精确的 `PUBLISHED` 版本。 |
| `GET /metrics/{id}/versions/{versionId}/usage` | `METRIC:READ` | 返回该精确版本的聚合占用计数。 |
| `POST /metrics/{id}/versions/{versionId}/status` | `METRIC:PUBLISH` | 手工执行受控状态迁移。 |
| `POST /metrics/{id}/query-runs/{queryId}/cancel` | `METRIC:READ` | 取消统一查询运行时中的查询；当前不提供精确指标版本归属保证，边界见下文。 |
| `GET /metrics/semantic-search?q=&limit=` | 全局 `METRIC:READ` + 全局 `DATASET:READ` | 用向量与关键词混合检索当前正式发布指标。 |
| `POST /metrics/ai/proposals` | 全局 `METRIC:MANAGE` | 基于调用者可读的内部精确版本生成只读指标创建、新建数据集或普通数据集改造提案。 |

对象接口还会校验调用者对相应指标对象的访问权限。凡是创建、修改、校验或执行指标定义的路径，还会复核精确数据集的 `DATASET:READ`，并要求 `datasetVersionId` 指向当前状态仍为 `PUBLISHED` 的不可变版本；普通数据集版本来自人工发布审批，带 `originTableId` 的初始映射镜像来自受控系统发布。接口不会接受数据集草稿、待审批/被拒绝申请、失效版本，也不会按 `datasetId` 回退到任何“当前版本”。定义包含 `METRIC_REF` 时，全部直接和传递依赖都必须具备对象级或角色级 `METRIC:READ`。`MANAGE`、`PUBLISH` 不会隐式授予这些读取权限。目录、精确读取、草稿试算和精确版本试算接口均返回 `Cache-Control: no-store`。

## 2. 创建与读取

创建请求把完整定义放在 `definition` 中：

```http
POST /api/v1/metrics
Authorization: Bearer <access-token>
Content-Type: application/json

{
  "definition": {
    "schemaVersion": "1.0",
    "metric": {
      "code": "sales_amount",
      "name": "销售额",
      "description": "已确认订单金额",
      "type": "ATOMIC"
    },
    "datasetId": "11111111-1111-4111-8111-111111111111",
    "datasetVersionId": "22222222-2222-4222-8222-222222222222",
    "expression": {
      "type": "FIELD_REF",
      "fieldId": "order_amount"
    },
    "aggregation": "SUM",
    "unit": "元",
    "numberFormat": "#,##0.00",
    "timeGrain": "NONE",
    "additivity": "ADDITIVE",
    "nonAdditiveDimensionFieldIds": [],
    "allowedDimensions": [],
    "decimalScale": 2,
    "roundingMode": "HALF_UP",
    "nullHandling": "IGNORE",
    "divisionByZero": "NULL"
  }
}
```

成功返回 `201 Created` 和指标记录：

```json
{
  "id": "55555555-5555-4555-8555-555555555555",
  "code": "sales_amount",
  "name": "销售额",
  "description": "已确认订单金额",
  "type": "ATOMIC",
  "status": "DRAFT",
  "version": 1,
  "draftVersionId": "66666666-6666-4666-8666-666666666666",
  "draftVersionNo": 1,
  "draftRecordVersion": 1,
  "datasetId": "11111111-1111-4111-8111-111111111111",
  "datasetVersionId": "22222222-2222-4222-8222-222222222222",
  "definitionHash": "<服务端 SHA-256 摘要>",
  "definition": {},
  "createdAt": "<RFC 3339 时间>",
  "updatedAt": "<RFC 3339 时间>"
}
```

示例中的空 `definition` 仅用于压缩响应展示，真实响应返回完整、规范化后的定义。发布过的指标还会返回 `currentPublishedVersionId`。

列表响应格式为：

```json
{
  "items": [],
  "total": 0,
  "limit": 50,
  "offset": 0
}
```

## 3. 更新草稿与并发控制

更新必须同时回传最近一次读取到的指标主对象版本、草稿记录版本和定义摘要：

```json
{
  "expectedVersion": 1,
  "expectedDraftRecordVersion": 1,
  "expectedDefinitionHash": "<最近读取到的 definitionHash>",
  "definition": {
    "schemaVersion": "1.0"
  }
}
```

上例中的 `definition` 同样为省略展示；实际请求必须提交完整合法定义。三项事实有任意一项不匹配时返回 `409 METRIC_VERSION_CONFLICT`，客户端应重新读取记录后合并。更新不能修改 `metric.code` 或 `datasetId`；切换 `datasetVersionId` 时仍需通过精确已发布版本和依赖一致性校验。

当前草稿校验请求为：

```http
POST /api/v1/metrics/{id}/validate
Content-Type: application/json

{}
```

成功响应：

```json
{
  "valid": true,
  "definitionHash": "<规范定义摘要>"
}
```

## 4. 草稿与精确版本试算

草稿试算和精确发布版本试算使用同一请求格式：

```json
{
  "queryId": "77777777-7777-4777-8777-777777777777",
  "parameters": {
    "start_date": "2026-01-01"
  },
  "dimensionFieldIds": [
    "region"
  ],
  "maxRows": 100
}
```

`queryId` 和 `maxRows` 可省略；`parameters` 必须符合数据集参数合同，`dimensionFieldIds` 只能选择指标定义声明的维度且不能重复。服务端从可信的精确数据集版本派生执行计划，客户端不能提交 SQL、连接、过滤或数据源覆盖。

成功响应不包含生成 SQL：

```json
{
  "queryId": "77777777-7777-4777-8777-777777777777",
  "columns": [
    "region",
    "sales_amount"
  ],
  "rows": [
    [
      "华东",
      "12345.67"
    ]
  ],
  "rowCount": 1,
  "durationMs": 28,
  "warnings": []
}
```

只允许对状态为 `PUBLISHED` 的精确版本试算；`STALE`、`DEPRECATED` 或依赖不可用的版本返回 `409 METRIC_VERSION_UNAVAILABLE`，不会自动改用其他版本。

查询审计会固定 `metricId`、`metricVersionId`、当次执行的 `datasetVersionId` 和派生 `planHash`。写入时数据库会锁定并复核三者关系；草稿后续升级数据集版本不会级联改写既有查询审计。发布版本本身不可变，可由精确版本 ID 重放口径；草稿版本仍可修改，当前审计尚未保存可恢复的草稿定义快照，不能仅凭草稿版本 ID 还原历史定义。

取消接口当前先读取路径中的指标以确定 `datasetId`，再由统一查询运行时按 `tenantId`、调用者、`datasetId` 和 `queryId` 复核并取消。它尚未把查询审计中的 `metricId` 或精确 `metricVersionId` 与路径参数交叉校验，因此不能把该端点当作精确指标版本归属合同。调用方只能取消自己在同一数据集下启动且仍在运行的查询；补齐精确指标/版本绑定与对应失败关闭校验属于后续工作。

## 5. 发布与幂等

发布请求必须携带 1 到 128 字节、无首尾空白和控制字符的 `Idempotency-Key`：

```http
POST /api/v1/metrics/{id}/publish
Authorization: Bearer <access-token>
Idempotency-Key: metric-55555555-draft-1
Content-Type: application/json

{
  "draftVersionId": "66666666-6666-4666-8666-666666666666",
  "expectedVersion": 1,
  "expectedDraftRecordVersion": 1,
  "expectedDefinitionHash": "<最近读取到的 definitionHash>",
  "validationParameters": {
    "start_date": "2026-01-01"
  }
}
```

服务端锁定上述草稿事实，重新执行语义校验，并通过统一查询运行时执行最多一行的发布校验查询。校验、查询或风险检查任一失败都不会创建发布版本。成功返回 `201 Created` 和不可变 `VersionRecord`。

同一租户、指标和 `Idempotency-Key` 只能绑定同一发布请求。网络结果不确定时，客户端必须使用完全相同的键和请求体重试；服务端会重放已成功结果。相同键配不同请求返回 `409 METRIC_IDEMPOTENCY_CONFLICT`。

第一阶段发布试算仅支持精确绑定的已发布单源数据库数据集。以下情况均失败关闭：`CROSS_SOURCE`、Excel、存在行级或列级数据策略、预聚合数据集，以及失效或不精确的数据集版本。预聚合数据集通常会在更早的定义语义校验阶段被拒绝。

## 6. 版本读取、占用和状态迁移

版本列表响应与指标列表使用相同分页包络。精确版本记录包含：

- `id`、`metricId`、`versionNo`、`status`；
- 发布时对应的 `metricRecordVersion`、`draftVersionId`、`draftRecordVersion`；
- 精确的 `datasetId`、`datasetVersionId`、`definitionHash` 和完整 `definition`；
- `publishedAt`、`publishedBy`。

占用接口只返回计数，避免枚举调用者无权读取的下游对象：

```json
{
  "reportDraftReferences": 0,
  "downstreamDraftReferences": 1,
  "downstreamPublishedReferences": 0,
  "activeQueryRuns": 0
}
```

该结果是读取时快照，不是删除或迁移操作的强一致租约。报告侧精确 `metricVersionId` 绑定尚未端到端完成，`reportDraftReferences` 仍包含迁移期的主指标引用兼容统计。

当前公开服务只接受手工 `PUBLISHED` → `DEPRECATED`。如果仍有状态为 `PUBLISHED` 或 `STALE` 的下游指标版本依赖目标版本，迁移返回 `409 METRIC_VERSION_IN_USE`；必须先处理下游版本，不能制造仍标记可运行但依赖已废弃的版本：

```json
{
  "expectedVersion": 2,
  "expectedStatus": "PUBLISHED",
  "targetStatus": "DEPRECATED"
}
```

`expectedVersion` 使用精确版本记录返回的 `metricRecordVersion`。`STALE` 自动传播及其他迁移尚未实现，不应由客户端模拟。

## 7. 错误合同

错误响应至少包含稳定的 `code` 和中文 `message`。定义语义错误还包含可定位的 `details`：

```json
{
  "code": "METRIC_VALIDATION_FAILED",
  "message": "指标定义或发布校验失败",
  "details": [
    {
      "path": "expression.metricVersionId",
      "code": "METRIC_REFERENCE_UNAVAILABLE",
      "reason": "依赖指标版本不可用"
    }
  ]
}
```

| HTTP | `code` | 含义 |
| --- | --- | --- |
| 400 | `INVALID_REQUEST` | 请求体不是严格、单一的指标 JSON。 |
| 400 | `INVALID_PAGE` | 分页参数越界或格式错误。 |
| 400 | `INVALID_IDEMPOTENCY_KEY` | 发布幂等键不合法。 |
| 400 | `METRIC_DEFINITION_INVALID` | 定义格式无效或引用资源不可用。 |
| 400 | `METRIC_PREVIEW_INVALID` | 参数或服务端派生计划无效。 |
| 403 | `PERMISSION_DENIED` | 缺少指标操作权限。 |
| 404 | `METRIC_NOT_FOUND` | 指标不存在。 |
| 404 | `METRIC_VERSION_NOT_FOUND` | 精确指标版本不存在。 |
| 404 | `QUERY_RUN_NOT_FOUND` | 查询不存在、已结束或无权取消。 |
| 409 | `METRIC_VERSION_CONFLICT` | 草稿或主对象已被并发修改。 |
| 409 | `METRIC_IDEMPOTENCY_CONFLICT` | 幂等键已绑定其他发布请求。 |
| 409 | `METRIC_CODE_CONFLICT` | 租户内指标编码重复。 |
| 409 | `METRIC_VERSION_UNAVAILABLE` | 精确版本失效、废弃或依赖不可用。 |
| 409 | `METRIC_VERSION_TRANSITION_INVALID` | 状态迁移不受支持或前置事实已变化。 |
| 409 | `METRIC_VERSION_IN_USE` | 仍有可运行的已发布下游指标依赖目标版本。 |
| 409 | `QUERY_ID_CONFLICT` | 查询标识已被占用。 |
| 422 | `METRIC_VALIDATION_FAILED` | 定义或发布语义校验失败。 |
| 422 | `METRIC_PREVIEW_SOURCE_UNSUPPORTED` | 不是第一阶段支持的单源数据库执行场景。 |
| 502 | `METRIC_PREVIEW_FAILED` | 数据库试算执行失败。 |
| 503 | `METRIC_PREVIEW_UNAVAILABLE` | 指标试算服务暂时不可用。 |
| 504 | `METRIC_PREVIEW_TIMEOUT` | 试算超时且服务端已发起取消。 |
| 500 | `METRIC_PERSISTENCE_FAILED` | 指标持久化服务失败。 |

## 8. 尚未开放的客户端能力

API 中出现定义或持久化字段不代表以下端到端能力已经完成：一级维度对象、派生指标可视化编辑、`STALE` 自动传播、报告精确 `metricVersionId` 绑定，以及 `decimalScale`/`HALF_UP` 的跨引擎统一精确实现。客户端必须按失败关闭边界展示能力，不得静默降级到其他数据集或指标版本。

## 9. 数据集发布后的内部原子度量事实

普通数据集发布申请审批通过时，或映射表初始镜像由可信系统自动发布时，不可变 `PUBLISHED` 版本与提取任务在同一数据库事务提交；提交申请或保存草稿不会提前提取。后台 Worker 只读取该精确版本并执行 DAG 事实提取：明细兼容数据集形成记录数事实，标识符和输出粒度键形成实体去重数事实，数值度量、数值属性以及金额、数量、百分比语义字段形成聚合事实。明细表因此不会因为字段角色尚未标成 `MEASURE` 而遗漏可用构件。

确定性规则固定来源字段、表达式、聚合、允许维度、周期、过滤范围和结构化血缘；随后 LLM 只补全业务名称、说明、口径文字、周期说明、血缘摘要和标签，不能改变可执行事实。LLM 不可用或输出非法时保存规则降级结果，因此模型故障不会阻塞数据集发布。

这些记录是指标创作使用的内部、不可绑定构件，不是正式指标，不进入指标中心，不参与公开指标语义检索，也不提供接受、拒绝或直接创建指标的公共 API。新增指标时，指标 AI 可在调用者的数据集权限与字段可见性范围内读取它们，用于判断已有普通数据集是否已具备所需字段、聚合和口径；最终仍须由业务需求生成正式指标草稿并经过校验、试算和发布。映射表事实只能帮助设计新的普通数据集，不能令报表直接绑定映射表。

内部记录仍保存精确 `datasetVersionId`、`dslHash`、来源字段、规则证据、置信度、假设、告警、阻断原因和语义文档。适用行列策略、隐藏字段、非当前发布版本以及依赖不可执行的数据集不会进入 LLM 检索上下文。

### 9.1 指标语义检索

报表生成或其他指标发现流程调用：

```http
GET /api/v1/metrics/semantic-search?q=每个月各区域的销售情况&limit=10
Authorization: Bearer <access-token>
```

接口只返回当前精确 `PUBLISHED` 正式指标版本，并令 `bindingAllowed=true`。响应包含向量分、关键词分、融合分、embedding 是否就绪以及是否发生关键词降级。远程 embedding 失败不会令接口不可用，但响应的 `degraded=true`。内部原子度量事实永远不会从该接口返回。

指标正式发布时，数据库触发器自动为该精确版本生成语义文档。旧指标版本、旧数据集版本、内部原子事实、停用资产和非当前发布指针均不会进入检索结果。

## 10. LLM 指标创建提案

当前提示词合同版本为 `metric-authoring-v7`。

新建页以自然语言需求作为唯一必需输入，不要求用户预先拆分名称、口径、时间或数据集配置。用户也可以选择已发布数据集、统计对象、统计日期、维度和聚合方式作为可选参考；任一项都可以留空并交由 AI 判断。统计对象随聚合方式变化：`SUM/AVG/MIN/MAX` 仅展示数值度量或数值属性，`COUNT` 展示全部非日期可见输出字段且留空时允许 AI 判断记录数，`COUNT_DISTINCT` 优先展示标识符、维度和属性并允许数值业务字段。切换聚合时，前端会清除已不合法的统计对象。前端会把已选择的业务名称整理成固定的 `【AI 参考条件】` 文本块，与需求描述一起提交，不要求用户读取或填写数据集、版本、字段 UUID：

```json
{
  "requirement": "创建净销售额指标：已支付订单金额扣除退款金额，不含取消订单，按支付时间归属并默认按月分析"
}
```

服务端按互斥层级检索调用者有权访问的语义资产：当前发布指标、普通 `PUBLISHED` 数据集及字段、可管理的普通 `DRAFT` 数据集及字段、映射表 `PUBLISHED` 数据集及字段，以及当前发布 DAG 提取出的内部原子度量事实。当前边界最多容纳 128 个数据集快照、2048 个可见字段和 512 项内部原子事实；超过边界时失败关闭，不会把被截断的上下文伪装成“已检查全部数据集”。每个快照都固定精确版本 ID 与摘要。

普通数据集版本来自发布审批。数据集主对象存在不可变来源标识 `originTableId` 时，检索上下文会把受控系统发布的初始镜像隔离到映射表证据集合。映射表是对应表资产的单表来源镜像，只能作为 `CREATE_DATASET` 的 DAG 来源证据；即使字段已经足够，也不能直接创建指标或成为 `MODIFY_DATASET` 目标，避免报表长期绑定系统维护的映射镜像。

服务端在操作者同时拥有 `DATASET:READ` 和 `DATASET:MANAGE` 时，把当前普通数据集草稿及其可见逻辑字段放入完全隔离的改造上下文。草稿不会进入可创建指标的数据集候选，也不会作为指标表达式、维度或时间字段的所有者；映射表草稿不会进入指标 AI 编排。上下文不暴露源数据行、凭据、SQL、隐藏字段或外部互联网检索；存在适用行列策略或精确发布依赖不可执行的数据集会被排除。

模型必须依次选择：复用现有指标；在全部普通已发布数据集中零改动创建；在普通已发布或普通草稿中选择改动最少的一个进行修改；前三层均不可行时，才根据映射表证据新建普通数据集 DAG。模型应基于授权语义资产尽可能补齐配置，不得要求用户提供数据集、版本或字段的内部 ID。

提案策略为：

| `strategy` | 客户端行为 |
| --- | --- |
| `REUSE_METRIC` | 展示可复用的精确已发布指标版本，不新建。 |
| `CREATE_ON_DATASET` | 返回完整、已通过服务端结构校验的原子指标定义；客户端以只读配置展示，用户确认后调用普通指标创建接口生成草稿。 |
| `CREATE_DATASET` | 不返回指标定义，也不指定现有目标数据集；仅在普通数据集均不可行时，基于授权的映射表数据集和字段证据返回由数据集 AI 新建普通数据集的完整业务指令。 |
| `MODIFY_DATASET` | 不返回指标定义；只能选择 `manageable=true`、未聚合且不带 `originTableId` 的普通已发布数据集或授权草稿，并返回面向数据集 AI 画布的结构化改造目标。存在匹配普通草稿时优先改造草稿。 |
| `DATA_GAP` | 显示授权数据缺口，不猜测字段或口径。 |
| `NEEDS_CLARIFICATION` | 仅在无法形成任何安全候选或改造方案时，显示需要用户补充到自然语言需求中的问题。 |

`CREATE_DATASET` 必须令 `targetDatasetId`、`targetDatasetVersionId` 和 `candidateMetricDefinition` 为空，并用映射表数据集及字段 `retrievalEvidence` 证明新建设计来自授权数据；不能把映射表数据集伪装成直接指标或原地改造目标。`MODIFY_DATASET` 则必须固定一个授权的普通数据集及精确版本。两种策略的 `datasetInstruction` 都必须明确输入、关联、过滤、计算、输出字段与粒度，不包含 SQL，也不能只写“新增某指标”。

响应同时返回 `requestId`、`retrievalContextHash`、采用的精确证据、假设、非阻塞确认问题和告警。无论哪种策略，提案接口本身都不保存数据集、不创建指标、不执行查询，也不提交或审批数据集发布。`CREATE_ON_DATASET` 由用户检查 AI 生成的只读配置并明确确认后，客户端才调用普通创建接口保存指标草稿；服务端仍会复核定义绑定的是同一精确 `PUBLISHED` 数据集版本。`CREATE_DATASET` 确认后进入空白数据集画布，`MODIFY_DATASET` 确认后进入指定普通数据集画布；页面在画布和草稿加载完成后会自动发起一次数据集 AI 方案生成，不再要求用户额外点击“AI 生成/修改”。自动触发只生成待审核方案，不会代替用户应用方案、保存草稿或提交发布。审批通过并得到新的精确 `PUBLISHED` 版本后，客户端带原指标需求回到指标中心重新生成或创建指标。被拒绝、仍待审批、已经失效或只有草稿的版本都不能用于指标设计。

提案 UI 始终先展示业务化方案供确认，并提供“重新生成方案”和“根据意见修改方案”。重新生成沿用当前完整需求及可选参考条件；按意见修改会把补充意见与原需求、参考条件一起再次提交，不要求用户重述全部内容，合成后的请求最多 4000 个字符。请求追踪 ID、授权上下文哈希、数据集/版本 ID 和复用指标版本 ID 默认收在“技术信息”折叠区；业务概览、步骤、证据原因、假设、告警和澄清问题不得用这些内部标识代替业务名称。指标创建提案随租户通用 AI 开关启用，不要求将 `METRIC_AUTHORING` 加入独立用途白名单；调用仍受操作者状态、配额和审计约束。
