# 智能分析决策平台灾备、业务连续性与图重建演练

> 范围说明：本文件定义平台控制库、对象存储、只读数仓依赖、Nebula 投影和异步工作的恢复合同。
> AskData 图重建仍是独立子演练；本地脚本通过不代表目标环境已经达到业务 RTO/RPO。

AskData 以 PostgreSQL `askdata` schema 为语义事实源；NebulaGraph 是指定
Semantic Release 的可重建投影。恢复操作必须在隔离环境先演练，生产执行前另行完成变更审批。

## 平台恢复原则与顺序

控制库中的 `platform`、`askdata`、`decision` schema 及迁移登记必须从**同一个一致恢复点整体恢复**；
下列顺序是启用与校验顺序，不授权按 schema 拼接不同时间点的备份。

1. 宣布恢复并冻结写入：记录事故时间、最后确认正常时间、备份/WAL 标识和所有 Worker lease 水位；停止
   API 写请求、调度、导出、投影、运行配置 rollout 和 decision due worker。
2. 在空目标恢复完整控制库，先校验迁移序列、tenant/user/domain、角色/成员关系和 RLS；会话恢复后仍按
   token version/撤销状态处理，不复制事故前内存 session。
3. 校验 AskData Release/Evidence、报告主对象/不可变版本/组件/依赖、取数申请、工作项 receipt、调度/
   delivery、运行配置、决策/行动/复盘的外键闭包、不可变事件数量和 hash。
4. 恢复对象存储中的报告制品和导出制品，只接受数据库已引用且对象 hash/大小/内容类型一致的对象；孤立
   对象保持隔离，不反向创建数据库事实。缺失报告制品的版本保持不可运行，缺失/过期导出保持失败关闭。
5. 以只读方式连接数仓，验证固定数据快照/物化标识仍存在且 schema hash 一致；数仓不能回到同一读取点时，
   AskData/报告运行进入明确 `NO_DATA`/不可用状态，绝不改用其他快照冒充。
6. 从控制库的固定 Release 重建 PostgreSQL 检索、向量和 Nebula 等可重建投影，比较 content/graph hash；
   投影未达 READY 前仅允许既定的有界降级路径。
7. 先启用只读 API，再按 runtime config rollout → 通用队列 → 报告调度/分发 → decision due 的顺序恢复
   Worker；每一层确认 lease 可回收、幂等键生效和 backlog 不增长后才进入下一层。
8. 执行恢复后闭包检查和业务冒烟，再解除写冻结；归档环境配置 hash、实际 RTO/RPO、数据库/对象/图收据、
   故障注入时间线、重复计数及业务 Owner 签署。

任一步不通过都必须停止推进，不能通过跳过无权数据、删除失败队列或改写历史事件来“修复”计数。

## PostgreSQL 备份与恢复

备份账号需要读取 `askdata` schema 和执行 `askdata.release_manifest_hash`：

```sh
ASKDATA_CONTROL_DATABASE_URL='postgres://...' \
  ./scripts/askdata-postgres-backup.sh --output /secure/askdata-backup-20260810
```

输出包含完整控制库的 custom-format archive、逐 release 的规范 manifest、逐 AskData 表行数和
`SHA256SUMS`。控制库整体备份是为了保持 tenant/user 与语义对象、评测/报表引用的外键闭包。
恢复脚本只接受没有用户表的空数据库，绝不覆盖已有数据库，并要求显式传入确认参数：

```sh
ASKDATA_CONTROL_DATABASE_URL='postgres://isolated-restore-target/...' \
  ./scripts/askdata-postgres-restore.sh \
  --backup /secure/askdata-backup-20260810 --confirm-empty-target
```

恢复完成后脚本重新计算每个 release 的 object count 与 manifest hash；任一差异都以失败退出。

## 恢复后闭包与一致性校验

平台恢复收据至少包含下列检查的计数、hash 和失败码；明细值、结果行、令牌和隐藏思维链不得写入收据。

- 每个 active tenant 至少有可用身份/领域治理闭包；最后一名平台/领域管理员保护仍生效，跨租户/跨领域
  负向访问全部拒绝。
- AskData Release 的对象数、manifest、四投影水位、Question Run/Artifact/Evidence 引用闭合；历史
  RETAINED Release 不因当前发布变化丢失。
- 每个已发布 Report Version 的 definition/artifact/component/dependency hash 一致；报告下架、撤权和
  分享撤销继续失败关闭；对象存储中缺失/多余对象分别进入缺失清单/隔离清单。
- `report_schedules`、subscription、delivery 和 append-only event 数量可对账；每个
  `(tenant, subscription, scheduled_for)` 最多一个 delivery。
