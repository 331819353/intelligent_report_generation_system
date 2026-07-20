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
  "type": "MYSQL",
  "host": "mysql.internal",
  "port": 3306,
  "database": "sales",
  "username": "report_reader",
  "password": "仅在请求中提交的数据库密码"
}
```

Oracle 将 `type` 改为 `ORACLE`，通常使用端口 `1521`，`database` 填写 Service Name 或 SID。接口不接收 JDBC 连接串；`host`、`port`、`database`、`username` 必须拆分提交。

密码仅在 HTTPS 请求处理期间短暂存在。Go 服务使用 `DATA_SOURCE_CREDENTIAL_KEY` 对完整连接凭据执行 AES-256-GCM 加密，控制库只保存不可回显的内部引用；列表、详情、审计和错误响应均不返回密码或引用。生产环境必须通过密钥系统注入独立的 32 字节 Base64 密钥，不能使用 `.env.example` 的开发默认值。

Oracle 的非敏感连接选项放在 `config` 中：

```json
{
  "oracleConnectMode": "SERVICE_NAME",
  "schemas": ["REPORT_READER"]
}
```

`oracleConnectMode` 可取 `SERVICE_NAME` 或 `SID`。Schema 名会转为大写、去重并严格校验，最多配置 20 个；实际可同步范围仍受源库账号权限约束。

更新配置后状态回到 `DRAFT`，需重新执行连接测试。编辑请求仍需提交非敏感连接字段；`password` 传空字符串表示保留已保存密码，填写新值表示轮换密码。

## 查询和状态操作

- `GET /api/v1/data-sources`：列表。
- `GET /api/v1/data-sources/{id}`：查看详情；返回 host、port、database、username，不返回密码。
- `POST /api/v1/data-sources/{id}/test`：测试连接，成功后变为 `ACTIVE`。
- `POST /api/v1/data-sources/{id}/sync`：同步元数据摘要。
- `GET /api/v1/data-sources/{id}/tables/discovery`：只读取源库中当前可见的表清单，不创建资产。
- `POST /api/v1/data-sources/{id}/tables/import`：把用户选择的表提交为后台采样与完善任务，返回 `202 Accepted`。
- `POST /api/v1/data-sources/{id}/tables/refresh`：把已纳管表提交为字段级增量或全量后台刷新任务，返回 `202 Accepted`；可传 `tableIds` 仅刷新指定活动表，省略时刷新全部已纳管表。
- `GET /api/v1/data-sources/{id}/metadata-jobs/{jobId}`：查询后台任务真实进度。
- `GET /api/v1/data-sources/{id}/metadata-jobs/latest-active`：恢复该数据源最近的活动任务；无任务时返回 `{"job":null}`。
- `POST /api/v1/data-sources/{id}/enable`：恢复已暂停数据源。
- `POST /api/v1/data-sources/{id}/disable`：暂停运行中数据源。
- `DELETE /api/v1/data-sources/{id}`：逻辑删除。

状态流转为 `DRAFT →（连接测试成功）→ ACTIVE → SYNCING → ACTIVE`；失败进入 `ERROR` 后必须重新测试，不能直接启用或同步。只有已验证后被停用的 `DISABLED` 数据源可以直接重新启用。删除经过 `DELETING → DELETED`。

同步会保存规范化的表与字段资产，同时保存完整 JSON 快照和 SHA-256 结构哈希。表、字段、约束或索引发生变化时记录 `ADDED`、`CHANGED`、`REMOVED` 差异；源库中消失的表和字段保留历史记录并标记为 `INACTIVE`，不做物理删除。

配置中心的“新增数据表”采用两阶段流程：每次打开弹窗都先实时调用 discovery 接口刷新源库表清单，再由用户全选或选择一部分表。import 请求示例：

```json
{
  "tables": [
    {"catalogName": "sales", "schemaName": "sales", "tableName": "orders"}
  ]
}
```

接口只持久化批任务和选中的表键，立即返回任务摘要及 `Location`；worker 在请求之外采集技术结构和最多三行样本，调用已配置的 LLM 完善业务元数据，并将最终表资产保存到 PostgreSQL。样本行只在 worker 内存和本次模型请求中短暂存在，不写入任务表或元数据资产表。刷新单表结构复用同一后台流程；配置中心只展示当前技术结构已经完成元数据完善的活动资产，旧结构的成功记录不会让未完善的新结构提前进入清单。整表映射完成时会在同一租户事务内确保其默认单表数据集存在，拓扑仅为“数据节点 → 结束节点”；数据集创建失败会连同本次映射完成标记一起回滚，避免产生不可预览的半成品。

刷新请求体为 `{"mode":"INCREMENTAL"}` 或 `{"mode":"FULL"}`，缺省语义为增量。两种模式都只处理 PostgreSQL 中已纳管且未删除的表，不会自动导入源库中的其他表，也不会复活用户已经删除的资产。

`INCREMENTAL` 是字段级增量刷新。worker 先同步当前技术结构，再逐字段比较已落库的结构版本：仅新增字段、结构发生变化的字段，以及此前尚未成功完善的字段会进入采样和 LLM 请求；未变化字段不采样、不调用 LLM，也不会被本次模型结果覆盖，其业务名称、说明、标签、语义类型、敏感级别、人工锁定状态和版本保持原值。只有表级技术信息本身发生变化时，才重新完善表级业务信息。字段从源表中消失时保留历史记录并标记为 `INACTIVE`，不调用 LLM；只有 Connector 返回表数量一致、时间水位和快照哈希有效且业务键无重复的权威完整快照时，源表缺失才会停用 PostgreSQL 中对应的表资产及其字段；更晚的同结构同步会阻止旧任务停用资产。所有删除均不物理清除历史数据。

`FULL` 会重新采样并对目标范围内的全部活动表和活动字段执行完整 LLM 完善，不使用字段级未变化跳过规则。无论哪种模式，任务都会把模型结果合并到本次明确处理的目标范围，不能用局部响应覆盖其他已落库字段。

任务状态可取 `QUEUED`、`RUNNING`、`SUCCEEDED`、`PARTIAL` 或 `FAILED`。查询响应返回 `total`、`completed`、`succeeded`、`skipped`、`failed`、`stage` 和 `currentTable`；存在失败项时还会返回可选的 `failures`，逐项包含库/Schema、表名、稳定错误码和安全文案。`completed` 是成功、跳过和失败之和，页面进度条使用 `completed / total`，不按时间伪造进度。单表失败不会阻断后续表，响应和审计不包含样本数据、连接器原始错误或模型正文。worker 使用租户 RLS 事务、数据库租约和独立心跳领取任务；AI 成功与任务项及结构哈希绑定，租约恢复可直接收口已提交结果，API 或页面关闭不会中止任务。

数据源的修改、测试、暂停/恢复和删除操作管理连接本身；表资产的修改、字段映射、刷新、停用/恢复和删除操作管理 PostgreSQL 中的资产记录，两组生命周期相互独立。字段映射只允许修改业务名称、说明、标签、语义类型、敏感级别和人工锁定，不改写源库物理字段名或技术类型。

Python Connector 按数据源维护有界连接池，并同时执行每租户查询并发上限和服务进程全局上限。Go 核心从租户配额表下发限制，但不会向 Python 服务日志或响应传递明文凭证。

连接池支持空闲 TTL 淘汰，更新或删除数据源时由 Go 调用内部关闭接口释放旧连接。查询请求可携带唯一 `queryId`，执行器可调用 `/v1/query/cancel`：Oracle 使用驱动取消，MySQL 关闭正在执行的连接。只读查询在执行前进行失败关闭的词法检查，拒绝 CTE-DML、DDL、事务、锁、文件导出、延时函数、注释和多语句；源数据库账号仍必须只授予只读权限，不能依赖应用校验代替数据库授权。

数据源创建、更新、测试、同步、暂停、恢复和删除均记录审计摘要；审计内容不包含连接配置、密码或内部凭证引用。

## Excel 文件版本

使用 `multipart/form-data` 上传，文件字段名为 `file`，可选的 `config` 字段是 JSON：

- `POST /api/v1/excel-files`：创建文件资产和版本 1。
- `POST /api/v1/excel-files/{id}/versions`：重传并创建新版本。
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

上传受租户 `max_excel_file_bytes` 限制。系统支持 `.xlsx`、`.xls` 和 `.csv`。CSV 默认使用 UTF-8、逗号分隔和双引号；`encoding` 可选 `UTF-8`、`GBK`、`GB18030`，`delimiter` 可使用 `COMMA`、`SEMICOLON`、`TAB` 或任意单字符，`quote` 可配置为任意单字符。`lazyQuotes` 用于兼容非严格引号，`trimLeadingSpace` 用于忽略非引号字段及引号字段前的空格。

系统自动为空标题命名、为重复标题添加序号，并拒绝损坏文件、超限文件、非法 CSV 和包含缓存公式错误值的工作簿。CSV 映射为名为 `CSV` 的单表资产。CSV 方言配置会随文件版本持久化，连接测试和同步均使用该版本的配置。上传成功后，以响应中的 `id` 作为文件数据源的 `fileAssetId`；同步始终读取当前版本，而已发布数据集应固定引用具体 `versionId`。
