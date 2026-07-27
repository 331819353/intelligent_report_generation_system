# 数据集 DSL API

基础路径：`/api/v1`。所有接口要求 Bearer Access Token，并从服务端身份上下文读取租户；客户端不能覆盖 `tenant_id`。

## 人工触发智能建模

以下接口需要租户级 `DATASET:MANAGE`，不接受请求体或查询参数，成功返回 `202 Accepted`：

- `POST /datasets/trigger-dim-dwd-modeling`：以当前已发布 ODS 版本为输入，提交 DIM/DWD 分层建模任务。
- `POST /datasets/trigger-dws-modeling`：遍历当前全部已发布 DWD，并携带全部当前 DIM 元信息，按实际分析主题提交分组式 DWS 规划任务。

DIM/DWD 与 DWS 建模不随发布自动启动，只能通过以上入口人工提交。每个入口通过租户隔离的 `SECURITY DEFINER` 函数写入 durable outbox，API 角色不能直接修改 worker 表。worker 异步生成可评审草稿，不直接发布数据集。明细建模每次人工点击创建新的三阶段批次；任务中心的重试才恢复原批次与有效检查点。主题建模按 `主题分组 + 当前全部输入摘要` 保持任务身份：同一分组的 DWD/DIM 版本或结构发生变化会形成新的范围摘要，终态任务再次人工提交时可重新入队，活动任务不会重复提交。

响应包含 `eligibleCount`、`enqueuedCount`、`existingCount`、`blockedCount`，以及可选 `blockedReason`。主题建模没有已发布 DWD、但存在 DWD 草稿时返回 `blockedReason=DWD_PUBLICATION_REQUIRED`；页面会提示先提交发布并完成审批，不再显示笼统的“不可用”。触发函数先读取全部当前 DWD，依据受治理的使用范围标签形成主题成员关系；一张 DWD 可以参与多个适用主题，没有范围标签时进入综合分析分组。每个分组的 LLM 上下文同时包含该组全部 DWD 的字段、事实合同与全部当前 DIM 的字段元信息，允许按共同粒度形成多事实汇总，不建立 DWD 与 DWS 的一对一约束。若任一精确 DWD 物化尚未变为 `ACTIVE`，worker 会进入依赖等待而不是制造失败草稿。

非 ODS 数据集（DIM、DWD、DWS、ADS）在当前建模草稿生成后即自动登记 LLM 标签建议任务，发布形成新的不可变版本时再自动登记该版本，不提供人工触发入口。worker 只能从租户内 `ACTIVE / CONTROLLED` 词表选择标签，并仅生成 `SUGGESTED` 绑定，不自动批准；页面展示当前发布版本的建议标签，没有发布版本时展示当前草稿标签。任务身份包含精确版本、prompt 版本和 schema hash，同一草稿继续编辑时不会复活或改写已终结的审计任务。

### 人工识别指标与维度

`POST /metric-candidates/identify` 需要全局 `METRIC:MANAGE` 和 `DATASET:READ`，由资产管理中心“自动识别指标与维度”按钮人工提交。DWS/ADS 发布、审批和物化不再自动登记指标候选、维度候选或字段画像任务，迁移前遗留的 DWS/ADS 自动候选也不会继续出现在审批清单。

人工识别先统计并比对现有正式指标和历史候选，只遍历当前发布的 DWS/ADS，
不再用 DWD 作为回退。每个精确版本登记一项
`metric-candidate-manual-v1` 任务；同一按钮还为当前 ACTIVE DWS 补建维度候选
和有界字段画像。对同一物化重复点击会复用已成功画像，只重放已失败画像，不会
再次把已发布的 `FULL` 成员索引收紧为 `NONE`。

接口返回覆盖数据集、提交任务、历史指标/候选、维度画像数量，以及按 DWS/ADS
分组的领域、指标清单和维度清单。维度候选审批为 `PUBLISHED` 后，低基数、
非敏感且采用 `FULL` 策略的去重成员值会异步刷新并独立向量化；再次读取清单会
返回成员总数、已向量化数量和最多 200 个成员标签。高基数维度只做精确匹配，
敏感维度不建立成员向量。指标候选和维度候选仍需人工审核，不会由模型直接转成
正式资产。