- 工作箱 receipt 与来源业务状态一致；已处理、撤回、终态、无权或已 resolved 的通知不再出现，摘要不
  作为可执行事实。
- Decision 的 Evidence source/hash/release、approval/event、action/event、outcome metric/review 和
  decision event 序号闭合；历史 actor 不改写，事件/审批事实仍不可更新删除。
- runtime config 的有效指针只指向合法 ACTIVE 版本，rollout hash 与节点终态一致；失败或未完成版本不
  被提升为有效配置。

`scripts/verify-database.sh` 用于验证迁移、RLS、权限、状态机和不可变边界；它是恢复收据的一部分，不能
替代对象存储、数仓、Nebula、混合负载和业务冒烟检查。

## 跨恢复点的确定行为

### 报告调度、补跑与分发

- 调度游标由 `next_run_at` 驱动。恢复后已到期计划重新 claim：距计划时间超过 `miss_after_seconds` 时为
  每个 active subscription 追加唯一 `MISSED/WINDOW_MISSED` 事实，否则生成唯一 `PENDING` delivery。
- 唯一键 `(tenant_id,subscription_id,scheduled_for)` 和 `ON CONFLICT DO NOTHING` 保证重放不重复投递；
  `RUNNING` 且 lease 过期的 delivery 可重新 claim，未过期 lease 不并发接管。
- 手工 backfill 必须给出原始 `scheduledFor`，同一时点重复调用返回零新增；下架、无权、版本不可用在
  真正交付前重新校验并进入稳定失败码，不附加数据。
- 连续失败达到阈值暂停计划；恢复过程不得清零 `consecutive_failures` 或跳过失败事件。恢复只能经受权
  命令追加事件并重新计算下一运行时点，DST 按计划时区合同处理。

### 导出、工作项与通知

- 导出 `RUNNING` lease 过期后由 Worker 重试；固定输入/结果 hash 或报告版本已失效时标记失败，不能换用
  新查询或新版本。对象已生成但数据库未提交的制品视为孤立对象并隔离，不能自动发布下载链接。
- 工作项列表始终从来源表和当前权限重新投影；receipt 只保存 actor 的已读事实。来源已终态/撤回/无权时
  项目消失，恢复不得根据旧摘要补写来源状态。
- 决策、行动和报告通知用 recipient + dedup key 重放；已有通知不重复插入，来源终态时设置
  `resolved_at`。恢复收据必须报告重复尝试数和实际新增数。

### 决策截止与复盘

- `IN_EXECUTION/REOPENED` 且 `review_at` 已越过恢复时钟的决策由 due worker 以数据库当前时间转为
  `REVIEW_DUE` 并追加一次事件/通知；已存在 dedup key 时不重复。
- 未终态且 `due_at` 越过恢复时钟的行动只追加去重 `ACTION_OVERDUE` 通知，不擅自改成 DONE/CANCELED。
- Outcome Metric 恢复后仍按当前查看者 PolicyScope 和固定 Semantic IR/Release 重放；无权、无数据或
  口径漂移进入明确结果，绝不复用事故前他人缓存。人工复盘结论不会在恢复过程中自动确认。

## 从指定 release 重建 NebulaGraph

该演练会删除 `ASKDATA_NEBULA_SPACE`，只能指向专门的演练 Space。脚本先从 PostgreSQL
构建只读 canonical proof，再删除/初始化 Space、以 Worker USER 投影，最后比较重建前后
release hash、graph hash、vertex/edge/object count。只有收据完全相同才通过。

```sh
set -a
. ./.env
set +a
./scripts/askdata-graph-rebuild.sh \
  --tenant-id 00000000-0000-0000-0000-000000000001 \
  --release-id 00000000-0000-0000-0000-000000000002 \
  --confirm-drop-space "$ASKDATA_NEBULA_SPACE"
```

可先执行不写图的基线检查：

```sh
go run ./cmd/askdata-graph-rebuild \
  --tenant-id "$TENANT_ID" --release-id "$RELEASE_ID"
```

演练证据应归档备份目录的 `SHA256SUMS`、恢复日志以及图重建 JSON 收据。任何 manifest
不一致、release 非 `READY/ACTIVE/SUPERSEDED/RETAINED`、父子对象不闭包或图写入失败都会
失败关闭。

## 平台演练完成判定

本地控制库恢复、`verify-database.sh` 和图重建通过，仅表示软件恢复合同可执行。`OPS-007` 仍保持未完成，
直到目标拓扑完成：控制库时间点恢复、对象存储校验、数仓依赖故障、Nebula 重建、跨调度窗口/行动截止
时间的 Worker 重放、混合负载恢复以及业务 Owner/运维/安全三方签署。报告中必须同时给出已批准和实际
RTO/RPO；任一缺失时状态固定为 `DR_NOT_SIGNED`。
