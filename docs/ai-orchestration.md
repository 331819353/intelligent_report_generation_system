# AI 编排基础设施

T0501 提供与具体报告业务解耦的通用 AI 调用边界。元数据补全、数据集 DAG 提案和指标创建提案已经接入该边界；后续整份报告生成、分块修改和结论生成只需提交各自的最小领域信封，不直接处理供应商 HTTP、重试、租户配额或审计表。

## 调用流程

1. 领域服务先按当前用户权限解析允许使用的资源，只构造本次任务必需的文本和图片引用。
2. 编排层严格校验消息、JSON Schema、输出 Token 上限和总输入大小，并替换常见 Bearer Token、口令、API Key、带凭据连接地址和 PEM 私钥。
3. PostgreSQL 在租户事务内校验操作者、AI 策略、允许用途、日请求数、月 Token 和月费用，按最大尝试次数登记 `RUNNING` 保守配额预留。
4. Provider Adapter 发送文本或视觉消息，并要求 `json_schema` 严格结构化输出。仅对网络故障、超时、429 和 5xx 做最多配置次数的有限重试；认证失败、请求拒绝、拒答及非法输出不重试。
5. 本地再次校验单一 JSON、重复键、尾随内容、未知字段、类型、约束和非零 Token 用量；成功保存真实用量，但配额计量不会低于预留，失败只记录稳定错误码。

### 数据集 DAG 修改编排

新建数据集时，AI 与当前打开的设计画布绑定。空白画布的首次请求不携带虚构的 `current`，按 `CREATE` 生成完整候选；画布一旦存在节点，后续请求都从实时编辑器序列化非 SQL `current`（节点、关联、分组、字段处理、结束节点和输出）并按 `MODIFY` 处理，实时画布优先于未应用的历史候选。生成期间若画布指纹变化，前端拒绝应用旧结果；候选只有经用户确认后才会一次性替换编辑器快照，失败不会留下半套节点。针对单个节点、连线或字段的修改仍返回完整候选图，但只能改动锁定变更集声明的局部范围。

进入 LLM 前，资产 Retriever 会在租户 RLS、数据源活动、表启用且当前结构已补全的条件下，将 `TABLE/COLUMN` 向量 top 32 与包含标签的中文关键词 top 32 以 RRF 融合。先选表 top 12，再限制字段检索于入选表；当前画布和 hints 是强制项。向量文档只使用业务/技术元数据与标签，不包含样本、SQL 或凭据；embedding 失败时以中文关键词排序降级，不放松后续的本地 DAG 安全校验。

数据集 DAG 的从零生成直接进入规划器，但会先由服务端从明确的日期转换、文本处理、数值运算、类型转换、空值填充和条件映射措辞中推导 `transformRequirements`；这些要求随原始指令一同进入规划器，并在生成后再次强制校验，不能被字段改名、分组粒度或结束节点名称替代。修改已有画布则使用两个相互隔离的结构化调用。第一阶段使用 `dataset-dag-intent-v9`，只把用户指令、服务端推导的拓扑事实和受预算约束的授权字段目录解析为 `READY` 或 `CLARIFY`，不生成候选图。`READY` 必须给出由 `ADD/UPDATE/REMOVE` 组成的组件级 `operations`：`UPDATE.fields` 精确限定可变顶层字段，`inputChanges` 以 `from/to` 精确限定 `JOIN.left/right`、`GROUP.input`、`TRANSFORM.input` 和 `END.input` 的改线。同时，字段级 `fieldChanges` 以物理 `nodeId + tableId + column` 锁定 `ADD/KEEP/REMOVE`、`FINAL_OUTPUT/INTERNAL_ONLY/SELECTED_ONLY` 及 Join、逐级 Group、End 的完整使用位置；新增节点的每个选列也必须显式绑定授权表。普通“增加字段”默认指最终输出，若穿过 Group 时无法确定维度或指标角色则返回 `CLARIFY`。整体删除组件的内部字段用途由组件 `REMOVE` 锁定，不重复生成 `fieldChanges`。切换现有节点的 `tableId` 时，旧表字段与新表字段按物理身份成对迁移；仅当字段数组结构真的改变时才声明相应数组更新。`CLARIFY` 不得包含操作，也不会调用规划器。第一阶段结构化输出若未通过本地可信边界校验，会在同一 Schema、原始输入和修改范围内自动修复一次；仍无法安全形成 `READY` 时才失败关闭或返回澄清，不会把无效意图交给规划器。

