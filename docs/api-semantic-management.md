# 语义管理 API

本 API 管理租户内的标签 taxonomy、标签与资产的绑定，以及 DWS 维度、成员别名
和维度—指标兼容关系。业务事务只写受治理的事实、任务和审计日志，不同步生成
语义文档或手工写向量。数据库触发器会在同一事务内合并写入
`semantic_change_outbox`，worker 随后重建文档并向量化。

## 权限、隔离与并发

- 所有接口都需要 Bearer access token。
- 一般查询需要全局 `DATASET:READ`，写入需要全局 `DATASET:MANAGE`。成员列表、
  成员别名列表和成员到指标检索是数据面读取：它们在数据库查询内按 actor 重新判定
  目标 DWS 数据集的全局或对象级 `DATASET:READ`，因此只有对象授权的用户也可使用；
  成员到指标检索另外需要全局 `METRIC:READ`。
- 服务从 token 取得 `tenantId` 与 actor，客户端不能指定租户或审计用户。
- 每个数据库操作都通过 tenant transaction 设置 `app.tenant_id`，并由 FORCE RLS
  二次隔离。跨租户 ID 对调用方表现为不存在。
- 标签使用显式 `version`；编辑和停用必须提交 `expectedVersion`。
- v60 的别名和绑定表没有业务版本列，因此响应提供不透明的 `recordVersion`；
  编辑和删除必须原样提交 `expectedRecordVersion`。令牌过期返回 `409`。
- 标签代码、别名或资产绑定唯一键冲突也返回 `409`。客户端应重新读取后决定，
  不应盲目覆盖。
- 维度、维度成员别名和兼容关系使用显式 `version`；任务提交还必须携带
  `Idempotency-Key`。actor、版本推进和审计均由服务端处理。

## 标签

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| `GET` | `/api/v1/semantic/tags` | 列表；支持 `q`、`category`、`status`、`limit`、`offset` |
| `POST` | `/api/v1/semantic/tags` | 创建草稿或活动标签 |
| `PUT` | `/api/v1/semantic/tags/{id}` | 全量编辑；需要 `expectedVersion` |
| `POST` | `/api/v1/semantic/tags/{id}/deprecate` | 不可逆停用；需要 `expectedVersion` |

受控 `category`：

`BUSINESS_DOMAIN`、`BUSINESS_ENTITY`、`TABLE_FUNCTION`、`USAGE_SCOPE`、
`DATA_GRAIN`、`JOIN_ROLE`、`SENSITIVITY`、`FREEFORM`。

`governance` 为 `CONTROLLED` 或 `FREEFORM`。普通写入状态为 `DRAFT` 或
`ACTIVE`；`DEPRECATED` 必须走单独的停用接口，且停用后不可直接复活或编辑。
父标签可编辑，但服务拒绝直接或传递环。

创建示例：

```json
{
  "code": "home_ecosystem",
  "name": "智家生态圈",
  "description": "受治理的业务域名称",
  "category": "BUSINESS_DOMAIN",
  "governance": "CONTROLLED",
  "status": "ACTIVE"
}
```

## 标签别名

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| `GET` | `/api/v1/semantic/tag-aliases` | 列表；支持 `tagId`、`q`、`aliasType`、分页 |
| `POST` | `/api/v1/semantic/tag-aliases` | 创建别名 |
| `PUT` | `/api/v1/semantic/tag-aliases/{id}` | 编辑别名值/类型/语言；需要行版本 |
| `DELETE` | `/api/v1/semantic/tag-aliases/{id}` | 删除；body 中需要行版本 |

`aliasType` 为 `BUSINESS`、`ABBREVIATION`、`LEGACY`、`LLM` 或 `USER`。
语言代码采用如 `zh-CN`、`en-US` 或 `zh` 的格式。别名所属标签是记录身份，
PUT 时必须保持原 `tagId`；如需改绑，应删除并重新创建。

“690”不是服务端硬编码规则。它可以由管理员作为任意受控标签的历史别名录入：

```json
{
  "tagId": "<智家生态圈标签 ID>",
  "alias": "690",
  "aliasType": "LEGACY",
  "languageCode": "zh-CN"
}
```

这种数据驱动方式允许不同租户采用不同的历史编码，不会把单一业务口径固化到代码。

