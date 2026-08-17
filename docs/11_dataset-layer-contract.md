# 数据集层级合同（layer × lineage）

> 状态：已实现。核心代码位于 `internal/dataset/model.go`（`Layer` / `Lineage` 定义）、
> `internal/dataset/dsl.go`（`validateLayerContract`、`Document.Lineage()`）、
> `internal/dataset/layer.go`（`ValidateLayerDependencies`）、
> `internal/materialization/validation.go`、`web/src/lib/dataset-layer.ts`，
> 数据库栅栏见 `migrations/000360_source_lineage_any_layer.up.sql`。

## 1. 为什么重新定义

早期约束把“层级”和“加工链路”绑在一起：只有 ODS 可以直接映射物理表，DIM/DWD/DWS/ADS
必须逐层从已发布的上游数据集版本加工得到，唯一例外是服务端分类器给源端已汇总表打上
`sourceMode: PRE_AGGREGATED` 后允许以 DWS 直落。

实际实施中，用户提供的往往就是一张 DWS 或 ADS 级别的宽表（历史数仓产物、导入的 Excel
汇总表、第三方系统的结果表）。强制它们先落 ODS 再逐层重建，既没有可复原的血缘，
也没有价值——层级不应该被“怎么来的”绑架。

## 2. 新定义：层级只描述粒度，血缘由拓扑推导

### 2.1 `layer` = 粒度合同

| 层级 | 一行代表什么 | 对输出的硬性要求 |
|---|---|---|
| ODS | 与物理源表逐行一致 | 单个 TABLE 节点、无 Join、不去重、不分组/聚合；不要求粒度键 |
| DIM | 一个业务实体 | 必须声明实体粒度说明与业务键；不允许业务分组/聚合 |
| DWD | 一条业务事实/事件 | 不去重、不分组/聚合；可关联维度；不要求粒度键 |
| DWS | 一个汇总统计粒度 | 必须声明粒度说明与粒度键；分层加工须有聚合指标，源表直落须至少一个度量字段 |
| ADS | 一个面向消费场景的输出粒度 | 必须声明粒度说明与粒度键；语义合同 1.0 需绑定消费合同 |

层级在首次发布后不可变（`Service.Update` 保持原有规则）。

### 2.2 `lineage` = 血缘方式（由 DSL 拓扑推导，不单独持久化）

```
SOURCE   恰好一个物理 TABLE 节点 且 无 Join      → 源表直落
MODELED  其他情况（全部 DATASET 节点，或历史多表正文） → 分层加工
```

对应 Go 判据 `Document.Lineage()`，前端判据 `datasetLineage()`，数据库判据
`platform.dataset_version_is_source_lineage(version)`。三处必须保持一致。

| 血缘 | 允许的层级 | 拓扑与粒度要求 | 上游校验 | 物化路径 |
|---|---|---|---|---|
| SOURCE | ODS / DIM / DWD / DWS / ADS 全部 | 单表、无 Join；ODS 以外的层级同样不允许去重、分组、再次聚合（保持源表既有粒度） | 无上游数据集版本可解析，直接通过 | `extract → stage → materialize` 三节点源抽取计划（原 ODS 路径），任意层级 |
| MODELED | DIM / DWD / DWS / ADS | 显式声明层级时只能引用已发布 DATASET 版本 | `DIM ← ODS`；`DWD ← ODS(+DIM)`；`DWS ← DWD/DIM`；`ADS ← DWS` | 治理物化：上游物化 → Join/Filter/Aggregate/Project → materialize |

要点：

- **完整链路仍是标准建模路径**。AI 建模、`ValidateLayerDependencies`、控制面输入推导、
  worker 上游校验、`enforce_build_run_input_layer` 触发器都继续按 ODS→DIM/DWD→DWS→ADS
  方向约束 MODELED 血缘。
- **直落只是把“进入数仓的层级”交给声明者**。系统分类器为映射表默认给出 ODS（高置信汇总表给
  DWS），用户可以在保存前把单表画布改成任何层级；层级要求（粒度键、度量）照常校验。
- **多表 Join 不能直落**。物化平面只支持单源抽取，跨表加工必须先各自落层再分层建模。
- **需要改变粒度就不是直落**。想对物理表分组、去重、聚合，请先落 ODS 再建 DWS/DIM。

### 2.3 物理表的“层级:”标签：清洗时判定，人工可改

物理表进入数仓的层级由**元数据清洗（metadata AI）在完善表资产时判定**，写成恰好一个
受控标签 `层级:ODS|DIM|DWD|DWS|ADS`（`metadata-completion-v16` 起为表级必填；字段资产不携带）。
判定的是“这张表现在已经处于哪一层”，不是“将来应加工到哪一层”：

