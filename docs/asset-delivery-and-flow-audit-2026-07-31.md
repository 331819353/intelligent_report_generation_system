# 数据产物、加工链路与智能问答落地审计

审计日期：2026-07-31
审计范围：当前工作区、控制面 PostgreSQL、数仓 PostgreSQL、运行中的 API/Worker

## 1. 总体结论

系统已经形成“控制面元数据与向量 + 独立数仓物理表”的双 PostgreSQL 架构：

- 平台基础 PostgreSQL 使用 `platform` schema，保存数据源、元数据、分层数据集
  DSL/版本、指标、维度、语义文档、向量、决策图、任务和物化登记，不保存业务明细。
- 数仓 PostgreSQL 使用 `warehouse_dim`、`warehouse_dwd`、`warehouse_dws`、
  `warehouse_ads` 和 `warehouse_published`，保存受控编译产生的物理表与稳定发布视图。
- ODS 当前是精确数据源版本上的虚拟治理映射，不长期复制源明细；DIM/DWD 正式构建时
  按冻结的 ODS 合同全量回源并在数仓侧加工。
- ODS、DIM、DWD、DWS 元数据和向量化已实际产生数据；ADS 具备数据集、任务和物化
  基础能力，但当前环境没有 ADS 产物。
- 指标、维度、维度成员和 WHERE 决策图当前只绑定 DWS。若要求 ADS 再独立落指标、
  维度和决策图，需要扩展现有治理合同，不能把 DWS 资产复制成 ADS 资产冒充落地。

当前环境最新迁移为 `000176_idempotent_dimension_profile_replay`。控制库与数仓库均在线。

## 2. 十八类产物核查

状态说明：

- **已落地**：表结构、写入链路和当前环境数据均存在。
- **能力已落地，当前无产物**：代码和数据库合同存在，但当前没有业务对象。
- **未按目标落地**：当前合同不支持该产物，需要新设计和迁移。

