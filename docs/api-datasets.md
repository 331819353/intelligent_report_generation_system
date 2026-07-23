# 数据集 DSL API

基础路径：`/api/v1`。所有接口要求 Bearer Access Token，并从服务端身份上下文读取租户；客户端不能覆盖 `tenant_id`。

## 校验并规范化

`POST /datasets/validate`，需要 `DATASET:MANAGE`。

请求：

```json
{ "dsl": { "dslVersion": "1.0" } }
```

成功返回规范 DSL、`dslHash`、逻辑计划和 `planHash`。此接口不写数据库。

## 创建草稿

`POST /datasets`，需要 `DATASET:MANAGE`。

```json
{
  "code": "monthly_orders",
  "name": "月度订单数据集",
  "description": "按月份汇总有效订单金额",
  "type": "SINGLE_SOURCE",
  "dsl": {}
}
```

外层 `code/name/type` 必须与 DSL 的 `dataset` 一致。创建成功返回 `201` 和数据集当前草稿，包含 `version`、`draftVersionId`、`draftVersionNo`、`draftRecordVersion`、规范 DSL 及逻辑计划。存在发布版本时还会返回 `currentPublishedVersionId`；客户端不能用该指针代替需要精确版本 ID 的报告绑定或运行请求。

### 映射表默认数据集

活动表及其全部活动字段完成当前技术结构的 LLM 映射后，系统会在保存映射结果的同一事务中自动创建并发布一张默认数据集。它通过只读的 `originTableId` 标识来源表，并与手工创建的数据集共用草稿、修订、预览和生命周期能力。默认 DSL 只包含一个 `TABLE` 数据节点和一个结束节点：`joins` 与 `preAggregations` 都为空，结束节点的输入直接指向该数据节点，默认输出为当前全部活动字段。

文件映射数据集使用 LLM 生成并通过本地规则校验的英文 `snake_case` 作为字段 `code`，因此查询和预览不会退化为 `field_1`、`field_2`；原始文件表头仍固定保存在 projection 与 `FIELD_REF` 中，中文表头同时作为字段显示名。文件数据集名称优先选择中文表业务名，其次为中文 Sheet 名或中文数据源名。目录不按名称去重，同名数据集按各自 ID 全部返回，并携带来源表及来源数据源名称用于区分。

指标 AI 将映射表数据集作为不可原地改造的来源证据，不会把它作为 `MODIFY_DATASET` 目标；已发布字段足够时可直接使用 `CREATE_ON_DATASET`，需要关联、派生、补字段或改变结构时使用 `CREATE_DATASET` 新建普通数据集。这是指标 AI 编排边界，不取消数据集中心对映射表数据集的原有人工维护能力；人工保存的新草稿仍必须走发布审批。

该创建和初始系统发布过程按 `(tenant_id, origin_table_id)` 幂等，只接受刚创建且从未被编辑、发布或提交审批的精确初始镜像。它是人工审批边界之外的受控系统路径，审计动作会明确记录 `AUTO_PUBLISH_MAPPED_DEFAULT` 和 `publicationSource=SYSTEM_MAPPED_DEFAULT`；公开 `/publish` 接口仍然只能提交人工审批申请。已有镜像不会被重复创建或覆盖，服务启动时会按租户补齐符合条件的存量映射表。映射数据集被软删除后不会无条件复活；只有该来源表在删除后再次成功完成元数据映射，系统才复用原主对象与草稿，保留已废弃历史快照并发布下一不可变版本，审计记录 `AUTO_REGENERATE_MAPPED_DATASET` 与 `publicationSource=SYSTEM_MAPPED_REGENERATE`。显式停用的数据集始终保持人工生命周期状态。

## AI 自动配置 DAG 提案

空画布使用 `POST /datasets/ai/proposals`；修改已有数据集使用 `POST /datasets/{id}/ai/proposals`。两者都需要 `DATASET:MANAGE` 和 `DATA_ASSET:READ`，已有数据集路由还会执行对象级管理权限校验。请求体上限为 128 KiB，`instruction` 为 1–4000 个字符：

```json
{
  "instruction": "在现有客户明细中增加地区字段",
  "current": {
    "dataset": { "name": "客户订单", "description": "当前画布" },
    "nodes": [
      {
        "id": "node_1",
        "tableId": "0f0d21b5-a825-4a8c-9b2f-a67d04cdb28a",
        "alias": "customers",
        "selectedColumns": ["customer_id", "customer_name"]
      }
    ],
    "joins": [],
    "groups": [],
    "transforms": [],
    "end": {
      "name": "最终输出",
      "input": { "kind": "NODE", "id": "node_1" },
      "outputs": [
        { "nodeId": "node_1", "column": "customer_id", "key": "node_1.customer_id", "name": "客户编号", "code": "customer_id" }
      ]
    }
  }
}
```

从零生成时省略 `current`；中途修改时必须传入当前完整画布计划，已有数据集路由缺少 `current` 会失败关闭。服务端只向模型提供当前租户内启用且已完成业务映射的表/字段元数据和这份无 SQL 画布，不发送样例行、查询结果、连接凭据或 SQL。模型只能返回 `INNER`/`LEFT` 关联、受支持的日期粒度与聚合函数，并且必须形成覆盖全部节点的单根 DAG。

资产目录默认使用 `HYBRID` 两阶段召回：对指令生成一次查询向量，把表/字段向量 top 32 与中文关键词 top 32 以 RRF 融合，先选表 top 12，再仅在入选表中排序字段。当前画布引用和请求 hints 不会被召回裁掉；embedding 不可用或覆盖不完整时会回退到包含标签的中文关键词排序，不会因向量故障返回“无资产”。送入模型的表/字段元数据包含标签，但仍继续受 160 字段和 Provider 字节预算限制。

