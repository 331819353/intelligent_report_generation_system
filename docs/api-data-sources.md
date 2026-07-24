# 数据源管理 API

基础路径：`/api/v1/data-sources`。所有接口都要求 Bearer 访问令牌及 `DATA_SOURCE:MANAGE` 权限，租户 ID 只取自令牌。

## 创建和更新

- `POST /api/v1/data-sources`
- `PUT /api/v1/data-sources/{id}`

MySQL 示例：

```json
{
  "code": "sales_mysql",
  "name": "销售 MySQL",
  "description": "销售域只读明细库",
  "ownerId": "00000000-0000-4000-8000-000000000001",
  "visibility": "PRIVATE",
  "type": "MYSQL",
  "host": "mysql.internal",
  "port": 3306,
  "database": "sales",
  "username": "report_reader",
  "password": "仅在请求中提交的数据库密码"
}
```

Oracle 将 `type` 改为 `ORACLE`，通常使用端口 `1521`，`database` 填写 Service Name 或 SID。接口不接收 JDBC 连接串；`host`、`port`、`database`、`username` 必须拆分提交。

`visibility` 可取 `PRIVATE` 或 `TENANT_PUBLIC`，省略时为 `PRIVATE`。`ownerId` 省略时使用创建人；描述、所属人和可见性与创建人、修改人一并保存在控制库中。

密码仅在 HTTPS 请求处理期间短暂存在。Go 服务使用 `DATA_SOURCE_CREDENTIAL_KEY` 对完整连接凭据执行 AES-256-GCM 加密，控制库只保存不可回显的内部引用；列表、详情、审计和错误响应均不返回密码或引用。生产环境必须通过密钥系统注入独立的 32 字节 Base64 密钥，不能使用 `.env.example` 的开发默认值。

Oracle 的非敏感连接选项放在 `config` 中：

```json
{
  "oracleConnectMode": "SERVICE_NAME",
  "schemas": ["REPORT_READER"]
}
```

`oracleConnectMode` 可取 `SERVICE_NAME` 或 `SID`。Schema 名会转为大写、去重并严格校验，最多配置 20 个；实际可同步范围仍受源库账号权限约束。

每次创建、更新连接字段或重新上传文件都会生成不可变 `configVersionId` 和 `configHash`。编辑请求必须提交详情响应中的正整数 `expectedVersion`；若期间已被其他请求修改，接口返回 HTTP 409 和 `DATA_SOURCE_VERSION_CONFLICT`，客户端必须刷新后再编辑，不能覆盖较新的草稿。编辑请求仍需提交非敏感连接字段；`password` 传空字符串表示保留已保存密码，填写新值表示轮换密码。已发布数据源的编辑只产生新草稿，旧的 `publishedVersionId` 和连接配置继续服务，直到新草稿测试通过、提交审核并获批。

数据库同时拒绝直接插入 `PUBLISHED` 数据源，以及缺少当前草稿精确版本、配置摘要
和未过期 worker 证明的发布指针切换。`report_app` 只能通过受限函数入队，不能直接
写任务、证明或历史 `data_source_test_runs`；通用 `report_worker` 也没有这些权限。
独立 `report_connection_tester` 只能通过 claim、heartbeat、complete、fail 函数改变
任务，不能自行入队或直接写表。

成功证明的 `completedAt` 和 `expiresAt` 均由 PostgreSQL 生成，且
`expiresAt = completedAt + 30 分钟`。完成函数还会重新校验任务租约、当前草稿版本和
配置摘要；连接测试期间发生编辑时只会将旧任务封存为 `CANCELLED`。证明插入后不可
修改或删除。历史同步测试记录仍可读，但不能用于新的发布指针切换。

