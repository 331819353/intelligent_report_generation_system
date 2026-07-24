# 指标定义 v2：结构化口径与修饰词设计

> **状态：DESIGN ONLY / NOT YET ACCEPTED BY RUNTIME**
>
> 本文是待评审的目标合同，不是已上线能力。当前 API、Go 解码器、JSON Schema、
> PostgreSQL 约束、查询运行时和前端编辑器只接受
> [`metric-definition-v1`](./metric-definition-v1.md)。在本文列出的迁移、实现、测试和
> 灰度门全部完成前，客户端不得发送 `schemaVersion: "2.0"`，服务端也不得把本文
> 示例解释为可执行定义。

## 1. 决策摘要

v2 的第一目标是在同一个精确 DWS 数据集版本上，为不同指标声明各自固定、可审计、
可重放的业务范围。例如“销售额”和“已支付销售额”可以复用同一 DWS，但后者能够把
“支付状态属于 PAID”保存为结构化修饰词，而不是把说明文字解释成 SQL，也不必为每个
指标复制一份数据集。

第一可执行切片刻意收窄为：

- 只读取当前发布指针所对应的精确 `ACTIVE DWS` PostgreSQL 物化；
- 只支持 `DERIVED + aggregation=NONE`、一个直接 `FIELD_REF`、不含
  `METRIC_REF` 的字段型指标；
- 度量字段必须是 DWS 的 `MEASURE`，原始表达式只能是可安全再聚合的顶层
  `SUM / MIN / MAX / COUNT`；
- 原始 `SUM / COUNT` 只允许声明为 `ADDITIVE`，原始 `MIN / MAX` 只允许声明为
  `NON_ADDITIVE`；第一切片拒绝 `SEMI_ADDITIVE`；
- 修饰词只引用该精确 DWS 输出中属于原始 `groupBy` 粒度的可见非度量字段；
- 修饰字段必须具有绑定当前 ACTIVE 物化的成功、低基数 `FULL` 画像；修饰字段和允许
  维度都采用历史敏感性下限，不能因标签或维度后来被放宽而重新启用；
- 多个修饰词固定使用 `AND`；
- 所有值都经过规范类型校验并作为数据库绑定参数执行；
- 首批只开放 `DATE` 时间字段和修饰值；当前无时区 `timestamp` 仓储不能证明
  `DATETIME` 瞬时语义，因此必须返回稳定错误而不是按会话时区猜测；
- v2 查询响应携带规范列类型，`INTEGER / DECIMAL` 以字符串传输，不能把精确数值交给
  JSON number 或 JavaScript `number` 隐式舍入；
- 派生指标、比率指标及“依赖指标各自具有不同修饰范围”的场景失败关闭。

这不是最终表达能力。后续只有在 `ScopedAggregate` 逻辑 IR 能保持每个原子分支的
范围、粒度和单位时，才可以开放带修饰词的派生指标。

## 2. 目标

v2 需要把以下事实纳入一个不可变指标版本：

- 业务口径：统计对象和面向业务人员的正式定义；
- 计算事实：字段表达式、业务聚合方式、空值和除零规则；
- 原子构件：直接度量字段或递归解析得到的精确指标版本依赖；
- 维度：可分组字段，以及可选的一级语义维度绑定；
- 修饰词：字段、操作符、规范类型值和可选一级语义维度绑定；
- 单位：稳定的单位编码和展示符号；
- 时间：业务时间字段、默认时间粒度和明确时区；
- 血缘：精确数据集版本、度量字段、修饰字段、时间字段、维度字段和指标依赖；
- 执行证据：定义摘要、物化 ID、schema/snapshot hash 和实际查询计划摘要。

所有可执行事实必须来自严格 JSON、已发布控制面资产和服务端编译器。自由文本只用于
解释和检索，不能改变字段、过滤、聚合、Join、数据源或物化。

## 3. 非目标

第一可执行切片不包含：

- 客户端 SQL、SQL 片段、函数名、物理表名、物理列名或稳定视图名；
- 客户端提交任意数据集 Filter DSL、Join 或执行计划；
- 运行时覆盖、放宽或临时追加指标修饰词；
- 跨字段 `OR`、嵌套布尔组、字段对字段比较或运行时参数修饰词；
- 非 DWS、非 PostgreSQL 或缺少精确 ACTIVE 物化的 v2 指标；
- 带 `METRIC_REF` 的 v2 指标执行；
- 单位换算、汇率换算、复合单位推导或跨单位加减；
- 自动把 v1 定义重写为 v2；
- 把 LLM 生成的口径文字、标签或血缘摘要作为执行事实；
- 从敏感维度枚举成员或在指标定义中保存敏感成员值；
- 仅凭本文即启用 v2 创建、保存、试算或发布。

## 4. 与 v1 的兼容关系

v1 与 v2 必须并存，而不是原地改变 `1.0` 的含义。

- 已有 v1 规范 JSON、`definitionHash`、发布版本和查询结果必须保持不变。
- v1 继续由现有 Schema 和校验器严格解码；v1 出现任何 v2 字段仍应因未知字段失败。
- v2 使用独立 JSON Schema。解码器先读取并校验唯一的 `schemaVersion`，随后分派到
  对应的严格结构，不能先用宽松联合结构吞掉未知字段。
- 指标草稿可以继续保存、校验和发布 v1。系统不得在读取、更新或发布时自动升级。
- 若以后提供显式升级操作，升级只能创建新的草稿修订，保留旧发布版本及其摘要。
- v2 在第一切片中保留 v1 根字段形状，新增 `businessDefinition`、`modifiers`、
  `unitCode`、`timeZone`、`emptySetHandling` 和一级维度快照字段，以降低存储和
  API 改造范围；这不表示 v1 客户端能够读取或修改 v2 草稿。
- v1 使用 `DERIVED + NONE` 表示数据集 DAG 已经聚合的 DWS 输出。MVP 沿用这个执行
  分类；“业务原子构件”由“无 `METRIC_REF` 且只有一个直接度量字段”判定，不把
  `metric.type` 的历史命名误当成是否存在上游指标依赖。

## 5. 完整定义示例

以下是设计目标的完整示例，不是当前 API 可接受请求：

```json
{
  "schemaVersion": "2.0",
  "metric": {
    "code": "monthly_paid_sales_amount",
    "name": "月度已支付销售额",
    "description": "按支付月份统计支付成功订单的含税销售金额。",
    "type": "DERIVED"
  },
  "businessDefinition": {
    "statisticalObject": "支付成功订单",
    "statement": "汇总支付状态为 PAID、销售渠道属于直营网或经销渠道的订单含税支付金额。"
  },
  "datasetId": "11111111-1111-4111-8111-111111111111",
  "datasetVersionId": "22222222-2222-4222-8222-222222222222",
  "expression": {
    "type": "FIELD_REF",
    "fieldId": "paid_amount_sum"
  },
  "aggregation": "NONE",
  "modifiers": [
    {
      "id": "payment_status_paid",
      "name": "支付成功",
      "fieldId": "payment_status",
      "semanticDimensionId": "33333333-3333-4333-8333-333333333333",
      "semanticDimensionVersion": 4,
      "semanticDimensionDefinitionHash": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
      "operator": "IN",
      "values": [
        {
          "type": "STRING",
          "value": "PAID"
        }
      ]
    },
    {
      "id": "sales_channel_scope",
      "name": "有效销售渠道",
      "fieldId": "sales_channel",
      "semanticDimensionId": "44444444-4444-4444-8444-444444444444",
      "semanticDimensionVersion": 2,
      "semanticDimensionDefinitionHash": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
      "operator": "IN",
      "values": [
        {
          "type": "STRING",
          "value": "DEALER"
        },
        {
          "type": "STRING",
          "value": "DIRECT"
        }
      ]
    }
  ],
  "unit": "元",
  "unitCode": "CNY",
  "numberFormat": "#,##0.00",
  "timeFieldId": "paid_date",
  "timeGrain": "MONTH",
  "timeZone": "",
  "additivity": "ADDITIVE",
  "nonAdditiveDimensionFieldIds": [],
  "emptySetHandling": "ZERO",
  "allowedDimensions": [
    {
      "fieldId": "region",
      "semanticDimensionId": "55555555-5555-4555-8555-555555555555",
      "semanticDimensionVersion": 7,
      "semanticDimensionDefinitionHash": "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
      "name": "区域",
      "hierarchyFieldIds": [
        "region"
      ],
      "sortDirection": "ASC",
      "nullLabel": "未归属区域"
    },
    {
      "fieldId": "paid_date",
      "semanticDimensionId": "66666666-6666-4666-8666-666666666666",
      "semanticDimensionVersion": 1,
      "semanticDimensionDefinitionHash": "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd",
      "name": "支付时间",
      "hierarchyFieldIds": [
        "paid_date"
      ],
      "sortDirection": "ASC",
      "nullLabel": "未知时间"
    }
  ],
  "decimalScale": 2,
  "roundingMode": "HALF_UP",
  "nullHandling": "IGNORE",
  "divisionByZero": "NULL"
}
```

