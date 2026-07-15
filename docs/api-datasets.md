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

外层 `code/name/type` 必须与 DSL 的 `dataset` 一致。创建成功返回 `201` 和数据集当前草稿，包含 `version`、`draftVersionId`、规范 DSL 及逻辑计划。

## 加载草稿

`GET /datasets/{id}`，需要 `DATASET:READ` 或对象级读取权限。跨租户 ID 在 RLS 下按不存在处理。

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
| 400 | `DSL-002-INVALID-DOCUMENT` | DSL 无法解析、版本不支持或基本信息不一致 |
| 401 | `ACCESS_TOKEN_REQUIRED` | 缺少有效访问令牌 |
| 403 | `PERMISSION_DENIED` | 无数据集权限 |
| 404 | `DATASET_NOT_FOUND` | 数据集不存在或不属于当前租户 |
| 409 | `DATASET_VERSION_CONFLICT` | 乐观锁版本冲突 |
| 409 | `DATASET_CODE_CONFLICT` | 租户内数据集编码已存在 |
| 409 | `QUERY_ID_CONFLICT` | 查询标识已被使用 |
| 422 | `DSL-001-VALIDATION-FAILED` | 领域校验失败，`details` 含 `path/reason` |
| 422 | `QUERY-002-UNSUPPORTED-SOURCE` | 当前节点或数据源尚无安全执行器 |
| 400 | `QUERY-001-INVALID-PREVIEW` | 参数、行数限制或可执行表达式无效 |
| 404 | `QUERY_RUN_NOT_FOUND` | 查询不存在、已结束或当前用户无权取消 |
| 502 | `QUERY-004-EXECUTION-FAILED` | 数据源执行失败，内部错误不透出 |
| 504 | `QUERY-003-TIMEOUT` | 查询超时且已发起取消 |
| 500 | `DATASET_PERSISTENCE_FAILED` | 数据集存储暂时不可用，响应不暴露内部错误 |

请求体上限为 2 MiB。错误响应不会返回 SQL、数据库内部错误或源端敏感信息。
