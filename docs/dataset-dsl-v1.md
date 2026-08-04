# 数据集 DSL V1

数据集 DSL 是数据集设计态的唯一事实来源。SQL 和逻辑计划均为可重新生成的派生产物，不允许客户端把 SQL 文本作为草稿的唯一保存内容。

## 合同文件

- JSON Schema：`api/schemas/dataset-dsl-v1.schema.json`
- 严格 DWS 示例：`api/examples/dataset-dsl-dws-v1.json`
- 兼容/算子回归示例：`api/examples/dataset-dsl-v1.json`
- 当前版本：`1.0`

Schema 负责结构和基础类型校验；服务端领域校验额外检查标识唯一性、节点/字段/参数引用、Join 两端、聚合阶段、输出粒度以及执行限额。服务端校验是最终安全边界。

## 规范化与兼容

保存前统一执行以下处理：

1. 清理文本首尾空白，统一枚举为大写；
2. 补齐 `visible`、超时、预览行数和结果行数默认值；
3. 将 `nil` 集合规范为 JSON 空数组；
4. 将早期 `0.9` 或早期示例中的 `dataset.grain` 迁移到 `outputGrain`；
5. 对缺少 `dataset.layer` 的历史 DSL 确定性推断层级：含分组/聚合为 `DWS`，单个物理表且无 Join 为 `ODS`，其余为 `DWD`；
6. 生成规范 JSON、SHA-256 `dslHash`、无方言逻辑计划和 `planHash`。

未知字段、未知版本和多余 JSON 文档一律失败关闭。相同输入经过规范化后得到相同的 DSL JSON、逻辑计划和哈希。V1 内仅做向前兼容读取；未来大版本通过显式迁移器升级，不原地改写已发布版本。

`dataset.domain` 与 `dataset.subject` 分别保存用户配置的业务领域和主题。两者是可选的版本化治理信息，修改后会形成新的 DSL 摘要并随草稿、发布版本和回滚快照保留；它们不改变查询逻辑，也不替代 `dataset.layer` 的单一物理落层语义。`dataset.sourceMode=PRE_AGGREGATED` 是服务端来源分类器专用标记，公开创建/编辑不能新增、删除或改变该值。

## 领域约束