示例中 modifier 的 `values` 已按规范值排序；这些值是当前成员代中的原始 typed
`member_key`，不是成员展示名或 alias。

## 6. v2 严格字段合同

除本节新增字段外，v1 的 `metric`、`datasetId`、`datasetVersionId`、`expression`、
`aggregation`、`numberFormat`、`timeFieldId`、`timeGrain`、`additivity`、
`nonAdditiveDimensionFieldIds`、`allowedDimensions`、`decimalScale`、
`roundingMode`、`nullHandling` 和 `divisionByZero` 约束继续适用。

v2 新增且必填的根字段是：

| 字段 | 规范形状 |
| --- | --- |
| `businessDefinition` | 对象；必须包含 6.1 的两个必填字符串 |
| `modifiers` | 数组；没有修饰词时也必须规范化为 `[]`，不能省略或使用 `null` |
| `unitCode` | 6.2 定义的稳定单位编码 |
| `timeZone` | 字符串；第一切片必须为空字符串，但字段本身不能省略 |
| `emptySetHandling` | `NULL` 或 `ZERO` |

一级维度 ID/version/hash 不是根字段；它们按 6.3 在每个维度或修饰词内成组三项条件
必填。以上新增字段全部进入规范 JSON 和 `definitionHash`。本节的 DWS 形状、修饰字段
粒度、治理证据和数值收口都是发布硬门，不是 UI 提示。

### 6.1 `businessDefinition`

| 字段 | 约束 | 执行语义 |
| --- | --- | --- |
| `statisticalObject` | 必填，1–200 个 Unicode 字符，无控制字符 | 只用于口径展示、审核和检索 |
| `statement` | 必填，1–2000 个 Unicode 字符，无控制字符 | 正式业务说明，但不解析为 SQL 或 Filter |

服务端必须另外生成一个不可编辑的“系统口径摘要”，按确定性顺序展示度量字段、聚合、
修饰词、时间、单位、空值和除零规则。发布确认页同时显示业务说明和系统摘要。两者
明显冲突时由用户修改定义；LLM 可以提示冲突，但不能替用户修改可执行事实。

### 6.2 `unit` 与 `unitCode`

- `unit` 保留 v1 的展示用途，最长 32 个字符。
- `unitCode` 必填，匹配 `^[A-Z][A-Z0-9_]{0,31}$`。
- 无量纲指标使用 `unitCode: "NONE"` 且 `unit` 为空。
- 计数指标可以使用业务单位编码，例如 `ORDER`、`CUSTOMER`，展示符号可以是“笔”或
  “人”；不能把聚合函数 `COUNT` 当作唯一业务单位。
- MVP 不进行单位换算。服务端和 UI 不得因为两个指标的展示符号相同就推断它们可相加。
- 后续派生指标只有在单位编码和粒度规则兼容时才可执行；复合单位必须由未来合同显式
  定义，不能拼接自由文本。

### 6.3 一级维度快照引用

`allowedDimensions` 可以省略一级维度引用；`IS_NULL / IS_NOT_NULL` 这类不携带成员值
的 modifier 也可以省略。任何携带 `values` 的 modifier 必须绑定一级维度，以便把 UI
中的成员或别名解析成当前成员代的原始 typed `member_key`，而不是把展示文字写入执行
合同。绑定时必须同时提供 `semanticDimensionId`、正整数
`semanticDimensionVersion` 和 64 位小写十六进制
`semanticDimensionDefinitionHash`，并满足：

- UUID 规范；
- 对应一级维度为 `PUBLISHED`；
- 一级维度固定到相同 `datasetVersionId + fieldId`；
- 指标版本内的 `fieldId` 仍是执行白名单，一级维度 ID 不能替代它；
- 发布事务为相同 DWS、相同字段的绑定生成固定到新指标版本的系统
  `DIRECT + SAFE + VERIFIED` 兼容证据；客户端不能提交、伪造或把人工
  `PROPOSED` 关系升级为该证据；
- 同一发布事务在把指标版本切为 `PUBLISHED` 前再次验证该兼容证据；执行时还要复核
  关系仍为 `VERIFIED`、`DIRECT` 且 `SAFE`，任何缺失、拒绝、停用或变为 `UNSAFE`
  都失败关闭；
- 敏感性和成员索引策略以执行时重新读取的治理事实为准，不能相信定义中的缓存。

版本和摘要冻结的是指标发布时看见的语义证据。一级维度后续改名不会改写历史指标
定义；界面可以同时展示历史快照和当前名称。当前治理事实若变得更严格，例如新增
敏感标签、维度停用或兼容关系变为不安全，执行必须失败关闭；不能因为历史快照较宽
而继续运行。

### 6.4 时间语义与 `timeZone`

- `timeFieldId` 为空时，`timeGrain` 必须为 `NONE`，`timeZone` 必须为空。
- `timeFieldId` 指向 `DATE` 时，`timeZone` 必须为空；业务日期不做时区换算。
- PostgreSQL 对 DATE 做粒度截断时必须生成等价于
  `DATE_TRUNC(<grain>, <date_value>)::date` 的 v2 专属表达式，不能把
  `DATE_TRUNC` 返回的 `timestamp` 当成 DATE。`WEEK` 固定为 ISO 周、周一为起始日；
  DAY/MONTH/QUARTER/YEAR 均返回对应周期首日的 `date`。
- DATE 响应值固定编码为 `YYYY-MM-DD`。同一查询在不同 PostgreSQL session
  `TimeZone` 下必须得到逐字节一致的日期；该改造不能静默改变 v1 的既有查询计划或结果。
- 第一可执行切片只允许 `DATE`。`DATETIME` 时间字段、DATETIME 修饰值或对 DATETIME
  做范围比较必须返回 `METRIC_DATETIME_SEMANTICS_UNAVAILABLE`。
- 原因是当前数据面把 DATETIME 落为 PostgreSQL `timestamp`，无法从列本身证明它是
  UTC 瞬时还是某个地区的墙钟时间；文件导入还可能在解析时丢失原始偏移。指标层
  不得用连接会话时区补猜。
- 后续开放 DATETIME 前，数据集字段必须增加不可变的时间来源合同，至少区分
  `UTC_INSTANT` 与 `LOCAL_WALL_TIME + IANA zone`；仓储采用 `timestamptz`，或在受控
  IR 中显式生成双向 `AT TIME ZONE`。定义/发布冻结 tzdb 规则版本，查询审计记录实际
  版本；版本不兼容时失败，不能在相同 definition hash 下静默改变边界。