该边界可以抵抗 API 或通用 worker 数据库凭据伪造成功证明；专用连接测试 worker
本身仍是可信执行方，并不构成对远端数据库的密码学证明。它只保存稳定错误码和安全
文案，不持久化连接凭据或驱动原始错误。tester 仍需共享
`DATA_SOURCE_CREDENTIAL_KEY` 来解密 claim 返回的精确配置密文；生产环境必须把它
作为独立可信执行域，配合专用测试 token、只读且仅限 uploads 对象读取的 MinIO
身份和网络策略。若要求连 tester 进程也不能持有主密钥，需要后续接入 KMS/凭据代理
签发短时连接授权。

## 查询和状态操作

- `GET /api/v1/data-sources`：列表。
- `GET /api/v1/data-sources/{id}`：查看详情；返回 host、port、database、username、`createdAt`、`updatedAt`、创建/修改人、所属人、可见范围、验证/发布/可用状态，不返回密码。
- `POST /api/v1/data-sources/{id}/test`：冻结当前草稿并提交连接测试任务，返回 `202 Accepted`；可传 `Idempotency-Key`，服务端只保存其 SHA-256 摘要。
- `GET /api/v1/data-sources/{id}/connection-tests/{jobId}`：轮询安全任务快照；不返回配置摘要、租约、worker 身份、凭据或驱动原错。
- `POST /api/v1/data-sources/{id}/publish`：兼容入口；行为等同于提交发布审核，不再直接切换运行配置。
- `POST /api/v1/data-sources/{id}/publish-requests`：冻结当前草稿和测试证明并提交审核，返回 `202 Accepted`。
- `GET /api/v1/data-sources/{id}/publish-requests`：查看该数据源的审核历史。
- `POST /api/v1/data-sources/{id}/publish-requests/{requestId}/withdraw`：申请人撤销审核中的申请。
- `POST /api/v1/data-sources/{id}/publish-requests/{requestId}/approve`：需要 `DATA_SOURCE:PUBLISH`；审批通过并在同一事务中切换发布指针。
- `POST /api/v1/data-sources/{id}/publish-requests/{requestId}/reject`：需要 `DATA_SOURCE:PUBLISH`；必须填写驳回原因。
- `POST /api/v1/data-sources/{id}/sync`：同步元数据摘要。
- `GET /api/v1/data-sources/{id}/tables/discovery`：只读取源库中当前可见的表清单，不创建资产。
- `POST /api/v1/data-sources/{id}/tables/import`：把用户选择的表提交为后台采样与完善任务，返回 `202 Accepted`。
- `POST /api/v1/data-sources/{id}/tables/refresh`：把已纳管表提交为字段级增量或全量后台刷新任务，返回 `202 Accepted`；可传 `tableIds` 仅刷新指定活动表，省略时刷新全部已纳管表。
- `GET /api/v1/data-sources/{id}/metadata-jobs/{jobId}`：查询后台任务真实进度。
- `GET /api/v1/data-sources/{id}/metadata-jobs/latest-active`：恢复该数据源最近的活动任务；无任务时返回 `{"job":null}`。
- `POST /api/v1/data-sources/{id}/enable`：恢复已暂停数据源。
- `POST /api/v1/data-sources/{id}/disable`：暂停运行中数据源。
- `DELETE /api/v1/data-sources/{id}`：逻辑删除。

连接测试入队响应示例：

```json
{
  "id": "22222222-2222-4222-8222-222222222222",
  "dataSourceId": "33333333-3333-4333-8333-333333333333",
  "configVersionId": "11111111-1111-4111-8111-111111111111",
  "status": "QUEUED",
  "attempt": 0,
  "maxAttempts": 3,
  "requestedAt": "2026-07-24T08:00:00Z"
}
```

成功后的查询响应示例：

```json
{
  "id": "22222222-2222-4222-8222-222222222222",
  "dataSourceId": "33333333-3333-4333-8333-333333333333",
  "configVersionId": "11111111-1111-4111-8111-111111111111",
  "status": "SUCCEEDED",
  "attempt": 1,
  "maxAttempts": 3,
  "serverVersion": "8.4.2",
  "latencyMs": 18,
  "requestedAt": "2026-07-24T07:59:59Z",
  "testedAt": "2026-07-24T08:00:00Z",
  "expiresAt": "2026-07-24T08:30:00Z"
}
```

