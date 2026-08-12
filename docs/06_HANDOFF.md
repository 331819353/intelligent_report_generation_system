# 智能分析决策平台——实施 Handoff（6/6）

> 文档状态：接手交接事实源（活文档，交接时更新）
> 基准日期：2026-08-12
> 配套文档：[产品设计](./01_产品设计终稿.md)、[技术设计](./02_技术设计终稿.md)、[前端用户旅程](./03_前端用户旅程终稿.md)、[实施规划与验收](./04_实施规划与验收终稿.md)、[TODO](./05_TODO.md)

---

## 1. 接手必读

1. 四份终稿（01～04）是唯一设计基线；[05_TODO](./05_TODO.md) 是唯一任务事实源。
2. 一期交付顺序：M1 数据建模 → M2/M3 语义建模与 Release ACTIVE → M4 报告主链 → M5 报告资产进入对话 → M6 门禁签署。能力依赖上**报告与问数是语义层的同级消费者**，报告资产只是问数的增强先验。
3. 决策与行动、统一任务中心、订阅分发、问数写回现已完成前后端和真实链路回归，不得回退为仅后端能力或静态页面。
4. 生产准确率状态：尚未评测，**不得宣称达到 95%**。

## 2. 当前运行环境

- 当前本地开发运行态：API `127.0.0.1:8080`、Web `127.0.0.1:5174`、Connector `127.0.0.1:8090`、Nebula loopback proxy `127.0.0.1:9669`；API/Worker、PostgreSQL、Warehouse PostgreSQL、MinIO、Nebula metad/storaged/graphd/proxy 已用于真实链路回归。
- 本地控制库已包含企业经营领域、真实发布数据集、ACTIVE Release、问数运行、报告、决策、反馈工单和运行配置测试数据；仍不得把这些回归数据当作生产业务事实。
- 本地开发验证使用 `.env.example`；机器上存在 Git 忽略的 `.env` 供运行，交接、日志和提交时不得回显其内容，也不得把 API key 写入 `.env.example`。

## 3. 已完成基线（按板块）

任务级完成清单不再逐项罗列；以下板块状态为 2026-08-13 权威结论，`go test ./...`、数据库/数仓权限校验、前端测试、lint 与生产构建全部通过：

| 板块 | 状态 | 关键缺口 |
|---|---|---|
| B01 平台底座与权限 | 安全基线、统一工作箱、领域准入、平台治理、用户权限与个人设置页面完成 | 生产组织 Owner 转交演练 |
| B02 数据接入与元数据 | 数据源连接、测试、审批、元数据资产化、明细取数与受控导出全链完成 | 外部 MySQL/Oracle 生产环境签署 |
| B03 数仓建模与物化 | 仓库基线、数据快照版本、增量水位、检查点恢复、历史变更全量回退与 SLA 诊断/重试完成 | 目标数仓容量签署 |
| B04 语义资产治理 | 后端主链、读取/导入/导出、时间/可加性（含建议补录与批量确认）、词典、KPI、数据质量规则绑定、语义管理与发布页面完成 | SEM-CTX-001 剩余三项字段级缺口 + HUMAN-008/015 业务输入 |
| B05 语义发布与投影 | READY、评测门禁、双人审批、原子激活、保留策略完成 | 真实业务审批/评测事实 |
| B06 检索与语义图谱 | 主链、降级矩阵、Recall@K 审计完成 | 正式人工黄金集 |
| B07 问句理解与联合绑定 | 全链完成（领域 Pin、Release Pin、范围白名单、拒答） | — |
| B08 查询编译与执行 | 全链完成（可加性编译、TopN、认证 Bundle、outcome） | — |
| B09 编排与问数 API | Loop/API/SSE、确定性链、答案校验重生成、预算/幂等与真实 Worker 运行完成 | — |
| B10 问数工作台前端 | 会话、澄清、结果、证据、反馈、导出、分享、加入报告、形成决策与取数申请完成 | KPI Bundle 专用视觉可继续增强 |
| B11 报表引擎与融合 | 模板/空白/AI 创建、组件增删布局、发布/回滚、运行、导出、分享、订阅与问数写回完成 | 生产模板内容由业务方维护 |
| B12 评测、反馈与运营 | 控制面、密封集轮换、误差预算、反馈工单、主动学习候选与运营页面完成 | 正式黄金样本 |
| B13 运维、可观测与成本 | 运行配置页面、配额/成本、DR/容量合同完成 | 目标环境签署 |
| B14 决策协同 | 分析到决策、审批、行动、复盘、关闭与统一任务中心全链完成 | 生产治理规则签署 |

## 3.1 兼容性变更：Report Definition 增加 `zone.order`（2026-08-12）