第二阶段使用 `dataset-dag-planner-v12`。领域服务先校验并锁定第一阶段的 `changeSet`，规划器只接收这份锁定边界、`current` 和授权资产，并返回完整候选图。规划器在生成前必须逐段审视“数据节点 → 源字段处理 → 关联前分组 → 关联 → 关联后分组 → 输出字段处理 → 结束节点”；每段允许 0 个、1 个或多个组件，只能依据真实计算需求取舍，不能机械补齐，也不能因链路复杂省略必要组件。影响关联键、分组维度或指标计算的字段处理放在对应 Join/Group 前，仅展示转换放在最后一次 Join/Group 后；多方明细需要降粒度时先在各分支预汇总，再关联并按统一口径汇总。分组组件只消费已产生的维度和指标，所有 `GROUP.dimensions.grouping` 必须为空；月度、季度、年度和日粒度会在 Group 前通过独立 `DATE_FORMAT` 产生 STRING 维度。候选 Schema 正式包含全部 13 个细粒度 `TRANSFORM` 组件、转换规则、输入产物 key 和派生输出 key；日期、文本、数值、类型、空值及条件映射不能再被模型折叠成字段名称。CREATE 和正向的 MODIFY 要求都会在规划后复核必需组件及其产物的真实消费，显式删除组件的指令不会误触发这项正向约束。转换产物可用稳定的 `transformId.outputId` 直接参与后续 Group，并在关联前派生表中以白名单表达式物化，后续 Join、Group、Transform 与 End 继续引用同一逻辑 key。后端会对结束节点中尚不可用的 key 做一次安全规范化：仅当字段处理产物的名称与 code 唯一匹配，或声明的物理字段确实由最终输入产生时，才改为真实的 `transformId.outputId` 或 `nodeId.column`；无法唯一确定时继续失败关闭。后端独立计算 `current → plan` 的组件结构差异和字段使用库存，按稳定 ID 逐项核对动作、字段、输入改线、未涉及字段的相对顺序及端到端传播闭包；当 Group/Join/End 消费 `TRANSFORM` 产物时，字段级 `fieldChanges` 会折算回该产物的受信物理血缘，派生 key 和组件内容仍由组件差异精确保护，从而允许“在 Join 与 Group 之间插入日期转换”而不放宽变更边界。`FINAL_OUTPUT` 字段必须经过路径上的每个 Group 并到达 End，`INTERNAL_ONLY` 字段必须确实被 Join 或 Group 使用且不能偷偷新增最终输出，`SELECTED_ONLY` 字段必须保持选中但没有下游用途。未声明的删除、新增、字段变化、ID 替换、漏过中间分组、顺带重排和连线变化全部失败关闭。规划器最多纠错一次，纠错请求沿用原锁定变更集，只能修正候选图，不能扩大修改范围。最终对外提案使用 `schemaVersion: "2.3"`，并由服务端附回已验证的 `changeSet` 供用户复核。两个阶段继续使用同一 `DATASET_DAG_GENERATION` 用途，但按各自提示词版本形成独立配额与审计记录。

多轮修改的安全范围以字段传播和精确连线声明为准：`fieldChanges` 已锁定字段语义，`inputChanges` 已锁定拓扑语义，因此服务端会确定性补全同义的 GROUP/JOIN/END 顶层字段和 input 冗余声明，避免模型漏写重复字段造成误拒绝。正向 MODIFY 的 `transformRequirements` 同时进入意图与规划阶段；若锁定路径上的 TRANSFORM 已经接入、但候选仍让直接消费者引用旧物理字段，只允许把同一物理血缘替换成该转换产物，其他字段、名称、聚合与顺序继续失败关闭。前端应用通过校验的候选时原子替换 nodes、joins、groups、transforms 与 end，撤销也使用同一完整快照，避免字段处理组件在 UI 应用阶段丢失。

### 指标创建提案编排