修改采用两阶段 LLM：意图解析器先结合当前 DAG、服务端推导的拓扑角色和受预算约束的授权字段目录，把自然语言解析为 `ADD/UPDATE/REMOVE` 组成的结构化 `changeSet`；无法唯一定位目标、字段用途或分组角色时停止并要求澄清。除组件级操作外，`fieldChanges` 还会锁定物理字段的 `nodeId + tableId + column`、选列动作，以及它在 Join、各级 Group 和 End 中的完整最终使用位置；`FINAL_OUTPUT/INTERNAL_ONLY/SELECTED_ONLY` 由这些最终用途确定性派生。对于 `KEEP` 字段，若意图未显式授权修改某个组件字段，服务端会从 `current` 补齐其既有用途；反之，只有 `fieldChanges` 确认存在真实前后差异的选列、关联条件、分组字段和输出字段才保留对应顶层更新，避免把冗余声明误判为必须制造的图变化。服务端会将已校验的字段最终状态确定性落到选列、分组和输出数组，再执行严格差异校验，因此规划器漏写数组项不会造成假冲突，未授权变化仍会失败关闭。唯一可回溯的转换派生绑定会折算为真实物理血缘；客户名称首尾空格清理在字段、输出分组和路径均唯一时，由服务端锁定 `TEXT_TRIM` 与直接消费者改线。唯一订单实体下的“订单数量”使用订单主标识 `COUNT_DISTINCT`，不因支付/退款事实表中的同名外键要求澄清。FULL、RIGHT 和 CROSS JOIN 不受支持，服务端在模型调用前返回 409，禁止静默替换为 LEFT/INNER。明确只改名称且其他不变时不会联动说明字段。`CLARIFY` 响应中的试探性变更集不会进入规划器或持久化。服务端校验并锁定变更集后，规划器只根据 `current`、锁定的 `changeSet` 和授权资产生成完整候选图，不再重新解释自然语言。服务端随后计算 `current → plan` 的真实组件差异和字段使用清单，要求实际动作、`UPDATE.fields`、输入改线及字段传播闭包与锁定范围完全一致；任何未声明的组件或字段变化、换 ID 重建、漏过中间分组、只修改前半段或顺带重排未涉及字段的方案都会拒绝整份提案。规划器输出不合法时最多执行一次受控纠错，纠错只能修改 `plan`，不能新增、改写或扩大锁定的 `changeSet`。最后仍会用权威资产目录复核表、活动字段、连通性、环路、分支归属、字段类型和输出编码；模型返回后若表结构摘要或启用状态已经变化，整份提案失败关闭。

当前画布中的转换产物还会进入服务端生成的 `editContext.derivedFields` 语义目录。每项包含转换组件类型、转换/产物中英文名称与稳定编码、真实物理字段血缘、精确引用点和直接消费者。用户可以直接说“不再关注某维度”“不要某指标”或“移除清洗后的字段”，唯一语义匹配时无需提供组件 ID；只有同等合理候选无法由上下游角色区分时才返回澄清。`fieldChanges` 仍只能绑定权威资产中的物理字段，现有派生产物 id/code 仅在可由当前图唯一回溯时归一；歧义继续失败关闭。当最终用途证明一个转换的全部产物均无引用时，服务端会补全该转换的 `REMOVE` 和每个直接消费者的旁路 `inputChanges`。MODIFY 规划器响应先校验 Schema 和摘要外壳，再落实这些已经锁定的字段数组、转换删除与改线，之后重新执行完整图校验和精确差异校验；因此不会在确定性补全之前把暂时残留的旧输出误报为非法，也不会跳过补全后的安全检查。

成功响应使用 `Cache-Control: no-store`，只返回待确认提案，不写草稿、不保存、不发布，也不执行查询：

```json
{
  "requestId": "9c66e944-436e-4b58-bfe5-621323bd7d18",
  "proposal": {
    "schemaVersion": "2.3",
    "mode": "MODIFY",
    "summary": "在客户明细中增加地区输出",
    "assumptions": [],
    "warnings": [],
    "changeSet": {
      "operations": [
        {
          "action": "UPDATE",
          "componentKind": "NODE",
          "componentId": "node_1",
          "componentName": "客户明细",
          "fields": ["selectedColumns"],
          "inputChanges": [],
          "description": "增加地区字段投影"
        },
        {
          "action": "UPDATE",
          "componentKind": "END",
          "componentId": "end_1",
          "componentName": "最终输出",
          "fields": ["outputs"],
          "inputChanges": [],
          "description": "增加客户地区输出"
        }
      ],
      "fieldChanges": [
        {
          "field": { "nodeId": "node_1", "tableId": "0f0d21b5-a825-4a8c-9b2f-a67d04cdb28a", "column": "region" },
          "selectionAction": "ADD",
          "purpose": "FINAL_OUTPUT",
          "groupUses": [],
          "joinUses": [],
          "outputUses": [
            { "endId": "end_1", "name": "客户地区", "code": "customer_region" }
          ]
        }
      ]
    },
    "plan": {
      "dataset": { "name": "客户明细", "description": "客户基础属性" },
      "nodes": [
        {
          "id": "node_1",
          "tableId": "0f0d21b5-a825-4a8c-9b2f-a67d04cdb28a",
          "alias": "customers",
          "selectedColumns": ["customer_id", "customer_name", "region"]
        }
      ],
      "joins": [],
      "groups": [],
      "transforms": [],
      "end": {
        "name": "最终输出",
        "input": { "kind": "NODE", "id": "node_1" },
        "outputs": [
          { "nodeId": "node_1", "column": "customer_id", "key": "node_1.customer_id", "name": "客户编号", "code": "customer_id" },
          { "nodeId": "node_1", "column": "customer_name", "key": "node_1.customer_name", "name": "客户名称", "code": "customer_name" },
          { "nodeId": "node_1", "column": "region", "key": "node_1.region", "name": "客户地区", "code": "customer_region" }
        ]
      }
    }
  }
}
```