任务状态为 `QUEUED`、`RUNNING`、`SUCCEEDED`、`FAILED` 或 `CANCELLED`。测试证据
有效期为 30 分钟；期间任何配置修改都会生成新版本并立即使旧任务或证明失效。发布
提交审核或审批时，活动任务、失败、缺少证明、证明过期或版本变化分别返回
`409 DATA_SOURCE_TEST_PENDING`、`409 DATA_SOURCE_TEST_FAILED`、
`409 DATA_SOURCE_TEST_REQUIRED`、`409 DATA_SOURCE_TEST_EXPIRED` 和
`409 DATA_SOURCE_VERSION_CHANGED`。

## 发布审核状态与约束

列表和详情响应增加 `reviewStatus`、`reviewRequestId`、`reviewRequestVersion`、
`reviewNote`、申请人/审核人以及提交/处理时间。`reviewStatus` 的状态机为：

| 状态 | 含义 | 可执行操作 | 数据表配置 |
|---|---|---|---|
| `NOT_SUBMITTED` | 当前草稿未提交 | 修改、测试；测试通过后提交 | 仅已发布运行版本可配置 |
| `PENDING` | 审核中 | 申请人只能撤销；审核人可通过或驳回 | 锁定 |
| `APPROVED` | 审核通过并已上线 | 按正常运行状态管理 | 可用 |
| `REJECTED` | 审核失败 | 查看原因、修改、重新测试和再次提交 | 锁定 |
| `WITHDRAWN` | 申请人已撤销 | 修改、重新测试和再次提交 | 按发布状态判断 |

Web 界面的审批入口只位于工作台“待处理任务”卡片：有 `DATA_SOURCE:PUBLISH` 权限且不是申请人的用户点击卡片，在审批弹窗内通过或驳回。数据源配置页不展示审批按钮；申请人只在该页提交或撤销。审批通过即在同一事务中上线冻结版本，不再出现第二个“发布”动作。

审核申请冻结精确 `configVersionId` 和 `configHash`。提交前以及审批事务内都会重新
校验未过期的专用 worker 连接测试证明；审核期间配置更新、重新测试、暂停、删除和
元数据配置均由服务端拒绝。审批申请事实不可修改，且只允许从 `PENDING` 一次迁移到
`APPROVED`、`REJECTED` 或 `WITHDRAWN`。审批通过、审核状态和运行版本指针在同一
PostgreSQL 事务中提交。

前端只保存 `{sourceId, jobId, configVersionId}` 这一非敏感任务引用，并在同一浏览器
会话中持续轮询到终态。刷新页面或短暂断网后先恢复原任务，不重复提交 POST；离开页面
会取消本地请求，但不会取消 PostgreSQL 中的任务。恢复时若任务已不存在则清理引用，
若期间配置版本变化则刷新数据源状态并要求对新版本重新测试。

状态由三个正交字段表达：

- `status`：兼容现有运行时的生命周期，包含 `DRAFT`、`ACTIVE`、`DISABLED`、`SYNCING`、`ERROR` 等。
- `validationStatus`：当前草稿的 `UNTESTED`、`PASSED` 或 `FAILED`。
- `publicationStatus`：`UNPUBLISHED` 或 `PUBLISHED`；`hasUnpublishedChanges=true` 表示当前草稿尚未切换为发布版本。

首次创建的流转为 `DRAFT/UNTESTED → DRAFT/PASSED → PENDING → APPROVED/ACTIVE/PUBLISHED`。未发布测试失败可进入兼容状态 `ERROR`，重新编辑或测试成功后仍需提交审核。已发布源的新草稿测试失败不会把旧发布版本置为 `ERROR`；同步、发现、采样和查询仍读取旧的不可变发布快照。只有已发布后被停用的 `DISABLED` 数据源可以直接重新启用。删除经过 `DELETING → DELETED`。

