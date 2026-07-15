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
- `style`、`binding`、`interaction`、`refreshPolicy` 和 `extensions` 当前是受外层结构约束的开放对象，便于 T0402/T0406 建立渲染器和组件注册机制；每类组件的专属 Schema 仍必须在组件实现阶段收紧。
- `PUBLIC` 仅表示合同中的目标可见性，不代表已经完成公开发布。实际发布仍需 T0105/T0602 的访问策略、安全检查和不可变版本校验。
- Go 校验器是服务端最终边界。前端在 T0402 接入同一 Schema 的运行时校验后，仍不能替代服务端复核。
