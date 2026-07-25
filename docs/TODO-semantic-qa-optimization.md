# 智能问答语义层优化 TODO

状态：核心仓库改造已完成；生产环境验收项待执行
更新日期：2026-07-25

总体设计见[改造计划](./semantic-qa-retrofit-plan.md)，自动化判定见[自动建模规则](./semantic-qa-automation-rules.md)，上线步骤见[运维手册](./semantic-qa-operations.md)。

## 1. 已完成：分层与合同

- [x] ODS、DIM、DWD、DWS、ADS 五层枚举、物理 schema、不可变版本与标签向量化。
- [x] 固定 `DIM <- ODS[1..N]`。
- [x] 固定 `DWD <- ODS[1..N] + DIM[0..N]`，允许任意数量普通 DIM。
- [x] 固定 `DWS <- DWD[1..N]`，多事实先聚合到共同粒度。
- [x] 固定 `ADS <- DWS[1..N] + PUBLISHED consumer contract`。
- [x] Bridge 关系角色、分配字段、主成员、关系类型、有效区间和 fanout policy。
- [x] SCD2 event-time Join、事实粒度/键/原子度量合同。
- [x] DWS analysis contract：输入模式、共同粒度、一致性维度、时间、度量、单位/币种。
- [x] 数据库、DSL、JSON Schema、materialization resolver、query runtime 和 tag worker 使用同一依赖矩阵。
- [x] ADS 数据库发布触发器核对合同和精确 DWS 输入；自动 ADS 生成保持关闭。

证据：

- 迁移 `84–86`；
- `testdata/semantic-qa` 七个正向夹具和一个 uncontrolled fanout 反例；
- `internal/dataset/semantic_qa_fixtures_test.go`。

## 2. 已完成：统一 ChangeSet 与可恢复自动化

- [x] `WarehouseBuildDAG`、一次性 `SemanticQueryPlan` 和持久 `DAGChangeSet` 分离。
- [x] ChangeSet 结构化 patch、精确基线、请求幂等、乐观锁、验证、应用、拒绝和冲突状态。
- [x] 旧问题/DatasetAI 候选经 `from-candidate` 转换为组件 patch，不持久化整份覆盖。
- [x] 自动流程与 `AUTOMATION / QUESTION / MANUAL` 三类人工流程使用同一协议。
- [x] ChangeSet 应用后仍进入 Dataset 草稿、发布审批和物化流程。
- [x] 阶段检查点使用 input hash、subject identity、lease token 和逐阶段证据。
- [x] Provider 重试与 worker 重试分层；无双层重试放大。
- [x] `WAITING_DEPENDENCY` 不消耗失败预算，长任务有租约心跳。
- [x] 自动草稿人工修改保护为 `MANUAL_OWNED`。
- [x] Dataset 创建后中断可按 code、创建人、层和 DSL hash 精确恢复。

证据：

- 迁移 `85、87、89、93`；
- `internal/semanticqa/patch.go`；
- `internal/semanticqa/dws_modeling_worker.go`。

## 3. 已完成：版本化语义图与可靠检索

- [x] 权威领域表保持事实源；图是可重建投影。
- [x] 不可变 graph generation、current pointer、event watermark 和原子切换。
- [x] 节点覆盖成员、维度、指标、数据集版本、物化、源和标签。
- [x] 边覆盖成员归属、指标兼容、精确数据集、物化、依赖和源血缘。
- [x] 数据集、维度、成员、指标兼容和物化变化会标记图 dirty。
- [x] Projector 使用租约、输入摘要和迟到 worker 栅栏。
- [x] 查询固定单一 READY generation，不能混用两代关系。
- [x] 成员/别名在规划时重新检查 ACTIVE 与有效区间。
- [x] 向量只做候选召回，最终关系必须来自图和控制面。
- [x] 完整路径为“维度值 → 维度 → 指标 → DWS 精确版本 → ACTIVE 物化 → DWD/ODS → 数据源”。
- [x] 图可从权威表重建；本地 generation 3 为 `120 nodes / 112 edges`。

证据：

- 迁移 `87、91、92、94`；
- `internal/semanticqa/graph_worker.go`；
- `internal/semanticqa/query_planner.go`；
- `scripts/verify-semantic-qa.sh`。

## 4. 已完成：查询执行与答案证据

