# 元数据 AI 补全 API

所有接口必须携带有效 Bearer Token，租户和操作者只从服务端认证上下文获取。生成和决策需要 `DATA_ASSET:MANAGE`，查询建议需要 `DATA_ASSET:READ`。

## 生成表及字段建议

`POST /api/v1/metadata-ai/tables/{tableId}/completions`

服务只向模型发送表字段技术元数据和现有业务元数据，不发送连接串、密钥或数据样本。模型输出必须满足固定 JSON Schema，并且精确覆盖请求中的表和全部活动字段。未知、重复或缺失的资产 ID，以及非法标签、语义类型、敏感级别、置信度和不安全文本都会使整次任务失败，不写入建议或正式资产。

置信度大于等于 `AI_CONFIDENCE_THRESHOLD` 且生成期间资产版本未变化、未人工锁定时自动应用。其他结果进入 `PENDING`：

- `LOW_CONFIDENCE`：置信度低于阈值；
- `MANUAL_LOCKED`：人工锁定，AI 不覆盖；
- `VERSION_CHANGED`：生成期间业务版本变化，避免并发覆盖。

未配置 `AI_API_KEY` 时返回 `503 AI_PROVIDER_UNAVAILABLE`，不影响其他数据源和资产接口。超时返回 `504 AI_TIMEOUT`，非法结构化输出返回 `502 AI_INVALID_OUTPUT`。

所有模型调用统一经过 [AI 编排基础设施](ai-orchestration.md) 的租户策略、配额预留、有限重试、输入脱敏和无正文审计。租户未启用该用途或操作者已失效时返回 `403 AI_TENANT_FORBIDDEN`；日请求、月 Token 或月费用不足时返回 `429 AI_QUOTA_EXCEEDED`。这两类失败会收口已经创建的元数据 AI 任务，但不会调用模型或写入建议。

## 查询建议

`GET /api/v1/metadata-ai/suggestions?jobId={jobId}&status=PENDING&limit=100`

`status` 可选值：`PENDING`、`APPLIED`、`ACCEPTED`、`REJECTED`。结果受 PostgreSQL RLS 租户隔离。

## 确认或拒绝建议

`POST /api/v1/metadata-ai/suggestions/{suggestionId}/decision`

```json
{"decision":"ACCEPT"}
```

`decision` 只能是 `ACCEPT` 或 `REJECT`。接受时再次检查业务版本和人工锁定状态；冲突返回 `409 SUGGESTION_CONFLICT`。

## 审计与安全

领域任务记录 Provider、模型、提示词版本、SHA-256 输入哈希、延迟、Prompt/Completion/Total Token、结构化结果和状态。通用 `platform.ai_requests` 另记录跨用途配额、费用、尝试次数和稳定错误码。审计日志记录开始、完成和人工决策。API Key、原始提示词、图片地址、数据样本和失败响应正文均不落库。
