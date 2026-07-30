# 分层数据平台与语义层技术方案

本文定义数据源、ODS/DIM/DWD/DWS/ADS、LLM 建模编排、物化执行、标签与语义检索的目标架构和本轮落地边界。核心原则是：控制面与数据面分离、ODS 不复制来源明细、DWD 才是首次全量落仓、所有运行版本不可变、测试不等于发布。LLM 负责分析元数据并生成表结构与结构化 DAG，不获得 SQL、DDL 或物理执行权；底层开发引擎负责把已治理 DAG 编译成物理过程。

> 后续已批准的目标合同与实施顺序见
> [智能问答语义层与分层建模改造计划](./semantic-qa-retrofit-plan.md) 和
> [智能问答语义层优化 TODO](./TODO-semantic-qa-optimization.md)。在对应 TODO
> 完成前，本文标注的“当前 worker 落地边界”仍代表实际运行行为。

## 1. 总体架构

```mermaid
flowchart LR
  subgraph Source["外部数据源"]
    MySQL
    Oracle
    Excel["Excel / CSV"]
  end

  subgraph Control["PostgreSQL 控制面 · platform"]
    DS["数据源与不可变配置版本"]
    Catalog["表 / 字段 / DAG / 数据集版本"]
    Build["构建运行 / 输入快照 / 质量结果"]
    Semantic["标签 / 维度 / 成员 / 指标兼容关系"]
    Outbox["语义变更 Outbox / 向量文档"]
  end

  subgraph Data["PostgreSQL 数据面"]
    Stage["warehouse_staging"]
    DIM["warehouse_dim"]
    DWD["warehouse_dwd"]
    DWS["warehouse_dws"]
    ADS["warehouse_ads"]
    Published["warehouse_published 稳定视图"]
  end

  subgraph Worker["后台执行器"]
    Metadata["元数据发现"]
    TagSuggest["发布版本标签建议"]
    Modeler["LLM 分层结构 / DAG 设计"]
    Builder["开发引擎 · DAG 编译 / COPY / CTAS"]
    Embed["语义文档重建与向量化"]
  end

  MySQL --> Metadata
  Oracle --> Metadata
  Excel --> Metadata
  Metadata --> Catalog
  Catalog --> Modeler --> Catalog
  Catalog --> Builder
  MySQL --> Builder
  Oracle --> Builder
  Builder --> Stage --> DWD
  Builder --> DIM
  Builder --> DWS
  Builder --> ADS
  Catalog -. "ODS 字段映射 + 精确源版本" .-> Builder
  DIM --> DWD
  DWD --> DWS --> ADS
  DIM --> Published
  DWD --> Published
  DWS --> Published
  ADS --> Published
  Catalog --> TagSuggest
  Semantic --> TagSuggest
  TagSuggest --> Outbox
  Catalog --> Outbox --> Embed --> Semantic
  DS --> Catalog
  Build --> Published
```

`platform` schema 只保存配置、元数据、不可变版本、ODS 映射、血缘、运行和索引信息，不保存动态业务事实表。ODS 明细始终留在来源：Excel/CSV 留在精确文件版本，MySQL/Oracle 留在精确发布源表。DWD、DIM、DWS、ADS 的业务数据写入独立 `warehouse_*` schema。所有表名由服务端根据租户、数据集和运行 ID 计算，客户端不能提交物理表名或 SQL。

## 2. 数据源生命周期

数据源主对象与连接配置版本分离。每次创建、连接字段修改、密码轮换或文件重传都会生成新的不可变配置版本；已发布版本继续运行，直到新草稿完成测试并显式上线。

```text
DRAFT / UNTESTED
       │ test
       ├────────失败────────> DRAFT / FAILED
       │
       └────────成功────────> DRAFT / PASSED
                                  │ publish（同 version + hash，30 分钟内）
                                  ▼
                             ACTIVE / PUBLISHED
                                  │ edit
                                  ▼
                     旧发布版本继续运行 + 新草稿 UNTESTED
```

约束如下：

- `test` 只产生绑定 `configVersionId + configHash` 的短期测试证据，不切换运行配置。
- `publish` 必须验证测试证据未过期、配置版本未变化、摘要完全一致。
- discovery、采样、同步和运行查询只读取发布版本；管理页面可以读取当前草稿。
- 密码以 AES-256-GCM 密文引用保存，API、审计和日志不回显密码或内部引用。
- Excel/CSV 每次上传产生不可变文件版本；数据集与物化输入固定到精确文件版本。
- 所属人、可见范围、描述、创建/修改人和时间是控制面字段，不混入连接 JSON。

## 3. 数据集层级合同

层级是数据集版本的不可变属性，不由表名或用户界面文字推断。

| 层级 | 合法输入 | 合法操作 | 明确禁止 |
| --- | --- | --- | --- |
| ODS | 一个已发布物理表或精确文件版本 | 保存清晰的字段/类型/说明映射；预览时从该精确来源截取最多 100 行 | 复制全量明细到数仓、Join、Group、业务聚合 |
| DIM | 一个已发布 ODS 精确版本 | 抽离人物、商品、组织、区域等实体说明；正式构建时从 ODS 固定的来源全量加工并落仓 | 事实聚合、改变实体粒度、绕过 ODS 映射读取其他来源 |
| DWD | 至少一个已发布 ODS 事实版本，可附加已发布 DIM | 正式构建时按已完成 DAG 从冻结来源全量抽取、清洗和关联后首次落仓；设计预览只加工各 ODS 的 100 行样本 | Group、业务聚合、缺少 ODS 事实根的 DIM-only 设计 |
| DWS | 一个或多个已发布 DWD 精确版本/物化，或单个已发布 DIM 精确版本/物化 | 按主题、主体、时间、商品等视角组合事实；DIM-only 输入只生成单一实体计数指标 | 同时混用 DWD 与直接 DIM 事实、引用 ODS/外部源表或在内存完成主计算 |
| ADS | 一个或多个已发布 DWS 精确版本/物化 | 面向报表、接口、看板或应用场景组合、裁剪、重命名和必要的二次汇总 | 直接越层引用 DIM、DWD、ODS 或外部源表 |