## 校验并规范化

`POST /datasets/validate`，需要 `DATASET:MANAGE`。

请求：

```json
{ "dsl": { "dslVersion": "1.0" } }
```

成功返回规范 DSL、`dslHash`、逻辑计划和 `planHash`。`dataset.layer` 支持 `ODS/DIM/DWD/DWS/ADS`；历史请求省略时由服务端根据 DAG 确定性推断。此接口不写数据库。

`dataset.domain` 与 `dataset.subject` 可选，分别保存最长 128 个字符的业务领域和主题。它们会清理首尾空白并进入规范 DSL、草稿修订和不可变发布版本；修改分类会改变 `dslHash`，但不会改变只描述执行逻辑的 `planHash`。

## 创建草稿

`POST /datasets`，需要 `DATASET:MANAGE`。

```json
{
  "code": "monthly_orders",
  "name": "月度订单数据集",
  "description": "按月份汇总有效订单金额",
  "type": "SINGLE_SOURCE",
  "layer": "DWS",
  "dsl": {}
}
```

外层 `code/name/type` 必须与 DSL 的 `dataset` 一致；外层 `layer` 可省略，提供时也必须与 DSL 规范化后的层级一致。`layer` 会直接出现在数据集响应中。创建成功返回 `201` 和数据集当前草稿，包含 `version`、`draftVersionId`、`draftVersionNo`、`draftRecordVersion`、规范 DSL 及逻辑计划。存在发布版本时还会返回 `currentPublishedVersionId`；客户端不能用该指针代替需要精确版本 ID 的报告绑定或运行请求。

新建数据集画布左侧的资产树按 ODS、DIM、DWD、DWS 四组展示可作为上游的当前精确发布版本；ODS 组内同时保留尚未形成 ODS 发布版本的原始映射表，并按物理数据源继续分组。保存弹窗仍展示 ODS、DIM、DWD、DWS、ADS 五个目标层级，并根据当前画布的精确上游血缘禁用不符合方向合同的层级。单个物理数据集仍只保存一个 `dataset.layer`；需要不同物理落层时应建立不同数据集和明确血缘，不能让同一版本同时承担多个执行层级。

### 映射表默认数据集

活动表及其全部活动字段完成当前技术结构的 LLM 映射后，系统会在保存映射结果的同一事务中自动创建并发布一张 `ODS` 默认数据集。LLM 输出按目标增量保存：已经通过校验的表或字段不会在下一轮重复请求，后续重试只补剩余目标。三轮增量重试后仍不完整时，系统也会用物理表结构和已应用结果创建一张可编辑的 `DRAFT / ODS`，但不会自动发布或物化；用户可直接进入数据集中心补充，或者通过表资产“手工完善”完成业务元数据。

默认数据集通过只读的 `originTableId` 标识来源表，并与手工创建的数据集共用草稿、修订、预览和生命周期能力。默认 DSL 只包含一个 `TABLE` 数据节点和一个结束节点：`joins` 与 `preAggregations` 都为空，结束节点的输入直接指向该数据节点，默认输出为当前全部活动字段。该系统生成的 DSL 固定使用 `MATERIALIZED_PREFERRED`，并声明 `materialization.enabled=true`、`refreshMode=ON_DEMAND`；普通人工设计的数据集默认仍关闭物化，系统不会批量替它们开启。

文件映射数据集使用 LLM 生成并通过本地规则校验的英文 `snake_case` 作为字段 `code`，因此查询和预览不会退化为 `field_1`、`field_2`；原始文件表头仍固定保存在 projection 与 `FIELD_REF` 中，中文表头同时作为字段显示名。文件数据集名称优先选择中文表业务名，其次为中文 Sheet 名或中文数据源名。目录不按名称去重，同名数据集按各自 ID 全部返回，并携带来源表及来源数据源名称用于区分。

指标 AI 将映射表数据集作为不可原地改造的来源证据，不会把它作为 `MODIFY_DATASET` 目标；已发布字段足够时可直接使用 `CREATE_ON_DATASET`，需要关联、派生、补字段或改变结构时使用 `CREATE_DATASET` 新建普通数据集。这是指标 AI 编排边界，不取消数据集中心对映射表数据集的原有人工维护能力；人工保存的新草稿仍必须走发布审批。

