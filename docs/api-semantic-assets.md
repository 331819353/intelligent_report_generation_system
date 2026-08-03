# 语义资产 API

语义资产用于维护“常用词 → 映射值 + 知识类型”的受控词典。它与 DWS
维度资产独立存储、独立展示；原 `/semantic-governance` 入口继续兼容，但会
跳转到资产管理中心。

## 数据与向量边界

- `commonTerm`：常用词，也是唯一送入向量模型的文本。
- `mappingValue`：匹配后返回的规范映射值，不参与向量化。
- `knowledgeType`：资产类型，不参与向量化。
- 同一租户内，`knowledgeType + commonTerm` 唯一。
- 新建、重新启用或修改常用词后由 worker 异步生成向量；停用后立即清除向量。

## 权限

- 查询接口需要全局 `DATASET:READ`。
- 新增、更新、批量导入和停用需要全局 `DATASET:MANAGE`。
- 所有读写均受租户行级安全策略隔离。

## 接口

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| `GET` | `/api/v1/semantic-assets/catalog` | 返回统一资产目录、治理状态、执行资格与同事务就绪度水位；支持 `q`、`objectType`、`status`、`limit`、`offset` |
| `GET` | `/api/v1/semantic-assets/readiness` | 在一个租户事务中返回问数资产就绪度、语义图水位、稳定检查码和阻断码 |
| `GET` | `/api/v1/semantic-assets/releases` | 分页查询语义发布包和四类投影状态 |
| `GET` | `/api/v1/semantic-assets/releases/active` | 查询当前原子激活的 `semantic_version/content_hash` 和状态指针版本 |
| `GET` | `/api/v1/semantic-assets/releases/{id}` | 查询发布包完整对象清单、验证结果和投影证据 |
| `POST` | `/api/v1/semantic-assets/releases` | 创建固定对象清单和内容哈希的 DRAFT 发布包 |
| `POST` | `/api/v1/semantic-assets/releases/{id}/validate` | 按 `expectedVersion` 执行七层合同完整性和关系安全门禁 |
| `POST` | `/api/v1/semantic-assets/releases/{id}/activate` | 按发布包和状态指针双重乐观锁原子激活；四类投影必须 READY 且 hash 一致 |
| `GET` | `/api/v1/semantic-assets` | 分页查询；支持 `q`、`knowledgeType`、`status`、`embeddingStatus` |
| `GET` | `/api/v1/semantic-assets/types` | 查询知识类型 |
| `POST` | `/api/v1/semantic-assets` | 新增资产 |
| `PUT` | `/api/v1/semantic-assets/{id}` | 按 `expectedVersion` 更新 |
| `POST` | `/api/v1/semantic-assets/import` | 幂等批量导入 |
| `POST` | `/api/v1/semantic-assets/{id}/deprecate` | 按 `expectedVersion` 停用 |
| `GET` | `/api/v1/semantic-parsing-rules` | 查询平台默认和当前租户解析规则；支持 `q`、`ruleType`、`status` |
| `POST` | `/api/v1/semantic-parsing-rules` | 创建或重新启用当前租户规则 |
| `PUT` | `/api/v1/semantic-parsing-rules/{id}` | 按 `expectedVersion` 更新租户规则 |
| `POST` | `/api/v1/semantic-parsing-rules/{id}/deprecate` | 按 `expectedVersion` 停用租户规则 |
| `GET` | `/api/v1/semantic/dimension-where-decision-groups` | 查询全部已发布 DWS 维度及成员、决策、待处理数量和构建状态 |
| `GET` | `/api/v1/semantic/dimension-where-decisions` | 分页查询已验证维度 WHERE 决策；支持 `q`、`tableName`、`dimensionId`、`limit`、`offset` |

资产管理中心首屏使用 `catalog` 统一控制面读模型，不再遍历指标、维度、词汇和规则的
多个分页接口后在浏览器自行拼接治理状态。Catalog 在同一个租户事务中返回
`readiness` 和 `items`，对象类型包括 `METRIC / DIMENSION / TERM / PARSING_RULE`；
每个对象统一包含认证状态、执行资格、就绪码、版本/内容哈希、负责人和敏感等级。

`readiness` 仍作为可独立轮询的发布门禁投影，包含：

- `questionEnabled` 与总状态 `PASS / WARN / BLOCKED`；
- 指标、发布维度、业务词汇、解析规则、决策图的 `total / ready`；
- `semanticVersion`、当前 graph generation 和 requested/applied event watermark；
- `checks[].code/status/current/required/detail/route` 与 `blockerCodes`。