历史 DSL 未声明 `layer` 时，服务端仍用确定性规则归类：含聚合为 DWS；单表、无 Join 为 ODS；其他为 DWD，并保留旧 TABLE 执行兼容以避免改写已发布正文/hash。显式声明层级的新 DSL 采用严格节点合同：`ODS <- SOURCE`、`DIM <- ODS`、`DWD <- 至少一个 ODS + 可选 DIM/受控次事实`、`DWS <- DWD[1..N] | DIM[1]`、`ADS <- DWS`。DIM-only DWS 必须是 `ENTITY_COUNT` 且只有一个计数指标。ODS 的 `DATASET` 版本是可审计的虚拟来源合同，不要求 ACTIVE 物化；DWD 注册时把它展开为精确来源快照，DWS/ADS 则只读取数仓中当前 ACTIVE 的上游物化。

### 3.1 LLM 建模与开发引擎边界

分层建模不要求一次 LLM 调用完成。编排器可以按依赖拆成多个过程，并在同一阶段内并行：

```mermaid
flowchart LR
  Meta["已发布 ODS 元数据、字段合同、标签与粒度"]
  Classify["LLM 业务角色识别"]
  ClassifyCheckpoint["分类检查点"]
  DIMPlan["DIM 表结构与清洗 DAG"]
  DWDPlan["逐 FACT 的 DWD 结构、关联与清洗 DAG"]
  FactCheckpoint["逐 FACT 设计检查点"]
  DWSPlan["DWS 分析视角与聚合 DAG"]
  ADSPlan["ADS 消费场景组合 DAG"]
  Consumer["已发布消费合同"]
  Review["DSL 校验 / 人工评审 / 不可变发布"]
  Engine["开发引擎：拓扑调度、SQL 编译、质量门、原子激活"]

  Meta --> Classify
  Classify --> ClassifyCheckpoint
  ClassifyCheckpoint --> DIMPlan
  ClassifyCheckpoint --> DWDPlan
  DWDPlan --> FactCheckpoint
  DIMPlan --> Review
  FactCheckpoint --> Review
  Review --> DWSPlan --> Review
  Consumer -. "当前预留" .-> ADSPlan --> Review
  Review --> Engine
```

未框选流程中，LLM 对领域内 ODS 逐数据集分类并最多四路并行；一张 ODS 可以形成零张、一张或两张不同实体 DIM，键集合存在包含关系的候选收敛为一张细粒度统一维度，互不包含的实体保持独立，分类结果同时回写精确版本的事实、维度、事实兼维度或其他建议标签。每张非纯维 ODS 单独进入 DWD：第一轮把其他事实分批识别业务关系和主次，只保留当前表为主表的至多一行次表关系；第二轮按当前表汇总已确认关系与 DIM 合同，再生成保持事实粒度的 DWD 草稿，并把普通流量、累计值、时点值和不可加值写入可执行度量合同。DWS 对每张 DWD 只设计一张统一主题表，使用 CUBE、ROLLUP 或 GROUPING_SETS 表达多个分析粒度；每张 DIM 仍生成只有一个实体计数指标的无事实主题。累计与时点指标保留日级时间并禁止跨时间求和。ADS 只比较单粒度 DWS 的维度集合，只有较细粒度维度严格包含目标维度且指标为可加流量时，才先预聚合细表并按完整目标维度键一对一关联两侧指标；维度互不包含、多粒度表或半可加指标均保持独立。显式框选继续冻结既有关系：多张 DWD 与可选 DIM 仍作为一个联合分析任务，ADS 仍逐所选 DWS 处理，不进入本轮默认关系发现。每个过程都保存独立检查点并输出同一受控 DSL；LLM 不返回 SQL、物理 schema、物理表名或调度命令。

开发引擎只接受通过层级、字段引用、表达式白名单和依赖校验的 DSL。它根据 DAG 拓扑并行执行无依赖节点，负责参数化 SQL 编译、COPY/CTAS、租约、重试、质量门和原子激活。由此允许建模过程拆分或并行，同时仍以精确版本、hash 和质量结果证明最终 DIM、DWD、DWS、ADS 一致。

系统由完成映射的表/Sheet 自动生成 ODS 时，会固定
`REALTIME + previewLimit=100 + materialization.enabled=false`。发布事务只保存
不可变 ODS 字段合同及精确源发布版本/文件版本，不登记 ODS build，也不创建
`warehouse_ods` 表。DWD/DIM 发布构建才把 ODS 合同中的精确来源转成不可变
build input；因此重新上传文件或重新发布数据库源不会静默改变已经登记的构建。

