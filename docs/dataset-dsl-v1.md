# 数据集 DSL V1

数据集 DSL 是数据集设计态的唯一事实来源。SQL 和逻辑计划均为可重新生成的派生产物，不允许客户端把 SQL 文本作为草稿的唯一保存内容。

## 合同文件

- JSON Schema：`api/schemas/dataset-dsl-v1.schema.json`
- 完整示例：`api/examples/dataset-dsl-v1.json`
- 当前版本：`1.0`

Schema 负责结构和基础类型校验；服务端领域校验额外检查标识唯一性、节点/字段/参数引用、Join 两端、聚合阶段、输出粒度以及执行限额。服务端校验是最终安全边界。

## 规范化与兼容

保存前统一执行以下处理：

1. 清理文本首尾空白，统一枚举为大写；
2. 补齐 `visible`、超时、预览行数和结果行数默认值；
3. 将 `nil` 集合规范为 JSON 空数组；
4. 将早期 `0.9` 或早期示例中的 `dataset.grain` 迁移到 `outputGrain`；
5. 生成规范 JSON、SHA-256 `dslHash`、无方言逻辑计划和 `planHash`。

未知字段、未知版本和多余 JSON 文档一律失败关闭。相同输入经过规范化后得到相同的 DSL JSON、逻辑计划和哈希。V1 内仅做向前兼容读取；未来大版本通过显式迁移器升级，不原地改写已发布版本。

## 领域约束

- 节点类型：`TABLE`、`DATASET`；单个数据集最多 16 个节点，避免校验和跨源执行无界扇出。`DATASET` 节点必须固定已发布的 `datasetVersionId`。`SINGLE_SOURCE` 可包含同一数据源内的多张表，不能按节点数量误判为跨源。
- Join 类型：`INNER`、`LEFT`、`RIGHT`、`FULL`；必须声明基数和至少一个条件。`manualConfirmed` 会随草稿保存，修改 Join 字段、类型或基数后设计器会重新置为未确认。
- 字段角色：`DIMENSION`、`MEASURE`、`ATTRIBUTE`、`TIME`、`IDENTIFIER`。
- 参数值只能通过 `PARAM_REF` 引用；表达式不接受 SQL 片段。
- 表达式型 `sourceFilters` 必须是布尔谓词，只能引用所属节点字段且不能包含聚合；跨节点过滤在 Join 后执行。
- `outputGrain.description` 和至少一个 `keyFields` 必填，键引用字段编码。
- TABLE 节点保存时校验数据源和表资产属于当前租户；Excel/CSV 节点必须通过 `fileVersionId` 固定不可变文件版本后才可执行预览。
- 默认查询超时 5 秒、预览 500 行、正式结果 10000 行；服务端同时设定硬上限。

## 数据库存储

`platform.dataset_versions.dsl_json` 保存规范 DSL；`logical_plan_json`、`dataset_fields`、`dataset_parameters` 和 `dataset_dependencies` 都是可重建索引。所有数据集表启用并强制 PostgreSQL RLS，仓储调用必须在租户事务中执行。草稿使用 `expectedVersion` 乐观锁，避免并发覆盖。

运行时不会保存或信任客户端 SQL。MySQL/Oracle 由 T0303 安全编译器生成参数化只读查询；Excel/CSV 按固定文件版本在受限内存执行器中解释同一 DSL；跨源实时预览按节点裁剪并读取后在网关执行等值 Join。Join 前会校验声明基数和扇出风险，风险告警不包含实际业务键。各路径统一执行参数规范化、行列权限、超时、取消、结果上限和不含明文的查询审计。
