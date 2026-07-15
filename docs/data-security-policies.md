# 行级与列级权限框架

## 查询编译流程

1. 服务端从认证上下文取得租户和用户，加载适用于该用户及其角色的策略。
2. `policy.CompileRows` 将受限 JSON AST 编译成参数化过滤条件；用户属性永远作为绑定参数，不拼接到 SQL。
3. `policy.CompileColumn` 在生成投影列表时应用 `ALLOW`、`DENY`、`MASK`、`HASH`、`NULLIFY` 或 `AGGREGATE_ONLY`。
4. 最终查询计划必须在执行、导出、报告快照和 AI 数据取样之前完成策略注入，前端过滤不能替代此步骤。

## 行级 DSL

允许节点：`FIELD_REF`、`USER_ATTRIBUTE_REF`、`LITERAL`、`EQUALS`、`NOT_EQUALS`、`IN`、`AND`、`OR`。字段编码仅允许字母、数字和下划线；其他表达式拒绝编译。策略组合支持 `AND`、`OR` 和拒绝优先的 `DENY_OVERRIDE`。

## 列级规则

- `DENY`：拒绝查询字段。
- `MASK`：当前支持 `KEEP_PREFIX_SUFFIX`。
- `HASH`：数据库侧 SHA-256 哈希展示。
- `NULLIFY`：投影为 `NULL`。
- `AGGREGATE_ONLY`：只允许白名单聚合，拒绝明细查询；可禁止明细导出并设置最小分组规模。

## 缓存隔离

`policy.BuildCacheKey` 强制包含租户、用户或权限范围、数据集版本，并纳入参数、数据水位、行列策略版本和查询引擎版本。策略版本变化会自然生成新键，禁止仅以 SQL 文本作为缓存键。