映射 ODS 的后续结构刷新采用保守所有权栅栏，不是“所有已发布映射表自动升级”。
仅当当前发布版本具有不可变的 `SYSTEM_MAPPED_* publication_origin`、当前草稿仍与
该版本冻结的来源草稿身份、记录版本、schema hash 和 plan hash 完全相同，且没有
`PENDING` 发布申请时，系统才能生成下一版本。人工保存、待审批和人工审批发布都会
使系统刷新失败关闭，且不得改变草稿、发布指针、build 或审计；软删除后的重新映射
也执行同一规则，不能以“重建”为由丢弃人工修订。审计只承担追踪，不参与授权；
该设计仍把 API/worker 数据库角色视为可信边界。

每次构建固定以下输入证据：

- 精确数据集版本、文件版本或物化 ID；
- schema hash、snapshot hash、来源版本和可选水位；
- 规范 DAG、plan hash、input snapshot hash；
- 每个节点的执行引擎和依赖关系。

输入快照不能包含密码、原始 SQL、样本行或任意业务数据。

## 4. ODS 元数据完善与标签

发布数据源后，用户才能进入表/Sheet 选择。数据集版本发布事务会同时写入
`dataset_tag_suggestion_jobs`，固定租户、数据集、精确版本、层级、schema hash、
源发布版本快照和 Prompt 版本。任务以这些内容作为幂等及版本栅栏；领取后若数据集
已被新发布版本取代、源版本或结构发生变化，任务以 `SKIPPED / SUBJECT_CHANGED`
结束，不把旧建议写回新版本。

数据集自动标签建议链路有意不读取业务样本行。ODS 输入只包含已治理的数据源类型、表和
投影字段技术元数据、已有业务说明、键属性和表标签；DIM/DWD/DWS/ADS 输入包含当前数据集的
字段、DAG、粒度，以及精确发布上游版本的字段和已批准标签摘要。任务、AI 审计、
日志和错误信息均不保存凭据、SQL、表达式字面值或业务数据值。

上游“表/Sheet → ODS 元数据完善”任务另有独立样本授权：默认 `DENY`，完全不读取
业务行；`MASK` 最多读取十行并在应用进程内做格式脱敏；`RAW` 是租户策略允许后的
逐任务高风险授权。任务冻结策略版本和授权人，并在任务开始、采样前、LLM 调用前
三次失败关闭复核。无论哪种模式，样本值都不落控制库、日志或审计表，也不会进入
后续数据集标签建议 worker。

模型只能从当前租户 `ACTIVE + CONTROLLED` taxonomy 中选择已有标签。请求使用严格
JSON Schema，并以 tag ID 枚举约束输出；输入最多 192 KiB、taxonomy 最多 1024 个
标签、单次最多 256 个建议。自动选择覆盖：

- `BUSINESS_ENTITY`：业务实体；
- `TABLE_FUNCTION`：表的功能；
- `USAGE_SCOPE`：使用范围；
- `DATA_GRAIN`：一行代表什么粒度；
- `JOIN_ROLE`：主键、外键、业务键、桥接键及关联方向；

业务领域不属于 taxonomy，由用户当前所属领域和资产 `domain_id` 确定。
`SENSITIVITY` 和 `FREEFORM` 仍可由人工治理，但模型不能自动创建或选择它们。
关联定位依据字段描述、主外键/唯一性/可空性和 DAG Join 条件中的字段引用；条件中的
业务字面值不会进入模型输入。

标签本体、别名、资产绑定和向量文档分别存储：

- `semantic_tags` / `semantic_tag_aliases`：受控词表与别名；
- `asset_tag_bindings`：标签和精确数据集版本、字段、维度、成员、指标版本的关联；
- `semantic_documents`：可重建的检索文档与 embedding；
- `semantic_change_outbox`：编辑后的可靠重建事件。

模型结果只会创建 `origin=LLM,status=SUGGESTED` 的精确数据集版本绑定，绝不会
自动批准；若同一绑定已存在，则保留其现有 `SUGGESTED / APPROVED / REJECTED`
治理结论。管理员通过语义管理 API 批准后，v60 触发器在同一事务推进
`semantic_change_outbox.event_version`，后台再重建文档及向量。因此编辑标签
不会改写数据集 DSL，迟到 worker 也不能覆盖新版本结果。

## 5. ODS 采样与 DIM/DWD/DWS/ADS 执行策略

ODS 不执行全量提取。交互式预览按 ODS 发布版本固定的来源读取：数据库节点使用
受限参数化扫描，Excel/CSV 使用精确 `fileVersionId` 的有界 worksheet 解析；每个
来源节点最多取前 100 行，再在内存中按正在编辑的 DIM/DWD DAG 执行过滤、转换、
Join 和字段投影。100 行是预览样本而非完整性证明，不能据此确认全量类型范围或
质量结果。编码、编号、ID、代码、标识等字段由字段合同强制为 `STRING`，不会因为
前 100 行恰好全为数字而改成整数。

DIM/DWD 正式构建会冻结 ODS 中的精确数据源版本、表结构摘要、文件版本及 SHA-256，
再从 MySQL/Oracle 流式读取或从 Excel/CSV 逐行解析全量明细，使用运行级
`warehouse_staging` 承接各 ODS 投影，并按已批准 DAG 加工后写入
`warehouse_dim / warehouse_dwd`。远端错误、字段漂移、类型错误、行数不一致或
COPY 失败都会回滚本次构建。DWS 的输入包含一个或多个精确 ACTIVE 的 DWD，或用于无事实实体计数的单个精确 ACTIVE DIM；
ADS 的输入是精确 ACTIVE 的 DWS，两层均只在数仓中检索、加工和落地，不回源。