该创建和初始系统发布过程按 `(tenant_id, origin_table_id)` 幂等。降级草稿只有在不存在人工保存/回滚、任何发布申请和发布历史时，才允许由后续 LLM 部分结果刷新；全部目标补齐后，同一系统持有草稿可升级为默认发布。它是人工审批边界之外的受控系统路径，审计动作会明确记录 `AUTO_PUBLISH_MAPPED_DEFAULT` 和 `publicationSource=SYSTEM_MAPPED_DEFAULT`；公开 `/publish` 接口仍然只能提交人工审批申请。发布指针与首个 `QUEUED` 物化 build 在同一租户事务内提交：build 固定刚发布的精确版本、当前数据源发布版本、元数据版本及结构摘要，事务内不访问源库也不执行构建。任一控制面登记失败会使元数据映射、数据集发布和 build 一起回滚。服务启动时会按租户补齐所有活动物理表的缺失 ODS 草稿，再为已经完整映射的表补齐系统发布和缺失 build；确定性幂等键保证 API/worker 并发对账不会为同一来源表创建平行对象。

已发布映射表不会因元数据变化而无条件刷新。`dataset_versions.publication_origin` 是不可变发布事实；运行时仅接受三类 `SYSTEM_MAPPED_*` 来源，审计日志只用于追踪，追加伪造的 `AUTO_*` 审计不能取得刷新资格。当前草稿的 `id + recordVersion + schemaHash + planHash` 还必须与该系统发布版本冻结的来源草稿完全一致，并且没有 `PENDING` 发布申请，系统才可覆盖草稿并产生下一系统发布及 build。任一条件不满足都会安全跳过，草稿、发布指针、build 和审计均保持不变。软删除重建执行同一规则。该防线面向正常应用运行和可追加审计伪造；持有 API/worker 数据库角色本身仍属于可信边界，若要抵御运行角色失陷，还需进一步拆分版本事实写入角色或使用受限 `SECURITY DEFINER` 提交函数。

### LLM 自动 DWD 设计

人工提交明细建模后，每个领域会创建三个独立且串行依赖的持久任务：`领域分类 → 维度建模 → 事实落地`。每次点击“明细建模”都创建新的父批次和三个新的阶段任务；同一精确 ODS 版本只允许一个活动批次，防止重复点击并发入队。任务中心的“重试”才恢复原阶段、原父批次及其有效检查点，不会伪造新的业务批次。每个任务独立保存状态、租约、尝试次数和重试边界，后续任务只读取同一批次前序任务已经通过本地合同校验的结构化检查点，不携带前一任务的 LLM 对话上下文。第一项任务按领域归集当前已发布 ODS，LLM 统一分析表结构；每张表分类为表示主要行粒度的 FACT、DIMENSION、MASTER、OTHER，同时可以为 FACT 声明可抽取实体的稳定业务键与说明属性。因此同一 ODS 可以同时产生 DWD 和 DIM，事实表与维度产物计数允许重叠。第二项任务以单个维度产物为调用粒度，既处理独立 DIMENSION/MASTER ODS，也处理 FACT 中声明的实体投影；它补齐维度名称、表说明、字段名称和字段说明，给出结构化字段值标准化方案。本地编译器只保留实体键与稳定属性，应用字符串修剪、标准空值以及日期时间转换，并通过 `DISTINCT` 按实体投影去重，逐 DIM 保存设计检查点。第三项事实落地先读取已校验的 DIM 草稿合同，立即设计并保存 DWD 草稿；如果 DIM 尚未发布，任务以 `PARTIAL / DIM_PUBLICATION_REQUIRED` 结束，不继续占用运行中队列。全部 DIM 人工发布后，发布指针事件会自动恢复事实阶段，把 DWD 依赖重绑定到精确发布版本并完成。普通人工保存和发布仍拒绝草稿依赖，因此该连续生成能力不会绕过发布治理。