## 资产标签绑定

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| `GET` | `/api/v1/semantic/asset-tag-bindings` | 列表；支持 `tagId`、`assetType`、`status`、分页 |
| `POST` | `/api/v1/semantic/asset-tag-bindings` | 创建绑定 |
| `PUT` | `/api/v1/semantic/asset-tag-bindings/{id}` | 编辑评审信息；需要行版本 |
| `DELETE` | `/api/v1/semantic/asset-tag-bindings/{id}` | 删除；body 中需要行版本 |

受控字段：

- `assetType`：`DATASET_VERSION`、`DATASET_FIELD`、`DIMENSION`、
  `DIMENSION_MEMBER`、`METRIC_VERSION`。
- `origin`：`USER`、`LLM`、`RULE`、`IMPORT`。
- `status`：`SUGGESTED`、`APPROVED`、`REJECTED`。
- `confidence`：可选，范围 `0..1`。
- `evidence`：JSON object，最多 64 KiB；不能是数组、字符串或原始样本行。

资产类型决定且只允许对应的身份字段：

| assetType | 必填身份字段 |
| --- | --- |
| `DATASET_VERSION` | `datasetId`, `datasetVersionId` |
| `DATASET_FIELD` | `datasetId`, `datasetVersionId`, `datasetFieldId` |
| `DIMENSION` | `dimensionId` |
| `DIMENSION_MEMBER` | `dimensionId`, `dimensionMemberId` |
| `METRIC_VERSION` | `metricId`, `metricVersionId`, `metricDatasetVersionId` |

PUT 只允许修改 `origin`、`status`、`confidence`、`evidence`。标签和资产身份
不可原地改绑，因为 v60 触发器只为新行身份生成重建事件；改绑必须删除并重新创建，
以确保旧、新语义对象都收到正确事件。`APPROVED` 时，服务用当前 actor 填充
`approvedBy/approvedAt`；其他状态会清空批准字段。

## DWS 维度勘测候选

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| `GET` | `/api/v1/semantic/dimension-survey-candidates` | 列表；支持 `datasetId`、`datasetVersionId`、`status`、`fieldRole`、分页 |
| `GET` | `/api/v1/semantic/dimension-survey-candidates/{id}` | 读取候选及冻结证据 |
| `PUT` | `/api/v1/semantic/dimension-survey-candidates/{id}` | 编辑 `SUGGESTED` 候选；需要 `expectedVersion` |
| `POST` | `/api/v1/semantic/dimension-survey-candidates/{id}/accept` | 接受并创建 `PUBLISHED` 维度；需要 `expectedVersion` |
| `POST` | `/api/v1/semantic/dimension-survey-candidates/{id}/reject` | 拒绝；需要 `expectedVersion` 和非空原因 |

DWS 版本发布时先登记 `WAITING_MATERIALIZATION` 勘测运行；只有该精确版本的
`ACTIVE` 物化出现后，运行才变为 `SUCCEEDED` 并产生候选。候选只来自
`DIMENSION`、`ATTRIBUTE`、`TIME`、`IDENTIFIER` 字段，永远不从 `MEASURE`
生成。证据固定 `datasetVersionId + schemaHash + materializationId +
materializationSnapshotHash + rowCount + field`，并显式记录
`containsBusinessSamples=false`；勘测不读取、回传或存储业务样本值。

候选与正式维度分离，初始状态只是 `SUGGESTED`，不会自动发布。名称、说明、类型和
索引策略可评审编辑，但冻结身份和证据不可改，敏感/高基数标志及索引策略只能收紧。
候选响应内的 `profile` 是同一精确物化和字段的冻结画像证据，包含 `status`、
`rowCount`、`nonNullCount`、`nullCount`、`distinctCount`、`distinctOverflow`、
`distinctRatio`、风险结论、推荐策略、版本和证据摘要，但不包含任何业务值。
`QUEUED / RUNNING / FAILED / STALE` 不能授权 `FULL`。

普通字段画像成功后状态为 `SUCCEEDED`。`IDENTIFIER` 或语义类型为
`IDENTIFIER` 的字段不扫描数据，状态为 `SKIPPED_POLICY`，结果码为
`IDENTIFIER_FIELD_PROFILE_SKIPPED`，建议 `EXACT_ONLY`。敏感字段同样在扫描前
短路，结果码为 `SENSITIVE_FIELD_PROFILE_SKIPPED`，建议 `NONE`，所有计数均为空。
敏感下限会考虑当前和历史已批准的 `SENSITIVITY` 绑定（即使标签后来停用）、非停用
敏感维度、候选风险及既往敏感跳过；只有创建新的数据集版本才能重新勘测，而不会由
后台任务自动放松同一版本。