`metric-authoring-v5` 只接收一段自然语言指标需求并生成供用户审核的提案，不要求用户预填名称、时间口径或任何结构化配置。调用前，服务端按操作者有效的全局或对象级权限检索内部当前且状态为 `PUBLISHED` 的精确数据集版本、可见逻辑字段和当前发布原子指标，同时把操作者同时具有 `DATASET:READ` 与 `DATASET:MANAGE` 的当前普通数据集草稿作为完全隔离的改造上下文。发布版本还会复核精确依赖、DSL/计划摘要及指标定义摘要；存在适用行列策略或不可执行依赖的数据集失败关闭。发送给模型的上下文不含物理节点、源标识、过滤值、参数、样例行、SQL 或凭据，也不提供外部搜索工具。

严格输出只允许 `REUSE_METRIC`、`CREATE_ON_DATASET`、`CREATE_DATASET`、`MODIFY_DATASET`、`DATA_GAP` 和 `NEEDS_CLARIFICATION`。模型从需求和授权字段语义尽可能补齐名称、编码、说明、表达式、聚合、单位、格式、精度、可加性、时间与维度配置，并把推断和风险放入假设、告警或非阻塞确认问题。

`CREATE_ON_DATASET` 仅支持原子指标，且只能引用不可变的精确 `PUBLISHED` 版本；普通数据集版本必须来自人工发布审批，映射表初始镜像可以来自受控系统发布。候选还会由本地 `metric.Prepare`、数值字段、维度/时间字段和逐项检索证据再次校验。带 `originTableId` 的数据集会在检索上下文标记为 `mapped=true`：它在指标 AI 编排中仅作为单表来源，字段已足够时可以直接支撑此策略，但绝不能成为 `MODIFY_DATASET` 目标。需要关联多个映射表、增加派生字段、补字段或改变结构时必须使用 `CREATE_DATASET`；该策略不指定现有目标，不返回指标定义，只给出让数据集 AI 新建普通数据集的完整无 SQL 业务指令，并引用实际采用的授权证据。这个限制不关闭数据集中心已有的人工维护流程。

`MODIFY_DATASET` 只能引用 `manageable=true`、`mapped=false` 且未聚合的普通数据集，可以选择授权的精确 `PUBLISHED` 版本或独立草稿上下文，存在匹配普通草稿时优先改造草稿。它不得返回指标定义，只能给出不含 SQL 的改造目标，再交由数据集 AI 审核、应用和保存。草稿字段永远不能进入候选指标表达式或指标候选目录；新建或改造保存后必须提交数据集发布申请，审批通过并得到新的精确 `PUBLISHED` 版本后，才能重新生成或创建指标。`NEEDS_CLARIFICATION` 只用于无法形成任何安全候选或数据集设计方案的实质冲突。

模型不得要求用户填写数据集、版本或字段的内部 ID；这些引用只能来自授权检索。UUID、哈希及版本 ID 只能进入结构化标识字段，面向用户的摘要、步骤、证据原因、假设、告警和问题必须使用业务名称。前端默认把请求 ID、上下文哈希及资源技术 ID 收入折叠的“技术信息”，并在提案上提供确认、沿用当前完整需求重新生成、以及把补充意见附加到原需求后重新生成三种操作。提案接口本身不保存数据集、不创建指标、不执行查询，也不提交或审批任何发布申请。

### 数据集发布后的指标语义编排

精确数据集版本发布后，发布事务会幂等登记 `metric-candidate-semantic-v2` 提取任务。Worker 先依据不可变 DSL 和字段角色生成候选指标定义、聚合、维度、周期及结构化血缘，这些事实不交给 LLM 决定；随后 `metric-candidate-enrichment-v1` 仅补充业务名称、说明、口径文字、周期说明、血缘摘要和检索标签。本地会按候选指纹、文本长度、标签数量与唯一性再次校验；LLM 不可用或输出非法时保存 `RULE_FALLBACK` 文档，数据集发布和候选生成不被阻塞。

规范语义文档不包含样本行、SQL 或凭据，使用独立 embedding 配置生成 `Qwen3-Embedding-4B` 的 2560 维向量，保存到 PostgreSQL `platform.metric_semantic_documents.embedding halfvec(2560)` 并建立 HNSW cosine 索引。Embedding Worker 使用租约、三次尝试和幂等输入摘要；查询向量不可用时退化为关键词检索。候选文档只进入审核和发现，不能直接绑定报表；候选被接受并形成正式指标草稿后，只有指标发布为精确 `PUBLISHED` 版本才自动继承其语义文档并标记为可绑定。检索只返回当前发布的数据集候选和当前发布指标版本，不会静默使用旧版本。