每轮先读取历史生成映射、既有 DWD 草稿节点和最近领域分类快照，按稳定 ODS 数据集身份比较精确版本：原组成事实 ODS 与已使用 DIM 版本均未变化的 DWD 直接记为 `UNCHANGED`，不调用事实设计也不改草稿；事实或已关联 DIM 变化时只更新受影响模型；新 FACT 补建新 DWD；新 DIMENSION/MASTER 会触发现有事实增量复核。DIM 草稿发布为相同字段合同时会复用事实设计检查点，只更新 DWD 的精确依赖版本，不重复调用 LLM；DIM 字段合同发生变化时才重新设计。Worker 中断、取消、租约回收或人工重试后只续跑缺失阶段。

DWD 设计决策以 LLM 结果为准，代码执行失败关闭的安全校验和 DSL 编译：每张输入表必须恰好分类一次；分类按真实行粒度而不是表名判断。订单商品/订单行项目若一行代表一个订单中的一个商品项，并含数量、单价、折扣、行金额等交易度量，其主要角色仍必须是 `FACT` 并落地 DWD；如果同一行还包含稳定 `SKU_ID` 以及商品名称、品牌、分类等稳定说明属性，则分类可同时声明商品实体投影，第二步从这些字段抽取并去重形成商品 DIM。订单号、事实行号、数量、成交价、折扣、金额和事件时间禁止进入该 DIM；没有稳定实体键和说明属性时不得虚构商品维度。独立商品目录仍按 `DIMENSION/MASTER` 处理。纯维度节点固定引用输入中的精确 ODS `PUBLISHED datasetVersionId`；从事实表抽取的 DIM 也保留精确 ODS 血缘。DWD 的事实节点固定引用事实 ODS；草稿阶段的维度节点可受控引用第二步产出的精确 DIM 草稿版本，发布前必须自动重绑定到精确 DIM `PUBLISHED datasetVersionId`。事实到 DIM 的 `LEFT JOIN` 除类型兼容外，关联字段 code 还必须忽略大小写后完全同名，禁止仅凭 INTEGER/STRING 等相同类型连接不同业务键。无法证明的维度关联及其扩充字段会被安全移除；能够由治理元数据唯一证明的事实全字段投影、同名业务键关联、维度说明字段以及粒度/时间输出编码则由编译边界确定性补齐，并把引用规范为输出字段的精确大小写。单个 FACT 最终仍不合法时只跳过该事实，不阻断同领域其他事实完成。系统生成 DIM/DWD 技术编码采用 `层级_领域_主题_业务名称` 的 ASCII 物理命名，例如 `dim_enterprise_enterprise_profile_customer`、`dim_operations_business_analysis_sku`、`dwd_operations_business_analysis_order_item`；卡片标题仍使用普通业务名称。物理名不可用时才从受治理实体键推导，无法得到可维护业务编码则跳过，不生成 UUID 风格编码。既有模型的来源未变化但编码规则已不合规时只执行一次身份修复；其余来源未变化的模型仍保持 `UNCHANGED`。DIM 编译器补齐说明信息与平台强制且无歧义的 `TRIM`、类型对应空值补位及原生 `DATE/DATETIME` 显式转换；DWD 编译器继续校验 LLM 的字段处理，丢弃无法通过类型和血缘校验的可选步骤。度量空值和时间空值不允许擅自补默认值。

三个任务分别使用租约、最多三次任务级重试和不可变 AI 请求审计。人工提交阶段按领域选择一个代表 ODS，每个领域创建一个父流程和三项阶段任务；领域分类仍统一覆盖该领域全部 ODS，不再先创建逐表兄弟任务后产生 `DOMAIN_PLAN_COALESCED`。并发或迁移前遗留的合并记录只作为正常跳过展示。同一租户的领取决策由 PostgreSQL 事务级锁串行化；维度设计和事实设计内部的独立小任务最多四路并发，并持续续租。重试某个失败阶段会保留已经验证的检查点，并把其后的阶段重新排队。单次结果不满足 JSON Schema 或本地安全合同，会收集精确字段/输出缺口，并在当前小任务内最多追加两次定向修复；仍不合格时任务以 `PARTIAL / SOME_LAYER_DESIGNS_SKIPPED` 保存其他有效结果，任务详情携带具体失败原因，不再用一个 `WAREHOUSE_MODELING_INVALID_OUTPUT` 关闭整个领域。

