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
  "maxAttempts": 3,
  "publishedVersionId": "811c575a-ac36-4f58-9e1e-bfe8410cc278",
  "requestId": "3e828fa0-dd0b-49a1-bf66-da08593c9214"
}
```

- `mode` 可省略，当前默认且只支持 `FULL`。
- `partitionKey` 可省略，当前只允许空字符串。
- `maxAttempts` 可省略，默认 `3`，允许范围为 `1..10`。
- `requestId` 可省略；用户点击“运行”时必须传入新的 UUID，用于登记一次新的 DAG
  执行。网络重试应复用同一个 UUID，从而幂等取得同一构建任务。
- `publishedVersionId` 必填，必须与事务内锁定的当前发布版本完全一致；指针已变化时
  返回 `409`，不会运行旧页面看到的版本，也不会改用新草稿。
- 未知字段、多份 JSON、超出 4 KiB 的请求体都会被拒绝。
- `plan`、`sql`、`inputs`、源物理名称和仓库物理名称不是该 API 的字段。

服务端在同一个事务内完成以下工作：

1. 锁定目标数据集的当前 `PUBLISHED` 版本，重新校验 DSL 摘要、层级和物化开关；
   没有当前发布版本时拒绝登记，绝不改用草稿。
2. 从 DSL 派生无 SQL 的安全拓扑和 PostgreSQL 目标合同。
3. 冻结输入：
   - 单表 ODS 与 `sourceMode=PRE_AGGREGATED` 的单表 DWS 冻结当前已发布的
     `data_source_version`、元数据表和 `structure_hash`；Excel 输入还固定精确文件版本
     及 SHA-256。Worker 全量抽取、运行级暂存后分别原子写入 `warehouse_ods` 或
     `warehouse_dws`。
   - DIM/DWD 的 ODS 输入冻结为 `virtual-ods-source-v1` 来源快照：数据库输入固定当前已发布的
     `data_source_version`、元数据表和 `structure_hash`；Excel 输入还必须固定精确
     文件版本及其 SHA-256。Worker 正式执行时按该快照全量回源，并先投影 ODS 字段合同。
   - DWD 可附加已经在数仓中的 DIM 或受控次事实；DWS 允许一个或多个 DWD，或允许
     单个 DIM 形成只有一个计数指标的 `ENTITY_COUNT`；ADS 只允许 DWS。
     这些数仓上游必须仍是其所属数据集的当前发布版本并拥有精确 `ACTIVE` 物化，
     同时冻结 materialization 身份、schema hash、snapshot hash 和 row count。
4. 原子写入运行、冻结输入、节点状态和
   `REGISTER_MATERIALIZATION_BUILD` 审计事件。

没有 `requestId` 时，相同操作者对完全相同的发布版本、输入快照和模式重复请求会
返回同一个构建。带 `requestId` 时，每个新 UUID 代表一次新的 DAG 执行，复用 UUID
则幂等返回同一构建。首次创建返回 `201`，幂等重放返回 `200`。输入、发布指针或
请求预算不一致时返回 `409`。

### 自动映射 ODS / 源端预聚合 DWS

带 `originTableId` 的系统映射数据集固定使用 `MATERIALIZED_PREFERRED`、
`previewLimit=10`、`materialization.enabled=true` 和 `refreshMode=ON_DEMAND`。发布事务
原子登记精确发布版本的首次全量 build；之后每次点击“运行”都用新的 `requestId`
再次执行同一当前发布版本。旧发布版本即使保存的是历史关闭开关，也只会在服务端
内存中启用兼容执行策略，不修改其不可变 DSL 或摘要。

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

## 停止 DAG 任务

`POST /api/v1/datasets/{datasetId}/materializations/builds/{buildId}/cancel`

停止请求不接受请求体或查询参数。`QUEUED` 或 `RUNNING` 任务可以转换为
`CANCELLED`；终态任务返回 `409`。执行中任务的 worker 会在租约心跳失效后取消
当前执行上下文。成功停止与
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

ODS 和源端预聚合 DWS 会复核不可变来源版本、结构摘要及文件 SHA-256：MySQL/Oracle
通过受控只读流抽取，Excel/CSV 逐行读取精确文件版本；完整来源先进入 run-scoped
staging，再按已发布字段合同写入对应数仓层。DIM/DWD 的 ODS 上游仍可按冻结快照
全量回源。只有完整 staging 和目标层质量门都成功才会原子激活；流截断、超过
500 万行/字节预算、类型错误、摘要漂移、超时或租约丢失都不会产生 ACTIVE 物化。

当前物化只支持 `FULL`；`INCREMENTAL`、`BACKFILL` 和 `PARTITIONED_TABLE` 失败关闭。
DWS/ADS 只从数仓中精确的上游 ACTIVE 物化读取，并在 PostgreSQL 内执行。

## ACTIVE 物化的查询消费

显式 DIM/DWD/DWS/ADS 的发布试跑与预览不会接受客户端提供的物理标识。DIM/DWD
固定的 ODS 上游必须是当前 `PUBLISHED` 精确版本；运行时把它展开为精确来源，并对
每个来源最多采样 10 行后执行当前 DAG，供中间过程展示。DWD 的 DIM 上游以及
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