`ADD` 和 `REMOVE` 的 `fields` 必须为空；`UPDATE` 只列实际改变的顶层字段。`fieldChanges.selectionAction` 使用 `ADD`、`KEEP` 或 `REMOVE`，其中普通的“增加字段”默认表示 `FINAL_OUTPUT`；字段穿过 Group 时必须在 `groupUses` 中逐级声明维度或指标角色，不能只加入上游选列。只有用户明确要求字段暂不参与下游计算或输出时才使用 `SELECTED_ONLY`。新增节点的每个选列也必须携带权威 `tableId`，防止规划阶段换表或夹带字段；整体删除组件时，其内部字段随组件删除，不重复生成字段级变更。切换现有节点的 `tableId` 时，旧表字段以 `REMOVE`、新表字段以 `ADD`（明确表示逻辑延续时也可为 `KEEP`）成对声明；若选列和下游用途的数组结构未改变，只声明 `tableId` 更新，不制造虚假的数组更新。若 `JOIN.left/right`、`GROUP.input` 或 `END.input` 发生变化，同一 `UPDATE` 还必须通过 `inputChanges` 精确声明旧输入和新输入。例如删除直接连接结束节点的分组时，除 `REMOVE GROUP:group_after` 外，还需声明：

```json
{
  "action": "UPDATE",
  "componentKind": "END",
  "componentId": "end_1",
  "componentName": "最终输出",
  "fields": ["input"],
  "inputChanges": [
    {
      "field": "input",
      "from": { "kind": "GROUP", "id": "group_after" },
      "to": { "kind": "JOIN", "id": "join_1" }
    }
  ],
  "description": "删除分组后将结束节点接回原关联结果"
}
```

修改意图存在歧义时不会生成或修复候选图，而是返回 `409 DATASET_AI_CLARIFICATION_REQUIRED`；`message` 是一句可直接展示给用户的问题，原画布保持不变：

```json
{
  "code": "DATASET_AI_CLARIFICATION_REQUIRED",
  "message": "当前有两个关联前分组，请说明要删除哪一个。"
}
```

前端先展示节点、关联字段、假设和告警；用户点击应用后，客户端重新加载活动字段，把完整提案一次性转换为编辑器状态，并调用现有 `/datasets/validate`。只有校验成功且期间画布未继续变化时才原子替换画布；应用仍不会自动保存。应用后可一键撤销，或继续发送修改要求，后续请求始终携带完整当前计划而不是不可审计的自由补丁。即使提案已经应用并保存，得到的也仍是数据集草稿；若要供指标设计使用，必须继续提交发布申请并通过审批。

## 加载草稿

`GET /datasets/{id}`，需要 `DATASET:READ` 或对象级读取权限。跨租户 ID 在 RLS 下按不存在处理。该可变聚合响应携带 `Cache-Control: no-store`；客户端同样必须以 `cache: no-store` 读取，不能把浏览器或代理缓存中的旧 `version` 当作保存、发布后的并发基线。

`GET /datasets?limit=50&offset=0` 返回当前租户的数据集摘要目录，不携带完整 DSL。映射表自动生成的摘要包含 `originTableId`，普通数据集省略该字段；客户端以此展示“映射表数据集”来源标识，不能根据名称、编码或拓扑猜测类型。`limit` 范围为 1–200。

## 更新草稿

`PUT /datasets/{id}/draft`，需要 `DATASET:MANAGE` 或对象级管理权限。

```json
{
  "name": "月度订单数据集",
  "description": "调整后的说明",
  "expectedVersion": 1,
  "dsl": {}
}
```

`expectedVersion` 必须等于最近一次加载结果的 `version`。更新成功后版本加一；冲突返回 `409 DATASET_VERSION_CONFLICT`。`code` 不允许通过草稿更新改变；`type` 是由草稿节点实际引用的数据源数量派生的摘要，增删跨源节点时会随规范 DSL 在 `SINGLE_SOURCE` 与 `CROSS_SOURCE` 之间自动切换。

画布中的“数据节点 → 分组组件 → 关联槽位”使用 DSL 顶层 `preAggregations` 保存，不再退化为 Join 完成后的全局 `groupBy`。每项通过 `nodeId`、`joinId` 和 `joinSide` 固定分组组件的输入节点与下游槽位，`groupBy` 只保存已经由上游产出的维度，`metrics` 保存字段与 `SUM/AVG/COUNT/COUNT_DISTINCT/MIN/MAX`。日期年、年月、年季或年月日必须由独立 `DATE_FORMAT` 转换组件先产出字符串维度，分组组件不再承担日期转换。领域校验要求关联条件只能引用该分组组件实际产出的字段；规范逻辑计划按 `SCAN → TRANSFORM → PRE_AGGREGATE → JOIN` 排序。单源编译器使用分组派生表后再 Join，跨源与文件执行器在内存 Join 前对对应节点分组，因此保存回显与实际预览采用相同拓扑。

创建和每次成功保存都会在同一数据库事务中追加一份不可变草稿修订。保存失败时，当前草稿、派生索引和修订目录全部回滚，不会出现只有部分内容进入历史的状态。

## 草稿历史与回滚

`GET /datasets/{id}/revisions?limit=50&offset=0` 需要 `DATASET:READ` 或对象级读取权限，按产生快照时的数据集聚合版本号倒序返回创建、保存和回滚历史。`versionNo` 对应当时的 `datasets.version`；发布、停用或版本状态操作同样会推进聚合版本但不产生草稿快照，因此编号允许存在间隙。成功响应使用 `Cache-Control: no-store`：