### 6.5 DWS 形状、空集合与数值收口

第一切片在 Schema、服务校验和数据库投影中同时强制：

- `metric.type` 必须为 `DERIVED`，`aggregation` 必须为 `NONE`；
- `expression` 必须是唯一直接 `FIELD_REF`，且定义和依赖图中都没有
  `METRIC_REF`；
- 目标字段必须是当前精确 DWS 的 `MEASURE`，其原始表达式必须是顶层
  `AGGREGATE(SUM|MIN|MAX|COUNT)`；计算度量、`AVG`、`COUNT_DISTINCT` 和嵌套聚合
  失败关闭；
- 原始 `SUM / COUNT` 的 roll-up 固定为 `SUM`，且 `additivity` 必须为
  `ADDITIVE`；原始 `MIN / MAX` 的 roll-up 保持同名，且 `additivity` 必须为
  `NON_ADDITIVE`；
- 第一切片不执行半可加快照、期末值或 last-value 语义，因此
  `additivity: "SEMI_ADDITIVE"` 一律返回 `METRIC_V2_ADDITIVITY_INCOMPATIBLE`；
  `nonAdditiveDimensionFieldIds` 按 v1 规则必须为空；
- DWS 不能携带运行参数；修饰字段必须是原始 DWS `groupBy` 中的输出字段，不能用
  已在 DWS 中被聚掉的明细属性重建来源范围。

`emptySetHandling` 必填，只允许 `NULL` 或 `ZERO`。无请求维度时，查询固定返回一行：
`NULL` 保持聚合空集合为 NULL；`ZERO` 使用类型化零值，且只允许用于原始
`SUM / COUNT`。`ZERO` 只在 modifier 过滤后输入行数确实为零时生效；如果至少存在一行
但 SUM 的度量值全部为 NULL，`nullHandling: "IGNORE"` 的结果仍为 NULL。实现必须
携带行存在性证据，例如使用同一聚合中的 `COUNT(*)`，不能用
`COALESCE(SUM(measure), 0)` 混淆“无行”和“全 NULL”。请求一个或多个维度时，没有
匹配组就返回零行，不能凭空合成维度笛卡尔积；已有分组中度量全 NULL 仍返回 NULL。

v2 把 `decimalScale + HALF_UP` 从 v1 的“已保存声明”升级为执行合同：在 PostgreSQL
roll-up 和无维度空集合处理之后，对 DECIMAL 结果执行等价于
`ROUND(value::numeric, decimalScale)::numeric(38, decimalScale)` 的服务端表达式。
半值对正负数都远离零；超出 38 位精度返回 `METRIC_NUMERIC_OVERFLOW`。在这一步
实现和基线测试完成前，v2 功能开关不得开启；v1 的既有精度边界不因此改变。

### 6.6 查询响应 wire codec

数据库内得到精确结果不等于浏览器已经得到精确结果。v2 查询响应在保留现有
`columns` 顺序的同时，必须增加等长、同序的 `columnTypes`，每项是服务端复核后的
`STRING / INTEGER / DECIMAL / BOOLEAN / DATE` 规范类型；行值按下列规则编码：

- `INTEGER` 使用规范十进制 JSON 字符串，包括超出 JavaScript 安全整数范围的值；
- `DECIMAL` 使用非指数 JSON 字符串；指标结果固定保留 `decimalScale` 位，负零规范为
  对应 scale 的正零；
- `DATE` 使用 `YYYY-MM-DD`，`BOOLEAN` 使用 JSON boolean，STRING 使用 JSON string，
  数据库 NULL 使用 JSON null；
- 服务端不能把 pgx 原生值或无类型 `[][]any` 直接交给 JSON 编码器，也不能让前端先
  转成 JavaScript `number` 再格式化。

响应类型映射、数值文本和日期文本都属于 v2 合同测试与 query audit 的一部分。v1
响应保持原合同；v2-aware 客户端必须按 `columnTypes` 解码。

## 7. 修饰词合同

### 7.1 结构和上限

每个修饰词只能包含：

| 字段 | 约束 |
| --- | --- |
| `id` | 必填，匹配 `^[A-Za-z][A-Za-z0-9_]{0,63}$`，定义内唯一 |
| `name` | 必填，1–200 字符，无控制字符，只用于展示 |
| `fieldId` | 必填，精确 DWS 版本中的逻辑字段 ID |
| `semanticDimensionId` | 可选，必须与同一精确版本和字段匹配 |
| `semanticDimensionVersion` | 绑定一级维度时必填，必须等于发布时读取的正整数版本 |
| `semanticDimensionDefinitionHash` | 绑定一级维度时必填，冻结发布时语义摘要 |
| `operator` | 必须来自下表白名单 |
| `values` | 非空与数量由操作符决定，元素必须是规范类型值 |

单个定义最多 16 个修饰词，全部修饰值合计最多 64 个；规范 JSON 总体仍受 1 MiB
定义大小和 2 MiB 请求体限制。`id` 在规范化后按字典序排序；`IN/NOT_IN` 的值按
类型规范化、去重并确定性排序，使语义等价输入得到稳定摘要。修饰词之间固定为
`AND`，不接受客户端提交布尔连接符。

### 7.2 操作符与参数个数

| 操作符 | 值数量 | 允许字段类型 | 说明 |
| --- | ---: | --- | --- |
| `EQ` | 1 | 除复杂类型外的匹配标量 | 等于 |
| `NEQ` | 1 | 除复杂类型外的匹配标量 | 不等于；NULL 不匹配 |
| `IN` | 1–64 | `STRING`、`INTEGER`、`DECIMAL`、`BOOLEAN`、`DATE`、`DATETIME` | 属于集合 |
| `NOT_IN` | 1–64 | 同 `IN` | 不属于集合；NULL 不匹配 |
| `GT` | 1 | `INTEGER`、`DECIMAL`、`DATE`、`DATETIME` | 大于 |
| `GTE` | 1 | `INTEGER`、`DECIMAL`、`DATE`、`DATETIME` | 大于等于 |
| `LT` | 1 | `INTEGER`、`DECIMAL`、`DATE`、`DATETIME` | 小于 |
| `LTE` | 1 | `INTEGER`、`DECIMAL`、`DATE`、`DATETIME` | 小于等于 |
| `BETWEEN` | 2 | `INTEGER`、`DECIMAL`、`DATE`、`DATETIME` | 闭区间；下界必须小于等于上界 |
| `IS_NULL` | 0 | 任意允许修饰字段 | 只匹配 NULL |
| `IS_NOT_NULL` | 0 | 任意允许修饰字段 | 只匹配非 NULL |

不接受 `LIKE`、正则表达式、任意函数、任意字符串表达式、`PARAM_REF`、字段对字段比较
或用户提供的操作符映射。将来如需模糊匹配，必须先定义跨排序规则的一致语义和资源
上限，不能复用数据库默认行为。

表中的 DATETIME 是 v2 目标合同的保留类型，不代表第一切片已启用。满足 6.4 的时间
来源合同、仓储和编译器改造前，任何 DATETIME 操作符组合都返回稳定错误。

### 7.3 规范值类型

每个 `values` 元素都必须是 `{ "type": ..., "value": ... }`，且 `type` 与目标字段
的 `canonicalType` 一致：

