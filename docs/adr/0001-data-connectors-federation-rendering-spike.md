# ADR-0001：首期数据连接、跨源计算与渲染验证

- 状态：部分验证，Oracle/MySQL 已验证，待独立联邦引擎和 PDF 环境补测
- 日期：2026-07-15
- 关联任务：T0005

## 决策

1. Excel `.xlsx` 首选 Excelize 2.11，使用行迭代器并强制文件、解压、工作表内存、行数和列数上限。
2. 旧 `.xls` 通过独立适配器读取，默认施加更严格的文件和行数配额；正式启用前必须补充真实复杂 `.xls` 样本回归。
3. 数据库连接层从 Go 核心业务服务拆分为独立 Python Connector Service。Oracle 使用 `python-oracledb` Thin 模式，MySQL 使用 PyMySQL，不要求 Go 服务或 Connector Service 安装 Oracle Client/OCI。Go 继续负责租户、权限、数据集计划、查询治理、跨源编排和报告业务，通过内部认证 HTTP 协议调用 Python 服务。
4. 首期跨源预览优先采用 Go 查询网关：源端过滤/投影后拉取受限结果，在网关执行有输出上限的 Hash Join。正式发布的复杂跨源报告使用物化结果。数据规模或 SQL 能力超过阈值后再评估独立联邦引擎，当前不引入 Trino 类运维面。
5. 设计器、查看器和 PDF 必须消费同一版本化报告 JSON。当前已验证合同校验和规范哈希一致；真实浏览器与 PDF 像素/分页一致性等待导出 Worker。

## 可复现结果

运行：

```bash
go test ./internal/spike/...
go test -run '^$' -bench BenchmarkHashJoin10K -benchmem ./internal/spike/federation
```

当前 Apple M5 Pro 结果：10,000×10,000 等值一对一 Hash Join 约 `0.61 ms/op`，约 `1.53 MB/op`、`10,051 allocs/op`。真实源测试使用 MySQL 8.4 的客户表和 Oracle Free 23.26.2 的订单表，Go 网关成功得到 3 行关联结果；该小样本不代表生产网络延迟。

Excel 原型验证：多工作表枚举、流式行读取、公式缓存值、日期格式值、文件/行/列/解压限制。`.xls` 适配器已编译，但复杂公式、合并单元格、代码页和日期样本尚未形成自动化样本集。

报告 JSON 原型验证：1920×1080 可视范围、12×10 主网格、实际高度大于 1080、纵向块坐标和越界拒绝；设计器、查看器、PDF 三入口对相同文档产生同一合同哈希。尚未验证真实 PDF 字体、分页、图表和冻结降级。

## Oracle 连接要求

- DSN 同时配置 Oracle Net `connect_timeout` 与 Go Context 超时；连接池设置最大连接、空闲连接和连接寿命。
- 查询统一使用 `QueryContext`，取消后关闭 Rows；连接池设置最大连接、空闲连接和连接寿命。
- DSN 和日志必须脱敏，凭证只从 Secret 引用解析。
- 上线前在目标 Oracle 版本上验证：连接、只读事务、查询取消、网络中断、连接池耗尽、CLOB/BLOB、NUMBER 精度、DATE/TIMESTAMP/时区和空字符串语义。

## 风险和未完成项

| 风险 | 当前状态 | 后续验证 |
|---|---|---|
| Python 服务连接池和并发上限 | 当前完成连接与小样本查询 | T0201/T0702 增加进程数、连接池、排队、取消和目标负载压测 |
| `.xls` 库维护活跃度与格式覆盖有限 | 适配器完成，样本不足 | 建立真实业务样本库；必要时用隔离转换服务转 `.xlsx` |
| Go Hash Join 内存随输入增长 | 有输出上限，暂无输入字节预算 | T0305 加输入行/字节预算、磁盘溢写和物化阈值 |
| 独立联邦引擎未做同机对照 | 未完成 | 有真实 MySQL/Oracle/Excel 后对比 Trino/DuckDB 类方案 |
| PDF 一致性未做像素级验证 | 未完成 | T0503/T0603 使用固定 Chromium、字体和截图差异测试 |

## 重新决策触发条件

- 跨源实时输入经下推后仍经常超过 100,000 行或内存预算。
- 需要复杂窗口函数、分布式聚合、磁盘溢写或统一 SQL Catalog。
- Python Connector Service 无法满足目标吞吐、取消或 Oracle 专有类型要求。
- `.xls` 真实样本兼容率无法达到验收标准。
