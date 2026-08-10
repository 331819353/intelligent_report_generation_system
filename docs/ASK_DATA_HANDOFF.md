# 智能分析决策平台实施 Handoff

> 实施依据：[ASK_DATA_CODEX_TODO.md](./ASK_DATA_CODEX_TODO.md)
> 架构依据：[ASK_DATA_TECHNICAL_DESIGN.md](./ASK_DATA_TECHNICAL_DESIGN.md)
> 产品基线：[可信智能问数与智能报表一体化平台_最终产品设计方案.md](./可信智能问数与智能报表一体化平台_最终产品设计方案.md)
> 技术基线：[可信智能问数与智能报表一体化平台_最终技术设计文档.md](./可信智能问数与智能报表一体化平台_最终技术设计文档.md)
> 闭环基线：[智能分析决策平台_功能模块与业务闭环审视.md](./智能分析决策平台_功能模块与业务闭环审视.md)
> 前端旅程基线：[智能分析决策平台_前端用户旅程与操作流程.md](./智能分析决策平台_前端用户旅程与操作流程.md)
> 页面门禁：任何新增页面、流程或显著视觉状态的 `WEB-*`、`WEB-RPT-*` 编码开始前，必须先提交页面设计稿并
> 取得用户确认；设计稿必须标注覆盖的 J-Pxx-xx 旅程以及主路径、异常、权限和终态；纯 API Client、类型和测试接线不触发页面门禁，但仍必须满足 TODO 依赖。

## 2026-08-10 平台定位与闭环交接（最新优先级）

- 项目统一定位为面向业务人员的「智能分析决策平台」；智能问数和智能报告是两个核心分析入口，不再以 AskData 代表全平台。
- 保留 P01～P14/M01～M19，新增 P15/M20「决策协同、行动跟踪与结果复盘」；其边界是决策支持，不直接执行外部业务交易。
- 逐模块主路径、异常、终态和当前实现证据见《功能模块与业务闭环审视》；缺口已进入 TODO §33～34。
- 任务完成必须分 BE、FE、E2E、BIZ 四层签署。页面有按钮、静态快照可展示或后端 API 存在均不等于业务闭环完成。
- 当前最优先的实施顺序：先去除问数/报告页面中的快照和提示性动作，再建分析首页与个人资产，再建 P15/M20，最后走真实业务旅程和生产签署。

## 2026-08-10 后端收敛交接（当前权威状态）

本节覆盖后文按日期累积的历史“下一步”快照；后文仍保留实现过程和设计决策，不应用其中的旧阻塞关系
判断当前完成度。本轮范围是**除前端页面外的全部软件模块**。

### 已完成后端能力

- 本轮补齐统一工作箱、真实会话历史、首页个性化读模型、报告关注、报告订阅/调度/站内分发、用户停用
  与全量 Owner Transfer SPI、受控运行配置，以及完整 Decision bounded context。API/Worker 已在
  `cmd/api`、`cmd/worker` 接线；前端页面不属于本轮交付范围。
- 工作箱支持 `kind=APPROVAL|TASK` 的服务端分类分页/计数、actor 已读、来源状态自动消失和受权详情/
  ActionCommand；详情仅返回来源对象、权威版本、固定版本/hash、有界差异/证据/风险摘要与字段约束，
  不复制业务事实或泄露无权标题。
- 会话历史支持 pinned/unpinned 稳定 keyset、搜索、置顶/取消置顶、归档、run 级分页和最新 ANSWER 标签；
  报告关注与保存问题分页、`auth/me` 共同补齐首页后端读模型。
- 报告调度固定已发布版本、时区和业务日历，覆盖 DST、lease、错过窗口、重试/自动暂停、手工恢复/
  幂等补跑、接收者当前权限重检、站内 delivery 和已读；未在 `HUMAN-015` 前扩展外部渠道。
- 用户停用以预览 → 计划 → 原子执行/失败重试处理 Transfer/AutoClose/ReadOnly/Block；覆盖数据、语义、
  Release、报告/模板/计划、保存问题、反馈/取数、决策/行动/审批和 runtime config。认证事实只允许专用
  SYSTEM owner-transfer 事务修改 owner 元数据，数据库/服务只阻断最后一名活跃平台/领域管理员。
- 运行配置完成无密钥白名单、canonical hash、草稿/提交/审批/驳回、分阶段 rollout、失败停止、有效版本、
  回滚和 append-only 审计；创建者不能自批，Worker 使用 tenant discovery 最小权限。
- 决策完成四份公共 schema/共享正反 fixture、独立 `decision` schema、三类可验证 Evidence 服务端固定、
  审批、行动、due/overdue 通知、Outcome 当前策略重放、人工复盘、关闭/重开。列表用单查询支持四 scope、
  筛选/排序/keyset/总数/scopeCounts/Owner/证据/行动摘要，并提供策略目录和 Evidence prefill；HUMAN-014
  未确认前明确拒绝 `decisionType`，不固化候选枚举。
- `RPT-001` 已完成报告 DSL 规范化、12 阶段校验、富文本清洗、规范 JSON 和稳定 hash，并通过常规、
  竞态和基准验证。报告主链已继续完成运行时、筛选交互服务、Insight Engine/Method Registry、AI
  两阶段生成与局部修改、真实 CSV/XLSX/PDF/PNG 导出、无匿名分享、模板版本保留与受控升级。
- `FUSE-001`～`005` 已完成：问数加入报告、报告资产投影/单人认证、从报告发起问数、图表推荐、依赖
  影响分析及预览/确认式升级。升级确认会固定当前 actor/release 重新编译 Semantic IR，用同一受治理
  runtime 对比样本，只在确认 token 未过期时追加编译制品和一个新草稿修订，不改写旧版本。
- 发布控制面已完成 `DB-007/008`、`REL-001`～`006`：数据库从事实重算 Wilson 门禁、LLM 评审仅提供
  建议、双人审批职责分离、单事务 ACTIVE 切换及失败关闭。没有业务评测事实时仍不会激活。
- 评测/运营后端已完成端到端 equivalence runner、反馈归因、Shadow/Canary 统计、密封集轮换/曝光
  退役、误差预算、保存问题、反馈工单与主动学习；明细取数已补齐敏感推导/安全会签、受控导出桥和
  重复申请资产化聚类。
- 运维后端已完成严格配置、独立 Worker 入口、结构化观测、质量/成本视图、四级配额与成本归集。
  灾备演练、生产容量 POC 和压测报告仍必须在目标环境实际执行，不能由单元测试替代。

### 数据库与安全边界

- 当前迁移序列到 `000297_release_gate_receipt_hash_ambiguity`。`000281`～`000297` 新增/修复 Decision、
  统一工作箱、会话历史、报告调度、Owner Transfer、运行配置、报告关注、审批追加事实/worker discovery、
  状态机、delivery read、runtime reject/trigger、认证语义转交覆盖、最后管理员保护及评估回执变量歧义。
- `scripts/migrate.sh` 对新增控制面重置宽授权后按 API/Worker 职责最小授权；`scripts/verify-database.sh`
  已逐表检查迁移记录、FORCE RLS、状态机/append-only 触发器、SECURITY DEFINER 和 app/worker/
  connection-test 权限边界。
- `platform.report_semantic_compilations` 强制 RLS 且禁止更新/删除；应用角色仅有 `SELECT,INSERT`，
  Worker/连接测试角色无表级权限。运行时只经 SECURITY DEFINER loader 读取与 READY 不可变报告版本、
  当前查看者权限、组件、plan hash、release/hash 和 IR hash 全部精确匹配的制品。
- 分享 token 只用于定位，永不授予匿名权限；保存问题和报告升级均按当前查看者策略重新执行，不能复用
  他人结果或旧权限快照。

### 已验证与仍需外部完成

- 已通过：新增模块定向/真实 PostgreSQL 测试、共享 Decision Go/TypeScript 合同、Web 单测、主库
  migrate/verify。全仓 Go、race、vet、compose/CI 和最终差异检查的本轮终态命令见本节末尾新增验证记录。
- 前端页面仍按用户指定排除：全部 `WEB-*`、`WEB-RPT-*`，以及混合任务 `ADD-002`、`RPT-006`、
  `RPT-008`、`RPT-011` 中的页面/打印样式部分。它们的后端范围已完成。
- `ASK_DATA_CAPACITY_BASELINE.md` / `ASK_DATA_DISASTER_RECOVERY.md` 已扩展平台混合负载、资源预算、
  RTO/RPO 计量点、全控制库/对象存储/数仓/Nebula/队列恢复顺序、闭包检查，以及调度/导出/通知/决策
  跨恢复点的确定行为。`OPS-007` 仍需目标拓扑混合压测、故障注入、实际恢复收据和三方签署。
- 仍需真实业务/生产输入：`EVAL-008`～`010` 的业务黄金样本与 150 条双人评审、`OPS-003/004`、
  `OBS-003`、`OPS-007` 目标环境部分、`PILOT-001`～`004`。在这些完成前不得宣称 Recall 门槛、95%
  准确率、灾备 RTO/RPO 或生产容量已经验收。

## 0. 板块蓝图同步（2026-08-06）

TODO 已按**功能区（板块 B01～B13）**重新组织，见 `ASK_DATA_CODEX_TODO.md` §0 与 §22～§32。交接时必须先读本节。

### 0.1 Wave 与板块的关系

`Wave` 是时间轴，`板块` 是责任面，两者正交。原有 Wave 0～Wave 5 的任务编号、状态和门禁**全部保持不变**，只是额外标注了所属板块；新增任务统一挂在板块下，并追加了 Batch 7～Batch 12。

### 0.2 板块当前状态

| 板块 | 名称 | 状态 | 关键缺口 |
|---|---|---|---|
| B01 | 平台底座与权限 | 安全基线、统一工作箱及用户停用/Owner Transfer 后端完成 | 业务导航和用户生命周期页面 |
| B02 | 数据接入与元数据 | 明细取数、敏感会签、受控导出与资产化聚类后端完成 | 前端入口及真实审批责任人配置 |
| B03 | 数仓建模与物化 | 仓库基线 + `SNAP-001` 数据快照版本完成 | 后续增量物化与运维增强 |
| B04 | 语义资产治理 | 后端主链、DRAFT、时间/可加性、导入、词典、KPI 与建议器完成 | `ADD-002` 前端补录入口 |
| B05 | 语义发布与投影 | READY、评测门禁、双人审批、ACTIVE 原子激活与保留策略完成 | 真实业务审批/评测事实 |
| B06 | 检索与语义图谱 | 主链、降级、Recall@K 评测器与 ANN/Exact 审计完成 | 正式人工黄金集 |
| B07 | 问句理解与联合绑定 | 主链、`NLU-007` 登录后已选领域强制 Pin、`NLU-008` 会话 Release Pin/澄清超时与 `NLU-009` 范围白名单/正确拒答完成 | — |
| B08 | 查询编译与执行 | 主链、`ADD-003/004`、`QUERY-007`～`011` 全部完成 | — |
| B09 | 编排与问数 API | Loop/API、答案校验重生成、预算/幂等与叙述观测门禁完成 | — |
| B10 | 问数工作台前端 | `WEB-001`～`WEB-006` 与 `WEB-011` 明细取数申请工作区完成 | `WEB-007`～`010`、`WEB-012`～`016`；尤其是真实会话和分析资产动作 |
| B11 | 报表引擎与融合 | 后端引擎、发布/回滚/导出/分享/升级、融合及订阅/调度/站内分发完成 | 前端运行时/筛选/打印及 `WEB-RPT-001`～`008` |
| B12 | 评测、反馈与运营 | 软件控制面、密封集、误差预算、保存问题与反馈闭环完成 | 正式黄金样本与人工评审 |
| B13 | 运维、可观测与成本 | 配置、独立 Worker、观测、看板、配额/成本、运行配置后端及平台 DR/容量合同完成 | `OPS-008` 页面；目标环境灾备/容量/生产 POC 与签署 |
| B14 | 决策协同与行动复盘 | 合同、数据库、服务/Worker、Evidence、审批、行动、复盘和列表后端完成 | `WEB-DEC-001`、HUMAN-014 与业务旅程 E2E |

### 0.3 本次新增的任务数量

| 来源 | 新增任务 |
|---|---|
| 产品/技术设计文档第五部分的口径裁定与功能补全 | `TIME-001`～`004`、`ADD-001`～`004`、`IMPORT-001`～`005`、`TERM-001/002`、`KPI-001`、`RETAIN-001`、`SNAP-001`、`PROJ-002`、`GRAPH-006`、`SEARCH-006`、`NLU-007`～`009`、`QUERY-007`～`011`、`ORCH-007`～`009`、`ANS-001`～`004`、`WEB-008`～`013`、`EVAL-008`～`012`、`SQ-001`、`FB-001/002`、`DR-001`～`003`、`OBS-003`、`OPS-006` |
| 原计划完全缺失的报表板块 | `RPT-CONTRACT-001`～`004`、`RPT-DB-001`～`005`、`RPT-001`～`013`、`FUSE-001`～`005`、`WEB-RPT-001`～`007` |

基线预留 **17 个迁移编号（`000225`～`000241`）** 与 **9 个 JSON Schema 合同**；实施中为
`IMPORT-005`、`SEARCH-006`、`NLU-008`、`DR-001`、`ORCH-008`、`ANS-003`、`ORCH-009` 追加 `000242`～`000248`；后续迁移已推进至
`000273_report_rollback_integrity`；`000274_askdata_quota_runtime` 已被配额任务占用，`000275_report_asset_governance` 已由 `RPT-013` 实现并在本地应用；后续已顺序推进到 `000280_report_semantic_upgrade_compilations`，当前迁移分配以 TODO §22.1
为准，不得重复占用。

### 0.4 新增的不可违反约束

在原有十条全局约束之外，本次同步追加：

11. 报告数据绑定必须声明 `bindingMode`，`SEMANTIC_IR` 与 `DATASET_FIELD` 二选一；只有前者能反哺问数。
12. 不可加指标（比率、去重）绝不得被 `SUM`/`AVG`；半可加指标缺时间聚合声明则编译失败。
13. 未结束周期默认 `MTD`，策略优先级为 指标级 > 时间合同级 > 业务域级 > 平台默认；实际区间必须对用户可见。
14. 任何叙述性文字必须通过事实校验；校验失败降级为结构化答案，**不得输出未校验文本**。
15. 报告分享不存在匿名类型，令牌只定位不授权。
16. 被引用的 Semantic Release 不得 `RETIRED`，必须进入 `RETAINED` 保留态。
17. 物化日常刷新只变更 `dataSnapshotVersion`，**不得**使 Release 进入 `STALE`。
18. 密封集按分片轮换，被查看的样本立即退役，绝不得用于修复和调参。

## 1. 当前状态

- 当前 Wave：Wave 0 已全部完成；Wave 1 注册表主链已完成；Wave 2A LLM
  认知协议已完成；Wave 2B 已完成画像合同、有界成员扫描 Worker、generation
  规范化/异常候选接线，以及分类文档、Embedding、混合检索和受约束 LLM 重排主链。
  Wave 2C 已完成 NebulaGraph 服务端/Go Client 兼容 POC、正式开发 Compose/初始化、
  GraphPlan 合同/Adapter、按 release/lease 幂等运行的正式 Projector Worker，以及
  Nebula/认证缓存/PostgreSQL 有界降级 Resolver，以及 `GRAPH-006` 六行失败关闭矩阵、熔断、指标和
  Evidence/SSE/UI 降级证据，以及 `SEARCH-006` label-free 查询向量、ANN/Exact `recall@K` 定期审计、
  小集合 exact 路由与 embedding 模型/维度门禁；`SEARCH-005` 仍依赖未提供的人工黄金集，B06 除该人工
  评测门禁外已完成。
  Wave 3A 已完成确定性问句规范化、原文
  span 回映、时间/比较/
  查询语法解析、安全会话上下文合并、受控 LLM 完整理解与取证计划、联合候选 Binder/
  Bundle Beam Search、基于 held-out 验证集的置信度校准与定向澄清、`NLU-007` 登录后已选业务域
  的策略强制 Pin，以及 `NLU-008` 会话 Release Pin、Release 漂移确认、澄清预算冻结/恢复与超时终态，
  `NLU-009` 15 类范围白名单、定义短路径与正确拒答出口；
  Wave 3A 已完成，
  Wave 3B 已完成 Binding Bundle -> Semantic IR、pinned Semantic Contract Resolver、
  IR -> Dataset Query DSL Adapter、计划 Validator/安全 EXPLAIN、只读问数执行适配，以及规则优先的
  结果核验/异常分析；Wave 3B 已完成。
  Wave 4B 已提前完成无外部依赖的 `EVAL-001` 结果
  规范化与等价判定、`EVAL-002` mention/binding 指标和校准训练/验证输入合同，以及 `EVAL-003`
  纯内存 Fixture Regression Runner。Wave 3C 的 `DB-005` 问数
  运行/审计控制面、`ORCH-002` Question 状态机/PostgreSQL Store，以及 Wave 4B 的
  `DB-006` 评测集、双人评审、追加式评测运行与结构化反馈迁移均已完成。Wave 4C 的
  `SEC-003` 敏感成员敏感度下限、数据库内 EXACT_ONLY、label-free evidence/LLM 遮罩和
  不披露授权边界也已完成；`ORCH-001` Typed Tool Registry、`ORCH-003` LLM 中枢 Agent Loop、
  `ORCH-004` 审计/预算/幂等、`ORCH-005` Question API/SSE、`ORCH-006` Conversation/运行保留策略和
  `ORCH-008` RunType 独立预算/P95 与熔断分离、`QUERY-009` 认证 Query Plan Bundle 独立编译/校验、
  四并发执行与逐项 PARTIAL 聚合，以及 `QUERY-011` P1～P6/Q1 确定性 outcome 与报告导出门禁
  已完成。用户已确认
  `DB-004` 以 release/
  READY 投影水位为完成边界，ACTIVE 原子切换归属 `REL-005` 并继续按门禁保持关闭；当前
  剩余任务的依赖/人工输入阻塞
  见“下一步”。用户已确认 `WEB-001` 方案 3「证据驾驶舱」、`WEB-003` 方案 1「运行中纵向时间线」、
  `WEB-004` 方案 1「内联双候选决策卡」、`WEB-005` 方案 1「KPI 总览 + 趋势与渠道并列」和
  `WEB-006` 方案 3「两步结构化反馈弹窗」、`TIME-003` 方案 2「轻量时间口径 disclosure + 证据同步」、
  `GRAPH-006` 方案 1「琥珀色次级关系降级证据」、`NLU-008` 方案 1「原位口径更新卡 + 右侧
  Release Pin 证据」、`WEB-011` 方案 3「我的申请主从工作区」，以及 `ANS-003` 方案 2「证据优先的
  结构化结果 + 文字结论隐藏说明 + 答案层级」。
  受保护 `/ask-data` 工作台、真实 Question API/SSE client/hook、受控运行进度、取消、失败关闭和终态
  可访问提示、定向澄清、真实治理证据投影、防分叉消费合同、结果 KPI/图表/表格、有界分页和绑定终态
  run 的结构化反馈均已完成；`REG-006` DRAFT 语义管理 API 已完成，下一项前端主线 `WEB-007`
  仍被未完成的 `REL-005` 阻塞。
- 已完成：`CONTRACT-001`～`CONTRACT-004`、`BASE-001`、`BASE-002`、
  `DB-001`～`DB-004`、`REG-001`～`REG-004`、`AI-001`～`AI-004`、
  `DIM-001`～`DIM-003`、`SEARCH-001`～`SEARCH-004`、`GRAPH-001`～`GRAPH-003`、
  `NLU-001`～`NLU-004`、`DB-005`、`ORCH-002`、`DB-006`、`EVAL-001`、`EVAL-002`、`EVAL-003`、
  `SEC-001`、`SEC-003`、`WEB-001`、`GRAPH-004`、`GRAPH-005`、`NLU-005`、`NLU-006`、`QUERY-001`、
  `QUERY-002`、`QUERY-003`、`QUERY-004`、`QUERY-005`、`QUERY-006`、`ORCH-001`、`ORCH-003`、
  `ORCH-004`、`ORCH-005`、`ORCH-006`、`REG-006`、`WEB-002`、`WEB-003`、`WEB-004`、`WEB-005`、`WEB-006`、
  `TIME-001`、`TIME-002`、`TIME-003`、`TIME-004`、`SNAP-001`、`ADD-001`、`ADD-003`、`ADD-004`、
  `IMPORT-001`、`IMPORT-002`、`IMPORT-003`、`IMPORT-004`、`IMPORT-005`、`TERM-001`、`TERM-002`、`KPI-001`、
  `RETAIN-001`、`PROJ-002`、`GRAPH-006`、`SEARCH-006`、`NLU-007`、`NLU-008`、`DR-001`、
  `QUERY-007`、`QUERY-008`、`QUERY-009`、`QUERY-010`、`QUERY-011`、`WEB-011`、`NLU-009`、`ORCH-007`、`ORCH-008`、`ORCH-009`、`RPT-CONTRACT-001`、
  `RPT-CONTRACT-002`、`RPT-CONTRACT-003`、`ANS-001`、`RPT-CONTRACT-004`、`ANS-002`、`ANS-003`、`RPT-DB-001`、`RPT-DB-002`、`RPT-DB-003`、`RPT-DB-004`、`RPT-DB-005`、`RPT-001`、`RPT-002`、`RPT-003`、`RPT-004`、`RPT-005`、`RPT-007`、`RPT-013`。
- 发布边界：`DB-004` 已完成 release manifest、四投影、lease、READY 收敛、`release_state`
  和 GraphPlan cache；ACTIVE 激活属于 `REL-005`，必须等待 `DB-007` 评测门禁和 `DB-008`
  双人审批，当前故意不存在。
- 当前已有数仓盘点结果：本地控制库没有“当前 PUBLISHED + ACTIVE”的 DWS/ADS，
  因此正式导入结果为空；使用回滚事务中的历史 DWS 合成发布夹具已验证真实导入链路。
- 生产准确率状态：尚未评测，不得宣称达到 95%。
- 板块视图：2026-08-06 已按功能区把计划重组为 B01～B13 十三个板块（见 §0 与 TODO §0）。
  B04～B10 问数主链已有多项完成，B11 已冻结 Report Definition v1、13 个组件 Manifest、41 类操作协议
  以及与问数共享坐标/失效规则的 Evidence Bundle/Insight Artifact；
  **报表板块仍有 18 项任务待完成**，是当前最大的建设面；`RPT-013` 已补齐报告清单、权限、发布状态与上下架后端治理，`WEB-RPT-007` 已完成用户确认稿与核心链路但仍保留生产交互缺口；`RPT-001`～`005` 与 `RPT-007` 已闭环 DSL 规范化、阶段校验、
  富文本清洗、稳定 hash、41 类原子操作、可验证 Undo/Redo 与 Go/TS 确定性布局。时间口径链 `TIME-001`～`004` 已闭环合同、确定性编译、四处统一展示和
  数据可用边界分流。指标可加性存储与三道独立关卡 `ADD-001`、编译规则 `ADD-003`、统一结果与合计/
  堆叠合同 `ADD-004` 已完成；批量导入的存储/状态机/可恢复 Worker `IMPORT-001` 和动态模板
  `IMPORT-002`、四层校验/报告 `IMPORT-003`、批量提交/审批/撤回 `IMPORT-004` 与对称导出
  `IMPORT-005` 已完成；版本化业务词、冲突裁决与安全正则 `TERM-001`、Release 固定的 Aho-Corasick
  确定性匹配 `TERM-002` 和认证默认答案组合 `KPI-001` 也已完成；Batch 8 主链闭环。B05 的
  `RETAIN-001` Release 引用计数、历史保留与可重建投影清理，以及 `PROJ-002` 四投影哈希运行门禁也已
  完成；B06 的 `GRAPH-006` 图不可用六行降级矩阵、熔断、观测和用户确认的证据视觉状态已闭环，
  `SEARCH-006` ANN/Exact 召回对照作业也已闭环；`NLU-007` 已按用户澄清改为“登录后、进入问数前已选
  领域”的强制单域合同并完成；`NLU-008` 的会话 Release Pin、澄清超时和方案 1 可见状态也已完成。
  `DR-001` 后端申请合同、用户确认的 `WEB-011` 主从申请工作区与 `NLU-009` 15 类范围白名单、严格
  公开工件、正确拒答统计和 `SCOPE_DETAIL_LIST` 预填出口现已闭环。`ANS-001/002/003` 已冻结 Answer Artifact、
  Unicode citation、共享 stale 合同、问数/报告共用的叙述事实校验器，以及两次失败后只保留 L1 的降级
  编排与用户可见状态；答案校验重生成闭环已由 `ORCH-007` 收敛，观测门禁仍归属 `ANS-004`。
- 人工业务输入：用户已确认 `HUMAN-005` 的开发部分采用 v3.8.0、单副本共享 Space、持久卷、
  API GUEST/Worker USER 和环境变量开发凭据；生产容量、TLS、备份、多副本及
  `HUMAN-001`～`HUMAN-004`、`HUMAN-006` 仍未提供。
- 2026-08-05 本地运行环境已按用户要求全量重置：控制库、数仓、MinIO、历史
  NebulaGraph/Redis 卷均已删除并重新初始化；随后 seed 已创建 1 个 demo tenant、
  2 个用户，dataset/askdata domain 仍均为 0，没有导入业务语义资产。
- 当前 Compose 运行态健康：API `127.0.0.1:8080`、Web `127.0.0.1:5173`、
  Connector `127.0.0.1:8090`、初始化后 loopback proxy `127.0.0.1:9669`；API/Worker/
  Connection Test Worker、PostgreSQL、Warehouse PostgreSQL、MinIO、Nebula metad/
  storaged/graphd/proxy 均为 healthy。GRAPH-004 共用后端镜像已重建，worker 已强制替换并
  健康启动；graphd 本身没有宿主端口，init 退出容器已移除。

## 2. 已落地产物

### 2.1 公共合同

- `internal/askdata/doc.go`：包依赖方向和根包边界。
- `internal/askdata/contracts.go`：稳定 ID、版本引用、发布引用、证据引用、权限范围和校准置信证据。
- `internal/askdata/strictjson.go`：拒绝未知字段、重复键、尾随 JSON 和非法 UTF-8 的统一解码入口。

关键决策：

- 内容哈希统一为 64 位小写 SHA-256。
- `PolicyScope` 采用规范排序后计算的不可变 hash，可进入缓存键和审计。
- LLM 输出只能引用 Evidence ID/hash，不保存隐藏思维链；候选重排另使用绑定 release 的
  `CANDIDATE_SET` EvidenceKind，避免把整个候选集与单条检索/图证据混用。

### 2.2 QuestionUnderstanding

- Go 合同：`internal/askdata/understanding/model.go`。
- JSON Schema：`api/schemas/question-understanding-v1.schema.json`。
- 支持：domain hypothesis、指标/维度/成员 mention、Unicode span、时间、比较、排序、limit 和 unresolved span。
- 明确禁止：物理表名、列名、SQL 和临时语义对象定义。

### 2.3 Semantic IR

- Go 合同与规范化：`internal/askdata/ir/model.go`、`normalize.go`。
- JSON Schema：`api/schemas/semantic-ir-v1.schema.json`。
- IR 只包含发布版本 ID、成员版本 ID、受限操作符、日期范围、排序和 limit。
- 指标、分组、过滤成员采用确定性排序；相同语义输入生成稳定 JSON/hash。

### 2.4 Cognition Action 与 Tool Host

- Action 合同：`internal/askdata/cognition/model.go`。
- Tool 合同：`internal/askdata/toolhost/model.go`。
- JSON Schema：`api/schemas/cognition-action-v1.schema.json`。
- 已冻结 9 个认知阶段、8 个动作和 14 个工具名称。
- Tool 参数使用关闭的字段词汇表；未知工具、跨工具夹带参数、阶段不匹配和多个动作载荷全部失败关闭。
- 外层 Action Schema 校验后，Tool Host 仍强制执行第二次参数校验。

### 2.5 合成 fixture

- `internal/askdata/testfixture/fixture.go`。
- 包含合成租户、用户、DWS/ADS 模型、指标、维度、成员、关系、问题、IR 和结果。
- 已覆盖：同名指标、同名成员、越权、Join fanout、空结果、过期成员。
- fixture 必须带 `Synthetic=true` 才能通过校验。

### 2.6 只读资产盘点

- 命令：`cmd/askdata-inventory`。
- 服务与 PostgreSQL 适配：`internal/askdata/registry/inventory.go`。
- 只读取当前 ACTIVE 且 PUBLISHED 的 DWS/ADS dataset/materialization。
- 使用 PostgreSQL `READ ONLY + REPEATABLE READ` 事务，不连接 Warehouse 数据库。
- 默认隐藏 published schema/view，只输出不可逆物理引用 hash；显式参数才可展示物理标识。
- 从已发布 DSL 确定性提取字段、粒度和时间字段，并复核 DSL hash 与 materialization hash。

运行示例：

```sh
DATABASE_URL='postgres://...' go run ./cmd/askdata-inventory \
  --tenant-id '<tenant-uuid>'
```

### 2.7 `askdata` 数据库控制面

- 迁移：`000213`～`000218` 为 askdata 主体，`000221` 清理退役语义运行时遗留的
  tenant 初始化触发器，`000222` 增加有界画像运行时；均包含 `.up.sql` 与
  `.down.sql`，`000219`～`000220` 仍按 TODO 为后续发布门禁与审批预留。
- `askdata` 与已退役的 `platform.semantic_*` 完全隔离；没有恢复历史运行时表。
- 38 张控制面/投影/画像/问数/评测表全部启用并强制 RLS；所有数据库外键均包含 `tenant_id`。
- 认证版本及其 metric-measure、hierarchy-level 子链接不可原地修改。
- 通用质量目标、别名目标、成员父子关系由触发器验证同 tenant/domain 和认证状态。
- 维度成员策略固定为 `FULL/EXACT_ONLY/ON_DEMAND/NONE`：高基数只能
  `ON_DEMAND/NONE`，敏感维度只能 `EXACT_ONLY/NONE`。
- 向量列为 `halfvec(2560)` + HNSW；敏感/高基数/非 FULL 成员无法进入 embedding outbox。
- release 只有四个投影 hash 全部一致才进入 READY；GraphPlan cache 必须绑定 READY
  的 NebulaGraph 投影和相同 release hash。
- 语义模型进入 release 时会再次验证其 DWS/ADS 仍是 current PUBLISHED + ACTIVE，
  不是只在导入时检查一次。

运行角色权限由 `scripts/migrate.sh` 每次部署先清空再显式授予：

- `report_app`：可编辑 registry DRAFT、写检索文档、创建密封 release；不能写
  embedding/projection worker 状态，也不能直接更新 release 生命周期；可创建/推进自己
  的问数 run 并追加 event/artifact/tool outcome，但不能删除 run 或改写追加式审计；仅领域/
  平台管理员可管理 DRAFT evaluation set/case/review，原 actor 可创建/乐观更新自己的反馈。
- `report_worker`：可处理 embedding outbox、投影 artifact、GraphPlan cache 和有界
  画像 job；profile/member observation 只能追加写，不能更新，且不能写权威语义对象。
  问数运行与审计当前只读，不能替 API actor 追加或推进状态；SYSTEM 模式可读取密封黄金题，
  但对评测控制面只有 `evaluation_runs` INSERT，不能改写 set/case/review/feedback。
- `report_connection_tester` 与 PUBLIC：无 `askdata` schema 权限。

### 2.8 Go 语义注册表与发布包

- `model.go` / `validation.go`：Entity、SemanticModel、Measure、Metric、Dimension、
  Hierarchy、Relationship、QualityRule、BusinessTerm 领域合同。
- Validator 使用稳定 `code + path`；确定性校验可加性、依赖、维度策略、关系基数、
  fanout、质量状态和 AST 安全边界，AST 禁止 raw SQL/nGQL/query/secret 字段。
- `postgres_store.go`：所有操作均使用显式 tenant transaction；metric 更新使用
  record version 乐观锁；列表使用 `(updated_at,id)` keyset cursor；跨领域查询表现为 Not Found。
- `canonical.go` / `release.go` / `object_contract.go`：JSON 键排序、数字规范化、
  精确对象版本排序、稳定 content/release hash 和幂等 release ID。
- `admin.go` / `admin_store.go` 与 `internal/askdata/http/admin.go`：认证业务域内的 DRAFT-only
  管理 CRUD、稳定分页、乐观锁、逐次权限复核和 audit-backed 幂等写；只允许创建 DRAFT manifest，
  不提供 validate/project/evaluate/approve/activate 生命周期入口。
- `importer.go` / `importer_store.go`：只读 current published + active DWS/ADS；复用
  Dataset DSL 稳定 field ID；确定性生成 model/measure/dimension DRAFT，绝不自动认证。

### 2.9 AI 环境参数处理

- 用户提供的 DeepSeek、GLM、Embedding 变量名和非密钥参数已确认；`.env.example`
  已同步长耗时配置：API write 650s、request 300s、attempt 270s、failover 75s、2 次尝试。
- API key 没有写入仓库、测试输出或本 handoff；部署时通过本地 `.env`/Secret Manager 注入。
- 密钥曾出现在会话正文中，建议在正式环境启用前轮换。
- 2026-08-05 已将三组 key 仅注入 Git 忽略的本地 `.env`，并通过非回显检查确认
  API 与 Worker 均能读取；仓库受版本控制文件和本文仍不包含 key。

### 2.10 LLM 认知执行协议

- `internal/ai` 新增 `SEMANTIC_QUESTION` Purpose；数据库策略白名单支持该值，
  但迁移不为任何租户自动授权，默认失败关闭。显式授权后复用统一配额、模型选择和摘要审计。
- `internal/askdata/cognition/executor.go`：每次只执行一轮受审计模型调用；空动作、
  未知动作、阶段错配、`length` 截断、重复 action hash 和重复 tool_call ID 全部失败关闭。
- assistant 决策与 typed tool result 可进入下一轮消息；Tool Message 必须带 call ID/name，
  且先通过 `toolhost.Response.Validate`。Provider reasoning content 不进入执行结果或审计合同。
- `prompts.go` / `schemas.go`：九个阶段分别限制可见 FactKind 和动作枚举；事实 payload
  规范化并绑定 evidence hash，SQL/nGQL/凭证键禁止进入；元数据中的指令被明确视为不可信数据。
- DeepSeek/GLM fixture 覆盖 `json_schema`、`json_object`、thinking、refusal、length、
  preferred-model 路由和 tool no-progress，不依赖真实 API key。
- Provider 的 `MaxOutputTokens` 配置被定义为硬上限，不再覆盖调用已预留的更小 token 预算。

### 2.11 维度画像、成员规范化与索引策略

- `internal/askdata/dimension/model.go` / `policy.go`：记录基数、NULL、保留值 hash/计数、
  成员变化率、敏感性、扫描预算和实际使用量；画像与决策都有可复核 content hash。
- 确定性决策输出 `FULL/EXACT_ONLY/ON_DEMAND/NONE`：低基数稳定且非敏感才可 FULL；
  高基数走 ON_DEMAND；敏感+高基数或 RESTRICTED 走 NONE；不完整扫描不会误判为 FULL。
- `normalize.go`：仅做 NFKC、空白和大小写等无损规范化；canonical value 与 aliases 分开，
  成员 key 为 `dimension_version_id + normalized_value`，避免跨维度同值误绑。
- UNKNOWN/NULL/N/A/测试等哨兵值不会成为成员候选，只记录 catalog version、hash 和计数；
  Profile 合同已升至 `1.1`，画像 input version 升至 `dimension-profile-v2`，避免旧 generation
  与新增 catalog/规范化证据合同复用同一幂等输入。
- `cognition.go`：DIM-002 的 Profile + MemberObservation 可确定性生成有界
  `DIMENSION_PROFILE` Prompt Fact；最多选择 64 个高频成员，并记录 omitted count、扫描
  完整性、profile/source hash。候选 ID 由 dimension-bound member key hash 稳定派生，
  不把观测值伪装成已认证 member version。
- 确定性 Unicode/大小写/空白等价只生成低风险别名候选；LLM 可提出别名、聚类、层级或
  哨兵异常，但每个成员、别名和 EvidenceRef 都必须回绑同一 generation。发明成员、跨
  generation/hash 引用、未观测别名、敏感成员和重复 proposal 均失败关闭。
- LLM proposal 永不自动应用；扫描截断或有成员因 Prompt 上限被省略时，确定性候选也保持
  review-only。该流程只返回可复核候选，不写入或认证 `dimension_members`。
- `worker.go` / `postgres_store.go`：只同步当前 PUBLISHED + ACTIVE DWS/ADS 的精确
  materialization snapshot；任务以 input hash 和递增 generation 幂等，具备租约、
  `FOR UPDATE SKIP LOCKED`、最多 3 次指数退避、配置/源版本过期检测。
- Warehouse 扫描限定 `warehouse_published`，使用 `READ ONLY + REPEATABLE READ`、
  `statement_timeout`、最大行数/成员数/样本字节，并通过服务端引用安全转义列名。
  RESTRICTED 或 `NONE` 维度直接记为 SKIPPED，不读取任何业务行。
- `dimension_profile_jobs`、`dimension_profiles`、`dimension_profile_members` 保存可审计的
  扫描预算、实际用量、profile/policy hash 和规范化成员观测；这些是候选证据，绝不因
  Worker 运行而自动升级为 `dimension_members` 认证资产。

### 2.12 分类文档、Embedding Worker 与混合检索

- `internal/askdata/search/document.go`：METRIC、DIMENSION、MEMBER、BUSINESS_TERM、
  CERTIFIED_EXAMPLE 各自使用独立模板/version/hash。MEMBER 文档同时写入维度名称、
  维度描述、canonical value 和 aliases，解决无维度上下文的同值竞争。
- 凭证形态、物理 SQL/nGQL、RESTRICTED 对象、敏感/高基数/非 FULL member 无法进入向量文档。
- `worker.go` / `postgres_store.go`：batch、租约 token、指数退避、input hash + model 幂等；
  文档或 lease 改变后旧结果更新 0 行并回滚，2,560 维不匹配失败关闭。
- `cmd/worker` 已启动 AskData embedding loop，复用既有 embedding Provider；Provider 未配置时安全空转。
- `retriever.go` / `retrieval_postgres.go` / `rank.go`：exact、pg_trgm/全文 lexical、
  halfvec cosine vector 三路召回；SQL 内先固定 tenant、USER RLS、domain、release hash、
  READY SEARCH_INDEX 投影和对象类型，再按类型 RRF。向量失败明确降级 exact+lexical。
- `reranker.go`：将最多 30 个受约束候选规范化为 `CANDIDATE_SET` Prompt Fact，绑定
  `PolicyScope`、release、检索 evidence、定义、反例、图兼容结论和 deterministic gate；
  caller 输入顺序不会改变 fact hash，CONFIDENTIAL/RESTRICTED、凭证形态和物理查询文本
  不会进入模型上下文。
- Reviewer 只能按稳定 object version ID 返回集合内可选候选，并且只能引用该候选自己的
  retrieval/graph/gate evidence。发明 ID、跨候选引用、错误 candidate-set hash 或覆盖
  graph/policy block 均失败关闭；全部候选被拦截时不调用模型，直接返回可审计 no-match。

### 2.13 NebulaGraph 服务端/Go Client 兼容 POC

- 已锁定 NebulaGraph metad/storaged/graphd `v3.8.0` 与官方
  `github.com/vesoft-inc/nebula-go/v3 v3.8.0`，均使用 release tag，不依赖 master/nightly。
  `nebula-go/v5` 是面向新 gRPC Nebula Service 的另一客户端，不适用于本设计选定的
  3.x graphd fbthrift 接口。
- `deployments/nebula-poc/compose.yaml` 是完全独立、全部数据使用 tmpfs 的临时验证拓扑：
  单 metad、单 storaged、两个普通 graphd、一个 TLS graphd 和一个 TCP blackhole；没有
  修改仓库根 `compose.yaml`。正式持久卷、账号和服务接线随后已由 2.27 的 `GRAPH-002` 落地。
- POC 确认真实启动顺序必须是 metad → graphd → console `ADD HOSTS` → storaged ready；
  graphd 的 HTTP `/status` 会早于 thrift 查询端口可用，因此 Go 测试还执行有界 readiness
  retry，不能只依赖 HTTP 绿灯。
- `internal/askdata/graph/poc_test.go` 实测 meta/storage/graph 三类服务版本、Connection Pool、
  Space-bound Session Pool、TLS 1.2+、参数绑定、socket timeout、8 路并发和缺失 Space；
  主动停止 `graphd-a` 后同一 Session Pool 经 `graphd-b` 恢复查询，再恢复故障节点。
- 一键命令为 `scripts/verify-nebula-poc.sh`；证书在系统临时目录生成，脚本只控制固定的
  `askdata-nebula-poc` Compose project，默认退出时清理全部临时容器、网络和证书。

### 2.14 GraphPlan 合同与 Nebula Adapter

- `internal/askdata/graph/model.go` 冻结 `PlanRequest`、`GraphPlan`、对象版本引用、Join
  Step/Path 和风险代码。请求必须绑定规范 `PolicyScope`、一个已授权 domain、精确候选版本
  和 `maxJoinHops <= 4`、`maxPaths <= 32`；GraphPlan 的 request/plan/path hash 均规范且
  可复算，并可生成 `GRAPH_PATH` EvidenceRef。
- VID 严格采用技术设计中的
  `tenant_id:object_type:stable_object_id:version`，限制 256 bytes；release 不混入 VID，
  而是在每个 Tag/Edge 上保存并强制匹配 `release_hash`。同 version ID 冲突、不同引用映射
  到同一 VID、注入形态 ID 和超限候选均在访问图前失败关闭。
- `query_builder.go` 只生成四类固定模板：metric `MODELED_BY` model、model
  `HAS_DIMENSION` dimension、dimension `HAS_MEMBER` member，以及
  `JOINS_TO*1..N` 有界路径。所有点边都同时匹配 tenant/domain/release；路径端点必须是
  metric-bound model，中间点只能来自请求中已授权的 model，关系必须为 certified。
- `client.go` 对外只有 typed `Resolve`，不能提交任意查询或切换 Space。返回值只包含稳定
  版本 ID、模型绑定、兼容维度、成员归属/状态、Join 方向/类型/基数/fanout、Allowed 和
  确定性风险代码；Nebula 服务端错误被收敛为稳定类别，不回显生成的 nGQL。
- `poc_test.go` 在隔离的 NebulaGraph `v3.8.0` 中创建四类 Tag、四类 Edge 和版本化合成图，
  通过公开 Adapter 实测模型、维度、成员、Join 路径、风险和 EvidenceRef。实际联调还确认
  HTTP 健康可能早于 `SHOW HOSTS` 的 Version 元数据就绪，版本断言已改为有界重试；
  Nebula `ORDER BY` 只接受返回列名，路径长度和端点排序键因此先显式投影为内部列。

### 2.15 确定性问句规范化与 span 回映

- `internal/askdata/understanding/normalize.go` 新增版本化
  `question-normalization-v1` 合同。`NormalizedQuestion` 同时保留原问句与规范文本，并以
  零基 Unicode rune offset 在规范 span 和原文 span 之间双向映射；组合字符收缩和兼容/
  大小写展开采用覆盖完整原始 normalization segment 的保守映射。
- 文本投影固定为 NFKC → Unicode case fold → NFKC，统一全半角、兼容单位、中英文标点、
  Unicode 空白和安全数字单位间距。数值不会换算，“区/市”等行政后缀不会删除，`C++`、
  `A/B测试`、`华东-1区` 等实体核心字符保持可回到原文。
- 无害语气词处理只作用于句首/句尾明确的礼貌包装；核心文本中的相同词不会全局替换。
  完全被剥离的原文 span 返回 `ErrRemovedOffsetSpan`，无语义内容的问题失败关闭。
- 规范结果 `Validate` 会按原文重新计算，拒绝文本或映射篡改；非法 UTF-8、超过 4,096
  runes、非空白控制符、双向覆盖符和零宽不可见字符在进入后续词典/LLM 前被拒绝。
- `normalize_test.go` 覆盖全半角英文、中文/英文礼貌包装、标点、百分比/重量单位、emoji、
  组合音标、`ß -> ss`、实体后缀、幂等性、双向 offset 和 fuzz 往返不变量。

### 2.16 时间、比较与查询语法解析

- `internal/askdata/understanding/time.go` 以显式参考时间解析今天/昨日、自然周、本月/上月、
  今年/去年和财年；所有计算固定为 `Asia/Shanghai`，自然周从周一开始，结果统一保存为
  `start` 左闭、`endExclusive` 右开的 `YYYY-MM-DD` 日历范围。
- 显式日期支持带年份的日/月/年和同粒度范围。无年份日期、月日顺序不明的数字日期、裸
  “财年/自然周”、非法日期、倒序/跨粒度范围和多个时间表达均产生稳定 unresolved；财年
  起始月必须由调用方配置，缺失时不会按自然年猜测。
- `internal/askdata/understanding/grammar.go` 确定性识别同比、环比、较上期、Top/Bottom N、
  升降序及“按/每个”分组标记。规则事实不提前绑定指标/维度；多比较、多排名、Top 百分比
  和排序方向冲突以稳定 reason 交给后续认知阶段，不静默选边。
- `question-rules-v1` 结果绑定原问句 SHA-256、规范化版本、参考日、固定时区和可选财年配置，
  `Validate` 会重新执行解析以拒绝内容或配置篡改。时间、比较、排名、排序、分组和 unresolved
  的文本/span 全部通过 `NormalizedQuestion` 映回精确原文 Unicode rune 范围。
- `time_test.go`、`grammar_test.go` 覆盖上海跨日、周/月/年/财年边界、半开范围、全半角
  span、日期歧义/非法输入、语法冲突、排名上限、英文变体，以及“按揭/按钮”等误命中；
  fuzz 持续检查任意成功解析结果的全部 span 均与原文完全一致。

### 2.17 安全会话上下文合并

- `internal/askdata/understanding/context.go` 新增 `conversation-snapshot-v1`：只有合法且
  没有 unresolved 的最终 `QuestionUnderstanding` 能进入快照；构造时深拷贝调用方数据，
  并绑定 conversation、turn、完整 `PolicyScope`、release 与可复算 snapshot hash。
- `conversation-merge-v1` 将上一轮事实按 domain、metric、grouping、filter、time、
  comparison、ordering、limit 分槽处理。`CURRENT_TURN_WINS` 是固定优先级；“那按地区呢”
  只替换原分组，“换成去年”只替换原时间，上一轮事实继续保留其源问句和源 span，不会被
  搬到本轮文本上制造虚假位置。
- AUTO 模式只对明确的续问前缀、规则片段和清除指令继承；可信编排层也可显式指定
  FOLLOW_UP/INDEPENDENT。完整新问题、清空上下文和独立模式不会读取旧快照；取消分组、
  筛选、时间、比较、排序或排名等显式清除优先于本轮规则命中。
- 缺少上一轮的追问返回 `CONTEXT_PREVIOUS_TURN_REQUIRED` 和精确本轮原文 span。旧快照的
  tenant、actor、domain、role、policy hash、release 或 conversation 任一不一致时，结果
  为不含旧内容/旧 hash 的安全 reset；独立问题即使收到损坏旧快照也不会读取它。
- 合并结果绑定 current question hash、policy/release、precedence、previous snapshot hash
  和自身 content hash；`Validate` 确定性重放全部决策并拒绝篡改，可生成绑定当前 turn 的
  `CONVERSATION` EvidenceRef 供后续受控认知阶段引用。
- `context_test.go` 覆盖示例追问、槽位继承/覆盖/清除、歧义时间阻止旧时间继承、完整新问、
  缺失上下文、跨 actor/role/release/conversation、快照深拷贝、hash/replay 防篡改和 fuzz
  span 不变量。

### 2.18 本地启动阻塞修复

- `internal/dataset/mapped_dataset.go` 的系统映射刷新幂等键已加入数据集版本和草稿
  record version。同一事务重试仍得到稳定 key，但语义结构回退到历史 hash 时不会再与
  旧发布记录撞键。
- `internal/dataset/mapped_dataset_test.go` 已覆盖同事务稳定、历史结构再次发布必须隔离，
  以及数据库 128 字符 key 上限。

### 2.19 LLM 完整理解与取证计划

- `internal/askdata/understanding/service.go` 新增 `question-understanding-review-v1`：LLM
  返回当前轮 `QuestionUnderstanding`、冲突、分类型 Evidence Request 和引用证据；该合同
  独立于已冻结的 8 类 Cognition Action，不增加动作类型，也没有 SQL、nGQL、表、列、
  对象版本或任意工具参数字段。
- `BuildUnderstandingReviewInput` 只向 UNDERSTANDING 阶段暴露 AI-003 允许的 4 类事实：
  `CONVERSATION`、`EXACT_MATCHES`、`RULE_PARSE`、`POLICY_EVIDENCE`。Exact fact 同时携带
  被词典/规则/上下文触发器消费后剩余的原文 residual rune spans；事实、匹配和证据集合
  都按规范顺序构造，输入顺序不能改变 Prompt Fact。
- Reviewer 只能引用本轮输入的 conversation/rule/policy/exact EvidenceRef，顶层决策必须
  同时引用 conversation、rule、policy。授权域 hypothesis 必须属于固定 PolicyScope；
  发明证据、旧 hash、输入外域、嵌套引用未列入顶层证据均失败关闭。
- 规则解析仍是权威：确定性时间、比较、TopN/limit、排名方向、显式排序不得被 LLM
  改写或省略；“按/每个”必须得到相邻 `GROUP_BY` role，不能判断时必须保留 unresolved
  并请求维度证据。规则/上下文 unresolved 和稳定冲突码也不得被模型吞掉。
- 当前轮与继承理解继续分离。Evidence Request 使用 `CURRENT/INHERITED` 标记并在对应源
  问句上验证精确 rune span；当前或继承的 metric/dimension/value mention 分别必须覆盖
  `METRIC_CANDIDATES`、`DIMENSION_CANDIDATES`、`MEMBER_CANDIDATES`，每个 unresolved
  声明的 needed evidence 也必须有同 span 请求。
- `question-understanding-result-v1` 保存当前理解、原 ContextMergeResult、冲突、取证计划、
  proposal hash 和 result hash；可使用原请求重建事实、重放全部权威校验并拒绝篡改。
  模型生成的摘要、target/dimension hint 或 request reason 若含物理查询形态同样被拒绝。
- `service_test.go` 使用纯合成难例覆盖完整“时间 + 成员 + 指标 + 分组 + 同比 + TopN +
  排序”问句、未知 residual、续问继承、同比/环比冲突、事实规范顺序、结果防篡改，以及
  规则覆盖、越权域、伪造证据、缺失取证计划和物理查询注入负向路径。
- 本任务未调用外部模型、未读取 API key、未新增依赖/迁移/Compose/页面。`HUMAN-002`
  仍未提供，因此正式业务指标词典、Prompt 调优和生产准确率评测尚不能进行或宣称完成。

### 2.20 结果规范化与等价判定

- `internal/askdata/evaluation/equivalence.go` 新增 `result-equivalence-v1`。比较不读取或
  比较 SQL，而是同时校验规范 Semantic IR 和类型化逻辑结果；输入受 256 列、10,000 行、
  100 万 cell、单 cell 32,768 rune 和 64 MiB 规范 payload 上限约束。
- 调用方必须给出可信 `ResultSchema`：稳定列名、STRING/INTEGER/DECIMAL/FLOAT/BOOLEAN/
  DATE/DATETIME 类型、Key 标记和 DATETIME IANA 时区。输入列按名称规范重排，行按 Key
  后全行排序；FLOAT 禁止作为 Key，任一重复 Key 以 `ErrDuplicateResultKey` 失败关闭。
- DECIMAL 只接受精确十进制文本、`json.Number`、整数或数据库 `driver.Valuer`，使用
  `math/big.Rat` 规范为精确约分值；binary float 不能伪装成 Decimal。NULL 通过独立标记
  与空字符串/`"NULL"` 区分，INTEGER/BOOLEAN/STRING 均规范化并限制非法或超长值。
- DATE 保留 `YYYY-MM-DD` 日历语义；带 offset 的 DATETIME 和 `time.Time` 统一为 UTC，
  zone-less SQL datetime 只能按 Schema 中显式 IANA 时区解释，不读取机器本地时区。
  FLOAT 保留精确 float64 规范值，比较时使用调用方显式、有限的绝对/相对容差。
- `NormalizedResult` 的 SHA-256 可复算并拒绝内容/排序篡改；预期或实际若携带 declared
  result hash，必须分别匹配自己的规范结果。容差内浮点可判 `ResultEquivalent=true`，
  但 `ExactResultHashMatch` 仍为 false，避免容差隐藏工件差异。
- IR 复用 `ir.Canonicalize`，严格比较版本、release/hash、model、metrics、groupBy、filters、
  timeRange、comparison、sort、limit；最终 `Equivalent` 只有 IR 和结果均等价才成立。
  比较报告只包含 IR/result hash、行数和最多 64 个稳定 code/path，不保存结果行或参数。
- `equivalence_test.go` 直接复用 synthetic fixture，覆盖列/行乱序、精确 Decimal 大数和
  科学计数、浮点绝对/相对容差、NULL、日期、上海时区、全部基础标量、重复 Key、IR 差异、
  declared hash、结果 hash/规范顺序篡改、NaN/Inf、缺失时区和 FLOAT Key 等负向路径。
- 本任务未新增第三方依赖、数据库结构、Compose、页面或外部模型调用，也未读取 API key。

### 2.21 Mention/Binding 指标与校准输入

- `internal/askdata/evaluation/binding.go` 新增 `mention-binding-evaluation-v1`。评测 case
  直接复用 `QuestionUnderstanding`，要求 gold/prediction 指向完全相同的原问句，并以
  metric/dimension/value(member) 类型 + 原文 Unicode rune span 作为 mention 身份；重复
  span、越界 mention index 和不同问句全部失败关闭。
- Binding 只接受稳定对象版本 ID。Metric 比较 metric version；Dimension 同时比较
  dimension version 和 `GROUP_BY/FILTER/TIME/SORT` 角色；Member 同时比较 member version
  和所属 dimension version。错误对象、角色、父维度或 span 会各形成一个 FP 和一个 FN，
  不会被局部命中掩盖。
- `PRFScore` 保留 TP/FP/FN、gold/predicted 数量及派生 precision、recall、F1；零分母固定
  输出 0 而不是 NaN。报告按整体、domain、`SIMPLE/COMPOSITE/CONTEXTUAL/RELATIONAL`
  复杂度和六类歧义确定性 micro 聚合，分层总数必须分别与 overall 一致。
- 报告与所有分层/校准样本规范排序并绑定可复算 SHA-256；`ValidateAgainst` 可根据原 cases
  完整重放，识别即使重新计算 hash 的错误标签。持久报告不包含原问句，只保存稳定 case、
  对象版本、mention span 和分层元数据。
- TRAIN/VALIDATION 的每个已选择 binding 会生成校准样本：系统候选分、candidate margin、
  exact/lexical/vector/graph/rule 归一化特征、retrieval rank 和由 gold 派生的正确标签。
  合同不存在 LLM 自报 confidence；SEALED 和 PRODUCTION_REGRESSION case 只参与指标，
  不进入训练/验证输出，防止评测集泄漏。
- `binding_test.go` 复用 synthetic fixture 的同名指标/同名成员资产，覆盖正确直答、错绑父
  维度、错误 mention 边界、漏召回、维度角色错误、输入顺序稳定、分层守恒、校准正负样本、
  sealed 隔离、报告/hash/重放篡改、非法特征/绑定及零分母。
- 本任务未新增依赖、数据库结构、Compose、页面或外部模型调用，也未读取 API key。

### 2.22 问数运行、事件、工件与 Tool 审计控制面

- `000217_askdata_question_runtime_audit` 新增 `question_runs`、`question_run_events`、
  `question_artifacts`、`tool_calls` 四张表及对称 down migration。Run 只保存原问句 hash，
  从创建时固定 tenant/domain/actor、policy scope、ACTIVE release ID/content hash、trace 和
  幂等 key hash；新 run 不能绑定 READY/DRAFT、错误 content hash 或其他 domain release。
- 状态词汇与技术设计一致：RECEIVED → AUTHORIZED → CONTEXT_READY → UNDERSTANDING →
  RETRIEVING → BINDING → GRAPH_VALIDATING → IR_READY → PLAN_VALIDATING → EXECUTING →
  RESULT_VERIFYING，且仅允许设计中的纠错/澄清/阻断边。每次更新必须将 `record_version`
  精确加一；身份、release/policy pin 和预算上限不可变，消耗计数/耗时只能单调增加。
- 预算以显式列限制最多 4 次 LLM、8 次 Tool、2 次正式查询、3 次验证查询和 25 秒；实际
  elapsed 可记录超时清理成本，但动作计数不能突破上限。PLAN/RESULT 回到 BINDING 时必须
  清空旧 binding/graph/IR/plan/result hash，避免纠错后复用陈旧下游工件。
- CLARIFICATION_REQUIRED、ANSWERED、BLOCKED 都是不可回退终态，分别要求 CLARIFICATION、
  ANSWER、BLOCK completion artifact；ANSWERED 还必须具备 understanding/binding/graph/
  IR/plan/result 全链 hash。Run、artifact 和终态通过复合外键绑定，不能引用其他运行的 hash。
- Event、Artifact、Tool outcome 都是 append-only。每条事实必须回绑当前 run version；event
  与 Tool state 必须等于当前 run state。Event 引用 `platform.ai_requests` 时还必须同 actor/
  tenant 且 purpose 为 SEMANTIC_QUESTION。Tool 使用当前 14 个关闭名称词汇表，
  `(tenant,run,tool_call_id)` 唯一，request/result/call hash 支持幂等重放核验。
- ACTIVE release 校验通过不向 app 暴露的 `SECURITY DEFINER` helper 在 trigger 内持有
  transaction-scoped `FOR SHARE`，避免校验与 supersede 间的竞态，也不授予 app
  `releases.UPDATE` 或任意持锁入口。Event/Artifact/Tool stamp 会对 run 行取 `FOR SHARE`，
  与 Store 状态更新串行；EventType 形状必须匹配对应 AI request、Tool outcome 或 Artifact。
- understanding/binding/graph/IR/plan/result hash 只能在各自治理阶段首次出现并形成连续链，
  非纠错路径不得清空或覆盖；因此不能在 RECEIVED 提前注入 result，也不能在 ANSWERED 前
  一次补齐伪造链。Go 与 SQL 的 14×14 状态矩阵由真实数据库集成测试逐项对拍。
- 新的递归 `question_audit_json_is_safe` 在现有 SQL/credential/row guard 上继续拒绝 raw
  question、prompt/messages、reasoning/chain-of-thought、arguments/parameters、SQL/query、
  raw response/answer/result data 等键；JSON 有 16～256 KiB 分类型上限，审计表没有 SQL、
  参数明文、结果行或原问句列。
- 四表全部 FORCE RLS，并额外限制普通 USER 只能访问自己的 run；同域其他 actor 不可读。
  `report_app` 只有 run INSERT/UPDATE 和三类事实 INSERT，worker 只有 SELECT，PUBLIC 和
  connection-test role 无 schema 权限；内部 release lock helper 对 app/worker/connection
  tester 均无 EXECUTE；append-only trigger 对管理员直接篡改同样生效。
- `scripts/verify-database.sh` 已加入关系/RLS/tenant FK、触发器、completion FK、Tool 唯一键、
  JSON guard 和权限静态断言，并在回滚事务中以真实 app 角色验证合法/非法状态迁移、错误
  release pin、current version/state、Tool 精确重试、终态完成形状、跨 actor RLS 和审计冻结。
- 本任务没有页面、Go 业务代码、Warehouse schema、外部模型或 Compose 改动，未读取 API key；
  ACTIVE 激活函数仍故意不存在。

### 2.23 Question 状态机与 PostgreSQL Store

- `internal/askdata/orchestrator/state.go` 是 `DB-005` 生命周期的 Go 可执行镜像：覆盖全部
  合法边、非终态同态 checkpoint、三个冻结终态、绝对且单调的预算消耗、completion shape
  和 PLAN/RESULT → BINDING 有界纠错。`Apply` 不修改输入，expected version 不匹配、非法
  跳转、空 checkpoint、预算回退/超限、hash 提前出现/断链/覆盖都返回稳定 sentinel。
- `store.go` 在 actor/domain 完全匹配的事务上下文内创建、推进和恢复运行；不存在 SYSTEM
  回退。创建先识别精确幂等重放与同 key 碰撞，再由数据库 ACTIVE trigger 原子固定 release；
  并发唯一键或 release 切换失败会在新事务中复查精确历史运行，避免把成功创建误报失败。
- Transition 先锁 run、完整验证已有 event/artifact/tool ledger，再按同一事务写 completion
  artifact、乐观更新 run 和追加 transition event。Event index 独立于 record version；事件以
  previous hash 形成规范链，Artifact/Tool 也按规范内容重算 hash，任何 pin/version/state/
  index/hash 或 EventType 引用篡改都会使 Resume 失败关闭。
- Resume 使用 Repeatable Read + Read Only 快照，终态必须由最后 transition event 与前一
  run version 的正确 completion artifact 同时证明；终态后事件、工件或 Tool outcome 均不
  能继续追加。`SeenActionHashes`/`SeenToolCallIDs` 为 ORCH-003/004 的 no-progress 与精确重放
  提供去重依据，但本任务没有提前实现 Agent Loop 或通用工具执行接口。
- `state_test.go`、`store_test.go` 和 `store_integration_test.go` 覆盖 14×14 Go/SQL 对拍、
  BLOCK/CLARIFY/ANSWER 完整持久链、两条纠错路径、乐观锁、错误 hash/引用、同 actor 幂等、
  跨 actor RLS、superseded release 的 resume/精确 replay 与新运行拒绝。真实 app/admin
  fixture 全部嵌在外层回滚事务；默认 Store 的 Resume 另实测事务隔离级别和只读属性。
- `.github/workflows/ci.yml` 已加入全仓 `go test`，基础设施迁移/数据库验收后再以真实 app/
  admin URL 执行 Orchestrator PostgreSQL 集成，避免数据库路径只在人工本地运行。
- 本任务未涉及页面、Warehouse schema、外部模型或 API key；没有实现 ORCH-003/004，也未
  开放 `activate_release`。

### 2.24 评测集、独立评审、评测运行与结构化反馈

- `000218_askdata_evaluation_feedback` 新增 `evaluation_sets`、`evaluation_cases`、
  `evaluation_case_reviews`、`evaluation_runs`、`query_feedback` 五表和对称 down migration，
  并为 release 增加 `(id,semantic_version,content_hash,domain_id,tenant_id)` 精确评测 pin。
- Set 固定 TRAIN/VALIDATION/SEALED/PRODUCTION_REGRESSION split 与 fixture/E2E mode；密封与
  生产回归必须固定 release。Seal 在数据库内按 set→case→review 锁序重算非空 case、每 case
  两条当前 APPROVED review、case/review count 与 C-collation manifest hash，SEALED/RETIRED
  后内容冻结；没有加入 release gate、审批或激活入口。
- Case 保存 question hash、经审批脱敏问句、redaction policy hash、answerability、预期
  disposition/security、复杂度/歧义、expected path/IR/result hash 与受限 opaque fixture ref，
  不保存 SQL、参数或结果行。数据库维护 `content_updated_by`；原作者和当前 content hash 的
  编辑者都不能评审，两个唯一 slot/不同 reviewer 与 review hash 共同证明独立性；内容变更会
  立即使旧 review 不再计数。
- Evaluation run 只能由 worker 追加，固定 set/case content hash、release ID/version/hash、
  expected/actual path/IR/result、warehouse snapshot/freshness、failure stage/code、显式 security
  与 leak 事实。裸 `*_equivalent=true` 只有同 hash 或存在 comparison report hash 才成立；
  leak 没有默认值，PASSED 禁止 leak，RETIRED 或未密封生产回归集拒绝新 run，既有 run 不可
  UPDATE/DELETE。
- Feedback 通过复合外键精确绑定终态 question run、原 actor、release/policy/domain/tenant；
  `ACCURATE` 只能配 `NONE`，`INACCURATE` 必须选择 metric/dimension/member/time/relationship/
  data/permission/expression/other，并使用 record version 与数据库重算 hash；不可删除，也不
  修改答案或权威语义。
- 五表全部 FORCE RLS。普通 USER 即使是领域管理员也只能管理 DRAFT 内容，Seal 后看不到问句/
  review；SYSTEM worker 只可读取密封内容并 INSERT evaluation run。`seal_evaluation_set` 另在
  SECURITY DEFINER 入口显式复核领域管理权限；PUBLIC/connection tester 无 schema 权限，
  manifest/lifecycle helper 不直接授权运行角色。
- `scripts/migrate.sh` 已同步最小权限；`scripts/verify-database.sh` 同时静态检查关系、RLS、
  复合 FK、trigger/function/search_path/constraint/grant，并以真实 app/worker/admin 回滚 fixture
  验证普通成员封存拒绝、当前内容作者自审拒绝、stale review、双审 Seal、密封隐藏/冻结、
  DRAFT production/错误 release/path/leak/遗漏 leak/RETIRED run 拒绝、run append-only，以及
  非终态/cross-actor/shape/乐观锁/删除 feedback 拒绝。
- 本任务没有页面、Go 业务代码、Warehouse schema、外部模型或 API key；页面确认门禁未触发。

### 2.25 敏感维度成员政策与数据库内 EXACT_ONLY

- `000223_askdata_sensitive_member_policy` 为 alias 增加数据库维护的维度绑定查找 hash，并在
  dimension/member 两侧强制 sensitivity floor：成员可以更严格，不能比所属维度宽松；维度
  也不能通过 DRAFT 更新越过已有成员。安装 trigger 前先锁表并审计历史数据，down migration
  对称移除本轮函数、trigger、index 和列。
- MEMBER release contract 只能有 `schemaVersion/type/dimensionVersionId/aliasVersionIds` 四个
  key；alias version ID 必须小写 UUID、排序、唯一、最多 64 个，并精确属于同一已认证成员。
  release object sensitivity 必须等于源 member；运行角色可读的 manifest 因此不含 label、
  member/alias lookup hash 或 alias content hash。DOMAIN manifest 另补 object/domain identity
  guard，避免既有 validator 改为 SECURITY DEFINER 后出现跨域入口。
- `askdata.lookup_exact_dimension_member(release_id,release_hash,dimension_version_id,lookup_hash)`
  是唯一 EXACT_ONLY 运行入口。它固定 USER access context、活动成员关系、READY PostgreSQL
  投影、DIMENSION/MEMBER manifest、CERTIFIED/有效期、候选唯一性和 release 状态；READY、
  ACTIVE 与已固定旧运行的 SUPERSEDED 均可重放。CONFIDENTIAL 与 RESTRICTED 分别检查
  `LOOKUP_CONFIDENTIAL_MEMBER`/`LOOKUP_RESTRICTED_MEMBER` 的 USER 或 ACTIVE ROLE 对象授权；
  missing、denied、ambiguous、expired、wrong hash 和 unpinned release 统一返回零行。
- `scripts/migrate.sh` 每次部署先清除历史表/列 ACL。app 仅可读取 member/alias 的非文本管理
  元数据并保留受治理 DML；worker 仅可读取画像差异所需的 `profile_id/member_key_hash`；raw
  canonical label、alias、normalized value、lookup hash 对 app/worker/connection tester 均不可读，
  lookup 函数只授权 app。
- `internal/askdata/security/member.go` 统一 FULL/EXACT_ONLY/ON_DEMAND/NONE 暴露矩阵。原始成员
  值只存在于构造 lookup 的问句 span，SQL 参数只有维度绑定 hash；lookup/match 的 fmt/JSON、
  error 和 evidence 均不输出 label、原问句或 lookup hash。匹配结果只携带成员/维度版本 ID、
  content hash 和 label-free EvidenceRef，并封装为私有 payload/proof；外部调用方只有只读
  accessor，复制合法结果后替换 ID、content hash 或 EvidenceRef 会失败关闭。
- `internal/askdata/understanding/service.go` 显式接收不可伪造的 sensitive match。当前与继承问句
  的敏感 span 会从 residual/rule/context/exact 等所有嵌套 Prompt fact 字符串递归移除，并以
  等 Unicode rune 长度遮罩 NFKC、case-fold、全角及 ASCII 邻接变体，保持后续 span 不漂移。
  Reviewer 必须基于遮罩问句返回；mention、summary、reason、conflict 或 evidence request 只要
  回显原值/规范变体或覆盖敏感 span，即失败关闭，验证完成后才在模型边界外恢复原问句。公开
  label grant/constructor 已删除，普通 `ExactMatch` 一律拒绝 MEMBER，不能由调用方自报 FULL
  policy 绕过数据库 label-free 路径。
- 画像 observation 的 `eligible_for_llm` 每次从 sensitivity、member index policy 与实际高基数
  状态重算，仅 `PUBLIC/INTERNAL + FULL + low-cardinality` 可为 true；伪造 persisted bool 会被
  拒绝。本地确定性 alias 检查仍可使用这些 observation，但 `DIMENSION_PROFILE` PromptFact 是
  独立聚合载荷，不含 label、alias、normalized value、member hash 或派生 ID；成员型 LLM reviewer
  在实现按 `profile_generation` 从 PostgreSQL 权威重载前始终不调用。外部包用自定义
  `ProfileStore + WarehouseScanner` 注入 FULL/INTERNAL 任意标签的攻击回归已覆盖该边界。
- MEMBER search document 统一复用安全暴露矩阵；敏感/受限或非 FULL 成员不能进入 lexical/
  embedding 文档。Reranker 在 request 规范化与 evidence 校验两层均拒绝显式 MEMBER，调用方
  即使伪造 INTERNAL/FULL/ALLOW 形状也不会触发 reviewer。当前 Go `internal/` request 只允许
  SQL/RLS/release 过滤后的可信 typed producer 构造，不能暴露为网络/插件合同；未来若改变该
  信任边界，必须先增加 authoritative candidate/coverage provenance。CI 已加入 security
  PostgreSQL integration；本任务没有页面、外部模型调用、Warehouse schema 或 ACTIVE 激活入口，
  页面确认门禁未触发。

### 2.26 WEB-001 问数工作台设计确认与落地（已完成）

- 已用应用内浏览器检查真实登录页、平台管理页和数据集配置页，并以现有 Haier 品牌、248px
  固定侧栏、`#2864DC` 主蓝、`#F7F8FA` 画布、白色细边框卡片、9～11px 圆角和现有字体/图标
  体系作为强制视觉约束。参考截图：
  `/Users/susanmartinez/.codex/visualizations/2026/08/05/019fd157-a519-7c12-afb3-234454839fae/existing-dataset-reference.png`。
- 方案 1「对话中枢」：
  `/Users/susanmartinez/.codex/generated_images/019fd157-a519-7c12-afb3-234454839fae/exec-4564e2b6-050a-4a0f-a01e-2d2e92ce803e.png`。
- 方案 2「分析画布」：
  `/Users/susanmartinez/.codex/generated_images/019fd157-a519-7c12-afb3-234454839fae/exec-1f52ab9e-76c7-4a08-ba9d-965404d7cc81.png`。
- 方案 3「证据驾驶舱」：
  `/Users/susanmartinez/.codex/generated_images/019fd157-a519-7c12-afb3-234454839fae/exec-66f629ed-773e-48f9-ac86-25084f7a53ec.png`。
- 确认状态：用户于 2026-08-06 选择 **方案 3「证据驾驶舱」**。
- 已新增 `/ask-data` 受保护路由和“智能问数”导航，严格使用
  `RequireAuth + RequireBusinessDomain`；实现文件为 `web/src/pages/AskDataPage.tsx`、
  `web/src/styles/ask-data.css`，并在 `AppShell.tsx` 增加兼容旧页面的 `titleMeta`。
- 页面使用 typed mock 覆盖会话筛选、常用/智能建议、开始新问题、idle/loading/complete、
  三阶段受控状态、ECharts 双向渠道贡献图、结果展开、证据折叠和反馈；未接入真实 API/SSE，
  未持久化 mock，也不展示 prompt、SQL 参数或思维链。
- `web/package.json`/lock 新增 ECharts，采用按组件注册方式控制增量；现有 Haier Logo 和
  Phosphor 图标直接复用，没有新增伪造图片、手工 SVG 或 CSS 图形资产。
- 设计 QA：`design-qa.md`；最终实现截图：
  `design-qa-artifacts/ask-data-implementation-final.png`；参考/实现同画面对照：
  `design-qa-artifacts/comparison-final.png`；最终结果为 `passed`，没有遗留 P0/P1/P2。
- 已执行 `docker compose up -d --build web`，默认 Compose 预览
  `http://127.0.0.1:5173/ask-data` 已加载本次源码；API、Web、Connector、控制库、数仓和
  MinIO 均恢复 healthy。

2026-08-06 `WEB-001` 验证：

```sh
cd web
npm run lint
npm run build
git diff --check -- package.json package-lock.json src/app/App.tsx \
  src/components/AppShell.tsx src/main.tsx src/pages/AskDataPage.tsx src/styles/ask-data.css
cd ..
docker compose up -d --build web
docker compose ps
```

结果：全部通过。Vite 仅保留“大于 500 kB chunk”性能提示，不影响构建退出码；ECharts
已由全量 import 改为模块化注册。应用内浏览器使用真实本地种子管理员登录和 ACTIVE“企业经营”
领域验证默认 Compose `/ask-data`，路由门禁、导航 active、提问/加载/完成、会话筛选、结果展开、
证据折叠和反馈均通过；重建后核心提问流程再次通过。

### 2.27 GRAPH-002 正式开发 Compose、初始化与最小权限（已完成）

- 用户于 2026-08-06 确认 `HUMAN-005` 的开发部分：NebulaGraph Server/官方 Go Client
  锁定 `v3.8.0`，采用单副本、持久卷、每环境共享 Space、tenant/release typed 隔离、
  API `GUEST`、Worker `USER` 和环境变量开发凭据。生产容量、TLS、Secrets、备份和多副本
  仍属 `HUMAN-005/OPS-004`，不得从本机结果推断。
- 根 `compose.yaml` 新增 metad/storaged/graphd、一次性 init、verification job 与
  `nebula-loopback-proxy`；Meta/Storage 只加入 internal cluster network，graphd 再加入
  internal client network，API/Worker 只能访问客户端面。graphd 不发布宿主端口；无凭据 proxy
  仅在 init 成功后显式启动并绑定 `127.0.0.1:${ASKDATA_NEBULA_PORT}`。Connection Test Worker、
  Connector、Web 不持有图凭据且不加入图网络。
- metad/storaged 数据与日志使用命名卷。init 在首次登录后立即把厂商 root 密码轮换为配置值，
  等待新密码生效且旧密码失效后才执行 `ADD HOSTS`；随后精确核验目标 storaged 的
  `host/port/ONLINE/v3.8.0`、单 partition/replica、`FIXED_STRING(256)` Space、当前 Adapter
  冻结的 `semantic_model/metric/dimension/member` 和 `MODELED_BY/HAS_DIMENSION/HAS_MEMBER/
  JOINS_TO` 字段、nullable/default/comment/TTL 合同，以及 API/Worker 的唯一 Space 角色。
- production 使用 `local_*`、未知 `APP_ENV`、错误/默认密码、Space 参数漂移、Schema 漂移、
  API/Worker 在其他 Space 的陈旧授权均失败关闭。API 的 GUEST 实测只读，Worker 的 USER
  实测可写/删投影但不能 DDL/授权；两者均不能访问未授权 Space。共享 Space 不提供数据库
  RLS，租户边界仍由 typed Graph Service 强制 tenant/domain/release 条件。
- `scripts/run-with-nebula-role.sh` 为 `make run-api/run-worker` 只映射本角色到 generic runtime
  凭据，再删除 root/bootstrap/canonical secret；Connection Test Worker 和 seed 删除全部图
  配置。Compose 也显式清空共享 `env_file` 带入的越界凭据。`check-compose.sh` 同时验证服务级
  端口/网络和本地进程凭据矩阵。
- `scripts/verify-nebula-compose.sh` 不读取用户 `.env`：
  `deployments/nebula/verification.override.yaml` 会重置 API、Worker、Connection Test Worker 的
  service `env_file`，配置检查同时禁用 env file 解析。每次由同一 nonce 创建唯一
  `askdata-graph002-verify-*` project 与 `g002_verify_*` Space，以及独立账号、随机 loopback
  端口和独立卷。Go integration 在图写入前反查该 project 的 proxy published endpoint 与
  Compose project/service 标签；随后对 4 类 Tag、4 类 Edge 分别执行 tenant/release 排除，
  只有 Resolve 成功、目标关系消失且无关对照关系保留才通过。测试还覆盖真实 GraphPlan、
  参数/并发、错误密码、未授权 Space、GUEST/USER 正负权限、强制重建持久化及 partition/Schema
  drift 失败关闭。退出 trap 仅 `down -v` 该精确格式项目，已实测无容器、网络或卷残留；
  CI 新增独立 `graph-compose` job。
- `dev-services.sh start` 已按 metad/storaged/graphd → init → storaged ready → proxy → migration →
  apps 顺序启动，并在应用健康后删除带 bootstrap 环境的已退出 init 容器。当前 canonical 栈已
  切换到新网络；早期实现遗留且容器数为 0 的旧 graph network 已删除，没有删除图数据卷。
- 本任务没有新增页面、流程或显著视觉状态，页面确认门禁未触发。

### 2.28 GRAPH-004 Release Projector Worker（已完成）

- 新增迁移 `000224_askdata_graph_projection_worker_contract`。`report_worker` 只能调用
  target-scoped tenant listing/claim、lease heartbeat 和 `NEBULA_GRAPH` 快照函数；
  `report_app`、Connection Test Worker 与 PUBLIC 均无执行权。快照必须匹配 tenant、release、
  projection、worker、未过期 lease token 与 content hash，只返回发布内稳定对象/版本 ID、
  version number、成员 ACTIVE/EXPIRED 状态和认证 `MODEL_JOIN` 事实，不返回 label、alias、
  lookup hash、AST、contract JSON 或物理标识。
- `internal/askdata/graph/projector.go` 冻结 `askdata-nebula-projection-v1` 快照与证明合同，规范排序
  后按类型、每批最多 128 个点/边执行固定参数化 mutation；每批前后 heartbeat。`JOINS_TO`
  rank 由不可变 relationship version ID 稳定派生，允许同一模型端点存在多个认证关系版本。
  mutation 成功后必须与本地规范快照的 graph hash、vertex/edge/object count 精确一致才可完成；
  合同/证明错误不可重试，传输故障按 release projection lease 退避，失败不会推进 READY。
- `internal/askdata/graph/postgres_store.go` 在 tenant SYSTEM transaction 内再次核对 release
  manifest hash/object count、projection lease 和每类预期图事实计数。非 `MODEL_JOIN` 的
  relationship 可以合法存在于 release，但不会误计为 `JOINS_TO`；缺少发布端点、未认证对象、
  hash 漂移或实际计数不一致均失败关闭。
- `cmd/worker/main.go` 已接入独立图投影循环，使用 Compose 已限定的 Worker `USER` 账号和
  Space-bound SessionPool；缺少/非法 endpoint、Space、账号或 TLS 布尔配置时进程启动失败。
  新后端镜像已显式构建，worker 已强制替换并保持 healthy。
- 正式隔离 Compose 验收不再只验证持久化重启：首轮 Projector 通过后删除整个测试 Space，
  等待 Meta 收敛，用 init 重建空 schema/账号/角色，再由同一 Projector 重建全部点边并复验
  GraphPlan、tenant/domain/release、GUEST/USER 权限。该过程同时修复了 Nebula 服务重建后
  临时 Space 创建和同名 Space 角色元数据的异步收敛等待。
- 数据库验证新增真实调用：target 隔离 listing/claim、错误/正确 lease heartbeat、陈旧 token
  快照拒绝、包含过期成员的 label-free 快照以及 completion；因此发现并修复了
  `load_release_graph_projection` 输出列与 CTE 未限定列的 PL/pgSQL 歧义。
- 本任务没有新增页面、流程或显著视觉状态，页面确认门禁未触发。

### 2.29 GRAPH-005 Resolver、认证缓存与 PostgreSQL 降级（已完成）

- `internal/askdata/graph/resolver.go` 冻结三段式解析顺序：正常 Nebula、认证 GraphPlan cache、
  PostgreSQL registry fallback。每一段输出都重新执行 `GraphPlan.Validate`，并精确回绑规范化
  request hash、tenant、domain、actor、policy scope 和 release ID/content hash；Nebula 返回零
  metric-model binding 视为结果不足。取消/超时直接返回，不会借降级链绕过调用方预算。
- `PostgresCertifiedPlanCache` 只读取同 question-shape、policy、release 与 graph hash 的未过期
  cache，并要求 release 仍为 READY/ACTIVE/SUPERSEDED、对应 `NEBULA_GRAPH` projection 仍 READY
  且 applied hash 一致；严格 JSON、plan hash 或请求证明任一不一致即拒绝。缓存写权限继续只属于
  Worker，本任务未开放 API 任意写入。
- `PostgresFallback` 必须持有与请求 actor/domain 一致的真实 AccessContext，并在 tenant RLS
  transaction 中验证 release manifest hash/object count，以及 POSTGRES_REGISTRY、NEBULA_GRAPH
  双 READY 投影。它只读取当前 release 内认证指标、模型、维度、成员归属和 `MODEL_JOIN` 的
  label-free 稳定事实，不读取 member key/label、alias、AST、物理表列或 contract JSON。
- 关系遍历由 Go 实现确定性、有界的简单路径搜索：最多 32 条关系、4 跳、32 条路径、4096 次
  展开，只能使用请求内模型作为中间点；方向按冻结 VID 顺序与 Nebula Adapter 保持一致，并由
  `NewJoinPath`/`NewGraphPlan` 重算基数、fanout 风险和稳定 hash。超限、缺失证明或无可验证结果
  均失败关闭，绝不落到未验证 Top 1。
- 单元测试覆盖 Nebula 正常、真实 Client 传输故障、结果不足、精确缓存命中、缓存篡改、scope
  重绑、PostgreSQL 回退、全链失败与 context cancellation；真实 PostgreSQL/RLS 回滚夹具以
  `report_app` 读取完整发布，证明关系库输出与标准 `GraphPlan` 完全一致且不泄漏敏感成员材料，
  再将图投影置为 FAILED，验证缓存和关系回退同时拒绝。
- 本任务没有新增页面、流程或显著视觉状态，页面确认门禁未触发。

### 2.30 NLU-005 Joint Binder 与 Bundle Beam Search（已完成）

- 新增 `internal/askdata/binding/{binder,beam,score}.go`。绑定请求同时携带 NLU-004 的原始请求/
  结果和 GRAPH-005 的 PlanRequest/Resolution；执行前完整重放两条 hash 链，并要求 policy scope、
  domain 与 release 精确一致。当前和继承 mention 以 `CURRENT`/`INHERITED + kind + index` 分开，
  不会把上一轮 span 伪造成本轮证据。
- 每个 mention 的候选必须属于同一 release-pinned GraphRequest，并携带候选集、检索、规则、
  Semantic Contract 与 Data Quality 证据。规则 `BLOCK` 是硬门禁，优先于 reviewer rank；成员
  只能走确定性精确候选，不能进入 LLM label-bearing rerank，并且必须由 GraphPlan 证明 ACTIVE
  状态、父维度归属和与已选模型兼容。
- Beam 按指标 → 维度 → 成员顺序联合展开：指标同时选择 MetricVersion/Model；维度按 GROUP_BY/
  FILTER/TIME/SORT 角色保留；成员自动补入已证明的隐含 FILTER 维度；多模型组合必须被一条
  allowed 认证简单路径覆盖，fanout block 路径不会生成 bundle。默认 beam width 64、Top 10，
  硬上限分别为 256/30。
- 排序分数只使用确定性系统特征：检索、exact、reviewer rank、rule、quality、graph、cost 与
  Join risk penalty；没有 LLM 自报 confidence。每个 Top Bundle 保存完整稳定 ID 绑定、时间、
  GraphPath/降级来源、分项分数、规范 evidence 与 bundle hash；零候选或无可行组合生成可审计
  `NoMatch`。严格 JSON 解码后还会对原始请求重算整个结果，适合作为 `BINDING_BUNDLE` 工件。
- 合成难例覆盖独立 Top 1 互不兼容、规则拦截 LLM 第一名、过期成员、成员父维度、fanout block/
  认证预聚合、空候选、续问 origin/span、输入顺序稳定、未知 JSON 字段与全链篡改。
- 本任务没有新增页面、流程或显著视觉状态，页面确认门禁未触发。

### 2.31 NLU-006 校准置信度与定向澄清（已完成）

- 新增 `internal/askdata/binding/{calibrator,clarification}.go`。生产拟合入口先以源 cases 重放
  EVAL-002 report，训练/验证样本按稳定 ID 归一化并拒绝交叉泄漏、重复、非法范围、未知角色/
  复杂度/歧义和缺少正负标签；固定参数 logistic 拟合后，只在 held-out 验证集上执行 PAV
  isotonic 校准，模型、数据集与结果均保存稳定 hash。
- 校准特征只来自确定性绑定产物：candidate total score、Top margin、exact、lexical、vector、
  graph、rule 和 rank；不读取或接受 LLM 自报 confidence。DIRECT 阈值必须同时满足验证集最小
  样本、precision、confidence 与 margin，找不到合格阈值时门禁默认关闭。
- 决策前完整重放 BindingResult，并重新计算每个 Top bundle 的特征和校准概率。通过 held-out
  门禁的高置信唯一候选进入 `DIRECT`；多候选低置信只生成 2～3 个与真实 bundle 一一对应的
  `CLARIFY` 选项；单候选证据不足返回 `EVIDENCE_REQUIRED`，NoMatch 返回 `NO_MATCH`，不会为了
  满足选项数量凭空构造候选。
- 澄清展示只接收经过净化的稳定业务标签、差异和该 bundle 已持有的 evidence，拒绝物理 SQL/
  表列文本、跨 bundle evidence 和伪造 bundle；问题、option ID、semantic version ID、模型证据、
  typed `ToolRequestClarification` 参数及 decision hash 均可确定性重放。
- 合成校准 fixture 覆盖输入顺序稳定、held-out gate、单调校准、训练/验证泄漏、直接执行、低
  margin、验证门禁关闭、单/零候选、伪造选项、敏感物理文本、跨候选证据、未知 JSON 字段与
  hash 篡改。任务没有数据库、外部模型或页面依赖，页面确认门禁未触发。

### 2.32 QUERY-001 Binding Bundle -> Semantic IR（已完成）

- 新增 `internal/askdata/ir/{builder,validation}.go` 和完整合成测试。`BuildRequest` 同时携带原始
  Binding Request/Result 与选中 bundle hash，构建前重放整个 NLU/Graph/候选绑定链；选中 bundle
  后再次校验 metric-model、dimension-model、ACTIVE member ownership 和 FILTER 维度归属，
  NoMatch、篡改或不在 Top bundles 内的 hash 均不能进入 IR。
- 生成的 Semantic IR 只含 pinned release/hash 和稳定版本 ID。指标 alias 由 metric version ID
  的 SHA-256 确定性生成，不引入未绑定标签；成员默认/集合与正/负操作符合并为 EQUALS、IN、
  NOT_EQUALS、NOT_IN，多个值自动使用集合操作。无成员的 FILTER 维度失败关闭；SORT 维度按自然
  粒度投影到 groupBy，以满足冻结 IR 的排序引用约束。
- 当前轮时间只能使用 Understanding Request 中已重放的 deterministic `ResolvedTime`；继承时间
  因 ContextSnapshot 有意不保存日期边界，必须由上一轮规则工件提供 `InheritedTimeResolution`，
  并同时匹配 previous snapshot hash、原 question span、TimeUnderstanding、RULE evidence 与
  resolution hash，禁止按当前日期重新解释“今年/上月”。time dimension 必须唯一，时间区间保持
  `[start,endExclusive)`；comparison 仅映射同比/环比/较上期且必须有时间，默认 limit 500、上限
  10000。
- `BuildArtifact` 保存 policy scope、domain、binding result、bundle、规范 IR、evidence 和 artifact
  hash；严格解码会从原请求重新构造后做全值比较。冻结的 Semantic IR v1 只有一个
  `modelVersionId`，多模型/GraphPath 当前明确失败关闭，不能静默丢失认证 join 语义；需要通过
  后续版本化 IR 合同升级后才能放开。
- 为使 `ir` 按设计消费 `binding` 且不形成包循环，纯 Semantic IR 合同下沉至
  `internal/askdata/ircontract`，`ir` 继续以类型别名和同名函数保持现有 Go/JSON API；共享校准
  feature/example 合同同样下沉到 `internal/askdata/calibration`，EVAL-002 类型保持别名兼容，
  `FitCalibratorFromEvaluation` 留在 evaluation 层先重放 report/cases 再调用 binding calibrator。
- 合成集成测试覆盖本轮时间、同比、成员过滤、分组、指标排序与 Top 5 的完整构建，以及 candidate
  set 顺序稳定、严格解码、binding/artifact 篡改；单元负测覆盖多模型、过期成员、无成员过滤、
  FILTER 集合归一、SORT 投影和继承时间 proof 篡改。本任务没有数据库、外部模型或页面依赖。

### 2.33 QUERY-002 Semantic Contract Resolver（已完成）

- 新增 `internal/askdata/compiler/resolver.go`：`ResolveRequest` 必须携带 QUERY-001 的原请求和
  Artifact，解析前完整重放 Binding -> IR；`ContractLookup` 固定 policy scope、domain、release
  ID/hash、IR hash 及精确 model/metric/dimension/member/relationship 版本集合，不读取或接受“当前
  release”指针。FILTER 的 member-dimension 边和 time dimension 也进入规范 `Resolution` 与 hash，
  防止多个维度存在时把合法成员重新绑定到错误过滤条件。
- `Resolution` 保存模型、字段、指标/measure AST、维度、label-free 成员、GraphPath、关系和物化
  快照合同。字段 ID/code/role/type、维度字段兼容、主时间字段、metric/measure 枚举、成员敏感度
  下限、对象精确集合和 canonical AST 均独立失败关闭；GraphPath 先重放 path ID、hop 连续性、
  方向和风险摘要，再逐步匹配 release 中的 join type/cardinality/fanout/model pair。
- 新增 `resolver_postgres.go`：生产 Store 在一个 `REPEATABLE READ + READ ONLY` USER/RLS 事务内读取
  pinned release，验证 manifest hash/count，以及 `POSTGRES_REGISTRY`、
  `EXECUTION_SEMANTIC_LAYER` 两个 READY 投影的 expected/applied hash 和 object count。模型、指标、
  measure、维度、成员、关系必须是该 release 的精确 CERTIFIED 对象，源 content hash 与 manifest
  一致；代码从不查询 `release_state` 或 ACTIVE release。
- 模型源必须仍是 current PUBLISHED DWS/ADS，且同一 dataset/version/materialization 的 layer、
  Dataset DSL hash、schema hash、published view、snapshot 和非空 row count 全部一致，物化必须
  ACTIVE。Store 重新执行 `dataset.Prepare`，只从通过完整 DSL 校验的稳定字段生成 field contract；
  stale、retired、缺失或不一致源统一拒绝。
- 敏感成员公开/持久化边界按技术设计保持 label-free：Resolution JSON 只含 member/dimension
  version ID、content hash 和 sensitivity，不包含 key、label、alias、lookup hash 或参数明文；
  PostgreSQL 读出的规范 member key 只作为 compiler 包内不可序列化、不参与 hash 的临时编译参数，
  且 Resolution 去除该参数后仍可重放校验。MEMBER manifest 还必须是四字段、label-free、alias
  version ID 有界排序的 v1 合同。
- Measure contract 同时固定逻辑 `measure_id` 与不可变 Measure Version ID；后续公式 AST 无论使用
  `measureId` 还是 `measureVersionId` 都必须归一到 release 中同一精确版本，不能按 code 或当前版本猜测。
- 新增小型、可重放的 `testfixture.SemanticMetricBuildRequest` 和 resolver 单元/集成测试。单元覆盖
  current release 切换后 pinned 结果/hash 不变、源顺序规范化、stale materialization、访问错配、
  member/time 归属、缺失对象、关系不一致、标签泄露和 AST/artifact 篡改；PostgreSQL 用例在显式
  `ASKDATA_INTEGRATION_DATABASE_URL`/admin URL 下复用现有 READY fixture，当前无 URL 时按仓库惯例
  跳过。本任务没有新增页面，未触发页面设计确认门禁。

### 2.34 QUERY-003 IR -> Dataset Query DSL Adapter（已完成）

- 新增 `internal/askdata/compiler/adapter.go`：`AdaptRequest` 携带 QUERY-002 原 `ResolveRequest` 与
  `Resolution`，先完整重放 Binding -> IR，再逐项匹配 scope/domain/release、model、IR/build/
  graph/resolution hash。Semantic IR v1 仍只允许单模型；存在非空 GraphPath/relationship 时明确失败，
  不会把认证 Join 路径静默丢弃。
- 生成临时、不可持久修改的 Dataset Document：维度输出复用模型稳定 field ID，指标输出 ID 从
  metric version ID 确定性派生；source node 只固定解析出的 Dataset Version，物理 schema/view 和
  column/type 白名单只来自 ACTIVE materialization。groupBy 日期粒度转换为受控 `DATE_TRUNC`/`CAST`，
  sort 只引用已投影稳定 ID，正式行数上限不超过 IR/DSL `resultLimit`。
- 新增 `adapter_ast.go` 严格解析 release 中的 Metric/Measure AST。Measure `FIELD_REF` 只能指向模型
  字段，非 COUNT 聚合必须可证明为数值表达式；Metric `MEASURE_REF` 支持逻辑 ID 和 version ID，但
  均归一到声明的精确 Measure Version，引用集合必须与 metric dependency 完全一致。有限算术、
  cast/round/coalesce、比较、集合与布尔节点映射为 Dataset Expression；未知字段、未知类型、额外
  JSON 字段、跨 metric 依赖和不受支持形状全部失败关闭。
- Metric default filter 不作为全局 WHERE 误伤其他指标，而是在各自 Measure 聚合参数内转换为
  `CASE WHEN`；IR 成员过滤只生成稳定 `PARAM_REF` 和参数类型/基数，规范 member key 仅存在于 live
  编译参数。时间使用 `[start,endExclusive)`；DATE/DATETIME 边界与 IANA timezone 固定，timezone
  留给 QUERY-005 只读执行会话使用。
- 普通查询生成一个 `CURRENT` 计划；同比、环比、较上期分别按日历年、日历月或等长前期生成
  `CURRENT`/`BASELINE` 两个同构计划。闰日/月末使用 clamp，不依赖机器当前日期；整体 QueryArtifact
  绑定比较合同和原 IR hash，未把比较请求静默降级为普通查询。
- `QueryArtifact` 只序列化 Dataset DSL、参数 shape/cardinality、受信物化白名单及 DSL、logical、
  compiled、aggregate plan hash；SQL、Args、member key 与时间参数值都在 JSON/hash/普通审计之外。
  公开 JSON 可用 dummy typed 参数重放 SQL shape/hash；仅 live plan 的 `CompiledQuery()` 能取得隔离
  拷贝，反序列化工件必须在执行前重新从 pinned registry 注入参数。
- `internal/querycompiler` 最小新增显式 `LimitKind`：既有调用默认 `PREVIEW`，AskData 正式计划显式
  使用 `RESULT` 且仍受 DSL `resultLimit` 限制；`CompiledQuery.PlanHash` 绑定方言、SQL placeholder
  shape、limit、参数合同和排序后的物理白名单，但排除所有运行值。
- Golden 测试固定 PostgreSQL SQL/Args，并覆盖逻辑/版本 Measure 引用、指标级默认过滤、月粒度、
  10000 行正式上限、CURRENT/BASELINE 参数移位、闰日 clamp、运行值不改变 compiled hash、公开 JSON
  无 SQL/Args/member/date 泄漏、物理 view 篡改、未知 AST/Measure 和 PREVIEW 越界失败。本任务没有
  新增页面，未触发页面设计确认门禁。

### 2.35 QUERY-004 计划 Validator 与安全 EXPLAIN（已完成）

- 新增 `internal/askdata/validator/plan.go`：只接受保留 in-process compiled query 的 live
  `QueryArtifact`，先验证 QUERY-003 全链 hash 和 compiled plan hash，再执行独立 SQL tokenizer/static
  gate。仅允许单条 SELECT/非递归 CTE，关系必须精确命中 plan 固定的 published schema/view；受控
  aggregate/date/cast 函数外的调用、DDL/DML/COPY、锁/会话语句、注释、分号、dollar quote、未知
  relation 和 `EXPLAIN`/`ANALYZE` 均以稳定 rejection code 失败关闭。
- 新增 `explain.go`：`PostgresExplainer` 校验 AccessContext 与 scope/domain，在数仓 reader pool 上开启
  `REPEATABLE READ + READ ONLY` USER/RLS 事务，事务内设置 tenant/user/domain、IANA timezone、
  `statement_timeout` 和 `lock_timeout`，且只拼接固定 `EXPLAIN (FORMAT JSON)` 前缀；不会运行
  `EXPLAIN ANALYZE`，也不会执行原 SELECT。
- 原始 EXPLAIN JSON 只在当前调用内解析并丢弃。公开 `ValidationArtifact` 仅保存 root node/cost/rows、
  plan node、Seq Scan、Join rows/fanout 数值摘要和 summary/validation hash，不包含 SQL、Args、成员/
  时间值或物理标识符。限制配置只能收紧，不能超过平台 ceiling；root/result/node rows、total cost、
  plan nodes/depth、Seq Scan rows、Join rows/fanout 都会在执行前拒绝。
- Artifact 重放会再次执行全部数值不变量，而不只检查 hash；因此篡改摘要后自行重算公开 hash 也不能
  把超阈值计划伪装为已验证。测试覆盖正常 SELECT/CTE、DATE_TRUNC/CAST/CASE、SQL 注入形态、未知 UDF/
  relation、高成本、大扫描、Join 行数/fanout、超上限配置、live 参数丢失和 JSON 无泄漏。本任务没有
  新增页面，未触发页面设计确认门禁。

### 2.36 QUERY-005 问数执行适配（已完成）

- 新增 `internal/askdata/validator/executor.go`：`ExecutionRequest` 必须同时携带同一 live
  `QueryArtifact` 与匹配的 `ValidationArtifact`，逐计划重查 role、query/compiled hash、MaxRows、
  static SQL gate 和 live Args；公开 JSON 重放后因没有参数值而明确不可执行。
- 新增 `queryruntime.SemanticMaterializationRevalidator`：控制库在 read-only repeatable-read USER/RLS
  快照内只按 compiler 固定的 materialization ID 复核 ACTIVE DWS/ADS、current PUBLISHED dataset
  version 和精确 published view，不跟随当前 semantic release。数仓执行则让普通/对比双计划共享一个
  `REPEATABLE READ + READ ONLY` reader 事务，设置 tenant/user/domain、IANA timezone、statement/
  lock timeout；运行时验证 reader 非 superuser/createrole/bypassRLS/继承角色，且发布 relation 是
  SELECT-only view，没有 INSERT/UPDATE/DELETE/TRUNCATE 权限。
- `ExecutionResult` 的行仅存在于进程内私有字段，`Rows(role)` 只返回深拷贝。公开
  `ExecutionArtifact` 不含行，只记录 scope、plan/validation hash、列名+PostgreSQL type OID、每计划
  MaxRows/row count/result hash 和总 result hash。typed canonical hash 区分 NULL/布尔/整数/浮点/
  DECIMAL/日期时间/字节/字符串；DECIMAL 与时间不经 float64 损失精度，NaN/Inf/未知值失败关闭。
- `queryruntime` 新增独立 `SEMANTIC_QUESTION` 审计合同和 Auditor 接口；Start 只含 plan/validation
  hash、EXPLAIN 最大成本、计划数、行/超时预算，Finish 只含 result hash、行数、耗时、状态和稳定
  error code。Executor 强制 Auditor 存在，终态审计失败会丢弃已经成功的结果，SQL、Args、成员/时间
  参数和结果行均没有审计字段可写。
- 行数与总字节均有硬上限；run ID 注册进程内 cancel handle，并校验发起取消的 AccessContext actor/
  domain。caller deadline、整体 25 秒 ceiling、DSL timeout、PostgreSQL statement timeout 和 pgx
  cancellation 共同收口；CURRENT/BASELINE 仍是同一一致性快照。本任务没有页面或数据库迁移，未触发
  页面设计确认门禁。

### 2.37 QUERY-006 规则 + LLM 结果核验与异常分析（已完成）

- 新增 `internal/askdata/validator/result.go`：`ResultRuleRequest` 必须同时持有匹配的 Semantic IR、
  live QueryArtifact、QUERY-005 进程内结果和可信质量证据；先重放 IR/query/result/scope/hash，再按
  Dataset 输出合同核对列名和 PostgreSQL OID。DECIMAL 只接受 exact NUMERIC，整数、布尔、日期、
  DATETIME 和字符串也只能接受相容 OID，不允许用 float8 冒充精确小数。
- 确定性规则检查 result limit、schema/type、重复行、输出 key 唯一性、key/非空字段 NULL、metric-only
  fanout、DIVIDE 的零分母证据、IR 时间覆盖、数据新鲜度、质量总状态/逐规则，以及 CURRENT/BASELINE
  同构 shape。所有规则和证据引用进入稳定 `RuleArtifact` hash，BLOCKING 失败不能被后续模型修改。
- 空结果按每个计划角色核对成员是否存在、请求时段是否确无数据、是否由权限裁剪；只有“成员存在 +
  时段无数据 + 非权限裁剪”才设置 `NoDataConfirmed`。空结果、fanout/重复 key、覆盖/质量问题或异常
  对比趋势都要求先执行 `ANOMALY_ANALYSIS`，不会直接把空集表述成业务无数据。
- 新增 `anomaly.go`：模型仅接收原问题 fact、Semantic IR/plan hash、列级 count/distinct、指标
  non-null/null/min/max/sum、CURRENT/BASELINE 聚合趋势、规则检查、质量和 policy evidence；完整结果行、
  SQL、Args 和物理查询不进入 PromptFact。每个 Action 先通过 cognition schema，再校验证据必须精确
  来自本轮 prompt；发明、错 hash 或跨轮混用 evidence 均失败关闭。
- `RESULT_VERIFICATION` 必须包含 `RESULT_ANSWERS_QUESTION` 检查；模型给出 PASS 时全部模型检查也必须
  通过。最终 verdict 由治理层合并：确定性规则失败时只能 RETRY/BLOCK，模型 PASS 仅记录
  `RuleOverridePrevented=true`，不能覆盖规则。最终工件深拷贝认知结论，绑定 plan/result/rule/evidence
  hash，并交叉验证摘要、异常/核验引用和最终 verdict。本任务没有页面、迁移或真实外部模型调用，
  未触发页面设计确认门禁。

### 2.38 ORCH-001 Typed Tool Registry（已完成）

- 新增 `internal/askdata/toolhost/registry.go`、`tools_catalog.go`：Registry 必须一次注入架构文档全部
  14 个编译期 typed handler，缺少、重复或未知工具均不能构造；目录不存在通用 SQL/nGQL/数据库工具。
  `Definitions()` 返回按工具名稳定排序的隔离副本，每个 Definition 固定 argument/result JSON Schema、
  schema hash、Permission、BudgetCharge、timeout、最大结果字节和整体 definition hash，可做重放审计。
- `AuthorizationContext` 必须持有有效 PolicyScope、scope 内单一 domain 和无重复显式 Permission；Call
  在执行前再次通过封闭 `ToolArguments.ValidateFor`，且 release 必须与 scope 的 pinned release 完全一致，
  参数中的 domain 也只能等于当前 run domain。权限、release、domain 或预算失败返回稳定 REJECTED，
  不触达 handler、不扣调用预算。
- 每个工具固定一次 tool call 扣费；`execute_query_plan` 消耗一次 formal query，
  `compare_candidate_results` 消耗两次 formal query，`probe_join_cardinality` 与
  `execute_validation_query` 各消耗一次 validation query。`AvailableTools` 同时按 permission 和剩余
  8/2/3 预算过滤后给认知 Prompt 使用；每个工具还有 1～20 秒独立 timeout，caller cancel/timeout 与
  handler 稳定错误被规范化，原始内部错误不会回传模型。
- 14 个 Result 都有独立 Go 合同和 validator。Host 对结果做 evidence ID/ref 全量回绑、规范 JSON、
  result hash、大小限制和递归 unsafe-key 扫描；`rows/sql/ngql/args/parameters/prompt/messages/reasoning/
  credential` 等字段即使绕过 Registry 直接构造 Tool Message 也会被 `Response.Validate` 拒绝。正式查询
  结果只允许 hash、verdict、行/列/metric 聚合摘要，不可返回完整行；Semantic Contract formula 只暴露
  hash、操作符和稳定版本引用，不可返回物理表达式。
- 维度值 sanitizer 会对 `Sensitive=true` 的 member 清空 display label/aliases，保留稳定 ID、层级 ID 和
  label-free evidence；其他工具也只接收稳定对象 ID、Semantic IR、plan hash、质量/图/验证摘要。最终
  `Execution` 保存 definition hash、实际 BudgetCharge、耗时、timeout 状态和已验证 Response，供
  ORCH-003/004 驱动与审计。本任务没有页面、迁移或外部调用，未触发页面设计确认门禁。

### 2.39 ORCH-003 LLM 中枢 Agent Loop（已完成）

- 新增 `internal/askdata/orchestrator/loop.go`：外层 Loop 以一个 Question cognition stage 为边界，
  首轮由 `BuildMessages` 只装入已规范化的 PromptFact 和精确 EvidenceRef；即使规则已能走 fast path，
  仍必须完成至少一次 Cognition 裁决。模型只能返回封闭 Action 或选择当前阶段允许的 typed tool，不能
  直接执行 SQL、访问 Registry handler 或改写 Question 状态。
- Tool 列表先经 Registry 按 permission/剩余预算过滤，再与阶段白名单求交；每次调用前重新计算剩余
  8 tool/2 formal/3 validation 预算。Loop 对 action stage/hash、AI request/model/token 审计摘要、已见
  action/call、evidence allowlist、Tool response callId/tool 和实际收费二次复核，任何发明证据、重放、
  confused-deputy 响应或超预算收费都失败关闭。
- 成功工具结果必须已通过 Tool Host 的 typed/sanitized Response 合同；Loop 只把结构化 action 与工具
  脱敏结果追加到进程内 transcript，登记精确 evidence ref 后才允许下一轮引用，不保存隐藏思维链。
  Tool REJECTED/FAILED、无进展、冲突 evidence ID 和超过 512 KiB transcript 都立即停止。
- 使用 Question Run 的累计 BudgetUsage 执行最多 4 LLM、8 tools、2 formal、3 validation、16 step 和
  25 秒总时限；恢复调用可携带已见 action hash/call ID。caller cancel 原样传播，总时限触发稳定 timeout
  并标记预算 exhausted；复杂纠错在第 4 次模型调用后不会进入第 5 轮。本任务没有页面、迁移、数据库/
  数仓写入或真实外部模型调用，未触发页面设计确认门禁。

### 2.40 ORCH-004 审计、预算和幂等（已完成）

- 新增 `internal/askdata/orchestrator/audit.go`：`CheckpointLoop` 复用 ORCH-002 Store 和 `000217`
  既有表/触发器，以稳定 checkpoint ID/hash 执行一个 actor-scoped 原子事务。事务先锁定并验证完整
  ReplaySnapshot，再按认知顺序写入每个 `LLM_DECISION`、typed tool replay artifact、`tool_calls`、
  `TOOL_RESULT`、绝对 `BUDGET_UPDATED`、稳定 `ERROR`，最后更新 run 并追加状态事件；任一事实或
  外键失败时整批回滚，不存在仅扣预算或仅保存工具结果的半提交。
- 模型事件保存 action hash/type、AI request UUID、evidence IDs、provider model、attempt/token/cost/
  redaction、耗时，并由数据库外键再次约束 actor/tenant/`SEMANTIC_QUESTION` purpose。工具审计保存
  参数 hash、完整 sanitized outcome hash、call hash、definition hash、实际 charge、evidence IDs、
  duration/timeout/error code；tenant/domain/actor/policy scope/release/run version 均来自锁定 run，调用者
  无法另传覆盖。所有详情继续通过 audit forbidden-key 门禁，不保存问句、prompt、reasoning、SQL、Args
  或完整结果行。
- 每个完成工具额外保存 `tool-execution-replay-v1` EVIDENCE 工件，内容仅为 Tool Host 已验证的 typed
  output/evidence 和稳定执行摘要。Replay 会把工件与 `tool_calls` 的 request/result/call hash、预算、
  状态和证据做交叉校验；`ReplayToolExecution` 可重建成功的脱敏 typed response。精确 checkpoint 重试
  返回原 snapshot，checkpoint 内容碰撞或 stale version 失败；`BindReplayGuards` 将已见 action/call
  注入 Loop，恢复后重复 action 在 Tool Host 执行前即触发 no-progress。
- 新增 `budget.go`：只有实际达到 step/LLM/tool/formal/validation/time ceiling，或命中受控 transcript
  ceiling，才能构造 exhausted 终态。语义阶段在调用方明确选择时可生成证据绑定的 CLARIFICATION；
  context/硬时限等不能靠用户消歧的边界统一 BLOCK。completion artifact、预算/错误事件和终态迁移同
  事务提交，`budget_exhausted` 不会停留在非终态。本任务无页面或数据库迁移，未触发页面设计确认门禁。

### 2.41 ORCH-005 Question API 与 SSE（已完成）

- 新增 `internal/askdata/http/question.go`，正式提供 `POST /api/v1/questions`、
  `GET /api/v1/questions/{runId}` 和 `POST /api/v1/questions/{runId}/clarifications`；
  `cmd/api/main.go` 已装配同一个受 `auth.RequireAccessToken` 保护的 handler。认证层会实时复核 token、
  session 和选中业务域，HTTP 层再要求 claims actor 与数据库 access context 精确一致。
- 创建请求采用 16 KiB 严格 JSON、4096 rune 问句和必填 `Idempotency-Key`。HTTP 层规范化问句后只把
  域分离 SHA-256 交给 Backend；原问句、幂等明文不会进入 Store、响应或日志。未传 conversation 时用
  tenant/actor/domain/幂等 hash 确定性生成 UUID，使丢失响应后的重试仍命中同一 CreateRun 事实。
- `PostgresService` 在 USER/RLS 事务内解析当前 ACTIVE release 和用户真实 ACTIVE role IDs，构造规范
  `PolicyScope` 后复用 ORCH-002 `CreateRun/Resume`。读取历史 run 时先由 actor/domain RLS 取得其 pinned
  release，而不是错误跟随新 ACTIVE release；当前 role 集变化仍会在 Store 的 policy hash 核对中失败。
- 澄清 API 只接收 completion artifact 中公开 allowlist 的稳定 `optionId`；自由文本/未知字段被严格 JSON
  拒绝。合法选项创建相同 conversation、parent run、actor/domain/policy/release 的 child run；过期选项、
  非澄清终态、scope 改变或 release 已无法继续时失败关闭。
- 新增 `sse.go`：数据库 `event_index` 直接作为 SSE `id`，严格支持 `Last-Event-ID`、连续事件检查、轮询
  去重、15 秒 heartbeat、终态关闭和重连。每个公开 event 最大 16 KiB，只包含 event UUID/index、run
  version、state/type/stage/status/code、action/artifact hash、evidence IDs、duration 和 createdAt；不投影
  `Details`、AI request、Tool 原始结果、prompt、SQL、参数、result rows 或隐藏 reasoning。
- HTTP 测试覆盖鉴权前置、严格 body/header、确定性 hash/conversation、幂等冲突映射、completion 白名单、
  澄清自由文本拒绝、SSE Last-Event-ID/游标越界/断线续传/去重/超限 payload 和恶意审计详情泄漏。
  真实 app/admin PostgreSQL 回滚 fixture 另验证 ACTIVE release、实际 role scope 和同域跨 actor RLS。
  本任务未新增页面、迁移或真实模型调用，未触发页面设计确认门禁。

### 2.42 ORCH-006 Conversation 与运行保留策略（已完成）

- `internal/askdata/orchestrator/retention.go` 新增原问句与运行工件保留合同。默认 `HASH_ONLY`，不产生
  可恢复原文；`ENCRYPTED_SHORT_TERM` 使用独立 AES-256-GCM 密钥和随机 nonce，认证上下文固定
  tenant/domain/actor/conversation/run/policy/release/question hash/expiry，跨上下文调包、篡改和过期读取
  都失败关闭。
- 原问句 TTL 上限 7 天，artifact payload TTL 上限 365 天且不得短于原问句 TTL。到期 PurgePlan 只列出
  待清理 payload，继续保留 ArtifactDigest、状态、disposition、预算、事件/工件/Tool 计数、artifact
  hashes 和可复算 statistics hash；数据库追加式审计身份与统计不因清理改变。
- `internal/config` 与 `.env.example` 新增 `ASKDATA_QUESTION_RETENTION_MODE`、
  `ASKDATA_QUESTION_RETENTION_TTL`、`ASKDATA_RUN_ARTIFACT_TTL` 和可选
  `ASKDATA_QUESTION_ENCRYPTION_KEY`。hash-only 模式拒绝意外加载密钥；加密模式必须显式提供独立
  base64 32 字节密钥。
- `PostgresStore.CreateRun` 对同 tenant/domain/actor/conversation 使用事务级 advisory lock，以首个 run
  为 release anchor；同 release 可继续，release 漂移在写入前返回 `ErrPinnedScopeMismatch`。上下文继承
  只接受同 tenant/actor/domain/conversation/release 的完整 ANSWERED 治理链，policy 变化返回 scope reset。
- 本任务未新增页面或迁移；密钥未写入仓库。

### 2.43 WEB-002 Question API Client 与 SSE 状态（已完成）

- `web/src/lib/ask-data-api.ts` 冻结 ORCH-005 的 Operation、Run、Completion、Budget、PublicEvent
  TypeScript 合同，提供 create/get/clarify client。所有调用复用现有 `apiRequest/apiResponse`，继承
  Bearer token、`X-Business-Domain-ID`、并发 refresh 合并与单次 401 refresh/replay；没有另建认证旁路。
- SSE 采用 authenticated fetch stream，而不是无法附加认证和业务域 header 的原生 EventSource。
  增量 decoder 支持 chunk/CRLF/comment/multiline/retry；客户端再次限制 16 KiB payload，并校验状态/
  事件枚举、hash、时间、event ID 与连续 index。断线后携带持久 `Last-Event-ID` 有界退避续传，重复
  event 去重，gap/replay/payload 错误失败关闭；refresh stream error 与普通网络断开才允许重试。
- `web/src/hooks/use-ask-data-question.ts` 提供创建、恢复、澄清子 run、终态回读、取消与 reset；旧请求由
  AbortController 和 generation 双重隔离，迟到事件不能覆盖新 run。稳定错误映射区分认证、Release
  不可用、scope 漂移、冲突、游标/stream、网络和服务错误。
- 使用 Node 内置 TypeScript test runner 新增 5 条单测，覆盖 SSE 分块、retry、断线游标、重复事件、
  event gap、取消和错误映射；无需引入新的第三方测试依赖。
- 现有 `AskDataPage` 未改，仍保持 WEB-001 mock；真实进度 UI 消费这些 hooks 归属 WEB-003，需先通过
  页面设计确认门禁。

### 2.44 WEB-003 会话和运行进度 UI（已完成）

- 用户在三份运行状态设计方向中确认方案 1；设计真值为
  `/Users/susanmartinez/.codex/generated_images/019fd77d-fc16-7c13-93c2-72eb947fc395/exec-858b6505-95ec-4a17-9f45-55a93cb4cf59.png`。
  1280×720 浏览器实现、完整对照和中栏聚焦对照保存在 `design-qa-artifacts/web-003-*`，根目录
  `design-qa.md` 最终为 `passed`。
- `ConversationProgress` 把公开 Question 状态投影成“接收、权限、理解、检索、口径、关系、计划、
  执行、核验、完成”十步时间线。只使用固定受控文案和公开时间戳；`stage`、`code`、hash、prompt、SQL、
  Tool payload 与思维链不会直接出现在进度文案中。SSE 创建/连接/已连接/重连、取消和终态都有明确
  `role=status`/`aria-live` 语义。
- `ConversationOutcome` 为客户端错误、取消、BLOCKED、CLARIFICATION_REQUIRED 和 ANSWERED 提供可访问
  终态。WEB-003 只显示 completion artifact 的短引用和证据计数，不提前解释结果；真实澄清选择归
  `WEB-004`，结果表/图归 `WEB-005`。
- `AskDataPage` 已接入 WEB-002 `useAskDataQuestion` 的 create/cancel 生命周期。WEB-001 会话数据仍是
  确定性页面快照；选中的运行中会话作为设计/交互基线。真实运行开始后会隐藏静态口径、质量分与反馈，
  改为“等待受控证据”，防止把示例数据当作本轮事实。
- 新增纯状态投影单测，覆盖 RESULT_VERIFYING、BLOCKED 和有界纠错回到 BINDING；前端测试总数为 8。
  应用内浏览器验证取消进入空闲态、会话恢复、证据折叠、真实创建失败关闭、静态证据隐藏和 1120×800
  两栏响应式，无控制台错误。当前 `127.0.0.1:8080` 旧 API 容器未包含本工作区最新 Question 路由，
  因此成功 SSE 的浏览器 E2E 未在该容器执行；WEB-002 fetch-stream 单测与 WEB-003 状态投影测试已覆盖
  客户端合同，运行镜像更新后可补成功链路回归。

### 2.45 WEB-004 定向澄清与证据面板（已完成）

- 用户确认方案 1「内联双候选决策卡」并授权补齐实现所需合同；设计真值为
  `/Users/susanmartinez/.codex/generated_images/019fd77d-fc16-7c13-93c2-72eb947fc395/exec-131de369-22bd-4012-851e-2123c3092bbf.png`。
  1280×720 浏览器实现、完整/中栏聚焦对照和 1120×800 响应式截图保存在
  `design-qa-artifacts/web-004-*`，根目录 `design-qa.md` 最终为 `passed`。
- Question 公共完成工件新增 `clarificationId`，候选新增 `difference`、`evidenceIds` 与可选完整治理证据。
  公共证据只投影定义、Owner、语义版本/状态、实际时区/区间、质量分/状态和数据新鲜度；缺少或非法字段
  会被拒绝/隐藏，真实运行不会以设计稿中的王敏、李楠、v3.2、v2.8、98.7 等示例值补齐。
- `POST /api/v1/questions/{runId}/clarifications` 的 body 固定为
  `clarificationId + optionId + runVersion`。服务端校验父运行状态/版本、完成工件 UUID 和允许候选，并以
  父 run 与澄清工件身份派生稳定消费键；同一选择可重放，第二个不同选择稳定返回“已消费”，从服务端
  阻止不同请求幂等键造成 child run 分叉。前端另有同一选择的在途去重。
- 新增 `ClarificationCard`、`EvidencePanel` 和纯证据完整性/格式化模块。两个候选、差异、Owner、版本、
  实际时间、质量与新鲜度来自同一公共 DTO；原生 radio/label、提交、取消、region 和 disclosure 语义
  可访问。选择候选会同步更新右栏证据驾驶舱。
- 本次只前置实现 WEB-004 必需的原子澄清消费子合同；`NLU-008` 的会话 Release Pin、澄清 deadline、
  budget freeze/resume 和被取代 Release 的重新绑定仍未实现，`NLU-008` 保持待办。

### 2.46 WEB-005 结果表格与图表（已完成）

- 用户确认方案 1「KPI 总览 + 趋势与渠道并列」；设计真值为
  `/Users/susanmartinez/.codex/generated_images/019fd77d-fc16-7c13-93c2-72eb947fc395/exec-80d3bd85-26fd-4b71-99b5-a251ae49347d.png`。
  1280×720 浏览器实现、完整/结果区聚焦对照和 1120×800 响应式截图保存在
  `design-qa-artifacts/web-005-*`，根目录 `design-qa.md` 最终为 `passed`。
- `CompletionView` 新增可选 `question-result-v1` 结果投影。公开合同只接受带治理证据的结果摘要、比较、
  dataset/column、精确字符串或 null cell、有界预览、总行数和 presentation view；最多 4 个 dataset、
  16 列、100 行、800 个 cell 和 8 个 view。顶层 prompt、SQL 和未受控 result rows 不会进入公共响应。
- 服务端分别校验 STRING/INTEGER/DECIMAL/DATE/DATETIME、DIMENSION/MEASURE、schema/行列形状和实际时间；
  `LINE` 需要日期维度与数值度量，`BAR` 需要 2～20 个分类，`KPI` 只允许单行数值，`TABLE` 保持完整
  schema。推荐视图不合格时只能按确定顺序回退到另一个合格 view。
- 新增 `ResultWorkspace` 与纯结果投影模块：呈现 KPI、比较、折线、柱状、原生 table、5/10/20 每页行数、
  分页、当前页 CSV 和新鲜度。cell 保留精确字符串；超出 JavaScript 安全整数范围的值不进入图表。
  真实 `ANSWERED` 运行和设计快照使用同一组件；缺失或非法结果回退到受控 `ConversationOutcome`。
- 大结果只公开有界预览与 `totalRows`；前端明确显示已加载前缀并仅在该前缀内分页，完整结果不发回模型。
  `EvidencePanel` 复用相同治理证据。合计行和不可加指标仍由 `ADD-004` / `WEB-009` 决定，本任务没有
  根据设计 mock 伪造合计行为。

### 2.47 WEB-006 结构化反馈（已完成）

- 用户确认方案 3「两步结构化反馈弹窗」；设计真值为
  `/Users/susanmartinez/.codex/generated_images/019fd77d-fc16-7c13-93c2-72eb947fc395/exec-6b81b56c-e1b7-436f-85ab-4b8ad96cda0a.png`。
  1280×720 同状态完整/聚焦对照与 1120×800、800×800 响应式截图保存在
  `design-qa-artifacts/web-006-*`；根目录 `design-qa.md` 最终为 `passed`。
- 新增 `POST /api/v1/questions/{runId}/feedback`：只接受 actor 自有终态运行及精确 `runVersion`，正向为
  `ACCURATE + NONE`，负向为 `INACCURATE` 加九类治理问题之一；说明可选、最多 2,000 字符并拒绝控制字符。
  handler 使用严格 JSON shape，未知字段、非法组合和无效版本在进入 backend 前失败关闭。
- 反馈写入现有 `askdata.query_feedback`，绑定 tenant/domain/actor/run/release hash/policy hash；每个 actor/run
  使用事务咨询锁。完全相同的内容返回原收据，内容变化才递增 `record_version`，并发冲突返回稳定错误。
  提交路径只追加反馈记录，不修改 Question answer、完成工件、Release 或任何生产语义对象。
- 新增原生 `FeedbackDialog` 两步流程：第一步用 fieldset/legend/radio 选择 9 类问题，第二步确认分类并补充
  详情；含关闭、返回、提交中、失败和成功态。实时 `ANSWERED` 与设计快照使用同一入口；“有用”也提交
  公共正向反馈。前端分类模型与 API builder 都有纯函数测试，权限类文案明确进入人工复核。

### 2.48 TIME-001 TimeContract 合同、存储与版本化（已完成）

- `000225_askdata_time_contract` 已建立 `time_contracts` 和不可变 `time_contract_versions`，两表都有
  tenant/domain 复合外键、强制 RLS、owner、时间戳、稳定 code/version 唯一性；开发回退文件完整。
  `semantic_models.time_contract_version_id`、`domains.default_incomplete_period_policy` 和
  `metric_versions.incomplete_period_policy_override` 已纳入数据库约束与运行角色授权。
- `internal/askdata/registry/timecontract.go` 定义完整 Go/JSON 合同、8 类粒度、IANA 时区校验、财务日历依赖、
  对比对齐、月末溢出、数据可用截止表达式、预期延迟、粒度检查和规范 SHA-256。策略解析顺序固定为
  `METRIC > TIME_CONTRACT > DOMAIN > PLATFORM_DEFAULT`，平台默认是产品决策 D01 的 `MTD` 并回传 source。
- 平台数据集版本生命周期没有 `ACTIVE` 枚举，因此 `TIME_CALENDAR_NOT_ACTIVE` 的权威解释是：日历版本必须
  是同 tenant/domain 数据集的 current `PUBLISHED` version。数据库 trigger 与 Go resolver 对缺失、跨域、
  非当前、非发布版本使用同一失败关闭边界；没有写入任何 `HUMAN-007` 尚未提供的企业日历常量。
- `model_certify.go` 和数据库认证 trigger 都拒绝未绑定已认证时间合同的 semantic model。Release 新增
  `TIME_CONTRACT` 对象；Go `BuildReleaseManifest` 与数据库 `DRAFT -> VALIDATING` trigger 都要求每个模型的
  精确时间合同版本在依赖闭包内。已认证合同的任何 UPDATE/DELETE 返回
  `TIME_CONTRACT_VERSION_IMMUTABLE`，合同变化只能新建版本并重新走 Release。
- `api/schemas/time-contract-v1.schema.json` 关闭额外字段并与 Go/DDL 枚举一致。单元测试覆盖严格 JSON
  round-trip、未知字段、四层优先级、时区/日历/粒度、hash 稳定性、认证和 manifest；独立回滚事务集成测试
  覆盖数据库认证、财务日历、不可变和 Release 闭包。`000225` 已应用到本地开发控制库。

### 2.49 TIME-002 确定性时间编译器（已完成）

- Semantic IR 的 `timeRange` 新增 `requestedPeriod` 与 `grain`；理解层已有的 `ResolvedTime.Expression/Grain`
  不再在 IR 边界丢失。规范化器把旧合同稳定补为 `ABSOLUTE/DAY`，Schema 的 canonical 输出则要求两个字段，
  从而兼顾旧工件重放与新查询的确定性语义。
- `internal/askdata/compiler/time.go` 输出 `ir.ResolvedTimeSpec` / `ResolvedComparison`。计算只使用认证时间合同的
  IANA 业务时区，所有范围均为 `[start,endExclusive)`；支持 `MTD`、`FULL_PERIOD`、`LAST_COMPLETE`，记录
  `PolicyApplied/PolicySource`、data watermark、裁剪和周期回退。业务时区覆盖 IR 中的用户时区，日历日推进
  使用 `AddDate`，不会把 DST 日错误近似成固定 24 小时。
- 对比期实现 `YEAR_OVER_YEAR`、`MONTH_OVER_MONTH` 及按粒度映射的周/季/期环比；
  `SAME_DAY_COUNT` 保证当前期和对比期日历天数相同，`SAME_CALENDAR_RANGE` 平移两端。
  2 月 29 日、3 月 31 日等不存在目标日遵循 `CLAMP_TO_LAST_DAY` 或返回
  `TIME_COMPARISON_UNDEFINED`；不支持粒度、空区间、缺失日历分别返回 TODO 规定的稳定错误。
- `compiler/calendar.go` 定义 `FiscalCalendarResolver`：实现方必须基于认证 calendar dataset 的
  `fiscal_period_key` 查询 `min(date)` 与 `max(date)+1 day`。财月、财季、财年当前期、上一完整期和对比期
  均只能走该接口；无版本、resolver 或匹配行返回 `TIME_CALENDAR_LOOKUP_FAILED`，编译器没有财务月份近似。
- `QueryArtifact` 可绑定并哈希 `resolvedTimeSpec`；绑定后当前期和 BASELINE 参数都使用解析后的边界，重放校验
  同时核对时区、比较类型/期数、午夜边界和 `time_start/time_end_exclusive` 参数形状。未提供解析结果时保留
  既有绝对区间兼容路径，避免破坏已存在的 QUERY-003 工件。
- 测试覆盖月/季/年、T+1 MTD、LAST_COMPLETE、闰日、月末两种溢出、America/New_York DST、四月起始的
  `CURRENT_FISCAL_QUARTER`、财政上一完整期、错误码，以及 60 组任意日期区间和 SAME_DAY_COUNT 属性断言。
  本任务无页面改动，没有触发设计确认门禁。

### 2.50 TIME-003 resolvedTimeSpec 输出与四处统一渲染（已完成）

- `internal/askdata/answer/timespec.go` 提供唯一 Go `RenderTimeSpec`，输出 `RangeLabel`、`AsOfLabel`、
  `PolicyLabel`、`ComparisonLabel`、`TruncatedHint`；非法 spec、非法时区或不支持 locale 均返回空视图，
  不发布不完整时间说明。`compiler/artifact.go` 暴露稳定的解析时间 artifact 边界和同源校验入口。
- `web/src/askdata/format/timespec.ts` 实现同名、同逻辑浏览器渲染器；Go/TypeScript 共用
  `internal/askdata/testfixture/timespec/render-v1.json` 的 20 组 fixture，五个字段逐字符相等。用例覆盖
  MTD 裁剪、无对比、FULL_PERIOD、LAST_COMPLETE 回退、周/月/季/年、财月/财季/财年、绝对范围、
  闰日与月末 overflow。
- Question API 的浏览器结果合同新增原始 `resolvedTimeSpec` 和服务端生成的 `timeSpec`。公共投影先使用
  compiler 规则校验 spec，再调用 Go renderer；payload 中即使带伪造 `timeSpec` 也会被重新生成，测试已
  验证“DO NOT TRUST”不会进入响应。
- `ResultWorkspace` 的 KPI 摘要、裁剪提示、同比说明、详情 disclosure 和新鲜度，`EvidencePanel` 的
  “与结果一致”时间区，`web/src/report/runtime/timespec.ts` 的报告副标题以及
  `web/src/export/timespec-footer.ts` 的导出页脚均消费统一 renderer。CSV 当前页导出会追加时间口径、
  实际区间、数据截止、可选对比说明与裁剪提示。
- 页面采用用户确认的方案 2：KPI 下方显示紧凑摘要、橙色裁剪提示和蓝色“查看/收起时间口径”控件；
  展开后显示浅蓝锚定详情卡；EvidencePanel 标记“与结果一致”。1280×720 完整/结果区聚焦对照、
  1120×800 响应式、两处展开收起与控制台检查全部通过，`design-qa.md` 最终为 `passed`。
- B11 的真实报告页面和 PDF Worker 尚未开工；本任务只冻结其副标题/页脚 runtime bridge，没有虚构页面。
  后续 `RPT-011` 与导出 Worker 必须直接复用该 bridge/renderer，不能重新拼时间字符串。

### 2.51 SNAP-001 schema 身份与数据快照版本分离（已完成）

- 迁移 `000230_warehouse_data_snapshot_version` 新增强制 RLS 的
  `platform.materialization_snapshots`。刷新批次以 build-run UUID 作为 `snapshot_version`，开始事实先写，
  完成后一次性补齐 `snapshot_hash`、`snapshot_completed_at`、`data_available_through`、行数、大小与
  `OK|WARN|FAIL`；完成记录不可更新或删除。
- `platform.dataset_materializations` 由“每次刷新一个 ID”收敛为“每个 schema 一个稳定 ID”：同一规范化
  Dataset DSL `schema_hash` 的刷新原位切换当前物理表并追加 snapshot；schema 变化仍走
  `BUILDING → ACTIVE`、退役旧物化。这样 Semantic Model/Release 固定的 `materializationId` 不会因日常
  数据刷新失效。
- Worker 在仓库 Build 前调用 `BeginSnapshot`，失败路径把进行中 snapshot 完成成 `FAIL`，成功激活与快照
  完成、质量事实、build 终态和稳定视图切换保持同一控制事务边界。Warehouse Executor 只对 DSL 声明的
  `DATE|DATETIME outputGrain.timeField` 计算 `max()` watermark；读取端不扫描仓库事实。
- `GetLatestSnapshot` 只依赖控制面 reader，按完成时间读取最新已完成记录并忽略中断刷新。
  `registry.EvaluateReleaseRuntimeState` 只比较 schema hash；snapshot 变化不改变 Release，FAIL 仅产生
  `QUALITY_WARNING` 或阻断。快照完成触发 `materialization_snapshot_completed` 通知，为
  `QUERY-010 InvalidateBySnapshot` 提供主动失效入口；缓存 key 后续仍包含 snapshot version，通知丢失不会
  破坏正确性。
- 历史 build detail 与物理清理路径已改读 snapshot 历史：同 schema 多次刷新仍能审计每次构建，删除数据集
  时会清理全部历史物理表，不留下只存在于快照表的仓库对象。本任务没有页面，不触发设计确认门禁。

### 2.52 TIME-004 数据可用边界与 PARTIAL/NO_DATA 分流（已完成）

- `internal/askdata/validator/coverage.go` 新增受封装的 `CoverageVerdict`：严格按半开区间将请求判定为
  `FULL`、`TRUNCATED` 或 `NONE`，分别映射正常执行、`PARTIAL` 和 `NO_DATA`；结果码为
  `TIME_COVERAGE_TRUNCATED` / `TIME_COVERAGE_NONE`。
- `CoverageControl` 只依赖 SNAP-001 的 `SnapshotControlReader`，逐个读取控制面最新已完成快照；多模型
  查询取所有水位的最小值，不为覆盖判定扫描仓库事实表。水位缺失、来源重复或返回的 materialization
  身份不一致均失败关闭。
- `TRUNCATED` 会把 `dataAvailableThrough + 1 个业务日` 写回 `ResolvedTimeSpec.ResolvedEndExclusive`，
  设置 `TruncatedByDataAvailability=true`，并按 `SAME_DAY_COUNT` / `SAME_CALENDAR_RANGE` 同步缩短对比期。
  Evidence 记录请求区间、实际区间、水位、`timeRangeTruncated` 和用户提示。
- 带时间范围的 QueryArtifact 必须走 `Validator.ValidateCovered`：`NONE` 在任何 artifact/SQL 检查和 EXPLAIN
  之前短路，`FULL/TRUNCATED` 则校验工件使用的 spec 与物化来源和 verdict 完全一致后才进入既有安全
  EXPLAIN。旧 `Validate` 遇到时间工件直接返回 `ErrCoverageValidationRequired`，不能绕过门禁。
- 本任务没有页面或视觉状态实现，不触发设计确认门禁；统一的 PARTIAL 页面展示与“禁止加入报告”仍归属
  后续 `QUERY-011` / `WEB-013`。

### 2.53 ADD-001 指标可加性字段与三道关卡（已完成）

- 迁移 `000226_askdata_metric_additivity` 为模型内 `measures` 与命名指标 `metric_versions` 增加同构的
  `FULLY_ADDITIVE|SEMI_ADDITIVE|NON_ADDITIVE` 事实、半可加时间算法、聚合限制、不可加维度、单位/币种、
  零分母策略、显示精度、启发式建议和确认审计字段。现有数据库以 `measures` 作为度量版本层，因此未另造
  与既有模型冲突的 `measure_versions` 表。
- 旧的必填 `ADDITIVE|SEMI_ADDITIVE|NON_ADDITIVE` 值只迁移为非权威 suggestion（其中 `ADDITIVE` 规范化为
  `FULLY_ADDITIVE`）；新的事实列保持 NULL。导入器也只写 suggestion，任何认证、Release 或编译路径都不读
  suggestion，防止迁移或启发式结果被静默当成业务确认。
- 数据库关卡以 CHECK/FK 分别拒绝非法枚举、缺少半可加时间算法、错误的不可加聚合方式、非法零分母/精度、
  不完整确认对和缺少认证必填事实；`CERTIFIED` 完整性约束使用 `NOT VALID` 部署，避免历史认证行阻塞迁移，
  但所有新写入或更新仍立即受约束。
- 应用认证关卡提供 `ValidateAdditivity`、`CertifyMetric` 与 `CertifyMeasure`，严格返回五个规定错误码；管理员
  DRAFT 写入在事实变化时由服务端刷新 `confirmedBy/At`，清空事实时同步清空审计，客户端不能伪造确认人。
- Release 静态关卡在创建 manifest 前重新读取 metric/measure immutable contract，聚合并稳定排序全部失败
  对象；即使绕过认证 helper，也无法发布缺少权威事实的对象。三层均有相互独立的测试。
- 本任务没有页面或视觉状态实现，不触发设计确认门禁；启发式、补录清单和指标中心批量确认仍归属
  `ADD-002`，编译正确性下一步归属 `ADD-003`。

### 2.54 EVAL-003 Fixture Regression Runner（已完成）

- `internal/askdata/evaluation/runner.go` 新增 `fixture-regression-v1`：固定九类失败阶段，按稳定 case ID
  运行 synthetic fixture，并生成带可复算 content hash 的 case/stage 报告。报告不保存原问句或结果行。
- 内置纯内存 pipeline 从 synthetic 用户/域/模型/指标/维度/成员/关系资产执行权限裁剪、确定性召回、
  成员歧义/过期判断、fanout 阻断、IR 构造、计划校验和结果读取；不使用 expected disposition 驱动实际
  路径，也不访问真实模型、数据库、图服务或数仓。
- 直接回答复用 EVAL-001 比较完整 Semantic IR 和规范结果；早停路径精确比较 DIRECT/CLARIFY/REFUSE/
  NO_DATA 与稳定 reason code。IR、结果或敏感泄漏回归分别落入 IR、VALIDATION、SECURITY。
- `cmd/askdata-eval` 默认执行内置六类 hard cases；`-fixture` 只接受有界、严格 JSON 且显式 synthetic 的
  外部 fixture。全部通过返回 0，回归失败输出完整机器报告后返回 1，无效输入/运行器错误返回 2。
- `EVAL-004` 仍需 `HUMAN-001～004` 和可用 DWS/ADS，不能用 synthetic runner 冒充真实 E2E 评测。

### 2.55 ADD-003 可加性编译规则（已完成）

- `AggregationPlanner` 在 Dataset DSL/SQL 生成前读取 release-pinned 指标事实，返回稳定的
  `ADDITIVITY_MISSING`、`SEMI_ADDITIVE_TIME_AGG_MISSING`、`NON_ADDITIVE_SUM_ATTEMPT`、
  `INCOMPATIBLE_UNIT` 和 `NON_ADDITIVE_DIMENSION_COLLAPSED`；半可加缺查询时间维度另以
  `SEMI_ADDITIVE_TIME_DIMENSION_MISSING` 失败关闭。单位/币种不一致和不可加维度被折叠均不会进入 SQL。
- 比率公式继续从其精确 Measure 集展开：先产生 `SUM`/其他受控原子聚合，再在聚合之上执行除法；每个
  denominator 都包裹 `NULLIF(...,0)`，`zeroDenominatorPolicy=ZERO` 才显式转 0，且不会被通用 nullPolicy
  意外覆盖。去重指标在目标分组粒度直接生成 `COUNT(DISTINCT ...)`，不对分组结果求和。
- 半可加不是把明细值直接塞进与 GROUP BY 冲突的窗口函数，而是使用单源两阶段计划：内层按所有目标
  非时间维度和原始时间点先聚合，外层按时间桶或跨时间范围执行 ordered `ARRAY_AGG` 期初/期末取值，
  `PERIOD_AVERAGE` 使用期间快照均值。两阶段之前先应用成员、时间和指标默认过滤。
- Dataset DSL 将既有 PreAggregation 安全扩展到无 Join 的单源阶段，并只增加 `PERIOD_BEGIN`、
  `PERIOD_END`、`NULLIF` 三个受控表达式；SQL Compiler 仅在 PostgreSQL 接受 ordered period aggregate，
  SQL Validator 白名单同步接受编译器生成的 `ARRAY_AGG`/`NULLIF` 和数组下标语法。
- `semantic-query-adapter-v2` 新增带哈希的 `MetricAggregationContract`，固定指标版本、可加性、半可加算法、
  不可加维度、单位/币种、零分母和 `totalsNotSummable`，为 `ADD-004` 提供不依赖可变注册表的结果合同桥。
- 本任务没有页面或视觉状态实现，不触发设计确认门禁；合计行、重算合计和图表堆叠限制仍归属 `ADD-004`
  与后续 `WEB-009`。

### 2.56 ADD-004 结果契约与合计行/堆叠限制（已完成）

- Query Artifact 的 release-pinned `MetricAggregationContract` 继续补齐稳定 `resultColumnName` 与
  `displayPrecision`；Resolver 从认证指标读取显示精度。序列化工件仍不包含 SQL、参数值或可变注册表引用，
  结果规范化可仅凭查询工件和执行工件把每列投影为 `DIMENSION|TIME|METRIC` 及完整可加性元数据。
- `compiler.BuildRecomputedTotalPlan` 从原计划派生只保留目标指标、移除展示分组/排序且结果上限为 1 的验证
  查询；成员、时间、默认过滤、物化白名单和 live 参数绑定全部继承。多个不可加指标在同一角色合并为一条
  查询，CURRENT/BASELINE 各至多一条，并按查询次数计入既有 `maxValidationQueries <= 3`。
- 重算输出只接受单行、列名完全匹配的 exact decimal string；float、缺列、多行或查询失败均失败关闭。
  预算耗尽时不产生查询、不填 `RecomputedTotal`，下游因此只能隐藏，不得退化为分组行求和。
- `web/src/shared/totals.ts` 是三端唯一实现：问数、报告、导出模块只重新导出同一函数引用；完全可加返回
  `SUM`，半可加/不可加只在存在合法重算值时返回 `RECOMPUTED`，否则返回带说明的 `HIDDEN`。行求和以
  `BigInt` 对齐小数位，不经过 `Number`，覆盖 `0.1 + 0.2` 与超安全整数 decimal。
- 新增 `component-manifest-v1.schema.json`，将 `stackingRequiresAdditive` 固定为必填布尔值；共享组件可用性
  规则对非完全可加指标关闭堆叠/占比能力，供后续 FUSE-004、WEB-009 和报告编辑器直接复用。
- 本任务没有页面或显著视觉状态改动，不触发设计确认门禁；真实合计行、脚注、禁用 tooltip 仍归属
  `WEB-009`，实施时必须遵守其页面门禁。

### 2.57 IMPORT-001 语义资产导入存储与状态机（已完成）

- `000227_askdata_semantic_import` 新增 `semantic_imports` 与 `semantic_import_rows`：12 类资产、7 个批次状态、
  4 个行状态、上传幂等键、行号唯一键、租约/尝试次数/完成时间、DRAFT 版本追踪和严格错误数组均受数据库
  CHECK/FK/trigger/RLS 独立保护；所有外键均包含 `tenant_id`。
- `PostgresStore` 提供幂等 `CreateImport`、跨租户失败关闭的读取、SECURITY DEFINER tenant list/claim/heartbeat、
  最多 500 行的幂等写入、完成/失败和审计。合法状态边由数据库守卫；非法跳转、身份字段变更、非
  `VALID -> COMMITTED` 行变更均拒绝。
- `CommitValidRows` 将对象创建器注入同一事务，只读取 `VALID` 行并强制返回 `DRAFT`；任何非 DRAFT 或中途
  错误整批回滚。`WithdrawImport` 同样把实际 DRAFT/Release 引用检查留给 IMPORT-004 的注入实现，删除成功后
  才原子转为 `WITHDRAWN`，批次事实与版本 ID 审计仍保留。
- `FileRowSource` 只接受无凭据/查询参数的 `minio://` 或 `s3://` URI，读取后校验固定 SHA-256，再将单 Sheet
  CSV/XLS/XLSX 的表头和数据转为稳定 JSON 行；行号不含表头且跨重试不变。Worker 每 500 行提交、并行续租，
  基于已落库最大行号继续；确定性文件/合同错误记为 `FAILED`，基础设施或 lease 错误留待重试。
- `cmd/worker` 已注册导入 Worker；`IMPORT-003` 已将启动时的 `UnavailableValidator` 失败关闭守卫替换为
  生产四层校验器，文件解析后的行必须完整通过校验后才可提交。
- 本任务没有新增页面或显著视觉状态，不触发设计确认门禁；上传入口已随主链补齐，模板、校验报告与
  提交接口分别由 `IMPORT-002`～`004` 收口。
- 随 `IMPORT-003` 验收补齐上传入口：API 对 multipart 字段、域、资产类型、扩展名和 50 MiB 上限严格
  校验，按 SHA-256 内容寻址写入 MinIO 后只创建 `UPLOADED` 批次；响应不返回内部 MinIO URI。

### 2.58 IMPORT-002 按业务域动态生成导入模板（已完成）

- `TemplateDefinitionFor` 固化 TODO 指定的 12 类资产逐列合同；生成器同时支持 CSV 与 XLSX，XLSX 固定为
  `Import`、`References`、`Instructions` 三 sheet，主表带说明行、冻结窗格、筛选与 10,000 行枚举下拉。
- `PostgresTemplateCatalog` 在 tenant/domain RLS 事务内读取稳定可用的 MODEL、DIMENSION、METRIC、
  HIERARCHY，按类型/code/id 确定性排序并限制 10,000 条；对照表只供查阅，人工引用仍统一填写 code。
- `GET /api/v1/askdata/semantic/imports/template` 已接入认证 Admin Handler，只接受精确单值的
  `assetType`、`domainId`、`format`，请求域必须等于认证域；CSV/XLSX 以 no-store、nosniff 附件返回。
- `FileRowSource` 对多 sheet XLSX 只解析 `Import`，并跳过精确模板说明标记，使生成模板可以直接进入
  IMPORT-003 且首条业务数据仍为 row 1。测试覆盖 12 类精确列、全部格式、引用顺序、枚举下拉、空域、
  真实 PostgreSQL 空域查询与生成文件 round-trip。
- 本任务没有页面或显著视觉状态，不触发设计确认门禁；上传、四层校验/报告和提交继续分别归属
  `IMPORT-001`、`IMPORT-003`、`IMPORT-004`。

### 2.59 IMPORT-003 四层校验器与可下载报告（已完成）

- `FourLayerValidator` 通过 `PreparedRowValidator` 在校验前读取完整但有界（最多 100,000 行）的批次上下文，
  因而能确定性检查跨行公式环、层级连续性与同批 code；Worker 仍只按 500 行写库，并从已持久化最大行号
  恢复。空文件/超行数是确定性失败，目录/数据库故障保留为可重试基础设施错误。
- L1 严格模板列、必填、类型、日期、数组、枚举和安全 JSON AST；L2 使用 tenant/domain RLS 目录解析
  认证语义对象、当前 PUBLISHED + ACTIVE DWS/ADS、逻辑字段、Owner/角色及同批引用；L3 校验公式 DAG、
  模型内指标—维度兼容、层级连续、cardinality/fanout、可加性和时间粒度；L4 校验名称/别名、词典冲突、
  负向上下文、敏感性及画像高基数策略。
- 所有问题都有非空 `expected`；只有 `IMPORT_IMPACT_REQUIRES_REVIEW` 是非阻断警告，已有认证 code 的新草稿
  行仍为 `VALID`，但 IMPORT-004 提交必须要求 `acknowledgeImpact=true`。敏感成员原值表继续不授予 Worker
  SELECT；目录只使用非敏感注册表与聚合画像事实。
- `ReportService` 与 `GET /api/v1/askdata/semantic/imports/{id}/report?format=xlsx` 输出原始模板列并追加
  `errorCode/errorMessage/expected/actual`，保留模板说明标记，修复后可由同一 `FileRowSource` 直接重读。
  报告查询同时校验 tenant、domain、批次状态及行数事实，响应使用 no-store/nosniff 附件边界。
- 本任务没有页面或显著视觉状态，不触发设计确认门禁；下一步是 IMPORT-004 批量 DRAFT 创建、影响确认、
  批量审批与按批次撤回。

### 2.60 SEC-001 三阶段授权裁剪（已完成）

- `internal/policy/authorization.go` 新增 label-free `SemanticObjectRef` 和可重放 `SemanticAccessSnapshot`。
  PostgreSQL resolver 在每次阶段调用中重新验证认证 actor、scope 中全部 ACTIVE role、全部 domain membership、
  pinned release ID/content hash，以及阶段对应的 READY 投影；不读取 mutable active-release pointer，也不返回
  name、definition、alias、物理表或 SQL。跨租户、跨域、角色增删和投影漂移均失败关闭。
- `internal/askdata/security/authorization.go` 固定 `BEFORE_RECALL`、`BEFORE_BINDING`、
  `BEFORE_EXECUTION` 三个不可互换入口。召回前 receipt 提供唯一可进入检索的 label-free release object 集；
  绑定前和执行前必须对请求对象集合获得精确全量授权，不能用部分裁剪或早期 receipt 绕过阶段间撤权。
- `AuthorizationReceipt` 同时绑定 stage、PolicyScope hash、pinned release、domain、阶段输入 hash、授权对象集和
  policy snapshot hash；内外两层 hash 均可重放校验。公开错误只区分 invalid/denied/unavailable，不透传可能
  含未授权名称的存储诊断。`BuildAskDataCacheKey` 同时绑定完整 PolicyScope、IR hash、warehouse snapshot、
  freshness 与 engine version，保留旧数据集缓存合同不变。
- 单元测试覆盖三阶段独立调用、跨租户/跨域/跨角色、绑定部分放行、绑定后撤权再执行、snapshot/receipt 篡改、
  cache scope 漂移和候选名称无泄漏；可选 PostgreSQL integration 使用真实 RLS/roles/membership/release/
  projection 复核正向三阶段与三类越权。本任务没有数据库迁移、页面、外部模型或正式业务数据写入。

### 2.61 IMPORT-004 批量提交、批量审批与批次撤回（已完成）

- 用户已确认采用“先补齐存储前置再完整实现 12 类”的方案。`000228_askdata_terms_and_kpi_bundle` 新增
  `metric_dimensions/*_versions`、版本化 `business_terms/*_versions`、`certified_examples/*_versions`、
  `kpi_bundles/*_versions`、`evaluation_case_assets/*_versions`，并补齐既有 measure/metric/dimension/
  relationship 与 Release object 合同。该迁移已实际执行 down→up，回滚只在新表和新 Release 对象均无
  事实时允许，且恢复旧状态迁移函数；新的数据库守卫允许完成批次进入 `COMMITTED -> WITHDRAWN`。
- `PostgresDraftCreator` 把 12 类通过校验的行写入权威 DRAFT 版本表，所有 code/owner/role/数据集引用在提交
  事务中重新解析。修改认证对象时复用稳定 object ID、递增 version_no；MODEL 绑定当前 PUBLISHED + ACTIVE
  DWS/ADS，且已修复对不存在的 `dataset_materializations.updated_at` 排序，改按 activated/created 时间。
  HIERARCHY 的一行一级合同可在同一批次合并为单个版本；MEMBER aliases 与成员版本同事务创建。
- `TERM(MEMBER).targetCode` 固定为 `dimensionCode::canonicalValue`。L1 按该合同校验，L2/提交仅把绑定
  dimension version 的 SHA-256 传给 `askdata.resolve_governed_import_member`；函数为固定 search_path 的
  SECURITY DEFINER，只向 SYSTEM 或域/平台管理员返回 opaque member/version ID，不返回原值或别名，
  app/worker 仍无敏感成员表 SELECT 权限。
- commit 强制域 Owner、`all`/`rowNos` 二选一和影响告警确认；逐行创建中任一失败会回滚此前已建 DRAFT 并
  返回失败 rowNo。bulk-certify 对选中对象加锁、按依赖拓扑排序，在外层预检 savepoint + 单对象 savepoint
  中运行数据库单对象关卡及 TERM/层级/关系/example/KPI/eval 专项静态检查；任一失败整批零变更并返回稳定
  失败清单，成功才逐对象认证并写一对象一审计。member aliases 与 parent metric 状态也在同一事务更新。
- selective withdraw 先按唯一 version 分组（多行层级只处理一次），只删除仍为 DRAFT、没有 Release manifest
  或其他 DRAFT 引用的版本；拒绝项逐行返回 `VERSION_NOT_DRAFT`、`VERSION_REFERENCED`、
  `VERSION_NOT_FOUND` 及引用者，批次进入 WITHDRAWN 并保留拒绝对象，不做级联删除。
- API 已接入 `POST /api/v1/askdata/semantic/imports/{id}/commit`、`.../{id}/withdraw` 与
  `POST /api/v1/askdata/semantic/bulk-certify`；严格 JSON、域绑定、错误状态和 row/failure 清单均有 HTTP 测试。
  本任务没有页面或显著视觉状态，不触发设计确认门禁；下一项为无页面的 `IMPORT-005` 对称导出。

### 2.62 IMPORT-005 对称导出（已完成）

- `ExportService` 与 `PostgresExportCatalog` 覆盖 MODEL、MEASURE、METRIC、METRIC_DIMENSION、DIMENSION、
  MEMBER、HIERARCHY、RELATIONSHIP、TERM、CERTIFIED_EXAMPLE、KPI_BUNDLE、EVAL_CASE 全部 12 类；输出列
  直接消费 IMPORT-002 `TemplateDefinition`，多资产按治理顺序分 sheet，单资产使用 `Import` sheet 并附
  `ExportInfo`，可由现有导入解析器直接重读。规范内容 hash 不依赖 XLSX ZIP 元数据和行输入顺序。
- 未指定 Release 时按稳定身份取最新 `CERTIFIED`；指定 Release 时只取 manifest 固定版本。所有语义引用
  输出 code 而非 UUID。为保证历史指标展示元数据不漂移，`000242_askdata_semantic_export` 将 metric
  name/description 纳入 `metric_versions` 并回填旧版本，旧写入路径的空值仍安全回退稳定身份。
- `CONFIDENTIAL`/`RESTRICTED` MEMBER 行及指向这些成员的 MEMBER TERM 不进入导出；省略数写入
  `ExportInfo`、同步响应头和异步任务结果。成员原值/别名的数据库权限没有放宽。
- `GET /api/v1/askdata/semantic/exports` 严格校验 domain、12 类去重列表、可选 Release 和 xlsx 格式。
  不超过 5,000 行同步返回；更大请求创建持久任务并返回 202、状态 URL、鉴权下载 URL 与有效期。
  请求创建时固定精确版本 manifest，避免 Worker 执行期间 current head 变化。
- `semantic_export_jobs` 强制 RLS、不可变 manifest、状态形状和 PENDING/RUNNING/READY/FAILED 跳转；Worker
  无表级 SELECT/INSERT/UPDATE/DELETE，只能调用固定 search_path 的 list/claim/complete/fail 函数，租约
  到期可恢复、存储失败最多重试 5 次。产物以 `semantic-exports/<tenant>/<domain>/<job>/<hash>.xlsx`
  写入 MinIO，下载再次校验 tenant/domain/created_by、READY 与过期时间。
- `000242` 已实际执行 down→up；原 TODO 预留的 `000229_askdata_release_retention` 保持给 RETAIN-001，
  没有发生编号占用。本任务没有页面或显著视觉状态，不触发设计确认门禁；Batch 8 下一项为无页面的
  `TERM-001` 业务词典版本化与冲突检测。

### 2.63 TERM-001 业务词典版本化与冲突检测（已完成）

- TERM 管理合同已覆盖稳定身份、完整版本字段、内容 hash、目标/角色引用校验与 PENDING 审批隔离；管理
  API 仍复用 `/api/v1/askdata/semantic/terms` 的通用 CRUD，应用角色真实创建、读取、更新和删除均已验证。
  `FEEDBACK` 与 `FEEDBACK_CANDIDATE` 只能生成 PENDING DRAFT，数据库 Release/别名门禁只接受
  `CERTIFIED + APPROVED`。
- `TermService.DetectConflicts` 和 bulk-certify 共用同一冲突函数：相同稳定 term/type、半开生效期重叠、
  同 priority 且不同 target 时返回所有阻断候选并保持整批零变更；认证时锁定稳定身份行，封闭并发竞态。
  不同 priority 必须携带显式 Owner note 才能通过，并把所有 shadow 候选写入认证审计；首尾相接的非重叠
  生效期可并存。
- `REGEX_SAFE` 使用严格语法解析 + Go RE2 编译，只允许字面量、字符类、首尾锚点与 `{n,m}`（m≤32），
  拒绝反向引用、分组/嵌套量词、lookaround、分支、点号与无界量词；输入上限 64KiB，匹配 deadline 10ms。
  负向上下文为空、与 term 相同或互为子串时在草稿校验和认证预检中拒绝。
- 无需新增迁移：`000228_askdata_terms_and_kpi_bundle` 已由 IMPORT-004 提供完整表与 Release 门禁，
  `000229` 继续严格保留给 RETAIN-001。本任务无页面或显著视觉状态；下一项为同样无页面的 `TERM-002`。

### 2.64 TERM-002 Trie/Aho-Corasick 最长匹配与负向上下文裁剪（已完成）

- `understanding.DictionaryMatcher` 复用 NLU-001 `NormalizeQuestion` 与 origin span 映射，在 rune 级
  Aho-Corasick 自动机中完成精确全量匹配；候选按 span 长度降序、priority 降序、term 字典序和版本 ID
  稳定排序后贪心占位。输出同时携带原文/规范化 span、term/target version、target code、match mode、
  priority 与 evidence hash。
- 缓存按 tenant/domain/release ID 分区并保存 Release content hash；相同 Release ID 的 hash 变化会原子
  替换旧快照。PostgreSQL Loader 只读取指定 READY/ACTIVE Release manifest 中内容 hash 一致且
  `CERTIFIED + APPROVED` 的 BUSINESS_TERM，因此未发布、PENDING 或漂移版本不会进入自动机。
- 有效期使用 `[valid_from, valid_to)`，非空角色集合要求与请求角色相交，负向上下文按规范化文本 ±N
  rune 窗口裁剪；所有裁剪及重叠/残余覆盖均保留稳定 reason。`PREFIX`、`SUFFIX`、`REGEX_SAFE` 第二轮
  只处理精确命中未覆盖区间；正则使用 RE2 并保留 10ms deadline，`VECTOR` 明确排除于确定性匹配。
- `dictionarysearch` adapter 把已完成政策裁剪的命中转换为 SEARCH-003 的 trusted exact lane 与 NLU-005
  `ExactMatch` evidence；SEARCH exact 权重仍由 `RankConfig.ExactWeight` 独立配置。MEMBER 不进入通用
  ExactMatch，继续服从既有专用成员和敏感标签路径。
- 无新增迁移、页面或显著视觉状态；下一项为同样无页面的 `KPI-001`。

### 2.65 KPI-001 KPI Bundle 版本化对象与认证（已完成）

- `KPIBundle`、`KPIBundleItem` 与 canonical Release contract 已成为权威 Go 合同；item 数量限定 1～8，
  role 只接受 HEADLINE/TREND/BREAKDOWN，必须至少一个 HEADLINE，order 必须从 1 连续且唯一。每个 item
  的 chart type 与 legacy default chart type 都通过统一 Component Manifest catalogue 校验。
- 版本化 CRUD 已接入现有语义管理 backend 与 `/api/v1/askdata/semantic/kpi-bundles`；创建/更新前要求
  metric version 为同 tenant/domain CERTIFIED，group-by dimension 必须存在同域 CERTIFIED 且
  `compatible=true` 的 metric-dimension 版本。认证入口复用完全相同的结构、引用和内容 hash 校验，避免
  管理 API、批量导入和 bulk-certify 三套规则漂移。
- Release manifest 对 KPI Bundle 新增依赖闭包：Bundle 引用的全部 metric/dimension version 必须同时
  固定在同一 manifest。PostgreSQL Loader 只消费指定 READY/ACTIVE Release 中 object/version/hash 完全
  一致的 CERTIFIED Bundle，并在读取时再次验证依赖闭包；旧/新 Release 切换会确定性回退/前进版本。
- `KPIBundleMatcher` 对 applicable question pattern 与已识别 metric 覆盖度使用独立权重，稳定输出
  `KPIBundleCandidate`。唯一高分可选中；前两名 margin 小于门槛时不猜测，返回
  `clarificationRequired=true`；无命中返回空。匹配器只能加载认证 Release 中的 Bundle，没有 LLM 临时
  拼装路径；多计划执行和独立预算已由 QUERY-009 闭环。
- `000228` 已包含所需表、RLS 和 Release 数据库门禁，无新增迁移；本任务无页面或显著视觉状态。下一项
  按 TODO 为同样无页面的 `RETAIN-001`。

### 2.66 RETAIN-001 Release 引用计数与 RETAINED 保留态（已完成）

- `000229_askdata_release_retention` 新增 `release_references`、`RETAINED/RETIRED` 状态与
  `retained_at/retention_until/retired_at`，引用身份覆盖报告版本、认证问法、保存问题、KPI Bundle 和
  黄金用例。引用行保存受控名称与 Owner 快照，使 `Retire` 在源对象后续改名或尚未建表时仍能返回完整
  影响清单；表、函数和角色授权均启用 tenant/domain RLS 与失败关闭。
- `AddReference`、`ReleaseReference`、`CountActiveReferences`、引用列表和 `Retire` 已进入 PostgreSQL
  Store。有活跃引用的 `SUPERSEDED` Release 自动转为 `RETAINED` 并设置 24 个日历月的最早退役时间；
  活跃引用返回 `RELEASE_RETIRE_BLOCKED`，保留期未满返回 `RELEASE_RETENTION_NOT_EXPIRED`，满足条件后
  才能进入不可变 `RETIRED`。认证问法、KPI Bundle、密封评测集已接入自动引用；报告版本与保存问题尚未
  建表，后续任务可直接复用通用 Store API。
- 问数入口对 `SUPERSEDED/RETAINED/RETIRED` 的全新 run 返回 `RELEASE_NOT_RUNNABLE`；已有 pinned run
  的精确重放与恢复仍可读取 `RETAINED`，编译器也允许历史固定快照重编译。相同 IR 在 READY 与 RETAINED
  快照上产出相同 Query Artifact、Plan Hash 与 Compiled Plan Hash。
- `ReleaseProjectionCleanupWorker` 先断言对象版本、合同、manifest 与 content hash 完整，再调用注入式
  graph/member 外部清理器；任一外部清理失败都不会提交数据库清理水位。成功后只删除 release-scoped
  GraphPlan/search 工件并把 SEARCH/NEBULA 投影置 `STALE`，保留注册表事实、manifest 以及
  POSTGRES_REGISTRY/EXECUTION_SEMANTIC_LAYER 编译合同。全局 search 文档按对象版本共享，故不物理删除，
  由 Release 投影失效与后续重建负责可见性。
- `000229` 已完成 down→up 回放并实际落库；真实 app/admin/worker 角色验证引用生命周期、影响清单、
  跨租户 RLS、清理失败关闭、过期退役和历史 run 重放。本任务无页面或显著视觉状态；下一项按 TODO 为
  同样无页面的 `PROJ-002`。

### 2.67 PROJ-002 投影一致性四哈希校验与失败关闭（已完成）

- `ProjectionGuard.AssertRunnable(ctx, releaseID)` 通过已授权的 tenant/domain 上下文和 USER-mode RLS
  读取 Release 与四条投影水位。数据库目标映射为逻辑 `REGISTRY/SEARCH/GRAPH/MEMBER`；Release 仅允许
  READY/ACTIVE，每条投影必须存在、状态为 READY，且 expected/applied hash 同时等于 Release content
  hash。缺失行、非 READY、expected/applied 漂移均返回 `ReleaseProjectionMismatchError`，差异包含投影、
  期望/实际 hash、状态和最后更新时间；跨租户只得到 MISSING，不泄露 Release hash。
- 成功与失败决定缓存 30 秒。缓存命中先执行一次 release/projection revision 聚合读取（Release
  `updated_at`、投影数量和最大 `updated_at`），因此跨进程状态、哈希、增删变更会在下一次断言立即使
  完整快照缓存失效；同 revision 才复用决定。`Invalidate(releaseID)`/`InvalidateAll` 供已知写端主动
  失效，既避免每次运行重复读取四条完整投影，又不把 30 秒 TTL 变成错误放行窗口。
- 问数 PostgreSQL Store 在 `AUTHORIZED → CONTEXT_READY` 前调用同一守卫。失败不推进状态、不消耗
  run version，而是以同版本追加 `ERROR/BLOCKED/RELEASE_PROJECTION_MISMATCH` 审计事件，details 只含
  Release hash 与结构化差异；HTTP 公共错误映射为 409。READY/ACTIVE 激活函数按既有 `REL-005` 边界
  仍故意不存在，未来激活入口必须先复用同一 `AssertRunnable`，本任务未绕过评测和双人审批门禁。
- 单元测试逐一覆盖 REGISTRY/SEARCH/GRAPH/MEMBER 哈希漂移、非 READY、缺行、Release 状态、30 秒命中、
  revision/显式失效与访问上下文；真实 app-role/RLS 覆盖四哈希放行、跨进程图投影漂移立即拦截和跨租户
  隐藏，真实 Question Store 覆盖差异写入可重放审计链。本任务无页面或显著视觉状态；下一项按 TODO 为
  同样无页面的 `GRAPH-006`。

### 2.68 SEC-002 Prompt Injection 与工具参数净化（已完成）

- `internal/askdata/security/prompt.go` 对外提供稳定安全合同，底层 leaf guard 对 JSON 对象中的键和值做
  NFKC、case-fold 和有界递归检测。每次判定只保存来源、`UNTRUSTED_DATA`、`executable=false`、
  `ALLOW/BLOCK/REFUSE`、稳定原因码和规范内容 hash，不保存或回显原始攻击文本；创建工具、任意
  SQL/nGQL、tenant/domain/release 切换、预算和凭据扩张统一 REFUSE，系统角色/控制指令劫持统一 BLOCK。
- `cognition.NewPromptFact` 在事实入界时检测，`BuildMessages` 在发给 Provider 前重新检测，防止调用方手工
  伪造 PromptFact 绕过构造器。语义描述、认证样例、查询结果和其他阶段事实均在 JSON envelope 中显式
  标记为不可执行的不可信数据；物理查询/凭据键的原有闭集门禁继续保留。
- `SanitizeToolCall` 紧邻 Tool Host 执行前重放 closed `ToolArguments` 校验，要求工具来自阶段、权限和当前
  剩余预算共同生成的 allowlist，独立核对 catalog budget cost、pinned release 和单一 run domain，检测
  所有自由文本参数，并通过严格 JSON 往返生成无共享引用的深拷贝。工具合同没有 tenant、SQL、nGQL 或
  预算字段，unknown 参数不能通过严格 decoder；Registry 仍在 handler 前再次执行权限/范围/预算门禁。
- 安全集覆盖语义描述、样例、结果、Unicode/分隔符变体、键名注入、工具发明、跨 domain/release、预算
  耗尽和 unknown 参数；Loop 级断言被阻断调用不会触达 Tool Host。未新增数据库迁移、页面、外部模型
  调用或业务 fixture；下一项为依赖已满足且同样无页面的 `SEC-004`。

### 2.69 GRAPH-006 图不可用降级矩阵（已完成）

- `ClassifyQueryShape` 将单模型单/多指标、唯一 1-hop SAFE、跨模型多跳、不安全 fanout/预聚合和成员跨维度
  歧义冻结为六个互斥分类。允许项生成与正常路径同构的 `GraphPlan`；多跳与不安全 Join 分别结构化返回
  `GRAPH_UNAVAILABLE`/`GRAPH_UNSAFE_JOIN` 的 `BLOCKED`，歧义返回
  `MEMBER_DIMENSION_AMBIGUOUS` 的 `CLARIFICATION_REQUIRED`，不会产生可执行猜测。
- PostgreSQL fallback 只依赖权威注册表并继续执行 USER-mode app-role RLS、固定 Release 裁剪、认证关系和
  SAFE fanout 校验。Nebula 投影暂时陈旧时允许矩阵内降级，不放宽任何越权或 Release 外对象；认证缓存仍
  要求 READY Graph 投影。降级原因进入 `GraphPlan` 内容 hash，且 `graphDegraded` 已贯穿 Tool Result、
  Evidence Artifact、审计详情和公共 SSE，普通事件不误标。
- Resolver 默认连续 3 次主图失败后熔断 30 秒，开启期间直接执行 fallback 且不触达 Nebula；
  `graph_degraded_rate` 记录总量/降级量，首次越过 5% 阈值时告警。测试覆盖六行矩阵、128 组随机多跳、
  同构结构、熔断零主调用、指标越阈值、Evidence/SSE 以及真实 PostgreSQL RLS/Release 裁剪。
- 显著视觉状态按页面门禁先设计后实现。用户确认方案 1 后，EvidencePanel 保留绿色 ANSWERED/认证主状态，
  增加琥珀色次级降级角标和关系校验证据块；来源、1-hop SAFE、Fanout、结果状态与原因 disclosure 均可见。
  `design-qa.md` 的最终结果为 passed；1280×720 完整/聚焦对照、折叠/展开、1120×800 响应式和零控制台
  error/warning 均已验证。无新增迁移；下一项按 TODO 为无页面的 `SEARCH-006`。

### 2.70 SEC-004 缓存隔离与故障测试（已完成）

- `BuildAskDataCacheKey` 不再依赖隐式结构序列化，而是使用版本化安全信封逐项绑定 tenant、actor、完整
  PolicyScope hash、release ID/hash、IR、warehouse snapshot、freshness 和 engine version。缺失 hash、
  非规范 engine 或控制字符直接不生成键；未来结构演进不会意外遗漏既有隔离维度。
- 并发安全集用 64 个 tenant/actor 在相同 release、IR、snapshot、freshness 和 engine 下重复构造缓存键，
  验证同一请求稳定、跨 tenant/actor/domain/role/release/IR/snapshot/freshness/engine 均不碰撞、不命中。
- 图故障注入将 tenant A 的有效 GraphPlan 同时伪装为 tenant B 的认证 cache 与 PostgreSQL fallback；Resolver
  在两处重放 request/scope/policy/release hash 后均失败关闭。向量故障注入同时返回 foreign-tenant 毒化
  hit 和错误，Retriever 丢弃整个 vector lane，只合并同一 PolicyScope 下的 exact+lexical 事实。
- AI 主模型失败不会自动切到 fallback；显式 fallback 作为独立调用，64 租户并发下每个 Start/Fail 审计
  都保持原 tenant/request 绑定。专项竞态和全仓门禁均通过。未实现 `QUERY-010` 的结果缓存条目、反向索引
  或 snapshot 主动失效，也未新增数据库迁移、页面或真实外部依赖；`QUERY-010` 现已解除 SEC-004 依赖。

### 2.71 SEARCH-006 ANN/Exact 召回对照作业（已完成）

- `000243_askdata_search_recall_audit` 增加 label-free `search_query_samples` 与 append-only
  `search_recall_audits`，并给 `search_documents` 增加 `embedding_dim`。在线向量检索只通过受限
  SECURITY DEFINER 入口记录固定 tenant/domain/Release/doc type/model/dimension 的 embedding，不保存
  问句原文；API 无样本 SELECT 权限，也无 search document 的 embedding/model/dimension INSERT/UPDATE
  权限，Worker 仅能读取/清理样本并追加聚合审计。
- `RecallAuditor` 每小时检查、每租户 24 小时最多执行一次，按最近 7 天、domain/doc type、稳定 hash
  采样最多 100 条，30 天后清理。ANN 与 exact 分别使用独立只读连接和低优先级 timeout；一次取 Top 30
  后计算 `recall@10/20/30` 与双方 p95 延迟。默认阈值 0.99，低于阈值只记录并告警，不自动改 HNSW。
- 在线路由先以最多 1,000 行的 bounded candidate estimate 判断；少于 1,000 关闭 index/bitmap scan 走
  exact，否则设置 `hnsw.ef_search=100` 走 ANN。Embedding Claim 固定当前模型与 2,560 维，完成写入前
  重放校验，不一致返回 `SEARCH_EMBEDDING_MODEL_MISMATCH`；数据库形状约束确保成功向量的 model/dim
  同时存在，非成功状态必须清零。
- 单元测试覆盖已知交集、强制退化 ANN、三种 K、p95、24 小时间隔、候选路由和模型/维度拒绝；真实
  PostgreSQL app/worker/admin 用例创建 30 条已知 halfvec，证明在线小集合走 exact、只记录一条不可由
  app 读取的样本、ANN/Exact 均返回完整 Top 30，并追加三条 recall=1 审计。迁移已 down→up，Worker
  镜像已重建并健康运行；本任务无页面。下一项为 `NLU-007`。

### 2.72 NLU-007 登录后已选业务域强制约束（已完成）

- 用户明确：业务域在登录后、进入智能问数前已经选定。原 TODO 的概率 Domain Routing、领域二选一
  澄清卡与 `SCOPE_CROSS_DOMAIN` 分支不符合产品口径，已同步修订产品设计、技术设计和 TODO；此前生成
  的三份领域澄清预览均作废，未进入代码或产品真值。
- Question API 既有 `RequireAccessToken`、session `business_domain_id` 与 `resolveActiveScope` 已将每个
  Question Run 绑定到唯一当前领域和该域 ACTIVE Release。`UnderstandingService` 的 Policy Fact 从
  `domainIds[]` 收窄为单值 `domainId`；模型省略领域时，服务端用 Policy Evidence 确定性写入
  `score=1` 的唯一领域，模型返回其他领域时失败关闭，模型不再拥有领域路由权。
- Joint Binder 在构造 beam 前要求 PolicyScope 恰好包含一个领域且与 GraphPlan `domainId` 相同。
  检索与图计划本身已经在该领域 Release 内完成授权裁剪，因此其他领域对象无法进入候选，不需要先构造
  跨域 Bundle 再事后过滤。
- Semantic IR 增加必填单值 `domainId`，JSON Schema、Build Artifact、Resolver、Compiler、合成 Fixture、
  Fixture Runner 和 IR 等价比较全部纳入该字段；缺失、多值或持久化篡改都会被验证/重放拒绝。
- 本任务没有迁移、Compose 或页面改动。选定领域之外的产品级 `OUT_OF_SCOPE` 分类与引导统一留给
  `NLU-009`，不会复活领域澄清卡。下一项按 TODO 为 `NLU-008`。

### 2.73 NLU-008 会话 Release Pin 与澄清超时（已完成）

- 新增 `000244_askdata_conversation_release_pin`：`askdata.conversations` 按 tenant/domain/actor 隔离，
  新会话从空 Pin 开始；Question Run 新增 `clarification_deadline`、`budget_frozen_at` 与
  `budget_consumed_json`，状态机增加 append-only `CLARIFICATION_EXPIRED`。
- 首轮 run 只有在 `BINDING -> GRAPH_VALIDATING` 成功时才 Pin 当前 ACTIVE。后续追问相同 ACTIVE
  直接复用；`SUPERSEDED/RETAINED` 返回结构化 `RELEASE_DRIFT_CONFIRM_REQUIRED`，最多列出 20 个
  指标/维度变化，不静默切换；`RETIRED` 强制改用新 ACTIVE 并重新绑定。确认接口使用行锁，精确重复
  确认返回 `replayed=true`，历史 run 始终按原 Release 可重放。
- 进入澄清时冻结预算并设置默认 30 分钟 deadline（`ASKDATA_CLARIFICATION_TIMEOUT` 可配置）。运行时读取、
  澄清提交与 Worker 共用一个事务过期函数；超时后不可再选择。按时恢复时 child run 从等待前消耗量
  继续，等待时长不占预算；Release 已变化则丢弃旧 Bundle 并从 `BINDING` 重验。
- 修正运行推进后的创建幂等重放：身份、scope、Release 与 limits 仍需精确相同，但当前累计 usage 不再与
  初始 usage 比较，避免同一请求在后续状态推进后误报幂等冲突。澄清消费键仍绑定父 run、工件与选择，
  不会分叉出两个 child run。
- 用户确认方案 1「原位口径更新卡 + 右侧 Release Pin 证据」后，`/ask-data` 已接入版本差异、指标/维度
  变化、确认重跑、只看历史和澄清超时状态。1280×720 完整/聚焦对照与 1120×800、720×900 响应式
  通过，记录在 `design-qa.md` 和 `design-qa-artifacts/nlu-008-*`。
- API 角色获得会话 Pin 最小权限；Worker 只获得超时所需的列级 UPDATE/INSERT。迁移已真实应用，且完成
  同事务 down→up→rollback；数据库 schema/权限验证、PostgreSQL 生命周期集成、全仓 Go test/vet、
  前端 26 条测试/lint/build 与 `git diff --check` 全部通过。
- `DR-001` 已解除后端申请出口依赖；`NLU-009` 仍须等待 `WEB-011` 页面方案确认与实现，不能先形成
  缺少用户可见出口的 OUT_OF_SCOPE 流程。

### 2.74 DR-001 明细取数申请工单（已完成）

- 先修正 TODO 中的循环依赖：产品 §5.24 已明确申请可从问数工作台主动发起，不必先拒答，因此实施顺序
  固定为 `DR-001 后端 → WEB-011 页面 → NLU-009 分类接线`。`NLU-009` 不再反向阻塞后端申请合同。
- `000233_platform_data_requests` 新增 tenant/domain 受控的申请与事件表、完整状态形状 CHECK、数据库
  状态机触发器、强制 RLS 与最小角色权限。API 只允许读取、创建和按状态机更新申请；事件表只可追加，
  通用 Worker 和连接测试角色没有任何访问权限。
- `internal/datarequest` 提供严格 JSON HTTP、服务合同和 PostgreSQL Store；正式路径为
  `/api/v1/data-requests`、`/{id}`、`/{id}/submit`、`/{id}/transition`。创建时固定登录 session 的唯一
  业务域；主动入口必须使用空上下文，有来源 run 时只允许申请人本人当前领域的 run，并只验证固定
  Release 内的 metric/dimension/member ID 和解析时间，不存在结果行或 SQL 输入字段。
- `requiredFields` 只能引用当前 RLS 可见的 PUBLISHED 数据集字段。DR-001 暂以 ACTIVE
  `DOMAIN_ADMIN` 形成可运行审批集合；未编造 `HUMAN-011` 尚未提供的数据 Owner、安全会签人或业务 SLA，
  敏感推导与会签门禁继续归属 `DR-002`。
- 真实生命周期测试发现同一时钟刻度内按 `created_at,id` 排序会使事件链偶发乱序，因此追加
  `000245_platform_data_request_event_sequence`，用 `sequence_no = record_version` 作为权威单调顺序；
  时间戳只保留审计含义。该任务没有占用报表预留编号；`000234` 后续已由 `RPT-DB-001` 正式使用。
- 真实 PostgreSQL 回滚事务覆盖主动申请、字段治理校验、审批人解析、跨用户 RLS、身份/context 不一致、
  陈旧版本冲突及六步完整闭环。当前开发库与全新隔离库均通过 schema/权限验证；未新增页面。

### 2.75 QUERY-010 缓存 Key 与快照失效（已完成）

- `internal/askdata/queryruntime` 新增独立结果缓存：Key 严格按 tenant ID、PolicyScope hash、Semantic
  Release hash、规范化 IR hash、按 materialization ID 排序的 snapshot version 五段以 `\x1f` 拼接后
  整体 SHA-256。Scope/Release 不一致、Policy hash 缺失、IR hash 非法、快照为空/重复/不完整或含歧义
  分隔符时直接不缓存，不会回退到弱 Key。
- 缓存条目保存 `result_hash`、`asOf=min(snapshot_completed_at)`、`row_count`、`created_at` 和默认一小时
  TTL；结果 payload 仅以隔离副本读写。普通查询携带当前快照版本，因此刷新后自然 miss；`forceFresh`
  还要求当前快照集合与条目完全一致，缺少当前版本证明或版本变化时跳过缓存。
- 反向索引使用 tenant + materialization ID → cache key；覆盖写、显式删除、TTL 淘汰和任一物化主动失效
  都会清理所有相关索引桶。`internal/materialization/invalidate.go` 新增 LISTEN 投影器，消费 `000230`
  已有 `materialization_snapshot_completed` 通知并立即定向清除；畸形通知不会停止后续消费，通知丢失也
  不影响 snapshot-version Key 的正确性。
- 属性测试随机验证不同 PolicyScope 不共用 Key/条目，并覆盖快照变化、主动通知失效、forceFresh、缺段
  不缓存、payload 隔离和反向索引全生命周期；专项 race、全仓 Go、vet、数据库和仓库门禁均通过。
  本任务未新增迁移、页面或外部服务。

### 2.76 QUERY-008 Cardinality 与 FanoutPolicy 枚举拆分（已完成）

- `askdata.relationships` 的 `cardinality` 与 `fanout_policy` 已收敛为两个正交枚举；`000226` 追加四项
  CHECK、bridge model 同 tenant/domain 外键，并移除 fanout 默认值。历史上能机械证明等价的预聚合
  拼写只做重命名，矩阵外旧组合则清空为人工复核 holding state，绝不推断成 `SAFE`。
- `registry.ValidateRelationshipCombination` 是认证、管理输入、导入 L3、Graph、Resolver 与编译器共用的
  应用侧矩阵。数据库仍以独立 CHECK 做第二道门禁；`MANY_TO_MANY + BRIDGE_REQUIRED` 还必须携带
  bridge model ID，NULL holding row 无法认证、进入 Graph 或编译。
- Graph 风险码已改为稳定的 `<cardinality>_<fanoutPolicy>` 八种组合码，Binder 按预聚合、bridge 和
  BLOCK 分级惩罚。`CompileJoin` 只接收 Release-pinned relationship/source facts 和受限标识符，分别
  生成直接 Join、右侧预聚合、两侧预聚合 + bridge 去重 SQL；任意 BLOCK/NULL 直接返回
  `PLAN_JOIN_BLOCKED` 且不产生 SQL。
- `CompileJoin` 是 QUERY-008 冻结的安全编译边界；既有 QUERY-001/003 明确冻结 Semantic IR v1 为
  单模型，`Adapt` 继续对多模型 GraphPath 失败关闭，不在本任务中绕开该合同或静默接线。
- 单元与属性测试覆盖完整矩阵、非法组合、缺桥、NULL、AST/标识符注入和风险码；真实 PostgreSQL
  fixture 验证预聚合/bridge 结果与手写正确 SQL 等价。全新隔离数据库已从零回放全部迁移并通过
  `verify-database.sh`，验证完成后临时库已删除。开发库确认关系表无存量行后，在单事务内补齐已登记
  批次的 QUERY-008 DDL，并再次通过数据库门禁及真实数据库拒绝/认证失败关闭集成测试。

### 2.77 QUERY-007 排序、TopN、并列与 Other 编译（已完成）

- Semantic IR 与结构化理解合同已补齐 `rankBy=CURRENT_VALUE|DELTA|RATIO`、
  `otherPolicy=NONE|AGGREGATE_REMAINDER`、`tieBreaking=INCLUDE_ALL|DETERMINISTIC_CUT`；TopN 默认
  10、硬上限 1,000，执行结果上限继续独立保持 10,000。对比查询的排序依据不得缺省，维度排序只允许
  `CURRENT_VALUE`，排序目标仍只接受已选 metric/groupBy 的稳定版本 ID。
- `CompileSort`/`CompileLimit` 只消费 Release-pinned 目标到编译器列别名的绑定：并列全收使用
  `RANK() <= N`，确定截断使用分组稳定键参与的 `ROW_NUMBER() <= N`。伴随元数据 SQL 可返回
  `tiesIncluded`、`tiesCut` 与实际行数；ASC 与 DESC 共享同一边界，BottomN 不另开不一致逻辑。
- `CompileOther` 复用 `AggregationPlanner`：全可加指标生成 total-minus-top；半可加和不可加指标禁止
  相减，只能读取针对 `remainder_rows` 的重算关系。Other 行携带 `is_remainder=true` 与归并成员数，
  分组空值通过空的 typed projection 保持原列类型，不会把日期/数值键误推成 text。
- 规则层识别按当期值、增长额、增长率三种显式依据，并排除这些“按…”短语的分组误判。Binder 在
  TopN + comparison 缺少 `rankBy` 时，无论只有一个还是多个 Bundle 都强制返回三个证据绑定的澄清项，
  不允许置信度门槛静默默认。
- 单元测试覆盖非法排序目标、0/1001、默认 10、两种并列 SQL/标记、三类可加性、同比缺省澄清、
  DELTA 与 BottomN。PostgreSQL 集成 fixture 覆盖边界并列实际 3/2 行和 remainder 数值/成员数；未提供
  integration DSN 时按仓库惯例跳过外部数据库用例。

### 2.78 WEB-011 明细取数申请入口（已完成）

- 用户确认方案 3「我的申请主从工作区」后，`/ask-data` 顶栏增加“问数 / 我的申请”；申请列表、详情、
  六态进度、驳回原因、审计事件与交付优先级完整落地。所属领域固定为登录 session 的唯一业务域，
  侧栏、工作区和弹窗均只读显示且没有领域选择器。
- `web/src/askdata/api/dataRequest.ts` 对接 DR-001 真实 list/get/create/submit/transition；可申请字段只从
  当前领域 RLS 可见的 PUBLISHED 数据集版本加载。真实库字段为空时展示明确空状态并禁用提交，不把
  结果行或模拟字段混入正式路径。
- 新建弹窗支持主动入口和来源 run 预填。上下文清洗器只保留 metric/dimension/member UUID 与解析时间，
  丢弃 rows、result、SQL、answer 和未知扩展；有来源 run 时才发送上下文。敏感级按所选字段最高等级
  实时只读展示，真实会签门禁继续归属 DR-002。
- 申请状态按 `record_version` 和事件 `sequence_no` 展示；当前测试用户同时是申请人和领域管理员，因此
  `SUBMITTED` 提供后端真实支持的批准/驳回动作，不伪造状态机不存在的撤回。生命周期批准、开始处理、
  交付与关闭均通过真实/快照交互验证。
- 设计同状态对照、1280×720、1120×800、720×900、真实 API 空状态和完整交互通过。证据位于
  `design-qa.md` 与 `design-qa-artifacts/web-011-*`，最终结果 `passed`。

### 2.79 NLU-009 问题类型白名单与 OUT_OF_SCOPE（已完成）

- `internal/askdata/understanding/scope_lexicon.go` 冻结 15 类词表、结构阈值、版本和规范内容 hash；Classifier
  深拷贝 Release 固定配置。规则优先级避免“列出各区域销售额”被弱明细词误伤，只有规则无法确定时才
  调用 LLM 兜底，未知枚举或错误会记录 `RULE_FALLBACK_REJECTED` 并保留规则候选。
- `ScopeVerdict` 固定 type/outcome/reason/action 组合。`DETAIL_LIST` 指向平台内
  `DATA_REQUEST_DIALOG`，预测、临时公式、未启用贡献分解、跨域分别提供受控重述/指标建设出口，未治理
  数据源失败关闭。跨域说明遵循用户确认口径：领域在登录后已固定，问数页面内不提供切换器。
- NextAction 只保存 target 与 `CURRENT_QUESTION` 预填方式，不持久化原始问题。`ParsedContext` 复用
  DR-001 的 UUID/时间白名单；HTTP Block 工件公开投影严格拒绝未知字段、rows/SQL、动作篡改和不合法
  type/outcome 组合。
- `definition_card.go` 提供只依赖 Registry 的口径卡短路径，类型上没有查询执行器；固定零数据查询、
  最多 1 次 LLM，并验证 pinned metric version、显示安全公式与证据。评测累计正确拒答和错误拒答，
  只有类型匹配的 `OUT_OF_SCOPE` 才进入正确拒答分子。
- 前端 `ConversationOutcome` 读取公开 ScopeVerdict；只有 `SCOPE_DETAIL_LIST` 显示“发起明细取数申请”，
  点击后切换到已确认工作区并预填本地当前问题、来源 run 与受控上下文。应用内浏览器截图为
  `design-qa-artifacts/nlu-009-scope-detail-exit.png` 和 `web-011-scope-detail.png`。
- 15 类各 3 条（45 条）矩阵、定义零查询、非法 LLM 枚举、正确拒答统计、严格公开工件、前端出口、
  全仓 Go test/vet、32 条前端测试、lint/build 与 `git diff --check` 均通过。全仓 gofmt 门禁仍发现工作区
  中与本任务无关、尚未格式化的 `orchestrator/budget.go` 与未跟踪 `runner.go`；本任务未改写这些并行
  工作文件，新增/修改的 NLU/HTTP 文件本身 `gofmt` 通过。

### 2.80 ORCH-008 RunType 预算修订（已完成）

- `budget.go` 冻结 Fast/Complex 单查询、Bundle、Definition 四档预算；`RunBudget.Limits()` 映射到
  Question Run 标量上限。`BudgetCatalog` 按 domain/class 解析严格配置，覆盖只能在 4 LLM、10 Tool、
  6 正式查询、3 验证查询、30 秒、4 并发的治理包络内生效。
- Loop 请求可显式选择预算档，执行前把持久化限制与解析预算逐项取较小值。Fast/Definition 会真实减少
  模型、工具、查询与时间额度；默认仍为 Complex，现有 Run 与重放语义不变。
- `runner.go` 提供完整消费快照、单次 P95 指标、独立 HardTimeout 观察、按 PARTIAL → 证据澄清 →
  TIMEOUT 的确定性熔断结果，以及排除澄清等待的 ActiveBudgetClock。P95 越线不会设置 exhausted 或
  interrupt。
- `000246_askdata_run_budget_consumption` 不改写 `000244`：扩大 Bundle 所需的数据库治理包络，并以名称
  排在生命周期守卫之后的 BEFORE trigger，从权威单调计数生成所有状态的 `budget_consumed_json`。
  非澄清 Run 的 Go replay 会核对 JSON 与标量一致但不误当作冻结状态；澄清 child 继续复用相同精确快照。
- 严格配置、四档预算、领域收紧、20 分钟冻结、P95/HardTimeout 分离、熔断分流和完整 JSON 单测通过；
  迁移已在开发库应用，真实 app/admin PostgreSQL 创建/更新与消费快照一致，down→up 在回滚事务中通过。

### 2.81 RPT-CONTRACT-001 Report Definition v1（已完成）

- 新建 `api/schemas/report-definition-v1.schema.json`，所有声明为 object 的节点均显式
  `additionalProperties:false`，并以 `$defs` 复用 block/zone/slot/component。顶层必含 metadata、
  template/theme、固定画布、Data Context、全局筛选、页面树、顶层组件表、交互、运行策略和来源。
- 新建 `internal/report/model.go` 与 `decode.go`。`Decode` 先限制 5 MB、24 层、普通字符串 4096 字符、
  富文本 64 KB，拒绝 SQL/连接串/凭据字段/脚本/HTML 事件属性，再复用 askdata strict decoder 拒绝
  未知字段、重复键和尾随 JSON；`Validate` 可单独调用且仍执行同一内容守卫。
- 顶层组件表由 `slot.componentId` 引用，page/section/block/zone/slot/component 各自全局唯一；Validator
  同时校验页面/章节/分块/槽位/组件上限、画布和网格边界、Data Context/筛选/交互引用。
- 数据绑定严格二选一：`SEMANTIC_IR` 固定 Release ID/hash、完整 Semantic IR 与 Query Plan hash，且
  三者一致；`DATASET_FIELD` 只能引用定义内 Data Context 的逻辑字段。默认参数使用关闭的类型化数组，
  避免开放 JSON map 破坏全对象闭合约束；组件公共 options 由下一项 Manifest 按 type/version 再收窄。
- 新建 `simple-report.json`、`multi-page-report.json`、`ask-data-report.json` 三份契约示例与契约
  测试；全部示例解码后 marshal/decode 保持结构体相等，负测覆盖 TODO 指定的全部边界和悬空/重复引用。

### 2.82 RPT-CONTRACT-002 Component Manifest v1（已完成）

- 完整更新 `api/schemas/component-manifest-v1.schema.json`，冻结 renderer/category、网格尺寸、数据角色、
  optionSchema 子集、移动策略、交互和 migration。Manifest 顶层及所有固定子对象关闭未知字段；只有
  option 属性名和默认值表作为受控动态对象，并在 Go 中由同一 optionSchema 二次校验。
- 新建 `internal/report/template/manifest.go`、`registry.go` 与 13 个 `manifests/*.json`。注册表使用
  `go:embed` 直接加载唯一 JSON 真值，按 type@version 唯一索引并返回深拷贝，避免调用方污染全局配置。
- `OptionSchema.ValidateJSON` 同时服务默认选项和运行时组件；`ValidateComponent` 复用它并检查最小尺寸、
  维度/度量区间、DATASET_FIELD 角色与 SEMANTIC_IR 数量/时间要求。三份 Report Definition 示例的所有
  被引用组件已逐一通过 Manifest 兼容验证。
- `CheckUpgrade` 对 patch/minor 执行向后兼容门禁，对 major 要求精确上一版本和注册 migrator；NewRegistry
  按同组件版本排序并逐相邻版本检查，CI 不会静默接受破坏性升级。

### 2.83 RPT-CONTRACT-003 Report Operation v1（已完成）

- 新建 `api/schemas/report-operation-v1.schema.json`：41 个操作各自使用 `oneOf`、`op.const` 和专属/复用
  的关闭 payload Schema，所有操作 envelope 都只允许 op/targetId/payload；UNDO/REDO 不在枚举内。
- 新建 `internal/report/operation/model.go`：Bundle 上限 USER/IMPORT/SYSTEM 100、AI 30；41 个 payload
  都是具体 Go 类型。解码逐层核对必填字段，未知字段、错 payload、空操作、非法 source/ID/revision
  在应用前拒绝。
- 新建 `guard.go`：AI 必须提供 aiRunId/scope；模板应用、页删、章节删和超过 5 个删除返回
  `REPORT_OP_NOT_ALLOWED_FOR_AI`。scope guard 从当前定义构造完整祖先索引并核对 reportId、scope 真实
  层级和 targetId 全部路径，越界返回 `REPORT_OP_OUT_OF_SCOPE`。
- 41 项正例和 41 项 payload 负例、100/30/5 上限、AI 禁止操作、跨页越界、精确 Block 内放行、Schema
  41 分支与 Go 枚举对齐均已自动化。

### 2.84 QUERY-009 Query Plan Bundle 多计划运行（已完成）

- `api/schemas/query-plan-bundle-v1.schema.json` 与 `compiler.QueryPlanBundle` 冻结同序合同：完整
  PolicyScope、Release ID/hash、共享 resolved time/filters、1～6 个按 `p1...p6` 排序的独立 Semantic IR、
  role/chart type、逐项 IR hash、1～4 并发和整体 Bundle hash。合同不包含 SQL、参数或结果行。
- `BuildQueryPlanBundle` 只接受 CERTIFIED KPI Bundle；先重算 Release Manifest 并与 scope 的 Release hash
  精确对拍，再证明 Bundle ReleaseObject、每个 metric→model 绑定、group/filter/time dimension 均存在于
  同一 manifest。DRAFT/临时组合稳定返回 `BUNDLE_NOT_CERTIFIED`；计划超过 6、输入指标合同超过 8 或
  并发超过 4 均失败关闭。
- 每个 KPI item 确定性展开为单指标 Semantic IR，HEADLINE 使用 limit=1，其余沿用 TopN=10；时间区间、
  比较和筛选来自同一共享上下文，时间 group-by 才附着粒度。全部 IR 规范化并独立 hash，任何计划、
  Release、Policy 或 chart 篡改均不能通过 replay。
- `BundlePipelineProcessor` 明确串接 `CompileBundlePlan → ValidateCovered → Execute`。编译产物必须重新匹配
  scope/domain/IR/resolved time，Validation 与 Execution hash 必须逐层闭合；每个 plan 从根 run UUID
  确定性派生独立执行 UUID，因此并发执行不会复用校验或撞 active-run 门禁。
- `BundleRunner` 使用 BUNDLE 预算、`errgroup.SetLimit` 和 30 秒共享 deadline，最多四项同时执行；单项
  编译、校验、执行、权限或超时失败只影响该项，不取消兄弟计划。结果始终保持输入顺序并只公开稳定
  failure code；全部成功为 `ANSWERED`，任一失败为 `PARTIAL`，硬超时保留已完成工件。
- 测试覆盖 3 项全成功、执行失败、权限裁剪、6/8 上限、并发峰值 4、硬超时后保留完成项、DRAFT 拒绝、
  Release/model 漂移、Schema 上限和 hash 篡改；专项 race 无数据竞争。本任务无迁移、页面或外部调用。

### 2.85 QUERY-011 PARTIAL 触发条件与结果状态分流（已完成）

- 新建 `internal/askdata/validator/outcome.go`，冻结 `query-outcome-v1`、P1～P6/Q1 Evidence 和
  `outcomeHash`。时间 coverage 必须来自 sealed verdict；权限裁剪只接受请求/授权数量；Bundle、成员策略
  与多源条件都要求仍有非空成功子集。全失败/全过滤、重复 ID、阻断质量规则和篡改 hash 均失败关闭。
- `DetermineOutcome` 固定按 P1～P6、Q1 累积并规范排序。任一 P 命中为 `PARTIAL`；只有非阻断质量告警
  时为 `QUALITY_WARNING`；两者并存仍为 `PARTIAL`，同时保留 `qualityWarnings[]`。公开合同不含无权限
  指标名称或 ID。
- 新增受保护的 `/api/v1/questions/{runId}/add-to-report`。入口要求 Idempotency-Key、规范 report UUID
  和精确 runVersion，只从 actor-scoped ANSWER completion artifact 读取已校验 outcome；客户端不能自报
  ANSWERED。`PARTIAL` 在报告后端调用前返回 `409 RESULT_PARTIAL_NOT_EXPORTABLE`，提示缩小范围或确认后
  重跑；非 PARTIAL 才进入独立报告 bounded-context 接口。
- 本任务只冻结并执行导出门禁，不伪造尚未实现的 report intent/outbox。真实报告持久化接线缺失时，
  完整结果明确返回 `REPORT_ADD_UNAVAILABLE`；后续报告任务实现同一接口即可复用门禁。
- 测试覆盖 P1～P6、Q1、P1+Q1、P2 不泄漏、矛盾子集、PARTIAL 拒绝、Q1 放行、客户端 outcome 注入拒绝
  和可信 outcome hash 传递；专项 race、全仓 Go、vet、CI 与 diff 检查全部通过。无迁移、页面或外部调用。

### 2.86 ANS-001 Answer Artifact 与引用合同（已完成）

- 新建 `api/schemas/answer-artifact-v1.schema.json` 与 `internal/askdata/answer/model.go`。Schema 和 Go
  同时关闭未知字段；结构化层固定 headline/cards/chart/tableRef，指标值只允许 decimal 字符串。strict
  decoder 拒绝重复键、尾随 JSON 和错类型，Normalizer 固定 nil/空数组并按 citation span 排序。
- citation 使用 `[start,end)` Unicode code-point 区间，统一指向 `summary + 换行 + findings` 的规范文本；
  越界、空区间和重叠均拒绝。RESULT_CELL、CONTRACT、TIME_SPEC 是关闭联合，其中结果单元格直接复用
  `internal/askdata/shared.CellRef`，rowKey 以 Query group-by 顺序规范百分号编码为 `key=value|...`，可逆
  解析且拒绝重复键、非规范转义和分隔符碰撞。
- Prompt/模型、校验器/词表、Evidence/Result hash、Semantic Release 和图表规则统一投影到
  `shared.Provenance`，与报告共用一个 fail-closed `IsStale`。降级工件强制 narrative 为空且
  `passed=false/degraded=true`，因此未校验叙述不能伪装成完整 Answer。
- 复用既有 `askdata.question_artifacts` ANSWER 类型和不可更新/删除 trigger。PostgreSQL integration 现写入
  真实 Answer Artifact、重放后 strict decode，并在管理员嵌套事务内验证原地 UPDATE 被数据库拒绝。

### 2.87 RPT-CONTRACT-004 Evidence Bundle 与 Insight Artifact（已完成）

- 新建 `api/schemas/evidence-bundle-v1.schema.json`、`insight-artifact-v1.schema.json` 与
  `internal/report/insight/model.go`。Evidence Bundle v1.1 固定两类来源、Dataset/Snapshot/Query/Filter、
  asOf、实际半开时间区间、分析方法/版本、证据算法、事实、质量告警和 generatedAt；语义来源要求 Release/
  IR hash 非空，直接 Dataset 查询要求两者为 null。
- Fact 的 current/previous/change 全部是规范 decimal 字符串，禁止 float/指数；PERIOD_COMPARISON 强制
  基期和变化率成对存在。facts.cellRefs 与 Answer citations 是同一个 `shared.CellRef/Citation` Go 类型，
  契约测试从 Evidence cellRef 构建 Answer citation 再解析回同一坐标。
- Evidence Bundle 规范 JSON 计算内容 hash。Insight Artifact 精确绑定该 hash，固定 Prompt/模型/校验器/
  词表与 CURRENT/STALE/FAILED；人工编辑为 true 时编辑人/时间必填，为 false 时二者必须 null，FAILED
  内容与引用必须为空。
- 九项指定 stale 因素及词表/Evidence hash 全部通过 ANS-001 的同一个 `shared.IsStale`，没有报告侧副本。
  两份 Schema round-trip、两种 source、九项失效、float 拒绝、坐标互解析、人工编辑、hash 篡改与未知字段
  均已自动化。

### 2.88 ANS-002 叙述事实校验器（已完成）

- `internal/askdata/answer` 已形成 `extractor → matcher → verifier` 的确定性边界：抽取中文/阿拉伯数值、
  万/亿、百分比/百分点、时间、单位、Binding 对象和禁用断言，所有 span 都使用 ANS-001 的 Unicode
  code-point 坐标。对象采用最长匹配，已知但未绑定的指标/维度/成员直接报幻觉。
- 数值比较使用 `math/big.Rat` 和显示精度容差；只接受引用单元格或显式声明的差、比、百分比、占比、
  同比派生，绝不搜索任意 cell 组合。时间只接受 resolvedTimeSpec 当期/对比期，单位从 RESULT_CELL 或
  Metric CONTRACT 的 unit/currency 核对。
- 校验器与 `wordlist/v1.json` 均按 Release 版本固定，因果、预测、外部事实、越界建议默认拦截；贡献分解
  模式只放行治理词表中的弱化表达，强因果仍失败。六个错误码输出元素、原文、Unicode span、原因和期望。
- Answer 的 Result hash/Semantic Release 与验证证据强绑定；报告 `VerifiableInsight` 先验证 Evidence hash、
  状态和人工编辑标记，再进入同一个 `Verifier.VerifyNarrative`。契约测试证明同一 Evidence 在问数和报告
  链路返回完全相同的 `VerifyReport`。
- 测试覆盖每个错误码至少三条负例、中文数值等价、百分比/百分点、容差边界、同比派生、随机组合反例、
  幻觉对象与贡献模式；专项 race、全仓 Go test 与 vet 均通过。无迁移、页面、外部调用或业务数据写入。

### 2.89 ANS-003 三层答案与失败降级（已完成）

- `internal/askdata/answer/composer.go` 最多生成两次 L2；首次失败只向重试传递去原文、去 span 的结构化
  失败码/期望，第二次失败统一调用 `ToStructured` 清空 narrative/citations，写入稳定降级提示。
  `DefaultAskDataInterpretationEnabled=false` 固定 Ask Data 不生成 L3，报告侧若显式开启仍必须走 ANS-002。
- `AnswerVerificationRunner` 强校验 actor/domain/policy/release/result hash 与 input artifact/result/binding，
  执行 `RESULT_VERIFYING → ANSWER_VERIFYING → ANSWERED`；逐次失败审计只含 code/count/version，completion
  分为 `ANSWER_VERIFIED`、`ANSWER_DEGRADED`。迁移 `000247` 同步 PostgreSQL 状态与事件约束。
- Question HTTP 公开投影只返回已核验 L2 或稳定 L1 提示；SSE 严格接受
  `answer.verifying/answer.degraded` 对应状态。降级但非 PARTIAL 的持久化 outcome 仍通过 add-to-report，
  未核验文字不会进入报告输入。
- 用户确认方案 2。前端新增 `AnswerSummary`、L1/L2/L3 层级状态、蓝灰降级说明、重新生成和查看校验依据；
  登录领域继续只读锁定。前端快照、SSE、浏览器主要交互与控制台均通过；1280×720 同状态完整/聚焦对照
  和 1120×800 响应式证据记录在 `design-qa.md`，最终结果为 `passed`。

### 2.90 ORCH-007 ANSWER_VERIFYING 阶段接入状态机（已完成）

- 复用 ANS-003 的 `AnswerVerificationRunner` 把结果验证后的唯一成功出口固定为
  `RESULT_VERIFYING → ANSWER_VERIFYING → ANSWERED`；`EXECUTING → ANSWERED` 等跨级路径由 Go 状态矩阵和
  PostgreSQL 约束双重失败关闭。
- 首轮失败以去原文、去 span 的结构化元素/原因码/期望证据驱动唯一一次重生成。连续失败只提交
  `ToStructured` 生成的 L1；失败事件详情不含叙述、prompt 或结果行，终态只可能是
  `ANSWER_VERIFIED` 或 `ANSWER_DEGRADED`。
- 答案模型余量同时受 LLM call、step 与 hard duration 限制，且上限为两次。预算已耗尽时不调用模型；
  只剩一次调用时失败后不再重生成，直接降级并持久化 `usage.exhausted=true`。
- SSE 事件严格按持久化 index 输出 `answer.verifying`、失败 checkpoint 与 `answer.degraded`；从任意已确认
  `Last-Event-ID` 恢复均只发送后继事件，保证顺序与去重。

### 2.91 ORCH-009 幂等键中间件（已完成）

- `internal/platform/idempotency` 是 Ask Data、Data Request 与 Report V2 共用的唯一 HTTP 幂等边界；
  Question API 与 `POST /api/v1/data-requests` 已在认证之后接线，`internal/report/http.WithIdempotency`
  冻结报告侧复用入口。尚未开放的 release activate、report operations/publish 只预留同一 allowlist，
  本任务不越权开放其业务路由。
- body hash 基于拒绝重复 key/尾随值/过深嵌套后的规范 JSON；同一 actor/endpoint/key 的字段顺序差异可精确
  重放，内容变化返回 `IDEMPOTENCY_KEY_REUSED`。并发抢占只允许一个请求执行业务，其他请求得到
  `IDEMPOTENCY_IN_FLIGHT`。
- 2xx/4xx 的有效 JSON status/body 由 `000248_askdata_idempotency_records` 保存 24 小时；读取时重新计算
  response hash。5xx、panic、超时或不可重放响应释放 IN_FLIGHT，不会永久锁死 key。
- actor-scoped RLS 阻止跨 tenant/actor 读取。API 角色可抢占/完成/释放自身记录；Worker 只能读取并按
  `(expires_at,id)` 有界删除到期批次，后台主循环已接入，未到期 COMPLETED 由 trigger 禁止删除。

### 2.92 RPT-DB-001 报告主对象、草稿、修订与版本（已完成）

- `000234_report_v2_core` 建立 Report V2 主对象、每报告唯一草稿、不可变修订链与不可变发布版本；
  `internal/report/store.Repository` 提供创建、读取、乐观锁保存、修订列表和版本读写的统一边界。
- 草稿保存先锁定当前 draft，再比较 expected revision；成功路径在同一事务更新 draft、追加连续 revision
  并重建索引。并发使用相同 base revision 时只有一个提交，冲突返回权威 current revision 与最多 100 条
  operation 摘要。
- 四张核心表 FORCE RLS 同时校验 tenant、选定 domain 和报告对象权限。owner、平台/租户/领域管理员及
  USER/ROLE VIEW/EDIT/PUBLISH grant 按动作生效；VIEW-only 可读不可写，跨 tenant 无法观察对象。
- revision 内容和 version 定义/删除由数据库 trigger 拒绝。JSONB 读回会重新规范化并核对持久化 hash，
  不把 PostgreSQL 重排后的非 canonical 文本误当发布工件；约 4.9 MiB 定义已验证完整写入和读回。

### 2.93 RPT-DB-002 模板、主题与组件模板（已完成）

- `000235_report_v2_templates` 建立结构、布局、主题、叙述、报告组合与组件模板的 12 张 FORCE RLS 表；
  报告模板版本以四个外键固定子模板版本，不把四类模板压成一个不可治理的大 JSON。版本字段执行严格
  SemVer，`1.10.0` 按数值高于 `1.9.0`，前导零等非法版本在 Go 与数据库两侧失败关闭。
- `PostgresTemplateStore` 支持按 `(reportTemplateId, version)` 或精确不可变 version ID 解析组合；主模板
  与四个子版本必须处于已发布/已弃用/保留态。五份 JSONB 读回后重新规范化并分别校验 content hash，
  跨 tenant 不暴露对象是否存在。
- 13 个内置组件由同一 embedded registry 驱动编译器与数据库 seed。迁移只建立显式 placeholder，API
  启动时在一个事务内替换为完整 manifest；二次执行只核对 type/version/hash，不覆盖任何真实内容漂移。
  `000256` 为已经应用旧 000235 的数据库补齐同一 hydration 触发器和 SemVer 约束。
- 组件版本只允许 `ACTIVE → DEPRECATED → RETAINED`，manifest、hash、version、migrator 与所属模板身份
  均不可变。`000236` 的 dependency 索引删除保护按 `type@version` 查询，已被发布报告引用时返回
  `REPORT_TEMPLATE_IN_USE`，不扫描 Report Definition JSON。

### 2.94 RPT-DB-003 组件索引与依赖索引（已完成）

- `compiler.BuildIndexes` 从已校验的 Report Definition 确定性投影组件位置与全部版本依赖。每个组件必须
  恰好落在一个 slot；声明但暂未被组件消费的 dataContext 仍写入 DatasetVersion 依赖，避免影响分析漏掉
  报告级过滤/预取上下文。同一依赖只保留一行，`component_ids` 与组件索引均稳定排序。
- 草稿保存时，definition、revision 和“先删后插”的草稿索引位于同一事务；任何一步失败或显式回滚都
  不留下半套索引。发布版本的组件/依赖索引随 version 同事务插入，随后 UPDATE/DELETE 由不可变 trigger
  返回 SQLSTATE 55000。
- `report-admin rebuild-indexes` 接受 tenant/actor/domain/report 四个显式坐标并复用对象权限。命令锁定
  report/draft 后重建并逐行核对草稿索引；版本索引完整时只验证，整组缺失时从不可变 definition 回填，
  部分缺失或内容不一致失败关闭，不用“修复”覆盖取证证据。
- 四张索引表全部 FORCE RLS，并调用 `report_v2_can_access` 区分 VIEW/EDIT/PUBLISH，不再只按 tenant 放行。
  `000261` 为已应用早期 `000236` 的安装补齐同一对象策略和 tenant-aware 组件引用删除保护；影响分析使用
  `(tenant_id, dependency_type, dependency_id)` 索引，全程不扫描 definition JSON。
- 从空库顺序执行全部迁移的 nonce 数据库测试覆盖真实 `report_app` owner/VIEW-only/跨 tenant、事务回滚、
  发布、增量重建、版本索引回填和不可变保护，退出时强制删除临时库。随机定义属性测试另执行 200 组，
  覆盖索引完整性、去重与排序。

### 2.95 RPT-DB-004 AI 运行、证据与结论工件（已完成）

- `reportai.PostgresStore` 支持 `PLAN`、`GENERATE_DRAFT`、`SCOPED_EDIT`、`INSIGHT` 四种运行，数据库
  强制运行只能从 RUNNING 单向进入 SUCCEEDED/FAILED/REJECTED，终态记录不可重写或删除。请求摘要使用
  闭合字段白名单和字符串/数组类型检查，不保存完整 prompt、数据样例、原始值或 transcript。
- AI 操作全部追加留痕；REJECTED 必须带稳定拒绝码。VALID 操作创建时不得声称已应用，仅在成功运行的
  草稿提交事务中允许一次性写入正数 applied revision，之后操作内容、校验结果和应用修订均冻结。
- Evidence Bundle 为不可变快照。Insight 生成、重生成和人工编辑均追加新行，并在同一事务把旧 CURRENT
  置 STALE；人工编辑保留编辑者和时间。Artifact 合同稳定 ID 与数据库版本行 UUID 分离，避免重生成
  覆盖历史或触发行主键冲突。
- 四张表均 FORCE RLS。AI run 额外按发起 actor 隔离；Evidence/Insight 使用报告 VIEW/EDIT 对象权限，
  tenant 和当前已选 domain 仍为不可绕过边界。`000257/000261/000265` 为早期 `000237` 安装补齐摘要
  完整性、对象策略和生命周期 trigger。
- 真实 PostgreSQL 测试覆盖四种 kind、拒绝码、摘要白名单、VIEW-only/跨 tenant、终态/证据不可变、
  两次生成的 CURRENT/STALE 切换及人工编辑追加留痕；迁移上下行事务演练、专项 race 和全仓回归均通过。

### 2.96 RPT-DB-005 无匿名分享记录（已完成）

- Go 与数据库把分享类型闭合为 INTERNAL_USER/INTERNAL_GROUP/EXTERNAL_ACCOUNT；没有 PUBLIC/ANONYMOUS，
  EXTERNAL_ACCOUNT 在 MVP 固定拒绝。创建要求报告 EDIT 权限，默认期限 30 天、数据库上限 180 天。
- 32 字节随机 token 只在创建响应返回一次，库内仅保存 SHA-256 hash，Record JSON 不暴露 hash。
  AccessShare 先拒绝匿名，再用 hash 定位，随后核对 user/group principal 和报告 VIEW 权限，最后始终以
  viewer 身份读取固定版本或当前发布版本；令牌不承载授权，也不能绕过 tenant/domain。
- 分享行仅允许访问计数、创建者撤销、SYSTEM 过期标记三种单用途更新；报告/版本、principal、token hash、
  过滤快照和期限不可变。撤销提交后调用缓存失效合同，访问路径实时检查期限，所以 Worker 延迟不影响拒绝。
- `ExpiryWorker` 按 tenant 及 `(expires_at,id)` 有界索引，以最多 500 行和 SKIP LOCKED 标记 `expired_at`；
  app/worker 都不具备直接执行主体校验或生命周期 trigger 函数的权限。
- 真实 PostgreSQL 覆盖用户/用户组、无报告权限不提权、跨 tenant、实时与后台双重过期、撤销、访问计数、
  非法主体、180 天 CHECK 和不可变字段。`000267` 另前向修复早期 report version trigger 错绑，恢复合法
  artifact READY 推进而不放宽定义不可变边界。

### 2.97 RPT-001 DSL 规范化、校验与哈希（已完成）

- `NormalizeWithRegistry` 不修改调用方对象，固定执行字符串/枚举规范化、Schema/Manifest 默认值合并、
  nil 集合空数组化、语义无关集合稳定排序、分阶段校验、规范 JSON 与 SHA-256；默认 Registry 使用
  13 个 embedded Manifest，显式 false 等指针值不会被模板默认覆盖。
- ID、引用、结构/布局、Manifest/option schema、数据角色五个阶段之间短路；每个阶段内部累积所有问题，
  一次返回稳定 code/path/message 集合。交互目标先排序，再按 source/event/targets/action/id 排序，页面、
  分块、组件、筛选、参数、映射与 provenance 引用均有稳定键。
- 规范 JSON 把所有对象转换为字典序键、保留 `json.Number` 精度、移除多余空白并限制为 5 MiB；hash 只对
  最终 UTF-8 字节计算。V1 minor 兼容读取统一落为 1.0；大版本必须走 `migrate/v1_to_v2` 显式适配器，
  适配器在深拷贝上运行并拒绝缺失迁移器、错误版本与超限输出。
- 富文本清洗允许有限排版/链接标签和安全属性，拒绝危险 URL 协议，删除 script/style/iframe/svg 等节点
  及其内容；`target=_blank` 自动补 `noopener noreferrer`，属性稳定排序。Decode 和 Normalize 共用清洗器，
  清洗后的字节参与 hash。
- 大定义安全扫描用字符级无损候选门禁后再运行 SQL/连接串/脚本正则，近 5 MB 基准在 Apple M4 上 5 次
  平均约 51.6 ms；属性、分阶段负测、XSS、显式迁移和专项 race 均通过。

### 2.98 RPT-002 Operation、逆操作与并发控制（已完成）

- 41 类协议操作全部由 `operation.Apply` 在深拷贝上逐项执行，失败返回 index/code/message 并丢弃工作副本；
  `ApplyAndValidate` 复用 RPT-001 的完整规范化、布局、Manifest/options、dataContract 与 hash 路径。
  BLOCK_COPY 复制完整嵌套值，避免派生 zone/slot ID 时污染源分块。
- `Invert/InvertBundle` 在每个操作的准确前态上生成逆操作并反序排列。删除携带删除前快照，更新/移动/
  resize/reorder 恢复旧值，merge/split 恢复原 slot 映射；模板和主题一律生成整定义 snapshot 恢复，避免
  模板副作用无法逆转。空 insight 前态通过 COMPONENT_UPDATE 恢复，不构造协议禁止的空 Insight payload。
- 存储顺序固定为 EDIT 授权、AI 独立能力、baseRevision、AI scope、纯函数应用/校验、规范 hash、同事务
  同步 reports identity、更新唯一草稿、追加修订、重建索引和 AI 审计 applied revision。Report ID 不可由
  REPORT_CREATE 替换；任何后续失败都会回滚主对象、草稿、修订和索引。
- `REPORT_AI_EDIT` 映射为租户权限 `report.ai_edit/REPORT/AI_EDIT`，由 `000269` 为内置平台/租户/数据管理
  角色登记；对象 REPORT_EDIT 与该能力必须同时满足。Handler 不再在授权前读取/预演定义；只有权限和
  baseRevision 均通过后的 scope/内容拒绝进入 AI rejection 审计。
- 409 响应包含 expected/current revision 与自 base 以来最多 100 条 `rN:OP,...` 摘要；ApplyError 包含失败
  operationIndex。Undo/Redo 修订只追加，双栈校验 inverse_of_revision_no，支持多级撤销/重做，普通新编辑
  清空 redo 分支，损坏链立即失败关闭。
- 单元测试覆盖 41 类应用/逆操作、120 步随机序列全撤销、失败原子性、整快照、slot ID 和双栈；真实
  PostgreSQL 覆盖 AI 权限/scope、无修订拒绝、连续 Undo/Redo、设置同步、模板恢复、并发一成功一 409、
  RLS 和 revision 不可变。

### 2.99 RPT-003 Go/TypeScript 确定性布局引擎（已完成）

- `compiler/layout.go` 完成 24 列边界、运行时像素换算、NONE/VERTICAL 深拷贝紧凑、四种区域高度和
  显式模板优先级重分配；桌面碰撞由 x 扫描线 + AVL y 区间树实现，复杂度 `O((n+k) log n)`，边接触不
  误报，输出稳定排序。
- Report Slot 允许省略 componentId 作为设计期空槽。Merge 同区、连续矩形、至多一个非空组件与
  Manifest minSize 四道门禁使用固定错误码；服务端推导几何、组件和排序后的 mergedFrom，Split 必须
  与 provenance、几何及组件同时一致。正常外部 payload 不能清空 provenance。
- Mobile 按 order/visible 独立转成全宽 block，AUTO/FIXED/ASPECT_RATIO 和 STACK/CAROUSEL/
  PRIMARY_ONLY/COLLAPSE 均有确定性结果；PRIMARY_ONLY 只能指向可见非筛选槽位，其他槽位不进入查询，
  筛选区进入 drawer，Manifest 图例/标签降级策略随组件下发。
- `web/src/report/designer/layout/` 实现同规则实时预览库；Go 与 TypeScript 共用
  `api/examples/report-layout-contract-v1.json`，对碰撞和 merge 错误码做双端契约测试。Schema 补齐
  primarySlotId/mergedFrom 条件约束，多页示例已同步。
- 随机暴力碰撞对照、300 分块 `<10ms`、四高度、模板切换、四 slotMode、PRIMARY_ONLY 零隐藏查询、
  merge/split 完整恢复、Manifest mobilePolicy 和双端 fixture 均通过。

### 2.100 RPT-007 数据绑定双模型与查询编排（已完成）

- Schema 与 Go runtime 共同执行严格 `SEMANTIC_IR` / `DATASET_FIELD` 二选一。六类编译期拒绝使用稳定
  错误码；语义 model/metric/dimension/member 均核对认证事实，单位/币种在运行时与 PostgreSQL 发布
  校验两处检查。`RETAINED` 可重编译，`RETIRED` 和 content hash 漂移拒绝。
- 语义查询固定 Release ID/hash、IR 与 Plan hash，由受治理 runner 重新编译或读取固定 Artifact；Dataset
  查询只携带逻辑字段、筛选、参数与 limit，Report 边界无 SQL，并标记 `uncertifiedDefinition=true`。
- QueryHash 包含 policy scope；同哈希组件合并，不同查看者不合并。默认并发 8、硬上限 16，超时与最大
  行数失败关闭。当前查看者 context 每次传入 Semantic/Dataset runner，发布权限不替代查看权限；
  `NO_PERMISSION` 不携带结果或绑定详情。
- 单测覆盖六类拒绝、RETAINED/RETIRED、固定身份篡改、三组件单执行、策略隔离、低权限结果裁剪、无
  SQL、并发、超时与行数上限；全仓 Go test/vet 和数据库验证通过。

### 2.101 RPT-004 发布编译与不可变制品（已完成）

- `publication.Publisher` 按固定 14 步执行，支持选择精确历史 revision；Schema/领域/Manifest/权限依赖/
  布局与双端预览/交互/Insight 分阶段失败关闭，规范化与索引在数据库写入前后独立复核。发布后只消费
  immutable version，不回读草稿。
- Report Definition provenance 新增可选的分析方法 SemVer、Prompt version 与模型策略固定字段；依赖索引
  增加 `ANALYSIS_METHOD/PROMPT_VERSION/MODEL_POLICY`。旧 v1 工件未携带这些可选字段时 canonical
  hash 不变，避免破坏历史重放。
- 对象存储临时键与正式 `object_uri` 统一保留 `report-v2/` 前缀。DB 失败会删除 `.tmp`；Promote 或
  pointer 失败保留可恢复状态，补偿 Worker 按已提交 `object_uri` 幂等推进。请求幂等 hash 包含 revision、
  双端预览 hash 和发布选项；重放不产生新版本。
- `000272_report_publication_version_pins` 为不可变版本的 `SEMANTIC_RELEASE` 依赖自动写入
  `askdata.release_references(reference_type='REPORT_VERSION')`。真实 PostgreSQL 集成验证 ACTIVE 发布被
  引用后，SUPERSEDED 自动转 RETAINED，活跃引用阻止 RETIRED；安全定义器不向应用/Worker 角色开放。
- 14 步失败矩阵断言版本行/对象副作用边界；另覆盖历史修订、STALE 默认拒绝/显式确认审计、固定字段、
  幂等、DB 清理、对象 Promote 故障和补偿修复。无新增页面或显著视觉状态，未触发设计确认门禁。

### 2.102 RPT-005 重新发布式回滚（已完成）

- `POST /api/v1/reports/{id}/rollback` 已接入发布权限与 actor-scoped 幂等；原因必填、trim 后最多 1000
  Unicode 字符且禁止控制符，目标必须为同报告 READY version。回滚仍执行发布第 3～14 步，不存在
  force/bypass 分支。
- 新 version 记录固定 `rollback_of_version_no` 与 `rollback_reason`，沿用临时对象、事务版本、Promote、
  pointer 和补偿恢复流程。回滚的回滚只是在更高 version 上引用上一次回滚 version，历史 definition/hash
  从不更新。
- API 对依赖/Manifest/布局等重校验失败返回结构化 `issues`。存储事务再次核对目标状态、definition hash
  与 source revision，修复了真实幂等回执 INSERT 中 UUID/text 参数推断冲突。
- `000273_report_rollback_integrity` 增加 `(report_id, rollback_of_version_no)` 自引用外键，以及原因 trim/
  长度/控制字符 CHECK。真实 PostgreSQL 覆盖正常指针切换、历史不可变、伪造 definition 拒绝、缺失目标
  外键和 Release 依赖重放。

## 3. 验证记录

在仓库根目录执行：

```sh
gofmt -w $(rg --files internal/askdata -g '*.go')
go test ./...
ENV_FILE=.env.example ./scripts/check-compose.sh
./scripts/ci-check.sh
```

结果：全部通过。

2026-08-08 `NLU-008` 验证：

```sh
go test ./internal/askdata/http ./internal/askdata/orchestrator -count=1
ASKDATA_INTEGRATION_DATABASE_URL='postgres://report_app:...@127.0.0.1:5432/...' \
ASKDATA_INTEGRATION_ADMIN_DATABASE_URL='postgres://report_admin:...@127.0.0.1:5432/...' \
  go test ./internal/askdata/orchestrator \
  -run 'TestQuestionStateMatrixMatchesPostgres|TestPostgresStoreQuestionLifecycleResumeAndPinnedRelease' \
  -count=1 -v
go test ./... -count=1
go vet ./...
npm --prefix web test
npm --prefix web run lint
npm --prefix web run build
./scripts/ci-check.sh
./scripts/verify-database.sh
git diff --check
```

- 全部通过；前端共 26 条测试。PostgreSQL 集成覆盖首轮 Pin、Release 漂移不静默切换、显式确认、
  澄清冻结/恢复、超时终态、追加事件重放与历史精确幂等重放。
- `000244` 已真实应用，并用同一事务执行 down→up→rollback；down 完整恢复旧状态机/触发器函数，
  schema、RLS、API 会话权限和 Worker 列级过期权限验证通过。
- 应用内浏览器完成主操作确认重跑、次操作查看历史、控制台和 1280/1120/720 响应式检查；视觉对照
  记录在 `design-qa.md`，最终结果为 `passed`。

2026-08-08 `DR-001` 验证：

```sh
go test ./internal/datarequest -count=1
ASKDATA_INTEGRATION_DATABASE_URL=<local-app-dsn> \
ASKDATA_INTEGRATION_ADMIN_DATABASE_URL=<local-admin-dsn> \
  go test ./internal/datarequest \
  -run TestPostgresStoreActiveRequestLifecycleAndRLS -count=1 -v
go test ./... -count=1
go vet ./...
./scripts/ci-check.sh
./scripts/migrate.sh
./scripts/verify-database.sh
./scripts/verify-warehouse.sh
git diff --check
```

- 全部通过。真实 PostgreSQL 用例覆盖无拒答主动申请、空预填边界、PUBLISHED 字段验证、跨用户 RLS、
  身份与数据库 context 不一致、乐观锁和 `DRAFT → ... → CLOSED` 六步闭环；六条事件序号严格递增。
- `000233` 与 `000245` 已真实应用；`000245` 完成 down→up→rollback。另用 `template0` 创建全新隔离库，
  从零回放全部迁移并通过 `verify-database.sh` 后删除；当前开发库及 warehouse 验证也通过。
- 本任务没有新增页面、没有调用外部模型、没有写入正式业务数据；集成夹具全部在回滚事务中清理。

2026-08-08 `QUERY-010` 验证：

```sh
go test -race ./internal/askdata/queryruntime ./internal/materialization
gofmt -w $(rg --files internal/askdata -g '*.go')
go test ./internal/askdata/...
go test ./...
./scripts/migrate.sh
./scripts/verify-database.sh
./scripts/verify-warehouse.sh
go vet ./...
test "$(gofmt -l cmd internal | wc -l | tr -d ' ')" = "0"
git diff --check
```

- 全部通过。数据库门禁确认既有 `000230` snapshot 完成触发器、通知合同、RLS 和仓库边界仍有效；
  本任务没有新增或回放额外迁移。
- 256 组随机 PolicyScope 属性检查没有碰撞或跨权限命中；主动失效用真实缓存实例消费完成 payload，
  不等待 TTL 即 miss。专项 race 未发现数据竞争。

2026-08-08 `QUERY-008` 验证：

```sh
go test ./internal/askdata/... -count=1
go vet ./internal/askdata/...
ASKDATA_INTEGRATION_ADMIN_DATABASE_URL=<local-admin-dsn> \
  go test ./internal/askdata/compiler \
    -run TestPostgresPreaggregateAndBridgeSQLMatchHandwrittenCorrectResults -count=1 -v
POSTGRES_DB=<isolated-query008-db> ENV_FILE=/dev/null ./scripts/migrate.sh
POSTGRES_DB=<isolated-query008-db> ENV_FILE=/dev/null ./scripts/verify-database.sh
git diff --check
```

- 全部通过。隔离库从空库应用全部迁移，数据库门禁确认 nullable 人工复核状态、无 fanout 默认、四项
  CHECK、bridge FK 和矩阵定义；验证结束后使用明确库名强制删除临时库。
- SQL 等价 fixture 覆盖一对多右侧重复行及桥表重复边，生成 SQL 与手写正确 SQL 的行数和聚合总值
  一致；开发库 integration 还验证非法组合与缺桥分别命中命名 CHECK、NULL holding row 无法认证。
  默认测试不依赖外部数据库，未设置 integration DSN 时仅跳过 PostgreSQL 用例。

2026-08-08 `QUERY-007` 验证：

```sh
test "$(gofmt -l internal/askdata | wc -l | tr -d ' ')" = "0"
go test ./internal/askdata/... -count=1
go vet ./internal/askdata/...
git diff --check
ASKDATA_INTEGRATION_ADMIN_DATABASE_URL=<local-admin-dsn> \
  go test ./internal/askdata/compiler \
    -run TestPostgresTopNTiesAndRemainderMatchPolicies -count=1 -v
```

- 格式检查、V-GO-ASKDATA、vet 与 diff 检查均通过。当前环境和本地忽略配置都未提供 integration admin
  DSN，因此 PostgreSQL 专项未在本轮执行；测试本身使用事务内临时表并回滚，配置 DSN 后可直接复验。
- 默认测试覆盖所有验收分支，并确认 10/1,000 的 TopN 合同不会再次挤占独立的 10,000 行执行安全上限。

2026-08-05 `DIM-003` 验证：

```sh
go test ./internal/askdata/dimension ./internal/askdata/cognition
go test ./internal/askdata/...
go vet ./internal/askdata/...
test "$(gofmt -l cmd internal | wc -l | tr -d ' ')" = "0"
go test ./...
./scripts/ci-check.sh
```

- 全部通过；流程测试覆盖 generation 输入顺序稳定性、确定性别名候选、LLM 聚类/层级/
  哨兵建议、未知成员、错误 sensitivity、截断扫描和 EvidenceRef/hash 回绑。
- 本任务未改数据库结构或页面；未运行外部真实模型，不需要也未读取 API key。

2026-08-05 `SEARCH-004` 验证：

```sh
go test ./internal/askdata/search ./internal/askdata/cognition ./internal/askdata
go test ./internal/askdata/...
go vet ./internal/askdata/...
git diff --check
go test ./...
./scripts/ci-check.sh
```

- 全部通过；覆盖候选顺序/hash 稳定性、Prompt 指令边界、可选稳定 ID 重排、全量拦截时
  跳过 LLM、block override、发明 ID、跨候选 EvidenceRef、错误集合 hash、敏感/物理查询
  上下文，以及 unknown/duplicate JSON 字段失败关闭。
- 本任务未改数据库结构或页面，未调用外部真实模型，也未读取 API key。

2026-08-05 `GRAPH-001` 验证：

```sh
go mod verify
go test ./internal/askdata/graph -count=1
./scripts/verify-nebula-poc.sh
go test ./internal/askdata/...
go vet ./internal/askdata/...
go test ./...
./scripts/ci-check.sh
git diff --check
```

- 全部通过；真实 POC 在 Linux/ARM64 Docker 上运行 NebulaGraph Server `v3.8.0`，并使用
  锁定的 `nebula-go/v3 v3.8.0` 完成 plain/TLS 查询、Space、参数、超时、并发和 graphd
  故障切换验证；结束后未残留 `askdata-nebula-poc` 容器或网络。
- 本任务未修改生产 Compose、数据库结构或页面，未读取 API key。

2026-08-05 `GRAPH-003` 验证：

```sh
gofmt -w internal/askdata/graph
go test ./internal/askdata/graph -count=1
go vet ./internal/askdata/graph
./scripts/verify-nebula-poc.sh
go test ./internal/askdata/... -count=1
go vet ./internal/askdata/...
test "$(gofmt -l cmd internal | wc -l | tr -d ' ')" = "0"
go test ./...
./scripts/ci-check.sh
git diff --check
```

- 全部通过；真实 POC 使用公开 Adapter 写入并解析合成 GraphPlan，覆盖固定查询模板、
  Tag/Edge scope、认证 Join、风险和稳定 EvidenceRef；结束后没有残留
  `askdata-nebula-poc` 容器或网络。
- 本任务未修改生产 Compose、数据库结构或页面，未调用外部模型，也未读取 API key。

2026-08-05 `NLU-001` 验证：

```sh
gofmt -w internal/askdata/understanding/normalize.go \
  internal/askdata/understanding/normalize_test.go
go test -race ./internal/askdata/understanding -count=1
go test ./internal/askdata/understanding -run '^$' \
  -fuzz '^FuzzNormalizeQuestionOffsetRoundTrip$' -fuzztime=5s
go test ./internal/askdata/... -count=1
go vet ./internal/askdata/...
test "$(gofmt -l cmd internal | wc -l | tr -d ' ')" = "0"
go test ./...
./scripts/ci-check.sh
git diff --check
```

- 全部通过；fuzz 约执行 21 万次，验证任一规范 rune 映回原文后再投影时仍覆盖原 rune，
  race 未发现共享 Unicode case folder 的并发问题。
- 本任务未新增依赖、数据库结构或页面，未调用外部模型，也未读取 API key。

2026-08-05 `NLU-002` 验证：

```sh
go test ./internal/askdata/understanding -count=1
go vet ./internal/askdata/understanding
go test -race ./internal/askdata/understanding -count=1
go test ./internal/askdata/understanding -run '^$' \
  -fuzz '^FuzzRuleParserPreservesOriginalSpans$' -fuzztime=5s
go test ./internal/askdata/... -count=1
go vet ./internal/askdata/...
test "$(gofmt -l cmd internal | wc -l | tr -d ' ')" = "0"
go test ./...
./scripts/ci-check.sh
git diff --check
```

- 全部通过；fuzz 约执行 16 万次，race 未发现问题。测试验证固定上海时区、自然周周一边界、
  财年配置、显式半开日期范围、歧义失败关闭、查询语法冲突，以及所有命中到原文 span 的
  精确回映和解析结果重放防篡改。
- 本任务未新增依赖、数据库结构、Compose 或页面，未调用外部模型，也未读取 API key。

2026-08-05 `NLU-003` 验证：

```sh
go test ./internal/askdata/understanding -count=1
go vet ./internal/askdata/understanding
go test -race ./internal/askdata/understanding -count=1
go test ./internal/askdata/understanding -run '^$' \
  -fuzz '^FuzzMergeContextReplayAndSpanSafety$' -fuzztime=5s
go test ./internal/askdata/... -count=1
go vet ./internal/askdata/...
test "$(gofmt -l cmd internal | wc -l | tr -d ' ')" = "0"
go test ./...
./scripts/ci-check.sh
git diff --check
```

- 全部通过；最终 fuzz 约执行 9 万次，race 未发现问题。测试覆盖续问识别、槽位优先级、
  显式清除、缺失上下文、独立问题不读取旧快照、权限/release/conversation 变化安全 reset、
  snapshot/result hash 防篡改、确定性重放和所有本轮触发 span 的精确原文映射。
- 本任务未新增依赖、数据库结构、Compose 或页面，未调用外部模型，也未读取 API key。

2026-08-05 `NLU-004` 验证：

```sh
go test ./internal/askdata/understanding -count=1
go test ./internal/askdata/understanding -coverprofile=/tmp/askdata-nlu004.cover
go test ./internal/askdata/... -count=1
go test -race ./internal/askdata/...
go vet ./...
test "$(gofmt -l cmd internal | wc -l | tr -d ' ')" = "0"
go test ./...
./scripts/ci-check.sh
```

- 全部通过；`understanding` 包覆盖率为 77.8%，askdata race 未发现问题。合成测试覆盖
  conversation/exact/residual/rule/policy 四类事实、完整意图、当前/继承源 span、规则冲突、
  授权域和证据白名单、取证覆盖、物理查询拒绝、稳定 hash 与结果重放防篡改。
- 本任务未运行数据库/数仓/Nebula integration test，因为没有迁移、存储或外部服务改动；
  未调用真实 LLM，也未读取或输出本地密钥。

2026-08-06 `EVAL-001` 验证：

```sh
go test ./internal/askdata/evaluation -count=1 \
  -coverprofile=/tmp/askdata-eval001.cover
go test ./internal/askdata/... -count=1
go test -race ./internal/askdata/...
go vet ./...
test "$(gofmt -l cmd internal | wc -l | tr -d ' ')" = "0"
go test ./...
./scripts/ci-check.sh
git diff --check
```

- 全部通过；`evaluation` 包覆盖率为 71.0%，askdata race 未发现问题。测试覆盖 synthetic
  golden IR/result、列/行规范顺序、Decimal 精度、FLOAT 容差与 exact hash 差异、NULL、
  日期/时区、重复 Key、全部标量、IR/result hash 防篡改和有界失败关闭。
- 本任务没有迁移、存储、外部服务或页面改动，因此未运行数据库/数仓/Nebula integration；
  未调用真实模型，也未读取或输出本地密钥。

2026-08-06 `EVAL-002` 验证：

```sh
go test ./internal/askdata/evaluation -count=1 \
  -coverprofile=/tmp/askdata-eval002.cover
go test ./internal/askdata/... -count=1
go test -race ./internal/askdata/... -count=1
go vet ./...
test "$(gofmt -l cmd internal | wc -l | tr -d ' ')" = "0"
go test ./... -count=1
./scripts/ci-check.sh
git diff --check
```

- 全部通过；`evaluation` 包覆盖率为 77.1%，askdata race 未发现问题。测试覆盖三类
  mention/binding PRF、domain/复杂度/歧义分层守恒、训练/验证校准正负标签、sealed 隔离、
  错误 span/角色/成员父维度、规范顺序、零分母和报告重放防篡改。
- 本任务没有迁移、存储、外部服务或页面改动，因此未运行数据库/数仓/Nebula integration；
  未调用真实模型，也未读取或输出本地密钥。

2026-08-06 `DB-005` 验证：

```sh
ENV_FILE=.env.example ./scripts/migrate.sh
ENV_FILE=.env.example ./scripts/verify-database.sh
ENV_FILE=.env.example ./scripts/verify-warehouse.sh
sh -n scripts/migrate.sh scripts/verify-database.sh
go test ./internal/askdata/... -count=1
go test ./... -count=1
go vet ./...
ENV_FILE=.env.example ./scripts/check-compose.sh
./scripts/ci-check.sh
git diff --check
```

- 全部通过。`000217` 在本地无业务运行数据时完成精确 down→up 往返；最终向前迁移幂等，
  数据库验收 fixture 全部位于回滚事务，未留下 synthetic tenant/release/run。
- 实测 app 角色 ACTIVE release pin、合法状态链、错误 release hash/跨级迁移拒绝、current
  version/state 回绑、Tool 重试单行幂等、BLOCK completion artifact、终态冻结、同域跨 actor
  RLS；管理员 UPDATE/DELETE event/artifact/tool outcome 也被 append-only trigger 拒绝。
- 本任务没有页面、Go 业务代码、Warehouse schema、外部模型或 Compose 改动；未读取或输出
  本地密钥，ACTIVE 激活函数仍不存在。

2026-08-06 `ORCH-002` 验证：

```sh
gofmt -w internal/askdata/orchestrator/*.go
ENV_FILE=.env.example ./scripts/migrate.sh
ENV_FILE=.env.example ./scripts/verify-database.sh
ASKDATA_INTEGRATION_DATABASE_URL='postgres://report_app:...@127.0.0.1:5432/...' \
ASKDATA_INTEGRATION_ADMIN_DATABASE_URL='postgres://report_admin:...@127.0.0.1:5432/...' \
  go test ./internal/askdata/orchestrator -count=1
ASKDATA_INTEGRATION_DATABASE_URL='postgres://report_app:...@127.0.0.1:5432/...' \
ASKDATA_INTEGRATION_ADMIN_DATABASE_URL='postgres://report_admin:...@127.0.0.1:5432/...' \
  go test -race ./internal/askdata/orchestrator -count=1
go test ./internal/askdata/... -count=1
go test ./... -count=1
go vet ./...
bash -n scripts/migrate.sh scripts/verify-database.sh
git diff --check
```

- 全部通过。真实数据库测试逐项对拍 Go/SQL 14×14 状态矩阵，并覆盖 RECEIVED 到
  ANSWERED、CLARIFICATION_REQUIRED、BLOCKED 以及 PLAN/RESULT 两条 correction 的 Store +
  trigger + replay 组合；缺失/错误 EventType 引用、提前 hash、非法跳转、过期 version、跨
  actor 读取、错误/superseded release 新建均失败关闭。
- `000217` 在确认四张运行时表均为 0 后再次完成精确 down→up，最终 migrate 幂等；数据库
  verify 与 Go 集成 fixture 均回滚，检查后没有 `orch_*` / `askdata_runtime_*` 合成数据。
- ACTIVE release lock helper 只供 lifecycle trigger 内部使用；app、worker、connection tester
  均无 EXECUTE，app 仍无 releases UPDATE。`FOR SHARE` release/fact 锁、hash 阶段链和事件
  引用约束已纳入数据库静态与真实角色验收。
- 本任务没有页面、Warehouse schema、外部模型或 API key 调用，ACTIVE 激活入口仍不存在。

2026-08-06 `DB-006` 验证：

```sh
ENV_FILE=.env.example ./scripts/migrate.sh
ENV_FILE=.env.example ./scripts/verify-database.sh
go test ./internal/askdata/... -count=1
go test ./... -count=1
go vet ./...
bash -n scripts/migrate.sh scripts/verify-database.sh
git diff --check
```

- 全部通过。`000218` 在确认五张评测/反馈表没有业务数据后完成精确 down→up；真实 app/
  worker/admin fixture 全部回滚，验证后没有 evaluation/query feedback synthetic residue。
- 实测当前内容作者自审、同 reviewer 双 slot、stale review、少于两条独立 APPROVED review、
  SEALED 内容可见/可改、错误 release/path/result equivalence、leak 事实遗漏/矛盾、run 更新删除，
  以及非终态或跨 actor feedback 均失败关闭；本任务没有页面或激活入口。

2026-08-06 `SEC-003` 验证：

```sh
ENV_FILE=.env.example ./scripts/migrate.sh
ENV_FILE=.env.example ./scripts/verify-database.sh
ASKDATA_INTEGRATION_DATABASE_URL='postgres://report_app:...@127.0.0.1:5432/...' \
ASKDATA_INTEGRATION_ADMIN_DATABASE_URL='postgres://report_admin:...@127.0.0.1:5432/...' \
  go test ./internal/askdata/security -count=1
go test ./internal/askdata/... -count=1
go test -race ./internal/askdata/security ./internal/askdata/dimension ./internal/askdata/understanding -count=1
go test ./... -count=1
go vet ./...
./scripts/ci-check.sh
bash -n scripts/migrate.sh scripts/verify-database.sh
git diff --check
```

- 全部通过。`000223` 在确认 member/alias/MEMBER release object 均为 0 的前提下精确
  down→up，并重新执行正式 `migrate.sh` 补齐新建函数的运行角色 ACL；最终迁移为 220 个、
  最高版本 `000223`，随后完整数据库验证再次通过。
- 真实 `report_app` fixture 覆盖 confidential canonical/alias 成功、restricted ACTIVE ROLE
  成功，以及 missing/unauthorized 同为零行、ambiguous/expired/wrong hash/random release 为零行；
  sensitivity-only manifest 更新、低于维度的 member、敏感 MEMBER search document 均被拒绝。
  SUPERSEDED release 的固定回放成功，fixture 回滚后 tenant/member/alias/MEMBER release residue
  均为 0。
- Go/race 覆盖 lookup/match 格式化与 JSON 不泄漏、相邻 ASCII/全角/NFKC/case-fold 变体、当前/
  继承问句递归 fact 遮罩、敏感 span 冲突及 Reviewer 在 mention/summary/reason/conflict/evidence
  request 中回显失败关闭；另覆盖合法 match 复制后篡改、调用方构造普通 MEMBER hit、
  EXACT_ONLY/ON_DEMAND/NONE/高基数伪造 eligible，以及外部包通过自定义 Store/Scanner 注入标签。
  画像 PromptFact 仅含聚合统计且 member reviewer 调用次数为 0；Reranker 外部包攻击 fixture
  进一步验证显式 MEMBER 即使携带伪造 INTERNAL/FULL/ALLOW 证据也不能生成 PromptFact 或触发
  reviewer。本任务没有页面、Warehouse、外部模型或 API key 调用。

2026-08-05 全新环境启动验证：

```sh
go test ./...
./scripts/dev-services.sh start
./scripts/dev-services.sh status
```

- 该次全新环境启动时有 218 个迁移版本；加入 `000218` 后为 219 个，本轮 `000223` 后当前为
  220 个，范围为 `000001`～`000223`，`000192` 不存在，`000219`～`000220` 仍按 TODO 保留给
  发布门禁与审批迁移。
- API live/ready、Connector live、Web 首页均返回 HTTP 200，四个应用容器均 healthy。
- `000221` 已移除 `000195` 遗留、仍引用已删除表的 tenant trigger；`make seed-dev`
  已在空库成功创建 demo tenant 与管理员。
- 映射数据集刷新幂等键已加入数据集版本和草稿修订版本；相同 transition 可重放，
  回退到历史 schema 时不会与旧发布撞键。
- `verify-database.sh` 改用事务内自建跨领域夹具，不再依赖预置 membership；
  `verify-warehouse.sh` 已移除退役画像运行时的旧函数断言，两者均通过。

数据库验证还执行了：

```sh
ENV_FILE=.env.example ./scripts/migrate.sh
ENV_FILE=.env.example ./scripts/verify-database.sh
ENV_FILE=.env.example ./scripts/verify-warehouse.sh
```

- `000213`～`000216` 完成一次完整空 schema down→up 往返；`000221`、`000222`
  已在现有库完成向前迁移；画像三表的强制 RLS、tenant 复合外键和 app/worker
  最小权限均由数据库验收脚本断言。
- PostgreSQL 实测角色权限、跨领域 RLS、全部 tenant 复合外键、HNSW、四投影函数和
  “激活函数不存在”安全断言。
- Registry integration test 实测 app role tenant transaction、乐观锁、稳定分页、
  跨领域不可见。
- Search integration test 实测 worker 角色 embedding claim SQL，以及 app 角色下
  exact/lexical/vector 三类查询的 USER RLS、release pin 和空 release 失败关闭路径。
- Dimension integration test 实测 worker 同步 SQL 的数值参数类型，以及 Warehouse
  reader 的只读有界扫描、NULL/基数统计、成员截断和 snapshot 行数漂移检测。
- 回滚事务中的真实 DWS fixture 实测：DRAFT 导入、认证依赖、release object、四投影
  claim/complete→READY、GraphPlan cache，以及源 DWS 失效后 release 失败关闭。

已验证的负向边界：

- 空 ID、非法 hash、篡改 PolicyScope hash；
- JSON 未知字段、重复键、尾随值；
- 中文 Unicode span 与原问句不一致；
- Semantic IR 夹带物理字段、未绑定成员和无效排序引用；
- 未知 Tool、跨 Tool 参数、未执行二次校验；
- Action 阶段不匹配、多个 payload 和夹带 SQL 字段。
- Semantic Question 默认未授权、Tool Message 顺序/identity、截断、拒答、模型切换、
  reasoning content 丢弃、重复 action/tool no-progress；
- 画像预算/计数矛盾、profile/decision hash 篡改、敏感/高基数索引策略；
- canonical/alias 混写、跨维度同值 key、UNKNOWN/NULL/测试值、LLM 高风险自动合并；
- member sensitivity 低于维度、仅修改 MEMBER manifest sensitivity、contract 夹带 label/hash、
  跨 member/未认证/未排序 alias version ID，以及 DOMAIN release object 跨域 identity；
- 敏感成员 missing/denied 结果分叉、错误 release/hash/投影、未发布或未固定对象、过期成员、
  同 hash 多成员歧义、非活动 ROLE/错误动作，以及 raw member/alias/profile label 列权限；
- EXACT_ONLY 原值、question/lookup hash 进入 fmt/JSON/evidence，当前或继承问句敏感 span 进入
  Prompt fact，Reviewer 在 mention/summary/reason/conflict/evidence request 回显规范化变体；
- profile generation 输入顺序漂移、伪造成员 ID、跨 generation/evidence hash、未观测别名、
  敏感成员进入 LLM、扫描不完整时自动应用；
- MEMBER 文档缺少维度上下文、文档凭证/物理查询、Embedding 维数错误、旧租约覆盖；
- Hybrid Retriever 无权限 scope、错误 release、水位过滤和向量降级。
- Reranker 候选集合篡改、错误 release-bound candidate-set hash、发明/blocked candidate、
  跨候选 evidence、非规范反例、敏感/凭证/物理查询上下文，以及全候选 block 后误调用 LLM；
- Nebula Client 缺失 Space、TLS 证书/握手错误、注入形态参数、无响应 endpoint 超时、
  单 graphd 故障和非 POC Compose target；版本锁禁止 master/nightly 及错误使用 v5 gRPC Client；
- GraphPlan 注入形态/冲突稳定 ID、重复 VID、候选和跳数上限、跨 tenant/domain/release、
  篡改 PolicyScope、发明模型/维度/成员、未授权中间模型、未认证 Join、风险/path/plan hash
  篡改、任意 nGQL 泄露；
- 问句非法 UTF-8、超长输入、空/纯标点内容、控制字符、双向覆盖/零宽不可见字符、规范
  span 越界、已删除语气词 span 伪映射、规范文本同长度篡改，以及 NFKC 组合/展开导致的
  offset 漂移；
- 无年份/顺序不明/非法日期、裸财年/自然周、财年配置缺失、多个时间表达、倒序或跨粒度
  日期范围，以及固定上海时区的 UTC 跨日边界；
- 多个比较/排名、Top 百分比、越界 limit、显式排序相互冲突或与排名方向冲突，以及
  “按揭/按摩/按钮/按键/按压”被错误识别为分组标记；
- 未完成/含 unresolved 的理解进入会话快照、缺少上一轮的片段追问、旧快照或合并结果
  hash 篡改、同 turn 重放、完整新问题误继承旧条件，以及清除指令被规则命中反向覆盖；
- 跨 tenant/actor/domain/role/policy/release/conversation 继承，或在独立/清空问题中读取、
  暴露损坏旧快照；
- LLM 完整理解改写确定性时间/比较/TopN/排序、吞掉规则或上下文冲突、错误分组 role、
  当前/继承 span 混用、越权 domain hypothesis、发明/跨输入 EvidenceRef、嵌套证据未列入
  顶层引用、mention 或 unresolved 缺少分类型取证请求，以及模型文本夹带物理查询；
- 结果列缺失/重复、行列数不一致、超出行/cell/字节预算、Decimal 经 binary float 损失精度、
  NaN/Inf、FLOAT Key、重复业务 Key、NULL 与字符串混同、非法日期/时区、zone-less 时间读取
  本机时区、规范行/hash 篡改、declared result hash 不匹配，以及仅结果相同但 IR 关键字段错误；
- gold/prediction 问句不一致、同类型 mention span 重复、binding index 越界/重复、对象版本 ID
  非法、dimension role 与 mention 不一致、member 缺少所属 dimension、校准特征 NaN/Inf/越界、
  retrieval rank 非法、分层总数漂移、校准顺序/shape/hash 篡改，以及 SEALED/生产回归样本泄漏
  到训练或验证输入；
- 问数 run 绑定非 ACTIVE/错误 hash release、跨级或终态后状态迁移、`record_version` 非精确加一、
  身份/release/policy/预算上限改写、预算用量回退、阶段前注入或终态前补齐 hash、hash 断链/
  覆盖、纠错保留陈旧下游 hash、终态缺少对应 artifact、EventType 缺少/伪造 AI/Tool/Artifact
  引用、事实缺少对应审计 event、child fact 伪造 run version/state/actor/release/policy、跨 actor
  读取、Tool Call 重复、event/artifact/tool outcome 更新或删除，以及审计 JSON 夹带 question/
  prompt/reasoning/SQL/参数/response/result rows；
- 普通成员管理/封存评测集、case 原作者或当前内容编辑者自审、同 reviewer 占用两个 slot、
  stale review 继续计数、少于两条当前 APPROVED review Seal、SEALED case/review 改写、USER
  读取密封问句、DRAFT production regression 或 RETIRED set 新增 run、错误 set/case/release/path
  pin、未固定 warehouse freshness、等价布尔缺少 hash/report 证据、遗漏 leak 字段、leak 与
  security 状态矛盾、evaluation run 更新/删除，以及非终态/cross-actor/shape 错误/过期 version/
  删除 query feedback；
- AVG/COUNT_DISTINCT 可加性错误、MANY_TO_MANY SAFE fanout、敏感/高基数成员策略；
- canonical JSON 重复键、对象顺序变化、contract hash 篡改、release 对象重复；
- repository stale record version、空/篡改 cursor、跨领域读取；
- 非数值 measure 导入、跨 domain asset 过滤、重复导入稳定 ID/hash。

2026-08-06 `GRAPH-002` 验证：

```sh
./scripts/check-compose.sh
./scripts/verify-nebula-env-isolation.sh
./scripts/verify-nebula-compose.sh
./scripts/verify-nebula-poc.sh
docker compose --env-file .env.example --env-file .env \
  --profile verification run --rm --no-deps nebula-verify
go test ./... -count=1
go vet ./...
ENV_FILE=.env ./scripts/verify-database.sh
ENV_FILE=.env ./scripts/verify-warehouse.sh
./scripts/ci-check.sh
npm --prefix web run lint
npm --prefix web run build
./scripts/dev-services.sh start
./scripts/dev-services.sh status
git diff --check
```

- 正式隔离 Compose 验收通过并自动清理容器、网络、命名卷；canonical Compose 已切换到
  cluster/client/host-proxy 三网结构，所有基础设施和应用服务 healthy，graphd 无宿主端口，
  proxy 仅为 `127.0.0.1:9669`。
- 全量 Go test/vet、数据库/数仓验证、Compose 静态检查、本地凭据隔离、CI 静态检查及前端
  lint/build 通过。Vite 仅保留既有大 chunk 提示。
- GRAPH-001 POC 在与其他重型回归并行时出现过 2 秒 SessionPool 并发认证超时；其独立的
  600ms blackhole 超时负测保持不变，普通 POC socket timeout 调整为 10 秒并在独立重跑通过。
- 本轮没有调用外部模型、没有读取或输出 API key，没有新增页面。

2026-08-06 `GRAPH-004` 验证：

```sh
gofmt -w cmd/worker/main.go internal/askdata/graph/*.go
bash -n deployments/nebula/init.sh deployments/nebula/verify.sh \
  scripts/migrate.sh scripts/verify-database.sh scripts/verify-nebula-compose.sh
ENV_FILE=.env.example ./scripts/migrate.sh
ENV_FILE=.env.example ./scripts/verify-database.sh
./scripts/verify-nebula-compose.sh
go test ./internal/askdata/... -count=1
go test ./... -count=1
go vet ./...
./scripts/check-compose.sh
./scripts/ci-check.sh
ENV_FILE=.env.example ./scripts/verify-warehouse.sh
docker compose --env-file .env.example --env-file .env build api
docker compose --env-file .env.example --env-file .env \
  up -d --no-deps --force-recreate --wait worker
./scripts/dev-services.sh status
git diff --check
```

- 全部通过。正式 Nebula 验收真实删除并重建 Space；第二轮 Projector 在空图上恢复 6 个点、
  5 条边，证明与规范快照一致，随后 drift 负测仍通过。临时项目、网络和卷由 trap 清理。
- 数据库验证真实执行 `NEBULA_GRAPH` target listing/claim、heartbeat、快照和 completion；错误
  lease token 失败关闭，快照只含稳定 ID/版本/状态/关系事实。静态权限检查确认只有 Worker
  可执行新增函数。
- 全量 Go、vet、Compose、CI 静态检查、数据库和数仓验证通过；新 worker 容器健康，日志只有
  正常启动记录。本轮没有新增页面，也未读取或输出 `.env` 中的 secret。

2026-08-06 `GRAPH-005` 验证：

```sh
gofmt -w internal/askdata/graph
go test ./internal/askdata/graph -count=1
ASKDATA_INTEGRATION_ADMIN_DATABASE_URL='postgres://<admin>@127.0.0.1:<port>/<db>?sslmode=disable' \
ASKDATA_INTEGRATION_APP_ROLE='<app-role>' \
  go test ./internal/askdata/graph \
  -run '^TestPostgresFallbackAndCertifiedCacheAgainstRuntimeRole$' -count=1 -v
go test ./... -count=1
go vet ./...
ENV_FILE=.env.example ./scripts/verify-database.sh
ENV_FILE=.env.example ./scripts/verify-warehouse.sh
./scripts/check-compose.sh
./scripts/ci-check.sh
git diff --check
```

- 全部通过。故障注入覆盖公开 Nebula Client 的传输失败与结构完整但缺少指标绑定的结果；精确
  认证缓存命中后保留降级原因，缓存篡改、scope/release/request 重绑与全链失败均关闭。
- PostgreSQL 集成夹具在同一回滚事务内构造 READY release、四投影和认证关系，以真实
  `report_app` RLS 权限读取；回退结果与标准构造器生成的 `GraphPlan` 深度一致，序列化输出不含
  成员 key/label。将 NEBULA_GRAPH 投影置为 FAILED 后，缓存和关系回退都拒绝重放。
- 全仓 Go test/vet、数据库/数仓动态检查、Compose/CI 静态门禁通过。本任务没有新增页面，也未
  读取或输出 `.env` 中的 secret。

2026-08-06 `NLU-005` 验证：

```sh
gofmt -w internal/askdata/binding
go test ./internal/askdata/binding -count=1 -v
go test -race ./internal/askdata/binding -count=1
go test ./internal/askdata/... -count=1
go vet ./internal/askdata/...
go test ./... -count=1
go vet ./...
./scripts/ci-check.sh
git diff --check
```

- 全部通过，race 未发现共享状态问题。合成测试证明独立候选 Top 1 不兼容时只保留整体可行
  bundle；规则 block、过期成员、错误父维度、无 allowed GraphPath、跨 release/graph request
  候选以及 understanding/GraphPlan/result hash 篡改均不能形成可执行组合。
- 续问夹具证明继承指标仍引用上一轮 mention origin/span，本轮分组维度保持 CURRENT；候选顺序
  变化不改变 Top bundle 或结果 hash。空候选生成稳定 NoMatch，严格解码拒绝未知物理查询字段。
- 本任务没有数据库、Compose 或页面变更，未调用外部模型，也未读取或输出 API key。

2026-08-06 `NLU-006` 验证：

```sh
gofmt -w internal/askdata/binding
go test ./internal/askdata/binding -count=1 -v
go test -race ./internal/askdata/binding -count=1
go test ./internal/askdata/... -count=1
go vet ./internal/askdata/...
go test ./... -count=1
go vet ./...
./scripts/ci-check.sh
git diff --check
```

- 全部通过，race 未发现共享状态问题。校准模型在输入顺序变化时保持同一 hash，并证明更强
  特征的样本校准概率不低于弱特征样本；训练/验证身份泄漏、单标签或越界输入均失败关闭。
- held-out 验证满足 precision/confidence/margin 后才允许 DIRECT；低 margin 或验证门禁关闭时
  输出证据绑定的真实候选，单/零候选进入 EVIDENCE_REQUIRED/NO_MATCH，不制造虚假歧义。
- 严格 JSON 重放拒绝 LLM confidence 字段、物理 SQL 展示、伪造 bundle、跨 bundle evidence、
  未知字段和 hash 篡改。本任务没有数据库、Compose 或页面变更，未调用外部模型。

2026-08-06 `QUERY-001` 验证：

```sh
gofmt -w internal/askdata/{binding,calibration,cognition,evaluation,ir,ircontract,toolhost}
go test ./internal/askdata/ir -count=1 -v
go test -race ./internal/askdata/ir ./internal/askdata/binding \
  ./internal/askdata/evaluation -count=1
go test ./internal/askdata/... -count=1
go vet ./internal/askdata/...
go test ./... -count=1
go vet ./...
./scripts/ci-check.sh
git diff --check
```

- 全部通过，race 未发现共享状态问题。完整 fixture 从规则解析、Understanding、GraphPlan、Binding
  一直重放到 IR，验证 2026 年半开区间、同比、华东稳定 member ID、地区 groupBy、指标降序与
  Top 5；candidate set 反序后 BuildArtifact 与全部 hash 保持一致。
- 过期 member ownership、多模型 bundle、无成员 FILTER、错误继承 snapshot/hash/date、未知物理
  SQL 字段和 binding/IR/artifact 篡改全部失败关闭。包合同下沉后 cognition/toolhost schema、
  evaluation alias 和原 `ir` API 的全仓调用均通过编译与既有回归。
- 本任务没有数据库、Compose 或页面变更，未调用外部模型，也未读取或输出 API key。

2026-08-06 `QUERY-002` 验证：

```sh
gofmt -w internal/askdata/compiler internal/askdata/testfixture/ir_build.go
go test ./internal/askdata/compiler -count=1 -v
go test -race ./internal/askdata/compiler ./internal/askdata/testfixture -count=1
go test ./internal/askdata/... -count=1
go test ./... -count=1
go vet ./...
./scripts/ci-check.sh
git diff --check
```

- 全部必需门禁通过，race 未发现共享状态问题。PostgreSQL 集成测试已编译并接入显式
  `ASKDATA_INTEGRATION_DATABASE_URL`/admin URL 门禁；当前 shell 未提供 URL，因此该外部用例按既有
  约定跳过，未读取或回显 `.env`。
- 可重放 fixture 证明 Resolver 只向 Store 提交 run 固定 release；即使 Store 的 current release
  改变，Resolution 全值与 hash 仍不变。负测覆盖 retired materialization、错误 actor/domain、
  member 跨 FILTER 维度、time dimension 错绑、manifest 对象缺失、GraphPath 关系不一致、带 label
  MEMBER contract 和 resolved AST 篡改。
- 全仓 test/vet、CI 静态检查和格式/diff 检查通过。本任务未新增页面、数据库迁移或外部模型调用。

2026-08-06 `QUERY-003` 验证：

```sh
gofmt -w internal/askdata/compiler internal/querycompiler
go test ./internal/askdata/compiler ./internal/querycompiler -count=1
go test -race ./internal/askdata/compiler ./internal/askdata/testfixture \
  ./internal/querycompiler -count=1
test "$(gofmt -l cmd internal | wc -l | tr -d ' ')" = "0"
go test ./... -count=1
go vet ./...
./scripts/ci-check.sh
git diff --check
```

- 全部通过，race 未发现共享状态问题。Golden 用例固定 PostgreSQL 参数化 SQL 与 Args；正式查询
  `RESULT` 上限 10000 可编译，同一 SQL shape 更换成员/时间参数后 compiled plan hash 不变，默认
  `PREVIEW` 仍拒绝超过 previewLimit 的请求。
- 复杂合成用例覆盖 Metric 逻辑/版本 Measure 引用、Measure 字段 AST、指标级 `PAID` default
  filter、华东成员、月粒度、2026 当前期/2025 基线期、指标排序及闰日同比 clamp。公开 Artifact
  JSON 不含 SQL、Args、`EAST` 或日期边界，反序列化后仍能重放 shape/hash 但不能取得执行参数。
- 负测覆盖未知 AST 字段、未知 Measure、非 exact dependency、物理 view 篡改、非法 limit kind；
  Resolution JSON 去除临时 member key 后仍可验证。本任务未新增页面、数据库迁移或外部模型调用。

2026-08-06 `QUERY-004` 验证：

```sh
gofmt -w internal/askdata/validator
go test ./internal/askdata/validator -count=1 -v
go test ./internal/askdata/... ./internal/querycompiler -count=1
test "$(gofmt -l cmd internal | wc -l | tr -d ' ')" = "0"
go test ./... -count=1
go vet ./...
go test -race ./internal/askdata/validator ./internal/askdata/compiler \
  ./internal/askdata/testfixture ./internal/querycompiler -count=1
./scripts/ci-check.sh
git diff --check
```

- 全部通过，race 未发现共享状态问题。危险 DDL/DML/锁/多语句/注释/递归 CTE、未知 relation/UDF、
  高成本、超行数、超大 Seq Scan、Join rows/fanout 和可放宽 ceiling 均被负测拒绝；正常 CTE、
  DATE_TRUNC、CASE、CAST NUMERIC 及真实 QUERY-003 live plan 均通过。
- 验证 Artifact JSON 不含 SQL、Args、发布物理名或 metric/field 名；公开工件丢失 live 参数后不能再次
  EXPLAIN，必须回到 pinned compiler 重新注入。PostgreSQL adapter 已编译通过；实际数仓连接验证留给
  明确要求 warehouse integration 的 `QUERY-005` 一并覆盖。本任务无页面、迁移、外部模型调用。

2026-08-06 `QUERY-005` 验证：

```sh
gofmt -w internal/askdata/validator internal/queryruntime
go test ./internal/askdata/validator ./internal/queryruntime -count=1
ASKDATA_INTEGRATION_WAREHOUSE_DATABASE_URL='postgres://<reader>@127.0.0.1:5433/<warehouse>' \
ASKDATA_INTEGRATION_WAREHOUSE_ADMIN_DATABASE_URL='postgres://<admin>@127.0.0.1:5433/<warehouse>' \
  go test ./internal/askdata/validator \
  -run TestPostgresExecutorUsesReaderRoleReadOnlySnapshotAndSupportsCancellation -count=1 -v
test "$(gofmt -l cmd internal | wc -l | tr -d ' ')" = "0"
go test ./... -count=1
go vet ./...
go test -race ./internal/askdata/validator ./internal/askdata/compiler \
  ./internal/askdata/testfixture ./internal/queryruntime ./internal/querycompiler -count=1
./scripts/ci-check.sh
git diff --check
```

- 全部通过，race 未发现共享状态问题。真实本地数仓 integration 创建隔离 DWS table + published view，
  确认 warehouse reader 不能 INSERT，真实 EXPLAIN/执行得到精确 `30.7500000000` DECIMAL；普通审计
  不出现任一输入/结果值。随后以 ACCESS EXCLUSIVE lock 阻塞同一查询，actor/domain 合法的 cancel 在
  2 秒内终止 pgx/PostgreSQL 调用并写入 CANCELED，fixture 已完整清理。
- 单元负测覆盖序列化计划无 live Args、validation/plan 不匹配、超 MaxRows、NaN、跨 actor 取消、caller
  deadline、Start/Finish audit 故障；成功 result hash 不受 run ID 影响，Artifact/Audit JSON 不含 SQL、
  Args 或结果行。本任务无页面、迁移、外部模型调用。

2026-08-06 `QUERY-006` 验证：

```sh
gofmt -w internal/askdata/validator
go test ./internal/askdata/validator -count=1
go test ./internal/askdata/... -count=1
go vet ./internal/askdata/...
go test -race ./internal/askdata/validator ./internal/askdata/cognition \
  ./internal/askdata/evaluation ./internal/askdata/compiler \
  ./internal/askdata/testfixture -count=1
go test ./... -count=1
go vet ./...
./scripts/ci-check.sh
git diff --check
```

- validator、askdata module test/vet/race 已通过。fixture 覆盖正常精确 Decimal、错误 OID、NULL key、
  重复行/key、metric-only fanout、除零、过期数据、质量失败、完整/不完整时间覆盖、确认空结果、权限
  裁剪空结果，以及 CURRENT/BASELINE 900% 异常趋势。
- 认知路径使用受控 reviewer fixture 验证 ANOMALY_ANALYSIS → RESULT_VERIFICATION 顺序、Prompt 不含
  SQL/Args/完整 rows、发明 evidence 失败关闭，以及规则失败而模型 PASS 时最终仍为 RETRY 并记录覆盖
  阻止。本任务无页面、迁移、数据库/数仓写入或真实外部模型调用。

2026-08-06 `ORCH-001` 验证：

```sh
gofmt -w internal/askdata/toolhost internal/askdata/cognition/executor_test.go
go test ./internal/askdata/toolhost -coverprofile=/tmp/askdata-orch001.cover -count=1
go test ./internal/askdata/... -count=1
go vet ./internal/askdata/...
go test -race ./internal/askdata/toolhost ./internal/askdata/cognition \
  ./internal/askdata/orchestrator -count=1
test "$(gofmt -l cmd internal | wc -l | tr -d ' ')" = "0"
go test ./... -count=1
go vet ./...
./scripts/ci-check.sh
git diff --check
```

- 全部通过，toolhost 覆盖率 68.8%，race 未发现共享状态问题。测试枚举并重放全部 14 个 Definition/
  schema/hash 和全部 14 个 typed Result validator；覆盖稳定 AvailableTools、search typed dispatch、
  permission/release/domain/budget preflight、候选对比两次正式查询预算、timeout/cancel、错误遮罩和
  Definition schema 深拷贝。
- 负测验证 NaN、发明/不完整 evidence、`rows` 等禁用字段无法形成 Tool Message；敏感 member handler
  即使返回 label/alias，也会在 Registry 结果中被清空。既有 Cognition ToolMessage fixture 已补充强制
  EvidenceRef，未放宽动作或参数合同。本任务无页面、迁移、数据库/数仓写入或真实外部模型调用。

2026-08-06 `ORCH-003` 验证：

```sh
gofmt -w internal/askdata/orchestrator/loop.go internal/askdata/orchestrator/loop_test.go
go test ./internal/askdata/orchestrator -count=1
go test ./internal/askdata/... -count=1
go vet ./internal/askdata/orchestrator
test "$(gofmt -l cmd internal | wc -l | tr -d ' ')" = "0"
go test ./... -count=1
go vet ./...
go test -race ./internal/askdata/orchestrator ./internal/askdata/cognition \
  ./internal/askdata/toolhost -count=1
./scripts/ci-check.sh
git diff --check
```

- 全部通过，race 未发现共享状态问题。测试覆盖单次 LLM fast path、typed search tool → 新证据 → 第二轮
  binding 决策、4 次模型复杂纠错上限、无进展、总 timeout、caller cancel、初始/运行中预算耗尽。
- 负测覆盖发明/冲突 EvidenceRef、重复 action、预算过滤后的不可用 formal tool、模型返回错误 stage、
  Tool response callId 错配和 adapter 超剩余预算收费；模型只接收 sanitized prompt/tool result，transcript
  不含 rows/SQL/Args/reasoning。本任务未连接数据库、数仓或真实模型，也没有页面变更。

2026-08-06 `ORCH-004` 验证：

```sh
gofmt -w internal/askdata/orchestrator
go test ./internal/askdata/orchestrator -count=1
go test -race ./internal/askdata/orchestrator -count=1
go vet ./internal/askdata/orchestrator
set -a && source .env.example && set +a
ASKDATA_INTEGRATION_DATABASE_URL="$DATABASE_URL" \
ASKDATA_INTEGRATION_ADMIN_DATABASE_URL="postgres://${POSTGRES_USER}:${POSTGRES_PASSWORD}@${POSTGRES_HOST}:${POSTGRES_PORT}/${POSTGRES_DB}?sslmode=disable" \
  go test ./internal/askdata/orchestrator -count=1 -v
go test ./internal/askdata/... -count=1
go vet ./internal/askdata/...
test "$(gofmt -l cmd internal | wc -l | tr -d ' ')" = "0"
go test ./... -count=1
go vet ./...
go test -race ./internal/askdata/orchestrator ./internal/askdata/cognition \
  ./internal/askdata/toolhost -count=1
./scripts/ci-check.sh
git diff --check
```

- 全部通过，race 未发现共享状态问题。真实 app/admin PostgreSQL integration 在回滚 fixture 内验证
  2 个 `SEMANTIC_QUESTION` AI request、2 次 LLM decision、1 次 typed tool、replay artifact、预算事件
  和 RETRIEVING → BINDING 状态事件原子落库；Resume 后可重建同 result/definition/charge 的脱敏执行，
  重复 action 未触达 fake Tool Host。
- 第二条真实数据库链验证 4 次 LLM 预算已用尽后，预算/错误事件、CLARIFICATION 工件和终态迁移同
  事务提交；相同 checkpoint 返回同一版本/事件数量，内容碰撞返回幂等冲突。完整 ORCH-002 状态矩阵、
  生命周期/RLS/乐观锁/pinned release integration 同次回归通过，fixture 全部回滚。负测另覆盖收费与
  usage 不符、决策/终态矛盾、无效 ceiling、重放 hash 碰撞及审计敏感键；本任务无页面或新迁移。

2026-08-06 `ORCH-005` 验证：

```sh
gofmt -w internal/askdata/http cmd/api/main.go
go test ./internal/askdata/http -count=1 -v
go vet ./internal/askdata/http
set -a && source .env.example && set +a
ASKDATA_INTEGRATION_DATABASE_URL="$DATABASE_URL" \
ASKDATA_INTEGRATION_ADMIN_DATABASE_URL="postgres://${POSTGRES_USER}:${POSTGRES_PASSWORD}@${POSTGRES_HOST}:${POSTGRES_PORT}/${POSTGRES_DB}?sslmode=disable" \
  go test ./internal/askdata/http \
  -run TestPostgresQuestionScopeResolutionUsesActiveReleaseRolesAndActorRLS -count=1 -v
go test ./internal/askdata/... -count=1
go vet ./internal/askdata/...
go test -race ./internal/askdata/http ./internal/askdata/orchestrator -count=1
test "$(gofmt -l cmd internal | wc -l | tr -d ' ')" = "0"
go test ./... -count=1
go vet ./...
./scripts/ci-check.sh
git diff --check
```

- 全部通过，race 未发现共享状态问题。真实数据库用例在回滚事务中以实际 app role 执行 scope query，
  确认 actor 可取得 ACTIVE release/role policy 和自身 pinned run，同域 observer 因 RLS 只能得到 not found。
  单元/HTTP 用例确认响应和 SSE 不含注入到 audit Details/payload 中的 prompt、SQL、result rows 或内部错误。

2026-08-06 `ORCH-006` 验证：

```sh
go test ./internal/askdata/orchestrator ./internal/config -count=1
go test -race ./internal/askdata/orchestrator ./internal/config -count=1
ASKDATA_INTEGRATION_DATABASE_URL="$DATABASE_URL" \
ASKDATA_INTEGRATION_ADMIN_DATABASE_URL="postgres://..." \
  go test ./internal/askdata/orchestrator \
  -run TestPostgresStoreQuestionLifecycleResumeAndPinnedRelease -count=1 -v
test "$(gofmt -l cmd internal | wc -l | tr -d ' ')" = "0"
go test ./... -count=1
go vet ./...
git diff --check
```

- 全部通过。单元覆盖 hash-only 无可恢复材料、AES-GCM 随机 nonce/认证上下文/篡改/过期、非法 mode/key/
  TTL、会话 tenant/actor/domain/conversation/release 约束、policy reset，以及 artifact payload 到期前后
  statistics hash 不变。真实 PostgreSQL 回滚夹具确认同 release 会话可继续，新 scope 试图静默切换
  release 时在 INSERT 前失败关闭。全仓与 race 均通过。

2026-08-06 `WEB-002` 验证：

```sh
npm --prefix web run test
npm --prefix web run lint
npm --prefix web run build
git diff --check
```

- 全部通过；5 条 Node TypeScript 单测全部通过。production build 保持既有约 1.39 MB 主 chunk，Vite
  继续提示既有的 500 kB chunk size warning，本任务未增加运行时依赖，也未改页面视觉。

2026-08-06 `WEB-003` 验证：

```sh
npm --prefix web run test
npm --prefix web run lint
npm --prefix web run build
git diff --check
```

- 全部通过；8 条 Node TypeScript 单测通过。production build 保持既有约 1.43 MB 主 chunk，Vite 继续
  提示既有的 500 kB chunk size warning；未增加运行时依赖。
- 应用内浏览器固定 1280×720 与选定设计同状态对照，完整和中栏聚焦比较均无未解决 P0/P1/P2；另在
  1120×800 验证无横向溢出、证据面板正确移到下一行。取消、会话恢复、证据折叠、真实 API 失败关闭、
  静态证据隐藏和控制台 0 error 均已验证，详见 `design-qa.md`。

2026-08-07 `WEB-004` 验证：

```sh
go test ./internal/askdata/http -count=1
go test ./internal/askdata/... -count=1
go test ./... -count=1
npm --prefix web run test
npm --prefix web run lint
npm --prefix web run build
git diff --check
```

- 全部通过；前端共 11 条 Node TypeScript 单测。Go 用例覆盖公共证据投影、工件身份/run version 绑定、
  稳定澄清消费键、陈旧版本拒绝和竞争选择防分叉；前端用例覆盖证据完整性、确定性格式化与提交合同。
- production build 主 chunk 约 1.45 MB，Vite 继续提示既有 500 kB chunk size warning，未新增运行时依赖。
- 应用内浏览器固定 1280×720 与确认设计同状态完成完整和中栏聚焦对照，无未解决 P0/P1/P2；1120×800
  无横向溢出。已实测候选切换与右栏同步、继续、取消、会话恢复和折叠，DOM/可访问性检查确认原生
  radio group/label、region、按钮和 disclosure 语义，控制台 0 error。详见 `design-qa.md`。

2026-08-07 `WEB-005` 验证：

```sh
go test ./internal/askdata/http -count=1
go test ./... -count=1
npm --prefix web run test
npm --prefix web run lint
npm --prefix web run build
```

- 全部通过；前端共 14 条 Node TypeScript 单测。Go 用例覆盖只发布合格结果投影和拒绝不合格推荐形状；
  前端用例覆盖稳定 view 选择、不安全大整数回退 table 和精确格式化。
- production build 主 chunk 约 1.50 MB，Vite 仅提示 500 kB chunk size warning。
- 应用内浏览器固定 1280×720 与确认设计同状态完成完整和结果区聚焦对照，无未解决 P0/P1/P2；
  1120×800 document/viewport/scroll width 均为 1120。已实测趋势/渠道/明细 tab、查看明细、第 2 页、
  每页 10 行和证据 disclosure；DOM 检查确认 tab、table、select、pagination navigation 和按钮语义，
  最终干净会话控制台 0 error、0 warning。详见 `design-qa.md`。

2026-08-07 `WEB-006` 验证：

```sh
go test ./internal/askdata/http -count=1
go test ./... -count=1
npm --prefix web run test
npm --prefix web run lint
npm --prefix web run build
git diff --check
```

- 全部通过；前端共 16 条 Node TypeScript 单测。Go 用例覆盖严格反馈形状、终态/version 绑定、内容精确重放
  和变更升版；前端覆盖九类选项、权限类人工复核文案、正负反馈构造与公共 API 接线。
- production build 主 chunk 约 1.53 MB，Vite 仅提示既有 500 kB chunk size warning。
- 应用内浏览器固定 1280×720 与确认设计同状态完成两轮完整和聚焦对照，无未解决 P0/P1/P2；1120×800、
  800×800 均无横向溢出。已实测分类选择、下一步、返回保留、详情提交、成功关闭、有用反馈和显式关闭；
  DOM 检查确认原生 dialog、fieldset/legend、radio/label 和按钮语义，最终控制台 0 error、0 warning。
  原生 Escape 的 `cancel` handler 已实现，但应用内浏览器合成键盘事件未能触发浏览器默认 dialog 行为，
  这是本轮唯一残余自动化覆盖缺口。详见 `design-qa.md`。

2026-08-07 `TIME-001` 验证：

```sh
go test ./internal/askdata/registry -count=1
go test ./internal/askdata/... -count=1
go vet ./internal/askdata/...
go test ./... -count=1
ENV_FILE=.env ./scripts/migrate.sh
ENV_FILE=.env ./scripts/verify-database.sh
ASKDATA_INTEGRATION_ADMIN_DATABASE_URL=<local-admin-dsn> \
  go test ./internal/askdata/registry \
  -run TestTimeContractDatabaseGuardsCertificationImmutabilityAndReleaseClosure \
  -count=1 -v
./scripts/ci-check.sh
git diff --check
```

- 全部通过；`000225_askdata_time_contract` 已在本地开发控制库登记。`verify-database.sh` 确认两张新表
  强制 RLS、所有新外键含 tenant、API 角色有受控 DML、worker 无写权限，且激活 API 仍故意不存在。
- 单元测试覆盖严格 JSON round-trip、unknown field、四层策略 source、非法时区、缺失/非当前日历、
  8 类粒度、hash 稳定性、认证和 Go Release 闭包。独立 admin 回滚事务集成测试在无业务 fixture 的空库上
  验证 `TIME_CALENDAR_REQUIRED`、`TIME_CONTRACT_MISSING`、`TIME_CONTRACT_VERSION_IMMUTABLE` 与数据库
  Release 闭包；未生成或保留任何企业日历数据。
- 既有依赖真实 current PUBLISHED DWS/ADS 的 importer integration 在当前空业务库仍会按设计 skip；TIME-001
  的独立集成用例不依赖该 fixture 并已通过。全仓 Go 测试、askdata vet、CI 静态检查和 diff whitespace
  检查均通过。

2026-08-07 `TIME-002` 验证：

```sh
go test ./internal/askdata/compiler ./internal/askdata/ir ./internal/askdata/ircontract -count=1
go test ./internal/askdata/... -count=1
go vet ./internal/askdata/...
go test ./... -count=1
./scripts/ci-check.sh
git diff --check
```

- 全部通过。时间编译测试覆盖自然月/季/年、MTD T+1、LAST_COMPLETE、闰日同比、3 月 31 日环比的
  CLAMP/SKIP、纽约 DST 和四月起始财政季度；60 组属性用例确认所有成功区间 `end > start`，且
  `SAME_DAY_COUNT` 当前期/对比期天数恒等。
- IR builder 测试确认相对时间表达式和粒度进入 canonical IR；查询适配测试确认 current/baseline 参数来自
  `ResolvedTimeSpec`。全仓回归未发现既有绝对时间工件、查询编译、验证或运行链路退化。
- 本任务没有数据库迁移、外部服务调用或页面改动；没有写入或猜测 `HUMAN-007` 尚未提供的企业日历值。

2026-08-07 `TIME-003` 验证：

```sh
go test ./internal/askdata/answer ./internal/askdata/compiler ./internal/askdata/http
go test ./...
go vet ./...
cd web && npm test && npm run build && npm run lint
./scripts/ci-check.sh
git diff --check
```

- 全部通过。共享 fixture 共 20 组；Go/TypeScript 五个展示字段逐字符相等，专项测试覆盖裁剪、无对比和
  LAST_COMPLETE。HTTP 测试确认不信任 payload 提供的 `timeSpec`，并从校验后的 `resolvedTimeSpec` 重建。
- 应用内浏览器在真实本地登录和 ACTIVE“企业经营”领域下验证 1280×720 结果状态；结果 disclosure 与证据
  时间 section 的展开/收起均可用，最终控制台 0 error、0 warning。1120×800 下 document scroll width =
  viewport width = 1120，无重叠和横向溢出。
- 用户确认的方案 2、实现截图、完整/聚焦对照、响应式截图与逐轮 P2 修正记录见 `design-qa.md` 和
  `design-qa-artifacts/time-003-*`；最终 `final result: passed`。前端 production build 仅有既有的大 chunk warning。

2026-08-07 `SNAP-001` 验证：

```sh
go test ./...
go vet ./...
ENV_FILE=.env ./scripts/migrate.sh
ENV_FILE=.env ./scripts/verify-database.sh
ENV_FILE=.env ./scripts/verify-warehouse.sh
./scripts/ci-check.sh
git diff --check
```

- 全部通过。专项测试覆盖 Worker 必须先登记 snapshot 再构建、完成时传递业务 watermark、连续 10 次同
  schema 刷新 Release 仍 ACTIVE、schema 变化才 STALE，以及 FAIL 快照警告/阻断两种查询策略。
- 数据库验证以回滚 fixture 证明最新已完成读取会忽略开始时间更晚但未完成的刷新、能返回最新 FAIL 状态，
  且完成快照不可修改；迁移、RLS、完成通知、质量 FK 分离和控制面索引均通过静态与真实 SQL 断言。
- Warehouse 验证通过；time watermark 只允许来自 `DATE|DATETIME` 输出字段。`SnapshotService` 的依赖合同
  只有 control reader，mock 断言读取路径没有 warehouse 连接。

2026-08-07 `TIME-004` 验证：

```sh
go test ./internal/askdata/compiler ./internal/askdata/validator
go test ./...
go vet ./...
./scripts/ci-check.sh
git diff --check
```

- 全部通过。专项测试覆盖 `FULL/TRUNCATED/NONE`、多模型最小水位、`end == available` 为 FULL、
  SAME_DAY_COUNT/SAME_CALENDAR_RANGE 对比期同步裁剪、缺失水位失败关闭和非空用户提示。
- `NONE` 用例断言 EXPLAIN/仓库查询计数为 0；绕过覆盖门禁的带时间计划同样在 EXPLAIN 前失败关闭。
  本任务无数据库迁移、页面改动或外部服务调用。

2026-08-07 `ADD-001` 验证：

```sh
go test ./internal/askdata/...
go test ./...
go vet ./...
ENV_FILE=.env ./scripts/verify-database.sh
ASKDATA_INTEGRATION_ADMIN_DATABASE_URL=<local-admin-dsn> \
  go test ./internal/askdata/registry \
  -run TestAdditivityDatabaseChecksRemainIndependentOfApplicationGate -count=1
./scripts/ci-check.sh
git diff --check
```

- 全部通过；`000226_askdata_metric_additivity` 已在本地开发控制库登记。数据库验证确认 20 个新增字段和
  两层共 12 个核心 CHECK/FK 均存在，管理员回滚事务进一步证明：缺失事实、旧枚举、半可加缺算法和
  不可加错误限制会被数据库独立拒绝，字段齐全的 `FULLY_ADDITIVE` 认证指标可写入且没有残留夹具。
- 单元测试逐项覆盖五个规定错误码、suggestion 不能绕过认证、完整 DRAFT 可认证，以及 Release 对多个缺失
  对象的整包拒绝和稳定清单。全仓回归还验证导入器只写 suggestion，旧 `ADDITIVE` SQL fixture 已改为新
  事实词汇和完整认证字段。
- Down migration 在存在未确认事实时主动拒绝有损回滚；正常回滚只对非空权威事实恢复旧枚举，不会把
  suggestion 倒灌为事实。本任务没有页面改动或外部服务调用。

2026-08-07 `ADD-003` 验证：

```sh
go test ./internal/askdata/compiler ./internal/askdata/validator ./internal/dataset ./internal/querycompiler -count=1
go test -race ./internal/askdata/compiler ./internal/askdata/validator -count=1
go test ./...
go vet ./...
ENV_FILE=.env ./scripts/verify-database.sh
./scripts/ci-check.sh
git diff --check
```

- 全部通过。专项测试穷举 0/1/2/3 个分组维度的 8 种组合，逐棵检查比率 Dataset AST 的 DIVIDE 位于
  聚合之上且 denominator 是 `NULLIF(AGGREGATE,0)`；编译 SQL 不出现对比率的外层 `AVG`。去重用例确认
  目标 grain 生成 `COUNT(DISTINCT ...)`，不产生 `SUM(COUNT(...))`。
- 半可加用例覆盖按月 `PERIOD_END`、按季 `PERIOD_BEGIN` 和跨两年无时间分组的
  `PERIOD_AVERAGE`，并断言成员/时间过滤全部进入内层快照汇总。混合单位、混合币种、缺少半可加声明、
  不可加维度折叠和非法后聚合均返回稳定代码；Query Artifact 的重哈希不允许伪造 totals 标记。
- SQL Validator 专项验证受控 ordered aggregate；本地 PostgreSQL 直接执行
  `(ARRAY_AGG(value ORDER BY time DESC NULLS LAST))[1]` 返回最新时间点值。数据库全量验证无回归；当前无
  READY 真实语义 Release，因此既有 Postgres Resolver integration 按设计 skip，未冒充真实业务数据测试。
- 本任务无迁移、页面改动或外部模型调用。

2026-08-07 `ADD-004` 验证：

```sh
go test ./internal/askdata/compiler ./internal/askdata/validator -count=1
go test -race ./internal/askdata/compiler ./internal/askdata/validator -count=1
go test ./...
go vet ./...
npm --prefix web test
npm --prefix web run lint
npm --prefix web run build
ENV_FILE=.env ./scripts/verify-database.sh
./scripts/ci-check.sh
git diff --check
```

- 全部通过。Go 专项覆盖结果列治理元数据、精确长 decimal、无分组重算 SQL、预算从 2/3 扣到 3/3、
  3/3 时不再产生查询，以及 float 重算结果失败关闭；compiler/validator race 无数据竞争。
- Web 共 25 项测试通过，其中 ADD-004 契约测试覆盖三类可加性、问数/报告/导出函数引用一致、
  `0.1 + 0.2 = 0.3`、超安全整数精确求和、Manifest 可加性门禁和非完全可加图表不可用。
- Production build 通过；Vite 仍报告既有单 bundle 大于 500 kB 的非阻断 warning。本任务未新增页面、
  数据库迁移或外部模型调用。

2026-08-07 `IMPORT-001` 验证：

```sh
go test ./internal/askdata/registry/import -count=1
go test -race ./internal/askdata/registry/import ./cmd/worker -count=1
ASKDATA_INTEGRATION_DATABASE_URL=<local-app-dsn> \
ASKDATA_INTEGRATION_ADMIN_DATABASE_URL=<local-admin-dsn> \
ASKDATA_INTEGRATION_WORKER_DATABASE_URL=<local-worker-dsn> \
  go test ./internal/askdata/registry/import -run TestPostgresImport -count=1 -v
go test ./... -count=1
go vet ./...
ENV_FILE=.env ./scripts/migrate.sh
ENV_FILE=.env ./scripts/verify-database.sh
./scripts/ci-check.sh
git diff --check
```

- 全部通过；`000227` 已登记到本地控制库。专项单元测试覆盖全部合法边、8 条非法边、CSV 文件 hash/断点
  行号、尾随 JSON 拒绝、确定性失败关闭，以及 10,000 行中断后从 1,500 行继续并最终形成 20 个 500 行批次。
- 三角色数据库 integration 使用临时双租户 fixture，实测重复上传返回同一 ID、7 条非法数据库跳转被拒、
  非 DRAFT 创建器整批回滚、1+2 行部分/剩余提交最终进入 `COMMITTED`、跨租户读取只返回 not found，以及
  `VALIDATED -> WITHDRAWN`；夹具和审计均在测试后清理。
- 数据库全量验证确认两表强制 RLS、状态/行 trigger、幂等约束、lease 函数 SECURITY DEFINER + 固定
  search path，以及 API/Worker/Connection Test 三角色最小权限。本任务没有页面、外部模型或业务数据写入。

2026-08-07 `IMPORT-002` 验证：

```sh
go test ./internal/askdata/registry/import ./internal/askdata/http -count=1
go test -race ./internal/askdata/registry/import ./internal/askdata/http -count=1
ASKDATA_INTEGRATION_DATABASE_URL=<local-app-dsn> \
ASKDATA_INTEGRATION_ADMIN_DATABASE_URL=<local-admin-dsn> \
ASKDATA_INTEGRATION_WORKER_DATABASE_URL=<local-worker-dsn> \
  go test ./internal/askdata/registry/import -run 'TestPostgresImport|TestPostgresTemplateCatalog' -count=1 -v
go test ./... -count=1
go vet ./...
ENV_FILE=.env ./scripts/migrate.sh
ENV_FILE=.env ./scripts/verify-database.sh
./scripts/ci-check.sh
git diff --check
```

- 全部通过。合同测试以独立硬编码期望核对 12 类列名，并逐类生成 CSV/XLSX；XLSX 校验三 sheet、引用
  确定性顺序和枚举下拉。生成模板写入样例数据后由实际 `FileRowSource` 重新解析，首条数据 row 为 1。
- HTTP 测试覆盖成功下载、安全响应头、域越权、重复/多余参数、小写资产类型和非法格式；三角色数据库
  integration 继续通过，真实 PostgreSQL catalog 对空业务域返回空引用。没有页面、迁移或业务数据写入。

2026-08-07 `IMPORT-003` 验证：

```sh
go test ./internal/askdata/registry/import ./internal/askdata/http -count=1
go test -race ./internal/askdata/registry/import ./internal/askdata/http -count=1
ASKDATA_INTEGRATION_DATABASE_URL=<local-app-dsn> \
ASKDATA_INTEGRATION_ADMIN_DATABASE_URL=<local-admin-dsn> \
ASKDATA_INTEGRATION_WORKER_DATABASE_URL=<local-worker-dsn> \
  go test ./internal/askdata/registry/import -run TestPostgresImport -count=1 -v
go test ./... -count=1
go vet ./...
ENV_FILE=.env ./scripts/migrate.sh
ENV_FILE=.env ./scripts/verify-database.sh
./scripts/ci-check.sh
git diff --check
```

- 全部通过。专项测试覆盖 17 个治理错误码、12 类合法行、L1 短路、同批引用、A↔B 公式环、兼容性、
  层级断层、fanout、可加性、时间合同、画像高基数、影响警告仍为 VALID，以及每个错误均可通过数据库
  错误对象约束。报告 XLSX 的原始列 + 四个修复列已由真实模板解析器 round-trip。
- 上传 API 测试覆盖内容寻址对象键、服务端 MIME、大小/扩展名/路径拒绝、域越权与 multipart 歧义；HTTP
  报告覆盖安全头和参数拒绝。app/worker 双角色真实 catalog、报告行读取、跨租户 not found 及既有 IMPORT-001
  integration 全部通过；敏感成员原值权限未放宽。本任务没有页面、外部模型或正式业务数据写入。

2026-08-07 `IMPORT-004` 验证：

```sh
go test ./internal/askdata/... -count=1
go test -race ./internal/askdata/registry/import ./internal/askdata/registry ./internal/askdata/http -count=1
ASKDATA_INTEGRATION_DATABASE_URL=<local-app-dsn> \
ASKDATA_INTEGRATION_ADMIN_DATABASE_URL=<local-admin-dsn> \
ASKDATA_INTEGRATION_WORKER_DATABASE_URL=<local-worker-dsn> \
  go test ./internal/askdata/registry/import -count=1 -v
go test ./... -count=1
go vet ./...
ENV_FILE=.env.example ./scripts/migrate.sh
ENV_FILE=.env.example ./scripts/verify-database.sh
./scripts/ci-check.sh
git diff --check
```

- 全部通过。真实 PostgreSQL 覆盖 12 类 DRAFT 落表、HIERARCHY 多行合并、提交中途失败零残留、影响确认、
  整批认证原子拒绝/成功与 N 条对象审计、同稳定对象新增版本，以及自由 DRAFT、Release 引用、已认证三类撤回。
- `000228` 已 down→up 回放；数据库验证确认新增权威版本表、Release 合同、成员哈希解析函数、app/admin/
  worker 最小权限和 `COMMITTED -> WITHDRAWN` 状态边。没有页面、外部模型或正式业务数据写入。

2026-08-07 `IMPORT-005` 验证：

```sh
go test ./internal/askdata/... -count=1
go test -race ./internal/askdata/registry/import ./internal/askdata/registry ./internal/askdata/http -count=1
ASKDATA_INTEGRATION_DATABASE_URL=<local-app-dsn> \
ASKDATA_INTEGRATION_ADMIN_DATABASE_URL=<local-admin-dsn> \
ASKDATA_INTEGRATION_WORKER_DATABASE_URL=<local-worker-dsn> \
  go test ./internal/askdata/registry/import -count=1 -v
go test ./... -count=1
go vet ./...
ENV_FILE=.env.example ./scripts/migrate.sh
ENV_FILE=.env.example ./scripts/verify-database.sh
./scripts/ci-check.sh
git diff --check
```

- 全部通过。专项测试核对 12 类每一列和每个输入值、单资产工作簿经真实导入解析器 round-trip 后 hash
  不变、当前 metric v2 与 Release 固定 v1 内容不同，以及 CONFIDENTIAL MEMBER 和关联 MEMBER TERM 均省略。
- HTTP 覆盖同步附件、异步 202、状态、鉴权下载、过期 410、跨域和参数歧义；三角色 PostgreSQL 覆盖
  精确版本 manifest、租约完成、可重试失败、永久失败、跨租户 not found 和 Worker 无任务表权限。
  `000242` 已 down→up 回放，000229 仍保留给 RETAIN-001；数据库总校验确认 RLS、状态守卫、固定 search
 path 的 SECURITY DEFINER 函数及最小权限。没有页面、外部模型或正式业务数据写入。

2026-08-07 `TERM-001` 验证：

```sh
go test ./internal/askdata/registry ./internal/askdata/http -count=1
ASKDATA_INTEGRATION_DATABASE_URL=<local-app-dsn> \
ASKDATA_INTEGRATION_ADMIN_DATABASE_URL=<local-admin-dsn> \
ASKDATA_INTEGRATION_WORKER_DATABASE_URL=<local-worker-dsn> \
  go test ./internal/askdata/... -count=1
ASKDATA_INTEGRATION_DATABASE_URL=<local-app-dsn> \
ASKDATA_INTEGRATION_ADMIN_DATABASE_URL=<local-admin-dsn> \
ASKDATA_INTEGRATION_WORKER_DATABASE_URL=<local-worker-dsn> \
  go test -race ./internal/askdata/registry ./internal/askdata/registry/import ./internal/askdata/http -count=1
go test ./... -count=1
go vet ./...
ENV_FILE=.env.example ./scripts/verify-database.sh
ENV_FILE=.env.example ./scripts/ci-check.sh
git diff --check
```

- 全部通过。应用角色真实 CRUD 覆盖完整版本字段、目标引用和 PENDING 重置；真实 PostgreSQL 认证覆盖
  同优先级不同目标返回候选且零变更、不同优先级缺 note 拒绝/有 note 通过并审计 shadow，以及相邻但不
  重叠的生效期均可认证。认证失败后仍遵守同一稳定身份最多一个 DRAFT 的既有导入合同。
- 单元/HTTP 覆盖受限正则合法/危险构造、已取消上下文的超时保护、64KiB 输入上限、负向上下文矛盾及
  conflict 候选结构化响应。全量数据库验证继续确认强制 RLS、Release 只接受 APPROVED 词条与最小权限。
  无新增迁移、页面、外部模型或正式业务数据写入。

2026-08-07 `TERM-002` 验证：

```sh
go test ./internal/askdata/understanding ./internal/askdata/understanding/dictionarysearch \
  ./internal/askdata/understanding/dictionarypostgres ./internal/askdata/search -count=1
go test ./internal/askdata/understanding \
  -run TestDictionaryTenThousandTermsWarmMatchUnderOneMillisecond -count=3 -v
go test -race ./internal/askdata/understanding \
  ./internal/askdata/understanding/dictionarysearch ./internal/askdata/search -count=1
ASKDATA_INTEGRATION_DATABASE_URL=<local-app-dsn> \
ASKDATA_INTEGRATION_ADMIN_DATABASE_URL=<local-admin-dsn> \
  go test ./internal/askdata/understanding/dictionarypostgres -count=1 -v
ASKDATA_INTEGRATION_DATABASE_URL=<local-app-dsn> \
ASKDATA_INTEGRATION_ADMIN_DATABASE_URL=<local-admin-dsn> \
ASKDATA_INTEGRATION_WORKER_DATABASE_URL=<local-worker-dsn> \
ASKDATA_INTEGRATION_WAREHOUSE_DATABASE_URL=<local-warehouse-reader-dsn> \
ASKDATA_INTEGRATION_WAREHOUSE_ADMIN_DATABASE_URL=<local-warehouse-admin-dsn> \
  go test ./internal/askdata/... -count=1
go test ./... -count=1
go vet ./...
ENV_FILE=.env.example ./scripts/verify-database.sh
ENV_FILE=.env.example ./scripts/ci-check.sh
git diff --check
```

- 全部通过。专项覆盖最长/priority/字典序重叠、残余 PREFIX/SUFFIX/REGEX、负向原因、有效期、角色、
  Release hash 缓存替换、全半角/大小写/数字单位 span 与取消传播；10,000 词条、500 字问句的 warmed
  median 正常构建连续三次 `<1ms`，竞态构建另行通过功能检查。
- 真实 PostgreSQL 夹具确认 app-role/RLS 只加载 Release 固定的 APPROVED 词条，排除未发布和 PENDING
  候选，并验证 target code、角色命中和负向裁剪。全量数据库与 CI 门禁继续通过；未运行外部模型、页面
  或写入正式业务数据。

2026-08-07 `KPI-001` 验证：

```sh
go test ./internal/askdata/registry ./internal/askdata/registry/import \
  ./internal/askdata/http -count=1
go test -race ./internal/askdata/registry ./internal/askdata/registry/import \
  ./internal/askdata/http -count=1
ASKDATA_INTEGRATION_DATABASE_URL=<local-app-dsn> \
ASKDATA_INTEGRATION_ADMIN_DATABASE_URL=<local-admin-dsn> \
  go test ./internal/askdata/registry \
    -run 'TestKPIBundleAdminRoundTripAndReferenceValidation|TestPostgresKPIBundleMatcherPinsAndRollsBackReleaseVersion' \
    -count=1 -v
ASKDATA_INTEGRATION_DATABASE_URL=<local-app-dsn> \
ASKDATA_INTEGRATION_ADMIN_DATABASE_URL=<local-admin-dsn> \
ASKDATA_INTEGRATION_WORKER_DATABASE_URL=<local-worker-dsn> \
ASKDATA_INTEGRATION_WAREHOUSE_DATABASE_URL=<local-warehouse-reader-dsn> \
ASKDATA_INTEGRATION_WAREHOUSE_ADMIN_DATABASE_URL=<local-warehouse-admin-dsn> \
  go test ./internal/askdata/... -count=1
go test ./... -count=1
go vet ./...
ENV_FILE=.env.example ./scripts/verify-database.sh
ENV_FILE=.env.example ./scripts/ci-check.sh
git diff --check
```

- 全部通过。单元/HTTP 覆盖 item 0/9、HEADLINE、order、Component Manifest、CRUD 路由、Release
  metric/dimension 闭包，以及唯一命中、低 margin 澄清、无命中和版本回退；竞态检查无数据竞争。
- 真实 PostgreSQL 覆盖非认证指标、跨域指标、不兼容维度、完整 CRUD/认证预检，以及 app-role/RLS 下
  同一 Bundle 的 v1/v2 随双 Release 精确切换。全量数据库与 CI 门禁继续通过；未执行新迁移、外部模型、
  页面或正式业务数据写入。

2026-08-07 `RETAIN-001` 验证：

```sh
go test ./internal/askdata/registry ./internal/askdata/compiler \
  ./internal/askdata/orchestrator ./internal/askdata/http -count=1
go test -race ./internal/askdata/registry ./internal/askdata/compiler \
  ./internal/askdata/orchestrator ./internal/askdata/http -count=1
ASKDATA_INTEGRATION_DATABASE_URL=<local-app-dsn> \
ASKDATA_INTEGRATION_ADMIN_DATABASE_URL=<local-admin-dsn> \
ASKDATA_INTEGRATION_WORKER_DATABASE_URL=<local-worker-dsn> \
  go test ./internal/askdata/registry ./internal/askdata/orchestrator \
    -run 'TestReleaseRetentionPostgresLifecycleAndRLS|TestPostgresStoreQuestionLifecycleResumeAndPinnedRelease' \
    -count=1 -v
ASKDATA_INTEGRATION_DATABASE_URL=<local-app-dsn> \
ASKDATA_INTEGRATION_ADMIN_DATABASE_URL=<local-admin-dsn> \
ASKDATA_INTEGRATION_WORKER_DATABASE_URL=<local-worker-dsn> \
ASKDATA_INTEGRATION_WAREHOUSE_DATABASE_URL=<local-warehouse-reader-dsn> \
ASKDATA_INTEGRATION_WAREHOUSE_ADMIN_DATABASE_URL=<local-warehouse-admin-dsn> \
  go test ./internal/askdata/... -count=1
go test ./... -count=1
go vet ./...
ENV_FILE=.env.example ./scripts/verify-database.sh
ENV_FILE=.env.example ./scripts/ci-check.sh
git diff --check
```

- 全部通过。`000229` 先执行 down→up 回放并再次迁移到最新定义；专项覆盖引用验证、幂等恢复、计数、
  完整受影响清单、24 月保留期、投影清理顺序与外部失败关闭，以及 Retained 编译 Plan Hash 一致性。
- 真实 PostgreSQL 覆盖认证资产自动引用、ACTIVE→SUPERSEDED→RETAINED、跨租户 RLS、保留期前拒绝、
  到期 RETIRED、投影清理后注册表/manifest/编译合同完整；真实 Question Store 覆盖已有 run 重放/恢复与
  新 run 拒绝。未运行外部模型、页面或写入正式业务数据。

2026-08-07 `PROJ-002` 验证：

```sh
go test ./internal/askdata/registry ./internal/askdata/orchestrator \
  ./internal/askdata/http -count=1
go test -race ./internal/askdata/registry ./internal/askdata/orchestrator \
  ./internal/askdata/http -count=1
ASKDATA_INTEGRATION_DATABASE_URL=<local-app-dsn> \
ASKDATA_INTEGRATION_ADMIN_DATABASE_URL=<local-admin-dsn> \
  go test ./internal/askdata/registry \
    -run TestProjectionGuardPostgresFourHashRLSAndInvalidation -count=1 -v
ASKDATA_INTEGRATION_DATABASE_URL=<local-app-dsn> \
ASKDATA_INTEGRATION_ADMIN_DATABASE_URL=<local-admin-dsn> \
  go test ./internal/askdata/orchestrator \
    -run TestPostgresStoreQuestionLifecycleResumeAndPinnedRelease -count=1 -v
ASKDATA_INTEGRATION_DATABASE_URL=<local-app-dsn> \
ASKDATA_INTEGRATION_ADMIN_DATABASE_URL=<local-admin-dsn> \
ASKDATA_INTEGRATION_WORKER_DATABASE_URL=<local-worker-dsn> \
ASKDATA_INTEGRATION_WAREHOUSE_DATABASE_URL=<local-warehouse-reader-dsn> \
ASKDATA_INTEGRATION_WAREHOUSE_ADMIN_DATABASE_URL=<local-warehouse-admin-dsn> \
  go test ./internal/askdata/... -count=1
go test ./... -count=1
go vet ./...
ENV_FILE=.env.example ./scripts/verify-database.sh
ENV_FILE=.env.example ./scripts/ci-check.sh
git diff --check
```

- 全部通过。专项覆盖四个投影各自 hash/status/缺行差异、Release READY/ACTIVE 限制、缓存命中、TTL、
  revision/显式失效、结构化 HTTP 错误和错误上下文失败关闭；竞态检查无数据竞争。
- 真实 PostgreSQL app-role/RLS 覆盖完整四哈希放行、管理连接制造的跨进程 GRAPH 漂移在下一次断言即时
  拦截、主动失效和跨租户不可见；真实 Question Store 覆盖守卫调用及 mismatch ERROR 事件的持久化、
  hash 链重放和状态不推进。未执行新迁移、外部模型、页面或正式业务数据写入。

2026-08-07 `SEC-001` 验证：

```sh
go test ./internal/policy ./internal/askdata/security
go vet ./internal/policy ./internal/askdata/security
test "$(gofmt -l cmd internal | wc -l | tr -d ' ')" = "0"
go test ./...
```

- 全部通过。`authorization_integration_test.go` 在配置 AskData app/admin DSN 时额外验证真实 PostgreSQL
  tenant RLS、ACTIVE roles、domain membership、pinned release/hash 和三类 READY projection；默认全量
  Go 门禁不依赖外部数据库，缺少 DSN 时只跳过该 integration。
- 本任务未执行数据库迁移、Compose、外部模型或页面验证，也未读取或回显 API key。

2026-08-07 `SEC-002` 验证：

```sh
go test ./internal/askdata/security ./internal/askdata/search \
  ./internal/askdata/cognition ./internal/askdata/toolhost \
  ./internal/askdata/orchestrator
test "$(gofmt -l cmd internal | wc -l | tr -d ' ')" = "0"
go test ./...
go vet ./...
git diff --check
```

- 安全 fixture 全部得到 BLOCK/REFUSE，工具升级、作用域切换、任意 SQL/nGQL 和预算扩张均失败关闭；
  编排层断言被拦截调用不会进入 Tool Host。V-GO-ALL、vet 与 diff 检查全部通过。
- 本任务未执行数据库迁移、Compose、外部模型或页面验证，也未读取或回显 API key。

2026-08-07 `GRAPH-006` 验证：

```sh
go test -race ./internal/askdata/graph ./internal/askdata/toolhost \
  ./internal/askdata/orchestrator ./internal/askdata/http -count=1
ASKDATA_INTEGRATION_ADMIN_DATABASE_URL=<local-admin-dsn> \
ASKDATA_INTEGRATION_APP_ROLE=report_app \
  go test ./internal/askdata/graph \
    -run TestPostgresFallbackAndCertifiedCacheAgainstRuntimeRole -count=1 -v
go test ./...
go vet ./...
cd web && npm test && npm run lint && npm run build
ENV_FILE=.env.example ./scripts/verify-database.sh
./scripts/ci-check.sh
git diff --check
```

- 全部通过；前端 25 项测试通过，Vite 仅保留既有的大 chunk 提示。真实 PostgreSQL 用例覆盖图投影陈旧时
  注册表 fallback 仍可用、认证 cache 仍失败关闭、app-role RLS、Release 外对象不泄露和 SAFE 关系裁剪。
- Docker Web 已重建到当前工作区版本，并在已认证的 `127.0.0.1:5173/ask-data` 完成 Design QA；证据文件
  位于 `design-qa-artifacts/graph-006-*`。未调用外部模型、未写入正式业务数据，也未回显本地凭据。

2026-08-07 `SEC-004` 验证：

```sh
go test -race ./internal/policy ./internal/askdata/security \
  ./internal/askdata/graph ./internal/askdata/search ./internal/ai -count=1
test "$(gofmt -l cmd internal | wc -l | tr -d ' ')" = "0"
go test ./...
go vet ./...
git diff --check
```

- 全部通过。安全集覆盖 64 租户并发缓存键、跨 scope/release/freshness 负例、图 cache/fallback 毒化、
  vector error 携带毒化 hit 以及主/备用模型失败审计；竞态检查无数据竞争。
- 本任务未执行数据库迁移、Compose、页面或真实外部模型验证，也未读取或回显 API key。

2026-08-07 `SEARCH-006` 验证：

```sh
go test -race ./internal/askdata/search ./cmd/worker -count=1
go vet ./internal/askdata/search ./cmd/worker
ASKDATA_INTEGRATION_DATABASE_URL=<local-app-dsn> \
ASKDATA_INTEGRATION_WORKER_DATABASE_URL=<local-worker-dsn> \
ASKDATA_INTEGRATION_ADMIN_DATABASE_URL=<local-admin-dsn> \
  go test ./internal/askdata/search \
    -run TestPostgresRecallAuditRecordsLabelFreeSampleAndComparesExact -count=1 -v
go test ./...
go vet ./...
ENV_FILE=.env.example ./scripts/verify-database.sh
./scripts/ci-check.sh
git diff --check
```

- 全部通过。`000243` 在本地完成 down→up 回放；真实 PostgreSQL 用例覆盖三角色列级权限、RLS、
  SECURITY DEFINER 样本写、app 原始向量不可读、30 条 halfvec 的 exact/ANN 对照、三种 K 聚合落库与
  fixture 清理。专项 race 未发现数据竞争。
- Backend 镜像已重建，Worker 强制替换后为 healthy，启动日志无错误；未调用外部 embedding/LLM，
  未保存或输出问句原文、凭据或正式业务数据。

2026-08-08 `NLU-007` 验证：

```sh
go test ./internal/askdata/understanding ./internal/askdata/binding \
  ./internal/askdata/ir ./internal/askdata/compiler -count=1
go test ./internal/askdata/testfixture ./internal/askdata/evaluation \
  ./internal/askdata/toolhost ./cmd/askdata-eval -count=1
go test ./...
go vet ./...
./scripts/ci-check.sh
git diff --check
```

- 全部通过。测试覆盖模型省略领域后的 Policy Pin、foreign domain 失败关闭、单值 Policy Fact、
  多领域 scope/错误 Graph domain 在 beam 前拒绝、IR 缺失/多值/篡改拒绝，以及 Fixture Runner/IR
  等价比较不漏检 `domainId`。
- 本任务未执行数据库迁移、Compose、页面或外部模型调用；先前领域澄清设计预览已按用户最新口径废弃，
  没有作为项目资产或实现依据落地。

2026-08-08 `WEB-011 + NLU-009` 验证：

```sh
go test ./internal/askdata/understanding ./internal/askdata/http -count=1
go test ./... -count=1
go vet ./...
npm --prefix web test
npm --prefix web run lint
npm --prefix web run build
git diff --check
```

- 全部通过；NLU 专项覆盖 15 类各 3 条、弱“列出”歧义、定义零数据查询、非法 LLM 枚举、正确拒答
  统计、严格公开 Block 工件和原问句不落工件。前端共 32 条测试，Vite 仅保留既有大 chunk 提示。
- 应用内浏览器在 1280×720 验证 `SCOPE_DETAIL_LIST → 申请弹窗`：按钮只在明细拒答出现，点击后切换
  “我的申请”，原问题、来源 run、语义上下文与固定“企业经营”领域正确预填；页面 `scrollWidth` 等于
  1280，弹窗无遮挡。此前主从工作区已完成 1120×800、720×900 和真实 API 空字段状态验收。
- `git diff --check` 通过。全仓 gofmt-only 门禁发现并行工作区已有的
  `internal/askdata/orchestrator/budget.go`、`runner.go` 尚未格式化；为避免覆盖非本任务修改没有代为重写，
  本任务触及的 Go 文件均已单独 gofmt。

2026-08-08 `ORCH-008` 验证：

```sh
go test ./internal/config ./internal/askdata/orchestrator -count=1
go test ./internal/askdata/... -count=1
ASKDATA_INTEGRATION_DATABASE_URL='postgres://report_app:...@127.0.0.1:5432/...' \
ASKDATA_INTEGRATION_ADMIN_DATABASE_URL='postgres://report_admin:...@127.0.0.1:5432/...' \
  go test ./internal/askdata/orchestrator -run 'Test(Postgres|QuestionStateMatrix)' -count=1
./scripts/migrate.sh
git diff --check
```

- 全部通过。`000246` 已应用到开发库，随后在单事务中执行 down→up→rollback；新上限未使用时可逆，
  存在 10 Tool/6 正式查询/30 秒 Run 时 down 会失败关闭。真实 Store 测试同时断言 INSERT 与后续更新的
  `budget_consumed_json` 精确等于 LLM/Tool/正式/验证查询、step、elapsed 和 exhausted 标量。
- 未调用外部模型、未读取或输出 API key、未改动页面，也没有写入正式业务数据。

2026-08-08 `RPT-CONTRACT-001` 验证：

```sh
go test ./internal/report -count=1
go vet ./internal/report
go test ./... -count=1
go vet ./...
git diff --check
```

- 全部通过。契约测试覆盖三份示例 round-trip、Schema 闭合与核心 `$defs`、未知字段、24 层、20 页面、
  5 MB、SQL/script/onclick 与恶意字符串、悬空组件引用，以及 page/section/block/zone/slot/component 六类
  命名空间重复 ID。
- 本任务没有页面改动、数据库迁移、外部服务调用或业务数据写入，也未修改并行工作区的 ORCH 文件。

2026-08-08 `RPT-CONTRACT-002` 验证：

```sh
go test ./internal/report/... -count=1
go vet ./internal/report/...
go test ./... -count=1
go vet ./...
for file in api/schemas/component-manifest-v1.schema.json \
  internal/report/template/manifests/*.json; do jq empty "$file"; done
git diff --check
```

- 全部通过。13 个 MVP Manifest、未知 option、错误枚举/整数、维度/度量/角色越界、最小尺寸、三份报告
  集成、注册表深拷贝、minor 新增必填项和 major 缺/有 migrator 均有自动化覆盖。
- 本任务无页面、迁移、外部服务或业务数据写入；原本未跟踪的 Manifest Schema 已在本任务范围内补全，
  其他并行工作区文件未改写。

2026-08-08 `RPT-CONTRACT-003` 验证：

```sh
go test ./internal/report/... -count=1
go vet ./internal/report/...
go test ./... -count=1
go vet ./...
jq '.["$defs"].operation.oneOf | length' api/schemas/report-operation-v1.schema.json
git diff --check
```

- 全部通过；Schema 分支数固定为 41。专项测试逐项覆盖 41 个 payload 的成功解析/round-trip 和错误结构，
  以及全量 Bundle/AI/删除上限与固定错误码。
- 本任务无页面、迁移、外部服务或业务数据写入，也未修改其他并行工作区文件。

2026-08-08 `QUERY-009` 验证：

```sh
jq empty api/schemas/query-plan-bundle-v1.schema.json
go test -race ./internal/askdata/compiler ./internal/askdata/orchestrator -count=1
go test ./... -count=1
go vet ./...
./scripts/ci-check.sh
git diff --check
```

- 全部通过。专项测试覆盖三计划全成功、单项执行失败、权限裁剪、六计划峰值并发精确为 4、以 100ms
  领域覆盖收紧并验证硬 deadline 保留已完成计划（默认预算另固定为 30 秒）、DRAFT/临时组合拒绝、6/8 上限、Release/model 漂移、
  Schema 上限、公开错误不泄漏与 Bundle hash 篡改；race 未发现数据竞争。
- 未新增数据库迁移、页面或外部服务调用，未读取凭据、执行真实数仓查询或写入业务数据。B08 下一项
  `QUERY-011` 的依赖现已全部满足。

2026-08-08 `QUERY-011` 验证：

```sh
go test -race ./internal/askdata/validator ./internal/askdata/http -count=1
go test ./... -count=1
go vet ./...
./scripts/ci-check.sh
git diff --check
```

- 全部通过。专项测试逐项覆盖 P1～P6、Q1、P1+Q1、矛盾子集失败关闭、权限数量隐私、服务端 artifact
  信任边界、PARTIAL 报告拒绝和质量告警放行；race 未发现数据竞争。
- 无数据库迁移、页面或显著视觉状态，不触发设计稿门禁；无外部服务调用、真实报告写入或业务数据写入。
  B08 查询编译与执行新增链已闭环。`ANS-001` 已在下一批完成，当前 P0 依赖清理链为
  `ANS-002 → ANS-003/ORCH-007`。

2026-08-08 `ANS-001`、`RPT-CONTRACT-004` 验证：

```sh
go test ./internal/askdata/shared ./internal/askdata/answer ./internal/report/insight \
  ./internal/askdata/orchestrator -count=1
go vet ./internal/askdata/shared ./internal/askdata/answer ./internal/report/insight \
  ./internal/askdata/orchestrator
go test ./... -count=1
go vet ./...
git diff --check
```

- 专项测试覆盖 Answer/Evidence/Insight 三份 Schema round-trip、未知字段、Unicode citation 越界/重叠、
  rowKey 百分号编码与 group-by 顺序、两类 Evidence 来源、decimal/float 边界、Answer 六类与 Insight 九类
  stale、Evidence hash 绑定、降级 Answer、人工编辑 Insight 和数据库追加式工件合同。
- PostgreSQL integration 需要既有 `ASKDATA_INTEGRATION_*` 环境变量时执行；测试用真实 Answer Artifact
  进入 ANSWER completion，Resume 后重放同一 payload，并以管理员子事务证明 UPDATE 被不可变 trigger
  拒绝。本批次没有页面、迁移、浏览器操作、外部服务调用或业务数据写入，不触发设计确认门禁。

2026-08-08 `ANS-002` 验证：

```sh
go test -race ./internal/askdata/answer ./internal/report/insight -count=1
go vet ./internal/askdata/answer ./internal/report/insight
go test ./... -count=1
go vet ./...
git diff --check
```

- 专项测试覆盖六个固定失败码各至少三条负例、中文数值与量词等价、百分比/百分点隔离、精确容差边界、
  显式同比派生、随机 cell 组合拒绝、幻觉成员和贡献模式；race 未发现数据竞争。
- 报告侧先验证不可变 Evidence Bundle，再与问数侧进入同一 `VerifyNarrative`；同 Evidence 幻觉数字返回
  完全相同的结构化 `VerifyReport`。全仓 Go test 与 vet 通过；无迁移、页面或外部服务调用。

2026-08-09 `ANS-003` 验证：

```sh
go test -race ./internal/askdata/answer ./internal/askdata/orchestrator ./internal/askdata/http -count=1
go test ./... -count=1
go vet ./...
npm --prefix web test
npm --prefix web run lint
npm --prefix web run build
git diff --check
```

- 专项覆盖首轮通过、失败后去拒绝原文重试、连续两次失败、L3 默认关闭、跨 run/release/result 证据拒绝、
  公开投影只出已核验文字/稳定提示、SSE 事件对应、非 PARTIAL 降级加入报告与前端快照。
- 用户确认方案 2 后完成 Product Design 实现与阻塞式 QA；1280×720 完整/聚焦对照、1120×800
  响应式、说明折叠/展开、查看校验依据、视图切换、重新生成与浏览器控制台全部通过，
  `design-qa.md` 最终结果为 `passed`。
- `000247` 为 `ANSWER_VERIFYING`、`OUT_OF_SCOPE` 和审计约束的迁移来源；本任务不调用外部模型，不读取或
  输出 API key，不写入正式业务数据。

2026-08-09 `ORCH-007` 验证：

```sh
go test -race ./internal/askdata/orchestrator ./internal/askdata/http ./internal/askdata/answer -count=1
go test ./... -count=1
go vet ./...
git diff --check
```

- 六类验收覆盖一次通过、失败后结构化重生成通过、连续失败清空未核验叙述、非法
  `EXECUTING → ANSWERED`、LLM/step 预算耗尽直接降级，以及 SSE 失败序列与断线游标恢复。
- 无新增迁移、页面或显著视觉状态，不触发设计确认门禁；不调用外部模型、不读取凭据、不写入业务数据。

2026-08-09 `ORCH-009` 验证：

```sh
go test -race ./internal/platform/idempotency ./internal/askdata/http \
  ./internal/datarequest ./internal/report/http -count=1
go test ./... -count=1
go vet ./...
git diff --check
# 000248 down → up + RLS/lifecycle assertions in one ROLLBACK transaction
```

- 覆盖缺键、规范等价重放、异 body 冲突、真实并发 IN_FLIGHT、24 小时过期、跨 tenant/actor、5xx 释放、
  data-request 生产接线和 Report wrapper；业务 mock 在重放/并发场景只执行一次。
- PostgreSQL 回滚事务覆盖 app actor RLS、COMPLETED 24 小时保留、到期删除、response hash 篡改拒绝与
  owner replay；`000248` down/up 对称。无页面或显著视觉状态，不触发设计确认门禁。

2026-08-09 `RPT-DB-001` 验证：

```sh
go test ./internal/report/store -count=1
go vet ./internal/report/store
# 一次性临时库：000251 → 000234 down，000234 → 000238 up
# 使用真实 report_app 运行并发/RLS/不可变/近 5 MiB integration，随后删除临时库
git diff --check
```

- 并发相同 expected revision 恰好一个成功、一个 `ErrRevisionConflict`；修订号连续且 base revision 正确。
- owner、VIEW-only、跨 tenant 三类身份路径通过；version/revision UPDATE/DELETE 均返回 SQLSTATE 55000。
- 5,141,122 字节 canonical 定义写入/读回 hash 一致；测试同时捕获并修复 JSONB 文本重排问题。
- 无页面或显著视觉状态，不触发设计确认门禁；临时数据库已自动删除，未写入共享业务数据。

2026-08-09 `RPT-DB-002` 验证：

```sh
go test -race ./internal/report/template ./internal/report/store -count=1
go test ./... -count=1
go vet ./...
sh -n scripts/migrate.sh scripts/verify-database.sh
git diff --check
# 临时库 A：回滚后重放新版 000234/000235/000236，真实 report_app integration
# 临时库 B：克隆已部署的 13 个旧 placeholder，应用 000256 后运行同一 integration
```

- 四类子模板与报告组合按版本 ID 解析正确，五份 canonical JSON/hash 一致；跨 tenant 不可见。
- 13 个完整 embedded manifest 全部可解码、hash 正确且二次 seed 幂等；旧库 placeholder 前向升级通过。
- `ACTIVE → DEPRECATED → RETAINED` 合法，逆向/跳级、非法 SemVer 与引用中版本删除均由数据库拒绝。
- 迁移链测试同时修复 `000253.down` 先删函数后删依赖触发器的回滚顺序；两个临时库均已删除。
- 无页面或显著视觉状态，不触发设计确认门禁；未写入共享业务数据。

2026-08-10 `RPT-DB-003` 验证：

```sh
go test -race ./internal/report/compiler ./internal/report \
  ./internal/report/store ./cmd/report-admin -count=1
REPORT_FRESH_MIGRATION_ADMIN_DATABASE_URL='postgres://<admin>/postgres' \
REPORT_FRESH_MIGRATION_APP_DATABASE_URL='postgres://<app>/postgres' \
  go test ./internal/report/store \
  -run TestFreshReportMigrationsAndStoreLifecycle -count=1 -v
./scripts/migrate.sh
./scripts/verify-database.sh
go test ./... -count=1
go vet ./...
sh -n scripts/migrate.sh scripts/verify-database.sh
git diff --check
```

- nonce 数据库从空库依序应用全部 up migration，再以真实 `report_app` 执行完整 Store 生命周期；owner、
  VIEW-only、跨 tenant、同事务回滚、近 5 MiB version、四表精确索引、缺失版本索引回填与 SQLSTATE
  55000 均通过，临时库在退出路径强制删除。
- 属性测试随机生成 200 组 Report Definition，验证组件全覆盖且只落位一次、依赖去重、全部声明
  DatasetVersion 与 Semantic IR 版本依赖、稳定排序；重建结果与增量维护逐行一致。
- 全库 V-DB 在收敛过程中同时发现并修复三类已部署链问题：5 条 AskData 外键缺 tenant 坐标
  （`000262`）、`000249` 重建搜索触发器时丢失 SECURITY DEFINER/search_path（`000263`），以及早期
  `000256` 仍保留平台组件 `FOR ALL` 可写策略（`000264`）。本地幂等过期索引的非仓库 schema 漂移也
  已恢复为 `000248` 定义的无条件 `(expires_at,id)` 索引。
- 无页面或显著视觉状态，不触发设计确认门禁；未调用外部模型，测试夹具业务数据均在回滚事务或临时库。

2026-08-10 `RPT-DB-004` 验证：

```sh
go test -race ./internal/report/reportai ./internal/report/insight ./internal/report/store -count=1
ASKDATA_INTEGRATION_DATABASE_URL='postgres://report_app:...@127.0.0.1:5432/...' \
ASKDATA_INTEGRATION_ADMIN_DATABASE_URL='postgres://report_admin:...@127.0.0.1:5432/...' \
  go test -race ./internal/report/reportai \
  -run TestPostgresReportAIAuditInsightAppendOnlyAndRLS -count=1 -v
./scripts/verify-database.sh
go test ./... -count=1
go vet ./...
sh -n scripts/migrate.sh scripts/verify-database.sh
git diff --check
```

- 四类 AI run、有效/拒绝操作、拒绝码、摘要字段白名单、终态单向不可变均通过真实 PostgreSQL 验证。
- Evidence UPDATE 与 Insight DELETE 被 SQLSTATE 55000 拒绝；两次生成和一次人工编辑得到一条 CURRENT、
  两条 STALE，且稳定 artifact ID、版本行 UUID、编辑人和时间均符合合同。
- owner、VIEW-only、跨 tenant/actor 的可见与写入边界通过；`000265` down/up 在同一事务演练后回滚，
  从空库全迁移和 V-DB 同步通过。无页面或显著视觉状态，不触发设计确认门禁。

2026-08-10 `RPT-DB-005` 验证：

```sh
go test -race ./internal/report/sharing ./internal/report/http ./cmd/worker -count=1
ASKDATA_INTEGRATION_DATABASE_URL='postgres://report_app:...@127.0.0.1:5432/...' \
ASKDATA_INTEGRATION_ADMIN_DATABASE_URL='postgres://report_admin:...@127.0.0.1:5432/...' \
ASKDATA_INTEGRATION_WORKER_DATABASE_URL='postgres://report_worker:...@127.0.0.1:5432/...' \
  go test -race ./internal/report/sharing \
  -run TestPostgresShareAuthorizationExpiryRevocationAndRLS -count=1 -v
REPORT_FRESH_MIGRATION_ADMIN_DATABASE_URL='postgres://report_admin:...@127.0.0.1:5432/postgres' \
REPORT_FRESH_MIGRATION_APP_DATABASE_URL='postgres://report_app:...@127.0.0.1:5432/postgres' \
  go test ./internal/report/store -run TestFreshReportMigrationsAndStoreLifecycle -count=1 -v
./scripts/verify-database.sh
go test ./... -count=1
go vet ./...
sh -n scripts/migrate.sh scripts/verify-database.sh
git diff --check
```

- INTERNAL_USER/INTERNAL_GROUP、无权限令牌不提权、跨 tenant、固定/当前版本读取和访问计数通过。
- Worker 尚未运行时的实时过期、worker role 的有界过期标记、撤销立即拒绝和缓存失效合同通过；原 token
  只返回一次且响应不泄露 hash。
- 180 天上限、无效 principal、分享身份字段不可变与 trigger 权限边界分别返回预期 SQLSTATE；266～268
  down/up 在同一事务演练后回滚，空库全迁移、全仓测试、V-DB 通过。无页面或显著视觉状态。

2026-08-10 `RPT-001` 验证：

```sh
go test ./internal/report ./internal/report/compiler ./internal/report/compiler/migrate \
  ./internal/report/template ./internal/report/store -count=1
go test -race ./internal/report ./internal/report/compiler ./internal/report/compiler/migrate \
  ./internal/report/template ./internal/report/store -count=1
go test ./internal/report/compiler -run '^$' \
  -bench '^BenchmarkNormalizeNearFiveMegabytes$' -benchtime=5x -count=1
go test ./... -count=1
go vet ./...
sh -n scripts/migrate.sh scripts/verify-database.sh
git diff --check
```

- 幂等、语义顺序/nil 等价、值变化改 hash、步骤 6～10 分阶段短路并阶段内累积、XSS 清洗、V1 minor 与
  显式 V1→V2 迁移器均通过；调用方定义未被修改，规范对象键为字典序。
- 近 5 MB 基准 5 次平均 `51,572,033 ns/op`（约 51.6 ms，87.26 MB/s），显著低于 500 ms 门槛。
- 专项 race、全仓测试/vet、脚本语法和差异检查全部通过。无页面或显著视觉状态，不触发设计确认门禁。

2026-08-10 `RPT-002` 验证：

```sh
go test -race ./internal/report/operation ./internal/report/store ./internal/report/http -count=1
ASKDATA_INTEGRATION_DATABASE_URL='postgres://report_app:...@127.0.0.1:5432/...' \
ASKDATA_INTEGRATION_ADMIN_DATABASE_URL='postgres://report_admin:...@127.0.0.1:5432/...' \
  go test -race ./internal/report/store \
  -run 'TestPostgresStoreOperationUndoRedoAndAIGuards|TestPostgresStoreReportLifecycleConcurrencyImmutabilityAndRLS' \
  -count=1 -v
REPORT_FRESH_MIGRATION_ADMIN_DATABASE_URL='postgres://report_admin:...@127.0.0.1:5432/postgres' \
REPORT_FRESH_MIGRATION_APP_DATABASE_URL='postgres://report_app:...@127.0.0.1:5432/postgres' \
  go test ./internal/report/store -run TestFreshReportMigrationsAndStoreLifecycle -count=1 -v
go test ./... -count=1
go vet ./...
./scripts/verify-database.sh
sh -n scripts/migrate.sh scripts/verify-database.sh
git diff --check
```

- 41 类操作、逐类逆操作、120 步随机全撤销、失败 index/原子性、模板/主题 snapshot、slot merge/split 和
  多级 Undo/Redo 通过；HTTP 409/ApplyError 结构化细节通过。
- 真实库确认对象 EDIT 与 AI 独立能力同时生效，拒绝/越 scope 不增 revision；连续撤销/重做、主对象设置
  同步、模板恢复、并发一成功一冲突、RLS 与不可变链通过。`000269` down/up 事务回放和空库全迁移通过。
- 全仓 test/vet、V-DB、脚本语法和差异检查全部通过。无页面或显著视觉状态，不触发设计确认门禁。

2026-08-10 `RPT-003` 验证：

```sh
go test ./...
go vet ./...
npm --prefix web test
npm --prefix web run build
npm --prefix web run lint
./scripts/verify-database.sh
git diff --check
```

- 全仓 Go test/vet、39 条 Web 测试、TypeScript 编译、Vite build、ESLint、V-DB 和差异检查全部通过；
  Vite 仅保留既有的大 chunk 提示。
- 布局专项覆盖随机暴力对照和 300 分块时限、逻辑/像素坐标、紧凑策略、四高度、空区模板优先级、
  四项 merge 拒绝、派生 provenance、严格 Split、移动端四模式/抽屉/Manifest 策略与 Go/TS 共享夹具。
- 本任务只提供布局编译器和前端实时预览纯函数，没有新增页面或显著视觉状态，不触发设计确认门禁。

2026-08-10 `RPT-007` 验证：

```sh
go test ./internal/report/runtime ./internal/report/publication -count=1
go test ./...
go vet ./...
./scripts/verify-database.sh
git diff --check
```

- 六类稳定绑定拒绝、RETAINED 历史重编译、固定语义身份、逻辑 Dataset 请求、无 SQL、hash 去重、策略
  隔离、查看者权限重应用、并发/超时/行数上限与发布依赖单位检查全部通过。
- 全仓 Go test/vet 和 V-DB 通过；无页面或显著视觉状态，不触发设计确认门禁。

2026-08-10 `RPT-004` 验证：

```sh
go test ./internal/report/... -count=1
ASKDATA_INTEGRATION_DATABASE_URL='postgres://report_app:...@127.0.0.1:5432/...' \
ASKDATA_INTEGRATION_ADMIN_DATABASE_URL='postgres://report_admin:...@127.0.0.1:5432/...' \
  go test ./internal/report/store \
  -run TestPostgresStoreReportLifecycleConcurrencyImmutabilityAndRLS -count=1 -v
go test ./...
go vet ./...
./scripts/migrate.sh
./scripts/verify-database.sh
git diff --check
```

- 发布专项、真实 PostgreSQL 历史修订/不可变版本/Release 引用保留、全仓 test/vet、幂等迁移、V-DB 与
  差异检查全部通过。迁移记录与权限发生的本地漂移已由 `migrate.sh` 重新同步，复跑 V-DB 通过。
- `000271_data_request_governance` 已先占用，RPT-004 使用无冲突的
  `000272_report_publication_version_pins`；本地已真实应用，未回写或泄漏 `.env` 凭据。
- 本任务没有页面或显著视觉状态，不触发设计确认门禁；下一项为同样无页面的 `RPT-005`。

2026-08-10 `RPT-005` 验证：

```sh
go test ./internal/report/publication ./internal/report/http ./internal/report/store -count=1
ASKDATA_INTEGRATION_DATABASE_URL='postgres://report_app:...@127.0.0.1:5432/...' \
ASKDATA_INTEGRATION_ADMIN_DATABASE_URL='postgres://report_admin:...@127.0.0.1:5432/...' \
  go test ./internal/report/store \
  -run TestPostgresStoreReportLifecycleConcurrencyImmutabilityAndRLS -count=1 -v
perl -e '...down + up in one transaction...' \
  migrations/000273_report_rollback_integrity.down.sql \
  migrations/000273_report_rollback_integrity.up.sql | psql ...
go test ./... -count=1
go vet ./...
./scripts/verify-database.sh
git diff --check
```

- 正常/幂等回滚、原因/权限/目标拒绝、依赖失败 issues、连续回滚、HTTP 失败清单、真实数据库 pointer/
  immutable lineage/伪造 definition/外键均通过；`000273` down→up→rollback 事务回放通过。
- 本任务没有页面或显著视觉状态。下一项 `RPT-006` 包含八种组件状态、错误边界和懒加载的真实运行时
  视觉，必须先提交设计页面并由用户确认，再落地 Go/React 实现。

### 2.103 报告资产治理产品缺口补全（已确认并落地核心链路）

- 用户在 `RPT-006` 运行时设计评审中指出：原范围直接进入“看报告”，缺少“现在已经做了哪些报告”以及
  权限、修改、发布、下架等资产管理入口。该反馈已确认为真实产品缺口，不并入运行时内部勉强解决。
- 产品基线新增“报告资产中心与生命周期”，技术基线新增 §5.35；TODO 新增 `RPT-013` 后端资产治理和
  `WEB-RPT-007` 报告资产中心。`RPT-013` 已完成，B11 当前剩余 18 项。
- 生命周期固定推导为 `DRAFT_ONLY/PUBLISHED/CHANGED/OFFLINE`。下架是保留草稿、修订、不可变版本和
  当前版本指针的软下架，但普通 runtime、指定版本 URL 和分享都必须返回 `REPORT_OFFLINE`；上架前重新
  校验固定制品和依赖。权限仍与领域、数据集、行列级策略取交集，不能放大数据可见范围。
- 用户已明确确认方案 2“视觉资产库 + 轻量治理检查器”作为报告默认入口，并再次澄清：点击方案 2 的
  “新建报告”必须进入其提供的完整报告页面，而不是另起空白表单。实现固定为：
  `GET /reports?snapshot=assets` → 点击“新建报告” → `/reports/new?snapshot=runtime-draft`；目标页显示
  `待发布/r1/保存草稿/发布报告`，不伪造已发布版本。

### 2.104 `RPT-013` 与报告双页面实现验证

- 后端新增 `internal/report/asset/*`、`000275_report_asset_governance` 和报告 HTTP 路由：固定当前领域的
  资产列表、scope/生命周期/Owner/类型/搜索/游标、服务端 `allowedActions`，用户/角色六动作权限，统一
  资产事件时间线，以及带原因的 archive/restore。恢复已发布报告前重新校验对象制品 hash、规范 JSON、
  固定组件版本和数据依赖；恢复从未发布报告回到 `DRAFT_ONLY`。
- `PostgresStore.GetVersion` 统一在读取当前版或指定版前检查主对象状态；分享服务同样复用该入口，因此
  下架报告稳定返回 `REPORT_OFFLINE`，不能经版本 URL 或分享绕过。archive/restore、grant/revoke 已纳入
  actor-scoped 共享幂等中间件。
- 前端新增 `ReportAssetsPage`、`ReportRuntimePage`、真实 ECharts 预览与八态组件。资产列表接服务端筛选
  和游标，权限面板即时授权/撤销，生命周期面板展示事件；下架/上架原因必填，上架明确展示制品/组件/
  依赖重校验。运行时具有 Block + Component 双层错误边界，`NO_PERMISSION` 不泄露绑定标题。
- 视觉证据：`design-qa-artifacts/report-assets-final-live.png`、`report-runtime-final-viewport.png`、两张
  `*-comparison-final.png` 和 1120×800 响应式截图；`design-qa.md` 已记录 `final result: passed`。
- 验证：Web lint、41 条测试、production build；全仓 Go test/vet；空库全迁移；真实 PostgreSQL
  授权/越权、shared 列表、并发下架、offline runtime、恢复、撤销与时间线；迁移本地应用及 V-DB 均通过。
- 边界：`RPT-013` 已完成。`RPT-006` 的不可变加载/计划/执行器与视觉已实现，但生产页面尚未把批量查询
  结果绑定到全部组件；`WEB-RPT-007` 仍缺 Owner/类型/更新时间前端筛选、发布恢复中专态、版本差异和
  后续设计器/发布页面，因此两项保持未完成，避免把确认稿 fixture 当作生产结果。

### 2.105 跨模块后端补全（统一工作箱、调度、生命周期、配置与决策）

- API 新增/补全：`/api/v1/work-items` 分类分页及 `/{type}/{id}` 详情、`/api/v1/conversations` 与 run
  分页/搜索/pin/unpin/archive、`/api/v1/auth/me`、保存问题游标、报告 follows、report schedules/
  subscriptions/deliveries/backfill/read、user lifecycle preview/execute/retry、runtime config
  draft/submit/approve/reject/rollout/rollback，以及 Decision list/detail/create/submit/approve/action/outcome/
  evidence-prefill/approval-policies。
- Worker 新增报告 schedule/delivery、runtime rollout 与 decision due/overdue；tenant discovery 只经
  SECURITY DEFINER 返回有待处理工作的 tenant，连接测试角色无执行权。
- 数据库迁移 `000281`～`000297` 已在本地真实应用；所有新表 tenant/domain scoped，控制面表强制 RLS。
  Decision Evidence/options/events/approval facts、delivery/lifecycle/runtime 事件保持 append-only；决策、
  行动、runtime config 和最后管理员规则均有数据库守卫。
- 真实库测试覆盖：Decision 完整审批→行动→复盘→关闭/重开、Evidence 固定与跨域拒绝；工作项分类/
  详情/通知消失；报告计划 DST、补跑、无权/下架、重复和已读；生命周期跨上下文转交/失败回滚/认证事实
  保护/最后管理员；runtime reject、自批拒绝、rollout/回滚；会话 pin/keyset/run 分页；report follow
  撤权/下架/幂等。
- 额外修复了四类由全量集成测试发现的旧回归：graph rebuild 命令 UUID 预校验、Top-N fixture 列绑定、
  Release Evaluation Gate `receipt_hash` PL/pgSQL 歧义，以及 KPI/可加性/数据集测试夹具与最新审计/Owner
  约束不一致；失败测试事务现在总会 rollback，不再阻塞 pool close。
- 最终验证通过：带 app/admin/worker 真实库的 `go test ./... -count=1`、新增模块 `go test -race`、
  `go vet ./...`、52 条 Web/共享合同测试、Web lint/TypeScript/Vite build、`ci-check.sh`、Compose 静态与
  Nebula 凭据隔离、主库 migrate/verify、warehouse verify、`000296/000297` down→up 回滚事务回放和
  `git diff --check`。Vite 仍有已登记 `PERF-001` 的主 chunk >500 kB 警告，不属于本轮后端范围。
- 未完成项严格限于前端页面/页面 E2E、HUMAN-001～015 业务输入、正式黄金样本，以及必须在目标拓扑
  实测和签署的容量/灾备/Shadow/Canary/Pilot；这些不应被本地软件测试标为完成。

## 4. 工作区注意事项

- 用户在本次实施前已有多项 `docs/*` 删除状态；这些文件未被恢复或修改。
- `ASK_DATA_TECHNICAL_DESIGN.md`、`ASK_DATA_CODEX_TODO.md` 为当前项目设计和执行事实源；
  `可信智能问数与智能报表一体化平台_*.md` 为产品与技术基线，其第五部分与前四部分冲突时以第五部分为准。
- TODO 已按功能区划分为 B01～B13 个板块（见 TODO §0）。领任务时先确认所属板块，再确认 Wave 与 Batch；
  不要把新任务放到任何板块之外。
- 新增迁移编号中 `000225` 已由 `TIME-001` 使用并完成，`000229` 已由 `RETAIN-001` 使用并完成，
  `000230` 已由 `SNAP-001` 使用并完成，`000242` 已由 `IMPORT-005` 使用并完成，`000243` 已由
  `SEARCH-006` 使用并完成，`000244` 已由 `NLU-008` 使用并完成，`000245` 已由 `DR-001` 用于事件
  单调序号修复，`000246` 已由 `ORCH-008` 用于全状态预算消费快照，`000247` 已由 `ANS-003` 用于
  `ANSWER_VERIFYING` 状态与审计约束，`000248` 已由 `ORCH-009` 用于共享幂等重放与清理；`000234` 已由
  `RPT-DB-001` 用于 Report V2 核心存储，`000235` 已由 `RPT-DB-002` 使用模板表，`000236` 已由
  `RPT-DB-003` 使用组件/依赖索引与引用保护，`000237` 已由 `RPT-DB-004` 使用 AI 运行/操作/证据/结论表，
  `000238` 已由 `RPT-DB-005` 使用无匿名分享记录；`000256` 为已应用旧 000235 的 hydration/SemVer 前向
  修复，`000261` 为早期索引表补齐对象级 RLS 与 tenant-aware 引用保护，`000262`～`000264` 分别修复
  历史跨租户外键、搜索触发器安全属性回退和旧平台组件可写策略，`000265` 为 AI 运行/操作生命周期、
  摘要白名单和触发器权限的前向修复，`000266/000268` 为分享生命周期/RLS/过期 Worker 与安全触发器，
  `000267` 修复早期 report version trigger 错绑，`000269` 为 RPT-002 登记独立 REPORT_AI_EDIT 能力，
  `000271` 已由 DR-002/003 使用数据申请治理，`000272` 已由 RPT-004 使用固定依赖与 Report Version
  Release 引用保留，`000273` 已由 RPT-005 使用回滚目标自外键与原因完整性；`000274` 已由配额运行时
  占用，`000275` 已由 `RPT-013` 实现报告资产事件、上下架完整性和列表索引，并已在本地开发库应用及通过 V-DB。
  `000281`～`000297` 已依次用于 Decision、工作箱、会话历史、报告调度、Owner Transfer、运行配置、
  报告关注及其安全/状态机前向修复，最后一个编号为 Release Evaluation Gate 回执变量歧义修复；不得
  复用这些编号。`000259/000260` 已存在于工作区并由后续报告任务接续，但不得据此
  把后续分享或导出任务标为完成；其余预留迁移继续按
  TODO §22.1 分配到具体任务，新 Schema 仍按 §22.2 分配，不得重复占用。
- 报表板块（B11）新建独立 Report V2 bounded context：不修改历史迁移，不假定旧报告表仍存在，
  不恢复 `000195` 删除的旧运行时。
- 不要恢复历史 `platform.semantic_*` 运行时；新控制面使用 `askdata` schema。
- `WEB-001` 方案 3、`WEB-003` 方案 1、`WEB-004` 方案 1、`WEB-005` 方案 1、`WEB-006` 方案 3、
  `TIME-003` 方案 2、`GRAPH-006` 方案 1、`NLU-008` 方案 1、`WEB-011` 方案 3、`ANS-003` 方案 2 与
  报告资产中心方案 2/用户提供的报告工作台均已取得确认。后续新增页面、流程或显著视觉状态仍须先出
  设计稿并取得用户确认；下一项
  `WEB-007` 的 `REG-006` 依赖已解除，当前仍受 `REL-005` 阻塞；依赖满足后仍须先出语义管理与发布页面设计稿。
  `WEB-010`～`013` 与全部 `WEB-RPT-*` 均需先过设计稿门禁。
- 本地开发验证使用 `.env.example`；不要把用户给出的 API key 补写到 `.env.example`。
- 当前机器存在 Git 忽略的 `.env` 以供运行；交接、日志和提交时不得回显其内容。
- Nebula 兼容 POC 使用 `./scripts/verify-nebula-poc.sh`；默认会创建并清理独立临时栈，
  不要把 `deployments/nebula-poc/compose.yaml` 当作正式部署配置。
- 正式开发图验收使用 `./scripts/verify-nebula-compose.sh`；脚本拒绝 canonical project/Space，
  通过 `deployments/nebula/verification.override.yaml` 删除应用 service `env_file`，不读取
  `.env`，只控制 project/Space nonce 严格对应的 `askdata-graph002-verify-*` 隔离项目并在退出时
  删除其独立卷。Go integration 还会核对 proxy endpoint 与 Compose ownership；不要把测试
  环境变量手工指向开发共享 Space。
- `nebula-init` 的环境变量凭据只获准用于本地开发；生产必须补齐 HUMAN-005 的 TLS、Secrets、
  副本、容量和备份方案。graphd 不应直接增加 `ports`，本地端口只由 init 后 proxy 暴露。
- 数据库 integration test 使用 `ASKDATA_INTEGRATION_DATABASE_URL`、
  `ASKDATA_INTEGRATION_ADMIN_DATABASE_URL` 和 `ASKDATA_INTEGRATION_WORKER_DATABASE_URL`
  显式开启；画像扫描另使用 `ASKDATA_INTEGRATION_WAREHOUSE_DATABASE_URL` 和
  `ASKDATA_INTEGRATION_WAREHOUSE_ADMIN_DATABASE_URL`，默认单元测试会跳过外部数据库。

## 5. 历史下一步快照（仅供追溯）

> 本节记录当时尚未实现的依赖关系，已被文首“2026-08-10 后端收敛交接”覆盖；不得再据此恢复已经
> 完成的阻塞或删除现有发布门禁。

### 5.1 立即可执行（不依赖人工业务输入）

1. `WEB-006`、`REG-006` 已完成。按 B10 顺序的下一项是 `WEB-007` 语义管理与发布页面，但其剩余依赖
   `REL-005` 尚未完成；`REL-005` 又被 `EVAL-005`、`REL-004`、`DB-008` 阻塞，当前停止在真实依赖卡点，
   不得提前出设计或编码。解除依赖后，仍须先提交页面设计稿
   并取得用户确认，再实现指标/维度/成员策略/术语/关系/release/投影/评测审查与显式激活确认。
2. `EVAL-003` 已完成；下一项 `EVAL-004` 依赖未提供的 `HUMAN-001～004` 和可用 DWS/ADS，当前不能
   进入真实端到端评测。`QUERY-007`～`011` 已完成 TopN/Other、关系 fanout 安全矩阵、认证 Bundle
   独立编译/校验/四并发/PARTIAL 聚合、权限隔离结果缓存、P1～P6/Q1 统一 outcome 和报告导出门禁，
   B08 新增链已闭环。`ANS-001/002/003` 已完成 Answer Artifact、共享叙述事实校验器与两次失败降级，
   `ORCH-007` 已完成预算内重生成与 SSE 可恢复闭环，`ORCH-009` 已完成跨问数/取数申请/报告共享幂等
   边界；`ANS-004` 仍由未完成的 `OBS-001` 阻塞。发布链的 `REL-001` 仍受需要人工黄金集的
   `SEARCH-005` 阻塞。
3. **Batch 7 口径闭环**的时间链 `TIME-001`～`004`、前置 `SNAP-001`、可加性三关 `ADD-001`、编译规则
   `ADD-003` 与统一结果合同 `ADD-004` 已完成。真实问数/报告页面视觉状态仍归属 `WEB-009`，届时必须先
   提交设计稿并取得用户确认。`ADD-002` 为独立
   P1 建议补录能力，包含指标中心批量确认视觉状态，实施其页面部分前同样必须先过设计稿门禁。
4. **Batch 8 资产建设产能**：`IMPORT-001`～`005`、`TERM-001/002` 与 `KPI-001` 已完成，12 类资产已具备模板、四层校验、
   权威 DRAFT 提交、整批认证、选择性撤回、当前/Release 对称导出，以及版本化业务词、冲突裁决、
   `REGEX_SAFE`、PENDING 候选隔离、Release 固定的确定性词典匹配与人工认证 KPI 默认答案组合。Batch 8
   主链已闭环；B05 的 `RETAIN-001` Release 引用计数、RETAINED 保留态、历史重放和可重建投影清理也已
   完成，`PROJ-002` 四投影哈希一致性运行门禁也已闭环，`GRAPH-006` 图不可用六行降级矩阵、熔断、
   观测、Evidence/SSE 与用户确认的证据视觉状态也已完成；`SEARCH-006` 的 label-free 查询样本、
   ANN/Exact `recall@10/20/30`、小集合 exact 路由与模型/维度门禁也已完成；`NLU-007` 已按用户确认的
   “登录后进入问数前选定领域”口径完成 Policy Pin、Binding 单域门禁和 IR/编译/评测全链固定；`NLU-008`
   已完成会话 Release Pin、显式漂移确认、澄清预算冻结/恢复、超时 Worker 与用户确认的方案 1 可见状态。
   `DR-001 → WEB-011 → NLU-009` 已按既定顺序闭环：主动/拒答申请共用同一入口，领域固定，预填无结果
   行，15 类范围白名单和正确拒答统计已完成。B07 当前无遗留；后续叙述层仍归属 `ANS-*`。
5. **Batch 10 报表合同与核心存储**：`RPT-CONTRACT-001`～`004`、`RPT-DB-001`～`005`、`RPT-001`～`005`、`RPT-007` 与 `RPT-013` 已完成，Answer
   citations、Evidence cellRefs、`IsStale`、报告主对象、草稿乐观锁、不可变修订/版本、对象权限 RLS、
   四类模板组合、13 个组件 manifest、组件/依赖索引、同事务维护、不可变版本索引与管理重建已闭环。
   四类 AI 运行、脱敏摘要、操作拒绝审计、Evidence 不可变、Insight 追加版本与人工编辑留痕，以及
   无匿名分享、令牌只定位不授权、实时/后台双重过期和撤销均已闭环；DSL 12 步规范化、分阶段校验、
   富文本清洗、字典序 JSON、稳定 hash、41 类原子 Operation、差异冲突、多级 Undo/Redo、24 列桌面网格、
   区域高度、slot merge/split、独立移动布局、双绑定校验、按查看者策略隔离的查询编排，以及固定 14 步
   发布、不可变对象制品、跨存储故障恢复、语义 Release 引用保留、重新发布式回滚，以及报告清单/
   权限/统一时间线/软下架和恢复校验后端闭环。用户已确认“报告资产中心方案 2 → 待发布报告工作台”，
   两页核心链路已落地；下一步继续收敛 `RPT-006` 的真实查询结果绑定，并在已确认资产中心内补齐
   `WEB-RPT-007` 剩余筛选/恢复中/版本入口，涉及新的设计器或发布页面时仍须按各自页面门禁确认。

### 5.2 治理边界（已确认，不得改动）

5. `DB-004` 以 READY/投影基础完成，ACTIVE 原子切换归属 `REL-005`；`REG-006` 只负责 DRAFT 管理 API，
   发布生命周期 endpoint 分属 `REL-001`～`REL-005`；Wave 5 未完成的配置任务已改号为 `OPS-005`。
   不要恢复旧边界或重复编号。
6. `askdata.activate_release` 必须继续不存在，直到 `DB-007` 评测门禁、`DB-008` 双人审批和
   `REL-005` 同时完成；Projector/Resolver 的完成不构成放宽激活门禁的理由。
7. 产品决策 D01～D04 已确认并写入设计文档：未结束周期默认 `MTD`；报表资产认证**单人审批**
   （语义 Release 激活仍为双人审批，两者不得混同）；报告分享**不允许匿名**；明细取数申请入口
   **在本平台内**。相关任务为 `TIME-001`、`FUSE-002`、`RPT-DB-005`、`DR-001`。

### 5.3 人工输入阻塞

8. `REG-005` 仍需要 `HUMAN-001`～`HUMAN-003`；`SEARCH-005` 仍需要 `HUMAN-002`～`HUMAN-004`，
   当前不能生成正式认证资产或宣称 Recall@K / 95% 准确率。
9. 新增人工门禁 `HUMAN-007`～`HUMAN-013`（业务日历与时间策略、指标可加性、报告模板与叙述规范、
    报表资产认证责任人、明细取数审批链、配额与成本策略、容量与压测目标）。未提供前，Codex 可以
    建设合同、校验器、导入工具和测试夹具，**但不得编造业务答案**。

### 5.4 排期风险提示

10. B11 报表板块 34 项中 `RPT-CONTRACT-001`～`004`、`RPT-DB-001`～`005`、`RPT-001`～`005`、`RPT-007` 与 `RPT-013` 已完成，仍有 18 项，是当前最大的建设面；其中 7 项前端任务需逐个通过设计稿
    门禁，排期时必须为设计评审预留时间，不能按纯编码估算。
11. `EVAL-011` 密封集分片轮换必须在首次运行密封集**之前**完成。一旦按旧方式反复使用整套密封集，
    密封性即不可恢复，95% 论证将失效。

## 6. 接手验证命令

```sh
git status --short
go test ./... -count=1
go test -race ./internal/askdata/... ./internal/report/... ./internal/datarequest/... -count=1
go vet ./...
./scripts/check-compose.sh
./scripts/migrate.sh
./scripts/verify-database.sh
git diff --check

# 以下为有对应外部环境时执行的专项验证。
./scripts/verify-nebula-compose.sh
./scripts/verify-nebula-poc.sh
./scripts/dev-services.sh status
ASKDATA_INTEGRATION_DATABASE_URL='postgres://report_app:...@127.0.0.1:5432/...' \
ASKDATA_INTEGRATION_ADMIN_DATABASE_URL='postgres://report_admin:...@127.0.0.1:5432/...' \
ASKDATA_INTEGRATION_WORKER_DATABASE_URL='postgres://report_worker:...@127.0.0.1:5432/...' \
  go test ./internal/askdata/... -count=1
ENV_FILE=.env.example ./scripts/verify-database.sh
ENV_FILE=.env.example ./scripts/verify-warehouse.sh
./scripts/ci-check.sh
npm --prefix web run lint
npm --prefix web run build
```
