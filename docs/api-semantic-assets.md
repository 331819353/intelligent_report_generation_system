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
`/assets/parsing-rules`。当前支持指标名称后缀、行政区划后缀、问句剩余词和
宽泛指标问法四类规则。运行时每次请求从平台基础 PostgreSQL 读取，保存后无需
发版或重启，下一次智能问答立即生效。

平台规则为只读默认值。当前租户创建相同 `ruleType + pattern` 的规则时优先于
平台规则；停用该租户规则会形成屏蔽项，不会退回平台默认值。重新提交同一规则
可恢复为生效状态。所有写操作保留乐观锁版本和审计记录。