```json
{
  "items": [
    {
      "id": "cb7fa5a2-785d-4999-af04-f04226191ea2",
      "datasetId": "7b1956f7-e7d6-458b-bf5e-fad91a2f191d",
      "versionNo": 8,
      "operationType": "ROLLBACK",
      "sourceRevisionId": "8accbffd-0d56-47b3-a3d4-4c9aa92b9dd2",
      "name": "月度订单数据集",
      "description": "按月份汇总有效订单金额",
      "type": "SINGLE_SOURCE",
      "draftVersionId": "7d84df84-537f-4218-954a-e2258a5e5dd1",
      "draftRecordVersion": 6,
      "dslVersion": "1.0",
      "dslHash": "38fb8de15798d3632b6319f9fc9ad1ae3211de69e43db0c7993947c099168fa3",
      "planHash": "10b20264e4c020fbd04d99dd51c09953fc5908381613d420ab6077d62249c95c",
      "createdAt": "2026-07-19T12:00:00Z",
      "createdBy": "3737515e-4bd0-4faf-b7d4-e740350fcdf5"
    }
  ],
  "total": 8,
  "limit": 50,
  "offset": 0
}
```

`GET /datasets/{id}/revisions/{revisionId}` 返回同一摘要及完整 `dsl`、`logicalPlan`。修订必须同时属于 URL 中的数据集和当前租户；服务端不会根据修订 ID 回退到当前草稿。迁移到草稿历史功能前已经被原地覆盖的旧保存内容无法可信补造，迁移只为每个既有数据集回填当时的当前草稿快照。

`POST /datasets/{id}/revisions/{revisionId}/preview` 需要 `DATASET:READ` 或对象级读取权限，请求和响应沿用普通数据集预览结构。`maxRows` 省略时默认为 5，且不能超过 5；成功响应使用 `Cache-Control: no-store`。服务端精确加载修订中的 DSL、DSL 哈希和计划哈希并在执行前复核完整性，但草稿修订不是发布时的物理依赖快照，因此会使用当前仍有效的资产映射、调用者当前适用的行列策略和当前数据生成样本。资产已停用、字段已撤回或修订摘要不一致时失败关闭，不会改读当前草稿。查询继续使用统一审计、主动取消以及 DSL 超时与 25 秒硬上限中的较小值。

`POST /datasets/{id}/revisions/{revisionId}/rollback` 需要 `DATASET:MANAGE` 或对象级管理权限：

```json
{ "expectedVersion": 7 }
```

服务端在数据集行锁内校验 `expectedVersion`，把精确历史修订的规范 DSL 和逻辑计划复制到唯一当前草稿，重新校验上游资产并重建字段、参数、依赖和资产血缘，随后追加一份新的 `ROLLBACK` 修订并返回新的数据集草稿基线。来源修订、其他历史修订和 `currentPublishedVersionId` 均不会被修改；若需要让恢复内容成为当前发布版本，必须把新的草稿修订提交审批，并在批准时通过正常发布校验后生成新的不可变版本。并发保存、提交/审批或生命周期操作返回 `409 DATASET_VERSION_CONFLICT`，目标修订不存在或父数据集不匹配返回 `404 DATASET_REVISION_NOT_FOUND`。

## 停用与删除

`POST /datasets/{id}/disable`，需要 `DATASET:MANAGE` 或对象级管理权限。请求体携带最近读取的数据集聚合版本：

```json
{ "expectedVersion": 4 }
```

停用会在一个事务中把数据集主状态切换为 `DISABLED`、保存停用前的稳定状态和精确发布指针，并清除活动的 `currentPublishedVersionId`。草稿、不可变发布快照和历史审计都保持不变；精确版本查询还会检查所属数据集不是 `DISABLED`，因此停用后不能通过旧版本标识绕过。成功返回更新后的数据集，聚合版本加一。并发保存、发布或其他生命周期操作导致版本变化时返回 `409 DATASET_VERSION_CONFLICT`；已经停用、正在校验或永久废弃的数据集不能重复停用。

`POST /datasets/{id}/restore` 同样需要 `DATASET:MANAGE` 或对象级管理权限，请求体继续使用最近读取的聚合版本：

```json
{ "expectedVersion": 5 }
```

恢复只接受 `DISABLED` 数据集。服务端在数据集行锁内优先还原停用前的 `PUBLISHED`、`STALE` 或 `DRAFT` 状态；恢复已发布状态时只重新挂接停用时保存的精确 `PUBLISHED` 版本，不反向改写版本快照。迁移前只有审计轨迹能够证明最近一次相关生命周期动作是 `DISABLE` 的旧记录才会进入可恢复状态；没有可靠停用快照的兼容记录按安全约定恢复为可编辑 `DRAFT`，真正的 `DEPRECATED` 数据集不会暴露恢复入口。成功返回更新后的数据集并把聚合版本加一；重复恢复或状态不匹配返回 `409 DATASET_VERSION_TRANSITION_INVALID`。

`DELETE /datasets/{id}` 同样需要管理权限和上述请求体。删除是软删除：目录和普通加载不再返回该数据集，发布版本被废弃，历史版本和审计记录不做物理清除。服务端会先检查该数据集全部精确版本；只要仍被活动指标、下游数据集、报告草稿或运行中查询占用，就返回 `409 DATASET_IN_USE` 且不修改任何状态。

## 发布审批与不可变版本

除映射表来源镜像的受控初始系统发布外，数据集不能由 HTTP 调用方直接发布。普通数据集的公开流程固定为“保存草稿 → 提交发布申请 → 人工审批 → 原子发布”：

- `POST /datasets/{id}/publish-requests` 需要 `DATASET:MANAGE` 全局权限或对象级 `MANAGE` 权限，只提交申请，不运行发布试跑、不创建版本，也不移动发布指针；
- `POST /datasets/{id}/publish` 是兼容旧客户端的提交申请别名，权限和 `202 Accepted` 响应与上一个接口完全相同，不能绕过审批；
- `GET /datasets/{id}/publish-requests?limit=50&offset=0` 需要 `DATASET:READ`，按提交时间倒序返回该数据集的审批记录；`limit` 范围为 1–200；
- `POST /datasets/{id}/publish-requests/{requestId}/approve` 和 `/reject` 需要独立的 `DATASET:PUBLISH` 全局权限或对象级 `PUBLISH` 权限。这里的 `PUBLISH` 表示审批发布，不会由 `MANAGE`、前端按钮状态或对象级 `READ` 隐式授予。