LLM 方案只创建或更新 `DRAFT / DWD`，不会绕过人工发布审批。系统维护 `(fact ODS version -> DWD dataset)` 幂等映射，并保存最近一次自动生成的草稿摘要；人工编辑过的 DWD 草稿与该摘要不一致时，后台任务返回 `MANUAL_DRAFT_CHANGED`，不会覆盖人工内容。DWD 经正常审批发布后，才由既有 PostgreSQL 物化链路在独立仓库实例中形成可查询数据表。

DWD 草稿和发布版本都记录对精确 ODS 版本的依赖。删除 ODS 时，应用层会先检查下游依赖，数据库还通过约束触发器阻止绕过服务的软删除；只要引用它的下游 DWD 数据集尚未删除，包括历史废弃版本在内的血缘都会返回 `409 DATASET_IN_USE`。必须先删除下游 DWD 数据集，不能仅靠废弃某个 DWD 版本绕过。删除 ODS 记录本身也不会删除外部数据源中的物理表。

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

`GET /datasets?limit=50&offset=0` 返回当前租户的数据集摘要目录，不携带完整 DSL。摘要明确返回 `layer`（`ODS`、`DIM`、`DWD`、`DWS`、`ADS`）与 `tags`；`tags` 优先取当前发布版本已经建议或批准的语义标签，自动 ODS 数据集的标签任务尚未完成时回退到来源表的 LLM 标签。映射表自动生成的摘要仍包含 `originTableId` 供服务端编排与来源追踪，普通数据集省略该字段；客户端直接展示 `layer` 和 `tags`，不再以“映射表数据集”作为用户可见层级。`limit` 范围为 1–200。

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

`expectedVersion` 必须等于最近一次加载结果的 `version`。更新成功后版本加一；冲突返回 `409 DATASET_VERSION_CONFLICT`。`code` 不允许通过草稿更新改变；`type` 是由草稿节点实际引用的数据源数量派生的摘要，增删跨源节点时会随规范 DSL 在 `SINGLE_SOURCE` 与 `CROSS_SOURCE` 之间自动切换。`layer` 与草稿版本同步保存：ODS 仅允许单物理表且无 Join/聚合；DIM 只允许固定 ODS 精确发布版本；DWD 至少固定一个 ODS 事实输入并可附加任意数量 DIM，且禁止业务聚合；DWS 只允许一个或多个 DWD，要求明确输出粒度且至少一个聚合；ADS 只允许固定 DWS，可按消费场景投影、组合或再次聚合。DIM/DWD/DWS/ADS 不允许 `TABLE` 节点或 TABLE/DATASET 混用。

迁移前未写入 DSL `dataset.layer` 的历史文档仍按 DAG 确定性推断并保留旧 TABLE 兼容，避免原地改写其 DSL/hash；新客户端必须始终显式发送层级。这个兼容不放宽 `DATASET` 节点的精确版本、上游层级、当前发布指针和 ACTIVE 物化校验。

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

当前产品语义不再单独定义“下架”状态：可恢复下架就是 `DISABLED` 停用。配置中心统一展示为
“停用（下架）”，避免两个按钮执行同一状态迁移。列表支持多选后批量提交发布审批、停用
（下架）和删除；浏览器以最多 5 个并发调用既有单对象 API，继续保留每个数据集各自的权限、
乐观锁、审批和占用保护。批次允许部分成功，失败项保持选中并显示原因。批量提交发布只创建
逐数据集审批申请，不会直接批准或上线。

`POST /datasets/{id}/restore` 同样需要 `DATASET:MANAGE` 或对象级管理权限，请求体继续使用最近读取的聚合版本：

```json
{ "expectedVersion": 5 }
```

恢复只接受 `DISABLED` 数据集。服务端在数据集行锁内优先还原停用前的 `PUBLISHED`、`STALE` 或 `DRAFT` 状态；恢复已发布状态时只重新挂接停用时保存的精确 `PUBLISHED` 版本，不反向改写版本快照。迁移前只有审计轨迹能够证明最近一次相关生命周期动作是 `DISABLE` 的旧记录才会进入可恢复状态；没有可靠停用快照的兼容记录按安全约定恢复为可编辑 `DRAFT`，真正的 `DEPRECATED` 数据集不会暴露恢复入口。成功返回更新后的数据集并把聚合版本加一；重复恢复或状态不匹配返回 `409 DATASET_VERSION_TRANSITION_INVALID`。