接受操作在一个租户事务内重新验证勘测运行、当前发布指针、精确字段、schema、
snapshot 和同一个 `ACTIVE DWS` 物化。它还会在精确版本/字段的事务锁下重新读取
当前活动且已批准的敏感标签，不能用生成候选时的旧风险快照越过治理。若标签批准与
接受并发，先批准则接受返回 `409`；先接受则标签事务立即把已发布维度收紧为
`sensitive=true + NONE`，并跳过尚未完成的完整扫描。已批准绑定所引用的标签若
后来才被编辑为活动 `SENSITIVITY`，也会按相同字段锁和收紧规则处理。

接受 `FULL` 候选时，同一事务会以确定性幂等键登记成员刷新任务，并固定刚刚验证的
物化，不需要再手工提交第一笔刷新。响应中的 `memberRefreshJob.status=QUEUED`、
`memberSearchReady=false` 和 `nextAction=WAIT_FOR_MEMBER_REFRESH` 表示只有任务
成功后成员值检索才可用。自动任务使用 100,000 个规范成员和 60 秒的边界；超基数
或超时会整体失败并保留旧快照，不会用截断结果冒充完整索引。`EXACT_ONLY` 和
`NONE` 不发起扫描，响应分别给出 `USE_EXACT_MATCH_ONLY` 和
`MEMBER_INDEX_DISABLED`。

## DWS 维度

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| `GET` | `/api/v1/semantic/dimensions` | 列表；支持 `q`、`datasetVersionId`、`dimensionType`、`status`、分页 |
| `POST` | `/api/v1/semantic/dimensions` | 创建维度 |
| `GET` | `/api/v1/semantic/dimensions/{id}` | 读取维度 |
| `PUT` | `/api/v1/semantic/dimensions/{id}` | 编辑语义属性；需要 `expectedVersion` |
| `POST` | `/api/v1/semantic/dimensions/{id}/deprecate` | 不可逆停用；需要 `expectedVersion` |
| `GET` | `/api/v1/semantic/dimensions/{id}/members` | 成员列表；支持 `q`、`status`、分页 |

维度只能固定到同租户、精确且已发布的 DWS 数据集版本，以及该版本上的
`DIMENSION`、`ATTRIBUTE`、`TIME` 或 `IDENTIFIER` 字段；`MEASURE` 不能伪装成维度。
这组数据集/版本/字段身份不可原地修改，变更身份应新建维度并停用旧维度。名称、
说明、类型、成员策略、敏感性、高基数标志和草稿/发布状态可编辑。

`memberIndexPolicy`：

- `FULL`：允许异步完整去重扫描；
- `EXACT_ONLY`：不做自动枚举，任务以 `SKIPPED` 明确结束，为后续按需精确解析预留；
- `NONE`：禁用成员索引，任务以 `SKIPPED` 明确结束。

`sensitive=true` 时数据库和服务层强制 `NONE`；`highCardinality=true` 时禁止
`FULL`，只能选择 `EXACT_ONLY` 或 `NONE`。敏感性开启、策略收紧或维度停用会在同一事务
停用活动成员，并把尚未完成的 FULL 刷新任务标记为 `SKIPPED`，防止旧任务继续扫描。

从 `FULL` 收紧到 `EXACT_ONLY` 或 `NONE` 时，现有活动成员会在同一事务内停用，
且维度的当前刷新快照被清空，避免旧的完整索引继续参与检索。
敏感维度的成员与别名不会从列表 API 或“维度值 → 指标”搜索接口返回；历史成员
语义文档会被删除。成员值只保留在受 RLS 保护的控制表中用于未来受控精确解析，
不会生成语义文档或发送给外部 embedding provider。

创建示例：

```json
{
  "datasetId": "<DWS 数据集 ID>",
  "datasetVersionId": "<精确已发布版本 ID>",
  "fieldId": "field_ecosystem",
  "code": "home_ecosystem",
  "name": "生态圈",
  "description": "智家生态圈维度",
  "dimensionType": "ORGANIZATION",
  "memberIndexPolicy": "FULL",
  "highCardinality": false,
  "sensitive": false,
  "status": "PUBLISHED"
}
```