提交请求把申请冻结到最近加载的精确草稿修订；备注可省略，最多 1000 个字符：

```json
{
  "draftVersionId": "7d84df84-537f-4218-954a-e2258a5e5dd1",
  "expectedVersion": 3,
  "expectedDraftRecordVersion": 3,
  "expectedDslHash": "38fb8de15798d3632b6319f9fc9ad1ae3211de69e43db0c7993947c099168fa3",
  "validationParameters": {
    "start_date": "2026-01-01",
    "regions": ["华东", "华南"]
  },
  "note": "订单口径已与业务负责人确认"
}
```

提交成功返回 `202 Accepted`。记录状态为 `PENDING`，并保存 `draftVersionId`、`expectedDatasetVersion`、`expectedDraftRecordVersion`、`expectedDslHash` 和服务端读取到的 `expectedPlanHash`：

```json
{
  "id": "13d5bed9-9026-436c-8840-469ec6b3ff12",
  "datasetId": "7b1956f7-e7d6-458b-bf5e-fad91a2f191d",
  "status": "PENDING",
  "version": 1,
  "draftVersionId": "7d84df84-537f-4218-954a-e2258a5e5dd1",
  "expectedDatasetVersion": 3,
  "expectedDraftRecordVersion": 3,
  "expectedDslHash": "38fb8de15798d3632b6319f9fc9ad1ae3211de69e43db0c7993947c099168fa3",
  "expectedPlanHash": "10b20264e4c020fbd04d99dd51c09953fc5908381613d420ab6077d62249c95c",
  "requesterId": "3737515e-4bd0-4faf-b7d4-e740350fcdf5",
  "requestNote": "订单口径已与业务负责人确认",
  "submittedAt": "2026-07-20T12:00:00Z",
  "updatedAt": "2026-07-20T12:00:00Z"
}
```

同一数据集、草稿版本和草稿记录版本只能形成一条申请，重复提交会返回既有记录；申请事实提交后不可修改或删除。被拒绝的精确草稿若要再次申请，必须先保存为新的草稿修订。`validationParameters` 用于审批通过前的一行最小查询试跑；DSL 中没有默认值的必填参数必须在提交时提供。参数值只保存在服务端审批记录中，不出现在 API 响应、发布版本、通用审计或错误正文，只允许进入受控查询和不可逆参数摘要。

审批通过请求使用申请自身的乐观锁版本，备注可省略且最多 1000 个字符：

```json
{
  "expectedVersion": 1,
  "note": "审批通过"
}
```

拒绝请求必须提供 1–1000 个字符的原因：

```json
{
  "expectedVersion": 1,
  "reason": "销售额字段口径仍需确认"
}
```

审批记录只允许 `PENDING → APPROVED` 或 `PENDING → REJECTED`。并发审批依靠 `expectedVersion` 失败关闭；重复批准已经通过的申请会返回其原有发布版本，重复提交相同拒绝原因会返回既有拒绝结果。审批记录中的草稿身份、哈希、申请人、提交备注和试跑参数在提交后都不可变。

批准时才会重新加载并锁定申请冻结的草稿，重新规范化 DSL，核对数据集/草稿乐观锁、DSL/计划哈希、物理资产、固定文件版本和全部启用的行列策略，要求所有 Join 已人工确认，并复用安全查询运行时执行 `VALIDATION` 试跑。申请提交后草稿或数据集已经变化时返回冲突，不会把申请静默升级到新草稿。跨源试跑返回的基数冲突、多对多和扇出告警会阻止发布；单数据源 MySQL/Oracle 会先为每条等值 Join 执行数据库侧聚合探测，再执行最终一行试跑。探测只返回两侧重复键组数、最大重复度和双侧重复键组数五个统计值，不把 Join 键、SQL、参数或样本带回响应与审计；非等值 Join 返回精确 `joins[i].conditions[j].operator` 路径并失败关闭。试跑或依赖复核失败时返回稳定 `details[].path/code/reason`，申请保持 `PENDING`，不创建半份发布版本，也不移动当前发布指针。

单源探测会应用节点 `sourceFilters` 和可证明仅引用该节点的聚合前过滤，并在同一次 25 秒上限、取消句柄和查询审计生命周期内完成。跨节点聚合前过滤当前不会反向缩小两侧基础键集合，因此风险判断是保守上界；复杂过滤下的精确 Join 后基数语义及大表探测代价门禁已记录到 TODO，不能通过返回业务键或截断样本规避。

批准成功返回 `201 Created`，同时包含已迁移为 `APPROVED` 的申请和新的不可变发布版本：

```json
{
  "request": {
    "id": "13d5bed9-9026-436c-8840-469ec6b3ff12",
    "datasetId": "7b1956f7-e7d6-458b-bf5e-fad91a2f191d",
    "status": "APPROVED",
    "version": 2,
    "draftVersionId": "7d84df84-537f-4218-954a-e2258a5e5dd1",
    "expectedDatasetVersion": 3,
    "expectedDraftRecordVersion": 3,
    "expectedDslHash": "38fb8de15798d3632b6319f9fc9ad1ae3211de69e43db0c7993947c099168fa3",
    "expectedPlanHash": "10b20264e4c020fbd04d99dd51c09953fc5908381613d420ab6077d62249c95c",
    "requesterId": "3737515e-4bd0-4faf-b7d4-e740350fcdf5",
    "requestNote": "订单口径已与业务负责人确认",
    "publishedVersionId": "1725b5d9-6756-429d-94cc-f99f11ed23e1",
    "reviewerId": "3737515e-4bd0-4faf-b7d4-e740350fcdf5",
    "reviewNote": "审批通过",
    "submittedAt": "2026-07-20T12:00:00Z",
    "reviewedAt": "2026-07-20T12:05:00Z",
    "updatedAt": "2026-07-20T12:05:00Z"
  },
  "publishedVersion": {
    "id": "1725b5d9-6756-429d-94cc-f99f11ed23e1",
    "datasetId": "7b1956f7-e7d6-458b-bf5e-fad91a2f191d",
    "datasetRecordVersion": 4,
    "draftVersionId": "7d84df84-537f-4218-954a-e2258a5e5dd1",
    "draftRecordVersion": 3,
    "versionNo": 1,
    "status": "PUBLISHED",
    "dslVersion": "1.0",
    "dslHash": "38fb8de15798d3632b6319f9fc9ad1ae3211de69e43db0c7993947c099168fa3",
    "planHash": "10b20264e4c020fbd04d99dd51c09953fc5908381613d420ab6077d62249c95c",
    "dsl": {},
    "logicalPlan": {},
    "publishedAt": "2026-07-20T12:05:00Z",
    "publishedBy": "3737515e-4bd0-4faf-b7d4-e740350fcdf5"
  }
}
```

