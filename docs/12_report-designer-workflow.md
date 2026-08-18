# 智能报表（报告）开发工作流

> 状态：手动编辑主链已实现。服务端 `internal/report/{model.go,operation,compiler,http,runtime,publication}`，
> 前端 `web/src/pages/ReportEditorPage.tsx`、`web/src/report/{designer,render,runtime,api}`。
> 报表（DASHBOARD）与报告（REPORT）共用同一份 Report Definition 1.0、同一个编辑器、同一条发布链。

## 1. 三条原则

1. **以数据集为基础绑定卡片。** 报告先声明它用到的数据集版本（`definition.dataContexts`，只能来自当前用户可读的
   已发布数据集目录），卡片再把维度/度量绑定到某个数据集的字段（`DATA_BINDING_UPDATE`）。字段的度量/维度角色
   来自数据集 DSL（`platform.dataset_fields.field_role`），列级权限在服务端裁剪后才进入编辑器。
2. **以 JSON 配置文件为核心。** 报表开发就是一份 `report-definition-v1` JSON 的开发：数据集、筛选器、页面/章节/
   卡片（Block）/区域（Zone）/槽位（Slot）、组件与绑定、联动、运行策略全部在其中。前端与后端读同一份 JSON：
   后端负责校验、归一化、执行数据；前端负责画布与渲染（编辑态与运行态同一套渲染器 `ReportPageView`）。
   编辑器「定义 JSON」可查看/复制/下载；新建页「从 JSON 导入」把任何一份合法定义变成新的草稿。
3. **所有修改都是受控 Operation。** 拖拽、缩放、加卡片、改绑定、加数据集、加筛选器都通过
   `POST /api/v1/reports/{id}/operations` 提交为新修订（Report Operation v1，共 43 种），可撤销/重做；
   AI 改稿产出的也是同一种 Operation，且 AI 不允许添加/删除数据集（`DATA_CONTEXT_*` 不在 AI 允许集内）。

## 2. 手动开发流程（补全版）

主链按「新建 → 选类型 → 选数据集 → 拖卡片 → 点卡片绑定 → 选指标 → 配过滤字段 → 保存/发布」组织；
每一步都只需要一个面板，编辑器不再一上来就把所有选项摊开。

| 步骤 | 界面 | 写入的定义 / Operation |
|---|---|---|
| ① 新建 | 报告中心「新建报告」→ **弹窗向导**：第 1 步选类型（报告 REPORT / 报表 DASHBOARD）并命名；第 2 步勾选该报告要用的数据集（可多选、可搜索，只列当前领域已发布的数据集版本） | `POST /reports/blank {name, reportType, dataContextIds[]}`（第一个为主数据集；服务端逐个按目录校验）。模板 / AI 生成 / JSON 导入仍在 `/reports/new` 页 |
| ② 拖卡片 | 编辑器左侧 **组件面板**（按 图表 / 表格 / 内容 / 控件 分组）→ 拖到画布落点，或点击加到当前章节空位；空报告拖入第一张卡片即自动建分区 | `COMPONENT_CREATE` + `BLOCK_CREATE`（+ `SECTION_CREATE`）；落点与既有卡片重叠时附带 `BLOCK_MOVE/RESIZE` 让位；绑定按字段角色预填合同下限，数据集取报告主数据集 |
| ③ 布局 | 画布拖拽把手移动、右下角缩放；碰撞自动消解；章节上移/下移 | `BLOCK_MOVE` / `BLOCK_RESIZE` / `SLOT_UPDATE` / `ZONE_REORDER` / `SECTION_REORDER` |
| ④ 点卡片绑定 | 点击卡片 → 右侧 **卡片配置**（内联，不再弹窗）：**数据集** → **展示类型** → 标题/副标题 | `COMPONENT_UPDATE`、`COMPONENT_REPLACE`（换类型时绑定按新合同重映射、表现属性按新清单裁剪） |
| ⑤ 选指标 | 同一面板：**指标（度量）** 与 **维度** 行（角色下拉 + 字段下拉），**按字段角色填充**（确定性）或 **AI 识别度量与维度**；表现设置按清单 optionSchema 生成；「保存卡片配置」一次提交 | `DATA_BINDING_UPDATE`（+ 上述 UPDATE/REPLACE）合并为一条修订 |
| ⑥ 配过滤字段 | 同一面板下方 **过滤字段**：只列作用于这张卡片的过滤器；新增默认作用范围=这张卡片，数据集默认=卡片数据集；类型按字段语义建议（时间→日期区间、度量→数值区间、其它→多选） | `FILTER_CREATE` / `FILTER_UPDATE` / `FILTER_DELETE`；取值在运行页顶部筛选栏填写，服务端解析 |
| ⑦ 报告级设置 | 未选中卡片时右侧为「数据与筛选」：数据集增删（被引用的不能删）、全报告筛选器 | `DATA_CONTEXT_CREATE` / `DATA_CONTEXT_DELETE`、`FILTER_*` |
| ⑧ 联动 / 结论 | 卡片配置下方「结论证据」「图表联动」 | `INTERACTION_*` / `INSIGHT_*` |
| ⑨ 保存 | 每次操作即修订（`r1, r2, …`），撤销/重做；草稿按当前用户权限实时执行预览 | `POST .../undo|redo`，`POST .../draft/execute` |
| ⑩ 发布 | 「预览与发布」→ 发布评审（确定性门禁 + 可选 AI 说明）→ 发布人确认桌面/移动布局 → 版本 | `POST .../publish-review`、`POST .../publish`、版本/回滚/升级 |
| ⑪ 运行 | `/reports/{id}`：筛选栏 + 卡片；交互（点击筛选/钻取）由服务端解析 | `POST .../runtime/execute` |

