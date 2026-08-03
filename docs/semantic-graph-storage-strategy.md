# 语义与 WHERE 决策图存储策略

> 时序决定已由 [ADR 0005：NebulaGraph 作为语义关系运行时投影](./adr/0005-nebulagraph-semantic-runtime-projection.md)
> 替代。PostgreSQL 继续作为事务事实源；在线标准图投影改为 NebulaGraph。本文件对
> 现有 PostgreSQL 图和 WHERE 决策图的描述保留为迁移基线与历史回放依据。

## 结论

系统采用“事务事实源 + 图投影”的混合架构，不把所有治理资产直接迁移到外部
图数据库：

- PostgreSQL 继续保存指标、维度、维度成员、版本、权限、物化和已验证 WHERE
  决策，是唯一可写事实源。
- `semantic_graph_generations / semantic_graph_nodes /
  semantic_graph_edges` 是按租户、按代次冻结的属性图投影，负责多跳语义解析、
  兼容性证明和数据血缘遍历。
- 外部图数据库只作为未来可替换的只读查询投影，不得反向修改指标口径、权限或
  可执行 WHERE。

当前实现已经是图数据模型，并不依赖传统表 JOIN 临时拼出一条“假流程”：

```text
MEMBER -> DIMENSION -> FIELD -> DATASET_VERSION
                         ^
METRIC ------------------+-> MATERIALIZATION
  |
  +-> DATASET_VERSION -> DATASET/SOURCE
```

每次投影生成一个不可变 generation。查询计划绑定精确 generation，并在执行前
重新校验指标版本、物化和权限，避免图投影过期后继续执行。

## WHERE 决策图

`platform.dimension_where_decisions` 保存成功执行后观察到的决策事实：

```text
Dimension
  -> Canonical Value / Vector Key
  -> WHERE Decision
  -> Metric Version
  -> Materialization
  -> Dataset Version / Source
```

决策事实包含：

- 维度 ID、字段名和字段描述；
- 规范值、同义表达和已选择治理成员集合；
- `维度描述:规范值` 向量键；
- 指标版本、指标字段和目标物化表；
- LLM 操作符、业务 WHERE 和参数化执行条件；
- LLM 模型、提示词版本、理由和最后观察到的查询计划。

空维度值不进入决策图。多个文本表达命中同一正式维度、同一治理成员集合、
同一指标版本和物化版本时，只保留一个决策节点并合并别名。

页面以正式维度作为一级分组：

```text
正式维度
  ├─ 规范值 A -> 指标字段 -> 目标表 -> WHERE
  ├─ 规范值 B -> 指标字段 -> 目标表 -> WHERE
  └─ 规范值 C -> 指标字段 -> 目标表 -> WHERE
```

不再展示“全部维度成员 -> 维度 -> 同一 DWS 指标”的静态画布，因为它既不是
一次真实问答决策，也无法证明具体 WHERE 已通过 LLM、安全编译和执行验证。

## 旧时序决定：是否引入外部图数据库

以下内容记录旧方案的延期条件，已不再作为当前实施决定。现有 PostgreSQL 属性图投影
在 NebulaGraph 双写、对照和切读阶段继续承担迁移基线，但不得据此跳过 ADR 0005。

满足以下任一条件后，再将 Neo4j、Memgraph 等作为只读投影进行压测：

1. 单租户有效边超过 1,000 万；
2. 典型路径超过 5 跳，递归 SQL 的 P95 超过 300 ms；
3. 需要高频执行任意路径、社区发现、中心性或相似子图算法；
4. 图查询扩容需求明显独立于事务数据库。

外部图投影采用 outbox/CDC 增量同步，必须携带 `tenant_id`、generation 和
证据摘要。切换只能发生在读取侧；查询执行仍以 PostgreSQL 事实源的版本、
权限和参数化条件复核结果为准。图服务不可用时回退到当前 PostgreSQL 图投影，
不能降低权限或执行门禁。

## 不应放入图数据库的职责

- LLM 生成自由 SQL；
- 指标和维度版本审批；
- 行列级权限决策；
- 查询参数绑定与 SQL 执行；
- 物化激活、幂等写入和事务锁；
- 原始问话、敏感维度值或未脱敏样本。

图数据库负责回答“节点之间有什么受证据支持的关系”，关系数据库负责决定
“当前版本是否允许被执行”。
