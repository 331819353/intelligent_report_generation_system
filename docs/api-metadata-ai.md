# 元数据 AI 补全 API

所有接口必须携带有效 Bearer Token，租户和操作者只从服务端认证上下文获取。生成和决策需要 `DATA_ASSET:MANAGE`，查询建议需要 `DATA_ASSET:READ`。

## 生成表及字段建议

`POST /api/v1/metadata-ai/tables/{tableId}/completions`

直接调用本接口时，服务只向模型发送表字段技术元数据和现有业务元数据，不发送连接串或密钥。Web 产品中的数据源导入与刷新任务默认选择 `MASK`，读取最多十行业务样本并在应用进程内完成格式脱敏后再发送；`DENY` 可关闭采样，`RAW` 才发送最多十行原值。`MASK/RAW` 都要求租户策略允许、当前操作者逐任务明确授权，并在任务开始、采样前和模型调用前复核冻结的策略版本；撤权会在业务值出域前失败关闭。服务端兼容调用若省略 `sampleDataMode`，仍按 `DENY` 失败关闭。`metadata-completion-v8` 会按本次输入动态固定表 ID、字段 ID 集合和字段数量，并要求表级结果描述业务功能、适用范围和数据粒度，关联候选字段描述业务实体、键角色、方向、唯一性及可空性。标签不设固定数量上限，但只能来自受控的领域、主题、作用、功能、范围、粒度和关联角色词表；模型标签先按首次出现顺序去重，再执行本地枚举校验。CSV 输入还会通过提示词、输出 Schema 字段说明和服务端领域校验约束：字段 `businessName` 必须为小写英文 `snake_case`，`businessDescription` 必须包含中文；安全的 ASCII 空格、连字符、点号和驼峰名称会先规范为 `snake_case`，无法可靠翻译的非 ASCII 名称不会被静默改写。服务端校验是最终可信边界，不依赖上游模型对 Schema 方言的完整实现。首次输出若存在未知、重复或缺失的资产 ID，或其他领域校验错误，服务会携带安全校验原因和准确目标 ID 清单进行一次结构化纠错；两次调用分别进入通用 AI 审计，领域任务累计两次 Token 用量。纠错后仍非法时整次任务失败，不写入建议或正式资产。

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

领域任务记录 Provider、模型、提示词版本、SHA-256 输入哈希、延迟、Prompt/Completion/Total Token、结构化结果和状态。通用 `platform.ai_requests` 另记录跨用途配额、费用、尝试次数和稳定错误码。审计日志记录开始、完成、样本模式/策略版本和人工决策。API Key、原始提示词、图片地址、原始或脱敏样本值以及失败响应正文均不落库。
