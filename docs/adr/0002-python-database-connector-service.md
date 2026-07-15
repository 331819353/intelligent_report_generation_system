# ADR-0002：数据库连接层采用 Python 独立服务

- 状态：已采纳
- 日期：2026-07-15

## 决策

系统核心业务后端继续使用 Go。数据库连接、连接测试、受控查询、元数据读取以及 Excel 解析由独立 Python Connector Service 承担。

- Go：租户、认证、权限、数据源领域状态、密钥引用、数据集 DSL、查询计划、跨源编排、缓存、报告和审计。
- Python：MySQL、Oracle、Excel 的驱动适配和源端执行。
- Oracle：`python-oracledb` Thin 模式，不依赖 Oracle Client、Instant Client、OCI 或 CGO。
- MySQL：PyMySQL。
- 通信：内部 HTTP JSON，必须携带独立服务令牌；生产环境应升级为 mTLS 或服务网格身份。

## 安全边界

Python 服务不管理租户和业务权限。Go 完成租户、对象、行列权限及字段校验后才可发起连接请求。Python 服务仍执行纵深防御：内部认证、只读 SQL、禁止注释和多语句、SQL/参数/行数限制、连接和查询超时，且日志不得记录密码。

## 扩展方向

当前 HTTP 协议适合连接测试和受限结果集。大结果集阶段应引入 Arrow IPC/Flight 或受控对象存储交换，避免超大 JSON；连接池按租户和数据源设置上限，并由 Go 侧实施并发配额和排队。
