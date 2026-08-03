# Report Studio 2.0 与共享 Renderer

本实现按《开源报表平台二开落地方案》重建独立的 Report Studio、Report DSL、Card SDK 和 Runtime。旧的 `pages → blocks → components` 设计器仅作为输入迁移源，不再是编辑或运行模型。

## 冻结边界

- 入口：`/report-studio/:reportId`；`/designer/:reportId` 仅保留兼容路由。
- 协议：JSON Schema 2020-12，`schemaVersion: 1.0.0`，12 列 `lg/md/sm` 响应式布局。
- 状态：Redux Toolkit 保存定义、断点、选择态、Undo/Redo 和待提交审计操作。
- 布局：GridStack 只通过 `GridStackCanvas` 适配，不把 DOM 或像素状态写入 DSL。
- 图形：ECharts 只位于内置 Card Plugin 中。
- 渲染：设计器和正式页面均调用 `ReportRenderer`；差异只来自 `mode` 和编辑回调。
- 查询：浏览器只提交卡片 ID、筛选值和交互身份，不提交 SQL、指标版本或查询 AST。
- 租户：`tenantId` 只来自认证上下文，禁止进入 Report JSON。

## Card DSL

合同和示例位于：

- `api/schemas/report-1.0.schema.json`
- `api/examples/report-1.0.json`
- `internal/reportjson/card_model.go`
- `internal/reportjson/card_contract.go`

内置卡片为 `TITLE`、`CONCLUSION`、`CHART`、`COMPARISON`、`RANKING`、`TABLE`。每张卡必须声明 `cardVersion`、三套布局、外观、绑定和交互。草稿允许绑定未完成，发布校验会拒绝不能运行的卡片。

旧 `schemaVersion: 1.0` 草稿在浏览器读取后通过显式 Migration 转成 `1.0.0`，并作为一条 `LEGACY_DRAFT_RECOVERY` 变更等待用户保存；无法安全映射的旧组件会生成告警，不会静默伪造数据卡。

## Card SDK

`CardRegistry` 是卡片类型到插件的唯一映射。插件负责：

- 绑定校验；
- 从可信卡片绑定生成语义查询描述；
- 卡片配置迁移；
- 设计态与运行态共用的 Renderer；
- 可选的专用属性面板。

未知卡片进入安全占位视图。每张卡有独立 Error Boundary，因此单卡异常不会中断整份报告。

## 编辑与保存

GridStack 的拖放、移动和缩放只在操作结束时提交 `CARD_CREATE`、`CARD_LAYOUT_UPDATE` 等语义操作。连续布局或文字修改在编辑 Store 内合并，Undo/Redo 产生精确补偿操作。保存使用完整结果、受限 JSON Patch、Revision 乐观锁和 Idempotency-Key；服务端重放并验证每条卡片级操作，不能把任意 JSON 修改伪装成合法审计事件。

断点布局分别持久化。切换断点不会覆盖其他断点。全局筛选与卡片的 `globalFilterBindings` 分离，筛选目标维度不要求成为卡片当前展示维度。

## 查询与交互

草稿仅在保存干净后执行真实 Query Batch。正式 Runtime 只查询指定的不可变发布版本。前端用 AbortController 取消过时批次；服务端限制卡片数和并发，按卡片返回成功或错误。

服务端从可信 DSL 重新生成指标预览请求，并合并：

1. 全局筛选映射；
2. 卡片静态筛选；
3. 下钻或跨卡交互上下文；
4. 精确指标发布版本。

交互请求只包含 `sourceCardId`、`interactionId` 和点击值。目标卡、源维度、目标筛选维度及下钻分组维度由服务端从 DSL 推导，避免浏览器篡改。当前支持下钻、Cross-filter、站内报告跳转、站内报告弹窗和站内路径跳转。

查询指纹包含租户、操作者、指标、精确版本和规范查询输入；进程内 L1 缓存带 TTL 抖动，并用 Singleflight 合并相同并发请求。Redis L2、租户公平队列、stale-while-revalidate 和发布预热仍属于容量生产化阶段，不能将当前 L1 当作完整高并发终态。

## 发布与运行

发布依次执行结构、语义、权限/依赖和安全校验，固定指标版本，规范化 JSON 并计算 SHA-256。发布 JSON 以租户和内容哈希作为对象键写入 MinIO/S3，再在数据库事务内创建不可变版本、索引、审计和当前版本指针。数据库保留精确字节作为灾备副本。

Runtime 先读取 Manifest，再按 `sizeBytes` 与 SHA-256 校验定义，最后交给共享 Renderer。对象 URI 格式或对象大小异常时失败关闭；对象存储暂时不可用时可读取数据库灾备副本，但不能回退到草稿。回滚只切换当前版本指针。

## 尚未宣称完成的生产项

- Redis L2、租户令牌桶/公平队列和数据源熔断；
- CDN 刷新、热门查询预热和孤儿对象对账任务；
- 异步 PDF/明细导出与相同权限继承；
- OpenTelemetry 卡片级遥测、10,000 在线用户压测和多可用区故障演练。

这些能力有清晰边界，但不以演示实现代替生产验收。
