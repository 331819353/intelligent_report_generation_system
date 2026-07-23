# 元数据标签向量检索落地方案

> 实施状态：已于 2026-07-22 完成代码、迁移、存量回填、50 条中文召回基准和真实 DAG 验收，本地开发环境已启用 `HYBRID`。

## 1. 实施结论

元数据补全与 DAG 资产检索的完整链路已打通：

1. 数据表导入任务会持久化技术元数据，读取最多三行样本，再调用 LLM。
2. LLM 以严格 JSON Schema 返回表/字段业务名称、业务描述、标签、敏感级别和字段语义类型。
3. 结果经过目标 ID、标签枚举、置信度、结构版本和人工锁定校验后，写入 `metadata_tables`、`metadata_columns` 及审计表。
4. 补全成功后会创建或更新映射数据集，并在同一业务事务内合并写入资产 embedding outbox。
5. 独立 Worker 使用版本化确定性文档生成 `TABLE/COLUMN` 向量，并以 `input_hash + model` 实现幂等跳过、租约防旧结果覆盖和有界重试。
6. DAG 生成前使用向量 top 32 和关键词 top 32 的 RRF 混合召回，先选表再限定表内排序字段；表/字段标签会进入向量文档、关键词打分和模型上下文。
7. 数值、时间和布尔语义类型已增加确定性相容性门禁；不相容 LLM 建议保持待确认，手工接受和直接编辑也会再校验，不会污染检索文档。

指标侧已经先行落地同一模式：数据集发布后的内部原子度量事实由确定性规则固定可执行事实，再由 LLM 补充名称、说明、口径文字、周期说明、血缘摘要和标签；其语义文档写入 `platform.metric_semantic_documents`，供受权限约束的指标创作检索使用，但不进入指标中心或公开指标检索。该实现可复用其 Provider、Worker、租约重试、降级和租户隔离方式，但不能把指标文档表直接复用于表/字段资产。

## 2. 已拍定的技术方案

### 2.1 存储与模型

- 向量统一存放在平台控制库 PostgreSQL，不写回 MySQL、Oracle 等业务数据源。
- 控制库切换为与 PostgreSQL 17 兼容的 pgvector 镜像，并由迁移执行 `CREATE EXTENSION IF NOT EXISTS vector`。
- embedding 使用独立配置：`AI_EMBEDDING_BASE_URL`、`AI_EMBEDDING_API_KEY`、`AI_EMBEDDING_MODEL`、`AI_EMBEDDING_DIMENSIONS`、`AI_EMBEDDING_TIMEOUT`。不得把 `deepseek-v3` 对话模型当作 embedding 模型。
- 当前服务的 `/models` 与 `/embeddings` 合同已经实测通过，模型锁定为 `Qwen3-Embedding-4B`，维度锁定为 2560。模型或维度变化采用新版本回填，不能原地混用。

新增 `platform.asset_embeddings`：

| 字段 | 说明 |
|---|---|
| `tenant_id` | 租户隔离键 |
| `asset_type` | `TABLE` 或 `COLUMN` |
| `asset_id` | 表或字段稳定 ID |
| `table_id` | 所属表 ID，便于字段二次召回 |
| `embedding` | 固定维度 `halfvec(n)`；当前 2560 维超过 HNSW `vector` 的 2000 维上限，使用 pgvector 原生 FP16 类型 |
| `model` / `model_version` | embedding 模型版本 |
| `document_version` | 检索文档模板版本 |
| `input_hash` | 规范化输入 SHA-256，保证幂等 |
| `status` / `error_code` | 生成状态与可重试错误分类 |
| `updated_at` | 最近成功生成时间 |

表启用 RLS，并建立租户内 HNSW cosine 索引。另建 `platform.asset_embedding_outbox`，用唯一键合并同一资产的重复更新。

### 2.2 向量输入

使用版本化、确定性的检索文档，不直接对单个标签字符串分别建向量：

- 表文档：数据源类型、schema、物理表名、业务表名、表描述、表标签，以及所有活动字段的业务名、物理名、描述、标签、语义类型。
- 字段文档：所属表业务名、字段物理名、字段业务名、字段描述、字段标签、语义类型、规范类型。
- 标签先去空白、去重、排序；字段按 ordinal position 排序，确保相同元数据得到相同 `input_hash`。
- 不包含数据库密码、连接串、样本行或其他业务数据。

表向量用于第一阶段选表；字段向量只在入选表范围内做第二阶段字段排序。这样既利用字段标签帮助选表，也避免全库字段 ANN 放大权限和成本边界。

### 2.3 生成时机与一致性