- [x] QueryPlan 返回 READY、AMBIGUOUS、GAP、REJECTED 等明确状态，不猜表。
- [x] 执行必须回传 generation ID 和 path hash。
- [x] 执行前重新检查 graph 水位、指标版本、DWS 版本、兼容关系、物化和权限。
- [x] 仅接受 `VERIFIED` 且非 `UNSAFE` 的维度—指标关系。
- [x] 维度成员使用有效 `member_key` 的参数化等值过滤；不嵌入 DSL 或日志。
- [x] 成员值过滤的派生计划由 query runtime 独立复核，不能伪造 literal。
- [x] generation 在执行期间变化时丢弃结果。
- [x] AnswerEvidence 返回版本、路径、物化、权限、新鲜度和兼容决定，不返回 SQL。
- [x] 保存执行时延与行数，不保存业务结果行。

当前安全边界：

- [x] 精确时间范围形成左闭右开的结构化合同，按指标 `timeFieldId` 注入参数化 `GTE / LT`，`DATE / DATETIME` 与输入精度必须匹配。
- [x] Top N 形成结构化合同，排名默认 `10 / DESC`，只允许服务端派生指标排序，并用执行行数上限强制收敛。
- [ ] 自然语言时间短语、同比/环比比较窗口和多成员复合过滤仍需扩展槽位协议；在形成同等级证据合同前保持 GAP/澄清，不从自由文本生成 SQL。
- [ ] 漏斗、留存、生命周期、贡献度和多事实对比已有 DWS 合同与模板预留，但不进行无上下文自动执行。

剩余项属于后续“问题槽位协议”扩展，不能通过放松现有执行边界完成。

## 5. 已完成：市场 DWS、质量与 ADS 预留

- [x] 模板目录：趋势、期间比较、分布、排名、下钻、漏斗、留存、生命周期、异常、贡献度、多事实对比。
- [x] 每个模板声明输入要求、输出粒度、安全规则、不适用条件和 `SUGGESTION_ONLY`。
- [x] 单事实安全模板在 DWD 发布并物化后生成可评审 DWS 草稿。
- [x] 多事实/复杂场景只由明确问题或分析上下文触发，不组合所有 DWD。
- [x] 逻辑候选、物化建议、物理激活相互分离。
- [x] 物化建议使用命中频次、不同问题数、执行时延、复用和当前物化状态。
- [x] 版本化问题模板、黄金问题集、激活和逐 generation 回放。
- [x] 黄金问题只保存 question hash、fixture、路径和失败阶段，不保存原问题或结果行。
- [x] 消费合同 API 和 ADS 发布数据库封口。

证据：

- 迁移 `87、90、93、95`；
- `internal/semanticqa/analysis_templates.go`；
- `internal/semanticqa/quality_catalog.go`；
- [Semantic QA API](./api-semantic-qa.md)。

## 6. 已完成：仓库级安全和验证

- [x] 新表具有 tenant foreign key、强制 RLS、索引、状态约束和审计字段。
- [x] API 角色不能写 graph 或 DWS 自动任务。
- [x] worker 角色不能写消费合同、ChangeSet、QueryPlan 和黄金问题。
- [x] 空库完整迁移 `1–95` 通过。
- [x] 现有库前向升级 `84–95` 通过。
- [x] 数据库、warehouse、兼容扫描和 Semantic QA 专项验证通过。
- [x] Go 后端、前端 lint/test/build 纳入统一验收。
- [x] API、自动化规则、运维、回退和灾备步骤已文档化。

## 7. 只能在目标环境完成的上线项

以下项目不能由本地代码替代，因此保持未勾选：

- [ ] `OPS-001` 在测试、预发布和生产分别核对迁移账本与只读兼容扫描。
- [ ] `OPS-002` 用真实业务规模测量精确召回、图验证、规划、执行的 P50/P95/P99。
- [ ] `OPS-003` 根据压测配置租户配额、超时、熔断和正式告警阈值。
- [ ] `OPS-004` 为每个业务域录入并激活真实黄金问题集，达到其 correctness/safety 阈值。
- [ ] `OPS-005` 执行控制库 + warehouse 恢复演练，记录真实 RTO/RPO。
- [ ] `OPS-006` 分批启用租户开关并观察至少一个完整业务周期。

这些项目有可执行入口和验收标准，但需要目标环境数据、运维权限与业务负责人签字。