| 编号 | 产物 | 状态 | 实际存储与落地方式 | 当前环境证据 |
| --- | --- | --- | --- | --- |
| a | 数据源配置信息 | **已落地** | 控制库 `data_sources`、`data_source_versions`；凭据只存加密引用/密文，不进入数仓 | 8 个数据源，2 个 ACTIVE |
| b | ODS 数据集元数据 | **已落地** | `datasets/dataset_versions/dataset_fields`，`layer=ODS`；表/字段原始业务元数据还在 `metadata_tables/metadata_columns`；`asset_embeddings` 与 `semantic_documents` 向量化 | 12 个 ODS，8 个当前 PUBLISHED；452 个 ODS 语义文档已向量化；8 个表和 88 个字段向量已就绪 |
| c | DWD 数据集元数据 | **已落地** | 控制库通用数据集版本表，`layer=DWD`；版本/字段语义文档由 semantic outbox 构建 | 24 个 DWD，4 个当前 PUBLISHED；804 个语义文档已向量化 |
| d | DWD 物理数据 | **已落地** | 数仓 `warehouse_dwd` 物理表，`warehouse_published` 稳定视图；控制库 `dataset_materializations` 只登记身份、版本、hash、行数和大小 | 4 个 ACTIVE DWD 物化、4 张物理表、4 个发布视图 |
| e | DIM 数据集元数据 | **已落地** | 控制库通用数据集版本表，`layer=DIM`；语义文档向量化 | 59 个 DIM，4 个当前 PUBLISHED；415 个语义文档已向量化 |
| f | DIM 物理数据 | **已落地** | 数仓 `warehouse_dim` + `warehouse_published`；控制库登记物化 | 4 个 ACTIVE DIM 物化、4 张物理表、4 个发布视图 |
| g | DWS 数据集元数据 | **已落地** | 控制库通用数据集版本表，`layer=DWS`；版本/字段语义文档向量化 | 29 个 DWS，8 个当前 PUBLISHED；267 个语义文档已向量化 |
| h | DWS 物理数据 | **已落地** | 数仓 `warehouse_dws` + `warehouse_published`；控制库登记物化 | 8 个 ACTIVE DWS 物化、8 张物理表、8 个发布视图 |
| i | DWS 指标信息 | **已落地** | `metrics/metric_versions/metric_dimensions`；`metric_semantic_documents` 和通用 `semantic_documents` 建立检索向量 | 25 个指标、84 个历史/当前 PUBLISHED 指标版本；566 个指标语义文档均已向量化 |
| j | DWS 维度信息 | **已落地** | `semantic_dimensions`、`dimension_members`、成员别名、兼容关系；定义向量和成员向量分别落表 | 37 个 PUBLISHED 维度、2,242 个 ACTIVE 成员；37 个维度定义和 2,242 个成员向量均就绪 |
| k | DWS 维度决策图 | **已落地** | `dimension_where_decisions` 保存“维度描述 + 规范值 → 指标版本 + DWS 稳定视图 + 受控 WHERE + 服务端编译条件”；预计算边复用成员向量文档，避免每条边重复存 2,560 维向量 | 9,624 条决策；全部绑定稳定视图、编译条件和向量文档；直接 `embedding` 为 0 是去重设计，不是缺失 |
| l | ADS 数据集元数据 | **能力已落地，当前无产物** | 通用数据集表、DSL 和 semantic outbox 已接受 `layer=ADS`；ADS worker 可生成草稿 | 当前 ADS 数据集为 0 |
| m | ADS 物理数据 | **能力已落地，当前无产物** | 物化合同支持 `warehouse_ads` 和发布视图；必须先有 ADS 发布版本并触发构建 | 当前 ADS 物化和物理表均为 0 |
| n | ADS 指标信息 | **未按目标落地** | 当前指标发现、发布和查询合同限定 DWS；ADS 作为消费层复用 DWS 指标，不生成独立 ADS 指标 | 0；需要新合同 |
| o | ADS 维度信息 | **未按目标落地** | 维度画像、成员刷新和维度发布限定 ACTIVE DWS 物化 | 0；需要新合同 |
| p | ADS 维度决策图 | **未按目标落地** | 决策图外键与校验限定 DWS 指标、维度和 ACTIVE DWS 物化 | 0；需要新合同 |
| q | 指标语义 | **已落地** | 目录语义使用 `metric_semantic_documents`；用户常用表达使用 `semantic_term_assets`，向量输入是用户表达，输出是标准指标语义 | 566 个目录语义文档已就绪；2 条显式指标别名语义已就绪 |
| r | 维度语义 | **已落地** | `dimension_semantic_documents`、`dimension_member_semantic_documents`、`semantic_term_assets` 分别保存维度名、维值和用户表达映射 | 37 个维度定义、2,242 个成员、67 条显式维度语义均已向量化 |

## 3. 数据加工流程核查

### 3.1 已实现主链路

1. 用户创建并发布数据源配置。
2. 元数据后台任务发现数据源下全部表；页面只提交用户选中的表。
3. 任务先保存技术元数据，再按授权读取少量样本。样本不持久化。
4. 样本行数已改为随字段数从 10 行线性降到 3 行：1 个字段为 10 行，50 个字段为
   3 行，超过 50 个字段保持 3 行。
5. 元数据 LLM 输出表业务名称、表描述、业务功能/适用范围/粒度、字段名称、字段描述、
   语义类型和受控标签；表和字段分批处理但整表事务提交。
6. 元数据完成事务同步创建或刷新 ODS 映射数据集；不完整结果创建可编辑 ODS 草稿。
7. ODS 发布后进入持久化 DIM/DWD 分阶段任务：
   `DOMAIN_CLASSIFICATION → DIMENSION_MODELING → FACT_MODELING`。
8. DIM 识别支持一张 ODS 产生 0–N 个维度；生成 DIM 草稿后等待发布治理边界。
9. DWD 事实建模分析 ODS 事实、DIM 和其他 ODS 事实的主次关系，生成结构化 DSL/DAG
   草稿；LLM 不输出可执行 SQL。