同步会保存规范化的表与字段资产，同时保存完整 JSON 快照和 SHA-256 结构哈希。表、字段、约束或索引发生变化时记录 `ADDED`、`CHANGED`、`REMOVED` 差异；源库中消失的表和字段保留历史记录并标记为 `INACTIVE`，不做物理删除。

配置中心的“新增数据表”采用两阶段流程：每次打开弹窗都先实时调用 discovery 接口刷新源库表清单，再由用户全选或选择一部分表。import 请求示例：

```json
{
  "sampleDataMode": "MASK",
  "tables": [
    {"catalogName": "sales", "schemaName": "sales", "tableName": "orders"}
  ]
}
```

接口只持久化批任务和选中的表键，立即返回任务摘要及 `Location`；worker 在请求之外采集技术结构、调用已配置的 LLM 完善业务元数据，并将最终表资产保存到 PostgreSQL。`sampleDataMode` 必须是：

- `DENY`：默认值；不调用采样接口，只向 LLM 发送技术元数据；
- `MASK`：最多读取十行，在应用进程内把字母、数字、日期、二进制和未知驱动值替换为格式占位值后再发送；
- `RAW`：最多读取十行原值并发送，属于高风险显式授权。

租户策略 `ai_tenant_policies.metadata_sample_mode` 是能力上限，按 `DENY < MASK < RAW` 排序；产品默认使用 `MASK`，即读取最多 10 行并在应用进程内完成格式脱敏后再发送给 LLM。请求不能超过租户策略上限。`MASK/RAW` 还会把当前操作者和策略版本冻结到任务中，响应返回 `sampleDataMode` 与 `samplePolicyVersion`。worker 在任务开始、读取源表前以及调用 LLM 前重新验证授权；管理员撤权、策略版本变化或授权人失效时以 `SAMPLE_POLICY_CHANGED` 失败关闭，样本不会继续发送。样本值、脱敏结果和授权正文均不落任务、元数据或审计表。

刷新单表结构复用同一后台流程；配置中心只展示当前技术结构已经完成元数据完善的活动资产，旧结构的成功记录不会让未完善的新结构提前进入清单。整表映射完成时会在同一租户事务内确保其默认单表数据集存在，拓扑仅为“数据节点 → 结束节点”；数据集创建失败会连同本次映射完成标记一起回滚，避免产生不可预览的半成品。

刷新请求体为 `{"mode":"INCREMENTAL","sampleDataMode":"MASK"}` 或 `{"mode":"FULL","sampleDataMode":"DENY"}`，`mode` 缺省为增量，Web 产品层 `sampleDataMode` 缺省为 `MASK`；服务端兼容调用未显式传值时仍按 `DENY` 失败关闭。两种模式都只处理 PostgreSQL 中已纳管且未删除的表，不会自动导入源库中的其他表，也不会复活用户已经删除的资产。

`INCREMENTAL` 是字段级增量刷新。worker 先同步当前技术结构，再逐字段比较已落库的结构版本：仅新增字段、结构发生变化的字段，以及此前尚未成功完善的字段会进入 LLM 请求；只有任务冻结的 `sampleDataMode` 为 `MASK` 或 `RAW` 时，这些字段才会同时进入最多 10 行的数据采样流程。未变化字段不采样、不调用 LLM，也不会被本次模型结果覆盖，其业务名称、说明、标签、语义类型、敏感级别、人工锁定状态和版本保持原值。只有表级技术信息本身发生变化时，才重新完善表级业务信息。字段从源表中消失时保留历史记录并标记为 `INACTIVE`，不调用 LLM；只有 Connector 返回表数量一致、时间水位和快照哈希有效且业务键无重复的权威完整快照时，源表缺失才会停用 PostgreSQL 中对应的表资产及其字段；更晚的同结构同步会阻止旧任务停用资产。所有删除均不物理清除历史数据。

`FULL` 会对目标范围内的全部活动表和活动字段执行完整 LLM 完善，不使用字段级未变化跳过规则；只有任务明确选择 `MASK/RAW` 时才采样。无论哪种模式，任务都会把模型结果合并到本次明确处理的目标范围，不能用局部响应覆盖其他已落库字段。