| `type` | `value` 形状 | 规范化 |
| --- | --- | --- |
| `STRING` | JSON 字符串，最长 1024 字符，无控制字符 | 保留有效 Unicode；不做大小写或全半角猜测 |
| `INTEGER` | 十进制字符串，范围 `-9223372036854775808` 到 `9223372036854775807` | 禁止前导 `+`、指数、非规范前导零和 `-0` |
| `DECIMAL` | 十进制字符串，总数字不超过 38 位 | 禁止指数；去除无意义前导/尾随零；任何正负零统一为 `0` |
| `BOOLEAN` | JSON boolean | 只接受 `true` 或 `false` |
| `DATE` | `YYYY-MM-DD` 字符串 | 必须是有效公历日期 |
| `DATETIME` | RFC 3339 字符串，必须带 `Z` 或显式偏移 | 规范为 UTC 瞬时值后绑定执行 |

服务端不能根据文本外观做跨类型强制转换。例如字符串 `"001"` 不能自动转换为整数
`1`，日期字符串也不能由数据库会话隐式转换。

编译器不能把 typed value 降级成无类型 `any`。PostgreSQL 参数必须使用匹配的 OID
或显式 `::bigint / ::numeric / ::boolean / ::date` cast；DATETIME 开放后使用
`timestamptz`。modifier DECIMAL 先在服务端以任意精度十进制解析，并把不超过 38 位
的规范文本绑定为 PostgreSQL `numeric`；这里不能使用指标 `decimalScale`，也不能用
未在字段合同中声明的 scale 对过滤值先行舍入。只有 6.5 的最终指标结果才收口为
`numeric(38, decimalScale)`。STRING 比较在字段和参数两侧显式采用确定性的
`COLLATE "C"`，避免数据库默认或非确定 ICU collation 改变相等语义。参数值不进入
SQL 文本、日志或错误消息。

成员别名只用于 UI 查找，不属于执行合同。编辑器选择成员名或“690”一类别名时，服务端
必须在当前 `member_refresh_generation` 中解析到 `ACTIVE` 成员，把该成员的原始
`member_key` 按字段 `canonicalType` 转为上述 typed value，并只把这个规范 typed
`member_key` 写入 `values`。alias、`canonicalLabel` 和 `normalizedValue` 均不得写入
定义，也不能在执行时重新解释。

保存、发布和每次执行都要验证每个非空 typed `member_key` 仍属于该维度的当前活动成员
代：成员必须为 `ACTIVE`，其 `refresh_generation` 与维度当前 generation 一致，且来源
刷新任务仍是当前成功任务。成员消失、换代、维度收紧或 alias 改绑后返回
`METRIC_MODIFIER_MEMBER_STALE`，不能继续使用旧成员、按 alias 猜值或静默改写
`definitionHash`。`IS_NULL / IS_NOT_NULL` 没有成员值，不执行 member_key 存在性检查。

### 7.4 NULL 语义

除专门判断空值的 `IS_NULL / IS_NOT_NULL` 外，比较和集合操作产生的 SQL 三值逻辑
UNKNOWN 一律视为“不匹配”：

- `NEQ` 和 `NOT_IN` 不会自动包含 NULL；
- “非取消且允许未知状态”需要未来显式 `OR` 能力，MVP 不得把它近似为 `NEQ`；
- `IS_NULL` 与 `IS_NOT_NULL` 是唯一能够声明空值范围的操作符；
- 聚合继续遵守 `nullHandling: "IGNORE"`；修饰词筛选和度量空值处理是两个独立阶段。

### 7.5 敏感性与高基数

MVP 对敏感修饰字段和敏感允许维度完全失败关闭，包括
`IS_NULL / IS_NOT_NULL`：

- 敏感性采用不可放宽的历史下限，而不是只看当前布尔值：历史上已批准的
  `SENSITIVITY` 字段绑定、未废弃的敏感维度、敏感勘测候选/建议，或历史
  `SENSITIVE_FIELD_PROFILE_SKIPPED` 画像任一存在，字段即按敏感处理；
- 敏感字段既不能作为 modifier，也不能进入 `allowedDimensions`；否则分组查询会直接
  返回成员值。第一切片不以“只返回聚合值”或当前成员索引关闭作为例外；
- 运行时在保存、校验、试算、发布和每次执行时重新读取治理事实，不能只依赖草稿创建
  时的状态；
- 后续敏感标签生效时，既有草稿必须变为不可发布；已发布版本的处置需要单独的
  `STALE` 传播设计，但传播完成前执行事务已经必须失败关闭，不能静默改写定义；
- LLM 永远不能接收或生成敏感成员值。

高基数但非敏感字段不提供成员枚举。第一可执行切片也不允许人工绕过索引输入高基数
值：该字段不能作为 modifier，并返回
`METRIC_MODIFIER_HIGH_CARDINALITY_FORBIDDEN`。这避免在尚未定义审核对象、权限、
值摘要、失效条件和重审流程时，用一个“已告警”布尔值假装完成治理。未来若引入精确
值审批，批准必须绑定草稿 record version、definition hash、字段画像 evidence hash
和审核人；任一内容或治理状态变化都使批准失效。AI 在任何阶段都不得猜测高基数值。

### 7.6 治理证据与并发锁序

任何依赖语义治理事实的 options、保存、校验、试算、发布和执行操作，都必须在同一个
租户事务中先取得
`semantic-governance-write:<tenantId>` advisory key 的**共享事务锁**，并持有到读取
或查询完成。语义标签、画像、成员、维度、兼容关系和 ACTIVE 物化的所有写事务继续先
取得同一 key 的**排他事务锁**；共享读取不得另造一把不与现有写门冲突的锁。

取得租户共享门后的唯一锁序是：

1. 当前精确 `ACTIVE DWS` materialization 行；
2. 当前发布数据集/版本行，然后取得
   `dimension-dataset-profile:<tenantId>:<datasetId>` 数据集 advisory lock；
3. 按 `datasetVersionId + fieldId` 字典序取得所有
   `dimension-field-risk:<tenantId>:<datasetVersionId>:<fieldId>` advisory lock；
4. 当前 profile、semantic dimension、当前 generation member、alias 解析结果和
   dimension—metric compatibility 行。

服务端不能先锁 profile/dimension/compatibility 行再回头取得字段锁。直接 SQL 写入、
API 写入、worker 完成和指标执行必须遵守同一个门和锁序，以避免安全检查与敏感标签
批准、物化切换或成员换代并发穿越。

每个 modifier 字段必须有一条与当前 materialization ID、schema hash、snapshot hash、
field ID、`dws-dimension-profile-v1` 和 `dimension-member-policy-v1` 精确匹配的当前
画像。画像只有在 `status=SUCCEEDED`、`risk_high_cardinality=false` 且
`recommended_member_index_policy=FULL` 时可用；`QUEUED / RUNNING / FAILED / STALE /
SKIPPED_POLICY` 或证据缺失一律返回 `METRIC_MODIFIER_PROFILE_UNAVAILABLE`。不得把
数据库默认 `false`、旧物化画像或人工勾选当作低基数证据。

持有全部锁后，发布事务才为绑定的一级维度和新指标版本生成 6.3 的系统
`DIRECT + SAFE + VERIFIED` 兼容证据并完成发布；执行事务则重新验证历史敏感性下限、
当前画像、维度状态/版本、当前成员代和兼容关系。任一事实变化都失败关闭。查询审计
除定义和物化摘要外，还要冻结实际采用的 profile ID/evidence hash、dimension
version/hash、member refresh generation 和 compatibility ID/version。

## 8. MVP 服务端校验与执行顺序

执行顺序必须固定，任一步失败都不降级到 v1、其他版本、其他物化或自由 SQL：

1. 根据租户、`metricId`、精确 `metricVersionId`、`datasetId`、
   `datasetVersionId` 和 `definitionHash` 初次加载指标定义。
