# 指标定义 v1 与执行边界

本文描述 `metric-definition-v1` 的服务端事实合同。机器可校验结构见
[`api/schemas/metric-definition-v1.schema.json`](../api/schemas/metric-definition-v1.schema.json)，完整原子指标示例见
[`api/examples/metric-definition-v1.json`](../api/examples/metric-definition-v1.json)。

## 1. 第一阶段可依赖能力

- 指标定义必须精确绑定一个 `datasetId` 和一个处于 `PUBLISHED` 状态的 `datasetVersionId`。服务端不会把失效、废弃或不存在的版本替换为数据集当前版本。
- 指标可以是原子指标 `ATOMIC`、派生指标 `DERIVED` 或比率指标 `RATIO`。指标间依赖使用精确的已发布 `metricVersionId`，而不是指标主对象 ID。
- 草稿校验、草稿试算和发布前试算复用同一套服务端语义校验与查询运行时。发布前会执行最多一行的校验查询；查询失败时发布失败关闭。
- 第一阶段只执行 `SINGLE_SOURCE` 单数据源数据库数据集，目前数据库范围是 MySQL 和 Oracle。
- 发布版本是不可变快照。发布后可以按精确版本读取、试算、查看占用计数，也可以手工从 `PUBLISHED` 单向迁移为 `DEPRECATED`。

## 2. JSON 与规范化

定义使用 UTF-8 JSON，根对象的 `schemaVersion` 固定为 `1.0`。HTTP 请求体上限为 2 MiB，其中 `definition` 自身的服务端上限为 1 MiB。

服务端执行严格解码：拒绝未知字段、重复键、尾随 JSON 文档和非法数字。通过校验后，服务端会规范 UUID、十进制字面量和对象内容，并对规范 JSON 计算 SHA-256 `definitionHash`。保存草稿、发布和幂等重放均以服务端返回的规范内容与摘要为准，不应对客户端原始 JSON 文本自行推断等价性。

## 3. 根字段

| 字段 | 约束与语义 |
| --- | --- |
| `schemaVersion` | 固定为 `1.0`。 |
| `metric` | 指标描述对象，包含 `code`、`name`、`description`、`type`。 |
| `datasetId` | 所属数据集 UUID；更新现有指标时不可变。 |
| `datasetVersionId` | 精确绑定的已发布数据集版本 UUID。 |
| `expression` | 字段、精确指标版本、十进制字面量或二元算术表达式。 |
| `aggregation` | `NONE`、`SUM`、`AVG`、`MIN`、`MAX`、`COUNT` 或 `COUNT_DISTINCT`。 |
| `unit` | 最长 32 个字符的展示单位，可以为空。 |
| `numberFormat` | 1 到 64 个字符的展示格式。 |
| `timeFieldId` | 时间字段；`timeGrain` 非 `NONE` 时必填，且必须出现在 `allowedDimensions` 中。 |
| `timeGrain` | `NONE`、`DAY`、`WEEK`、`MONTH`、`QUARTER` 或 `YEAR`。 |
| `additivity` | `ADDITIVE`、`SEMI_ADDITIVE` 或 `NON_ADDITIVE`。 |
| `nonAdditiveDimensionFieldIds` | 仅半可加指标可设置，至少一项且必须来自允许维度。 |
| `allowedDimensions` | 当前指标版本内嵌的可分组字段映射。 |
| `decimalScale` | 0 到 12 的小数位声明。 |
| `roundingMode` | 第一阶段只接受 `HALF_UP`。 |
| `nullHandling` | 第一阶段只接受 `IGNORE`。 |
| `divisionByZero` | 第一阶段只接受 `NULL`；服务端为除法生成分母为零时返回 `NULL` 的表达式。 |

`metric.code` 必须匹配 `^[A-Za-z][A-Za-z0-9_]{0,63}$`，在租户内唯一，且现有指标更新时不可变。名称长度为 1 到 200，说明最长 2000；服务端拒绝控制字符。

`decimalScale` 和 `HALF_UP` 当前是经过校验并持久化的合同声明，但生成查询尚未在 MySQL、Oracle 等不同引擎上统一强制最终精度与舍入。因此第一阶段不能宣称同一指标已经具备跨引擎逐位一致的精确小数结果；需要精确财务口径的调用方应在目标引擎上验证结果。

## 4. 表达式 DSL

单个定义的表达式最多 16 层、128 个节点。服务端还会限制跨版本依赖图和最终执行表达式：最多加载 128 个精确依赖版本、依赖深度不超过 64 层，展开结果不超过 2048 个节点或 64 层。重复指标引用和除零保护生成的节点都会计入总预算，超限会在生成查询计划前失败关闭。支持以下节点：

| `type` | 必填载荷 | 说明 |
| --- | --- | --- |
| `FIELD_REF` | `fieldId` | 引用精确数据集版本中的字段。仅 `INTEGER`、`DECIMAL` 字段可作为指标值。 |
| `METRIC_REF` | `metricVersionId` | 引用同一精确数据集版本上的已发布指标版本。 |
| `LITERAL` | `value` | 使用字符串表达的规范十进制数，总数字位数不超过 38。 |
| `ADD` | 两个 `arguments` | 二元加法。 |
| `SUBTRACT` | 两个 `arguments` | 二元减法。 |
| `MULTIPLY` | 两个 `arguments` | 二元乘法。 |
| `DIVIDE` | 两个 `arguments` | 二元除法，分母为零时结果为 `NULL`。 |

每个算术节点必须恰好有两个参数。单个定义不能混合 `FIELD_REF` 和 `METRIC_REF`：原子字段表达式由数据集字段计算，派生表达式则由精确指标版本和字面量组合。

