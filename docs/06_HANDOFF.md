# 智能分析决策平台——实施 Handoff（6/6）

> 文档状态：接手交接事实源（活文档，交接时更新）
> 基准日期：2026-08-11
> 配套文档：[产品设计](./01_产品设计终稿.md)、[技术设计](./02_技术设计终稿.md)、[前端用户旅程](./03_前端用户旅程终稿.md)、[实施规划与验收](./04_实施规划与验收终稿.md)、[TODO](./05_TODO.md)

---

## 1. 接手必读

1. 四份终稿（01～04）是唯一设计基线；[05_TODO](./05_TODO.md) 是唯一任务事实源。
2. 一期交付顺序：M1 数据建模 → M2/M3 语义建模与 Release ACTIVE → M4 报告主链 → M5 报告资产进入对话 → M6 门禁签署。能力依赖上**报告与问数是语义层的同级消费者**，报告资产只是问数的增强先验。
3. 决策与行动、统一任务中心、订阅分发、问数写回为后期规划；其已完成后端全部保留，不得回退或删除。
4. 生产准确率状态：尚未评测，**不得宣称达到 95%**。

## 2. 当前运行环境

- Compose 运行态：API `127.0.0.1:8080`、Web `127.0.0.1:5173`、Connector `127.0.0.1:8090`、Nebula loopback proxy `127.0.0.1:9669`；API/Worker/Connection Test Worker、PostgreSQL、Warehouse PostgreSQL、MinIO、Nebula metad/storaged/graphd/proxy 均需 healthy。
- 本地控制库为 2026-08-05 全量重置后状态：1 个 demo tenant、2 个用户，无业务语义资产；正式导入前需要真实 PUBLISHED + ACTIVE 的 DWS/ADS。
- 本地开发验证使用 `.env.example`；机器上存在 Git 忽略的 `.env` 供运行，交接、日志和提交时不得回显其内容，也不得把 API key 写入 `.env.example`。

## 3. 已完成基线（按板块）

任务级完成清单不再逐项罗列；以下板块状态为 2026-08-11 权威结论，`go test ./...`、`go vet ./...`、前端 41 项测试、`npm run build` 全部通过：

| 板块 | 状态 | 关键缺口 |
|---|---|---|
| B01 平台底座与权限 | 安全基线、统一工作箱、用户停用/Owner Transfer 后端完成 | 业务导航与用户生命周期页面 |
| B02 数据接入与元数据 | 明细取数、敏感会签、受控导出后端完成 | 前端入口与真实审批人 |
| B03 数仓建模与物化 | 仓库基线 + 数据快照版本完成 | 增量物化与运维增强 |
| B04 语义资产治理 | 后端主链、时间/可加性、导入、词典、KPI 完成；对象读取接口支持全生命周期状态过滤（SEM-READ-001） | SEM-READ-002（成员/层级/认证问法等 4 类未覆盖）、ADD-002、SEM-CTX-001、WEB-007 |
| B05 语义发布与投影 | READY、评测门禁、双人审批、原子激活、保留策略完成 | 真实业务审批/评测事实 |
| B06 检索与语义图谱 | 主链、降级矩阵、Recall@K 审计完成 | 正式人工黄金集 |
| B07 问句理解与联合绑定 | 全链完成（领域 Pin、Release Pin、范围白名单、拒答） | — |
| B08 查询编译与执行 | 全链完成（可加性编译、TopN、认证 Bundle、outcome） | — |
| B09 编排与问数 API | Loop/API/SSE、答案校验重生成、预算/幂等完成 | — |
| B10 问数工作台前端 | WEB-001～006、011 完成 | WEB-007～010、012～017 |
| B11 报表引擎与融合 | 后端引擎、发布/回滚/导出/分享/升级、融合、订阅调度完成；空白新建、数据绑定面板、确定性发布评审回退已交付 | WEB-RPT-001 模板中心、WEB-RPT-005 移动端、WEB-RPT-009 组件增删与布局 |
| B12 评测、反馈与运营 | 控制面、密封集轮换、误差预算、反馈闭环完成 | 正式黄金样本与运营页面 |
| B13 运维、可观测与成本 | 配置、观测、配额/成本、DR/容量合同完成 | OPS-008 页面、目标环境签署 |
| B14 决策协同（后期） | 合同、存储、服务、行动、复盘后端完成 | 页面与业务验收（后期） |

## 4. 不可违反的工程约束

1. SQL 只能由 Semantic IR 经 Go 编译器生成；LLM 零直接 SQL。
2. `askdata.activate_release` 的激活必须同时满足评测门禁与双人审批；投影完成不构成放宽理由。
3. 报告数据绑定必须声明 `bindingMode`；只有 `SEMANTIC_IR` 能反哺问数。
4. 不可加指标绝不得被 `SUM/AVG`；半可加缺时间聚合声明则编译失败。
5. 未结束周期默认 `MTD`；策略优先级：指标级 > 时间合同级 > 业务域级 > 平台默认。
6. 叙述文字必须通过事实校验，失败降级为结构化答案，不得输出未校验文本。
7. 报告分享无匿名类型，令牌只定位不授权。
8. 被引用的 Semantic Release 只能 `RETAINED`，不得 `RETIRED`。
9. 物化日常刷新只变更 `dataSnapshotVersion`，不得使 Release 进入 `STALE`。
10. 密封集分片轮换，被查看样本立即退役，绝不用于修复和调参。
11. 不恢复历史 `platform.semantic_*` 运行时与 `000195` 删除的旧报告运行时；Report V2 为独立 bounded context。
12. 数据库迁移编号已使用至 `000301`（Decision、工作箱、会话历史、报告调度、Owner Transfer、运行配置、会话自定义标签、报告发布评审、问数运行租约、问数澄清出口等），新迁移顺延登记，不得复用或重复占用。
13. 新页面/流程/显著视觉状态先出设计稿并取得用户确认；已确认方案（证据驾驶舱、纵向时间线、双候选决策卡、两步反馈弹窗、报告资产中心方案 2 等）不得擅自推翻。
14. 用户反馈不直接写生产语义；候选必须经 Owner 审批与回归。
15. **报告主链不得硬依赖模型提供方。** 空白新建、结构编辑、数据绑定、发布、运行、导出与分享
    在未配置 LLM 密钥时必须完整可用；发布评审的裁决由确定性门禁产生，模型只负责叙述，
    缺失或失败时回退为 `DeterministicPublishReview`，绝不因此放宽门禁。
16. **前端组件库统一为 React + Ant Design。** 不得再引入 element-plus、Vue 或任何 Vue 生态依赖；
    按钮统一走 `web/src/components/AppButton.tsx`，主题 token 与 `zh_CN` locale 在
    `web/src/main.tsx` 的 `ConfigProvider` 集中声明，页面不要各自硬编码 antd 主题。
17. **不得在生产构建中提供设计走查数据。** 所有 `?snapshot=` 分支必须由 `import.meta.env.DEV` 兜住；
    真实模式（idle/live）下的证据面板、执行计划与收据只能展示服务端返回的事实，
    不得回退到示例指标、示例可信度分或示例结论。

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