2. 严格解析 v2，验证全部 required 字段、业务定义、单位、DATE 合同、修饰词、复杂度、
   6.5 的 additivity 矩阵和规范摘要；这一步不产生数据库执行权。
3. 开始租户执行事务，按 7.6 先取得同租户语义治理共享门，再依次锁定精确
   `ACTIVE DWS` materialization 和当前发布数据集/版本；复核 schema/snapshot hash、
   稳定视图关系类型和 SELECT 权限。
4. 按 7.6 取得数据集和全部相关字段锁，再加载 profile、历史敏感性下限、一级维度、
   当前成员代及兼容关系。modifier 必须满足当前 `SUCCEEDED + FULL + low-cardinality`
   画像；modifier 和 allowed dimension 都必须非敏感。
5. 在持锁事务中重新加载指标定义并比较
   `metricVersionId + definitionHash + datasetVersionId`，同时验证
   `DERIVED + NONE`、唯一直接度量、无依赖、可再聚合顶层聚合、无运行参数，以及
   modifier 字段属于原始 DWS `groupBy` 输出粒度，拒绝 TOCTOU 漂移。
6. 继续执行 v1 已有的 source envelope 和行列策略门；客户端仍不能改变节点、Join、
   数据集固定过滤、参数或执行限额。
   在行/列策略尚不能安全编译到 materialized root 的 roll-up 之前时，v2 与 v1
   一样失败关闭；策略事实必须在同一执行事务内复核，modifier options 端点也必须先
   应用列可见性。
7. 把指标派生计划改写为受控 `materialized_root`，将逻辑字段映射为稳定视图白名单
   中的字段编码。
8. 在物化改写完成后、指标最终 roll-up 之前，把规范修饰词编译为服务端
   `PRE_ROLLUP` Filter；多个 Filter 固定 `AND`。它过滤的是已经聚合好的 DWS 粒度
   行，不是来源明细，不能恢复 DWS 已经聚掉的状态。客户端请求不携带这些 Filter。
9. 由 PostgreSQL 编译器为每个修饰值生成 7.3 规定的明确 OID/cast 绑定参数；日志、
   审计和错误不保存生成 SQL、alias、member_key 或值明文。
10. 在仍持有共享治理门和全部精确锁的事务中执行查询：先应用 modifier，再按原始聚合
    的安全函数 roll-up，最后按 6.5 区分“无行”与“全 NULL”并做数值精度收口。
11. 查询完成且全部结果行读取完毕后，写入冻结
    `metricDefinitionVersion + metricDefinitionHash`、一级维度历史快照、7.6 治理
    evidence 和实际 materialization 绑定的审计；计划摘要覆盖注入修饰词后的最终结构。
12. 事务提交后按 6.6 编码响应。任何值无法按声明的 `columnTypes` 无损编码时整次失败，
    不能回退为无类型 JSON number 或部分结果。

数据集在构建 DWS 时已经应用的固定过滤不会重放。v2 修饰词只会进一步收窄当前 DWS
物化行，不可能放宽数据集范围。

## 9. 为什么 MVP 禁止派生指标

假设：

- 指标 A 是“已支付金额”：`SUM(amount) WHERE status = PAID`；
- 指标 B 是“退款金额”：`SUM(amount) WHERE status = REFUNDED`；
- 指标 C 是“净支付金额”：`A - B`。

如果把 A 和 B 的修饰词简单合并到一个全局 `WHERE`，条件会变成
`status = PAID AND status = REFUNDED`，结果错误；若只采用其中一个条件，也会改变
另一个原子分支的口径。比率指标还可能要求分子、分母拥有不同范围和粒度。

当前执行器会把 `METRIC_REF` 展开为一棵普通算术表达式，DWS 物化改写随后把来源
表达式替换为再聚合列。这个过程没有保存“某个修饰范围只属于哪一个聚合分支”的边界。
因此以下任一情况在 MVP 都必须返回
`METRIC_SCOPED_DEPENDENCY_UNSUPPORTED`：

- v2 定义自身含 `METRIC_REF`；
- 引用的任何依赖版本含修饰词；
- 试图在已经聚合的派生表达式外追加修饰词；
- 分子、分母或其他算术分支需要不同修饰范围。

要求用户先把场景建成可证明粒度的 DWS 输出不是静默降级；它是 ScopedAggregate IR
可用前的失败关闭边界。

## 10. 存储投影与不可变性

规范 `definition_json + definition_hash` 仍是指标版本的唯一事实来源。关系表只保存
可由定义重建、便于约束和检索的投影，不能反向覆盖 JSON。

### 10.1 计划中的投影

- `metric_versions.definition_version`：允许 `1.0` 与 `2.0`，并强制等于
  `definition_json.schemaVersion`；
- `metric_dimensions.semantic_dimension_id/version/definition_hash`：可空，填写时冻结
  相同 `datasetVersionId + fieldId` 上的发布时一级维度快照；
- `metric_version_modifiers`：保存精确指标版本、修饰词 ID/名称、字段 ID、可选一级
  维度 ID/version/hash、操作符、规范 typed values JSON 和顺序；
- `metric_version_field_lineage`：保存 `MEASURE_SOURCE`、`MODIFIER`、`DIMENSION`、
  `TIME` 角色、字段 ID 和定义 JSON path；
- 现有 `metric_dependencies`：继续保存精确指标版本依赖；未来用于递归原子构件；
- 现有 dimension—metric compatibility 存储：发布事务写入由系统生成、固定到指标
  版本的 `DIRECT + SAFE + VERIFIED` 证据，不能由定义 JSON 或客户端投影；
- `query_runs.metric_definition_version/hash` 及治理证据引用：冻结草稿和发布版本执行时
  采用的定义摘要、profile evidence、一级维度版本、成员代和 compatibility 版本。

草稿更新在同一事务删除并重建投影；发布副本在 `PUBLISHING` 事务中从规范定义重建。
现有“发布指标派生索引不可修改”数据库触发器必须覆盖新投影表。所有新表强制租户
RLS，并使用包含 tenant、metric、metricVersion、datasetVersion 的复合外键。

### 10.2 结构化血缘

血缘完全由服务端推导，不接受客户端提交：

- 直接字段形成 `MEASURE_SOURCE`；
- 每个修饰字段形成 `MODIFIER`；
- 时间字段形成 `TIME`；
- 允许维度和层级字段形成 `DIMENSION`；
- `METRIC_REF` 继续形成 `metric_dependencies`；
- 查询运行另行记录实际物化和 snapshot。

只读合同 API 可以递归展开依赖并返回原子构件，但不能把 LLM 生成的
`lineageSummary` 当作结构化边。

### 10.3 语义文档

v2 发布版本的 `caliber`、modifier 摘要、单位、时间和结构化 lineage 必须从正式定义
确定性生成。候选或 LLM 文档只能补充可审核的业务描述、同义词和标签，不能优先覆盖
正式口径。语义文档不得写入敏感修饰值；值是否进入普通非敏感检索文档也应由租户
策略决定，默认只写修饰词名称和受控成员标签。

调用外部 embedding provider 是单独的数据出境动作，只有同时满足以下条件才允许：

- 租户已对目标 provider、数据域和文档类别显式授权；系统默认未授权；
- 文档先经过租户 DLP 策略检查并获得 `ALLOW`，检查使用的 policy version 和
  document hash 进入审计；
- 发送文档不包含原始 typed `member_key`、alias、modifier 值、敏感字段或敏感成员；
  只有租户策略明确允许时，才可使用经过审核的非敏感受控成员标签；
- prompt、队列载荷、错误、trace 和 provider 日志均不能记录被禁止的原文。