| 层级标签 | 典型证据 |
|---|---|
| `层级:ODS` | 业务系统交易/事件/流水/状态明细、未清洗的贴源导入表 |
| `层级:DIM` | 客户、商品、门店、组织、日历等实体主数据/维度表 |
| `层级:DWD` | 已按统一口径清洗、一行一个事实/事件的明细宽表（`dwd_`、含维度冗余字段） |
| `层级:DWS` | 每行是按维度汇总的统计结果（`agg_`/`dws_`/summary/汇总/指标；度量列为汇总值） |
| `层级:ADS` | 面向报表/看板/应用直接消费的结果宽表（`ads_`/report/rpt/看板/驾驶舱） |

证据不足时按 ODS 优先于 DWD、DWD 优先于 DWS 保守选择。之后人工可以在资产页的
“数仓层级”下拉框（或直接编辑标签）改写；表资产至多一个合法层级标签（`asset.BusinessMetadata.Validate`）。

该标签的下游作用：

- **默认映射数据集**：`ClassifyMappedDatasetLayer` 以层级标签为准（DIM/DWS/ADS 还需有键/度量证据，
  否则保守回落 ODS）；人工改写层级标签时 `UpdateTable` 在同一事务里重新对齐尚未人工修改/发布的
  映射数据集（已发布数据集层级不可变，需新建）。
- **数据集画布**：来源目录里的物理表按标签层级归入 ODS/DIM/DWD/DWS/ADS 分组（无标签视为 ODS；
  已发布默认映射数据集的物理表由该数据集在其层级分组中代表）；单表直落时 `chooseDatasetLayers`
  把标签层级放在首位作为默认值。
- **手工完善**：表资产没有层级标签时不能标记为已完善。
- 共享判据在 `internal/warehouselayer` 与 `web/src/lib/warehouse-layer.ts`；
  迁移 `000361` 为已生成映射数据集的历史表回填与现有数据集层级一致的标签。

### 2.4 `sourceMode` 降级为遗留标记

- `sourceMode: PRE_AGGREGATED` 只为解码历史 DSL 保留：只能出现在 SOURCE 血缘的 DWS 上，
  语义等价于普通的“DWS 直落”，不再承担任何校验或路由职责。
- 已发布版本原样解码、原样编码，保证 `schema_hash` 稳定；分类器与前端不再写入该字段。
- 未提交的 `HISTORICAL_DIRECT` 方案已整体移除，被本定义取代。

## 3. 各处落点

| 位置 | 变化 |
|---|---|
| `internal/dataset/dsl.go` | `validateLayerContract` 按 layer × lineage 重写；`Document.Lineage()`；`IsSourceBackedMaterialization` = SOURCE 血缘 |
| `internal/dataset/layer.go` | SOURCE 血缘直接通过上游层级校验 |
| `internal/dataset/postgres_store.go` | 发布后处理按血缘分流：SOURCE → 源抽取入仓 outbox；MODELED → 治理物化 outbox；移除 sourceMode 与 origin_table 绑定检查 |
| `internal/dataset/publication_approval_postgres.go` | 领域血缘校验对 SOURCE 血缘跳过 |
| `internal/dataset/mapped_dataset.go` | 分类器只决定默认层级，不再写 sourceMode |
| `internal/dataset/service.go` | 语义命名只服务 MODELED 血缘的 DWD/DWS/ADS |
| `internal/materialization/validation.go` | SOURCE 输入对任意目标层合法；仓库输入按 `modeledUpstreamLayers` |
| `internal/materialization/control_postgres.go` | 输入推导：SOURCE 血缘取物理表；MODELED 按方向表 |
| `internal/materializationworker/ods_resolver.go` | 源抽取解析器接受五个层级 |
| `internal/warehouse/executor.go` | 运行期租户/层级边界：run-scoped stage 对任意层级合法 |
| `migrations/000360_*` | `dataset_version_is_source_lineage()`；重写 `enforce_build_run_input_layer`、`enforce_build_run_required_input_layers`、`enforce_dataset_domain_lineage`、`system_mapped_source_layer_allowed` |
| `web/src/lib/dataset-layer.ts` | `datasetLineage()`、`chooseDatasetLayers()`（SOURCE 开放五层，默认 ODS）、`sourceLayerRequirement()` |
| `web/src/pages/DatasetCenterPage.tsx` | 保存对话框按血缘提示可选层级与该层要求；移除“历史数据直接指定层级”开关 |
| `api/schemas/dataset-dsl-v1.schema.json` | `layer` 说明更新；`sourceMode` 标记 deprecated |

## 4. 兼容性

- 历史 DSL（未声明 `layer`）继续按 `InferLayer` 推断并 grandfather：单表聚合正文推断为 DWS，
  不施加直落粒度要求。
- 已发布的 `PRE_AGGREGATED` DWS：DSL 与哈希不变；下一次元数据刷新时分类器会生成不带该标记的
  新草稿并按既有刷新流程重新发布一个版本（一次性）。
- 回滚 `000360` 后，除 ODS / PRE_AGGREGATED DWS 之外的直落版本无法再注册构建输入（失败关闭）。
