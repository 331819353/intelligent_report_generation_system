# 语义目录文档与向量化 Worker

`platform.semantic_change_outbox` 是标签、数据集、字段、维度、维度成员和指标语义变更的事务出口。业务写事务只负责写入或更新 outbox；`internal/semanticcatalog` 异步生成确定性检索文档，并把文档向量写入 `platform.semantic_documents.embedding halfvec(2560)`。

## 可信边界

`report_worker` 是当前物化、维度画像、成员刷新和语义目录的通用可信控制面角色。
API 不能直接修改画像任务或维度成员；画像资源 GUC 也只能通过匹配当前
`RUNNING + attempt + owner + lease token + lease expiry` 的函数设置。这个边界能
阻止 API、PUBLIC 和迟到 worker 伪造正常完成，但不能在通用 worker 进程被完全攻陷
时提供密码学不可伪造性。独立 profiler 数据库角色和更窄的函数式写接口属于后续
纵深隔离，不应把当前通用 worker 描述成独立证明服务。

## 处理链路

1. Worker 先枚举有效租户，再通过 `WithTenantTx` 设置 `app.tenant_id`。outbox、语义文档及所有参与文档生成的表均受 PostgreSQL RLS 约束。
2. 每个租户一次最多 claim 32 个事件。claim 使用 `FOR UPDATE SKIP LOCKED`，生成新的 `lease_token`，并记录 `lease_owner`、`event_version`、尝试次数和租约到期时间。
3. `TAG`、`DATASET_VERSION`、`DATASET_FIELD`、`DIMENSION`、`METRIC_VERSION` 事件只从经过约束的控制面列生成文档。`DIMENSION_MEMBER` 事件不加载成员原值，只删除可能存在的旧成员文档并以 `SKIPPED` 收口。不会读取样本行、连接凭据、数据集 DSL/逻辑计划正文、表达式树或任意 SQL。
4. 文档通过主题唯一键幂等 upsert。只有文档版本、规范文本或输入摘要实际变化时才清空旧向量并置为 `PENDING`。migration 000060 的文档触发器随后在同一事务中生成 `SEMANTIC_DOCUMENT` 事件。
5. `SEMANTIC_DOCUMENT` 事件调用统一 `embedding.Provider`。调用前先续租，调用期间
   每隔 `lease / 3` 以事件 ID、owner、lease token、event version 和 attempt
   心跳续租。心跳失败会取消 provider context，且该批次不再执行完成或失败写入，
   由租约到期后的正常 reclaim 重试。历史维度成员文档会被
   删除；即使升级期间残留的成员文档事件被领取，也会在本地确认后跳过 provider。
   其他请求同时受 32 条和
   240 KiB 总输入限制；返回向量必须逐条对应、长度严格为 2560，且不能包含 NaN
   或无穷值。标签绑定数量本身不设固定上限；极端大文档保留确定性的前缀、完整
   事实数量和完整事实摘要，因此尾部标签变化仍会改变输入摘要并触发重新向量化。
6. 向量写入同时比较文档 `input_hash`。因此编辑标签或其他语义事实后，旧 worker 即使迟到，也不能把旧文本的向量写到新文档。

标签名称、说明或别名变化时，数据库触发器会同时重建标签自身，以及所有已批准
绑定资产的语义文档。维度成员键 `690`、规范名称 `智家生态圈` 和业务别名直接
保存在租户内成员/别名表，由精确倒排查询解析，不创建中间成员文档。

## 数据集标签建议的上游任务

`internal/datasettagsuggestion` 是语义目录 worker 的独立上游生产者。数据集版本进入
`PUBLISHED` 的业务事务通过 migration 000064 写入
`platform.dataset_tag_suggestion_jobs`；标签建议成功后最多写入
`origin=LLM,status=SUGGESTED` 的 `asset_tag_bindings`。它不会直接写
`semantic_documents` 或向量，也不能把建议批准为治理事实。