Connector 的资源上限不只按单数据源配置。进程级
`CONNECTOR_MAX_POOLS`（开发默认 1,000）限制连接池注册表，
`CONNECTOR_MAX_TOTAL_CONNECTIONS`（开发默认 100）限制池化与 one-shot 连接合计
的物理数据库 socket；两项在 production 都必须显式配置。新池达到上限时，LRU
只淘汰没有借用者、没有等待 acquire 引用的完全空闲池。新物理连接达到上限时也只
回收已有池中的空闲连接，活动连接永不被驱逐；没有空闲连接时等待本次连接超时后失败
关闭。草稿连接测试共享全局物理连接配额，但每次使用 one-shot 连接并在所有终态关闭，
不进入普通池，也不会因大量不同草稿配置在 TTL 窗口内堆积池。

Connector 和 Go 调用方同时执行字节预算；远端配置漂移或 Connector 被替换时，
Go 侧仍在 JSON 解码、NDJSON 消费和 `COPY` 前独立失败关闭。当前开发默认值如下，
生产环境必须逐项显式注入：

| 边界 | 默认上限 | 失败语义 |
| --- | ---: | --- |
| 单个 Connector HTTP 请求体 | 1 MiB | 解析凭据前返回 `413 CONNECTOR_REQUEST_BODY_LIMIT_EXCEEDED` |
| 普通 JSON 响应 | 64 MiB | 服务端稳定资源码；Go 用 `LimitReader` 做 max+1 复核 |
| 技术元数据同步 | 200,000 个源字典行，并共享普通 JSON 64 MiB | fetch 阶段 `METADATA_SYNC_*_LIMIT_EXCEEDED` |
| 元数据样本单元格 / 单行 / 整体响应 | 16 KiB / 64 KiB / 512 KiB | `METADATA_SAMPLE_*_BYTES_EXCEEDED` |
| DWD/DIM 回源 NDJSON 单元格 / 单行 / 整条流 | 1 MiB / 4 MiB / 1 GiB | `QUERY_*_BYTES_EXCEEDED`，整次流失败 |
| 单个 NDJSON 事件 | 8 MiB 内部硬上限 | `QUERY_STREAM_EVENT_BYTES_EXCEEDED` |
| 每租户、每物化任务的数据库或 Excel/CSV staging 逻辑载荷 | 1 GiB | `ErrStageBytesExceeded`，事务回滚 |

结构识别/LLM 授权样本仍最多十行；ODS/DWD 设计预览则每个来源最多 100 行。两者最多
256 个明确投影字段；投影来自已发现元数据，二进制、LOB、
Oracle `LONG / LONG RAW / XMLTYPE / JSON` 等不安全类型在 Go 和 Connector 两侧
排除或拒绝。普通查询、样本和流式查询不会用静默 `LIMIT` 把截断结果当成完整结果；
行数、列数、单元格、单行、响应/整流以及仓储载荷任一超限都使调用失败。对外物化
仍统一收口为 `ODS_STAGING_FAILED`，底层稳定码只用于无敏感值的运维诊断。

文件对象读取受租户 `max_excel_file_bytes` 约束；XLSX 采用逐行解析与 1,000 行
批次 COPY，worksheet XML 超过 16 MiB 时落到临时文件。展开总量不得超过压缩文件
大小的 8 倍且绝对不超过 2 GiB，逻辑投影行在 COPY 前继续累计受 staging 上限约束。

MySQL 技术元数据同步、普通查询和 DWD/DIM 正式回源都使用真正的 `SSCursor` 逐批读取。预算终止、
主动取消、客户端断流或任何流式异常都会先关闭 socket，并把物理连接标为不可复用，
防止驱动在归还连接池时排空未读结果而产生无界读取；Oracle 游标也遵循同样的异常
连接丢弃合同。驱动仍须先物化一个源字段值，cell 上限不是源端单值峰值内存的绝对
上限，因此还需容器内存限制、源字段/LOB 策略和数据库最大包配置。上述字节
预算是应用级防线，不替代容器内存/CPU 限额、数据库 statement timeout、任务超时
和源库资源管理。

## 6. 物化、质量门与发布

物化采用 shadow build + atomic activation：

1. `Register` 固定已发布数据集版本、计划和全部输入快照，以内容摘要生成幂等键。
2. worker 使用 `FOR UPDATE SKIP LOCKED` 和随机 lease token 领取任务。
3. 节点按 DAG 执行，记录输入/输出行数、大小、错误和重试。
4. PostgreSQL 使用服务端编译的 DSL 执行 CTAS，生成运行级不可变 shadow table。
5. 至少执行行数、业务键非空/唯一、schema hash 和声明的质量规则。
6. 质量门通过后，在一个事务中把旧 ACTIVE 物化标记为 RETIRED、登记新 ACTIVE、完成构建运行；稳定发布视图指向新物化。
7. 任何失败都保留可审计运行记录，但不会替换当前发布版本。

构建运行、节点运行、输入快照、质量结果与物化登记分别存储，避免把易变运行状态塞入数据集版本。

### 当前 worker 落地边界

本轮可执行 worker 采用“ODS 虚拟合同、DWD/DIM 首次落仓、DWS/ADS 数仓内加工”的闭环：

