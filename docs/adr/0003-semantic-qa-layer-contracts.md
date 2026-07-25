# ADR 0003：智能问答分层依赖与 DAG 边界

- 状态：Accepted
- 日期：2026-07-25
- 决策范围：DIM、DWD、DWS、ADS、LLM 建模和语义查询
- 实施清单：[智能问答语义层优化 TODO](../TODO-semantic-qa-optimization.md)

## 背景

现有实现采用：

```text
DIM/DWD <- ODS
DWS <- DWD + optional DIM
ADS <- DWS
```

该合同可以完成基础分层物化，但存在三个问题：

1. DWD 不能正式引用已经治理的用户、商品、地域等多个 DIM；
2. DWS 可以再次直接绑定 DIM，导致事实明细扩充和分析聚合的责任边界不清晰；
3. 持久化构建 DAG、一次问答查询计划和对 DAG 的修改提案没有统一的对象边界。

系统最终目标是智能问答。向量相似度只能发现候选，不能证明维度值、维度、指标、
数据集、物化和数据源之间的关系，因此还需要版本化语义关系和完整执行证据。

## 决策

### 1. 层级输入矩阵

新建或重新发布的显式分层数据集采用：

| 目标层 | 合法物理输入 | 必需输入 |
| --- | --- | --- |
| ODS | SOURCE | 一个 SOURCE |
| DIM | ODS | 至少一个 ODS |
| DWD | ODS、DIM | 至少一个 ODS |
| DWS | DWD | 至少一个 DWD |
| ADS | DWS | 至少一个 DWS，且后续必须绑定消费合同 |

DWD 的“至少一个 ODS”代表事实来源。它可以再关联任意数量的用户、商品、地域、渠道、
门店等 DIM。多张 DIM 不等于一对多 Join；每条事实到普通 DIM 的直接关系仍应为
`MANY_TO_ONE` 或 `ONE_TO_ONE`。

Bridge 是 ODS/DIM 输入关系的建模角色，不增加第六个数据层。多值维度必须声明
Bridge、有效区间和 `PRIMARY / ALLOCATE / DEDUPLICATE / NON_ADDITIVE / UNSAFE`
中的处理语义。没有安全策略时，自动查询不得使用该关系。

DWS 的物理输入只来自一个或多个 DWD。维度一致性通过 DWD 保留的正式 DIM 键、版本
血缘和语义图证明；DWS 不再直接读取 DIM。多个事实必须先聚合到共同粒度再组合。

### 2. ADS 门禁

ADS 保留层级、物理 schema 和手工治理能力，但不进行无场景自动组合。后续 ADS 自动
任务必须绑定已发布消费合同，至少包含 consumer、业务场景、DWS 版本、指标版本、
交付结构、刷新 SLA 和权限策略。

在消费合同实现前，自动 ADS 生成器保持关闭。

### 3. 三类 DAG

系统区分：

- `Warehouse Build DAG`：持久化构建 DIM、DWD、DWS；
- `Semantic Query Plan`：一次问答的可执行计划，不修改仓库；
- `DAG ChangeSet`：对 Build DAG 的结构化局部补丁或新 DAG 提案。

自动建模和用户问题都可以产生 ChangeSet，但必须共用版本、验证、预览、人工所有权、
发布和审计合同。LLM 不直接覆盖已发布 DAG，也不生成 SQL、DDL 或物理表名。

### 4. 语义检索权威边界

标签和 embedding 负责召回候选。最终自动执行必须由控制面的权威事实证明：

```text
维度值/别名
→ PUBLISHED 维度
→ VERIFIED 且非 UNSAFE 的指标兼容关系
→ PUBLISHED 指标版本
→ 精确 DWS 版本和 ACTIVE 物化
→ Build DAG
→ DWD / DIM / ODS 版本
→ 源对象版本
→ 数据源发布版本
```

任何版本失效、关系陈旧、权限拒绝、物化过期或 Join fanout 未解决都会失败关闭。

## 兼容策略

- 历史已发布版本保持正文和 hash 不变，可以继续读取；
- 新草稿和新发布版本必须满足新输入矩阵；
- 旧 DWS 直接依赖 DIM 时，生成影响分析和可评审 ChangeSet，不原地改写；
- 旧 DWD 只依赖 ODS 仍然合法；后续可通过 ChangeSet 晋升为正式 DIM 依赖；
- 迁移 84–86 已在独立临时库全量验证并前向应用到本地控制库；本地后续数据库修改从
  87 起新增前向迁移，不再改写 84–86；
- 任一其他环境若已应用相关迁移，只允许通过新的前向迁移修正。

## 后果

正向影响：

- DWD 可以正式复用多个受治理 DIM；
- DWS 的分析服务责任更清晰；
- 查询规划可以使用统一的层级、版本和血缘证明；
- 自动任务与人工提问修改流程相互兼容；
- 中断恢复可以针对具体实体、事实、DWS 或 ChangeSet 检查点。

成本和后续工作：

- 自动 DIM/DWD 建模需要从“同批并行生成”逐步改成 DIM 发布后再生成或重基 DWD；
- 旧 DWS+DIM 依赖需要迁移或保留在兼容读取路径；
- Bridge 的基础角色/扇出门禁已经进入 DSL；分配权重、有效区间、SCD2、消费合同、
  ChangeSet 和完整语义图仍需要后续迁移与 API；
- 指标查询运行时必须从 `allowedDimensions` 白名单升级为强制验证语义兼容关系。
