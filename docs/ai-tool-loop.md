# 多模型受控工具循环

## 1. 支持范围

通用 AI 边界现已支持 OpenAI Chat Completions 风格的：

- `tools`
- `tool_choice`
- `tool_calls`
- `role=tool` 与 `tool_call_id`
- assistant `reasoning_content` 的循环内回传

协议适配覆盖 DeepSeek、GLM 和 MiniMax。三者共享内部工具合同，但请求细节按模型
名称和端点自动适配：

| 模型族 | 思考配置 | 工具选择 | 推理连续性 |
| --- | --- | --- | --- |
| DeepSeek | `thinking.type=enabled` | `auto` 时省略显式字段，兼容会拒绝 `tool_choice` 的思考端点 | 完整回传 assistant 的 `reasoning_content/content/tool_calls` |
| GLM | `thinking.type=enabled`、`clear_thinking=false` | `auto` | 完整回传 assistant 的 `reasoning_content/content/tool_calls` |
| MiniMax | OpenAI 兼容工具协议，不额外发送 `thinking` | `auto` | 完整回传 assistant/tool 消息；若端点返回 `reasoning_content` 也原样回传 |

独立供应商通过以下环境变量配置，旧的统一企业网关配置继续兼容：

```dotenv
AI_DEEPSEEK_BASE_URL=https://api.deepseek.com
AI_DEEPSEEK_MODELS=deepseek-v4-pro
AI_DEEPSEEK_API_KEY=

AI_GLM_BASE_URL=https://open.bigmodel.cn/api/paas/v4
AI_GLM_MODELS=glm-5.2
AI_GLM_API_KEY=

AI_MINIMAX_BASE_URL=https://api.minimaxi.com/v1
AI_MINIMAX_MODELS=MiniMax-M3
AI_MINIMAX_API_KEY=
```

模型名由部署环境决定；企业网关可以继续使用其内部别名，例如
`deepseek-v3`、`glm-*` 或 `MiniMax-*`。

## 2. 运行边界

一次工具循环最多：

- 4 个模型轮次；
- 8 次工具调用；
- 5 次 Provider HTTP 尝试；
- 单个工具结果 32 KiB、全循环工具结果 64 KiB；
- 受现有 AI 总超时、单次超时、租户授权、Token 配额和费用配额约束。

每次工具调用都执行以下检查：

1. 工具名必须来自本次服务端白名单。
2. 参数必须是单个 JSON 对象，并通过该工具的封闭 JSON Schema。
3. 工具执行仍处于当前租户、操作者和业务服务权限边界。
4. 工具结果必须是有界 JSON，不允许凭据、任意 SQL 或无界明细。
5. 终止工具必须单独调用；普通文本不能绕过终止合同。

`reasoning_content` 仅保存在当前请求内存的消息数组中，用于下一轮协议回传；
不会进入 AI 审计表、业务数据库、日志、HTTP 响应或前端页面。审计只保存输入摘要、
模型、轮次总 Token、耗时、稳定结束原因和请求 ID 摘要。

智能问答响应的 `trace.metricToolLoop` 只公开 AI 审计 ID、模型、轮次、工具名及
终止标记，用于证明实际状态迁移；它不包含工具参数、工具结果或
`reasoning_content`。

主模型的工具协议不可用、输出越过工具合同、超时或未到达终止工具时，智能问答会按
配置顺序从下一模型重新开始完整循环。DeepSeek、GLM、MiniMax 可以位于同一有序
模型池；切换模型时不会接续失败模型的隐藏推理或半成品工具状态，而是创建新的工具
执行器和独立 AI 审计记录。租户禁用、配额不足、请求取消、非法请求及数据库工具
执行失败不会触发跨供应商重试。

## 3. 智能问答接入

指标识别已使用工具循环：

1. 模型调用 `search_metrics`。
2. 服务端执行当前租户的指标目录、指标语义、别名和向量混合召回。
3. 工具返回指标编码、名称、领域、精确数据集版本和来源 DWS 发布视图。
4. 候选不足时模型可改写检索词再次调用。
5. 模型最终只能调用 `submit_metric_selection`，逐字复制候选编码，或明确返回需要
   人工确认。
6. 终止结果再次由本地候选集合校验，再进入原有 QueryPlan 权限、版本、兼容性、
   物化、决策图和血缘门禁。

指标无法唯一判断时，`query-turns` 返回 `NEEDS_METRIC_CONFIRMATION`；维度决策无法
唯一判断时返回 `NEEDS_DIMENSION_CONFIRMATION`。页面提交的确认只包含指标编码或
决策图 ID，服务端重新加载发布资产，绝不接受页面传入的表名、字段、WHERE 或 SQL。
指标已确定但没有可证明的兼容维度或决策图时返回 `SEMANTIC_GAP`，不会生成无过滤
条件的空计划。

## 4. 降级策略

所有已配置的后备模型都未完成工具循环时，智能问答才降级到原有的固定安全编排：
服务端召回候选、结构化 LLM 重排、本地合同校验。降级不会绕过权限，也不会执行
未确认的结果。

官方协议参考：

- DeepSeek：https://api-docs.deepseek.com/guides/tool_calls/
- DeepSeek Thinking：https://api-docs.deepseek.com/guides/thinking_mode
- GLM 工具调用：https://docs.bigmodel.cn/cn/guide/capabilities/function-calling
- GLM 思考模式：https://docs.bigmodel.cn/cn/guide/capabilities/thinking-mode
- MiniMax 工具协议：https://platform.minimaxi.com/document/对话
