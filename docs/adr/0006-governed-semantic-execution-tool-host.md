# ADR 0006：以执行注册表和 PostgreSQL 预检治理现有查询执行器

状态：Accepted（2026-08-03）

## 决策

不另建一套问数执行引擎。当前项目的指标定义、结构化 DSL 编译器、策略加载、稳定物化
视图和只读 PostgreSQL 执行器继续承担物理执行，但不再自行决定“某个版本可以执行”。

在线 Question 只有同时满足下列条件才能进入物理执行：

1. PostgreSQL 活动语义发布和 `content_hash` 未变化；
2. NebulaGraph GraphPlan 已证明 Bundle、权限和关系闭包；
3. `EXECUTION_SEMANTIC_LAYER` 投影 READY，且 `semantic_execution_registry` 精确绑定
   当前原生指标版本、数据集版本、活动物化和质量规则；
4. 服务端从结构化 DSL 生成参数化 PostgreSQL 查询；
5. PostgreSQL 通过 `EXPLAIN (FORMAT JSON)` 解析单条查询，只读计划节点和优化器成本
   均在预算内；禁止 `EXPLAIN ANALYZE`；
6. 执行后结果通过语义版本/内容 hash、类型、形状、物化、新鲜度和比较期合同验证。

规范 14 Tool 全部由 Go Host 注册。模型只能建议检索和规划工具，不能直接调用执行器。
Host 注入租户、用户、角色、用途、release、版本和 hash，并执行 3/12/2/2/2/60s 预算。
每次核心调用只把脱敏 hash、证据、预算、耗时和状态追加到 `semantic_tool_calls`；数据库
触发器禁止更新和删除，API 只能 `SELECT/INSERT`，worker 和连接测试身份无权限。

## 理由

这保留了当前项目已经验证的发布、RLS、参数化编译和数仓适配能力，同时按照落地方案
把治理权提升到统一语义发布与图关系层。执行注册表是发布产物，可以重建和回滚；物理
执行器是受控适配器，不会形成第二份指标事实源。

## 边界

该决策只启用确定性语义路径 A。长尾 Text-to-SQL 路径 B 仍关闭；未来只有在目标方言
存在可靠 AST/血缘适配器，并复用相同 GraphPlan、PolicyScope、成本和结果门禁时才可
独立评审启用。