`internal/ai.Provider` 是供应商切换边界。当前实现兼容 Chat Completions 协议，远程 Provider 只允许 HTTPS，本机开发代理可使用 loopback HTTP，并且客户端不会跟随重定向。视觉输入只接受不含用户信息的 HTTPS 图片 URL；API 服务本身不抓取远程图片。T0502 必须先解析用户授权的附件或图片，再生成短期受控地址，不能把客户端任意 URL 当作资源授权。

本地结构化输出只接受明确实现的严格 JSON Schema 子集：基础类型与联合类型、`const`、`enum`、对象属性/必填/数量、数组元素/数量/唯一性、字符串长度/正则、数值边界/倍数、`allOf`、`anyOf`、`oneOf`、`not`，以及根级 `$defs`/`definitions` 命名引用。对象必须关闭额外字段并把全部属性列为必填，数组必须声明 `items`；引用只能指向根级已校验定义。未实现或未知关键字、错误约束类型、嵌套定义和任意 JSON Pointer 均在调用 Provider 前失败关闭。递归输出校验同时限制 128 层深度和 10000 个求值步骤，避免循环或组合引用耗尽服务资源。

## 租户策略与审计

迁移 `000024_ai_orchestration_audit.up.sql` 创建：

- `platform.ai_tenant_policies`：租户开关、允许用途、日请求、月 Token 和月费用上限；
- `platform.ai_requests`：输入摘要、Provider/模型、提示词版本、资源引用、尝试次数、稳定错误码、Token、费用和耗时。

两张表均强制启用 RLS。迁移 `000038_dataset_dag_ai` 为 `DATASET_DAG_GENERATION` 增加独立用途授权和审计枚举；`000040_metric_authoring_ai.up.sql` 只为 `METRIC_AUTHORING` 增加计量与审计枚举。指标创建提案随租户通用 `enabled` 开关启用，不检查 `allowed_purposes` 是否包含 `METRIC_AUTHORING`；元数据补全、数据集 DAG、报告生成、分块修改和结论生成继续遵循用途白名单。开发 Seed 会为本地演示租户幂等启用通用 AI，并合并元数据补全和数据集 DAG 两个独立用途。策略更新会递增版本。策略行锁会串行化同租户的配额检查、实际结算与新预留。`RUNNING` 审计带五分钟租约，新请求会先把已过期记录收口为失败；其预算仍按失败关闭规则保留，避免进程异常退出被误报成未发生调用。

数据集 DAG 领域只发送授权范围内已启用、已完成映射的表名、字段名、业务说明、规范类型、语义类型和用户当前无 SQL 画布；不发送样例行、结果数据、SQL 或连接凭据。修改时自然语言只由意图阶段解释，规划阶段只执行服务端锁定的结构化变更集；最终候选图还需同时通过结构差异边界、权威资产目录和领域约束复核，并在返回前重新检查资产结构摘要。由指标 AI 发起改造时，带 `originTableId` 的映射表数据集不进入现有数据集修改画布；`CREATE_DATASET` 会打开空白普通数据集画布，并把映射表作为不可原地改造的来源节点使用。指标流程跳转完成后，前端会等待空白画布或目标草稿加载完毕，并以一次性键自动发起一轮数据集 AI 方案生成；浏览器回退、React 严格模式重放或重复渲染不会重复调用，生成失败仍可由用户重试。自动触发不自动应用、保存或发布方案。提案接口不持久化、不发布、不执行查询；用户在前端明确应用后仍会经过现有 DSL 校验和保存流程。需要用于指标设计时，保存后的普通数据集草稿还必须提交数据集发布申请并通过人工审批，不能由 AI 提案或草稿保存绕过发布门禁。

预留按完整请求字节的 `1 字节/Token`、每张图片额外 16384 Token、最大输出和最大尝试次数计算。所有状态的配额计量至少保留该预留；成功实耗更高时采用实耗，缺失或全零 Usage 会作为非法响应失败收口。这样不会因重试、超时、5xx 或省略 Usage 释放预算，代价是配额可能偏保守。供应商最终计量仍可能高于模型无关的视觉预估，此时本次调用会如实超额结算并阻断后续请求，不能追溯取消已经发生的上游费用。

审计明确不保存以下内容：

