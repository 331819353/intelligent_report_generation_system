# 数据集物化控制面 API

物化控制面只负责从当前已发布的数据集事实派生不可变构建任务。客户端不能提交
执行计划、SQL、输入快照或任何源表/仓库物理名称。

## 权限与缓存

- 登记和取消构建需要目标对象的 `DATASET:MANAGE`。
- 列表和详情需要目标对象的 `DATASET:READ`。
- 权限对象 ID 始终是路径中的数据集 ID。
- 所有响应均带 `Cache-Control: no-store`。
- 所有读取和写入在租户 RLS 事务内执行。

## 登记当前发布版本

`POST /api/v1/datasets/{datasetId}/materializations/builds`

请求必须使用 `Content-Type: application/json`，且只能包含：

```json
{
  "mode": "FULL",
  "partitionKey": "",
  "maxAttempts": 3
}
```

- `mode` 可省略，当前默认且只支持 `FULL`。
- `partitionKey` 可省略，当前只允许空字符串。
- `maxAttempts` 可省略，默认 `3`，允许范围为 `1..10`。
- 未知字段、多份 JSON、超出 4 KiB 的请求体都会被拒绝。
- `plan`、`sql`、`inputs`、源物理名称和仓库物理名称不是该 API 的字段。

服务端在同一个事务内完成以下工作：

1. 锁定目标数据集的当前 `PUBLISHED` 版本，重新校验 DSL 摘要、层级和物化开关。
2. 从 DSL 派生无 SQL 的安全拓扑和 PostgreSQL 目标合同。
3. 冻结输入：
   - ODS 是来源上的虚拟字段映射，不是物化目标；对 ODS 调用本接口返回
     `MATERIALIZATION_INVALID_REQUEST`。
   - DIM/DWD 的 ODS 输入冻结为 `virtual-ods-source-v1` 来源快照：数据库输入固定当前已发布的
     `data_source_version`、元数据表和 `structure_hash`；Excel 输入还必须固定精确
     文件版本及其 SHA-256。Worker 正式执行时按该快照全量回源，并先投影 ODS 字段合同。
   - DWD 可附加已经在数仓中的 DIM；DWS 只允许一个或多个 DWD；ADS 只允许 DWS。
     这些数仓上游必须仍是其所属数据集的当前发布版本并拥有精确 `ACTIVE` 物化，
     同时冻结 materialization 身份、schema hash、snapshot hash 和 row count。
4. 原子写入运行、冻结输入、节点状态和
   `REGISTER_MATERIALIZATION_BUILD` 审计事件。

相同操作者对完全相同的发布版本、输入快照和模式重复请求会返回同一个构建；
首次创建返回 `201`，幂等重放返回 `200`。输入、发布指针或请求预算不一致时返回
`409`。

### 自动映射 ODS

带 `originTableId` 的系统映射 ODS 固定使用 `REALTIME`、`previewLimit=100` 且
`materialization.enabled=false`。首次发布、结构刷新和启动对账都只维护数据集版本、
字段合同及精确来源版本，不登记 build、不建立 `warehouse_ods` 表或稳定视图。

## 查询构建

列表：

`GET /api/v1/datasets/{datasetId}/materializations/builds?limit=50&offset=0`

- `limit` 范围为 `1..100`，默认 `50`。
- `offset` 必须为非负整数。
- 其他查询参数或重复参数会被拒绝。

详情：

`GET /api/v1/datasets/{datasetId}/materializations/builds/{buildId}`

详情包含运行状态、服务端冻结的输入身份/摘要、节点状态，以及成功激活后的物化
摘要。它不返回 `plan_json`、`snapshot_json`、SQL 或物理关系名称。

## 取消排队任务

`POST /api/v1/datasets/{datasetId}/materializations/builds/{buildId}/cancel`

取消请求不接受请求体或查询参数。只有 `QUEUED` 任务可以转换为 `CANCELLED`；
`RUNNING` 或终态任务返回 `409`。成功取消与
`CANCEL_MATERIALIZATION_BUILD` 审计事件在同一个事务内提交。

## 稳定错误码

| HTTP | code | 含义 |
|---|---|---|
| 400 | `MATERIALIZATION_INVALID_REQUEST` | 请求字段、模式、分区或重试预算无效 |
| 400 | `MATERIALIZATION_INVALID_PAGE` | 分页或查询参数无效 |
| 404 | `MATERIALIZATION_NOT_FOUND` | 数据集或构建在当前租户不可见 |
| 409 | `MATERIALIZATION_CONFLICT` | 当前发布源、上游活跃物化或冻结合同发生变化 |
| 409 | `MATERIALIZATION_INVALID_TRANSITION` | 构建状态不允许取消 |
| 415 | `MATERIALIZATION_JSON_REQUIRED` | 登记请求不是 JSON |
| 500 | `MATERIALIZATION_INTERNAL_ERROR` | 控制面内部错误 |

## 当前执行边界

ODS 不由 worker 物化。DIM/DWD worker 收到 `virtual-ods-source-v1` 输入后，复核
不可变来源版本、结构摘要及文件 SHA-256：MySQL/Oracle 通过受控只读流抽取，
Excel/CSV 逐行读取精确文件版本；完整来源先进入 run-scoped staging，再按已发布
ODS 字段合同投影并执行目标 DAG。只有完整 staging 和目标层质量门都成功，才会在
`warehouse_dim` 或 `warehouse_dwd` 原子激活。流截断、类型错误、摘要漂移、超时或
租约丢失都不会产生 ACTIVE 物化。

当前物化只支持 `FULL`；`INCREMENTAL`、`BACKFILL` 和 `PARTITIONED_TABLE` 失败关闭。
DWS/ADS 只从数仓中精确的上游 ACTIVE 物化读取，并在 PostgreSQL 内执行。

## ACTIVE 物化的查询消费

显式 DIM/DWD/DWS/ADS 的发布试跑与预览不会接受客户端提供的物理标识。DIM/DWD
固定的 ODS 上游必须是当前 `PUBLISHED` 精确版本；运行时把它展开为精确来源，并对
每个来源最多采样 100 行后执行当前 DAG，供中间过程展示。DWD 的 DIM 上游以及
DWS/ADS 的数仓上游则必须存在 schema hash 一致的当前 `ACTIVE` 物化，查询运行时
只把其 `warehouse_published` 稳定视图作为允许表。当前交互式预览对“虚拟 ODS 来源
+ 数仓 DIM”的混合执行失败关闭，避免把样本和全量维表混成不可解释结果。DWS 指标
直接绑定精确 DWS 当前 ACTIVE 物化，不重放 DWS DAG。

解析完成后，PostgreSQL 执行事务会用租户 RLS 再次锁定并复核发布指针、版本、
materialization ID、schema/snapshot hash、稳定视图类型和 API 角色 SELECT 权限。
查询审计在 `query_run_materializations`（候选预览使用对应 candidate 表）保存
本次实际读取的全部精确绑定，不保存 SQL、参数或结果。ACTIVE 缺失或在解析后已经
完成切换时，本次查询按旧精确绑定失败关闭，调用方可重新发起并重新解析；若执行
事务已先取得共享锁，激活切换会等待本次 SELECT 完成。两种时序都不会混读两个
物化。