10. DIM/DWD 发布后由物化 Worker 锁定精确版本，回源 ODS，受控编译 PostgreSQL SQL，
    创建物理表并原子切换发布视图。
11. DWD 发布/物化后进入 DWS 建模；默认每张事实 DWD 生成统一多维主题，也支持明确
    框选的联合 DWS。多事实联合会先把每张事实分别预聚合到共同月份和最多四个类型
    一致的公共维度，再关联各事实度量，避免原始事实直接 JOIN 造成扇出。
12. DWS 发布和 ACTIVE 物化后同步提取/发布指标、画像维度、刷新成员、生成向量和
    预计算 WHERE 决策图。
13. ADS 只通过明确的 ADS 建模触发器进入 Worker。默认模式可在两个单粒度 DWS 维度
    集合存在严格包含关系且指标可安全再聚合时生成联合 ADS，否则生成单 DWS 投影。

### 3.2 与目标描述的差异

- **“DWD + DWD → DWD”不符合当前分层合同。** 当前合同是
  `DWD <- ODS + DIM`、`DWS <- DWD`。现有第二轮事实关系分析比较的是多个 ODS
  事实源并生成 DWD，不是让已生成 DWD 递归生成 DWD。建议把 DWD 间主题关系放在
  DWS，避免明细层递归、粒度漂移和血缘闭环。
- **发布与物化存在治理门。** LLM 只生成可评审草稿；发布、权限、依赖校验和物化是
  独立步骤。若产品要求完全自动，需要为“程序生成且通过静态校验”的对象定义明确的
  自动审批策略，不能让 LLM 直接激活物理数据。
- **ADS 当前不是自动串在每次 DWS 后。** 它需要手工/受控触发，且当前环境没有 ADS。
- **ADS 没有独立指标、维度和决策图。** 若 ADS 只是交付层，建议继续复用 DWS 语义；
  若 ADS 必须成为问答入口，需要新增 ADS 指标版本、维度画像、兼容关系、决策图和
  查询计划合同。
- **历史任务并非全成功。** 当前有 9 个 FAILED、3 个 PARTIAL 元数据任务；DWD 建模
  有 8 个 FAILED、3 个 PARTIAL，主要是模型输出未通过结构/字段合同。当前有效产物
  已落地，但任务中心应保留重试、修复和质量告警。
- **多事实自动发现仍有治理边界。** 显式框选两张或多张 DWD 已能生成
  `MULTI_FACT_COMPARISON`，并按共同时间与一致维度关联；未框选的默认任务仍逐 DWD
  建模，不会把领域内所有事实盲目笛卡尔组合。若要求自动发现事实星座，应增加独立的
  “事实关系候选 → 共同粒度证明 → 人工/规则确认”阶段。

## 4. 智能问答流程核查

### 4.1 当前已实现

正式 `POST /semantic-qa/query-turns` 已在服务端串联以下步骤：

1. Jieba/HMM 分词、词性和规则实体标注。
2. 原问题整句向量召回指标 Top 5。
3. 每个有效分词分别召回指标与维度语义 Top 5；向量失败时使用词法降级。
4. LLM 只能从已召回候选中选择意图、指标和维度，不能发明表、字段、WHERE 或 SQL。
5. 选定指标后锁定精确 DWS 指标版本，再检索其兼容维度、成员、别名和决策图。
6. 对同值跨维度歧义，LLM 只能复制一个候选维度 code；无法证明则不选择。
7. 决策图按“维度描述 + 规范值”向量召回；LLM 只在受控候选中保留一个决策。
8. WHERE 由服务端 AST/字段白名单重新编译并参数化；多个条件在受治理计划中组合。
9. QueryPlan 再校验权限、版本、物化、指标—维度兼容关系、图 generation 和血缘。
10. 只有全部计划为 READY 才执行；运行前再次校验并返回证据链。

### 4.2 本轮补齐与剩余验证

