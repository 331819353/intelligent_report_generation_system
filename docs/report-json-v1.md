# 报告 JSON V1 合同

报告 JSON 是设计器、在线查看器和导出渲染器之间的唯一事实来源。正式合同位于：

- JSON Schema：`api/schemas/report-json-v1.schema.json`
- 可执行样例：`api/examples/report-json-v1.json`
- Go 严格解析与领域校验：`internal/reportjson`

`internal/spike/reportjson` 仅保留为早期技术对照，不得被业务链路引用。

## 固定布局语义

- `logicalWidth` 固定为 `1920`，`viewportHeight` 固定为 `1080`。
- 首屏主网格固定为 `12 × 10`，但 10 行不是页面总高度上限。
- `page.contentGridRows` 等于 `max(10, 所有分块的 y + h)`；省略时 Go 规范化器会派生，显式错误值会被拒绝。
- 分块内网格严格等于 `block.grid.w × 4` 列和 `block.grid.h × 4` 行。
- 分块不得横向越过 12 列，组件不得越过所属分块内网格。
- 同一页面的分块、同一分块的组件不得碰撞。需要覆盖展示的视觉效果应在组件内部样式中实现，不能依赖非法网格重叠。

## 浏览态冻结合同

- 禁用态只能保存 `{"enabled":false}`；启用态必须同时保存 `enabled/top/scope/zIndex`，V1 中缺少 `sticky`、空对象、`null` 或禁用态夹带参数都会被拒绝。
- `top` 是 `0..10000` 的整数视口 CSS 像素，`zIndex` 是 `1..100000` 的整数；查看器会按当前画布缩放比换算为逻辑坐标。
- 分块允许 `PAGE`，或以 `CONTAINER + 所属 page.id` 显式表达页面祖先；组件允许 `PAGE`、所属 `BLOCK`，或以 `CONTAINER` 引用所属页面/分块。
- `containerId` 是报告实体引用，不是 DOM ID 或选择器。未知、非祖先、跨页或同时命中页面和分块类型的歧义引用失败关闭。
- 0.9 迁移器仍兼容旧零值语义，并把旧式缺失/空冻结配置规范化为显式禁用；当前 1.0 文档不享受该宽松规则。

## 严格解析和迁移

调用 `reportjson.Prepare(raw)` 会依次执行：

1. 读取 `schemaVersion`；
2. 严格解析并拒绝未知字段或 JSON 尾随内容；
3. 将 0.9 的 `canvas.logicalHeight`、`canvas.gridRows` 迁移为 V1 首屏字段；
4. 补齐可确定性派生的切片和页面内容行数；
5. 校验枚举、唯一性、引用、权限声明、冻结配置和二维布局；
6. 输出规范 JSON 和 SHA-256 内容哈希。

迁移不会把 `logicalHeight` 当作页面总高度。规范输出只包含 `viewportHeight`、`viewportGridRows` 和动态内容规则。

## 错误定位

领域错误返回 `ValidationError`，其中每个问题均包含可供设计器定位的路径和简体中文原因，例如：

```json
{
  "details": [
    {
      "path": "pages[0].blocks[1].components[2].grid",
      "reason": "与 components[0] 发生碰撞"
    }
  ]
}
```

校验器会尽量一次收集所有独立问题。解析错误、未知字段或不支持的 Schema 版本会在进入领域校验前失败。

## 安全与兼容边界

- JSON 只能保存数据集版本、指标、文件、角色和权限编码等引用，不得保存数据源密码、模型密钥或访问令牌。
- 首批正式组件的 `style`、`binding`、`interaction` 和 `refreshPolicy` 已按组件类型收紧；报告级 `theme/generation/extensions` 仍保留受外层结构约束的扩展对象，发布前必须按对应功能再次校验。
- `PUBLIC` 仅表示合同中的目标可见性，不代表已经完成公开发布。实际发布仍需 T0105/T0602 的访问策略、安全检查和不可变版本校验。
- Go 校验器是服务端最终边界。前端在 T0402 接入同一 Schema 的运行时校验后，仍不能替代服务端复核。