- 数据层级：`ODS` 只能包含一个物理 `TABLE` 节点且不能 Join、分组或聚合；`DIM` 保留实体说明粒度并禁止业务聚合；`DWD` 保持事实明细粒度，允许字段转换和 Join，但不能包含业务分组或聚合；普通 `DWS` 必须声明输出业务粒度并至少包含一个聚合指标；`ADS` 面向消费场景组合 DWS，可投影、Join 或二次聚合，但不强制再次聚合。唯一例外是系统分类生成的 `DWS / sourceMode=PRE_AGGREGATED`：它必须是单个物理 `TABLE`、至少一个度量和明确粒度键，保留源端已有汇总行，不允许 Join、去重或再次分组聚合。
- 显式声明 `dataset.layer` 的新 DSL 执行严格层级方向：`ODS <- TABLE`、`DIM <- ODS`、`DWD <- 至少一个 ODS + 可选 DIM`、普通 `DWS <- 一个或多个 DWD/DIM`、`ADS <- DWS`。DIM/DWD/普通 DWS/ADS 的每个节点都必须固定精确发布版本的 `DATASET`，不能混用 `TABLE` 与 `DATASET`，也不能用物理表绕开治理层；受系统所有权和 DSL 双重校验的源端已汇总 DWS 使用上述窄例外。
- 缺少 `dataset.layer` 的历史 DSL 继续按确定性规则推断并保留旧 TABLE 执行兼容，不追溯改写其规范正文或哈希。该兼容只用于读取和迁移既有资产；新客户端必须显式写入层级。无论是否为兼容文档，只要使用 `DATASET` 节点，其精确版本、层级和当前 ACTIVE 物化约束都不会放宽。
- 节点类型：`TABLE`、`DATASET`；单个数据集最多 16 个节点，避免校验和跨源执行无界扇出。`DATASET` 节点必须固定已发布的 `datasetVersionId`。`SINGLE_SOURCE` 可包含同一数据源内的多张表，不能按节点数量误判为跨源。
- Join 类型：`INNER`、`LEFT`、`RIGHT`、`FULL`；必须声明基数和至少一个条件。可选的 `relationshipType` 为 `DIRECT / ROLE_PLAYING / BRIDGE`，`relationshipRole` 保存如 `ORDERING_USER / PAYING_USER / SHIPPING_REGION` 的稳定业务角色，`fanoutPolicy` 为 `SAFE / DEDUPLICATE / UNSAFE`。角色扮演和 Bridge 必须声明角色；Bridge 还必须声明扇出策略。`manualConfirmed` 会随草稿保存，修改 Join 字段、类型、基数或关系语义后设计器会重新置为未确认。
- 新的显式 DWD 不接受 `UNKNOWN` 基数。多个用户、商品、地域 DIM 应分别从事实侧声明 `MANY_TO_ONE` 或 `ONE_TO_ONE`，不构成一对多；真正的 `ONE_TO_MANY / MANY_TO_MANY` 必须标记为 `BRIDGE` 并选择 `DEDUPLICATE` 或 `UNSAFE`。`UNSAFE` 只保留关系证据，不能用于自动可加指标。分配权重、主成员和 SCD2 event-time 仍由后续结构化合同扩展，不能用自由表达式或 SQL 代替。
- 字段角色：`DIMENSION`、`MEASURE`、`ATTRIBUTE`、`TIME`、`IDENTIFIER`。
- DWD `factContract.atomicMeasures[]` 除 `additivity` 外可声明 `valueBehavior / defaultAggregation / timeAggregation`。`FLOW` 必须是 `ADDITIVE + SUM`；`CUMULATIVE` 和 `POINT_IN_TIME` 必须是 `SEMI_ADDITIVE + LAST`，表示可以在同一时点沿实体维度横向汇总、但不能跨时间求和；`NON_ADDITIVE` 使用 `NONE`。DWS 的 `analysisContract.measures[]` 继承同一时间语义，不能用 `MAX` 伪装最后时点。
- 参数值只能通过 `PARAM_REF` 引用；表达式不接受 SQL 片段。
- 表达式型 `sourceFilters` 必须是布尔谓词，只能引用所属节点字段且不能包含聚合；跨节点过滤在 Join 后执行。
- 日期格式化使用独立的 `DATE_FORMAT` 表达式并返回字符串：`YEAR` 输出 `YYYY`、`MONTH` 输出 `YYYYMM`、`QUARTER` 输出 `YYYYQn`、`DAY` 输出 `YYYYMMDD`。`DATE_TRUNC` 仍只表示日期截断，供分组粒度使用；两种语义不得混用。
- 日期计算使用 `CURRENT_DATE`、`DATE_DIFF`、`DATE_EXTRACT`、`DATE_START` 和 `DATE_END`。`CURRENT_DATE` 是无参数结构化表达式：保存和组装查询时不得替换成日期字面量，数据库执行路径分别编译为 MySQL `CURRENT_DATE()`、Oracle `TRUNC(CURRENT_DATE)`、PostgreSQL `CURRENT_DATE`。`DATE_DIFF` 的开始、结束日期都可独立选择字段或 `CURRENT_DATE`，并固定按“结束日期 - 开始日期”返回自然年/月/日差；`DATE_EXTRACT` 支持年、季度、月、ISO 周、日、星期和年内第几天；周期首末日支持周、月、季度和年，其中 ISO 周从周一开始。所有日期计算对 NULL 输入返回 NULL。
- 字段处理组件必须正式写入顶层 `transforms[]`，不能只存在于 `designer.transforms[]`。每项包含 `id / name / family / componentType / input / rules`；每条规则包含稳定输入输出身份和结构化 `expression`。例如日期计算组件保存为 `componentType: "DATE_CALCULATION"`，并在逻辑计划中生成 `TRANSFORM_DATE_CALCULATION` 步骤。
- 数值处理支持 `ROUND`、`ABS`、`FLOOR`、`CEIL` 及四则运算；条件映射支持 `CONTAINS`、`NOT_CONTAINS` 和 `IN(left, ARRAY(...))`，其中数组元素可以是安全绑定的字面量或白名单字段引用，命中/未命中分支可输出字面量或 `CURRENT_DATE`。空值填充使用 `COALESCE(输入字段, 固定值/补值字段/CURRENT_DATE)`；三种路径都保存结构化表达式，数据库与文件执行器保持一致。
- 文本表达式支持 `SUBSTRING`、`TRIM`、`UPPER`、`LOWER` 和 `REPLACE`。`SUBSTRING` 的位置从 1 开始并按 Unicode 字符计数，长度必须是非负整数；超出末尾返回剩余文本，起始位置越界返回空字符串。`REPLACE` 替换全部普通文本匹配，查找文本不能为空；这些参数均为受控字面量并在数据库查询中绑定，不拼接为 SQL。
- 窗口计算使用顶层 `WINDOW` 表达式，`function` 允许 `ROW_NUMBER / RANK / DENSE_RANK / SUM / AVG / COUNT / MIN / MAX`，并要求非空的 `partitionBy[]` 与有序 `orderBy[]`；组内聚合还必须提供 `argument`。每个排序项只接受结构化表达式及 `ASC / DESC`，数据库安全编译为 `OVER (PARTITION BY … ORDER BY …)`，文件执行器采用相同的分区、排序、并列排名和组内聚合语义；过滤、关联前分组和嵌套表达式不得包含窗口函数。
- 上述文本表达式对 `NULL` 输入统一返回 `NULL`；MySQL、Oracle、Excel/CSV 文件执行器使用同一结构化表达式，数据库分别安全编译为方言函数，文件路径按平台规范语义求值。
- 关联前 `preAggregations.groupBy/metrics` 可以通过可选 `expression` 先计算字段处理产物，再分别执行分组或聚合；`field` 是派生表对下游公开的安全别名。表达式只能引用该预聚合所属节点且必须落在节点 projection 白名单内，不能嵌套聚合或跨分支取字段。未提供 `expression` 时保持原有物理字段语义，兼容既有 DSL。
- `outputGrain.description` 必填。DIM/DWS/ADS 至少需要一个引用输出字段编码的
  `keyFields`；ODS 以及保持源行粒度、且上游明确未声明业务主键的 DWD 可以保留空数组，
  不能用首列或任意标识字段猜测唯一键。