没有任何已发布指标时 `METRIC_CONTRACT_READY` 会阻断问数；
`SEMANTIC_GRAPH_READY` 是另一项全局阻断门禁。部分草稿指标、维度索引、词汇投影或
预计算维值决策不完整时只给出治理警告，具体问题仍须通过 QueryPlan 的精确版本、
兼容性和血缘校验，不能因无关草稿资产阻断整个租户。

## 原子语义发布包

`semantic_releases` 不复制或取代指标、维度和数据集的原生编辑 API。它在发布时固定
Domain、BusinessTerm、Entity、SemanticModel、Measure、Metric、Dimension、
DimensionValue、Time、Cohort、Relation、Dataset、TableColumn、Policy、QualityRule、
CertifiedExample 和 ParsingRule 的稳定 ID、对象版本、Owner、生效期、合同及内容哈希。

创建请求中的所有对象必须为 `CERTIFIED`，并且至少包含 Metric、Dimension、Time、
Relation、Dataset、Policy 和 QualityRule。服务端会执行对象类型相关的必填合同检查，
例如：

- Metric 必须声明公式、粒度、默认时间、来源数据集、权限和质量规则；
- Relation 必须认证、基数已知且 fanout 策略不能为 `UNSAFE`；
- Time 必须声明时区、日历和完整周期策略；
- Dataset 必须声明粒度、来源和新鲜度合同。

验证通过后发布包进入 `PROJECTING`。以下投影是固定门禁，不由客户端删减：

1. `EXECUTION_SEMANTIC_LAYER`；
2. `POSTGRES_REGISTRY`；
3. `SEARCH_INDEX`；
4. `NEBULA_GRAPH`。

只有四个投影的 `appliedContentHash` 都等于发布包 `contentHash`，发布包才会进入
`READY`。激活操作同时校验发布包 `expectedVersion` 和租户状态
`expectedStateVersion`，在一个数据库事务中将旧版本标记为 `SUPERSEDED`、新版本标记
为 `ACTIVE` 并切换活动指针。任何半发布状态都不能进入智能问答运行时。

批量导入请求示例：

```json
{
  "items": [
    {
      "commonTerm": "财资",
      "mappingValue": "财资生态圈",
      "knowledgeType": "领域名称"
    }
  ]
}
```

导入会返回 `inserted`、`updated`、`unchanged` 和 `total`。客户端应在导入前
拆分一个单元格中的多个常用词并剔除空词；本次 Excel 导入按 `、`、中文逗号
和英文逗号拆分同义词，仅剔除映射值为 `--` 的源行。

维度组接口用于页面首屏，始终返回全部已发布 DWS 正式维度；维度没有有效成员、
属于高基数精确匹配或仍在后台构建时也不会从列表消失。前端展开某个维度后，
再调用决策分页接口加载明细。

维度 WHERE 决策接口返回已通过 LLM 字段级策略与服务端安全编译的预计算关系，
以及被成功问答实际观察到的关系。结果按物化表、维度和最近观察时间排序，包含
`vectorKey`、`dimensionFieldName`、`canonicalValue`、`aliases`、
`metricFieldId`、`whereCondition`、`compiledCondition`、`sourceType` 与 LLM
审计摘要；向量本体和用户问题原文不会通过该接口返回。

## 语义解析规则

`semantic_parsing_rules` 存放无需向量化的精确语言规则，管理入口为
`/assets/parsing-rules`，页面名称为“问句解析设置”。它只解决四类固定问法：

- 识别指标简称：从正式指标名称中提取用户常说的业务词；
- 识别行政区域：把“北京市”拆成“城市 = 北京”；
- 忽略口语表达：忽略“帮我、请问、看一下”等不改变业务含义的词；
- 拦截宽泛问题：对“经营情况怎么样”等问题先要求用户确认指标。

“客诉量 = 投诉数量”“帝都 = 北京”等业务词义映射必须配置在语义资产中，
不能放入问句解析设置。页面不会向用户暴露 `matchMode`、`action`、优先级和
长度等引擎字段；这些字段由所选业务场景自动生成。

运行时每次请求从平台基础 PostgreSQL 读取设置，保存后无需发版或重启，下一次
智能问答立即生效。

平台规则为只读默认值。当前租户创建相同 `ruleType + pattern` 的规则时优先于
平台规则；停用该租户规则会形成屏蔽项，不会退回平台默认值。重新提交同一规则
可恢复为生效状态。所有写操作保留乐观锁版本和审计记录。
