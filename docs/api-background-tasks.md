# 后台任务运行中心 API

基础路径：`/api/v1/background-tasks`

该接口提供租户级业务后台任务只读视图和协作式中止控制。目前聚合：

- 数据表元数据完善；
- 数据源连接测试；
- 数据集物化构建；
- 数据集 LLM 标签建议；
- 指标候选提取与发布前准备；
- 维度值刷新与 DWS 维度画像；
- LLM DWD 建模。

低层级向量 outbox、AI 请求审计和交互式查询不会重复显示为独立业务任务。

## 查询任务

`GET /api/v1/background-tasks?view=ACTIVE&limit=100`

`view` 支持：

- `ACTIVE`：`PENDING / QUEUED / RUNNING`，默认值；
- `RECENT`：最近的终态任务；
- `ALL`：全部状态，仍受 `limit` 限制。

`limit` 范围为 1–200。读取只返回当前访问令牌所属租户的数据，不返回样本、凭据、
SQL、模型输入或模型输出。

响应示例：

```json
{
  "items": [
    {
      "id": "8ef830fa-44b2-4eb1-b20a-61ca206aa108",
      "kind": "DATA_SOURCE_METADATA",
      "kindLabel": "数据表元数据完善",
      "name": "订单数据源",
      "description": "takeout · IMPORT · FULL · LLM",
      "status": "RUNNING",
      "sourceStatus": "RUNNING",
      "resourceType": "DATA_SOURCE",
      "resourceId": "1b9d5ce2-8057-46fb-baea-5cad25bf372c",
      "processed": 2,
      "total": 4,
      "progressPercent": 50,
      "progressText": "已处理 2 / 4",
      "attempt": 1,
      "maxAttempts": 3,
      "canCancel": true,
      "createdAt": "2026-07-25T03:55:00Z",
      "startedAt": "2026-07-25T03:56:00Z",
      "updatedAt": "2026-07-25T03:59:00Z"
    }
  ],
  "activeCount": 1,
  "generatedAt": "2026-07-25T04:00:00Z"
}
```

只有存在可靠总量的任务才返回 `progressPercent`。外部 LLM、数据库或向量请求无法
可靠估算剩余工作时，不伪造百分比，前端展示不确定进度。

## 中止任务

`POST /api/v1/background-tasks/{kind}/{id}/cancel`

请求体和查询参数必须为空。操作者必须具备任务所对应对象的 `MANAGE` 权限：

- 数据源任务检查 `DATA_SOURCE:MANAGE`；
- 其他任务检查 `DATASET:MANAGE`。

成功后返回归一化的任务记录，`status` 为 `CANCELLED`。底层历史表若没有原生
`CANCELLED` 状态，会进入其既有安全终态并写入 `USER_CANCELLED`，统一视图再映射
为 `CANCELLED`。

中止采用租约栅栏：

1. 在 PostgreSQL 控制函数中按租户和活动状态原子终结任务；
2. 清除 worker 的租约或令牌；
3. worker 的后续心跳、完成或结果写回因状态/租约不匹配而失败；
4. 已经提交的结果和任务记录保留，不删除业务资产；
5. 写入 `CANCEL_BACKGROUND_TASK` 审计日志。

该操作不直接终止 PostgreSQL 后端进程，也不暴露数据库超级用户能力。

## 错误码

| HTTP | code | 说明 |
| --- | --- | --- |
| 400 | `BACKGROUND_TASK_INVALID_REQUEST` | view、limit、路径或请求体无效 |
| 403 | `PERMISSION_DENIED` | 缺少对象管理权限 |
| 404 | `BACKGROUND_TASK_NOT_FOUND` | 当前租户不存在该任务 |
| 409 | `BACKGROUND_TASK_NOT_ACTIVE` | 任务已经结束或在并发中失去活动状态 |
| 409 | `BACKGROUND_TASK_NOT_CANCELLABLE` | 任务类型不支持安全中止 |
| 500 | `BACKGROUND_TASK_OPERATION_FAILED` | 查询或中止执行失败 |