- 只领取服务端已登记的不可变 build run，并持续用随机 lease token 心跳续租；
- 重新加载仍为当前发布态的目标 `dataset_version`，严格校验规范 DSL 与 `schema_hash`；
- ODS 只接受单个 `TABLE` 节点和单个 SOURCE 输入，发布后不登记构建；合同固定当前
  `PUBLISHED` 的 `data_source_version`、活动元数据表及其 `structure_hash`；
- MySQL/Oracle 的投影和可下推过滤由受限方言编译器在源端执行，Connector 以严格
  NDJSON 流传输，最多 5,000,000 行，最终批次直接 typed COPY 到 run-scoped
  `warehouse_staging`，不会把完整源表装进 API/worker 内存，也不会把静默截断当成功；
- Excel/CSV 只读取发布配置绑定的精确不可变 `file_version`，在对象存储读取时复核
  `assetId + versionId + size + SHA-256`，再复核 Sheet、投影和规范类型。行数、列形状
  或单元格类型漂移都会让同一 PostgreSQL staging 事务回滚；对象读取、XLSX
  展开/worksheet 内存和逻辑 staging 同时受上述更严格预算约束；
- DWD/DIM 构建把虚拟 ODS 输入重新解析为同租户的精确来源快照，全量 staging 后按
  ODS 字段合同投影为运行级输入；只有 DWD/DIM 输出进入数仓物化和稳定视图；
- DIM 只接受 ODS；DWD 至少接受一个 ODS 事实输入并可接受 DIM；ODS 输入不要求
  ACTIVE 物化，DIM/DWD/DWS/ADS 数仓输入则必须解析到同租户、同精确版本、当前
  `ACTIVE` 的物化及其 `warehouse_published` 稳定视图；
- 解析时同时校验 ACTIVE 稳定视图和其不可变物理表；CTAS 读取冻结物化的运行级物理表，而不直接读取可能被下一次发布切换的稳定视图，避免“解析后、执行前”发生上游快照漂移；
- 输入的 schema hash、snapshot hash 和可选 row count 必须与 ACTIVE 物化完全一致，DSL 中每个节点必须有且只能有可信输入；`sourceVersion` 仅作为审计标签，不参与物理定位；
- 所有计划节点必须声明 PostgreSQL 执行；worker 按 DAG 拓扑记录节点运行，最终由受限 DSL 编译器和 CTAS 执行；
- 当前只执行 `FULL + TABLE`，`INCREMENTAL`、`BACKFILL` 和 `PARTITIONED_TABLE` 分别以 `REFRESH_MODE_UNSUPPORTED`、`PARTITIONED_TABLE_UNSUPPORTED` 失败关闭，不会暗中退化成全量表；
- 激活前记录输出行数、`pg_total_relation_size`、确定性 snapshot hash，以及行数和
  已声明粒度键的质量结果；ODS 不执行数仓质量门，DIM/DWS/ADS 必须显式声明业务键。
  保持源行粒度且上游明确未声明业务主键的 DWD 可以不声明键，此时只执行行数质量门，
  不得用首列虚构唯一性合同；
- 丢失租约后立即取消数据库工作，后续节点更新和激活仍由 token + 数据库时间双重栅栏保护。过期运行重领时会从计划起点重放全部节点。

当前仍不执行 `INCREMENTAL`、`BACKFILL`、`PARTITIONED_TABLE`。缺少
对应 Connector/stager 的源类型会以 `ODS_SOURCE_STAGING_NOT_CONFIGURED` 失败（错误码
保留 ODS 名称用于兼容，实际发生在 DWD/DIM 的 ODS 来源回放阶段）；
源重新发布、结构漂移或文件摘要不匹配会以 `ODS_SOURCE_CONTRACT_INVALID` 失败；
源读取、类型转换或 COPY 失败使用 `ODS_STAGING_FAILED`，超时使用
`ODS_STAGING_TIMEOUT`。这些都是可审计终态，不会创建 ACTIVE 物化。

### 发布试跑与指标查询读取路径

构建路径和交互式读取路径使用不同的并发合同。DWD/DIM worker 从冻结 ODS 来源全量
抽取，DWS/ADS worker 读取冻结 materialization ID 对应的运行级不可变物理表。
DWD/DIM 草稿预览先从每个 ODS 来源截取 100 行再执行 DAG；DWS/ADS 预览和 DWS
指标试算读取当前 ACTIVE 物化对应的 `warehouse_published` 稳定视图。

```text
严格 DSL
  → 租户事务解析精确版本 + 当前发布指针 + ACTIVE materialization
  → 允许列白名单 + PostgreSQL 结构化编译
  → 执行事务 FOR SHARE 再校验全部绑定和视图 SELECT 权限
  → 参数化 SELECT warehouse_published
  → 查询审计 + 精确 materialization 绑定
```

解析器只允许把 DIM/DWD 的 ODS 节点展开到该版本冻结的单一物理来源，不执行任意
数据集递归，也不接受客户端 SQL、稳定视图名或物理表名。DIM 节点只能绑定当前发布
的 ODS 精确版本；DWD 节点至少绑定一个同条件的 ODS，并可绑定 ACTIVE DIM 或第一轮确认的次事实；
DWS 节点只能绑定一个或多个 ACTIVE DWD，或单个 ACTIVE DIM 形成 `ENTITY_COUNT`；ADS 节点只能绑定 ACTIVE DWS。任一节点缺失、失效、换版、换 ACTIVE
指针、摘要漂移或稳定关系异常都会在
执行前失败关闭。执行事务对 materialization、版本和所有者行加共享锁，阻止原子
激活在查询中途切换指针；实际 SELECT 同时取得 PostgreSQL 视图关系锁。

