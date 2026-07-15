# 数据资产中心 API

所有接口均从访问令牌获取租户范围。读取要求 `DATA_ASSET:READ`，维护要求 `DATA_ASSET:MANAGE`。

## 搜索与详情

- `GET /api/v1/assets/tables`
- `GET /api/v1/assets/tables/{id}`
- `GET /api/v1/assets/tables/{id}/columns`
- `GET /api/v1/assets/catalog`：只返回 `TENANT_PUBLIC` 资产，供租户内只读复用。

列表支持 `q`、`dataSourceId`、`sourceType`、`status`、`sensitivity`、`tag`、`visibility`、`limit` 和 `offset`。搜索同时匹配技术表名、业务名称和业务描述。

## 业务元数据

- `PUT /api/v1/assets/tables/{id}/business-metadata`
- `PUT /api/v1/assets/columns/{id}/business-metadata`

表资产请求示例：

```json
{
  "businessName": "订单事实表",
  "businessDescription": "订单交易明细",
  "tags": ["订单", "核心"],
  "sensitivityLevel": "CONFIDENTIAL",
  "visibility": "TENANT_PUBLIC",
  "manualLocked": true,
  "expectedVersion": 1
}
```

字段资产还可以传入 `semanticType`，不需要 `visibility`。敏感级别可取 `PUBLIC`、`INTERNAL`、`CONFIDENTIAL`、`RESTRICTED`。

`expectedVersion` 是业务元数据的乐观锁版本。版本过期返回 409，防止多人编辑互相覆盖。业务字段与源技术字段分开保存，数据库或 Excel 再同步不会覆盖人工维护结果。开启 `manualLocked` 后，后续 AI 补全不得修改该资产。

所有业务元数据修改均写入不可变审计日志。

## 差异与影响

- `GET /api/v1/metadata-diffs?dataSourceId={id}&limit=100`
- `GET /api/v1/assets/tables/{id}/impact`

影响接口读取统一依赖表。当前返回已登记的下游数据集、指标或报告；T0301 之后由数据集保存流程自动写入依赖关系。