批准事务会复制已提交的数据集草稿生成独立 `PUBLISHED` 行，并把申请状态、审批人、不可变版本、`currentPublishedVersionId`、发布审计和内部原子度量事实提取任务原子提交；任一步失败都会整体回滚。审批界面在成功后明确显示“内部原子度量事实提取中”，后台 Worker 随后基于 DAG 和可见字段写入仅供指标创作使用的内部构件；该异步提取不阻塞发布，构件不进入指标中心，也不能直接绑定报表。原草稿仍可继续编辑，首次正式发布版本号为 1。指标定义、指标 AI 的 `CREATE_ON_DATASET` 提案和后续报告绑定只能引用精确 `PUBLISHED` 版本；人工维护版本来自审批，映射表初始镜像可来自受控系统发布。它们都不能绑定 `PENDING` 申请、可变草稿或仅有数据集主对象 ID 的“当前版本”。普通草稿在指标创建流程中可作为 `MODIFY_DATASET` 的 AI 改造目标；映射表结构不足时改用 `CREATE_DATASET` 新建普通数据集，保存后再提交发布审批。

当前审批发布试跑只支持由物理 `TABLE` 节点组成的数据集。DSL 中的 `DATASET` 节点仍可表达精确上游版本引用，但在递归执行、参数传播、循环检测及深度/扇出边界完成前，批准会返回对应 `nodes[i]` 路径和 `PUBLISH_DATASET_NODE_UNSUPPORTED`，失败关闭且不会回退到上游当前草稿或当前发布版本。

## 发布版本目录与精确加载

`GET /datasets/{id}/versions?limit=50&offset=0`，需要 `DATASET:READ` 或对象级读取权限，返回 `PUBLISHED`、`STALE` 和 `DEPRECATED` 发布版本摘要，不包含可变草稿或仅存在于事务内部的发布构建态。`limit` 范围为 1–200。版本目录、精确版本和占用统计的成功响应都使用 `Cache-Control: no-store`，避免状态迁移或发布后继续展示旧并发基线。

```json
{
  "items": [
    {
      "id": "1725b5d9-6756-429d-94cc-f99f11ed23e1",
      "datasetId": "7b1956f7-e7d6-458b-bf5e-fad91a2f191d",
      "versionNo": 1,
      "status": "PUBLISHED",
      "dslVersion": "1.0",
      "dslHash": "38fb8de15798d3632b6319f9fc9ad1ae3211de69e43db0c7993947c099168fa3",
      "planHash": "10b20264e4c020fbd04d99dd51c09953fc5908381613d420ab6077d62249c95c",
      "draftRecordVersion": 3,
      "publishedAt": "2026-07-16T12:00:00Z",
      "publishedBy": "3737515e-4bd0-4faf-b7d4-e740350fcdf5"
    }
  ],
  "total": 1,
  "limit": 50,
  "offset": 0
}
```

`GET /datasets/{id}/versions/{versionId}` 按父数据集和精确版本 ID 返回完整发布快照。版本必须同时属于 URL 中的数据集和当前租户；服务端不会根据 dataset ID 改读 `currentPublishedVersionId`，也不会把不存在、跨数据集或跨租户版本替换成其他版本。

## 发布版本占用统计

`GET /datasets/{id}/versions/{versionId}/usage` 使用与精确版本加载相同的读取权限和父级/租户校验，只返回当前控制库中可见的聚合数量，不返回报告、下游数据集、用户或查询 ID：

```json
{
  "reportDraftReferences": 2,
  "downstreamDraftReferences": 1,
  "downstreamPublishedReferences": 3,
  "activeQueryRuns": 1
}
```

`reportDraftReferences` 按报告去重；下游数据集引用按精确下游版本去重，并把 `DRAFT` 与 `PUBLISHED/STALE/DEPRECATED` 分开；`activeQueryRuns` 只统计仍为 `RUNNING` 的查询。软删除的报告或下游数据集不计入。该响应是界面提示使用的即时快照，不是删除/状态迁移租约；已发布报告版本引用要等 T0601 建立不可变报告依赖表后才能纳入，强占用阻断和租约语义不能由这些计数替代。

## 发布版本状态迁移

`POST /datasets/{id}/versions/{versionId}/status`，需要与发布审批相同的 `DATASET:PUBLISH` 全局权限或对象级 `PUBLISH` 权限。

```json
{
  "expectedVersion": 4,
  "expectedStatus": "PUBLISHED",
  "targetStatus": "STALE"
}
```

`expectedVersion` 是最近加载的数据集主对象 `version`，`expectedStatus` 是精确版本当前状态。只允许 `PUBLISHED -> STALE`、`PUBLISHED -> DEPRECATED` 和 `STALE -> DEPRECATED` 单向迁移；冲突或非法逆向迁移不会修改版本。目标是当前发布版本时，服务端清除 `currentPublishedVersionId` 并把数据集主状态同步为目标状态，表示当前没有可供新绑定使用的发布版本；原精确版本仍可通过版本目录和精确加载查看，其 ID 和不可变内容保持不变。