## 维度成员刷新

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| `POST` | `/api/v1/semantic/dimensions/{id}/member-refresh-jobs` | 提交任务；需要 `Idempotency-Key` 和 `expectedDimensionVersion` |
| `GET` | `/api/v1/semantic/dimension-member-refresh-jobs` | 查询任务；支持 `dimensionId`、`status`、分页 |

`FULL` 任务在提交时固定维度版本、字段代码和该精确数据集版本的当前
`ACTIVE DWS` 物化。worker 不接受客户端表名或 SQL；长扫描读取由该登记身份推导的
run-scoped 物理表，有界原子合并阶段再验证 `warehouse_published` 稳定视图，并在
两个阶段分别验证：

- 维度仍为相同版本、相同精确字段和 `PUBLISHED + FULL + 非敏感`；
- 数据集当前发布指针、版本状态和物化仍是同一精确版本上的 `ACTIVE DWS`；
- 同一物化、schema、snapshot 和字段存在 `SUCCEEDED + FULL` 的当前画像；
- 发布对象确为 worker 自己拥有、且只依赖登记物理表的普通视图；
- 字段仍存在，所有动态标识符经过严格校验与引用。

worker 用游标流式读取完整 `SELECT DISTINCT`。原值按 C collation 确定性排序，
校验无首尾空格后，再以 NFKC 和小写后的规范值去重，因此 `ABC`、`abc`、全半角变体不会随机
撞唯一键；`maxMembers` 按规范成员数计算。扫描不会用 `LIMIT` 截断未知尾部，
超基数或超时会让整次任务失败。默认上限为 100,000 个成员、60 秒；请求上限为
1,000,000 个和 300 秒。

刷新在同一专用 PostgreSQL 连接的单个 `READ COMMITTED` 事务内分成“扫描 +
late-gate merge”两个阶段。扫描阶段只锁并读取精确、run-scoped 的物理表，不锁
租户治理门或稳定发布视图；把每批最多 1,000 行写入 scratch 临时表，在 PostgreSQL 内
规范去重后立即合并到上限为 `maxMembers + 1` 的 `ON COMMIT PRESERVE ROWS`
stage，并清空 scratch；Go 堆内不保存 100,000 个成员的 map。物理表由可信 worker
拥有且“ACTIVE 后不再 DML”是 report_worker 必须遵守的可信生命周期边界；这不是
针对已攻陷 owner 的数据库绝对封存。事务额外使用 `SHARE` relation lock，并一直
持有到 generation 提交，因此扫描期间已经等待的 `INSERT/UPDATE/DELETE/TRUNCATE`
不能穿过扫描与 merge 之间。扫描完成但不提交事务，late-gate merge 才取得租户治理门，
并按物化 → 数据集 → 字段 → 任务/维度行的顺序重新锁定和核对
精确维度版本、策略/敏感性、materialization、schema、snapshot、画像和未过期
lease，并复核稳定发布视图。物化激活可以在扫描期间切换稳定视图而不等待旧物理表
扫描；fenced merge 随后看到 source 漂移并返回
`REFRESH_SOURCE_CHANGED`，或看到激活事务已原子跳过旧任务而触发 lease fence；
两种情况都不会合并 stage。`READ COMMITTED` 保证 late gate 的重读能看到扫描期间
已提交的治理变化；同一事务设置租户上下文、statement timeout 和 lock timeout。
无论成功、失败还是取消，
stage 都会在连接返回池前删除，清理失败则直接丢弃该连接。

成员新增、变化、恢复、未见成员停用，以及维度快照指针和任务成功状态只在上述
事务的 merge 阶段原子提交。该阶段不再包含源表全量 `DISTINCT`，但对最多 1,000,000 个
成员的 INSERT/UPDATE/DEPRECATE 仍是按成员量增长的有界工作；为保证 generation
一次切换，该段合并期间会持有租户治理门，并非常数时间临界区。失败时旧快照保持
不变。前后带空格、纯空白、控制字符、非法 UTF-8 或超长值有意失败关闭，不自动
trim 后掩盖源数据质量问题。

