# 报告草稿、查询、发布与运行时 API

报告设计器入口为 `/report-studio/:reportId`。`/designer/:reportId` 只用于旧链接兼容。

## 草稿与修订

- `POST /api/v1/reports`：创建草稿，需要 `Idempotency-Key`。
- `GET /api/v1/reports/{id}/draft`：读取完整 DSL、Revision、ETag 和能力。
- `PUT /api/v1/reports/{id}/draft`：提交完整结果及有序受限 JSON Patch，正文包含 `expectedRevision` 和语义 `changes`。
- `GET /api/v1/reports/{id}/revisions`：分页读取不可变编辑审计。
- `POST /api/v1/reports/{id}/draft/query-batch`：从服务端当前可信草稿编译卡片查询。

Card DSL 保存操作包括 `REPORT_SETTINGS_UPDATE`、`FILTER_UPDATE`、`CARD_CREATE`、`CARD_DELETE`、`CARD_LAYOUT_UPDATE`、`CARD_CONFIG_UPDATE`、`UNDO` 和 `REDO`。每项修改都必须与操作目标和前后定义精确一致。

Query Batch 请求示例：

```json
{
  "cardIds": ["region-ranking", "monthly-trend"],
  "filters": { "date-range": ["2026-07-01", "2026-08-01"] },
  "interactionContext": {
    "monthly-trend": {
      "sourceCardId": "region-ranking",
      "interactionId": "region-filter-link",
      "value": "华东"
    }
  }
}
```

请求不能上传 SQL、Metric Query AST、目标维度、指标版本或 tenantId。服务端从可信 DSL 推导这些内容。单卡失败进入该卡的 `ERROR` 结果，不中断同批其他卡。

## 校验、发布与回滚

- `POST /api/v1/reports/{id}/validate`，正文 `{ "revision": 12 }`。
- `POST /api/v1/reports/{id}/publish`，需要 `Idempotency-Key`，正文 `{ "revision": 12, "comment": "...", "prewarm": true }`。
- `GET /api/v1/reports/{id}/versions`。
- `POST /api/v1/reports/{id}/versions/{version}/rollback`，需要 `Idempotency-Key`。
- `GET /api/v1/reports/{id}/versions/{version}/manifest`。
- `GET /api/v1/reports/{id}/versions/{version}/definition`。
- `POST /api/v1/reports/{id}/versions/{version}/query-batch`。

发布锁定指定草稿 Revision，重新验证报告权限、指标读取权限和精确发布状态，固定指标版本并生成 `report.status=PUBLISHED` 的规范 JSON。制品先按 SHA-256 内容寻址写入对象存储，再在强制 RLS 的事务中写入不可变版本、卡片/依赖索引、审计与幂等响应，并原子切换 `current_published_version_id`。同一草稿修订不会生成多个版本。

Manifest 返回 `definitionUrl`、`sha256` 和 `sizeBytes`。客户端和服务端均验证发布字节，校验失败时不能回退到可变草稿。回滚只切换当前版本指针，不修改历史版本或 JSON。

## 兼容与限制

- `schemaVersion: 1.0` 仅用于旧 pages/blocks/components 草稿；新建与迁移后保存使用 `1.0.0`。
- 新的 Card DSL 与旧 V1 同时可读，数据库约束允许两者共存。
- 当前查询面已实现 Batch、取消、精确指标版本、L1 TTL 缓存和 Singleflight；Redis L2、公平队列、预热与异步导出未包含在本 API 的完成声明中。
