-- 数据集中心改为人工触发 LLM 后台流程。保留原有函数和 durable outbox，
-- 便于回滚以及继续处理人工提交的任务；只移除发布状态变化时的自动入队入口。
DROP TRIGGER IF EXISTS dataset_versions_enqueue_tag_suggestion
  ON platform.dataset_versions;
DROP TRIGGER IF EXISTS dataset_versions_enqueue_dwd_modeling
  ON platform.dataset_versions;
DROP TRIGGER IF EXISTS dataset_versions_enqueue_dws_modeling
  ON platform.dataset_versions;

COMMENT ON TABLE platform.dataset_tag_suggestion_jobs IS
  '数据集中心人工提交的精确非 ODS 发布版本标签建议 outbox；带租约、fencing 和有界重试';
COMMENT ON TABLE platform.dwd_modeling_jobs IS
  '数据集中心人工提交的同领域 DIM/DWD 分层建模 outbox；带租约、幂等和有界重试';
COMMENT ON TABLE platform.dws_modeling_jobs IS
  '数据集中心人工提交的市场分析模板选择与 DWS 草稿任务；依赖等待不消耗失败预算';