现在 `Zone.Order` 是独立的结构字段（同 `Page.Order`、`Section.Order`），
`emptyPriority` 只保留其字面含义。新建或编辑后的区域必须使用正数顺序；变更前完全
不含该字段的块作为不可变 V1 兼容形态继续读取，并以原有 `emptyPriority + id` 稳定排序。
同一块混用新旧顺序会被拒绝，避免产生歧义。

**处置结论**：不得重写历史已发布制品。兼容读取保留原字节和 Hash，分享、导出、订阅
与报告资产抽取继续锚定同一不可变版本；编辑操作会一次性把该块全部区域升级为显式
顺序。迁移 `000337` 已将兼容修复前耗尽重试的有效抽取任务重新入队，并把已删除来源
归档为 `REPORT_ASSET_SOURCE_GONE`。

## 3.2 全产品真实链路回归收据（2026-08-13）

- 数据主链：真实 MySQL 8.4 连接测试、发布、元数据导入、脱敏样本、结构差异、下游冻结、安全下架与全历史凭据撤销完成；数仓全量 → 增量水位 → 质量失败不切快照 → 诊断重试恢复完成。
- 智能主链：数据集与语义 Release 进入 ACTIVE 后，真实问数运行完成 `RECEIVED → ANSWERED`，结构化数值、叙述、证据和置信门禁全部通过。修复了两个真实 Worker 断点：确定性工具间延用已消耗预算，以及从受信 EXPLAIN 估算扫描字节，不再把成功查询误判为成本记账失败。
- 报告主链：已验证问数结果真实写入报告草稿，通过桌面/移动预览和 6 项发布预检后发布 v2；运行时四个组件全部 `READY`，问数数值与证据在发布版中可追溯展示。
- 决策主链：真实领域成员发起决策，领域负责人批准，发起人创建行动、开始执行、提交完成凭证、进入复盘、按“证据不足”完成定性复盘并关闭，完整审批/行动/复盘轨迹均保留。
- 身份边界：平台管理员只拥有租户治理控制面，不再被领域列表或认证服务隐式转换为业务成员；没有真实成员关系时，业务路由会在上游直接回到平台管理，避免决策、审批等深层操作中途 `FORBIDDEN`。
- UI 回归：登录页与 22 个业务/平台根路由在 `1920×1080` 设计画布下完成真实接口加载、主内容存在、无横向溢出回归；组件继续统一为 React + 原生 HTML/CSS。

## 4. 不可违反的工程约束

1. SQL 只能由 Semantic IR 经 Go 编译器生成；LLM 零直接 SQL。
2. `askdata.activate_release` 的激活必须同时满足评测门禁与双人审批；投影完成不构成放宽理由。
   **当候选将取代一个在线 ACTIVE Release 时，还必须存在一份 `ACCEPTED_95/ACCEPTED` 的
   Shadow/Canary 记录，且其对照必须正是当前那个 ACTIVE Release**——对照已被替换的灰度
   证明不了此刻这一次比较。该条件与灰度收口写在激活过程内部，不在应用层，
   任何调用方都绕不过（`000329`）。
   反之，**业务域的首个 Release 不需要灰度**：没有对照就没有可切分的流量，
   评测门禁与双人审批即为其全部控制项。此前应用层无条件要求灰度，
   而灰度又必须有 ACTIVE 对照才能启动，导致首个 Release 永远无法激活。
3. 报告数据绑定必须声明 `bindingMode`；只有 `SEMANTIC_IR` 能反哺问数。
4. 不可加指标绝不得被 `SUM/AVG`；半可加缺时间聚合声明则编译失败。
5. 未结束周期默认 `MTD`；策略优先级：指标级 > 时间合同级 > 业务域级 > 平台默认。
6. 叙述文字必须通过事实校验，失败降级为结构化答案，不得输出未校验文本。
7. 报告分享无匿名类型，令牌只定位不授权。
8. 被引用的 Semantic Release 只能 `RETAINED`，不得 `RETIRED`。
9. 物化日常刷新只变更 `dataSnapshotVersion`，不得使 Release 进入 `STALE`。
10. 密封集分片轮换，被查看样本立即退役，绝不用于修复和调参。
    退役单位是**分片**：不存在、也不得新增任何能显示密封题面的读取接口——
    退役副作用只让曝光可审计，并不让它安全，而门禁恰恰依赖题面从未被看过。
    发现泄露时走曝光声明（`expose_evaluation_shard`），整片立即不可逆退役；
    运营页只展示计数、轮换状态与能否签发 95% 结论。
11. **数据质量规则是绑定，不是表达式语言。** `askdata.quality_rules.rule_ast` 只能是
    `DATASET_QUALITY_BINDING`，指向物化链路**已经在执行**的检查
    （`materialization.ExecutingQualityRules` 为唯一目录）。语义层只读结果，绝不自行求值；
    绑定到不被执行的检查在写入、认证与 Release 门禁三处均被拒绝。
    `get_data_quality_status` 的状态词表固定为 `PASS/WARNING/FAIL/UNKNOWN`，
    定序 `FAIL > WARNING > UNKNOWN > PASS`；未测量与 SKIPPED 一律是 UNKNOWN，
    绝不读作通过——「从未检查」与「检查通过」必须可区分。
