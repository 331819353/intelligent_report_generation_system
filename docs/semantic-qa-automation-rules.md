# 语义问答自动建模规则

状态：已落地的服务端合同
适用版本：迁移 84–95、Dataset DSL `1.0`、语义合同 `1.0`

## 1. 唯一生成顺序

```text
数据源物理对象 -> ODS
ODS（逐数据集全量扫描） -> DIM[0..2] + ODS 角色标签
非纯维 ODS[1] + DIM[0..N] + 可选次事实 ODS -> DWD
DWD[1] 或 DIM[1] -> DWS
DWS[1] + 维度包含目标[0..1] -> ADS
```

- ODS 是源物理表、Sheet 或文件版本的治理映射，不承载跨源业务推断。
- 未框选 DIM 对领域内 ODS 逐数据集并行识别；一张 ODS 可以不产出 DIM，也可以按两个不同实体键产出两张 DIM。两个候选键集合存在包含关系时合并为一张较细粒度统一维度，互不包含时保持两张。分类完成后，精确 ODS 发布版本获得互斥的事实、维度、事实兼维度或其他建议标签。
- 未框选 DWD 对每张非纯维 ODS 单独建模。第一轮把其他事实按批判断业务关联和主次，只保留“当前表为主表、候选表至多一行”的关系；第二轮汇总这些关系与 DIM 合同后再由 LLM 设计明细表。
- 未框选 DWS 对每张 DWD 只生成一张统一多维主题表；可组合维度通过 `CUBE / ROLLUP / GROUPING_SETS` 表达，不按分析模板拆表。每张 DIM 还会单独形成无事实 `ENTITY_COUNT` 主题，只包含一个实体计数指标。
- 未框选 ADS 比较领域内两张单粒度 DWS 的受治理维度集合；只有源维度严格包含目标维度且源指标为 `FLOW + ADDITIVE` 时，才先把源预聚合到目标粒度，再以完整目标维度键一对一关联两侧指标。维度互不包含、任一侧为多粒度分组表、或指标为累计/时点值时保持单 DWS 投影。
- 非空框选继续使用既有冻结关系：联合 DWS 与逐 DWS ADS 行为不因上述默认智能化流程改变。

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
- 未框选 DWS 的统一多维选择只记录一个结果；框选流程的多个模板仍分别记录，一个失败不回滚其他安全草稿；
- 租约心跳避免长任务被另一 worker 重领；
- 进程在 Dataset 创建后、输出映射前中断时，只能按确定性 code、创建人、DWS 层和精确 DSL hash 恢复；
- 相同输入与模板不重复生成；已经人工接管的草稿不再自动更新。

## 6. DWS 模板策略

模板目录仍覆盖趋势、期间比较、分布、排名、下钻、漏斗、留存、生命周期、异常、贡献度和多事实对比。未框选 worker 从能够由单事实安全证明的模板中选一个作为统一主题意图，并用多维分组模式承载其他消费视角；框选流程继续允许既有多模板结果：

```text
TREND
PERIOD_COMPARISON
DISTRIBUTION
RANKING
DRILLDOWN
ANOMALY
ENTITY_COUNT
```

漏斗、留存、生命周期、贡献度和多事实对比必须由问题或明确分析上下文触发 ChangeSet，不能遍历组合所有 DWD。

## 7. 累计值与时点值

- `FLOW` 是期间发生量，声明 `ADDITIVE + timeAggregation=SUM`；
- `CUMULATIVE` 是累计、YTD/MTD、running total，声明 `SEMI_ADDITIVE + timeAggregation=LAST`；
- `POINT_IN_TIME` 是余额、库存、存量、期末数和在手量，声明 `SEMI_ADDITIVE + timeAggregation=LAST`；
- 累计值与时点值在同一日期可以沿实体维度使用合同中的默认聚合，自动 DWS 必须保留日级时间；CUBE/ROLLUP 会转换成每个集合都含日期的安全 `GROUPING_SETS`；
- 当前 DSL 不用 `MAX` 伪装最后观测值，也不把半可加指标送入月度多事实 SUM 或 ADS 二次汇总。需要真正跨期末值时，必须由后续显式 last-value 能力或受治理快照表提供。

## 8. 语义检索与执行规则

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

## 9. ADS 规则

ADS 必须引用已发布 `semantic_consumer_contract`，合同冻结：

- consumer、业务场景和输出粒度；
- 精确 DWS 输入版本；
- SLA 和访问策略；
- 必选输入集合。

绑定语义合同 `1.0` 的 ADS 仍由数据库发布触发器逐个核对合同输入；没有合同、合同未发布、少必选输入、使用非 DWS 或错误版本均失败关闭。默认应用建模草稿不声明语义合同版本；存在包含关系时固定较细粒度和目标粒度两个精确 DWS 物理输入，并在输出映射中记录目标版本。用户补充消费合同后再进入严格合同发布校验。