`DELETE /datasets/{id}` 同样需要管理权限和上述请求体。控制面始终采用软删除：目录和普通加载不再返回该数据集，发布版本被废弃，历史版本和审计记录不做物理清除。ODS 删除只停用平台元数据记录，绝不向外部源库生成 `DROP/DELETE`。DIM/DWD/DWS/ADS 是平台 PostgreSQL 仓库中的派生表，删除事务会同时登记带租约和三次重试的物理清理 outbox；仓库 worker 随后删除该数据集的 `warehouse_published` 稳定视图、已退休历史视图和精确校验过的派生层物理表，并在工作台显示清理进度。API 仍只持有仓库只读账号，不因删除能力获得 DDL 权限。服务端会先检查该数据集全部精确版本；只要仍被活动指标、下游数据集、报告草稿、运行中查询或排队/运行中的物化构建占用，就返回 `409 DATASET_IN_USE` 且不修改任何状态。

## 发布审批与不可变版本

除映射表来源镜像的受控初始系统发布外，数据集不能由 HTTP 调用方直接发布。普通数据集的公开流程固定为“保存草稿 → 提交发布申请 → 人工审批 → 原子发布并登记加工 → Worker 加工”：

- `POST /datasets/{id}/publish-requests` 需要 `DATASET:MANAGE` 全局权限或对象级 `MANAGE` 权限，只冻结并提交申请，不创建候选准备任务、不运行加工、不创建版本，也不移动发布指针；
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

审批记录允许人工执行 `PENDING → APPROVED / REJECTED`，也允许数据库在草稿记录版本、DSL hash 或计划 hash 变化时自动执行 `PENDING → CANCELLED`。自动取消与草稿更新在同一事务中完成，审批中心刷新后不再展示旧申请；审批人对未刷新的旧申请操作时返回明确的取消提示。并发审批依靠 `expectedVersion` 失败关闭；重复批准已经通过的申请会返回其原有发布版本，重复提交相同拒绝原因会返回既有拒绝结果。审批记录中的草稿身份、哈希、申请人、提交备注和试跑参数在提交后都不可变。

提交和批准 DWD 时都会检查其精确 DIM 上游：DIM 版本必须已经发布、仍是所属数据集的当前发布指针且数据集处于发布状态；否则返回 `DIM_PUBLICATION_REQUIRED`，必须先完成 DIM 发布。批准时还会重新加载并锁定申请冻结的草稿，重新规范化 DSL，核对数据集/草稿乐观锁、DSL/计划哈希、物理资产、固定文件版本和全部启用的行列策略，要求所有 Join 已人工确认，并复用安全查询运行时执行 `VALIDATION` 试跑。申请提交后草稿或数据集已经变化时不会把申请静默升级到新草稿。跨源试跑返回的基数冲突、多对多和扇出告警会阻止发布；单数据源 MySQL/Oracle 会先为每条等值 Join 执行数据库侧聚合探测，再执行最终一行试跑。探测只返回两侧重复键组数、最大重复度和双侧重复键组数五个统计值，不把 Join 键、SQL、参数或样本带回响应与审计；非等值 Join 返回精确 `joins[i].conditions[j].operator` 路径并失败关闭。试跑或依赖复核失败时返回稳定 `details[].path/code/reason`，申请保持 `PENDING`，不创建半份发布版本，也不移动当前发布指针。

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

