# 智能分析决策平台

面向业务人员的可信分析平台。一期交付两条主链：

- **智能报告**：设计、绑定受治理数据、发布不可变报告版本，形成报告资产。
- **智能问数**：在对话中按当前权限查询已发布的语义资产，并引用已认证的报告。

SQL 只能由查询编译器确定性生成，LLM 不直接产出 SQL，也不能绕过权限、版本与发布门禁。

设计基线见 [`docs/`](./docs)；本文件只负责「把系统跑起来」。

---

## 1. 前置条件

| 依赖 | 版本 | 说明 |
|---|---|---|
| Go | 见 [go.mod](./go.mod)（当前 1.26.5） | 后端 API 与 Worker |
| Node.js | 22+ | 前端构建与单测 |
| Docker + Compose | 近期版本 | PostgreSQL、MinIO、NebulaGraph 等本地依赖 |

**LLM 提供方是可选的。** 未配置模型密钥时，下列能力仍然完整可用：

- 新建空白报告、编辑结构、配置数据绑定、发布、运行、导出与分享；
- 发布评审回退为「确定性门禁评审」，裁决与配置了模型时完全一致（模型只负责叙述，不能放宽门禁）。

需要模型密钥的能力：AI 生成报告结构、AI 改稿、智能结论、智能问数的问句理解与编排。

## 2. 快速开始

```bash
cp .env.example .env
```

在 `.env` 中至少设置 `SEED_ADMIN_PASSWORD`；需要 AI 能力时再补 `AI_DEEPSEEK_API_KEY` 或 `AI_GLM_API_KEY`。
`.env` 已被 Git 忽略，不要把密钥写进 `.env.example`。

```bash
make infra-up
```

启动 PostgreSQL（控制库 + 数仓库）与 MinIO。

```bash
make db-migrate
```

应用 `migrations/` 下的全部迁移。

```bash
make db-seed-report-components
```

写入内置的报告组件模板合同（Component Manifest）。**不执行这一步，报告组件将无法通过校验。**

```bash
make seed-dev
```

创建演示租户、平台管理员、默认领域负责人和三个业务领域。种子会生成两类权限层级不同的账号：

- 平台管理员：`SEED_ADMIN_EMAIL`（默认 `admin@example.com`），拥有全平台最高权限，可治理平台并进入全部业务领域；
- 业务领域负责人：`SEED_DOMAIN_OWNER_EMAIL`（默认 `biz.owner@example.com`），用于数据、问数、报告与决策主流程。

两者默认都使用 `SEED_ADMIN_PASSWORD`；如需单独设置业务账号密码，可配置 `SEED_DOMAIN_OWNER_PASSWORD`。

```bash
make run-api
```

在另外的终端分别启动 Worker 与前端：

```bash
make run-worker
```

```bash
npm --prefix web install && npm --prefix web run dev
```

打开 <http://127.0.0.1:5173>。验收业务主流程时使用领域负责人账号；治理平台时使用平台管理员账号。API 监听 `127.0.0.1:8080`。

> 也可以用 `make dev-up` 让 Docker Compose 托管全部应用与基础设施进程（含 NebulaGraph 与 Connector），`make dev-status` 查看状态，`make dev-stop` 停止。

## 3. MVP 主路径：从数据到已发布报告

这条路径**不需要语义层，也不需要模型密钥**——报告组件使用 `DATASET_FIELD` 绑定，只依赖已发布的数据集版本。

1. **接入数据源**（导航「数据资产」）：新建 → 测试连接 → 提交发布审批 → 发现并导入元数据。
2. **建模并发布数据集**（导航「数据集」）：新建数据集 → 画布建模 → 预览 → 发布 → 运行物化，得到 `PUBLISHED` 版本。
3. **新建报告**（导航「报告中心」→ 新建报告）：填写名称，选择数据来源，点击**创建空白报告**。
   （已配置模型时可改用「让 AI 生成」，由 AI 规划章节与组件。）
4. **配置组件**：在编辑器中打开「手动精修」，编辑标题并在**数据绑定**区选择维度与度量。
   可选字段已按你的列权限裁剪，维度/度量数量受组件模板合同约束。
5. **发布**：点击「预览与发布」，查看六项确定性门禁，确认后发布不可变版本。
6. **查看与分享**：发布后进入报告运行页，按当前查看者权限执行组件，可导出与创建有期限分享。

## 4. 智能问数所需的额外准备

问数走语义层，因此需要先有一个 `ACTIVE` 的语义发布：

1. 下载导入模板：`GET /api/v1/askdata/semantic/imports/template`；
2. 提交导入批次：`POST /api/v1/askdata/semantic/imports` → `.../commit`；
3. Owner 认证：`POST /api/v1/askdata/semantic/bulk-certify`；
4. 创建并激活 Release：`POST /api/v1/askdata/semantic/releases` → `/validate-project` → `/gate` → `/approvals` → `/activate`。

导入后可以按对象类型核对结果（不带 `status` 返回全部状态，也可显式过滤）：

```bash
curl -H "Authorization: Bearer $TOKEN" '127.0.0.1:8080/api/v1/askdata/semantic/metrics?status=CERTIFIED&limit=50'
```

对象类型：`models`、`measures`、`metrics`、`metric-versions`、`dimensions`、`terms`、`kpi-bundles`、`relationships`。

> 语义资产目前**只有 API，没有管理界面**（语义中心页面 `WEB-007` 未交付）；
> 维度成员、层级、认证问法与指标维度绑定的读取接口尚未覆盖（`SEM-READ-002`）。
> 详见 [docs/05_TODO.md](./docs/05_TODO.md)。

没有 `ACTIVE` 发布时，问数会明确返回 `QUESTION_RELEASE_UNAVAILABLE`，不会伪造答案。

## 5. 验证命令

```bash
make check
```

该命令统一检查迁移编号与代码格式，并运行后端测试、静态检查、应用构建，以及前端 lint、测试和生产构建。

```bash
./scripts/ci-check.sh && ./scripts/verify-database.sh
```

需要外部环境时另有 `./scripts/verify-nebula-compose.sh`、`./scripts/verify-warehouse.sh`。
数据库集成测试需显式提供 `ASKDATA_INTEGRATION_*` 连接串，命令见 [docs/06_HANDOFF.md](./docs/06_HANDOFF.md) §5。

## 6. 仓库结构

```text
cmd/            API、Worker、种子与运维命令
internal/       领域模块（askdata 问数、report 报表、dataset 建模、access 权限 …）
migrations/     PostgreSQL 迁移，编号顺延不得复用
api/schemas/    跨端共享的 JSON Schema 合同
web/src/        React SPA（pages 页面、report 报表域、askdata 问数域）
scripts/        迁移、校验、备份与压测脚本
docs/           产品、技术、前端旅程、实施规划、TODO 与交接
```

## 7. 常见问题

**平台管理员可以进入哪些业务领域？** 平台管理员拥有全平台最高权限，可以进入全部启用领域；领域管理员可查看和管理所属领域的全部信息，普通用户只能访问自己创建、领域内共享或明确分享给自己的内容。

**报告中心提示没有可用数据来源。** 当前业务领域还没有 `PUBLISHED` 的数据集版本，先完成第 3 节的第 1～2 步。

**发布评审显示「未启用模型评审」。** 这是未配置模型密钥时的正常回退，发布裁决完全由确定性门禁给出。

**问数返回 `QUESTION_RELEASE_UNAVAILABLE`。** 当前业务领域没有 `ACTIVE` 语义发布，见第 4 节。
