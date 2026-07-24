# 分层数据平台升级与灰度手册

本文用于把已有环境升级到数据源不可变发布、ODS/DWD/DWS 物化和语义层。迁移必须先在数据库快照副本演练；不要把旧运行记录猜测性地绑定到当前数据源版本。

## 1. 上线前检查

先停止新的数据集发布和物化调度，等待正在运行的任务结束，再检查：

```sql
SELECT status,count(*)
FROM platform.dataset_build_runs
GROUP BY status
ORDER BY status;

SELECT source_type,count(*)
FROM platform.build_run_inputs
WHERE source_type IN ('SOURCE_TABLE','FILE_VERSION')
GROUP BY source_type;
```

若环境已经应用 v60/v61，但尚未应用 v62，且第二条查询有结果，v62 会有意失败。旧
`build_run_inputs` 没有记录精确 `data_source_version_id`，不能用“当前发布版本”回填，
否则会伪造历史证据。处理顺序是：

1. 备份数据库并将受影响的 build run、输入、节点、质量结果和物化登记导出到只读审计存档；
2. 确认这些旧运行不再是任何 ACTIVE 物化、稳定视图或在途构建的依赖；
3. 经数据治理负责人批准后移除受影响的旧运行，应用 v62；
4. 通过新的构建控制 API 重新登记，生成带精确数据源版本和摘要的输入快照。

不要直接把空列更新为当前指针，也不要关闭 v62 的失败检查。

## 2. 数据库与角色

迁移按文件名顺序、每个文件一个事务执行。生产至少使用四个角色：

- `report_admin`：只用于迁移和受控运维；
- `report_app`：API 控制面读写，不能在 `warehouse_*` 创建对象；
- `report_worker`：后台任务和数据面执行，可在 staging/ODS/DWD/DWS 创建对象。
- `report_connection_tester`：只执行冻结的数据源连接测试，只能调用受租约保护的
  claim/heartbeat/complete/fail 函数，不能入队或直接写任务与证明。

API、通用 worker 和连接测试 worker 使用三个不同 DSN。`migrate.sh` 会把平台表的
默认权限收紧为只读；新任务表必须显式授权，不能依赖一次性 `REVOKE` 修补未来默认
DML。源库 MySQL/Oracle 账号必须只读，并限制可见 catalog/schema/table；应用的 SQL
编译和词法检查只是第二道防线。

每个运行容器从启动时只能注入自己的一个 DSN。应用启动后删除多余环境变量仅是纵深
防御，不能替代编排层隔离。迁移会在角色重名、角色继承/成员关系或高权限属性存在时
失败，而不会自动降权；三个运行角色也必须都不同于拥有迁移对象 ownership 的管理员。
连接测试进程还必须使用不同于通用 Connector 的测试 token，
以及启用 TLS、独立且仅有 uploads 对象读取权限的 MinIO 凭据；通用 token 和 MinIO
管理凭据不能进入该进程。

专用 tester 仍需要 `DATA_SOURCE_CREDENTIAL_KEY` 解密 claim 返回的冻结配置，这是
当前方案保留的可信边界。数据库最小权限能够阻止它自行入队或直接伪造证明，但不能在
tester 进程已被完全攻陷时提供密码学隔离；更高等级部署应通过 KMS/凭据代理发放短时
连接授权。

通用 `report_worker` 同时拥有仓库对象和部分治理任务的写权限，是当前可信控制面，
不是密码学不可伪造的证明主体。维度画像的资源限制函数会校验当前 RUNNING 任务的
attempt、owner、lease token 和未过期租约，API 不能写画像任务或成员表；但已攻陷的
通用 worker 仍处于可信边界内。将 profiler 拆为只读发布视图、只能调用租约函数的
独立数据库角色，是后续纵深隔离项，不在本轮伪装为已完成能力。

生产 Connector 启动前还必须完成两类互不替代的防线：

1. 显式注入 `CONNECTOR_MAX_POOLS`（基准 1,000）、
   `CONNECTOR_MAX_TOTAL_CONNECTIONS`（基准 100）、全部请求/响应/样本/流式预算
   以及 `WAREHOUSE_STAGE_MAX_BYTES`，不能使用开发默认值。当前字节/行基准依次为
   请求 1 MiB、普通 JSON 64 MiB、技术元数据同步 200,000 个源字典行、样本
   16 KiB/单元格、64 KiB/行、512 KiB/响应，NDJSON 1 MiB/单元格、
   4 MiB/行、1 GiB/整流，数据库或 Excel/CSV 单任务 staging 逻辑载荷 512 MiB。
   文件对象/CSV/XLS 读取使用
   `min(max_excel_file_bytes, WAREHOUSE_STAGE_MAX_BYTES)`，XLSX 展开和 worksheet
   内存预算再取解析器上限与 staging 上限中的更严格值。
2. `CONNECTOR_EGRESS_ALLOWLIST` 只配置获批的 `IP/CIDR:port`；
   `CONNECTOR_EGRESS_DENYLIST` 显式覆盖平台 PostgreSQL、Redis、MinIO、API 和云
   控制面网段。Connector 子网的 Security Group / NetworkPolicy / 主机防火墙采用
   默认拒绝，并只放行同一批准目标。