成员列表、别名列表和“成员值 → 指标”搜索只返回 `ACTIVE` 成员，并要求成员的
`refreshGeneration + lastRefreshJobId` 与维度当前指针完全一致；该任务还必须是
当前物化、当前维度版本上的 `SUCCEEDED` 任务。显式查询 `DEPRECATED` 也不会暴露
历史成员。新物化、画像待处理/失败/过期或维度编辑后，旧代际不能被复用。

这三个读取面还继承 actor 的数据策略。只要目标维度字段存在适用于该 actor 的任一
非 `ALLOW` 列策略（包括 `DENY`、`MASK`、`AGGREGATE_ONLY`、`NULLIFY` 或
`HASH`），或目标数据集存在适用行策略，预计算成员索引就被视为无法安全裁剪：

- 精确指定维度的 members/aliases 请求返回稳定
  `403 SEMANTIC_MEMBER_ACCESS_DENIED`，不返回成员键、标签、规范值或别名；
- 未指定维度的别名目录以及跨维度 member-metric-search 只在同一 SQL 快照中返回
  actor 有权且无策略限制的维度；无权/受限命中与不存在都表现为空，不提供探测
  侧信道；
- 跨租户维度 ID 继续在 FORCE RLS 下表现为不存在。

直接通过维度 API 创建 `FULL` 维度时仍需显式提交刷新任务；通过上节勘测候选
接受的第一笔 `FULL` 刷新会自动登记。两条路径都不能在任务 `SUCCEEDED` 之前
宣称成员值已经可检索。

典型终态结果码包括：

- `CARDINALITY_LIMIT_EXCEEDED`
- `REFRESH_TIMEOUT`
- `PUBLISHED_VIEW_UNTRUSTED`
- `MEMBER_VALUE_INVALID`
- `REFRESH_SOURCE_CHANGED`
- `EXACT_ONLY_AUTOMATIC_DISCOVERY_SKIPPED`
- `MEMBER_INDEX_DISABLED`

## 维度成员别名

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| `GET` | `/api/v1/semantic/dimension-member-aliases` | 支持 `dimensionId`、`dimensionMemberId`、`q`、`aliasType`、分页 |
| `POST` | `/api/v1/semantic/dimension-member-aliases` | 为活动成员创建别名 |
| `PUT` | `/api/v1/semantic/dimension-member-aliases/{id}` | 编辑别名；需要 `expectedVersion` |
| `DELETE` | `/api/v1/semantic/dimension-member-aliases/{id}` | 删除别名；body 中需要 `expectedVersion` |

成员别名类型为 `CODE`、`BUSINESS`、`ABBREVIATION`、`LEGACY`、`LLM` 或
`USER`，可带有效期。“690 → 智家生态圈”是普通的租户数据，不是代码分支：

```json
{
  "dimensionId": "<生态圈维度 ID>",
  "dimensionMemberId": "<智家生态圈成员 ID>",
  "alias": "690",
  "aliasType": "LEGACY"
}
```

别名编辑会触发 outbox 失效处理；检索直接使用租户内
`dimension_members / dimension_member_aliases` 精确索引，成员值不生成语义文档，
也不进行外部向量化。

## 维度—指标兼容关系

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| `GET` | `/api/v1/semantic/dimension-metric-compatibilities` | 支持 `dimensionId`、`metricVersionId`、`status`、分页 |
| `POST` | `/api/v1/semantic/dimension-metric-compatibilities` | 提议关系 |
| `PUT` | `/api/v1/semantic/dimension-metric-compatibilities/{id}` | 编辑 `PROPOSED` 关系；需要 `expectedVersion` |
| `POST` | `/api/v1/semantic/dimension-metric-compatibilities/{id}/verify` | 验证；需要 `expectedVersion` |
| `POST` | `/api/v1/semantic/dimension-metric-compatibilities/{id}/reject` | 拒绝；需要 `expectedVersion` |

关系类型为 `DIRECT / BRIDGE / DERIVED`，扇出策略为
`SAFE / DEDUPLICATE / UNSAFE`。Join 路径只能是有界结构化 JSON；含 SQL、
query、凭据或原始样本等字段会被拒绝。`VERIFIED` 和 `REJECTED` 是终态。
`UNSAFE` 永远不能进入 `VERIFIED`；验证时服务与数据库触发器都会重新确认维度、
指标版本、DWS 数据集版本和两侧精确 `ACTIVE DWS` 物化仍然可用。

## 成员值到指标检索

`GET /api/v1/semantic/member-metric-search?q=690&limit=20`