批准事务会复制已提交的数据集草稿生成独立 `PUBLISHED` 行，并把申请状态、审批人、不可变版本、`currentPublishedVersionId`、发布审计和后续任务 outbox 原子提交；任一步失败都会整体回滚。ODS 批准后登记指标候选提取，DIM/DWD/DWS/ADS 还会为本次刚批准的精确版本登记 PostgreSQL 物化 build。任务登记属于审批事务，真正的 LLM/规则提取和数据加工只由 Worker 在事务提交后执行。派生层在对应 build 生成 `ACTIVE` 物化前不可查询，不会复用审批前任务或旧版本物理表；完成后查询运行时自动使用 `warehouse_published` 稳定视图。原草稿仍可继续编辑，首次正式发布版本号为 1。指标定义、指标 AI 的 `CREATE_ON_DATASET` 提案和后续报告绑定只能引用精确 `PUBLISHED` 版本；人工维护版本来自审批，映射表初始镜像可来自受控系统发布。它们都不能绑定 `PENDING` 申请、可变草稿或仅有数据集主对象 ID 的“当前版本”。普通草稿在指标创建流程中可作为 `MODIFY_DATASET` 的 AI 改造目标；映射表结构不足时改用 `CREATE_DATASET` 新建普通数据集，保存后再提交发布审批。

审批发布试跑支持两条互斥路径：

- ODS 与未显式声明层级的历史 TABLE 文档继续使用受控源查询/文件执行路径；
- 显式 DIM/DWD/DWS/ADS 的全部 `DATASET` 节点使用 PostgreSQL 物化路径。DIM 上游必须是 ODS；DWD 至少有一个 ODS 事实输入且可附加 DIM；DWS 只能包含一个或多个 DWD；ADS 上游必须是 DWS。每个节点固定的精确上游版本还必须是所属数据集的当前 `PUBLISHED` 版本，并拥有 schema hash 一致的当前 `ACTIVE` 物化及可读 `warehouse_published` 稳定视图。

PostgreSQL 路径不会递归重放上游 DAG，也不会读取上游源库或在内存拼接。缺失 ACTIVE 物化、精确版本不再是当前发布版本、层级/摘要漂移、稳定关系不是视图、读取权限不足或混用 TABLE/DATASET 时均失败关闭，且不会回退到上游草稿、其他发布版本或任意物理表。

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
      "layer": "DWS",
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

显式 DIM/DWD/DWS/ADS 预览只读取 PostgreSQL 中由服务端解析的 `warehouse_published` 稳定视图。解析结果包含节点、精确上游数据集版本、精确 ACTIVE materialization ID、schema/snapshot hash 和允许列；客户端 DSL 不保存、也不能提交稳定视图或物理表名称。执行事务在编译后再次以租户 RLS 锁定并复核当前发布指针、版本状态、ACTIVE 物化、全部摘要、视图类型和 API 角色的 SELECT 权限，再执行参数化 PostgreSQL 查询，防止解析与执行之间发生激活切换。

`CROSS_SOURCE` 实时预览支持 MySQL↔Oracle、数据库↔Excel 和 Excel↔Excel 等值 Join。执行器只向各源读取 Join、显式关联前分组、过滤和输出所需字段，可证明只引用单节点的前置过滤会参数化下推；规范化后的数据先执行 `preAggregations`，再在网关完成 Join、最终聚合、排序和行列策略。单个数据集最多 16 个节点，每个源按预览行数放大读取且硬上限为 10,000 行；源返回超过上限时整次预览失败，不使用截断结果。当前不支持非等值 Join、缓存和物化。

当数据库节点在参与的每条 Join 中都是声明基数的“多”侧，且度量仅以直接字段形式参与 `SUM/MIN/MAX/COUNT/AVG` 时，执行器会按所有仍需保留的 Join、过滤和维度字段在源端预聚合，再由网关归并部分结果。COUNT 对部分计数求和并保持整数类型，AVG 使用部分 SUM/COUNT 计算加权平均；空集合分别保持 0/NULL，纯整数求和溢出会失败关闭。出现 `COUNT(*)`、`COUNT_DISTINCT`、复杂聚合参数、`AGGREGATE_ONLY` 最小分组人数、COUNT/AVG 的直接聚合策略校验、度量字段被非聚合复用、ONE 侧节点或文件节点时，会自动回退原始扫描，避免改变行数、去重或权限语义。所有标识符仍来自物理白名单，过滤值和行数限制仍使用参数绑定。

