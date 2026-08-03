# NebulaGraph 语义关系运行时

本模块按《智能问数项目技术架构与实施方案》7.7～7.9 节落地，不替代 PostgreSQL
中的权威语义合同。`semantic_releases` 保存版本、内容哈希和激活状态，NebulaGraph
只保存可按发布包重建的关系投影。

## 发布与一致性

1. API 创建并校验完整语义发布包，状态进入 `PROJECTING`。
2. 专用租约函数只允许 worker 领取 `NEBULA_GRAPH` 投影；worker 无权直接修改发布表。
3. worker 从 PostgreSQL 读取不可变对象清单，构建顶点和认证关系，以参数化属性执行
   幂等 `UPSERT VERTEX/EDGE`。NebulaGraph 不支持在 VID 语法位置使用参数，因此 VID
   必须先通过稳定 VID 白名单，再由适配器写入语句；用户输入不能进入该位置。
4. worker 逐个 `FETCH PROP` 核验节点和边，投影构建器同时阻断孤儿端点、重复关系、
   未认证关系和不支持的对象类型。
5. 完成函数校验租约、`content_hash` 和资源版本。四个强制投影全部 READY 后，发布包
   才能进入 READY 并由 API 原子激活。

投影失败使用指数退避和最大尝试次数；租约过期可以安全重领，因为写入是版本化、
幂等的。历史版本不会立即删除，以便历史问答按原版本回放。

## 图模型

- 每个环境一个 Space：开发环境 `smart_query_dev`，预发和生产分别使用
  `smart_query_staging`、`smart_query_prod`。
- Tags：`domain`、`entity`、`metric`、`dimension`、`dimension_value`、`dataset`、
  `table_column`、`business_term`、`certified_example`、`role`、`quality_rule`。
- Edges：方案定义的 12 类关系，另含认证示例专用的 `uses`。
- VID：`type:tenant_hash:object_id:version`。超过 128 字节时使用相同输入的 SHA-256，
  原始 ID 和版本仍保存在属性中。
- 所有顶点和边携带 `tenant_scope`、`semantic_version`；顶点另携带对象
  `content_hash`。

Schema 位于 `deployments/nebula/schema.ngql`；由于 `nebula-console -f` 按物理行执行，
每条 Schema 语句固定为单行，并由初始化任务显式检查控制台错误。本地 Compose 使用单副本便于开发；生产
必须使用多 metad/storaged/graphd 副本、TLS、独立强密码和备份恢复演练。

## 六类在线能力

`internal/semanticgraph.Graph` 固定提供：

1. 已知指标 VID 出发的 1～2 跳候选扩展；
2. `has_value` 维度值归属与有效期证明；
3. `groupable_by` 与 `has_value` 组成的 Bundle 兼容性闭包；
4. `joins_to` 的 1～4 跳候选路径和 Go 侧风险排序；
5. `can_access` 权限传播；
6. `depends_on/derived_from/sourced_from/uses` 的有界影响分析。

路径成本为每条边的基础、fanout、陈旧、跨源和策略复杂度成本之和。未知基数、未认证、
不允许查询或超过四跳的路径直接淘汰；最多返回三条低风险路径。

`verify-nebulagraph.sh` 不只检查 Tags/Edges 是否存在，还会在真实 NebulaGraph 中投影
数据集、指标、维度值和权限对象，并逐项验证候选扩展、值归属、Bundle 兼容性、Join
路径、权限传播和影响分析六类查询。

## 故障降级

连接使用官方 `nebula-go` Session Pool，配置连接/查询超时、池上限和熔断。图不可用
时，只有 Join 路径允许读取 PostgreSQL 中的认证 GraphPlan 缓存，而且缓存键必须完整
匹配 `tenant_id + semantic_version + content_hash + request_hash` 并且尚未过期。候选、
值归属、Bundle 和权限验证没有缓存降级，失败时进入澄清或阻断，不能回退到旧版本图。

## 本地验证

```sh
docker compose --env-file .env.example up -d nebula-metad nebula-storaged nebula-graphd nebula-init
./scripts/migrate.sh
./scripts/verify-nebulagraph.sh
./scripts/verify-semantic-qa.sh
```