- 元数据 LLM 建议成功应用、人工修改表/字段业务元数据、技术结构刷新、资产启停时，在原业务事务内 upsert outbox 事件。
- 在写入和索引前增加确定性质量门：`AMOUNT/PERCENTAGE/QUANTITY` 只能用于数值字段，`DATE/TIME/DATETIME` 只能用于相容时间字段，`BOOLEAN` 只能用于布尔或明确可映射字段；不相容建议进入人工确认，不能自动应用或生成向量。
- embedding worker 在事务提交后批量读取规范化文档、调用 embedding 服务并幂等 upsert 向量。
- `input_hash` 未变化时跳过调用；变化时生成新向量后原子替换。
- 资产导入不能因 embedding 服务短暂不可用而整体失败；映射表保持可用，embedding 状态为 `PENDING/FAILED` 并重试。
- INACTIVE、已删除或未完成元数据补全的资产不得进入召回；失活时立即从可检索集合排除。

### 2.4 DAG 混合召回

将当前“最多读取一批资产后本地字符串排序”替换为租户内混合召回：

1. 对用户指令生成一次查询向量。
2. 分别取向量 top 32 与关键词 top 32，使用 RRF 融合；表 ID/字段 ID hints 和当前 DAG 引用的表永远强制保留。
3. 在权限过滤、ACTIVE、ENABLED、补全成功条件下选出表 top 12；所有过滤在数据库查询阶段完成，不能先跨租户召回再过滤。
4. 仅为入选表加载字段，并结合字段向量、精确名称、语义类型和标签排序，继续受现有 160 字段和模型输入字节预算限制。
5. 将入选资产的表/字段标签加入 `CatalogTable`、`CatalogColumn`，供 DAG 模型解释关联与角色；本地 DAG 结构、字段、类型和安全校验保持最终可信边界。
6. embedding 不可用或覆盖不完整时自动回退到改进后的关键词检索，不能退化为“无资产”。

运行开关固定为 `DATASET_AI_RETRIEVAL_MODE=LEXICAL|SHADOW|HYBRID`。先以 `SHADOW` 记录召回差异，不改变线上结果；通过验收后切到 `HYBRID`。

## 3. 实施记录

- [x] **基础能力**：pgvector/halfvec 迁移、独立 embedding 配置、Provider Adapter、向量与 outbox 表、HNSW 索引及 RLS。
- [x] **质量门与索引流水线**：语义类型相容性、确定性文档、批量 Worker、租约重试、事件触发和存量回填。
- [x] **混合召回**：tenant-safe Retriever 已接入 DAG `loadCatalog`，保留 hints/current 强制项、中文双字切分和关键词降级。
- [x] **模型上下文**：`CatalogTable`、`CatalogColumn` 已包含标签，并继续受 12 表、160 字段和 Provider 字节预算约束。
- [x] **影子验证与切换**：支持 `LEXICAL|SHADOW|HYBRID`；开发环境基准达标后已切换为 `HYBRID`。

## 4. 验收结果

| 验收项 | 2026-07-22 结果 |
|---|---|
| 存量回填 | 8 条 `TABLE` 和 45 条当前合格活动 `COLUMN` 向量均为 `SUCCEEDED`；28 条失活/未完成资产被明确 `SKIPPED`。 |
| 指定语句 | top 10 包含销售订单、Oracle 门店和 Oracle 区域；生成 3 节点、2 Join、1 Group 的 MySQL + Oracle 跨源 DAG，日期粒度为 `MONTH`，本地安全校验通过。 |
| 50 条中文基准 | 107/107 目标表命中，table recall@10 = 100%。数据集位于 `testdata/asset-retrieval-zh.json`，可用 `make verify-asset-retrieval` 重跑。 |
| 幂等与局部重建 | 数据库验证证明字段标签变更只提升所属表和该字段的 `event_version`，outbox 仍只有两条唯一事件；未变 `input_hash + model` 时 Worker 直接确认而不再调 Provider。 |
| 质量与隔离 | 语义类型单元测试、LLM 建议分流、手工接受复核、跨租户 RLS 与无租户 RLS 均通过；检索 SQL 在查询阶段再次过滤停用/失活/未补全资产。 |
| 降级 | Provider 失败会记录稳定错误码并重试；Retriever 自动使用中文关键词召回，DAG 目录层仍会以改进关键词排序补齐，不返回虚假“无资产”。 |
| 性能与可观测性 | 100 次本地 ANN 查询 p95 = 2 ms；50 条 query embedding + 混合召回 p95 = 86 ms。运行时记录模式、候选 ID/分数、耗时、模型就绪和稳定降级原因，不记录凭据或样本值。 |

## 5. 已验证的外部前提

2026-07-22 已使用当前 OpenAI 兼容服务完成只读能力检查：服务列出 `Qwen3-Embedding-4B`，单条中文检索文本调用成功，返回 2560 维向量。实现时默认复用同一 Base URL，但使用独立环境变量和 Provider Adapter；生产环境可为 embedding 配置独立密钥。聊天模型保持 `deepseek-v3`，embedding 模型保持 `Qwen3-Embedding-4B`。
