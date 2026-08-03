# ADR 0004：统一 Question Orchestrator 与受治理执行路径

状态：Accepted

## 决策

产品问答主链统一为 `POST /api/v1/questions`。浏览器只提交问题、会话 UUID 和服务端
返回的受治理确认 ID；意图补全、Semantic IR、计划门禁、执行、结果核验和答案生成均由
服务端 Question Orchestrator 持有。

默认执行路径为版本化 `SEMANTIC_IR`。模型只参与意图和候选选择，不能生成可直接执行的
SQL、表名、字段名或 WHERE 表达式。Host 通过中央 Tool Registry 注入工具定义、允许
状态、只读能力和 JSON Schema；Evidence Loop 最多三轮，非终止调用必须产生新证据。

长尾 Text-to-SQL 路径必须具备目标方言的可靠 AST 解析、标识符白名单、只读验证、预算
和 dry-run 后才能启用。当前仓库没有覆盖全部数据源方言的可靠 AST 适配器，因此路径 B
明确关闭并给出 `RELIABLE_DIALECT_AST_ADAPTER_NOT_CONFIGURED`，不能以字符串规则代替
AST 验证。无法走路径 A 的问题进入最小澄清或拒绝。

Question 状态与安全事件持久化；持久层只保存哈希、版本、路由、计划引用、错误码和安全
摘要，不保存原始问题、提示词、SQL 或结果行。

## 兼容性

旧的 `/api/v1/semantic-qa/query-turns` 和 `/query-plans/{id}/execute` 暂时保留用于旧
调用方和治理调试，但新前端不再串联它们，也不再在浏览器生成比较结论或答案文本。