前端入口：`web/src/report/designer/{NewReportDialog,ComponentPalette,DataPanels,operations}.tsx|ts`，
`web/src/pages/ReportEditorPage.tsx`（`CardInspector`、`dropFromPalette`）。

## 3. LLM 在卡片上的角色

- **识别度量与维度**（⑤）：`POST .../ai/card-binding`（`reportai.SuggestCardBinding`，提示词 `report-card-binding-v1`）。
  输入是卡片的组件合同（维度/度量上下限、角色白名单）+ 所选数据集经列权限裁剪的字段目录（含角色/语义类型）+ 标题/意图；
  输出经 `ValidateCardBindingSuggestion` 按字段目录、角色白名单与数量校验，只作为建议填入面板，
  保存仍走 USER 操作。没有模型提供方时按钮不可用，「按字段角色填充」始终可用——报告主链不依赖模型。
- 内嵌的 AI Schema 在启动时剥离 `$schema`/`$id`（`orchestrated.go aiFacingSchema`）：AI 平台的严格结构化输出
  合同不接受这两个元数据关键字，此前 AI 生成/上下文选择/发布评审因此被拒绝。
- **整体生成**（①）：`/reports/ai/create` 由模型规划章节与组件、服务端确定性实例化；**改稿**：AI 改稿会话。
- 结论文本只能写标记，不写数字；证据由服务端从真实执行派生（见 docs/06 规则 21）。

## 4. 报告 vs 报表

| | 报告 REPORT | 报表 DASHBOARD |
|---|---|---|
| 形态 | 分章节的分析文档：图表 + 结论 + 明细，可导出/定时分发 | 一屏多卡片的看板：筛选器、联动、钻取 |
| 定义 | 同一份 Report Definition，`metadata.reportType` 区分；默认页名 `报告正文` / `看板` | 同上 |
| 编辑/发布 | 同一个编辑器与发布链 | 同上 |
| 资产库 | 按类型分类展示（图标 报告 / 看板） | 同上 |

## 5. 明细表与指标（与 docs/11 呼应）

- 汇总类卡片（KPI/折线/柱/饼）绑定 DWS/ADS 数据集或语义指标（`SEMANTIC_IR`）；
- 明细类卡片（数据表格）绑定 DWD/ODS 数据集版本作为 DataContext；`DATASET_FIELD` 绑定的行会汇总到绑定维度，
  不可加度量失败关闭；后续按 docs/11 的钻取合同把"指标 → 明细"接起来。

## 6. 尚未覆盖（下一步）

- AI 改稿（`ai/preview`，`report-operation-v1`）：该 Schema 使用 if/then/else、dependentRequired 与跨文件 `$ref`
  （report-definition-v1），不满足 AI 平台"每个属性必填、无条件关键字、只允许本地引用"的严格合同，因此当前
  仍无法被模型消费；需要为 AI 侧生成一份内联、严格化的操作 Schema（或按操作类型拆分多个小 Schema）。

- 卡片级筛选器 UI（`FILTER` 区域已有模型，运行页尚未渲染卡片内筛选控件）；
- 多页面编辑（`PAGE_*` 操作已在，编辑器仍只编辑第 1 页）；
- 起始模板改为数据驱动（当前 3 个模板为服务端硬编码）；
- 模板中心（`/report-templates`）目前是本地分析思路模板，与 Report Definition 尚未打通。
