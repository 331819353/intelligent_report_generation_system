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

| 步骤 | 界面 | 写入的定义 / Operation |
|---|---|---|
| ① 新建 | `/reports/new`：名称、**类型（报告 REPORT / 报表 DASHBOARD）**、初始数据集；空白 / 模板 / AI 生成 / **从 JSON 导入** | `POST /reports/blank`、`/report-templates/{id}/instantiate`、`/reports/ai/create`（均含 `reportType`）、`POST /reports {definition}` |
| ② 声明数据集 | 侧栏「数据与筛选 → 数据集」：从已发布数据集目录加入/移除 | `DATA_CONTEXT_CREATE` / `DATA_CONTEXT_DELETE`（被卡片或筛选器引用的数据集不能移除；服务端按目录重解析 ID/查询策略） |
| ③ 添加卡片 | 「添加组件」：选展示类型（组件清单）、标题、**数据集**、放置方式（新卡片 / 加入当前卡片的区域） | `COMPONENT_CREATE` + `BLOCK_CREATE`（或 `ZONE_CREATE`）；绑定按字段角色预填合同下限 |
| ④ 布局 | 画布拖拽把手移动、右下角缩放；碰撞自动消解；章节上移/下移 | `BLOCK_MOVE` / `BLOCK_RESIZE` / `SLOT_UPDATE` / `ZONE_REORDER` / `SECTION_REORDER` |
| ⑤ 配置卡片 | 点击卡片 →「属性与绑定」：**展示类型切换**、**数据集切换**、标题/副标题、表现属性（按清单 optionSchema 生成）、维度/度量绑定；**按字段角色填充**（确定性）或 **AI 识别度量与维度** | `COMPONENT_REPLACE`（换类型，绑定按新合同重映射）+ `COMPONENT_UPDATE` + `DATA_BINDING_UPDATE` |
| ⑥ 筛选器 | 侧栏「筛选器」：数据集 → 字段 → 类型（按字段语义建议：时间→日期区间、度量→数值区间、其它→多选）→ 作用范围（全报告 / 指定卡片） | `FILTER_CREATE` / `FILTER_UPDATE` / `FILTER_DELETE`；取值在运行页顶部筛选栏填写，服务端解析后作用于绑定卡片 |
| ⑦ 联动 / 结论 | 侧栏「图表联动」「结论证据」 | `INTERACTION_*` / `INSIGHT_*` |
| ⑧ 保存 | 每次操作即修订（`r1, r2, …`），撤销/重做；草稿按当前用户权限实时执行预览 | `POST .../undo|redo`，`POST .../draft/execute` |
| ⑨ 发布 | 「预览与发布」→ 发布评审（确定性门禁 + 可选 AI 说明）→ 发布人确认桌面/移动布局 → 版本 | `POST .../publish-review`、`POST .../publish`、版本/回滚/升级 |
| ⑩ 运行 | `/reports/{id}`：筛选栏 + 卡片；交互（点击筛选/钻取）由服务端解析 | `POST .../runtime/execute` |

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