字段表达式必须使用非 `NONE` 聚合；包含 `METRIC_REF` 的表达式必须使用 `NONE`。`ATOMIC` 不允许引用指标版本，`DERIVED` 与 `RATIO` 至少引用一个指标版本，`RATIO` 还必须包含 `DIVIDE` 节点。例如：

```json
{
  "type": "DIVIDE",
  "arguments": [
    {
      "type": "METRIC_REF",
      "metricVersionId": "33333333-3333-4333-8333-333333333333"
    },
    {
      "type": "METRIC_REF",
      "metricVersionId": "44444444-4444-4444-8444-444444444444"
    }
  ]
}
```

服务端会验证依赖版本均为 `PUBLISHED`、属于同一 `datasetVersionId`、调用者具有 `METRIC:READ`、允许维度兼容、时间字段和时间粒度完全一致、不是自身历史版本，并拒绝直接或间接循环依赖。只含 `LITERAL` 而不引用字段或指标版本的定义会被拒绝，避免把逐明细行常量误当成单值指标。

## 5. 维度、时间和可加性

`allowedDimensions` 中的每项包含：

- `fieldId`：精确数据集版本中的非度量字段，角色只能是 `DIMENSION`、`TIME`、`ATTRIBUTE` 或 `IDENTIFIER`；同一定义中不能重复。
- `name`：1 到 200 个字符的展示名。
- `hierarchyFieldIds`：有序的下钻字段声明，字段也必须是非度量字段且不能重复。
- `sortDirection`：`ASC` 或 `DESC`。
- `nullLabel`：最长 100 个字符的空值展示标签。

时间粒度和时间字段必须同时存在或同时省略。时间字段的数据类型必须是 `DATE` 或 `DATETIME`，角色必须为 `TIME`；执行计划对该维度使用定义中的时间粒度截断。

半可加指标必须声明至少一个不可加维度；其他可加性类型不得声明该列表。使用 `AVG`、`COUNT_DISTINCT`、`RATIO` 或任何 `METRIC_REF` 的定义不能标记为 `ADDITIVE`。

当前维度只是每个指标版本内嵌的字段映射，尚不是可独立管理、复用和版本化的一级维度对象。

## 6. 服务端派生执行计划

客户端不能提交指标 SQL，也不能直接提交指标执行计划。服务端从已发布数据集 DSL 派生一个临时查询计划：

1. 加载请求中精确指定的已发布数据集版本。
2. 将所选维度限制为定义中的 `allowedDimensions`，展开数据集字段表达式和精确指标版本依赖。
3. 只改写输出字段、分组、排序和输出粒度，并清空 `having`；数据源、节点、连接、过滤、参数和执行策略必须与可信数据集 DSL 完全一致。
4. 把派生计划交给统一查询运行时，使用绑定参数执行，并在查询审计中记录 `metricId`、指标版本 ID、当次数据集版本和计划摘要。发布指标版本可精确还原；可变草稿的历史定义快照仍属于后续审计增强项。

查询运行时会再次比较上述不可扩张的安全包络。客户端请求无法借由指标定义替换数据源、放宽过滤、注入连接或提升执行范围。

## 7. 第一阶段失败关闭边界

以下能力尚未开放，不能作为成功路径依赖：

| 场景 | 当前行为 |
| --- | --- |
| `CROSS_SOURCE` 数据集 | 草稿试算和发布前试算拒绝执行，发布失败关闭。 |
| Excel 数据源 | 草稿试算和发布前试算拒绝执行，发布失败关闭。 |
| 行级或列级数据策略 | 当前策略编译层位于聚合之后，指标试算拒绝执行，发布失败关闭，避免聚合前策略旁路。 |
| 预聚合数据集 | 数据集 DSL 已有 `groupBy`、`having` 或字段表达式含聚合时，指标语义校验即拒绝；因此无法通过创建/更新校验、试算或发布。 |
| 非精确或未发布数据集版本 | 拒绝，不自动切换到其他数据集版本。 |
| `STALE` 或 `DEPRECATED` 指标版本试算 | 拒绝，不自动切换到当前发布版本。 |

连接数据集还会沿完整 Join 图追踪指标来源，进行包含多跳关系的静态风险检查。对重复敏感的指标，如果发布试算产生 `JOIN_FANOUT_RISK`、`JOIN_CARDINALITY_MISMATCH` 或 `JOIN_MANY_TO_MANY` 风险，发布会失败关闭。

## 8. 后续能力，不属于 v1 已完成范围

- 一级维度对象及其独立复用、权限和版本生命周期。
- 派生指标和比率指标的可视化编辑器；后端表达式合同已存在，不代表前端可视化建模已经完成。
- 派生指标的组合单位、可加性传播和需要独立子查询才能保持的复杂粒度语义；第一阶段只允许依赖版本使用完全相同的时间字段与时间粒度，不能据此宣称任意派生口径均已闭合。
- 数据集或上游指标变化后的 `STALE` 自动传播与完整状态迁移。目前只提供手工 `PUBLISHED` → `DEPRECATED`。
- 报告定义对精确 `metricVersionId` 的端到端绑定。持久化层保留了迁移兼容能力，但报告 JSON 合同和设计器仍需后续改造；在完成前不能把报告重放描述为精确指标版本重放。
- `decimalScale` 与 `HALF_UP` 的跨数据库引擎统一、逐位精确执行语义。
- `CROSS_SOURCE`、Excel、聚合前数据策略以及预聚合数据集上的安全指标试算与发布。
