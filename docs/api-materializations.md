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
   - ODS 只允许一个 `TABLE` 节点。数据库输入固定当前已发布的
     `data_source_version`、元数据表和 `structure_hash`；Excel 输入还必须与当前
     发布数据源版本的精确文件版本一致，`snapshotHash` 使用文件 SHA-256。
   - DWD 只允许 `DATASET` 节点且上游必须为 ODS；DWS 只允许 `DATASET` 节点且
     上游必须为 DWD。每个上游必须仍是其所属数据集的当前发布版本，并拥有精确
     `ACTIVE` 物化。登记优先固定 `MATERIALIZATION` 身份，同时保存 schema hash、
     snapshot hash 和 row count。
4. 原子写入运行、冻结输入、节点状态和
   `REGISTER_MATERIALIZATION_BUILD` 审计事件。

相同操作者对完全相同的发布版本、输入快照和模式重复请求会返回同一个构建；
首次创建返回 `201`，幂等重放返回 `200`。输入、发布指针或请求预算不一致时返回
`409`。

### 自动映射 ODS 的首次构建

带 `originTableId` 的系统映射 ODS 固定启用 `ON_DEMAND` 物化。其首次及后续系统
刷新发布不依赖浏览器调用本接口：数据集发布事务直接复用上述服务端派生逻辑，把
精确新版本、源数据发布版本、表/文件版本和结构摘要写成 `QUEUED` build。事务中
只登记控制面任务，真正的源端读取、staging、CTAS 和激活仍由 materialization
worker 在提交后执行。启动对账会补登记历史遗漏任务，同一版本和冻结输入只会得到
一个确定性幂等任务；不同操作者触发对账不会改写已有任务的 `requestedBy`。

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

ODS worker 已启用 MySQL、Oracle 与 Excel/CSV 的精确发布输入搬运。数据库源把
受限投影/过滤前置到源库，以严格 NDJSON 流和 typed COPY 写入 run-scoped
PostgreSQL staging；Excel/CSV 复核不可变文件版本、SHA-256、Sheet、投影和类型后
分批 COPY。两条路径都限制为 5,000,000 行，完整 staging 成功后才执行
`warehouse_ods` CTAS、质量门和原子激活。

当前仅支持 `FULL + TABLE + 单 TABLE 节点`。`INCREMENTAL`、`BACKFILL`、
`PARTITIONED_TABLE` 和非单表 ODS 失败关闭；数据源重新发布、结构/文件摘要漂移、
流截断、类型错误、超时或租约丢失都不会产生 ACTIVE 物化。DWD/DWS 仍只接受由
上游活跃物化解析出的 PostgreSQL 输入，并全部在 PostgreSQL 执行。

## ACTIVE 物化的查询消费

显式 DWD/DWS 的发布试跑与预览不会递归执行上游数据集，也不会接受客户端提供的
物理标识。DWD 的每个 ODS 上游、DWS 的每个 DWD 上游都必须是所属数据集的当前
`PUBLISHED` 精确版本，并存在 schema hash 一致的 `ACTIVE` 物化；查询运行时只把
其 `warehouse_published` 稳定视图作为允许表。DWS 指标则直接绑定指标定义中的
精确 DWS 当前 ACTIVE 物化，不重放 DWS DAG。

解析完成后，PostgreSQL 执行事务会用租户 RLS 再次锁定并复核发布指针、版本、
materialization ID、schema/snapshot hash、稳定视图类型和 API 角色 SELECT 权限。
查询审计在 `query_run_materializations`（候选预览使用对应 candidate 表）保存
本次实际读取的全部精确绑定，不保存 SQL、参数或结果。ACTIVE 缺失或在解析后已经
完成切换时，本次查询按旧精确绑定失败关闭，调用方可重新发起并重新解析；若执行
事务已先取得共享锁，激活切换会等待本次 SELECT 完成。两种时序都不会混读两个
物化。
