# 数据集 DSL API

基础路径：`/api/v1`。所有接口要求 Bearer Access Token，并从服务端身份上下文读取租户；客户端不能覆盖 `tenant_id`。

## 校验并规范化

`POST /datasets/validate`，需要 `DATASET:MANAGE`。

请求：

```json
{ "dsl": { "dslVersion": "1.0" } }
```

成功返回规范 DSL、`dslHash`、逻辑计划和 `planHash`。此接口不写数据库。

## 创建草稿

`POST /datasets`，需要 `DATASET:MANAGE`。

```json
{
  "code": "monthly_orders",
  "name": "月度订单数据集",
  "description": "按月份汇总有效订单金额",
  "type": "SINGLE_SOURCE",
  "dsl": {}
}
```

外层 `code/name/type` 必须与 DSL 的 `dataset` 一致。创建成功返回 `201` 和数据集当前草稿，包含 `version`、`draftVersionId`、`draftVersionNo`、`draftRecordVersion`、规范 DSL 及逻辑计划。存在发布版本时还会返回 `currentPublishedVersionId`；客户端不能用该指针代替需要精确版本 ID 的报告绑定或运行请求。

## 加载草稿

`GET /datasets/{id}`，需要 `DATASET:READ` 或对象级读取权限。跨租户 ID 在 RLS 下按不存在处理。该可变聚合响应携带 `Cache-Control: no-store`；客户端同样必须以 `cache: no-store` 读取，不能把浏览器或代理缓存中的旧 `version` 当作保存、发布后的并发基线。

`GET /datasets?limit=50&offset=0` 返回当前租户的数据集摘要目录，不携带完整 DSL。`limit` 范围为 1–200。

## 更新草稿

`PUT /datasets/{id}/draft`，需要 `DATASET:MANAGE` 或对象级管理权限。

```json
{
  "name": "月度订单数据集",
  "description": "调整后的说明",
  "expectedVersion": 1,
  "dsl": {}
}
```

`expectedVersion` 必须等于最近一次加载结果的 `version`。更新成功后版本加一；冲突返回 `409 DATASET_VERSION_CONFLICT`。`code` 和 `type` 不允许通过草稿更新改变。

## 发布不可变版本

`POST /datasets/{id}/publish`，需要独立的 `DATASET:PUBLISH` 全局权限或该数据集的对象级 `PUBLISH` 权限。`DATASET:MANAGE`、前端按钮状态和对象级 `READ` 都不能替代发布授权。

请求必须携带 1–128 字节、无首尾空白和控制字符的 `Idempotency-Key`。请求体把发布操作绑定到最近加载的确定草稿修订：

```json
{
  "draftVersionId": "7d84df84-537f-4218-954a-e2258a5e5dd1",
  "expectedVersion": 3,
  "expectedDraftRecordVersion": 3,
  "expectedDslHash": "38fb8de15798d3632b6319f9fc9ad1ae3211de69e43db0c7993947c099168fa3",
  "validationParameters": {
    "start_date": "2026-01-01",
    "regions": ["华东", "华南"]
  }
}
```

`validationParameters` 用于发布前的一行最小查询试跑。DSL 中没有默认值的必填参数必须在此提供；服务端统一执行参数类型、多值和必填校验。参数值不写入发布版本、发布幂等响应、通用审计或错误正文，只允许进入受控查询和不可逆请求/参数摘要。

发布流程会重新规范化 DSL，核对 DSL/计划哈希、物理资产、固定文件版本和全部启用的行列策略，要求所有 Join 已人工确认，并复用安全查询运行时执行 `VALIDATION` 试跑。跨源试跑返回的基数冲突、多对多和扇出告警会阻止发布；单数据源 MySQL/Oracle 会先为每条等值 Join 执行数据库侧聚合探测，再执行最终一行试跑。探测只返回两侧重复键组数、最大重复度和双侧重复键组数五个统计值，不把 Join 键、SQL、参数或样本带回响应与审计；非等值 Join 返回精确 `joins[i].conditions[j].operator` 路径并失败关闭。试跑或依赖复核失败时返回稳定 `details[].path/code/reason`，不创建半份发布版本，也不移动当前发布指针。