- TABLE 节点保存时校验数据源和表资产属于当前租户；Excel/CSV 节点必须通过 `fileVersionId` 固定不可变文件版本后才可执行预览。
- 默认查询超时 5 秒、预览 500 行、正式结果 10000 行；服务端同时设定硬上限。

日期计算组件在 DSL 中的结构示例：

```json
{
  "transforms": [
    {
      "id": "transform_date_calc",
      "name": "日期计算 1",
      "family": "DATE",
      "componentType": "DATE_CALCULATION",
      "input": { "kind": "NODE", "id": "orders" },
      "rules": [
        {
          "id": "extract_year",
          "operation": "DATE_EXTRACT",
          "inputKeys": ["orders.order_date"],
          "output": {
            "id": "order_year",
            "name": "订单年份",
            "code": "order_year",
            "canonicalType": "INTEGER"
          },
          "expression": {
            "type": "DATE_EXTRACT",
            "unit": "YEAR",
            "argument": { "type": "FIELD_REF", "nodeId": "orders", "field": "order_date" }
          }
        }
      ]
    }
  ]
}
```

## 数据库存储

`platform.datasets.layer` 保存当前草稿层级摘要，`platform.dataset_versions.layer` 保存精确版本的不可变层级；历史 DSL 正文和哈希不会为回填层级而改写。`platform.dataset_versions.dsl_json` 保存规范 DSL；`logical_plan_json`、`dataset_fields`、`dataset_parameters` 和 `dataset_dependencies` 都是可重建索引。所有数据集表启用并强制 PostgreSQL RLS，仓储调用必须在租户事务中执行。草稿使用数据集 `expectedVersion`、草稿记录版本和 DSL 哈希共同识别一个确定修订，避免并发覆盖或把发布试跑结果应用到后续草稿。

当前草稿仍是唯一可变版本行。发布不会把该行原地改成 `PUBLISHED`，而是在一个租户事务中从指定且未变化的草稿复制规范 DSL、逻辑计划、字段、参数和依赖快照，形成新的不可变发布版本，再原子更新数据集的当前发布指针。草稿保留并继续编辑；后续草稿保存和再次发布都不能改写旧发布版本。