12. 不恢复历史 `platform.semantic_*` 运行时与 `000195` 删除的旧报告运行时；Report V2 为独立 bounded context。
13. 数据库迁移编号已使用至 `000339`，新迁移顺延登记，不得复用或重复占用。`000338` 仅允许退休数据源把历史版本凭据替换为 source-specific 撤销墓碑，其他版本字段仍受不可变触发器保护；`000339` 将平台管理员支持队列修正为租户级控制面，业务成员自己的工单仍保持领域隔离。
14. 新页面沿用已确认的蓝 / 白 / 灰设计系统与 1920×1080 基准自主延伸；只有业务口径、权限边界和生产数据变更需要人工治理确认。
15. 用户反馈不直接写生产语义；候选必须经 Owner 审批与回归。
16. **报告主链不得硬依赖模型提供方。** 空白新建、结构编辑、数据绑定、发布、运行、导出与分享
    在未配置 LLM 密钥时必须完整可用；发布评审的裁决由确定性门禁产生，模型只负责叙述，
    缺失或失败时回退为 `DeterministicPublishReview`，绝不因此放宽门禁。
17. **前端统一为 React + 原生 HTML/CSS。** 不得再引入 element-plus、Vue、Ant Design 或其主题层；
    通用按钮走 `web/src/components/AppButton.tsx`，复杂业务控件以原生可访问组件实现，避免第三方样式污染布局。
18. **不得在生产构建中提供设计走查数据。** 所有 `?snapshot=` 分支必须由 `import.meta.env.DEV` 兜住；
    真实模式（idle/live）下的证据面板、执行计划与收据只能展示服务端返回的事实，
    不得回退到示例指标、示例可信度分或示例结论。
19. 报告证据只能由服务端从真实执行推导；接口不接受调用方自带的事实、数值或溯源哈希。
20. 报告结论由模型撰写但不书写任何数字与对象名：模型只输出 `{{fact:…}}`/`{{field:…}}`
    标记，服务端按证据代入取值并计算引用区间，未通过共享事实校验器的结论不落库。
21. 发布前的人工门禁必须是人的声明：客户端不得代发布人确认告警，也不得用自己算得出
    的值充当校验凭据（预览确认已由回显哈希改为明确的 `previewedDesktop`/`previewedMobile`）。

## 5. 接手验证命令

环境搭建与 MVP 主路径见 [README](../README.md)；从零启动的最短序列是：

```sh
make infra-up
make db-migrate
make db-seed-report-components   # 缺这一步报告组件模板合同为空，组件校验必失败
make seed-dev
make run-api                     # 另开终端：make run-worker、npm --prefix web run dev
```

接手验证：

```sh
git status --short
go test ./... -count=1
go test -race ./internal/askdata/... ./internal/report/... ./internal/datarequest/... -count=1
go vet ./...
./scripts/check-compose.sh
./scripts/migrate.sh
./scripts/verify-database.sh
git diff --check
npm --prefix web run lint
npm --prefix web run build

# 有对应外部环境时执行的专项验证
./scripts/verify-nebula-compose.sh
./scripts/verify-nebula-poc.sh
./scripts/dev-services.sh status
ENV_FILE=.env.example ./scripts/verify-database.sh
ENV_FILE=.env.example ./scripts/verify-warehouse.sh
./scripts/ci-check.sh

# 数据库 integration test（显式开启）
ASKDATA_INTEGRATION_DATABASE_URL='postgres://report_app:...@127.0.0.1:5432/...' \
ASKDATA_INTEGRATION_ADMIN_DATABASE_URL='postgres://report_admin:...@127.0.0.1:5432/...' \
ASKDATA_INTEGRATION_WORKER_DATABASE_URL='postgres://report_worker:...@127.0.0.1:5432/...' \
  go test ./internal/askdata/... -count=1
```

灾备/容量专项：`./scripts/askdata-postgres-backup.sh`、`./scripts/askdata-postgres-restore.sh`（只接受空目标）、`./scripts/askdata-graph-rebuild.sh`（只指向演练 Space）、`go run ./scripts/loadtest`（见 [04 §6](./04_实施规划与验收终稿.md)）。

## 6. 交接更新规则

- 每次交接更新本文件 §2（环境）、§3（板块状态）与基准日期；历史交接记录不保留在本文件，以 Git 历史追溯。
- 完成任务时更新 [05_TODO](./05_TODO.md) 勾选状态并在提交信息中注明任务 ID。
- 范围或合同变化必须同步更新对应终稿（规则见 [04 §9](./04_实施规划与验收终稿.md)）。