查询主记录用 `execution_engine=POSTGRES` 区分源 Connector 路径，不伪造
`data_source_id`。`query_run_materializations` 与候选预览对应表保存每个节点实际
读取的 dataset/version/materialization、稳定视图及 schema/snapshot hash，且与
主审计一样强制租户 RLS、禁止身份字段原地修改。API 角色只拥有
`warehouse_published` 的 USAGE/SELECT；运行级物理 schema 仍只对 worker 可见。

DWS 指标始终绑定指标定义中的精确 DWS 版本及其当前 ACTIVE 物化，不重放 DWS
DAG。可证明可分解的输出才允许再次汇总：SUM/MIN/MAX 保持聚合，COUNT 按 SUM
汇总；AVG、COUNT_DISTINCT、计算度量和当前参数化 DWS 失败关闭。指标草稿预览和
发布前验证都经过同一解析、锁定、执行和 materialization 审计路径。

## 7. 语义层和倒排检索

物理 `DIM` 与一级“语义维度”不是同一个对象。`DIM` 是从 ODS 抽离的实体说明表，
负责人物、商品、组织等实体键和描述属性；它通过 DWD/DWS DAG 被对齐到事实粒度。
语义维度则是分析者最终可以切分指标的逻辑轴，固定到精确 DWS 发布版本和字段。
这一边界保证成员检索、指标聚合和权限判断都发生在同一个已证明粒度的 DWS 上，
不会绕过事实关系直接拿 DIM 与指标做不受控 Join。

语义层的发布链路是：

```text
DIM/DWD 物理合同
  → DWS 主题与粒度
  → 一级语义维度 + 指标精确版本
  → VERIFIED 维度—指标关系
  → ADS / 报告 / API 消费
```

ADS 可以组合多个 DWS 的已发布结果，但不成为语义指标或维度的反向事实来源；需要
新增业务口径时先在 DWS/语义层形成可复用定义，再由 ADS 裁剪和展示，避免同一指标
在多个报表层重复定义。

DWS 发布版本上的维度成为一级对象。维度固定到精确 DWS 数据集版本和字段，记录类型、
基数策略、敏感性、定义摘要和状态。维度成员由物化表去重扫描得到，存储规范值、
归一化值、哈希、有效期与别名。

DWS 版本发布只登记待物化的维度勘测运行；精确 `ACTIVE DWS` 物化出现后，数据库
根据非度量字段生成可审计、可编辑的 `SUGGESTED` 候选。候选证据冻结版本、schema、
materialization、snapshot、row count 和字段元数据，不读取业务样本，也不会自动
发布。接受时再次验证当前发布/物化绑定，并在精确字段风险锁下重读已批准敏感标签。
同一次物化激活还会为每个候选字段登记冻结的 `dimension_profile_jobs`。普通字段只
计算行数、非空/空值数和有界 NDV，不保存样本、极值或 Top 值；文本 NDV 直接按
原字段类型和 `COLLATE "C"` 分组，不先转成 text。敏感字段和 `IDENTIFIER` 在读取
业务行前分别以 `SKIPPED_POLICY + NONE` 和
`SKIPPED_POLICY + EXACT_ONLY` 收口。历史已批准敏感绑定、非停用敏感维度、候选
风险或既往敏感跳过记录构成不可自动放松的版本内风险下限。

`FULL` 只有在当前发布版本、当前 `ACTIVE DWS` 物化和该精确字段的
`SUCCEEDED + FULL` 画像同时成立时才可发布和刷新。新物化激活会立即把旧
`PUBLISHED + FULL` 维度收紧为 `NONE`，清空刷新代际和最后任务指针；画像完成后
仍需显式评审恢复 `FULL`。接受 `FULL` 候选会在同一事务登记固定物化、
100,000 成员/60 秒边界的刷新任务；新代际任务成功前，旧成员及别名一律不进入
列表或成员到指标检索。

完整成员刷新使用同一专用 PostgreSQL 连接、单个 `READ COMMITTED` 事务内的
“扫描 + late-gate merge” fenced 流程：

1. 扫描阶段只锁定并读取任务登记的 run-scoped DWS 物理表，不取得租户治理门，
   也绝不读取或锁定 `warehouse_published` 稳定视图。每批最多 1,000 行进入 scratch 临时表，
   随即在 PostgreSQL 内规范去重并以 `ON CONFLICT` 合并到最多
   `maxMembers + 1` 的持久会话临时 stage，再清空 scratch；Go 堆内不维护全量成员
   map。物理表上的 `SHARE` 锁一直保留到 generation 提交，等待中的 DML 不能穿过
   scan/merge 边界。
2. 扫描结束但不提交事务；late-gate 阶段才取得租户语义治理门，按照物化 → 数据集 → 字段 →
   任务/维度行的统一顺序加锁，重新验证 lease、维度版本、策略/敏感性、发布指针、
   精确物化、schema/snapshot、`SUCCEEDED + FULL` 画像，以及稳定视图 owner 和
   对物理表的唯一依赖，然后原子完成成员新增、更新、停用和 generation 切换。
   `READ COMMITTED` 使这些重读能看到扫描期间已经提交的治理变化。

