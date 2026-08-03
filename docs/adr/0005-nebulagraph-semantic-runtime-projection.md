# ADR 0005：NebulaGraph 作为语义关系运行时投影

- 状态：Accepted
- 日期：2026-08-03
- 决策范围：智能问数语义关系图、Binding Bundle 验证和执行子图规划

## 背景

项目现有 `semantic_graph_generations / semantic_graph_nodes /
semantic_graph_edges` 使用 PostgreSQL 保存版本化属性图，并由递归 SQL 支撑当前查询
规划。该实现提供了较好的事务一致性和租户 RLS，但它只覆盖旧方案的有限路径证明，
没有完整实现候选扩展、值归属、关系闭包、低风险连接路径、权限传播和影响分析六类在线
图能力。

目标架构要求语义定义与图投影分离：语义合同和发布状态由事务控制面负责，图数据库
保存可重建的执行关系，并通过有界图查询为在线绑定和规划提供证明。

## 决策

采用 NebulaGraph 作为标准语义关系运行时投影，并使用官方 Go Client `nebula-go`。

- PostgreSQL 继续保存权威语义注册表、活动 `semantic_version`、发布状态和事务 outbox。
- NebulaGraph 不成为指标公式、权限策略或治理状态的唯一事实源，不接受在线反向写入。
- 每个环境使用一个主 Space：`smart_query_dev`、`smart_query_staging`、
  `smart_query_prod`；只有强租户物理隔离要求时才拆分 Space。
- 所有顶点使用稳定 VID：`type:object_id:version`；超长身份使用可复现 hash，并保留原
  ID 属性。
- 在线查询必须从权限裁剪后的已知 VID 出发，只允许 1～4 跳、固定结果上限和超时，
  禁止全图扫描。
- NebulaGraph 返回认证候选路径；Go `GraphPlanner` 负责 fanout、跨源、陈旧、权限和
  策略复杂度成本计算，不把业务路径评分固化在 nGQL 中。
- 同步 Worker 先幂等写入新 `semantic_version`，通过节点/边数量、孤儿、重复 VID、
  必达路径和黄金问题影响检查后，再在 PostgreSQL 原子激活该版本。
- 图不可用时只允许使用与活动 `semantic_version` 完全一致的认证 GraphPlan 缓存；
  没有一致缓存时必须阻断，不能退回到未经当前版本验证的 Join。

## 权威顺序

```text
人工审批业务合同
  > 执行语义层已发布定义
  > 认证数据集/视图
  > PostgreSQL Semantic Registry
  > NebulaGraph 与全文/向量派生投影
  > 历史认证示例和模型先验
```

所有派生投影必须携带相同 `semantic_version` 和 `content_hash`，禁止在多个系统分别
手工维护同一指标或关系定义。

## 迁移策略

1. 引入 `SemanticGraph` 接口和 NebulaGraph 实现，同时保留 PostgreSQL 现有读取实现。
2. 使用同一权威 Registry 生成 PostgreSQL 和 NebulaGraph 投影，对节点、边和路径进行
   对照测试。
3. 先将 Bundle 闭包和 GraphPlan 读取切换到 NebulaGraph；历史 QueryPlan 仍固定原
   generation，以保证回放。
4. 线上稳定并完成回滚演练后，停止 PostgreSQL 图的在线规划读取；历史表延迟清理，
   不修改已执行 migration。

## 结果

正向结果：

- 图的职责与事务资产治理解耦；
- 能直接实现有界关系闭包、值归属和多终点执行子图；
- 图读服务可独立扩展，路径和影响分析更容易测试；
- `SemanticGraph` 接口使运行时不绑定某个存储实现。

代价和约束：

- 增加一套生产基础设施、同步、备份和恢复责任；
- 必须处理 PostgreSQL 与图投影的版本一致性；
- 需要双写对照和延迟迁移，不能一次性删除现有图表；
- 图服务不可用时系统可能减少直接回答覆盖率，但不得降低正确性和权限门禁。

## 被替代决定

本 ADR 替代 `docs/semantic-graph-storage-strategy.md` 中“目前不需要立即引入外部图数据
库”的时序决定。该文档关于“PostgreSQL 为事务事实源、图为只读可重建投影”的职责
边界继续有效，但在线标准图实现改为 NebulaGraph。