## 安全数据预览

`POST /datasets/{id}/preview`，需要 `DATASET:READ` 或对象级读取权限。

```json
{
  "queryId": "d7567ac1-dd36-4d16-aac4-65d48d491d74",
  "parameters": { "start_date": "2026-01-01", "regions": ["华东", "华南"] },
  "maxRows": 100
}
```

`queryId` 可省略并由服务端生成；设计器会预先生成 UUID，以便请求执行期间调用取消接口。参数必须在 DSL `parameters` 中声明，服务端会应用必填、默认值、标量/多值和 `STRING/INTEGER/DECIMAL/BOOLEAN/DATE/DATETIME` 类型校验。`maxRows` 不能超过 DSL `executionPolicy.previewLimit`；交互式预览超时取 DSL 配置与 25 秒中的较小值，避免超过 API 写超时。

成功响应：

```json
{
  "queryId": "d7567ac1-dd36-4d16-aac4-65d48d491d74",
  "columns": ["stat_month", "revenue"],
  "rows": [["2026-01-01", 12500.5]],
  "rowCount": 1,
  "durationMs": 18,
  "warnings": [
    {
      "code": "JOIN_CARDINALITY_MISMATCH",
      "message": "声明的 MANY_TO_ONE 基数与预览数据不一致：右侧 Join 键存在重复。",
      "joinId": "orders_customers",
      "estimatedRows": 320
    }
  ]
}
```

MySQL/Oracle 预览仅执行服务端从 DSL 生成的参数化 `SELECT`。物理表和字段来自当前租户的活跃资产白名单，执行前重新加载用户行列策略；Connector 再执行只读词法防护、连接并发限制和多取一行的结果上限检查。

Excel/CSV 预览使用 DSL TABLE 节点固定的 `fileVersionId`，不会回退到文件资产的可变“当前版本”。服务端先验证版本与数据源归属，再从对象存储读取对应版本，复核对象大小和 SHA-256，并用该版本的原始解析配置及真实表头执行过滤、同版本等值 Join、聚合、排序和行列策略。当前文件执行器最多保留 200,000 行中间结果；大文件流式/物化、非等值 Join 和跨源计划由 T0304 承担。

`CROSS_SOURCE` 实时预览支持 MySQL↔Oracle、数据库↔Excel 和 Excel↔Excel 等值 Join。执行器只向各源读取 Join、显式关联前分组、过滤和输出所需字段，可证明只引用单节点的前置过滤会参数化下推；规范化后的数据先执行 `preAggregations`，再在网关完成 Join、最终聚合、排序和行列策略。单个数据集最多 16 个节点，每个源按预览行数放大读取且硬上限为 10,000 行；源返回超过上限时整次预览失败，不使用截断结果。当前不支持非等值 Join、缓存和物化。

当数据库节点在参与的每条 Join 中都是声明基数的“多”侧，且度量仅以直接字段形式参与 `SUM/MIN/MAX/COUNT/AVG` 时，执行器会按所有仍需保留的 Join、过滤和维度字段在源端预聚合，再由网关归并部分结果。COUNT 对部分计数求和并保持整数类型，AVG 使用部分 SUM/COUNT 计算加权平均；空集合分别保持 0/NULL，纯整数求和溢出会失败关闭。出现 `COUNT(*)`、`COUNT_DISTINCT`、复杂聚合参数、`AGGREGATE_ONLY` 最小分组人数、COUNT/AVG 的直接聚合策略校验、度量字段被非聚合复用、ONE 侧节点或文件节点时，会自动回退原始扫描，避免改变行数、去重或权限语义。所有标识符仍来自物理白名单，过滤值和行数限制仍使用参数绑定。

设计期无法判断 Join 基数时允许提交 `cardinality: "UNKNOWN"`。它不阻止草稿创建，但不会触发依赖明确“一侧/多侧”语义的预聚合优化；指标引用穿过该 Join 时按潜在扇出处理并失败关闭。`manualConfirmed` 只表示用户已核对连接方式和关联字段，不再要求用户猜测基数。

跨源预览会对每条 Join 边检查 `manualConfirmed`、声明基数和两侧键重复情况，并返回 `JOIN_CONFIRMATION_REQUIRED`、`JOIN_CARDINALITY_MISMATCH`、`JOIN_MANY_TO_MANY` 或 `JOIN_FANOUT_RISK`。告警仅包含 Join ID 和计数，不包含业务键值。执行器按实际有向 Join 顺序精确传播预览样本的必要键值，任一阶段预计输出超过 200,000 行时会在分配该中间结果前失败；INNER Join 使用较小输入构建哈希索引，外连接不交换声明两侧。

查询主审计写入 `platform.query_runs`，只保存执行计划哈希、参数绑定哈希、状态、耗时、行数和不含业务值的 `warnings_json`；跨源节点另写入 `platform.query_run_sources`，保存来源版本、水位、固定文件版本、不可碰撞的子查询 ID，以及可信网关采集的节点状态、实际输入行数和从读取到规范化完成的耗时。这里的节点行数是 Join 前通过网关形状与上限校验的接收行数，不是最终结果行数；启用源端预聚合时，它表示部分聚合行数而非数据库扫描的明细行数。成功查询必须具备全部节点指标；失败、超时或取消时，未完成节点会跟随主查询终态收口。两张表均不保存 SQL、参数明文、结果样本或源端错误文本，并强制启用租户 RLS。远端 Connector 返回的告警或节点指标不会被信任或写入审计。

## 精确发布版本预览

`POST /datasets/{id}/versions/{versionId}/preview`，需要同一数据集的 `DATASET:READ` 或对象级读取权限，请求与响应结构和草稿预览相同。