授权缺失或 DLP 拒绝时，可以保留不含值的本地确定性语义文档，但外部 embedding 必须
标记为 `SKIPPED_POLICY` 并失败关闭，不能改投另一个 provider、降低脱敏等级或把
原始文档写入重试队列。后续授权只触发基于当前发布定义和当前策略的新任务，不能复用
旧的未审计载荷。

## 11. API 设计

在运行时正式接受 v2 后，现有创建、更新、校验、试算和发布端点可以按
`schemaVersion` 分派，不另开可绕过校验的“v2 快捷端点”。

当前二进制仍先把整个请求严格解码为 v1，再检查版本，因此对 `2.0` 不能笼统描述为
一个错误：

- 请求保持 v1 形状、只把 `schemaVersion` 改为 `2.0` 时，HTTP 为 422，顶层
  `code=METRIC_VALIDATION_FAILED`，validation detail 为
  `METRIC_SCHEMA_VERSION_UNSUPPORTED`；
- 请求包含本文完整 v2 新字段时，v1 严格解码先因 unknown fields 失败，HTTP 仍为
  422，顶层 `code=METRIC_VALIDATION_FAILED`，validation detail 为
  `METRIC_DEFINITION_JSON_INVALID`。

目标 read-capable 二进制必须先只读取并验证唯一的 `schemaVersion`，再分派到独立的
v1/v2 严格解码器。它能够严格读取和展示 v2；功能开关关闭时，v2 操作的 validation
detail 才统一为 `METRIC_SCHEMA_VERSION_NOT_ENABLED`，未知版本则为
`METRIC_SCHEMA_VERSION_UNSUPPORTED`。这是待实现目标，不是当前行为；任何阶段都不能
把 v2 宽松解码或降级为 v1。

建议增加只读端点：

```http
GET /api/v1/metrics/{metricId}/versions/{metricVersionId}/contract
```

返回：

- 正式业务口径和确定性系统口径摘要；
- 直接或递归原子构件；
- 聚合方式、单位、时区和可加性；
- 允许维度及一级维度绑定；
- 修饰词及治理状态；
- 数据集字段血缘、指标依赖血缘和发布定义摘要；
- 当前执行支持状态及失败关闭原因。

编辑器的安全候选可以由专用只读端点提供：

```http
GET /api/v1/datasets/{datasetId}/versions/{datasetVersionId}/metric-modifier-options
```

它只返回调用者可读、非敏感、非高基数且属于 DWS groupBy 粒度的逻辑字段，以及
有权枚举的成员；modifier 字段必须具有绑定当前精确 ACTIVE 物化的
`SUCCEEDED + FULL + low-cardinality` 画像，成员必须来自当前活动成员代。响应把原始
typed `memberKey` 作为后续保存值；alias 和受控标签只作为查找/展示元数据，不能被
客户端回传为执行值。端点不返回 SQL、物理列、隐藏字段、敏感成员或任意源样本，并
遵守 7.6 的共享治理门和字段锁序。

试算请求继续只接受 `queryId`、数据集运行参数、所选允许维度和 `maxRows`。它不能
接受 `modifiers`、`filters` 或 SQL 覆盖。修改固定修饰词必须更新草稿、推进
`recordVersion` 和 `definitionHash`，再重新校验与发布。

查询成功响应遵守 6.6 的 `columns + columnTypes + rows` 合同；v2 endpoint 不能返回
未经类型复核的原生 PostgreSQL 值。完成 Expand 并部署 read-capable 版本后，尚未
打开 v2 功能开关的请求才统一返回
`METRIC_VALIDATION_FAILED / METRIC_SCHEMA_VERSION_NOT_ENABLED`。

## 12. UI 设计

v2 编辑器至少分为五个区块：

1. **业务口径**：统计对象、正式定义，以及只读的系统口径摘要；
2. **计算方式**：度量字段、业务聚合/安全再汇总、空值、除零和精度；
3. **固定修饰范围**：字段/一级维度、操作符、规范值；控件随字段类型限制操作符；
4. **维度、时间与单位**：允许维度、默认粒度、IANA 时区、单位编码和展示符号；
5. **血缘与发布影响**：只读原子构件、字段血缘、物化要求和兼容关系。

交互规则：

- 不提供 SQL、Filter JSON 或任意表达式文本框；
- modifier 值只能从符合 7.6 的当前成员代选择；用户按成员名或 alias 搜索后，UI 保存
  服务端返回的原始 typed `memberKey`，定义中不保存 alias、展示名或规范化搜索词；
- 当前成员代变化、alias 改绑或服务端返回 `METRIC_MODIFIER_MEMBER_STALE` 时清除失效
  选择并要求重新加载，不能以手工文本、高基数绕过或旧 alias 猜值；
- 高基数字段在第一切片中不可选择，也不弹出全量成员列表；
- 敏感字段完全禁用，并说明治理原因；
- `NEQ/NOT_IN` 明示“NULL 不匹配”，不替用户猜测空值意图；
- `DATETIME` 在第一切片中禁用并显示“时间来源语义尚未治理”；后续具备时间来源合同
  后才显示 IANA 时区选择；
- 无维度预览明确显示空集合采用 `NULL` 还是 `ZERO`，有维度预览不伪造零值分组；
- 发布确认必须并排展示业务说明和系统口径摘要；
- 发布版本只读，任何修改创建或推进草稿；
- v1 编辑器保持原行为；当前客户端不应尝试用 v1 表单保存 v2 定义。

指标 AI 只有在响应 Schema 能把字段和成员限制为当次授权枚举后，才可以建议修饰词。
没有安全成员证据时返回 `NEEDS_CLARIFICATION`；模型不能猜原始值。LLM 对名称、说明
或标签的修改不能改变 expression、aggregation、modifier、unitCode 或 lineage。

## 13. 迁移策略

本文不创建或预留迁移文件。实际实现应从 `000072` 或之后的第一个未占用编号开始，
以落地时仓库中的真实最大编号为准；不得因为本文举例而复用已存在编号。

建议采用 expand/enable/contract 顺序：

1. **Expand**：增加 `2.0` 允许值、新投影表、复合外键、RLS、不可变触发器和审计列；
   不删除、不改写任何 v1 数据。现有
   `metric_versions.definition_version CHECK (definition_version='1.0')` 需要以
   可审计迁移 drop/re-add 为 `IN ('1.0','2.0')`；上线前在生产规模副本验证锁等待和
   扫描时间。
2. **Read capable**：先部署能够读取并明确拒绝执行 v2 的服务版本，保证滚动升级期间
   旧实例不会误解析；该版本必须实现“先读取唯一 schemaVersion、再严格分派”的
   pre-dispatch，不能复用当前先解码 v1 的错误链。
3. **Runtime capable, flag off**：部署 v2 Schema、校验、存储投影、共享治理门与锁序、
   typed 参数编译、查询响应 wire codec 和 UI，默认全局及所有租户关闭创建/发布。
4. **Canary enable**：只对测试租户和指定 DWS 版本开放，运行基线对账。
5. **Controlled enable**：按租户/数据域扩大；监控错误率、结果差异和查询成本。
6. **Contract**：只有在所有旧实例退出、回退窗口结束后，才考虑收紧无用兼容列或
   清理实验状态；不可删除历史 v1 定义或发布版本。

不得自动将 v1 迁移为 v2。若后续需要升级助手，它只能生成待审核草稿，并保留来源
v1 版本、转换规则版本和差异摘要。

一旦任一环境允许写入 v2，v2-aware 的 read-capable 二进制就是不可越过的回滚下限：
可以关闭 v2 创建/发布/执行开关，但不能回滚到只认识 v1、无法读取 v2 行的旧二进制。
应用部署系统应记录该 floor，并在允许首个 v2 写入前确认所有实例和离线任务都已达到
这一版本。