单源探测会应用节点 `sourceFilters` 和可证明仅引用该节点的聚合前过滤，并在同一次 25 秒上限、取消句柄和查询审计生命周期内完成。跨节点聚合前过滤当前不会反向缩小两侧基础键集合，因此风险判断是保守上界；复杂过滤下的精确 Join 后基数语义及大表探测代价门禁已记录到 TODO，不能通过返回业务键或截断样本规避。

成功返回新的不可变发布版本：

```json
{
  "id": "1725b5d9-6756-429d-94cc-f99f11ed23e1",
  "datasetId": "7b1956f7-e7d6-458b-bf5e-fad91a2f191d",
  "datasetRecordVersion": 4,
  "draftVersionId": "7d84df84-537f-4218-954a-e2258a5e5dd1",
  "draftRecordVersion": 3,
  "versionNo": 1,
  "status": "PUBLISHED",
  "dslVersion": "1.0",
  "dslHash": "38fb8de15798d3632b6319f9fc9ad1ae3211de69e43db0c7993947c099168fa3",
  "planHash": "10b20264e4c020fbd04d99dd51c09953fc5908381613d420ab6077d62249c95c",
  "dsl": {},
  "logicalPlan": {},
  "publishedAt": "2026-07-16T12:00:00Z",
  "publishedBy": "3737515e-4bd0-4faf-b7d4-e740350fcdf5"
}
```

服务端复制草稿内容生成独立 `PUBLISHED` 行，原草稿继续可编辑，首次正式发布版本号为 1。相同操作者使用同一个幂等键和完全相同请求时，在重新校验当前发布权限后精确重放首次响应；同键异载荷或其他操作者重放返回 `409 DATASET_IDEMPOTENCY_CONFLICT`。幂等重放先于草稿乐观锁检查，因此可安全覆盖“数据库已提交但客户端未收到响应”的重试。

客户端必须冻结请求体和幂等键；网络异常、超时、`5xx`、成功响应体截断或其他无法确认服务端结果的传输异常发生后，锁定编辑、保存和预览，只允许原样重试刚才的发布，不能依据已经变化的表单生成新键。模糊态重试若收到明确 `400/422`，清除候选并恢复编辑；收到 `401/403/404/409`，结束原样重试并进入重载核对。收到成功响应后再以禁缓存 GET 对账：只有草稿版本 ID、草稿记录版本和 DSL 哈希仍匹配，且 GET 的 `version` 不小于响应中的 `datasetRecordVersion`，才能采纳为新的本地并发基线；否则进入明确的重新加载状态，不能继续保存或发布。

当前发布试跑只支持由物理 `TABLE` 节点组成的数据集。DSL 中的 `DATASET` 节点仍可表达精确上游版本引用，但在递归执行、参数传播、循环检测及深度/扇出边界完成前，发布会返回对应 `nodes[i]` 路径和 `PUBLISH_DATASET_NODE_UNSUPPORTED`，失败关闭且不会回退到上游当前草稿或当前发布版本。

## 发布版本目录与精确加载

`GET /datasets/{id}/versions?limit=50&offset=0`，需要 `DATASET:READ` 或对象级读取权限，返回 `PUBLISHED`、`STALE` 和 `DEPRECATED` 发布版本摘要，不包含可变草稿或仅存在于事务内部的发布构建态。`limit` 范围为 1–200。版本目录、精确版本和占用统计的成功响应都使用 `Cache-Control: no-store`，避免状态迁移或发布后继续展示旧并发基线。

```json
{
  "items": [
    {
      "id": "1725b5d9-6756-429d-94cc-f99f11ed23e1",
      "datasetId": "7b1956f7-e7d6-458b-bf5e-fad91a2f191d",
      "versionNo": 1,
      "status": "PUBLISHED",
      "dslVersion": "1.0",
      "dslHash": "38fb8de15798d3632b6319f9fc9ad1ae3211de69e43db0c7993947c099168fa3",
      "planHash": "10b20264e4c020fbd04d99dd51c09953fc5908381613d420ab6077d62249c95c",
      "draftRecordVersion": 3,
      "publishedAt": "2026-07-16T12:00:00Z",
      "publishedBy": "3737515e-4bd0-4faf-b7d4-e740350fcdf5"
    }
  ],
  "total": 1,
  "limit": 50,
  "offset": 0
}
```

