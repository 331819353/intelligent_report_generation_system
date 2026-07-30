# 后台任务运行中心 API

基础路径：`/api/v1/background-tasks`

该接口提供租户级业务后台任务视图、协作式中止与安全重试控制。目前聚合：

- 数据表元数据完善；
- 数据源连接测试；
- 数据集物化构建；
- 数据集 LLM 标签建议；
- 指标候选提取与发布前准备；
- 人工确认后的维度值刷新；
- 领域分类、维度建模、事实落地三个 LLM 建模任务；
- 人工触发的主题分组 DWS 草稿建模。

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
      "canRetry": false,
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

数据集中心的“维度建模”入口复用本接口每 3 秒同步一次任务状态。按钮在提交、
排队和运行期间保持不可重复点击：存在可靠百分比时按真实值填充按钮背景，否则显示
不确定进度动画。悬停、键盘聚焦或点击日志图标会展示由任务时间戳、阶段、进度和错误
组成的实时安全摘要；摘要不包含模型输入、原始输出、SQL 或业务数据。页面刷新后也会
从当前活动任务恢复按钮运行态，客户端禁用之外仍由服务端活动任务约束保证幂等。

DIM/DWD 建模只按当前用户所属业务领域创建一个父流程和三个可分别重试的持久任务：
`ODS_DOMAIN_CLASSIFICATION`（领域分类）、`DIM_MODELING`（维度建模）和
`DWD_FACT_MODELING`（事实落地）。三者严格串行依赖，但租约、错误和尝试次数互相
隔离；后续任务只读取前序任务的结构化检查点，不继承对话上下文。领域分类结果记录
事实表、维度产物和其他表数量；一张 ODS 可贡献零至两个不同实体 DIM，FACT ODS
还会取得“事实兼维度”建议标签，因此前两项计数不互斥。领域分类阶段为每个精确 ODS
版本创建一次独立 LLM 调用，最多四路并发，逐 ODS 保存
`warehouse-classification-v7` 检查点，再由服务端合并成领域分类结果。
分类合同会把带一次性过程键、事件时间、状态或参与方外键的无度量事件流水确定性
纠正为 FACT；订单号、事件 ID、支付号等事实发生键不能进入 DIM 键或属性。
维度建模按单个维度产物的小上下文最多四路并发，先把全部候选保存为未发布 DIM
草稿；随后全量校验表名，仅对重名组读取字段和粒度做二次 LLM 裁决。兼容候选只保留
一张字段更完整、来源更权威的单 ODS DIM，不跨 ODS 关联或合入字段；不同实体/粒度
则保留并改为唯一名称。名称扫描和每个
重名组分别保存检查点，重试不会重复设计已完成的 DIM。事实落地按单张非纯维 ODS
最多四路并发；第一轮把其他事实每六张分批识别业务关联和主次，只保留当前表为主表
的关系，第二轮再汇总这些关系与全部已校验 DIM 合同生成 DWD 草稿。
DIM 尚未发布时，事实落地任务以
`PARTIAL / DIM_PUBLICATION_REQUIRED` 结束，不继续占用运行中队列；全部 DIM
发布后由发布指针事件自动恢复，重绑定精确 DIM 发布版本并完成。

“明细建模”按钮每次提交都会为各领域创建新的父批次和新的三阶段任务 ID；同一
精确 ODS 版本已经存在活动批次时不会重复入队。任务详情中的“重试”则保留原任务
ID、检查点和自动产物所有权，只恢复失败或中止的原批次。两种入口不会再共同重置
同一条任务记录。
父流程仍按领域保持一个稳定身份，逐 ODS 分类分支作为阶段内部小任务并发执行，不创建
会争抢流程所有权的兄弟父任务。进程中断后会复用同一 ODS 快照下已经完成的逐表、
逐 DIM 和逐 FACT 检查点，只补缺失分支；每次新模型调用前都会续租。检查点数量属于
任务内部恢复状态，不能伪装成尚不可可靠估算的百分比，也不会在该 API 返回模型输入、
原始响应或业务数据。

DWS 建模遍历当前领域全部已发布 DWD，为每个精确 DWD 版本创建一个独立任务；每项
任务只包含当前 DWD，并携带同领域全部 DIM 元信息作为规划上下文。默认单 worker 最多
四路并发执行。当前精确 DWD 发布版本尚无同版本 ACTIVE 物化时显示为排队状态，并
展示“等待 DWD 发布版本完成物化；物化转为可用后，主题建模会自动继续”；底层
`WAITING_DEPENDENCY / WAITING_ACTIVE_DWD_MATERIALIZATION` 不作为错误返回，也不消耗失败预算。任务只生成可评审草稿，不发布、不激活物化、
不生成 ADS，也不自动创建指标或维度审批项。默认主题建模只选择单事实分析模板；
`MULTI_FACT_COMPARISON` 保留给显式跨事实任务，不混入逐 DWD 默认入口。每个
分析模板独立记录 `CREATED / UPDATED / UNCHANGED /
MANUAL_OWNED / SKIPPED`，因此一个模板不适用不会让其他安全结果重做。该任务当前
没有对外安全中止协议，`canCancel=false`。

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
2. 对三阶段建模任务，中止其中任一项会同时终结同一领域流程内所有尚未完成的
   阶段，避免上游继续写回或下游永久等待；
3. 清除 worker 的租约或令牌；
4. worker 的后续心跳、完成或结果写回因状态/租约不匹配而失败；
5. 已经提交的结果和任务记录保留，不删除业务资产；
6. 写入 `CANCEL_BACKGROUND_TASK` 审计日志。

该操作不直接终止 PostgreSQL 后端进程，也不暴露数据库超级用户能力。

## 重试任务

`POST /api/v1/background-tasks/{kind}/{id}/retry`

请求体和查询参数必须为空，权限检查与中止相同。当前仅对具有检查点或确定性幂等
恢复协议的 `ODS_DOMAIN_CLASSIFICATION`、`DIM_MODELING`、`DWD_FACT_MODELING`
和 `DWS_MODELING` 终态任务开放。失败、部分完成、已中止或普通跳过任务返回
`canRetry=true`；成功任务、运行中任务和同领域合并记录不允许重试。重试前两项
建模任务会自动把其后的阶段重新排队，防止下游继续使用已失效的结构化结果。

重试会保留已经验证的检查点与自动产物映射，清除旧租约、错误和尝试次数，并把原
任务重新置为待处理。本次操作写入 `RETRY_BACKGROUND_TASK` 审计，不向 API 角色
授予 worker 表的直接更新权限。

## 错误码

| HTTP | code | 说明 |
| --- | --- | --- |
| 400 | `BACKGROUND_TASK_INVALID_REQUEST` | view、limit、路径或请求体无效 |
| 403 | `PERMISSION_DENIED` | 缺少对象管理权限 |
| 404 | `BACKGROUND_TASK_NOT_FOUND` | 当前租户不存在该任务 |
| 409 | `BACKGROUND_TASK_NOT_ACTIVE` | 任务已经结束或在并发中失去活动状态 |
| 409 | `BACKGROUND_TASK_NOT_CANCELLABLE` | 任务类型不支持安全中止 |
| 409 | `BACKGROUND_TASK_NOT_RETRYABLE` | 任务状态或类型不支持安全重试 |
| 500 | `BACKGROUND_TASK_OPERATION_FAILED` | 查询或中止执行失败 |