## 14. 稳定错误码

下表中的定义校验码是 validation detail code。沿用当前 API envelope 时，HTTP 422 的
顶层仍是 `METRIC_VALIDATION_FAILED`，客户端必须读取 detail code；除非未来通过独立
API 版本明确修改，否则不能把 detail code 提升为顶层 code。特别地，当前完整 v2
请求在 pre-dispatch 实现前仍会先得到既有的
`METRIC_DEFINITION_JSON_INVALID`，不能伪称为下列目标 v2 错误。

| `code` | 含义 |
| --- | --- |
| `METRIC_DEFINITION_JSON_INVALID` | 当前严格解码目标结构失败，包括当前 v1 解码器遇到完整 v2 新字段 |
| `METRIC_SCHEMA_VERSION_UNSUPPORTED` | pre-dispatch 识别到未知 schema version；当前 v1 形状的 `2.0` 请求也会走到此 detail |
| `METRIC_SCHEMA_VERSION_NOT_ENABLED` | 部署已能严格读取 v2，但当前环境或租户尚未启用相应操作 |
| `METRIC_V2_DWS_REQUIRED` | v2 MVP 没有绑定精确可用的 DWS 版本 |
| `METRIC_V2_ACTIVE_MATERIALIZATION_REQUIRED` | 缺少匹配 schema 的当前 ACTIVE 物化 |
| `METRIC_V2_SHAPE_UNSUPPORTED` | 不是 DERIVED + NONE 的单一可再聚合 DWS MEASURE |
| `METRIC_V2_ADDITIVITY_INCOMPATIBLE` | 原始聚合与 additivity 不满足 6.5 矩阵，或使用了 SEMI_ADDITIVE |
| `METRIC_MODIFIER_GRAIN_INVALID` | 修饰字段不属于原始 DWS groupBy 输出粒度 |
| `METRIC_MODIFIER_LIMIT_EXCEEDED` | 修饰词或总值数量超限 |
| `METRIC_MODIFIER_ID_DUPLICATE` | 修饰词 ID 重复 |
| `METRIC_MODIFIER_FIELD_INVALID` | 字段不存在、不可见、是度量或不属于精确版本 |
| `METRIC_MODIFIER_DIMENSION_MISMATCH` | 一级维度与版本或字段不匹配 |
| `METRIC_MODIFIER_PROFILE_UNAVAILABLE` | 当前精确 ACTIVE 物化缺少成功、FULL、低基数画像证据 |
| `METRIC_MODIFIER_MEMBER_STALE` | typed member_key 不属于当前活动成员代或来源刷新已失效 |
| `METRIC_MODIFIER_OPERATOR_UNSUPPORTED` | 操作符不在白名单 |
| `METRIC_MODIFIER_VALUE_ARITY_INVALID` | 操作符和值数量不匹配 |
| `METRIC_MODIFIER_VALUE_TYPE_MISMATCH` | 值类型或规范格式与字段不匹配 |
| `METRIC_MODIFIER_SENSITIVE_FORBIDDEN` | 修饰字段被敏感治理规则禁止 |
| `METRIC_MODIFIER_HIGH_CARDINALITY_FORBIDDEN` | 第一切片禁止高基数字段作为修饰词 |
| `METRIC_DIMENSION_SENSITIVE_FORBIDDEN` | 允许维度被历史敏感性下限禁止 |
| `METRIC_DIMENSION_COMPATIBILITY_UNAVAILABLE` | 缺少当前 `DIRECT + SAFE + VERIFIED` 系统兼容证据 |
| `METRIC_DATETIME_SEMANTICS_UNAVAILABLE` | 尚无可证明的 DATETIME 来源、仓储和时区合同 |
| `METRIC_TIME_ZONE_REQUIRED` | 后续 DATETIME 合同缺少 IANA 时区 |
| `METRIC_TIME_ZONE_INVALID` | 后续时区不存在、含糊或与时间字段合同冲突 |
| `METRIC_UNIT_CODE_INVALID` | 单位编码缺失或不规范 |
| `METRIC_EMPTY_SET_HANDLING_INVALID` | 空集合策略与原始聚合或请求粒度不兼容 |
| `METRIC_NUMERIC_OVERFLOW` | v2 执行精度收口后超出 numeric(38, scale) |
| `METRIC_RESULT_ENCODING_INVALID` | 查询值无法按声明的 v2 columnTypes 无损编码 |
| `METRIC_SCOPED_DEPENDENCY_UNSUPPORTED` | 定义或依赖需要 MVP 不支持的独立修饰范围 |
| `METRIC_CONTRACT_SNAPSHOT_MISMATCH` | 执行前定义、版本、治理事实或摘要发生漂移 |

HTTP 映射沿用 v1：定义语义问题返回 422，版本/摘要漂移返回 409，当前运行能力未启用
统一返回 422；部署整体不可用才返回 503，不能用 503 表示租户功能开关关闭。同一
错误码在所有实例上必须一致。

## 15. 测试门

### 15.1 合同和兼容

- v1 全部 golden JSON、规范 hash、创建、更新、试算和发布结果完全不变；
- v1 带任何 v2 字段严格拒绝；
- 对当前二进制分别固定两条现实基线：v1 形状仅改 `schemaVersion=2.0` 时为
  HTTP 422、顶层 `METRIC_VALIDATION_FAILED`、detail
  `METRIC_SCHEMA_VERSION_UNSUPPORTED`；完整 v2 新字段请求为相同 HTTP/顶层、detail
  `METRIC_DEFINITION_JSON_INVALID`；
- read-capable 阶段验证先读唯一 `schemaVersion` 再严格分派；v2 功能关闭时 detail
  为 `METRIC_SCHEMA_VERSION_NOT_ENABLED`，未知版本为
  `METRIC_SCHEMA_VERSION_UNSUPPORTED`，且同一部署阶段所有实例一致；
- v2 重复键、未知字段、尾随 JSON、复杂度、大小和控制字符测试；
- modifier 排序、IN 值去重/排序和 canonical hash 确定性测试；
- `businessDefinition`、必填空数组 `modifiers: []`、`unitCode`、`timeZone`、
  `emptySetHandling` 和一级维度 ID/version/hash 全部参与 canonical hash；
- v2 成功响应的 `columns` 与 `columnTypes` 等长、同序，v1 wire response 不变。

### 15.2 语义校验

- 每个操作符的 arity、字段类型和值类型正反用例；
- BIGINT 上下界、DECIMAL 38 位、`-0`、非法指数和 DATE 公历边界；
- 第一切片对 time field 或 modifier 中的 DATETIME 一律返回
  `METRIC_DATETIME_SEMANTICS_UNAVAILABLE`；后续时间能力另测 RFC 3339 偏移和 tzdb；
- NULL、`NEQ`、`NOT_IN`、`IS_NULL` 和 `IS_NOT_NULL` 的固定结果；
- 原始 `SUM/COUNT + ADDITIVE` 与 `MIN/MAX + NON_ADDITIVE` 通过；所有交叉组合及
  `SEMI_ADDITIVE` 返回 `METRIC_V2_ADDITIVITY_INCOMPATIBLE`；
- 非 DWS、非当前版本、缺少物化、度量修饰字段、隐藏字段、敏感 modifier 和敏感
  allowed dimension 均失败关闭；
- `ATOMIC`、非 NONE aggregation、计算度量、不可分解聚合、非 groupBy modifier、
  高基数 modifier 和带参数 DWS 失败关闭；
- 只有当前精确 `SUCCEEDED + FULL + low-cardinality` 画像可用；旧物化、旧 snapshot、
  `QUEUED/RUNNING/FAILED/STALE/SKIPPED_POLICY` 及默认 false 都失败关闭；