发布版本使用复合外键保证版本、数据集和租户归属一致。发布完成后，DSL、逻辑计划、哈希、版本号、来源草稿身份及字段/参数/依赖索引不能覆盖或删除，只允许 `PUBLISHED -> STALE -> DEPRECATED` 的受控生命周期迁移。固定文件版本的元数据同样禁止原地修改，防止相同 `fileVersionId` 在报告或查询运行期间改变对象、摘要或解析含义。

发布依赖保存当时观察到的物理表元数据版本与结构哈希、文件版本号与 SHA-256，以及上游数据集版本号、DSL 哈希和计划哈希。精确版本试跑会在同一个租户事务与锁定边界内确认发布版本仍为 `PUBLISHED`、依赖摘要未漂移并解析可信物理计划，事务外的安全编译与执行只使用已经解析的固定物理引用；任何失败都不能回退到当前草稿或当前发布指针。当前精确依赖快照已经按发布版本保存，但资产中心的统一下游血缘仍以数据集主对象/当前草稿为主；发布版本级全链路影响分析和依赖漂移自动 `STALE` 统一归入 T0307。

## 发布边界

发布前重新执行 DSL 规范化、派生哈希、物理资产、固定文件版本、全部启用行列策略、人工 Join 确认和最小查询试跑。跨源执行器根据受限节点输入返回基数和扇出风险；单数据源 MySQL/Oracle 为每条等值 Join 在源数据库内按键分组，只返回两侧重复键组数、最大重复度和双侧重复键组数，不返回业务键值。探测会应用节点源过滤和可证明只引用单节点的聚合前过滤；跨节点过滤尚不参与两侧基础键集合裁剪，因此结果是保守上界。非等值 Join 当前返回稳定操作符路径并失败关闭。必填且没有默认值的参数由发布请求显式提供，只参与类型校验、受控查询和不可逆摘要，不进入发布版本、幂等响应或错误正文。校验失败使用稳定 `path/code/reason` 指向 DSL 位置，并且不创建半份版本或移动发布指针。

严格 DIM/DWD/普通 DWS/ADS 的 `DATASET` 节点只按层级解析服务端可信输入。DIM/DWD 的 ODS 节点要求固定版本仍是当前 `PUBLISHED`，随后展开为该 ODS 合同中的精确物理来源；交互预览对每个来源最多采样 10 行后执行目标 DAG，正式构建则全量回源、投影 ODS 字段合同并把结果首次落入数仓。DWD 的 DIM 上游以及普通 DWS/ADS 的上游不递归重放 DAG，必须存在同一精确版本、schema hash 一致的当前 `ACTIVE` PostgreSQL 物化，执行器只读取服务端解析出的 `warehouse_published` 稳定视图。单表 ODS 与源端已汇总 DWS 发布后按其精确来源快照全量写入 `warehouse_ods` / `warehouse_dws`，源端已汇总 DWS 不再重复聚合。指针漂移、版本失效、来源摘要变化、层级不符、物化或稳定视图异常都会失败关闭，不会改读草稿或其他发布版本。

运行时不会保存或信任客户端 SQL。MySQL/Oracle 由 T0303 安全编译器生成参数化只读查询；Excel/CSV 按固定文件版本在受限执行器中解释同一 DSL；ODS 与 DIM/DWD 的来源预览按节点裁剪、每源最多读取 10 行，并在网关执行受控 DAG。DWD/DIM 正式构建的来源阶段全量抽取，目标加工以及 DWS/ADS 执行由 PostgreSQL 方言编译器从同一结构化 DSL 生成参数化查询。PostgreSQL 执行事务会再次锁定并复核精确版本、当前发布指针、ACTIVE 物化、摘要、稳定视图和读取权限。发布试跑的 Join 风险探测与最终查询共用超时、取消和审计生命周期，风险告警不包含实际业务键。各路径统一执行参数规范化、行列权限、结果上限和不含明文的查询审计；PostgreSQL 路径另保存本次实际使用的精确 materialization ID 与摘要。
