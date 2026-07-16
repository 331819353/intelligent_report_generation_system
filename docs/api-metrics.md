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

对象接口还会校验调用者对相应指标对象的访问权限。凡是创建、修改、校验或执行指标定义的路径，还会复核精确数据集的 `DATASET:READ`；定义包含 `METRIC_REF` 时，全部直接和传递依赖都必须具备对象级或角色级 `METRIC:READ`。`MANAGE`、`PUBLISH` 不会隐式授予这些读取权限。目录、精确读取、草稿试算和精确版本试算接口均返回 `Cache-Control: no-store`。

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