任务状态可取 `QUEUED`、`RUNNING`、`SUCCEEDED`、`PARTIAL` 或 `FAILED`。查询响应返回 `total`、`completed`、`succeeded`、`skipped`、`failed`、`stage` 和 `currentTable`；存在失败项时还会返回可选的 `failures`，逐项包含库/Schema、表名、稳定错误码和安全文案。`completed` 是成功、跳过和失败之和，页面进度条使用 `completed / total`，不按时间伪造进度。单表失败不会阻断后续表，响应和审计不包含样本数据、连接器原始错误或模型正文。worker 使用租户 RLS 事务、数据库租约和独立心跳领取任务；AI 成功与任务项及结构哈希绑定，租约恢复可直接收口已提交结果，API 或页面关闭不会中止任务。

数据源的修改、测试、暂停/恢复和删除操作管理连接本身；表资产的修改、字段映射、刷新、停用/恢复和删除操作管理 PostgreSQL 中的资产记录，两组生命周期相互独立。字段映射只允许修改业务名称、说明、标签、语义类型、敏感级别和人工锁定，不改写源库物理字段名或技术类型。

Python Connector 按数据源维护有界连接池，并同时执行每租户查询并发上限和服务进程
全局上限。Go 核心从租户配额表下发单源连接数和租户并发限制，但不会向 Python 服务
日志或响应传递明文凭证。Connector 进程另有两个不能由数据源放大的硬边界：

- `CONNECTOR_MAX_POOLS`：连接池注册表上限，开发默认 1,000；
- `CONNECTOR_MAX_TOTAL_CONNECTIONS`：池化连接与 one-shot 连接合计的物理 socket
  上限，开发默认 100。

两项在 production 必须显式配置。创建新池达到池数量上限时，LRU 只淘汰没有借用
连接、没有等待 acquire 引用的完全空闲池；不会关闭活动池。创建新物理连接达到全局
上限时，也只按 LRU 回收已有池中的空闲连接，活动 socket 永不成为淘汰对象；无空闲
连接可回收时最多等待本次 `connectTimeout`，随后失败关闭。这样大量租户、频繁密码
轮换或不断变化的连接参数不能让池注册表和源库会话无界增长。

草稿连接测试不进入普通连接池，每次使用受
`CONNECTOR_MAX_TOTAL_CONNECTIONS` 约束的 one-shot 连接，并在成功、失败或取消后
关闭；因此大量待测配置不会在空闲 TTL 窗口内残留池或数据库会话。普通连接池支持
空闲 TTL 淘汰，更新或删除数据源时由 Go 调用内部关闭接口释放旧连接。查询请求可
携带唯一 `queryId`，执行器可调用 `/v1/query/cancel`：Oracle 使用驱动取消，MySQL
关闭正在执行的连接。只读查询在执行前进行失败关闭的词法检查，拒绝 CTE-DML、DDL、
事务、锁、文件导出、延时函数、注释和多语句；源数据库账号仍必须只授予只读权限，
不能依赖应用校验代替数据库授权。

数据库样本请求不再使用 `SELECT *`。Go 根据已发现字段显式提交投影，Go 与
Connector 都排除 BLOB/CLOB/BINARY、Oracle `LONG/LONG RAW/XMLTYPE/JSON` 等
不安全大对象类型；Connector 再从源端字段字典复核投影后才执行最多 10 行的
`LIMIT/FETCH FIRST`。样本默认最多 256 列、16 KiB/单元格、64 KiB/行和
512 KiB/完整响应。超限分别使用
`METADATA_SAMPLE_COLUMN_LIMIT_EXCEEDED`、`METADATA_SAMPLE_COLUMN_UNSAFE`、
`METADATA_SAMPLE_VALUE_UNSUPPORTED`、`METADATA_SAMPLE_CELL_BYTES_EXCEEDED`、
`METADATA_SAMPLE_ROW_BYTES_EXCEEDED` 或
`METADATA_SAMPLE_RESPONSE_BYTES_EXCEEDED`；错误和日志不包含单元格值。