- **人工澄清闭环已补齐。** 指标歧义返回名称、编码、版本和来源物化表；维度歧义
  返回决策图 ID、维度和值。用户选择后用指标编码/决策图 ID 重提，服务端重新加载
  PUBLISHED 资产，浏览器不能提交表名、WHERE 或 SQL。
- **模型工具循环已补齐。** 通用 Provider 已解析并回传
  `tools/tool_choice/tool_calls/reasoning_content`；指标检索使用
  `search_metrics → submit_metric_selection` 有界循环，协议失败时降级到原有固定
  安全编排。
- **需要端到端验收。** 现有单元测试覆盖分词、语义候选、时间范围和安全过滤；仍需
  用真实业务黄金问题验证纯指标、同义词、指标歧义、维值跨字段重名、多维度、时间、
  无命中和不兼容维度。

## 5. DeepSeek-v3.2 的“边思考边检索”

这种能力不是模型自己访问数据库，而是应用实现一个有界工具循环：

1. 把只读工具的名称、说明和 JSON Schema 发给模型。
2. 模型返回 `tool_calls`，应用校验参数、租户、权限、版本和调用预算。
3. 应用执行检索，把结果作为 `tool` 消息回传。
4. 模型基于结果继续思考并再次调用工具，或输出最终答案。
5. 最终 SQL 仍由服务端编译器产生，模型不得提交可执行 SQL。

DeepSeek 官方文档说明 V3.2 支持思考模式下的工具调用；工具由调用方执行，模型只生成
调用请求。思考模式的多轮工具调用需要在同一轮工具循环中回传
`reasoning_content`。当前公共 DeepSeek API 已在 2026-04 后切换到 V4 系列并计划
停用旧别名；本项目接入的是企业网关别名 `deepseek-v3`，数据库审计只能证明该别名
成功完成过结构化调用，不能证明网关后端一定是官方 V3.2 或已开放 thinking tools。

当前代码已增加供应商无关的工具完成协议和有界循环。DeepSeek 与 GLM 开启 thinking
并回传 `reasoning_content`；MiniMax 回传完整 assistant/tool 消息。推理内容只在
本轮内存中传递，不写审计、日志、数据库或页面。智能问答的指标解析已经由模型按需
调用 `search_metrics`，候选不足时可再次检索，再通过终止工具提交选择或请求澄清。

实现采用混合方案，没有把数据库直接交给 Agent：

- 当前模型可选择 `search_metrics` 和终止工具 `submit_metric_selection`；维度、决策图、
  计划校验和 READY 计划执行仍由确定性的服务端阶段负责，模型不能绕过。
- 工具返回有界候选、稳定 ID、版本和证据，不返回凭据、任意 SQL 或无界明细。
- 工具循环限制最大轮数、总候选数、Token、总时长和并发；整个循环汇总写入 AI 审计。
- `reasoning_content` 只在本轮内存中为协议连续性回传，不写日志、不入库、不展示；
  面向用户展示的是候选、工具结果和最终决策摘要。
- 计划执行只接受已持久化且状态为 READY 的计划 ID，并再次执行权限和 generation
  校验；该能力不暴露为模型工具。
- 上线前先对企业网关做能力探测：模型别名、thinking 开关、tools、并行工具、
  strict schema、`reasoning_content` 回传和超时/限流行为必须逐项验证。

官方参考：

- https://api-docs.deepseek.com/guides/tool_calls/
- https://api-docs.deepseek.com/guides/thinking_mode
- https://api-docs.deepseek.com/api/create-chat-completion

## 6. 建议优先级

1. P0：修复或重跑当前 FAILED/PARTIAL 元数据与 DWD 建模任务。
2. P0：明确 ADS 定位——消费交付层复用 DWS 语义，或升级为独立语义查询层。
3. P1：为企业网关增加启动时能力探测和模型健康路由。
4. P1：增加多事实关系自动发现与共同粒度候选确认，不盲目组合领域内全部事实。
5. P1：建立覆盖加工、向量、决策图、查询计划和执行证据的端到端黄金问题。