建议任务固定精确数据集版本、层级、schema hash、ODS 源发布版本快照和
`dataset-tag-suggestion-v1` Prompt 版本。worker 使用独立 lease owner/token、
尝试预算和 RLS tenant transaction 领取任务。外部 LLM 调用前先续租，调用期间
每隔 `lease / 3` 以 owner、token、attempt 和精确数据集版本心跳；心跳失败会取消
模型 context，并禁止迟到结果或终态失败写入。完成前会在同一事务重新加载输入并
比较输入摘要；发布指针、schema、源版本或冻结依赖变化时任务进入
`SKIPPED / SUBJECT_CHANGED`，旧模型结果不会落库。

送入模型的上下文是有界且结构化的：

- ODS：当前版本字段及 DAG、已治理表/投影字段元数据、键属性、说明和已有表标签；
- DWD/DWS：当前版本字段、DAG、粒度，以及精确当前上游发布版本的字段、粒度和
  已批准标签摘要；
- taxonomy：当前租户最多 1024 个 `ACTIVE + CONTROLLED` 标签及其别名。

输入不包含样本行、凭据、SQL、表达式字面值或完整业务数据；总大小最多 192 KiB。
模型响应由 JSON Schema 把 `tagId` 枚举限制在现有受控 taxonomy 中，最多 256 条，
服务端再验证置信度、说明长度、分类、重复项和确定性输出摘要。允许的自动建议分类
仅为 `BUSINESS_DOMAIN`、`BUSINESS_ENTITY`、`TABLE_FUNCTION`、`USAGE_SCOPE`、
`DATA_GRAIN` 和 `JOIN_ROLE`。

管理员使用语义管理 API 把建议改为 `APPROVED` 或 `REJECTED`。批准绑定会触发
migration 000060 的 outbox 逻辑，推进对应数据集版本事件；从此才由本 worker
重建语义文档和向量。模型 provider 未配置时标签任务保持 `PENDING`，不会伪造失败
或空建议。

## 恢复与失败语义

所有完成、跳过和失败写入都必须同时匹配任务/事件 ID、租约 owner、lease token
及对应版本栅栏，并要求租约尚未过期。语义 outbox 使用 `event_version + attempt`，
标签建议任务使用 `attempt + dataset_version_id`。心跳也使用同一组栅栏，且不能
复活已经过期的租约。任一条件不匹配都返回 `ErrLeaseLost`，provider context 会被
取消，不产生完成写或其他语义状态修改。

失败事件按 30 秒、2 分钟、10 分钟退避，并在 `max_attempts` 后进入终态 `FAILED`；重新编辑主题会由 outbox upsert 提升 `event_version`、清零尝试次数并重新排队，不会丢失新事件。worker 崩溃后，未耗尽尝试次数的过期租约可以被其他实例接管；已耗尽的向量事件会同时把尚未成功的语义文档标记为 `FAILED`。

当 embedding provider 未配置、模型维度不是 2560 或暂时不可用时，确定性文档重建仍可继续。向量事件保留在队列中，避免 provider 配置问题阻塞标签和语义目录的文本更新。

## 文档合同

文档版本固定为 `semantic-catalog-document-v1`，`input_hash` 是文档版本和规范文本的 SHA-256。各主题允许进入文档的事实如下：

- 标签：编码、名称、说明、分类、治理方式、状态、版本、父标签和别名。
- 数据集版本：名称、说明、ODS/DWD/DWS 层级、版本状态、字段的类型与角色摘要、结构及计划摘要；不包含计划正文。
- 数据集字段：名称、规范/语义类型、角色、聚合方式、可空/可见属性和所属数据集。
- 维度：名称、说明、类型、成员索引策略、敏感/高基数属性、所属 DWS 数据集和字段摘要。
- 维度成员不属于语义文档合同；成员键、规范名称和别名只在租户内受 RLS 的
  成员/别名表中参与精确倒排检索。
- 指标版本：名称、说明、指标类型、聚合方式、单位、时间粒度、可加性、空值口径、适用维度和精确数据集版本摘要；不包含指标表达式。

新增或修改这些对象的服务必须在同一个业务事务中调用 `platform.enqueue_semantic_change`，或者依赖 migration 000060 已安装的标签、绑定、维度、成员和语义文档触发器。不能在事务提交后用“尽力而为”的独立消息代替 outbox。