“late-gate 阶段”表示其中不再执行源表全量 `DISTINCT`，并不表示常数时间：成员
INSERT/UPDATE/DEPRECATE 仍随成员量增长，当前最大可达 1,000,000 个，整个 merge
期间仍持有 tenant governance gate。它保证单代切换与治理写入串行化，但也形成需要
监控和容量规划的写入临界区。物化激活可在长扫描期间完成；若其切换了稳定视图或
当前 ACTIVE 指针，merge 栅栏以 `REFRESH_SOURCE_CHANGED` 或 lease 失效拒绝旧
stage，不污染新代际。临时表在连接归还池前删除，清理失败则丢弃该物理连接。

“run-scoped 物理表 ACTIVE 后不可变”目前是 report_worker 的可信生命周期边界，
不是对已攻陷 owner 的数据库绝对封存。`SHARE` 只阻止本次刷新事务并发的
`INSERT/UPDATE/DELETE/TRUNCATE`；事务提交后 owner 仍能主动修改或移除保护对象。
生产强化方向是把构建写角色与 immutable owner/清理角色分离，或为 ACTIVE 表安装并
核验拒绝 DML 的保护触发器，同时为历史 ACTIVE 物化做迁移回填。

成员读取不是只靠租户 RLS。`members`、成员别名和成员到指标检索会在同一 SQL
快照中使用 actor 身份重新判定：

- actor 必须具有全局 `DATASET:READ`，或目标 DWS 数据集的用户/角色对象级
  `DATASET:READ`；
- 只要该数据集存在适用于 actor 的任一行策略，预计算成员索引就不能安全裁剪；
- 只要维度字段存在适用于 actor 的任一非 `ALLOW` 列策略，包括 `DENY`、`MASK`、
  `AGGREGATE_ONLY`、`NULLIFY` 或 `HASH`，也禁止成员枚举。

精确指定维度的 members/aliases 请求稳定返回
`403 SEMANTIC_MEMBER_ACCESS_DENIED`，且不携带成员值；未指定维度的别名目录和
跨维度 member-metric-search 则静默过滤无权或受策略限制的维度，使“无权命中”和
“不存在”都表现为空。指标检索还必须同时满足指标数据集的读取权限。这些规则不会把
预计算索引误当成可以逐行执行 row policy 的查询结果。

检索链路不是物化“维度成员 × 指标”的笛卡尔积，而是分解为：

```text
查询词
  → dimension_members / dimension_member_aliases
  → semantic_dimensions
  → dimension_metric_compatibility
  → metric_versions
  → dataset_versions
  → 当前 ACTIVE materialization / warehouse_published 视图
```

`dimension_metric_compatibility` 只保存较小的维度—指标兼容关系，并明确：

- `DIRECT / BRIDGE / DERIVED` 关系类型；
- `SAFE / DEDUPLICATE / UNSAFE` 扇出策略；
- 最多 8 跳的可审计 Join 路径、证据来源、置信度和人工验证状态。

Join hop 只能声明起止 DWS 精确版本、起止字段和
`ONE_TO_ONE / MANY_TO_ONE / ONE_TO_MANY / MANY_TO_MANY` 基数。相邻 hop
必须拓扑连续，首跳从语义维度字段开始，末跳落到指标 DWS；字段必须是非度量角色且
规范类型相容。`SAFE` 只接受不会放大事实的 `ONE_TO_ONE / MANY_TO_ONE`。
API 严格解码后，服务再次读取当前发布指针和字段合同；关系验证还要求全部 hop
版本都有 ACTIVE 物化。数据库约束承担最终结构封口，LLM 只能提议这种结构化关系，
不能提交 SQL、表达式或物理表名。

同一精确 DWS 中，指标 `allowedDimensions` 与已发布一级维度的字段交集是确定性
事实。任一侧发布时，数据库直接产生
`DIRECT + SAFE + RULE + confidence=1 + PROPOSED` 关系并保留人工验证门，不调用
LLM；迁移会补齐历史交集，已存在或已拒绝的唯一关系不会被覆盖。LLM 只处理控制面
不能确定的跨 DWS、桥接或派生路径，因而请求更小、失败重试更少，也不会对已知关系
重复花费模型调用。

成员值和成员别名先分别通过以 `(tenant_id, normalized_*)` 开头的精确索引形成
候选集合，按成员优先保留别名命中后才关联关系和指标；不会为了跨维度搜索先扫描
全部活动成员。命中结果同时返回维度侧精确版本/字段和已经验证的 Join hop，供下游
规划器复用，不需要再让 LLM 从名称重新推断关系。

只有已验证且非 `UNSAFE` 的关系可以用于自动回答。敏感维度被数据库约束为只能
使用 `NONE`，高基数维度只能采用 `EXACT_ONLY` 或 `NONE`；敏感成员不会从
列表或指标搜索 API 枚举。所有维度成员检索都直接使用租户内成员/别名表，不生成
语义文档，也不发送给外部 embedding provider。

名词转换作为别名而不是硬编码特殊分支。例如“690 → 智家生态圈”保存为维度成员别名或受控标签别名，带来源、有效期和审计人；查询规范化后仍保留原始词用于解释和审计。

## 8. 指标合同

指标版本继续使用严格的结构化定义，当前可执行合同包含：

- 指标名称、编码、说明；`metric.description` 是正式版本中的业务口径正文；
- `ATOMIC / DERIVED / RATIO` 类型及精确原子指标依赖；
- 允许维度与时间粒度；
- 精确数据集版本所固化的过滤范围；
- 单位、数字格式、可加性与空值/除零策略；
- 聚合方式；
- 精确数据集版本、字段表达式和上游指标版本血缘。

