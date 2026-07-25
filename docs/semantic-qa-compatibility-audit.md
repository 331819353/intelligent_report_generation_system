# 智能问答分层兼容审计记录

审计日期：2026-07-25
审计工具：[`scripts/audit-semantic-layer-compatibility.sh`](../scripts/audit-semantic-layer-compatibility.sh)

## 审计范围

审计读取迁移账本、当前发布数据集的不可变 DSL、冻结 DATASET 依赖、
`dataset_build_runs / build_run_inputs` 和消费合同状态。它不读取业务行、凭据、
SQL 或物理表内容。

## 本地结果

- 迁移 84–95 均已登记；
- 独立空库完整迁移 1–95 通过；
- 本地有 8 个当前发布 ODS；
- 当前没有发布 DIM、DWD、DWS 或 ADS；
- 已登记 26 条 `ODS <- SOURCE` 构建输入；
- 未发现未解析、越层或不满足合同的当前发布依赖；
- 当前没有 PUBLISHED 消费合同，自动 ADS 生成按设计关闭；
- 数据库、warehouse 和 Semantic QA 专项验证全部通过；
- 当前语义图 generation 3 为 READY，包含 120 个节点和 112 条边。

从迁移 84 首次进入本地账本起，84–95 均视为不可变历史；后续修正必须从 96 开始使用
前向迁移。

## 目标环境操作

每个测试、预发布和生产环境分别运行：

```sh
ENV_FILE=/path/to/environment.env ./scripts/audit-semantic-layer-compatibility.sh
ENV_FILE=/path/to/environment.env ./scripts/verify-semantic-qa.sh
```

审计会列出：

- 84–95 迁移状态；
- 当前发布数据集层级数量；
- 目标层—输入层矩阵；
- DIM、DWD、DWS、ADS 的越层或缺失输入；
- 历史构建运行的实际输入矩阵；
- PUBLISHED/DRAFT 消费合同数量和 ADS 自动生成策略。

任何环境已经登记的迁移文件都不得修改。目标环境存在旧派生对象时，应根据冻结版本和
hash 创建 `KEEP / REBUILD / DEPRECATE / MANUAL_REVIEW` ChangeSet，不直接修改历史
发布版本。

## 当前合同

```text
DIM <- ODS[1..N]
DWD <- ODS[1..N] + DIM[0..N]
DWS <- DWD[1..N]
ADS <- DWS[1..N] + PUBLISHED consumer contract
```

Bridge 是输入关系角色，不是新层级；DWD 可关联任意数量普通 DIM。ADS 没有自动组合
DWS 的调度器。
