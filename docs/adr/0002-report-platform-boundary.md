# ADR-0002：报表平台边界与开源底座决策

- 状态：Accepted
- 日期：2026-08-03

## 决策

采用《开源报表平台二开落地方案》中“现有平台能力完整时使用等价适配层”的路径：在当前 Go + React 仓库内建设独立 Report Studio、Card DSL 和共享 Renderer，不嵌入或 Fork Superset。

当前系统已有统一认证/RBAC、PostgreSQL RLS、指标版本、查询编译和对象存储。引入 Superset 会形成第二套身份和语义事实源。Superset 继续作为管理、插件与部署能力参考，不成为运行时依赖。

冻结边界：

- `/report-studio/:reportId` 是唯一新设计器入口；
- `api/schemas/report-1.0.schema.json` 与 `internal/reportjson` 共同约束 Card DSL `1.0.0`；
- GridStack、ECharts、Redux Toolkit 都位于可替换适配边界后；
- 设计器和正式页面共用 `ReportRenderer` 与 Card Registry；
- 草稿、运行时状态、查询结果和发布制品严格分离；
- Runtime 只加载精确不可变发布版本；
- `tenantId` 只来自认证上下文，Report JSON 不得携带覆盖值。

## 兼容策略

仓库已有 `schemaVersion: 1.0` 的 pages/blocks/components 草稿，因此执行双读：服务端继续校验旧 V1；前端在读取后显式迁移到 `1.0.0` Card DSL，并将迁移作为待保存审计操作。新建报告直接使用 `1.0.0`。数据库约束允许两种版本共存，不做无审计的批量原地改写。

## 后果

优点是复用现有平台能力、避免双重语义层，并保留运行时独立性。代价是 Card SDK、画布生态和容量治理需要在本仓库持续建设，不能直接宣称拥有 Superset 的全部生产能力。
