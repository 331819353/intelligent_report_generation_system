# 数据资产中心 API

Excel/CSV 表资产在 `fileVersionId` 返回当前不可变文件版本；数据集设计器必须将其写入 TABLE 节点，数据库型资产不返回该字段。

所有接口均从访问令牌获取租户范围。读取要求 `DATA_ASSET:READ`，维护要求 `DATA_ASSET:MANAGE`。

## 搜索与详情

- `GET /api/v1/assets/tables`
- `GET /api/v1/assets/tables/{id}`
- `GET /api/v1/assets/tables/{id}/columns`
- `GET /api/v1/assets/catalog`：只返回 `TENANT_PUBLIC` 资产，供租户内只读复用。

列表支持 `q`、`dataSourceId`、`sourceType`、`status`、`sensitivity`、`tag`、`visibility`、`managementStatus`、`enrichedOnly`、`limit` 和 `offset`。搜索同时匹配技术表名、业务名称和业务描述。`managementStatus` 可取 `ENABLED` 或 `DISABLED`；`enrichedOnly=true` 只返回最近一次 LLM 元数据完善成功的表资产。

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

## 表资产生命周期

- `POST /api/v1/assets/tables/{id}/disable`：停用 PostgreSQL 表资产。
- `POST /api/v1/assets/tables/{id}/enable`：恢复 PostgreSQL 表资产。
- `DELETE /api/v1/assets/tables/{id}`：逻辑删除 PostgreSQL 中的表及字段资产。

上述接口只改变平台 PostgreSQL 中的资产记录，绝不对源数据库执行 `ALTER TABLE`、`DROP TABLE` 或数据删除。表资产删除后，源库原表仍会出现在数据源 discovery 清单中，可再次选择导入。被停用的表资产不能用于数据集发布或运行时查询，但不会影响所属数据源的连接状态。

## 差异与影响

- `GET /api/v1/metadata-diffs?dataSourceId={id}&limit=100`
- `GET /api/v1/assets/tables/{id}/impact`

影响接口读取统一依赖表。当前返回已登记的下游数据集、指标或报告；T0301 之后由数据集保存流程自动写入依赖关系。