- API Key、Authorization 请求头或连接凭据；
- 原始或脱敏后的提示词正文；
- 图片 URL、附件正文或业务数据样本；
- 模型响应正文、拒答正文或供应商错误正文。

`input_hash` 是发送前完成规范化和脱敏后的请求 SHA-256，只用于关联相同输入。上游请求 ID 只保存 SHA-256，模型名采用本地配置，结束原因收敛为固定枚举，避免把上游可控文本写入审计。元数据模块仍保留自己的领域任务与建议审计；通用审计负责跨用途的租户用量和供应商调用事实。

## 配置

| 环境变量 | 默认值 | 说明 |
|---|---:|---|
| `AI_BASE_URL` | `https://mgallery.haier.net/v1/` | 兼容协议根地址；远程仅 HTTPS，HTTP 仅限 loopback |
| `AI_MODEL` | `deepseek-v3` | 模型名称 |
| `AI_API_KEY` | 空 | 空值时返回明确不可用状态，不发起网络请求 |
| `AI_REQUEST_TIMEOUT` | `25s` | 单次领域调用总时限 |
| `AI_ATTEMPT_TIMEOUT` | `8s` | 每次 Provider 尝试时限 |
| `AI_MAX_ATTEMPTS` | `3` | 含首次调用，范围 1–5 |
| `AI_RETRY_BASE_DELAY` | `200ms` | 指数退避起点 |
| `AI_RETRY_MAX_DELAY` | `2s` | 本地退避及 `Retry-After` 最大等待 |
| `AI_MAX_INPUT_BYTES` | `262144` | 脱敏后完整 Provider 请求上限 |
| `AI_INPUT_COST_MICROS_PER_MILLION_TOKENS` | `0` | 每百万输入 Token 的微货币单位成本 |
| `AI_OUTPUT_COST_MICROS_PER_MILLION_TOKENS` | `0` | 每百万输出 Token 的微货币单位成本 |
| `AI_EMBEDDING_BASE_URL` | 同 `AI_BASE_URL` | 独立 embedding 兼容协议根地址 |
| `AI_EMBEDDING_API_KEY` | 同 `AI_API_KEY` | 可独立配置的 embedding 密钥，不写入仓库或日志 |
| `AI_EMBEDDING_MODEL` | `Qwen3-Embedding-4B` | 表/字段与指标语义文档的 embedding 模型 |
| `AI_EMBEDDING_DIMENSIONS` | `2560` | 与 `halfvec(2560)` 存储合同一致，其他维度失败关闭 |
| `AI_EMBEDDING_TIMEOUT` | `15s` | 单次 embedding HTTP 超时 |
| `DATASET_AI_RETRIEVAL_MODE` | `HYBRID` | `LEXICAL`、`SHADOW` 或 `HYBRID`；开发验收已切换为混合召回 |

价格配置为 `0` 时仍统计和限制 Token，但费用统计为零。生产环境应按实际模型账单配置两个价格，并通过 T0910 的受控真实模型验收确认 Schema 方言、限流和费用口径。

## 领域 AI 错误补充

元数据补全、数据集 DAG 提案与指标创建提案共用两类稳定响应：

- `403 AI_TENANT_FORBIDDEN`：租户未启用 AI、用途未授权或操作者不再有效；
- `429 AI_QUOTA_EXCEEDED`：日请求、月 Token 或月费用任一配额不足。

数据集 DAG 修改意图无法唯一解析时返回 `409 DATASET_AI_CLARIFICATION_REQUIRED`，响应 `message` 是模型生成并经长度约束的问题；此时不调用规划器、不返回候选图，也不改变原画布。该响应与非法模型输出不同，客户端应展示问题并让用户补充目标，而不是自动重试同一指令。

超时、Provider 未配置和非法结构化输出继续使用既有错误合同。`502 DATASET_AI_INVALID_OUTPUT` 除稳定 `reasonCode`/`stage` 外，返回由本地校验器映射的 `diagnosticCode` 和可执行 `suggestion`，例如 `INPUT_CONNECTION_MISMATCH`、`REQUESTED_CHANGE_MISSING`、`FIELD_LINEAGE_INCOMPLETE` 或 `TRANSFORM_COMPONENT_REQUIRED`。这些文案不包含未脱敏表/字段名或模型原文；无论失败类型如何，HTTP 响应和数据库都不会回显供应商正文。