检索只做规范值或有效别名的精确匹配，然后沿以下小型索引链返回结果：

```text
成员值/别名 → PUBLISHED 维度 → VERIFIED 且非 UNSAFE 的兼容关系
           → 当前 PUBLISHED 指标版本 → 精确 ACTIVE DWS 物化
           → warehouse_published 视图
```

响应含匹配类型、规范成员、维度、指标、数据集版本及发布视图身份。它不执行指标
SQL，也不物化“成员 × 指标”笛卡尔积。结果同时要求 actor 对维度数据集和指标
数据集都有 `DATASET:READ`；每个未授权或策略受限维度都在 SQL 内被过滤，查询不会
透露该成员或别名是否存在。

## 自动标签建议

ODS、DWD、DWS 数据集版本进入 `PUBLISHED` 的同一事务会写入
`dataset_tag_suggestion_jobs`。这张表是独立的 durable outbox，不复用成员刷新任务。
任务固定 `tenantId + datasetId + datasetVersionId + layer + schemaHash`、源发布版本
快照和 Prompt 版本；相同数据集版本及 Prompt 版本只会入队一次。

worker 的治理边界如下：

1. 只处理仍是数据集当前 `PUBLISHED` 指针的精确版本，并同时验证 schema hash、
   层级、冻结依赖和 ODS 源发布版本；任一事实变化都以
   `SKIPPED / SUBJECT_CHANGED` 结束；
2. ODS 输入只使用表、投影字段、键属性和已有业务元数据；DWD/DWS 使用精确上游
   发布版本的字段、粒度及已批准标签摘要，并结合当前版本的字段和 DAG；
3. 不读取或发送业务样本行，不保存凭据、SQL、表达式字面值、Prompt 正文或模型
   正文；统一 AI 审计只记录摘要、状态和用量；
4. 模型只能从当前租户 `ACTIVE + CONTROLLED` taxonomy 的
   `BUSINESS_DOMAIN / BUSINESS_ENTITY / TABLE_FUNCTION / USAGE_SCOPE /
   DATA_GRAIN / JOIN_ROLE` 中选择。严格 JSON Schema 将 `tagId` 限定为现有 ID，
   服务端再次校验、去重和规范化；
5. 结果最多创建 `origin=LLM,status=SUGGESTED` 的
   `DATASET_VERSION` 绑定。模型不能创建标签、不能批准绑定，也不会改写已存在的
   `APPROVED` 或 `REJECTED` 结论；
6. 外部 LLM 调用前先续租，并在调用期间每隔 `lease / 3` 以
   owner、lease token、attempt 和精确数据集版本心跳。心跳失败会取消模型
   context，且不执行完成或失败写入，由过期租约的正常 reclaim 保留重试语义；
7. 任务完成与每条不可变 suggestion item 都绑定 lease token、输入/输出摘要和
   AI request。写入前重新加载全部输入，避免迟到 worker 覆盖新事实。

建议的查询和评审复用上文资产标签绑定 API：

- `GET /api/v1/semantic/asset-tag-bindings?assetType=DATASET_VERSION&status=SUGGESTED`
  查询待审建议；
- `PUT /api/v1/semantic/asset-tag-bindings/{id}` 携带最新
  `expectedRecordVersion`，将状态改为 `APPROVED` 或 `REJECTED`；
- 批准事务会由 v60 触发器推进 `semantic_change_outbox.event_version`，随后重建
  对应数据集版本的语义文档和向量。

AI provider 未配置时任务保持 `PENDING`；临时错误按任务重试预算退避，超过预算
进入 `FAILED`。任务表没有面向普通客户端的创建接口，避免绕过发布事务制造任意
LLM 请求。

## 响应与错误

列表响应统一为：

```json
{"items":[],"total":0,"limit":50,"offset":0}
```

查询响应带 `Cache-Control: no-store`。稳定错误码：

- `SEMANTIC_REQUEST_INVALID`（400）
- `SEMANTIC_RESOURCE_NOT_FOUND`（404）
- `SEMANTIC_VERSION_CONFLICT`（409）
- `SEMANTIC_IDEMPOTENCY_CONFLICT`（409）
- `SEMANTIC_PERSISTENCE_FAILED`（500）

所有请求体采用严格 JSON 解码：未知字段、多个 JSON 文档和超过 256 KiB 的请求
都会被拒绝。