生产不接受 hostname allowlist。数据源可使用 DNS，但全部解析地址均须落在获批
CIDR，驱动随后 pin 到已验证 IP。该应用层校验不能完全约束 Oracle listener/SCAN
在协议阶段给出的重定向目标，因此网络层控制仍是硬门；应把预期 Oracle redirect
地址纳入最小授权集合，而不是放宽到整段控制面网络。

## 3. 推荐灰度顺序

1. 先上线数据库迁移和 `report_connection_tester` 角色，启动连接测试 worker，
   验证队列年龄、租约丢失、成功率和失败码监控。
2. 在 Connector 接收生产凭据前部署显式池/物理连接上限、字节预算、CIDR
   allow/deny 和网络层出站策略，先完成池耗尽、DNS 混合解析、控制面不可达和
   Oracle redirect 负测。
3. 将 API 测试入口切换为异步入队；v70 会清空尚待发布草稿上的 legacy PASSED
   缓存，要求重新测试。已经发布且草稿指针等于发布指针的运行配置保持不变。
4. 按数据源逐个完成“新草稿 → 异步测试 → 证明 → 发布”。旧发布版本在切换前继续服务。
5. 选择一个低风险域，将源表/Sheet 映射为 ODS，核对行数、字段类型、业务键和抽样聚合。
6. 发布 DWD，再发布 DWS；每层只引用前一层当前 ACTIVE 的精确物化。
7. 等待当前 DWS 物化的字段画像完成，评审风险下限后注册维度、刷新成员、录入历史
   别名，并只验证不会产生不安全扇出的指标兼容关系。
8. 对旧查询与新稳定视图做双读比对，达到约定窗口后再切换消费方。
9. 最后开启自动标签建议、语义向量 worker 和常规物化调度。

## 4. 验收门槛

本节只验收当前运行时能力：物化仍仅支持 `FULL + TABLE`，`INCREMENTAL`、
`BACKFILL` 和 `PARTITIONED_TABLE` 必须失败关闭；指标定义 v2 仍是
[`DESIGN ONLY / NOT YET ACCEPTED BY RUNTIME`](./metric-definition-v2-design.md)，
现有 API、数据库与查询执行只承诺 v1。

- 草稿未测试、测试失败、测试过期或版本已变化时均不能发布数据源；
- API、通用 worker 和连接测试 worker 对任务、证明及历史 test_runs 的直接 DML
  均被拒绝；tester 不能 enqueue，API 不能 complete；
- 三个运行角色互异且均不同于迁移管理员，无继承、无成员关系、无高权限属性；每个
  进程从启动时只拥有自己的 DSN，连接测试 token 与通用 token 不相同；
- 生产连接测试 MinIO 强制 TLS，专用身份只能读取允许的 uploads 对象，不能列举、
  写入或删除对象；
- 数据源更新的陈旧 `expectedVersion` 返回 409，不能覆盖较新草稿；
- ODS 输入固定精确数据源版本及表结构/文件摘要；
- DWD 只读 ODS，DWS 只读 DWD，且所有主计算在 PostgreSQL 完成；
- 构建失败或丢失租约时稳定视图不切换；
- 生产任一 Connector 池/物理连接上限或字节预算缺失、allowlist/denylist 为空，
  或 allowlist 含 hostname 时启动失败；DNS 名解析到“一个允许地址 + 一个未允许/
  deny 地址”时整次连接失败，deny 永远覆盖 allow；
- 池数量达到 `CONNECTOR_MAX_POOLS` 时只淘汰完全空闲的 LRU 池；全局物理连接达到
  `CONNECTOR_MAX_TOTAL_CONNECTIONS` 时只回收 LRU 空闲 socket。持有活动查询或
  等待 acquire 引用的池、活动 socket 均不会被驱逐，无可回收资源时在
  `connectTimeout` 后失败；
- 连接测试不增加普通池数量，每次 one-shot 连接计入全局物理连接上限，并在成功、
  失败、超时或取消后释放；并发压测期间观察到的池化连接与 one-shot 连接总数始终
  不超过硬上限；
- loopback、link-local、multicast、unspecified、reserved、metadata 以及所有
  IPv4-mapped IPv6 目标被拒绝；通过校验后实际 socket 目标是已验证 IP，而非再次
  解析 hostname；
- 网络层负测证明 Connector 不能访问平台 PostgreSQL、Redis、MinIO、API、云
  metadata 或未批准端口；Oracle listener/SCAN 返回未批准 redirect 时连接也被
  网络层拒绝；
- 无 `Content-Length` 的分块请求超过 1 MiB 时仍返回
  `413 CONNECTOR_REQUEST_BODY_LIMIT_EXCEEDED`；普通 JSON、样本和 NDJSON 在
  max、max+1 边界分别成功/失败，错误正文不含凭据、SQL 或业务值；
- 元数据样本最多十行、256 个已发现且安全的投影字段；二进制、LOB、Oracle
  `LONG / LONG RAW / XMLTYPE / JSON` 等类型不会被采样。单元格、行、响应任一
  超限都整体失败，不交给 LLM；
