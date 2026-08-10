# AskData 容量基线与生产 POC 运行手册

本文件是 `OBS-003` / `OPS-004` 的可重复执行入口，不是生产容量结论。生产副本数、
partition、连接池和并发预算只有在 `HUMAN-013` 提供目标规模、SLO 与目标环境后，使用该环境的
报告才能签署；开发机或单节点 Compose 报告不得用于生产推断。

## 首期规模假设与升级检查点

| 维度 | 首期假设 | 触发复核 |
|---|---:|---:|
| 租户 | ≤ 20 | > 50：检索按租户分区 |
| 业务域 | 首期 1，12 个月内 ≤ 5 | > 10：评估按域拆分图 Space |
| 检索文档 | ≤ 200,000 | > 2,000,000：评估独立向量服务 |
| FULL 成员/域 | ≤ 50,000 | > 200,000：强制转 ON_DEMAND |
| ON_DEMAND 成员/域 | ≤ 5,000,000 | > 50,000,000：Lookup 表分区 |
| 日问数运行 | 2,000～10,000 | > 50,000：复核缓存层 |
| 峰值并发问数 | 20～50 | > 200：水平扩容并重算连接池 |
| 已发布报告 | ≤ 2,000 | > 20,000：评估制品 CDN |
| 图顶点/边 | ≤ 100,000 / 500,000 | > 10,000,000 边：多副本容量 POC |

这些值只是架构复核阈值。`internal/askdata/observability/capacity.go` 会在报告越过阈值时生成稳定
告警，不会自动修改生产拓扑或扩大容量声明。

## 运行前门禁

1. 使用与生产同版本、同副本、同 partition、同连接池配置的隔离环境；记录节点 CPU、内存、磁盘、
   网络、PostgreSQL/Nebula/LLM Provider 版本与配置标签。
2. 准备专用租户、业务域、认证问法、Complex 问法、仓库超时、三跳图、向量召回和六计划 KPI Bundle
   夹具。不得把密封集问题正文写入配置或报告。
3. 通过环境变量 `ASKDATA_LOADTEST_BEARER_TOKEN` 注入短期访问令牌；配置文件不得包含
   Authorization、Cookie 或令牌。每个 POST 的唯一 `Idempotency-Key` 由运行器根据 seed、场景和请求
   序号生成。
4. `VECTOR_RECALL`、`GRAPH_3_HOPS`、`LLM_DEGRADATION` 使用部署侧受控容量适配端点；端点只返回
   状态码和 `X-AskData-*` 聚合头，不返回问题正文、查询参数或结果行。

## 执行

先复制并替换示例中的业务域、问法和受控适配端点：

```sh
cp scripts/loadtest/config.example.json /secure/askdata-capacity.json
ASKDATA_LOADTEST_BEARER_TOKEN='short-lived-token' \
  go run ./scripts/loadtest \
  -config /secure/askdata-capacity.json \
  -output /secure/askdata-capacity-report.json
```

同一配置和 seed 可重复运行；报告只保存环境标签、规模、计数、分位数、Recall@K 和失败码 hash，
不保存响应正文。必须覆盖：Fast Path、Complex Loop、仓库连接池/statement timeout、pgvector
Recall@K、Nebula 三跳、Provider 限流/熔断和 KPI Bundle 并发计划。

## 容量签署清单

生产建议必须同时附上：

- 原始 JSON 报告及其 `environment.configHash`；
- API/Worker/PostgreSQL/Nebula/向量索引的 CPU、峰值内存和存储增量；
- 数据库与数仓连接池占用、排队和 statement timeout 统计；
- 当前及建议副本数、partition、连接池、每租户/每域并发预算；
- 所有 `EvaluateCapacityAlerts` 告警的处置或风险接受记录；
- 两次同配置复跑的 P50/P95/P99、成功率和 Recall@K 对比。

缺少任一项，或报告来自开发单节点，结论状态必须保持 `POC_NOT_SIGNED`，不得写入生产容量基线。