历史 DSL 和非严格分层草稿在设计期无法判断 Join 基数时仍可使用 `cardinality: "UNKNOWN"`；它不会触发依赖明确“一侧/多侧”语义的预聚合优化，指标引用穿过该 Join 时按潜在扇出处理并失败关闭。新的显式 DWD 必须在保存前证明基数，不能使用 `UNKNOWN`。多个普通 DIM 各自使用 `MANY_TO_ONE / ONE_TO_ONE`；`ONE_TO_MANY / MANY_TO_MANY` 必须增加 `relationshipType: "BRIDGE"`、稳定 `relationshipRole` 和 `DEDUPLICATE / UNSAFE` 扇出策略。角色扮演 DIM 使用 `ROLE_PLAYING` 与如 `ORDERING_USER / PAYING_USER` 的角色编码。`manualConfirmed` 只表示用户已核对连接方式、关联字段和关系语义，不替代基数证据。

跨源预览会对每条 Join 边检查 `manualConfirmed`、声明基数和两侧键重复情况，并返回 `JOIN_CONFIRMATION_REQUIRED`、`JOIN_CARDINALITY_MISMATCH`、`JOIN_MANY_TO_MANY` 或 `JOIN_FANOUT_RISK`。告警仅包含 Join ID 和计数，不包含业务键值。执行器按实际有向 Join 顺序精确传播预览样本的必要键值，任一阶段预计输出超过 200,000 行时会在分配该中间结果前失败；INNER Join 使用较小输入构建哈希索引，外连接不交换声明两侧。

查询主审计写入 `platform.query_runs`，只保存执行引擎、执行计划哈希、参数绑定哈希、状态、耗时、行数和不含业务值的 `warnings_json`。SOURCE 运行固定数据源身份；POSTGRES 运行不伪造数据源身份，而是在 `platform.query_run_materializations` 保存每个节点实际使用的精确数据集版本、ACTIVE materialization ID、稳定视图名及 schema/snapshot hash。跨源节点另写入 `platform.query_run_sources`，保存来源版本、水位、固定文件版本、不可碰撞的子查询 ID，以及可信网关采集的节点状态、实际输入行数和从读取到规范化完成的耗时。这里的节点行数是 Join 前通过网关形状与上限校验的接收行数，不是最终结果行数；启用源端预聚合时，它表示部分聚合行数而非数据库扫描的明细行数。成功查询必须具备全部节点指标；失败、超时或取消时，未完成节点会跟随主查询终态收口。审计表均不保存 SQL、参数明文、结果样本或源端错误文本，并强制启用租户 RLS。远端 Connector 返回的告警或节点指标不会被信任或写入审计。

## 精确发布版本预览

`POST /datasets/{id}/versions/{versionId}/preview`，需要同一数据集的 `DATASET:READ` 或对象级读取权限，请求与响应结构和草稿预览相同。

服务端只执行 URL 中的精确发布版本。SOURCE 路径把版本状态、所属数据集状态、依赖摘要复核与可信物理计划解析放入同一个租户事务和锁定边界；严格 DIM/DWD/DWS/ADS 路径还要求每个固定上游版本仍为其当前发布版本并绑定当前 ACTIVE 物化，执行事务再次锁定复核该绑定后读取稳定视图。所属数据集为 `DISABLED`，版本为 `STALE`、`DEPRECATED`，或发布时固定的物理表结构摘要、文件版本 SHA-256、上游版本/当前指针/物化摘要已经漂移时，返回 `DATASET_VERSION_UNAVAILABLE`。当前依赖漂移只会拒绝本次执行，不会在请求内自动改写版本状态；基于影响分析幂等传播 `STALE` 仍归入 T0307。此接口不会回退到当前发布指针或当前草稿。查询审计的 `dataset_version_id` 固定为请求中的版本 ID，`run_type` 为 `PREVIEW`。

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
| 409 | `DATASET_PUBLICATION_REQUEST_CANCELLED` | 草稿已变更，原审批申请已自动取消，需按最新草稿重新提交 |
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
| 503 | `DATASET_LLM_TRIGGER_UNAVAILABLE` | LLM 人工触发 outbox 尚未装配，未创建任务 |
| 500 | `DATASET_PERSISTENCE_FAILED` | 数据集存储暂时不可用，响应不暴露内部错误 |

除数据集 AI 提案的 128 KiB 特例外，请求体上限为 2 MiB。错误响应不会返回 SQL、数据库内部错误或源端敏感信息。