技术元数据同步使用 MySQL `SSCursor` 小批读取，并在组装前共享
`CONNECTOR_METADATA_SYNC_MAX_ROWS`（默认 200,000 行）和普通 JSON 64 MiB
预算。普通查询同样用服务端游标增量构造响应，不会先读取 `maxRows` 全量结果；
行数、列数、单元格、单行或整响应超限返回稳定的 `QUERY_*` 代码。ODS 抽取使用
NDJSON 的 1 MiB/单元格、4 MiB/行、1 GiB/整流边界；Go 在 JSON 解码/NDJSON
消费前独立复核。`WAREHOUSE_STAGE_MAX_BYTES` 默认 512 MiB，按每租户、每物化任务
同时约束数据库 ODS 流和 Excel/CSV 的逻辑 staging 行；两条路径任一超限都不能产生
可激活的部分结果。文件 ODS 以
`min(max_excel_file_bytes, WAREHOUSE_STAGE_MAX_BYTES)` 作为 CSV、XLS 和对象读取
硬上限；XLSX 还把展开总量与 worksheet 内存预算分别收紧为解析器自身上限和
`WAREHOUSE_STAGE_MAX_BYTES` 中的更严格值，避免压缩工作簿在 COPY 前无界展开。
因此较低的 staging 上限会保守拒绝“压缩文件本体较大、但选中 Sheet 很小”的文件，
这是安全优先的兼容取舍。预算、取消或客户端断流会先关闭物理连接再关闭服务端游标，
未排空连接绝不回池；
数据库 staging 与完整源流共享事务成功边界，文件逻辑写入超限也会回滚其 staging
事务，均不会生成 ACTIVE 物化。

生产 Connector 的出站合同是两组取交集的显式规则：

- `CONNECTOR_EGRESS_ALLOWLIST` 只接受 `IP/CIDR:port`，不接受 hostname-only
  授权；数据源仍可保存 DNS 名，但其全部 A/AAAA 结果都必须落入批准 CIDR；
- `CONNECTOR_EGRESS_DENYLIST` 必须包含平台 PostgreSQL、Redis、MinIO、API 和云
  控制面网段，deny 优先；loopback、link-local、multicast、metadata 地址及全部
  IPv4-mapped IPv6 永久拒绝；
- 校验通过后数据库驱动连接到选定的已验证 IP，原始 host 只保留在配置/池摘要语义中，
  防止校验与连接之间再次解析 DNS。

应用层校验和 IP pinning 是纵深防御，不替代默认拒绝出站的 Security Group、
NetworkPolicy 或主机防火墙。尤其 Oracle listener/SCAN 可能在协议握手后返回另一个
目标，首次 DSN pinning 无法证明后续 redirect 安全；生产网络层必须只允许批准的
数据库 CIDR/port，并把未批准 redirect 与平台控制面不可达作为上线负测。

数据源创建、更新、连接测试入队、同步、暂停、恢复和删除均记录审计摘要；审计内容
不包含连接配置、密码、内部凭证引用、租约或驱动原始错误。

## Excel 文件版本

使用 `multipart/form-data` 上传，文件字段名为 `file`，可选的 `config` 字段是 JSON：

- `POST /api/v1/excel-files`：创建文件资产和版本 1。
- `POST /api/v1/excel-files/{id}/versions`：覆盖当前源文件并创建不可变新版本；引用该文件的数据源生成待验证草稿版本，测试成功并显式发布后切换。
- `GET /api/v1/excel-files/{id}/versions`：按倒序查询所有不可变版本及版本 ID。

```json
{
  "selectedSheets": ["销售明细"],
  "headerRow": 2,
  "skipEmptyRows": true,
  "columnOverrides": {
    "销售明细.订单日期": "DATE",
    "销售明细.订单金额": "DECIMAL"
  },
  "csvOptions": {
    "encoding": "GBK",
    "delimiter": "SEMICOLON",
    "quote": "'",
    "lazyQuotes": false,
    "trimLeadingSpace": true
  }
}
```

