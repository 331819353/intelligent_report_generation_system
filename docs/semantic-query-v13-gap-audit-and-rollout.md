# 智能问数 v1.3 落地差距审计与升级排期

状态：`AUDITED / IMPLEMENTATION IN PROGRESS`

目标依据：`智能问数项目技术架构与实施方案 v1.3`（2026-08-03）

审计范围：当前 `master` 上的资产管理、语义资产、语义图、智能问答、查询执行、
权限、评测和前端问数工作台，以及本工作区尚未提交的升级实现。

## 1. 审计结论

当前项目不是从零开始。它已经具备分层数据资产、指标和维度版本、PostgreSQL +
pgvector 检索、租户 RLS、物化与发布门禁、PostgreSQL 属性图投影、参数化指标执行和
一部分问答证据链。当前工作区还新增了统一 Question Orchestrator、三路径路由合同、
状态机、Semantic Query IR、Tool Registry、AccuracyEvidence 和资产治理概览。

但是当前实现仍处于“旧执行底座 + 新合同外壳”的过渡状态，尚未完整落地 v1.3：

1. `semantic_graph_*` 是 PostgreSQL 表，不是 NebulaGraph；现有策略文档还明确把外部图
   数据库推迟到未来，与 v1.3 固定选择 NebulaGraph 的决定冲突。
2. 资产管理仍以数据集、指标、维度、词条和解析规则的分散管理为主，尚未形成一套可
   原子发布的七层可执行语义合同。
3. 问答虽然已生成 Semantic Query IR，但执行仍主要调用项目原有指标预览链路；没有
   独立的执行语义层适配器和完整 GraphPlan 编译边界。
4. Tool Registry 已有工具定义，但 Evidence Loop 实际只覆盖指标和维度绑定的一部分，
   还不是贯穿图闭包、编译、`EXPLAIN`、执行和结果验证的 Host 控制闭环。
5. 当前 SQL Guard 主要验证服务端 IR、版本、路径 hash 和参数绑定，不包含目标方言
   AST、表列 allowlist、成本、扫描量和 `EXPLAIN` 门禁。
6. 当前 Result Verifier 能校验结果形状、计划/结果 hash 和版本一致性，但没有完整覆盖
   总分恒等式、时间完整性、Top N、比率分母、对照查询和 P0 认证结果。
7. 黄金问题表和回放入口已存在，但没有 2,000+ sealed test、Wilson 下界、分层错误预算
   和发布门禁实现，不能据此宣称达到 95%。

因此，本轮升级不是继续扩展旧 PostgreSQL 图查询，也不是只调整页面；必须先固定权威
语义合同，再将检索、NebulaGraph、问答规划和执行统一到同一个 `semantic_version`。

## 2. 当前落地与目标方案差距矩阵