指标执行不接收客户端 SQL。服务端从已发布 DWS 数据集版本和指标定义派生查询计划。
维度—指标兼容关系当前用于成员到指标的语义检索和自动回答候选筛选；v1 指标试算/
发布尚未注入该验证器，执行阶段只使用指标定义内的允许维度白名单和 DWS 再聚合安全
门。因此在兼容验证器真正接入前，不能把语义检索结果直接描述成已经受兼容关系保护
的指标执行。

当前 `metric-definition-v1` 没有可由单个指标另行放宽的数据集外
`modifiers/fixedFilters` 字段：固定修饰条件继承自精确数据集版本，内部指标候选会
另存 `caliber`、过滤摘要和结构化血缘供创作审核，但不能改变执行口径。若业务需要
“同一数据集、不同指标各自带固定修饰词”，应升级为新的指标合同版本，使用字段/
一级维度的精确引用和受限表达式执行，不能把自由文本口径当成可执行过滤。

候选 v2 合同、DWS 直接字段型 MVP、严格 NULL/时区/敏感性规则，以及未来
`ScopedAggregate` IR 见
[`指标定义 v2：结构化口径与修饰词设计`](./metric-definition-v2-design.md)。该方案
当前明确标记为 **DESIGN ONLY / NOT YET ACCEPTED BY RUNTIME**；现有 API、Schema、
Go 服务、数据库和查询运行时仍只承诺 v1，架构文档中的链接不构成 v2 上线声明。

## 9. 安全与运维边界

- 所有控制表启用并强制租户 RLS；跨租户 ID 表现为不存在。
- API 与 worker 不信任客户端 SQL、物理标识符、层级或输入快照。
- 源库账号必须只读；应用层词法校验不能替代数据库授权。
- 生产 Connector 必须显式配置 `CONNECTOR_EGRESS_ALLOWLIST`，且只接受
  `IP/CIDR:port`；数据源仍可填写 DNS 名，但每次新建物理连接时解析出的**全部**
  地址都必须落入对应端口的授权 CIDR。通过校验后驱动直连一个确定的已验证 IP，
  原始 hostname 只用于连接池身份与审计，阻断常规 DNS rebinding。
- 生产还必须显式配置平台控制面 CIDR 的 `CONNECTOR_EGRESS_DENYLIST`，deny
  优先于 allow；loopback、link-local、multicast、unspecified、reserved 和云
  metadata 地址，以及所有 IPv4-mapped IPv6 地址，即使误入 allowlist 也会被拒绝。
  hostname allowlist 只允许本地开发 compose 使用，生产配置中出现即启动失败。
- 应用 allow/deny 与 IP pinning 不是网络隔离的替代品。Connector 所在子网的
  Security Group / NetworkPolicy / 主机防火墙必须默认拒绝出站，只放行批准的
  数据库 CIDR 与端口，并明确阻断平台 PostgreSQL、Redis、MinIO、API 和云控制面。
  Oracle listener/SCAN 可能在协议握手后下发重定向地址，该后续连接不受首次 DSN
  DNS pinning 的完整证明；代理配置错误、驱动漏洞和已攻陷进程也属于同类残余。
  因此 Oracle 重定向目标约束及控制面不可达性仍以网络层作为硬门和验收项。
- `warehouse_*` 对 `PUBLIC` 无权限。生产部署应使用独立 warehouse executor DSN：执行角色拥有数据面 `USAGE/CREATE`，API 角色仅拥有必要的控制面权限和发布视图读取权限。
- 动态表需要保留期和清理任务：失败 staging、retired 物化和过期运行分开配置 TTL，清理前检查活动指针与血缘引用。
- retired 物理表清理还必须检查 `QUEUED/RUNNING` 构建的冻结输入（精确 materialization ID，或数据集版本 + schema/snapshot hash）；只要仍有进行中的构建引用该快照，就不能删除其不可变物理表。
- 监控至少覆盖构建及标签建议队列等待、租约恢复、标签建议跳过/失败率、源端读取量、
  COPY 吞吐、池注册表/全局物理连接使用率、空闲 LRU 淘汰、全局连接等待/超时、
  one-shot 测试连接数、各级字节预算使用率与拒绝码、文件读取/展开预算拒绝、断流
  连接丢弃、出站 DNS/allow/deny 拒绝、构建耗时、质量失败、Outbox 积压、
  embedding 失败和成员索引基数。
- 成员刷新需要把扫描和 merge 分开观测：扫描行数/耗时、规范去重数、超基数、
  `REFRESH_SOURCE_CHANGED`、过期 lease、stage 清理失败、merge 行数/耗时，以及
  tenant governance gate 等待/持有时间。日志和指标只能带租户、任务和稳定结果码，
  不得把 hostname、IP、凭据、SQL、成员/样本值作为高基数标签或错误正文。

## 10. 交付顺序

1. 数据源不可变版本、测试证据和显式发布；
2. 数据集层级合同与历史回填；
3. 物化控制表、执行计划、租约、质量门和数据面 schema；
4. 同源 PostgreSQL CTAS、跨源流式 staging；
5. 标签本体、资产绑定、Outbox 与向量文档；
6. DWS 维度勘测、成员索引和维度—指标兼容关系；
7. 管理 API/UI、调度、数据保留策略和独立执行角色；
8. 按租户/数据域灰度迁移，双读校验后再关闭旧实时链路。

在灰度期，旧数据集可以继续预览，但新物化链路只接受符合层级合同的已发布精确版本。不得自动把历史任意 TABLE 输入视为已治理的上游层级。
