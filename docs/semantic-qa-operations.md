# Semantic QA 运维手册

## 1. 发布顺序

1. 备份控制库并确认 warehouse 可用；
2. 运行 `make db-migrate`，前向应用迁移 84–95；
3. 启动通用 worker；等待 graph projector 追平；
4. 运行 `make db-verify`、`make warehouse-verify` 和 `make semantic-qa-verify`；
5. 对试点租户先启用 `enabled` 和 `graphProjectionEnabled`；
6. 图为 READY 后再启用 `questionChangeEnabled`；
7. 回放该业务域的 ACTIVE 黄金问题集；
8. 观察一轮 SLO 后分批扩大租户范围。

API 与 worker 必须使用不同数据库角色。API 不能写 graph 和自动 DWS 任务；worker 不能写 ChangeSet、消费合同、QueryPlan 和黄金问题。

## 2. 健康判定

健康状态同时满足：

- graph state 为 `READY`；
- `applied_event_version = requested_event_version`；
- current generation 的实际节点/边数等于 generation 记录；
- 没有 dangling edge；
- MATERIALIZATION 节点仍对应同版本、同 schema hash 的 ACTIVE 物化；
- READY/EXECUTED 计划都有 SOURCE 与 MATERIALIZATION 证据；
- 有成员值的计划都有 MEMBER 证据；
- ChangeSet 操作计数一致；
- PUBLISHED ADS 均绑定 PUBLISHED 消费合同。

本地和部署流水线统一执行：

```sh
make semantic-qa-verify
```

脚本只检查控制事实、ID、状态和 hash，不读取业务结果行。

## 3. 需要告警的信号

- `semantic_graph_projection_state.status=FAILED`；
- graph requested/applied 水位持续拉大；
- graph lease 反复过期；
- `dws_modeling_jobs` 在 `WAITING_DEPENDENCY` 超过 DWD 物化 SLA；
- 同一 DWS job 连续 `DRAFT_CONFLICT`；
- QueryPlan 的 `GAP / AMBIGUOUS / REJECTED / FAILED` 比例突增；
- 黄金问题 correctness 或 safety 低于 ACTIVE set 阈值；
- ACTIVE 物化变旧、schema hash 不一致或刷新失败；
- ChangeSet `CONFLICT` 比例突增；
- P95/P99 规划或执行时延超过按租户压测建立的阈值。

不要把 `WAITING_DEPENDENCY` 当作失败次数；它用于等待精确 DWD 物化。

## 4. 故障处理

Graph 未就绪：

1. 保留上一 READY generation；
2. 检查 projector lease、error code 和事件水位；
3. 修复控制事实或 worker 后重跑 projector；
4. 不直接编辑 graph 节点或 current pointer。

DWS 自动任务中断：

1. 重启 worker；
2. 有 selection checkpoint 的过期任务从选择结果继续；
3. 依赖未就绪任务继续等待且不增加 attempt；
4. Dataset 已创建但 output 尚未登记时，worker 只按确定性 code、创建人、层和 DSL hash 恢复；
5. 人工改过的草稿保持 `MANUAL_OWNED`。

查询执行冲突：

1. 丢弃已返回给服务端但尚未交付的结果；
2. 重新创建 QueryPlan，不能在旧 generation 上重试执行；
3. 若仍无法证明路径，返回结构化 gap，不猜测表或关系。

## 5. 回退

- 首选关闭租户 `questionChangeEnabled`，停止自动生成持久变更；
- 需要时关闭 `enabled` 和 `graphProjectionEnabled`；旧发布数据集、物化和历史证据保持不变；
- 停止 worker 不会删除 current READY generation；
- 数据库迁移采用前向修复。已经进入共享环境的迁移文件不得修改；
- 只有完成备份验证并确认没有新版本数据后，才考虑使用对应 down migration；
- 回退规划器时不得删除 QueryPlan、ChangeSet、golden run 或 generation 审计。

## 6. 备份与灾备演练

必须纳入备份：

- 控制面平台 schema 和迁移账本；
- Dataset/Metric 不可变版本；
- consumer contracts、ChangeSets、graph generations 与 QueryPlan evidence；
- warehouse 物化表、稳定视图登记和刷新状态。

季度演练至少验证：

1. 从控制库备份恢复到隔离环境；
2. 不依赖旧 graph 表，从权威领域表完整重建 generation；
3. 对照节点/边计数与路径 hash；
4. 恢复 warehouse 后验证 ACTIVE 物化 schema hash；
5. 回放 ACTIVE 黄金问题集；
6. 运行三项 verify 命令并记录 RTO/RPO。

生产规模的性能基线、告警阈值和灾备 RTO/RPO 必须在目标环境测量，不能用开发机数据代替。