- ODS 流的单元格、行、事件、整流或 5,000,000 行上限任一超限，或数据库
  staging 逻辑载荷超过 512 MiB 时，同一 PostgreSQL 事务回滚且不产生 ACTIVE
  物化；不能把截断结果视为成功；
- Excel/CSV 逻辑 staging 同样受 512 MiB 上限约束；对象、CSV、XLS 读取超过
  `min(max_excel_file_bytes, WAREHOUSE_STAGE_MAX_BYTES)`，或 XLSX 展开/
  worksheet 内存超过各自更严格上限时失败关闭。用“文件本体超过低 stage cap、
  选中 Sheet 很小”的用例验证保守拒绝，且不产生 ACTIVE 物化；
- MySQL 服务端游标在超限、取消、客户端断流或提前 EOF 后先关闭 socket，物理连接
  不回池；后续请求使用新连接，清理路径不会继续排空未读结果；
- 新 ACTIVE DWS 物化会立即撤销旧 FULL 维度的成员可用性；只有当前
  `SUCCEEDED + FULL` 画像和当前 `SUCCEEDED` 刷新代际可恢复成员检索；
- 敏感与 IDENTIFIER 画像在读取业务值前分别以 `NONE`、`EXACT_ONLY` 短路，
  API 无画像/成员 DML 权限，迟到 lease 写入被拒绝；
- 维度成员长扫描只读取登记的 run-scoped 不可变物理表，不读取或锁定稳定视图；
  激活可在扫描期间完成，随后 merge 因 source fence/lease fence 拒绝旧 stage；
- scratch 每批不超过 1,000 行、规范 stage 不超过 `maxMembers + 1`；任务超时、
  超基数、非法成员或临时表清理失败时保留旧快照，清理失败的连接不能回池；
- 成员 merge 在 tenant governance gate 内原子切换 generation；用接近上限的成员量
  验证治理写入会等待但不死锁，并记录随成员量增长的 gate 持有时间，不能把它误报
  为常数时间临界区；
- 只有对象级 `DATASET:READ`、没有全局授权的 actor 也能读取该对象成员；撤销授权，
  或增加任一适用行策略/维度字段上的非 `ALLOW` 列策略后，精确 members/aliases
  请求返回 `403 SEMANTIC_MEMBER_ACCESS_DENIED`，而跨维度别名目录/指标检索静默
  过滤该维度；
- 只有 `VERIFIED` 且非 `UNSAFE` 的维度—指标关系进入成员值检索；
- LLM 只写 `SUGGESTED` 标签绑定，人工批准后才进入正式语义文档和向量检索；
- `scripts/verify-database.sh`、全量 Go/前端测试、Connector 测试和 fresh migration 均通过。

## 5. 监控与告警

生产监控至少按租户、任务和稳定结果码聚合，禁止把密码、SQL、hostname/IP、
样本值或成员值放入日志正文和指标 label：

- Connector：池注册表使用率、全局物理连接数、one-shot 连接数、空闲池/连接 LRU
  淘汰、全局连接等待与超时，以及请求/普通 JSON/样本/流式各级预算使用率与拒绝数，
  `CONNECTOR_REQUEST_BODY_LIMIT_EXCEEDED`、`METADATA_SAMPLE_*`、
  `QUERY_*`，取消/断流/提前 EOF、物理连接丢弃和池重建；
- 出站：DNS 解析失败、全地址校验失败、allow/deny 拒绝、网络层拒绝和 Oracle
  redirect 失败。原始目标只进入受控审计，不作为常规指标高基数标签；
- ODS：源端字节、NDJSON 字节、数据库与文件 staging 逻辑字节、文件对象读取/
  XLSX 展开拒绝、COPY 行数/吞吐、预算余量、staging 回滚、
  `ODS_STAGING_FAILED` 和稳定视图未切换数；
- 维度刷新：扫描时长/行数、规范成员数、超基数/超时/非法值、
  `REFRESH_SOURCE_CHANGED`、lease 过期、scratch/stage 清理和连接丢弃；
- 语义治理：merge 时长/行数、tenant governance gate 等待与持有时间、generation
  切换、精确访问拒绝、宽检索静默过滤计数，以及标签/别名/兼容关系写入等待。

建议分别对“扫描慢”和“merge 持门过久”告警。前者不应阻塞物化激活；后者会串行化
同租户语义治理写入，应以成员规模分桶建立基线，并在灰度扩大前验证 P95/P99。

## 6. 回退

应用回退不等于删除新表。出现问题时先关闭新任务入口和 worker，再将消费方切回旧查询；
已经 ACTIVE 的物化保持只读以便对账。数据源发布切换失败时保留原
`current_published_version_id`；物化失败时保留原 ACTIVE 物化和稳定视图。禁止直接
删除不可变版本、测试证据、构建输入或质量结果。

确认不再引用后，按单独保留策略清理失败 staging 和 RETIRED 物理表。清理前必须检查
ACTIVE 指针、稳定视图、血缘和所有 `QUEUED/RUNNING` 构建冻结输入。