`GET /datasets/{id}/versions/{versionId}` 按父数据集和精确版本 ID 返回完整发布快照。版本必须同时属于 URL 中的数据集和当前租户；服务端不会根据 dataset ID 改读 `currentPublishedVersionId`，也不会把不存在、跨数据集或跨租户版本替换成其他版本。

## 发布版本占用统计

`GET /datasets/{id}/versions/{versionId}/usage` 使用与精确版本加载相同的读取权限和父级/租户校验，只返回当前控制库中可见的聚合数量，不返回报告、下游数据集、用户或查询 ID：

```json
{
  "reportDraftReferences": 2,
  "downstreamDraftReferences": 1,
  "downstreamPublishedReferences": 3,
  "activeQueryRuns": 1
}
```

`reportDraftReferences` 按报告去重；下游数据集引用按精确下游版本去重，并把 `DRAFT` 与 `PUBLISHED/STALE/DEPRECATED` 分开；`activeQueryRuns` 只统计仍为 `RUNNING` 的查询。软删除的报告或下游数据集不计入。该响应是界面提示使用的即时快照，不是删除/状态迁移租约；已发布报告版本引用要等 T0601 建立不可变报告依赖表后才能纳入，强占用阻断和租约语义不能由这些计数替代。

## 发布版本状态迁移

`POST /datasets/{id}/versions/{versionId}/status`，需要与发布相同的 `DATASET:PUBLISH` 全局权限或对象级 `PUBLISH` 权限。

```json
{
  "expectedVersion": 4,
  "expectedStatus": "PUBLISHED",
  "targetStatus": "STALE"
}
```

`expectedVersion` 是最近加载的数据集主对象 `version`，`expectedStatus` 是精确版本当前状态。只允许 `PUBLISHED -> STALE`、`PUBLISHED -> DEPRECATED` 和 `STALE -> DEPRECATED` 单向迁移；冲突或非法逆向迁移不会修改版本。目标是当前发布版本时，服务端清除 `currentPublishedVersionId` 并把数据集主状态同步为目标状态，表示当前没有可供新绑定使用的发布版本；原精确版本仍可通过版本目录和精确加载查看，其 ID 和不可变内容保持不变。

## 安全数据预览

`POST /datasets/{id}/preview`，需要 `DATASET:READ` 或对象级读取权限。

```json
{
  "queryId": "d7567ac1-dd36-4d16-aac4-65d48d491d74",
  "parameters": { "start_date": "2026-01-01", "regions": ["华东", "华南"] },
  "maxRows": 100
}
```

`queryId` 可省略并由服务端生成；设计器会预先生成 UUID，以便请求执行期间调用取消接口。参数必须在 DSL `parameters` 中声明，服务端会应用必填、默认值、标量/多值和 `STRING/INTEGER/DECIMAL/BOOLEAN/DATE/DATETIME` 类型校验。`maxRows` 不能超过 DSL `executionPolicy.previewLimit`；交互式预览超时取 DSL 配置与 25 秒中的较小值，避免超过 API 写超时。

成功响应：

```json
{
  "queryId": "d7567ac1-dd36-4d16-aac4-65d48d491d74",
  "columns": ["stat_month", "revenue"],
  "rows": [["2026-01-01", 12500.5]],
  "rowCount": 1,
  "durationMs": 18,
  "warnings": [
    {
      "code": "JOIN_CARDINALITY_MISMATCH",
      "message": "声明的 MANY_TO_ONE 基数与预览数据不一致：右侧 Join 键存在重复。",
      "joinId": "orders_customers",
      "estimatedRows": 320
    }
  ]
}
```

