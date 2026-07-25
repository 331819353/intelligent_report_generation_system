# 语义问答自动建模规则

状态：已落地的服务端合同
适用版本：迁移 84–95、Dataset DSL `1.0`、语义合同 `1.0`

## 1. 唯一生成顺序

```text
数据源物理对象 -> ODS
ODS[1..N] -> DIM
ODS[1..N] + DIM[0..N] -> DWD
DWD[1..N] -> DWS
DWS[1..N] + 已发布消费合同 -> ADS
```

- ODS 是源物理表、Sheet 或文件版本的治理映射，不承载跨源业务推断。
- DIM 抽离人物、商品、地域、组织等实体说明，保留自然键、代理键、来源键和 SCD 语义。
- DWD 表示一个业务事实粒度。一个事实可以由多张 ODS 还原，也可以关联任意数量相关 DIM。
- DWS 组合一个或多个 DWD 的分析视角。多事实必须先分别聚合到共同粒度。
- ADS 只由明确消费场景驱动。系统没有“自动组合所有 DWS”的入口。

## 2. 多 DIM 与一对多规则

一张 DWD 同时关联用户、商品、地域、渠道等多个 DIM 是正常设计，不构成 fanout。逐条关系判断：

- `MANY_TO_ONE`、`ONE_TO_ONE`：允许事实直接 Join DIM；
- `ONE_TO_MANY`、`MANY_TO_MANY`：必须声明 `BRIDGE`；
- Bridge 必须声明关系键、关系类型、有效期，以及 `PRIMARY / ALLOCATE / DEDUPLICATE / NON_ADDITIVE / UNSAFE` 中的处理策略；
- SCD2 必须以事实事件时间命中 `[validFrom, validTo)`，并证明每条事实至多命中一个版本；
- 未证明的基数、未受控的一对多、`UNSAFE` 关系不能发布，也不能进入自动问答。

## 3. LLM 的权限边界

LLM 只负责受限的语义判断：

1. 从服务端给定的候选中选择实体、事实和分析模板；
2. 输出符合 JSON Schema 的槽位、合同或 ChangeSet；
3. 对局部校验错误做一次定向修复；
4. 给出设计理由和待确认项。

LLM 不得输出或决定：

- SQL、DDL、物理 schema、表名或稳定视图名；
- 凭据、原始样本行或敏感成员值；
- 绕过 Dataset DSL 的自由表达式；
- 发布、物化激活、权限授予或 ADS 消费合同；
- 不在服务端候选集中的对象编码。

开发引擎负责 DSL 校验、依赖拓扑、编译、shadow build、质量门、原子激活和审计。

## 4. 自动流程与人工问题兼容

自动触发与人工问题共用 `DAGChangeSet`，不维护两套变更协议：

- 自动触发：发布事实后生成可评审 DWS 草稿；不自动发布或激活物化；
- 人工问题：先查询当前 READY graph generation；
- 路径完整：只创建一次性 `SemanticQueryPlan`，不改持久 DAG；
- 能力缺失：创建 `QUESTION` ChangeSet 或新 DWS 草稿；
- 已有设计器候选：`from-candidate` 在内存中校验完整 DSL，再转换为有界组件 patch；
- 人工修改过的自动草稿：进入 `MANUAL_OWNED`，自动任务不得覆盖；
- 所有应用操作都复核基线 `version + dslHash`，过期提案进入 `CONFLICT`；
- 人工可以验证、应用或拒绝 ChangeSet；拒绝会取消关联的未完成 DAG run。

## 5. 中断、重试与并行

- 每个 durable task 使用租约、lease token、input hash 和精确 subject identity；
- 依赖未就绪进入 `WAITING_DEPENDENCY`，不消耗失败次数；
- Provider 层拥有网络、429、5xx 等瞬时重试；业务 worker 不叠加模型重试；
- LLM 返回非法候选时只回退一次到确定性有界规则；
- DWS 模板分别记录结果，一个失败不回滚其他安全草稿；
- 租约心跳避免长任务被另一 worker 重领；
- 进程在 Dataset 创建后、输出映射前中断时，只能按确定性 code、创建人、DWS 层和精确 DSL hash 恢复；
- 相同输入与模板不重复生成；已经人工接管的草稿不再自动更新。

## 6. DWS 模板策略

模板目录覆盖趋势、期间比较、分布、排名、下钻、漏斗、留存、生命周期、异常、贡献度和多事实对比。自动 worker 只为能够由单事实安全证明的模板生成草稿：

```text
TREND
PERIOD_COMPARISON
DISTRIBUTION
RANKING
DRILLDOWN
ANOMALY
```

漏斗、留存、生命周期、贡献度和多事实对比必须由问题或明确分析上下文触发 ChangeSet，不能遍历组合所有 DWD。

## 7. 语义检索与执行规则

向量检索只提供候选。最终可执行路径必须在同一不可变 graph generation 中证明：

```text
维度成员/有效别名
  -> PUBLISHED 维度
  -> VERIFIED 且非 UNSAFE 的指标兼容关系
  -> 当前 PUBLISHED 指标版本
  -> 当前 PUBLISHED DWS 精确版本
  -> 同版本、同 schema hash 的 ACTIVE 物化
  -> DWD/ODS 冻结血缘
  -> 源对象与数据源
```

执行前再次校验 graph 水位、成员有效期、指标/数据集当前版本、兼容关系、物化、权限和策略。维度成员使用 `member_key` 参数绑定为 `EQUALS` 过滤，不进入 DSL 或日志。执行期间 generation 变化时丢弃结果，不能携带旧证据回答。

## 8. ADS 规则

ADS 必须引用已发布 `semantic_consumer_contract`，合同冻结：

- consumer、业务场景和输出粒度；
- 精确 DWS 输入版本；
- SLA 和访问策略；
- 必选输入集合。

数据库发布触发器逐个核对 ADS 节点与合同输入。没有合同、合同未发布、少必选输入、使用非 DWS 或错误版本，均失败关闭。自动 DWS worker永不创建 ADS。