上传受租户 `max_excel_file_bytes` 限制。系统支持 `.xlsx`、`.xls` 和 `.csv`。ODS
物化时，CSV/XLS/对象读取上限取租户文件配额与
`WAREHOUSE_STAGE_MAX_BYTES` 中的较小者，XLSX 展开/worksheet 内存预算再取解析器
默认值与 staging 上限中的较小者，逻辑 staging 行也累计受同一 staging 上限约束。
任一失败都不会发布部分结果；低 staging 上限可能拒绝本体较大而选中 Sheet 较小的
工作簿。CSV 默认使用 UTF-8、
逗号分隔和双引号；`encoding` 可选 `UTF-8`、`GBK`、`GB18030`，`delimiter`
可使用 `COMMA`、`SEMICOLON`、`TAB` 或任意单字符，`quote` 可配置为任意单字符。
`lazyQuotes` 用于兼容非严格引号，`trimLeadingSpace` 用于忽略非引号字段及引号
字段前的空格。

文件接入采用两个明确步骤：

1. 页面选择文件后以不含扩展名的文件名自动填写数据源名称。数据源编码必须满足 `^[A-Za-z][A-Za-z0-9_]{0,127}$`：英文文件名优先使用可读的“规范化文件名 + 扩展名”编码；中文、其他非 ASCII、非法首字符或超长文件名使用 `file_` 加规范化原编码的 32 位小写 MD5，确保相同文件名与扩展名始终得到同一英文兼容编码。新建阶段只校验文件扩展名、大小和 CSV 方言配置，将原始文件保存到 MinIO，创建不可变文件版本，再以响应中的 `id` 作为文件数据源的 `fileAssetId`。这一步不打开工作簿、不解析 Sheet，也不创建映射表；稳定编码已存在时改走版本上传接口，覆盖该数据源的当前文件指针，不重复创建数据源。
2. 在数据表资产页点击“新增数据表”后调用 `POST /api/v1/data-sources/{id}/file-inspection`。接口读取当前文件版本，对每个 Sheet 最多读取前 10 行，确定表头行、空行策略和字段类型并返回给当前有权限的管理页面预览。确定性解析方案写入 `file_asset_inspections`，与当前文件版本一对一关联，不修改原始文件版本。用户可以选择首次或已经映射过的 Sheet；后台始终使用 Sheet 名称、字段类型和真实表头，默认不把内容行发给 LLM。只有本次任务选择且租户策略允许 `MASK/RAW` 时才重新读取最多十行，并按上文规则脱敏或发送原值。Excel/CSV 表业务名称必须为中文，字段映射业务名称必须为小写英文 `snake_case`，字段业务描述必须包含中文；原始文件表头仍保存在技术字段名称中，用于文件读取、中文展示、血缘和追溯，不会被 LLM 覆盖。

文件数据源的“数据表资产”页不提供数据库元数据的增量/全量刷新或表级结构刷新，统一显示“重新上传文件”。重新上传复用原文件资产并创建不可变新版本，同时生成新的数据源草稿；旧发布文件版本继续服务。新草稿通过测试并显式发布后，用户再通过“新增数据表”预览、选择并重新映射 Sheet。数据库数据源仍保留原有元数据刷新能力。

结构解析会自动为空标题命名、为重复标题添加序号，并拒绝损坏文件、非法 CSV 和包含缓存公式错误值的工作簿；因此这些错误在“新增数据表”的结构预览阶段返回。CSV 映射为名为 `CSV` 的单表资产。CSV 方言配置随不可变文件版本持久化，结构解析、同步、采样和查询均重放同一配置；同步始终读取当前版本，而已发布数据集应固定引用具体 `versionId`。预览样本只随当次结构解析响应返回，不写入版本摘要或解析记录。