MySQL/Oracle 预览仅执行服务端从 DSL 生成的参数化 `SELECT`。物理表和字段来自当前租户的活跃资产白名单，执行前重新加载用户行列策略；Connector 再执行只读词法防护、连接并发限制和多取一行的结果上限检查。

Excel/CSV 预览使用 DSL TABLE 节点固定的 `fileVersionId`，不会回退到文件资产的可变“当前版本”。服务端先验证版本与数据源归属，再从对象存储读取对应版本，复核对象大小和 SHA-256，并用该版本的原始解析配置及真实表头执行过滤、同版本等值 Join、聚合、排序和行列策略。当前文件执行器最多保留 200,000 行中间结果；大文件流式/物化、非等值 Join 和跨源计划由 T0304 承担。

`CROSS_SOURCE` 实时预览支持 MySQL↔Oracle、数据库↔Excel 和 Excel↔Excel 等值 Join。执行器只向各源读取 Join、过滤和输出所需字段，可证明只引用单节点的前置过滤会参数化下推；规范化后的数据在网关完成 Join、最终聚合、排序和行列策略。单个数据集最多 16 个节点，每个源按预览行数放大读取且硬上限为 10,000 行；源返回超过上限时整次预览失败，不使用截断结果。当前不支持非等值 Join、缓存和物化。

当数据库节点在参与的每条 Join 中都是声明基数的“多”侧，且度量仅以直接字段形式参与 `SUM/MIN/MAX/COUNT/AVG` 时，执行器会按所有仍需保留的 Join、过滤和维度字段在源端预聚合，再由网关归并部分结果。COUNT 对部分计数求和并保持整数类型，AVG 使用部分 SUM/COUNT 计算加权平均；空集合分别保持 0/NULL，纯整数求和溢出会失败关闭。出现 `COUNT(*)`、`COUNT_DISTINCT`、复杂聚合参数、`AGGREGATE_ONLY` 最小分组人数、COUNT/AVG 的直接聚合策略校验、度量字段被非聚合复用、ONE 侧节点或文件节点时，会自动回退原始扫描，避免改变行数、去重或权限语义。所有标识符仍来自物理白名单，过滤值和行数限制仍使用参数绑定。

跨源预览会对每条 Join 边检查 `manualConfirmed`、声明基数和两侧键重复情况，并返回 `JOIN_CONFIRMATION_REQUIRED`、`JOIN_CARDINALITY_MISMATCH`、`JOIN_MANY_TO_MANY` 或 `JOIN_FANOUT_RISK`。告警仅包含 Join ID 和计数，不包含业务键值。执行器按实际有向 Join 顺序精确传播预览样本的必要键值，任一阶段预计输出超过 200,000 行时会在分配该中间结果前失败；INNER Join 使用较小输入构建哈希索引，外连接不交换声明两侧。

查询主审计写入 `platform.query_runs`，只保存执行计划哈希、参数绑定哈希、状态、耗时、行数和不含业务值的 `warnings_json`；跨源节点另写入 `platform.query_run_sources`，保存来源版本、水位、固定文件版本、不可碰撞的子查询 ID，以及可信网关采集的节点状态、实际输入行数和从读取到规范化完成的耗时。这里的节点行数是 Join 前通过网关形状与上限校验的接收行数，不是最终结果行数；启用源端预聚合时，它表示部分聚合行数而非数据库扫描的明细行数。成功查询必须具备全部节点指标；失败、超时或取消时，未完成节点会跟随主查询终态收口。两张表均不保存 SQL、参数明文、结果样本或源端错误文本，并强制启用租户 RLS。远端 Connector 返回的告警或节点指标不会被信任或写入审计。

## 精确发布版本预览

`POST /datasets/{id}/versions/{versionId}/preview`，需要同一数据集的 `DATASET:READ` 或对象级读取权限，请求与响应结构和草稿预览相同。