| 范围 | 当前落地 | v1.3 目标 | 判定 | 优先级 |
| --- | --- | --- | --- | --- |
| 权威顺序 | 指标、维度、数据集和词条分散存储；图 generation 单独演进 | 人工合同 > 执行语义层 > 认证数据集 > 检索/图投影 | 部分实现 | P0 |
| 语义对象 | 已有指标、维度、成员、词条、数据集和物化 | 七层合同：Domain、Term、Entity、SemanticModel、Metric、Dimension/Value/Time/Cohort、Relation、Policy/DQ | 缺少实体、时间、cohort 和统一关系合同 | P0 |
| 语义版本 | PostgreSQL graph generation、资产 version/hash 并存 | 所有执行层和派生投影携带同一 `semantic_version/content_hash`，原子切换 | 未统一 | P0 |
| 资产发布 | 各模块分别发布和触发投影 | 构建候选版本 → 静态校验 → 投影 → 黄金回归 → 原子激活 → 延迟清理 | 部分实现 | P0 |
| 关系图 | PostgreSQL `semantic_graph_nodes/edges` + 递归 SQL | NebulaGraph 可重建投影，稳定 VID，有界 1～4 跳，Go GraphPlanner | 未实现 | P0 |
| 图在线用途 | 指标、维度、成员和物化的部分路径证明 | 候选扩展、值归属、兼容性、路径选择、权限传播、影响分析六类能力 | 部分实现 | P0 |
| 图可用性 | PostgreSQL generation readiness | Nebula 多副本；不可用时只接受版本一致的缓存 GraphPlan | 未实现 | P0 |
| 检索 | PostgreSQL FTS/pgvector、词条和维度成员检索 | Span → 精确 Alias → 六路召回 → Bundle 联合重排 → 图闭包 → 校准 | 部分实现 | P0 |
| Alias | 通用词条映射与解析规则 | 作用域、locale、生效期、正/负 alias、hard negative、冲突门禁 | 部分实现 | P1 |
| 维度值 | 成员、别名、WHERE 决策 | `(dimension_id,value_id)` 复合身份、层级/父路径/有效期、图归属 | 部分实现 | P0 |
| 问答入口 | 已新增统一 `/questions` 编排，旧接口仍存在 | 单一生产入口 + REST/SSE + 可恢复状态机 | 部分实现 | P0 |
| 路径 A | 服务端生成 Semantic IR，底层执行原指标预览 | 受约束 IR，经语义层/适配器确定性编译，随后 Guard | 部分实现 | P0 |
| 路径 B | 明确关闭 | 仅在受治理范围内启用的 Text-to-SQL + AST/GraphPlan/成本门禁 | 未实现；关闭是当前安全行为 | P1 |
| 路径 C | 有澄清和阻断 | 最小澄清、越权/范围外/DQ/成本阻断和 continuation token | 部分实现 | P0 |
| 多指标 | 每个指标形成独立 QueryPlan 和结果集 | 求共同粒度；各事实先聚合再安全合并 | 部分实现 | P0 |
| Tool Loop | 指标/维度工具循环，有新增证据和重复签名检查 | 最多 3 轮，所有 Tool 统一 Schema/PolicyScope/预算/审计/错误矩阵 | 部分实现 | P0 |
| SQL Guard | IR、路径 hash、只读入口和参数绑定检查 | SQL AST、表列集合、函数白名单、RLS、fanout、`EXPLAIN` 和成本 | 未完整实现 | P0 |
| 结果验证 | schema、行数、hash、版本和权限复核 | 时间完整性、单位、空值、比率、Top N、恒等式、DQ、P0 对照 | 部分实现 | P0 |
| 答案忠实 | 确定性结果摘要和 AccuracyEvidence | 数字槽位逐项验证、口径/过滤/新鲜度/来源完整展示 | 部分实现 | P0 |
| 权限 | 应用授权 + PostgreSQL/warehouse RLS | 应用 + Policy Engine + 语义层 + 数仓四层 | 部分实现；尚无 OPA 适配 | P0 |
| 评测 | 黄金问题集、回放和基础验证脚本 | 2,000+ sealed、结果等价、Wilson 下界、分层错误预算、影子流量 | 未实现 | P0 |
| 可观测性 | 请求日志、进度和执行证据 | OTel trace 串联 Web、Go、模型、图、语义层和数仓 | 部分实现 | P1 |
| 前端 | 工作台、澄清、证据、资产概览正在重构 | 口径卡、最小澄清、结果/证据抽屉、治理和评测台 | 部分实现 | P1 |

## 3. NebulaGraph 强制落地边界

NebulaGraph 是本轮升级的标准语义关系图，不再作为未来可选项。PostgreSQL 继续保存
语义对象、发布状态、版本清单和事务 outbox，是治理控制面的事实源；NebulaGraph 是
按 `semantic_version` 重建的只读运行时投影，不能反向修改指标口径或权限。

### 3.1 运行时对象

- Space：`smart_query_dev`、`smart_query_staging`、`smart_query_prod`；默认不按业务域
  拆 Space，使用 `domain_id`、`tenant_scope` 和权限边隔离。
- Tags：`domain`、`entity`、`metric`、`dimension`、`dimension_value`、`dataset`、
  `table_column`、`business_term`、`certified_example`、`role`、`quality_rule`。
- Edges：`contains`、`measures`、`depends_on`、`sourced_from`、`groupable_by`、
  `belongs_to`、`has_value`、`joins_to`、`synonym_of`、`can_access`、`derived_from`、
  `guards`。
- VID：稳定的 `type:object_id:version`；查询只能从已知候选 VID 出发，路径限定 1～4
  跳，并设置 limit、timeout 和 trace。

### 3.2 六类在线能力

1. 从召回指标扩展合法维度、实体和认证数据集。
2. 验证 DimensionValue 对已选维度、层级和有效版本的唯一归属。
3. 验证完整 Binding Bundle 的指标、维度、值和时间兼容性。
4. 返回认证 Join 候选，由 Go GraphPlanner 按 fanout、跨源、陈旧和权限成本选路。
5. 在检索和规划前传播角色、租户和用途权限，非法节点和边不得进入候选集。
6. 语义变化时查找受影响指标、示例、黄金问题和下游数据集。

### 3.3 一致性和降级

同步 Worker 对新版本执行幂等 UPSERT，校验节点/边数量、孤儿、重复 VID、必达路径和
受影响黄金问题；通过后才在 PostgreSQL 原子切换活动版本。图不可用时只允许读取
`semantic_version` 与当前活动版本完全一致的认证 GraphPlan 缓存，否则进入阻断状态，
禁止回退到未经同版本验证的旧路径。

