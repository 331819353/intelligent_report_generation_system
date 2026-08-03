# ADR 0007：sealed 端到端评测作为语义版本激活硬门禁

状态：Accepted（2026-08-03）

## 决策

保留现有轻量规划回归，但明确标记为 `FIXTURE_REGRESSION`，不得显示或统计为端到端
准确率。正式评测使用 `END_TO_END_RESULT_EQUIVALENCE`：经脱敏并双人复核的问题通过
生产 Question Orchestrator、NebulaGraph、执行注册表、SQL Guard、数仓执行和 Result
Verifier，最终结果只以批准 hash 做等价比较。

SEALED 集只有在至少 2,000 条、全部双审且 READY 样本都有结果 hash 时才能激活并冻结。
发布门禁由服务端和 PostgreSQL 共同复算，要求点估计不低于 96%、95% Wilson 下界不低
于 95%、P0 与越权阻断 100%、敏感泄漏 0、直接回答覆盖率至少 85%、拒答精确率至少
95%。每条运行还必须固定到待发布的同一 `semantic_version/content_hash`。

首次 bootstrap 允许建立活动基线。此后任何语义版本切换必须在激活事务中提交通过的
sealed 集；数据库函数重新计算事实并把集合 ID/hash 写入发布记录，不能接受客户端传入
的“已通过”标志。

## 后果

- 用户点赞继续作为产品信号，但从正式准确率中彻底排除。
- 规划回归可快速发现路径变更，却永远不能解锁生产发布。
- 仓库不生成虚假的 2,000 条样本；缺少业务 Owner 数据时门禁保持 `BLOCKED`。
- 评测表只保存批准问句、结果 hash、安全布尔事实和首次失败阶段，不保存 SQL、参数、
  结果行、提示词或隐藏推理。