服务端只执行 URL 中的精确发布版本。版本状态/依赖摘要复核与可信物理计划解析在同一个租户事务和锁定边界内完成，事务外的 SQL 编译与执行只使用已经解析的固定物理引用；版本为 `STALE`、`DEPRECATED`，或发布时固定的物理表结构摘要、文件版本 SHA-256、上游数据集版本/计划摘要已经漂移时，返回 `DATASET_VERSION_UNAVAILABLE`。当前依赖漂移只会拒绝本次执行，不会在请求内自动改写版本状态；基于影响分析幂等传播 `STALE` 仍归入 T0307。此接口不会回退到当前发布指针或当前草稿。查询审计的 `dataset_version_id` 固定为请求中的版本 ID，`run_type` 为 `PREVIEW`。

## 取消预览

`POST /datasets/{id}/query-runs/{queryId}/cancel`，需要同一数据集的读取权限，且仅查询发起用户可取消仍处于 `RUNNING` 的记录。请求体为 `{}`；成功返回：

```json
{ "cancelled": true }
```

数据库查询由 Connector 取消；跨源数据库节点的子查询 ID 已持久化，其他 API 实例也可逐个发送取消。文件查询仍通过原执行实例的上下文取消，当前多副本部署需对同一文件查询使用粘性路由；文件共享取消协调已记录到 T0304。

## 错误码

| HTTP | code | 说明 |
|---|---|---|
| 400 | `INVALID_REQUEST` | 请求不是单一、严格 JSON 文档 |
| 400 | `INVALID_PAGE` | `limit` 不在 1–200、`offset` 为负数或分页参数不是整数 |
| 400 | `INVALID_IDEMPOTENCY_KEY` | 发布请求缺少合法的 1–128 字节 `Idempotency-Key` |
| 400 | `DSL-002-INVALID-DOCUMENT` | DSL 无法解析、版本不支持或基本信息不一致 |
| 401 | `ACCESS_TOKEN_REQUIRED` | 缺少有效访问令牌 |
| 403 | `PERMISSION_DENIED` | 无数据集权限 |
| 404 | `DATASET_NOT_FOUND` | 数据集不存在或不属于当前租户 |
| 409 | `DATASET_VERSION_CONFLICT` | 乐观锁版本冲突 |
| 409 | `DATASET_CODE_CONFLICT` | 租户内数据集编码已存在 |
| 409 | `DATASET_IDEMPOTENCY_CONFLICT` | 发布幂等键已由不同请求或操作者使用 |
| 409 | `QUERY_ID_CONFLICT` | 查询标识已被使用 |
| 422 | `DSL-001-VALIDATION-FAILED` | 领域校验失败，`details` 含 `path/reason` |
| 422 | `DATASET_PUBLISH_VALIDATION_FAILED` | 发布前 DSL、依赖、策略、Join 或查询试跑失败，`details` 含 `path/code/reason` |
| 422 | `QUERY-002-UNSUPPORTED-SOURCE` | 当前节点或数据源尚无安全执行器 |
| 400 | `QUERY-001-INVALID-PREVIEW` | 参数、行数限制或可执行表达式无效 |
| 404 | `DATASET_VERSION_NOT_FOUND` | 精确版本不存在、不属于 URL 中的数据集或不属于当前租户 |
| 409 | `DATASET_VERSION_UNAVAILABLE` | 精确版本不是可执行的 PUBLISHED 状态或发布依赖已经漂移 |
| 409 | `DATASET_VERSION_TRANSITION_INVALID` | 发布版本状态迁移不在允许的单向状态机内 |
| 404 | `QUERY_RUN_NOT_FOUND` | 查询不存在、已结束或当前用户无权取消 |
| 502 | `QUERY-004-EXECUTION-FAILED` | 数据源执行失败，内部错误不透出 |
| 504 | `QUERY-003-TIMEOUT` | 查询超时且已发起取消 |
| 503 | `DATASET_PUBLISH_UNAVAILABLE` | 发布校验执行器尚未装配，未创建发布版本 |
| 500 | `DATASET_PERSISTENCE_FAILED` | 数据集存储暂时不可用，响应不暴露内部错误 |

请求体上限为 2 MiB。错误响应不会返回 SQL、数据库内部错误或源端敏感信息。