现有 PostgreSQL 属性图在迁移期保留为核对源和回放依据，但在线规划读取将在阶段 2
切到 `SemanticGraph` 接口后的 NebulaGraph 实现；完成对照回归前不删除历史表。

## 4. 分阶段实施与提交边界

每个阶段遵循相同顺序：实现 → 单元/契约/集成验证 → 方案一致性复查 → 独立 commit
→ push `origin/master`。任何 P0 安全或正确性门禁失败时不得进入下一阶段。

### 阶段 0：审计、ADR 与基线

交付：本审计、NebulaGraph ADR、仓库级实施清单、测试基线。

退出门槛：Go 全量测试、前端 lint/build、静态门禁通过；明确旧实现保留/替换边界。

### 阶段 1：可执行语义资产与原子版本

交付：统一 Semantic Registry 合同；补齐 Domain/Entity/SemanticModel/Metric/
DimensionValue/Time/Cohort/Relation/Policy/DQ 的最小生产字段；`semantic_version` 与
`content_hash`；候选构建、校验、发布和回滚状态机；资产就绪 API 和治理页面。

退出门槛：不完整或冲突合同无法发布；P0 指标必须具有 Owner、公式、粒度、时间、
固定过滤、来源、权限和质量规则；投影版本不能部分激活。

### 阶段 2：NebulaGraph 投影和 GraphPlanner

交付：本地 Compose/生产配置、nGQL Schema、`nebula-go` Session Pool、参数化查询、
超时/熔断/trace；幂等同步 Worker；版本切换；`SemanticGraph` 接口；六类在线图能力；
低风险路径评分、最多 4 跳和多指标受约束连通子图；版本一致缓存降级。

退出门槛：节点/边/孤儿/VID/必达路径契约测试通过；P0 图路径 100%；未知基数、未认证
和未治理多对多边不能执行；图不可用不能降低权限或版本门禁。

### 阶段 3：对象检索、联合绑定和三路径问答

交付：Span/AlignmentMap；Alias Registry；维度作用域值字典；全文、向量、认证示例和
图候选融合；最多 20 个 Bundle、Top 3 图闭包、硬约束和最小澄清；统一 Question
Orchestrator；Semantic IR 主路径；多指标共同粒度；长尾路径保持默认关闭，直到 Guard
满足门槛。

退出门槛：指标 Recall@10、Bundle Exact 和歧义 Recall 可离线统计；越权对象召回为 0；
无认证执行子图时不得生成 Join 或执行查询。

### 阶段 4：Evidence Tool Loop、编译、Guard 和验证

交付：完整 Tool Host、PolicyScope 注入、类型化错误和修复矩阵；三轮/调用/查询/时间/
成本预算；循环签名和无新 evidence 停止；执行语义层适配；SQL AST/allowlist/
`EXPLAIN`/成本门禁；结果恒等式、DQ 和 AccuracyEvidence。

退出门槛：Tool Loop 净恢复率为正且权限回归为 0；语义路径 P0 编译/结果回归 100%；
答案新增数字、单位或时间必须被忠实性检查阻断。

### 阶段 5：产品闭环、评测和发布门禁

交付：React 口径卡、澄清卡、验证进度、结果与证据抽屉、资产治理和评测页面；
development/validation/sealed/production_regression 数据集；结果等价比较、Wilson 区间、
分层报告、安全/性能测试和 CI 门禁。

退出门槛：测试集规模和真实数仓输入满足 v1.3 后，点估计不低于 96% 且 95% Wilson
下界不低于 95%；P0 指标和越权阻断 100%，敏感泄漏 0，直接回答覆盖率不低于 85%。

## 5. 旧实现迁移规则

- 不直接删除 PostgreSQL 图表、旧 QueryPlan 或历史 evidence；先双写/对照、再切读、
  最后延迟下线，保证历史问答可回放。
- 不修改已经执行的历史 migration；所有数据库修正使用新的前向迁移。
- 不把未经审批的 LLM 输出、聊天记录、原始 PII 或历史自由 SQL写入权威语义合同。
- 不允许以“SQL 可执行”“图 generation READY”或用户点赞代替端到端准确率。
- 不为了启用路径 B 而降低 AST、GraphPlan、权限、成本和结果验证门禁。
- 现有资产、指标和数据集服务通过适配器逐步接入统一 Registry，不进行一次性破坏性重写。

## 6. 当前基线

审计时本地验证结果：

- `go test ./...`：通过；
- `npm run lint`：通过；
- `npm run build`：通过；
- `sh scripts/ci-check.sh`：通过。

该结果只证明当前代码可构建和既有回归通过，不代表 v1.3 的图、正确性和生产完成定义
已经达成。后续每个阶段都必须在此基线上增加相应契约和集成测试。