服务端只执行 URL 中的精确发布版本。版本状态、所属数据集状态、依赖摘要复核与可信物理计划解析在同一个租户事务和锁定边界内完成，事务外的 SQL 编译与执行只使用已经解析的固定物理引用；所属数据集为 `DISABLED`，版本为 `STALE`、`DEPRECATED`，或发布时固定的物理表结构摘要、文件版本 SHA-256、上游数据集版本/计划摘要已经漂移时，返回 `DATASET_VERSION_UNAVAILABLE`。当前依赖漂移只会拒绝本次执行，不会在请求内自动改写版本状态；基于影响分析幂等传播 `STALE` 仍归入 T0307。此接口不会回退到当前发布指针或当前草稿。查询审计的 `dataset_version_id` 固定为请求中的版本 ID，`run_type` 为 `PREVIEW`。

## 取消预览

`POST /datasets/{id}/query-runs/{queryId}/cancel`，需要同一数据集的读取权限，且仅查询发起用户可取消仍处于 `RUNNING` 的记录。请求体为 `{}`；成功返回：

```json
{ "cancelled": true }
```

数据库查询由 Connector 取消；跨源数据库节点的子查询 ID 已持久化，其他 API 实例也可逐个发送取消。文件查询仍通过原执行实例的上下文取消，当前多副本部署需对同一文件查询使用粘性路由；文件共享取消协调已记录到 T0304。

## 错误码

| HTTP | code | 说明 |
|---|---|---|
| 400 | `INVALID_REQUEST` | 请求不是单一、严格 JSON 文档 |
| 400 | `INVALID_PAGE` | `limit` 不在 1–200、`offset` 为负数或分页参数不是整数 |
| 400 | `DSL-002-INVALID-DOCUMENT` | DSL 无法解析、版本不支持或基本信息不一致 |
| 401 | `ACCESS_TOKEN_REQUIRED` | 缺少有效访问令牌 |
| 403 | `PERMISSION_DENIED` | 无数据集权限 |
| 404 | `DATASET_NOT_FOUND` | 数据集不存在或不属于当前租户 |
| 409 | `DATASET_VERSION_CONFLICT` | 乐观锁版本冲突 |
| 409 | `DATASET_CODE_CONFLICT` | 租户内数据集编码已存在 |
| 404 | `DATASET_PUBLICATION_REQUEST_NOT_FOUND` | 发布审批申请不存在、不属于 URL 中的数据集或不属于当前租户 |
| 409 | `DATASET_PUBLICATION_REQUEST_CONFLICT` | 审批申请乐观锁冲突或冻结事实与待发布计划不一致 |
| 409 | `DATASET_PUBLICATION_REQUEST_NOT_PENDING` | 申请已经通过或拒绝，不能执行当前审批操作 |
| 409 | `QUERY_ID_CONFLICT` | 查询标识已被使用 |
| 422 | `DSL-001-VALIDATION-FAILED` | 领域校验失败，`details` 含 `path/reason` |
| 422 | `DATASET_PUBLISH_VALIDATION_FAILED` | 发布前 DSL、依赖、策略、Join 或查询试跑失败，`details` 含 `path/code/reason` |
| 422 | `QUERY-002-UNSUPPORTED-SOURCE` | 当前节点或数据源尚无安全执行器 |
| 400 | `QUERY-001-INVALID-PREVIEW` | 参数、行数限制或可执行表达式无效 |
| 404 | `DATASET_VERSION_NOT_FOUND` | 精确版本不存在、不属于 URL 中的数据集或不属于当前租户 |
| 404 | `DATASET_REVISION_NOT_FOUND` | 精确草稿修订不存在、不属于 URL 中的数据集或不属于当前租户 |
| 400 | `DATASET_AI_REQUEST_INVALID` | AI 指令或当前画布不是严格有效的单一 JSON 文档 |
| 403 | `AI_TENANT_FORBIDDEN` | 当前租户未启用数据集 DAG AI 用途 |
| 409 | `DATASET_AI_CURRENT_REQUIRED` | 修改已有数据集时缺少可信的当前画布基线 |
| 409 | `DATASET_AI_NO_ASSETS` | 没有可用于建模的已映射启用表 |
| 409 | `DATASET_AI_CONTEXT_STALE` | 生成期间资产结构或启用状态发生变化 |
| 409 | `DATASET_AI_CLARIFICATION_REQUIRED` | 修改意图存在歧义；返回澄清问题且不生成候选图 |
| 429 | `AI_QUOTA_EXCEEDED` | 当前租户日/月 AI 配额不足 |
| 502 | `DATASET_AI_INVALID_OUTPUT` | 模型提案在受控纠错后仍未通过领域校验 |
| 502 | `AI_COMPLETION_FAILED` | Provider 调用或编排失败，原画布保持不变 |
| 503 | `AI_PROVIDER_UNAVAILABLE` | 模型 Provider 未配置或暂时不可用 |
| 504 | `AI_TIMEOUT` | 模型调用超时，原画布保持不变 |
| 409 | `DATASET_VERSION_UNAVAILABLE` | 精确版本不是可执行的 PUBLISHED 状态或发布依赖已经漂移 |
| 409 | `DATASET_VERSION_TRANSITION_INVALID` | 发布版本状态迁移不在允许的单向状态机内 |
| 409 | `DATASET_IN_USE` | 数据集仍被指标、下游数据集、报告草稿或运行中查询占用，不能删除 |
| 404 | `QUERY_RUN_NOT_FOUND` | 查询不存在、已结束或当前用户无权取消 |
| 502 | `QUERY-004-EXECUTION-FAILED` | 数据源执行失败，内部错误不透出 |
| 504 | `QUERY-003-TIMEOUT` | 查询超时且已发起取消 |
| 503 | `DATASET_PUBLISH_UNAVAILABLE` | 发布校验执行器尚未装配，未创建发布版本 |
| 500 | `DATASET_PERSISTENCE_FAILED` | 数据集存储暂时不可用，响应不暴露内部错误 |

除数据集 AI 提案的 128 KiB 特例外，请求体上限为 2 MiB。错误响应不会返回 SQL、数据库内部错误或源端敏感信息。