- alias 只在 options/UI 解析，规范定义只含当前成员代的原始 typed `member_key`；
  旧成员代、消失成员和 alias 改绑返回 `METRIC_MODIFIER_MEMBER_STALE`；
- 同 DWS、同字段的发布事务只能接受系统生成的 `DIRECT + SAFE + VERIFIED` 兼容证据；
  客户端提交、人工 PROPOSED、跨 DWS、INDIRECT 或 UNSAFE 关系失败关闭；
- `METRIC_REF` 及带修饰依赖始终失败关闭。

### 15.3 存储和安全

- 草稿投影可重建，发布副本与定义摘要一致；
- 发布后的 modifier/lineage/dimension 投影不可修改或删除；
- 一级维度发布时 version/hash 快照不可变；当前敏感性、停用或不安全兼容关系只能
  让执行失败，不能覆盖历史快照；
- 历史已批准敏感绑定、未废弃敏感维度、敏感候选/建议和历史
  `SENSITIVE_FIELD_PROFILE_SKIPPED` 各自都能单独触发敏感性下限；当前标签放宽不能
  重新启用 modifier 或 allowed dimension；
- options、保存、校验、试算、发布和执行持有租户共享治理门；画像、成员、维度、
  兼容关系和 ACTIVE 物化写入持有同 key 排他门，并发测试证明写者不能穿越安全检查；
- 锁序严格为 ACTIVE materialization、当前数据集/版本、dataset advisory、排序后的
  field advisory、profile/dimension/current-member/alias/compatibility；反序路径由
  测试或静态检查禁止；
- 发布生成的 compatibility evidence 固定到指标版本且不可由客户端修改；执行审计
  冻结实际 profile evidence、dimension version、member generation 和 compatibility；
- 复合外键、租户 RLS、跨租户不可见和并发发布；
- query run 冻结 definition version/hash，身份字段不可变；
- LLM 不能覆盖正式 caliber、modifier、unitCode 或结构化 lineage；
- 外部 embedding 未获租户授权或 DLP 非 ALLOW 时为 `SKIPPED_POLICY`，provider 和
  重试载荷均收不到原文；任何路径都不发送 member_key、alias、modifier 值或敏感内容；
- SQL 注入字符串只作为绑定值，日志、审计和错误不出现生成 SQL或值明文。

### 15.4 PostgreSQL 集成

- 全新数据库迁移及权限验证；
- 精确 ACTIVE DWS 上 `IN`、数值范围、日期范围和 NULL 修饰的预览/发布；
- 修饰发生在 DWS 行之后、指标 roll-up 前；按维度和无维度结果均与人工审核 SQL
  基线一致，且不能引用已被 DWS 聚掉的来源字段；
- 无维度 `SUM/COUNT + ZERO` 分别覆盖真正零输入行和“至少一行但 SUM 全 NULL”；
  前者为零、后者仍为 NULL；另测 `SUM/MIN/MAX + NULL` 和有维度零行；
- modifier DECIMAL 以任意精度规范文本绑定 `numeric` OID 或显式 `::numeric`，不会按
  指标 decimalScale 舍入；另测正负半值 HALF_UP、结果 scale、
  `numeric(38, scale)` 溢出；
- BIGINT 超过 `2^53`、38 位 DECIMAL、负零、NULL 和混合列通过 wire codec 后逐字节
  符合 `columnTypes`，INTEGER/DECIMAL 永不成为 JSON number；
- DATE 的 DAY/WEEK/MONTH/QUARTER/YEAR 表达式结果 PostgreSQL 类型均为 `date`，
  WEEK 以 ISO 周一开始；不同 session `TimeZone` 下的 `YYYY-MM-DD` 响应逐字节一致；
- ACTIVE 指针、schema/snapshot hash、稳定视图或定义 hash 漂移立即失败；
- 查询开始后并发切换 ACTIVE 物化、完成新画像、切换成员代或变更敏感/兼容关系时，
  排他治理写必须等待；下一次查询读取新事实并按合同通过或失败，不能出现 TOCTOU；
- 客户端篡改 QueryCandidate、DSL Filter、物理列或操作符不能越过运行时复核；
- 行列策略尚不能在 materialized root 上安全前置时失败关闭，options 端点不泄露隐藏列；
- 查询计划使用绑定参数，资源上限与取消仍生效。

### 15.5 前端

- v1 页面无回归；
- 字段类型切换会清除不合法操作符和值；
- NULL 和时区提示准确；
- DATETIME 未治理、敏感、高基数和无成员索引状态可见且不可绕过；
- alias 搜索只提交服务端返回的当前 typed `memberKey`；定义 payload 和重新打开的
  表单都不含 alias，成员换代/alias 改绑后要求重新选择；
- 空集合策略只展示与原始聚合兼容的选项；
- 乐观锁冲突重新加载；
- 发布版本只读；
- AI 只能选择响应中明确授权的字段和成员。

## 16. 灰度、观测与回退

灰度前为每个 canary 指标准备人工审核的 PostgreSQL 基线查询和固定测试数据。至少观测：

- v2 保存、校验、试算和发布成功率；
- 各稳定错误码计数；
- v2 结果与基线差异；
- DWS 扫描行数、耗时、超时和取消率；
- modifier 数量、IN 集合大小和高基数拒绝次数；
- 定义/物化快照漂移、profile unavailable、member stale、敏感性下限和 compatibility
  失败关闭次数；
- 共享/排他治理门等待时间、锁超时和死锁数；
- wire codec 失败及按 `columnTypes` 的编码数量；
- 语义文档重建、embedding 队列积压、租户未授权和 DLP 拒绝/跳过次数。

回退优先关闭功能开关：

- 停止创建、更新和发布 v2，不影响 v1；
- 保留已写入的 v2 草稿、发布版本、投影和审计，不做破坏性降级；
- 已发布 v2 可按租户开关停止执行并返回稳定错误，不自动改用 v1 或其他版本；
- 修复后从精确定义和物化重放验证；
- 数据库迁移保持 additive，回退窗口内不删除新列和新表；
- 不能把 v2 定义转成描述相似但语义不完整的 v1 定义。

## 17. 未来：`ScopedAggregate` 逻辑 IR

完整派生指标不能继续依赖“把所有指标定义展开成一棵无作用域算术表达式”。目标 IR
需要显式保存每个聚合分支：

```text
MetricPlan
└── Arithmetic(SUBTRACT)
    ├── ScopedAggregate
    │   ├── sourceFieldId: paid_amount_sum
    │   ├── rollup: SUM
    │   ├── predicate: payment_status IN [PAID]
    │   ├── grain: [paid_month, region]
    │   └── unitCode: CNY
    └── ScopedAggregate
        ├── sourceFieldId: refund_amount_sum
        ├── rollup: SUM
        ├── predicate: refund_status IN [SUCCESS]
        ├── grain: [refund_month, region]
        └── unitCode: CNY
```

编译器按分支选择条件聚合或独立同粒度子查询，再按经过验证的维度键合并。开放前必须
证明：

- 每个 predicate 只作用于自己的聚合；
- 分支粒度、时间归属和允许维度兼容；
- Join 不产生扇出；
- ADD/SUBTRACT 单位一致，DIVIDE 的结果单位明确；
- AVG、COUNT_DISTINCT 等不可分解度量携带足够部分状态，或明确失败关闭；
- NULL、空集合和除零在所有分支一致；
- 复杂度、子查询数、扫描量和超时有硬上限；
- 最终计划仍由服务端生成并以绑定参数执行。

在这些条件完成前，`ScopedAggregate` 只属于后续设计，不能以全局 Filter 或表达式
文本近似实现。
