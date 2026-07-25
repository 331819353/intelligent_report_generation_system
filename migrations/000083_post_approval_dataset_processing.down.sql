-- 已中止的历史预审批任务不能在回滚时安全恢复其租约与外部 LLM 调用，
-- 因此这里只恢复旧版本的对象说明，不重新排队或伪造处理状态。
COMMENT ON TABLE platform.metric_candidate_preparation_jobs IS
  '数据集发布审批前后台生成指标候选暂存批次的可恢复 outbox';
COMMENT ON COLUMN platform.dataset_publication_requests.metric_candidate_generation_status IS
  '发布前指标候选生成状态';
COMMENT ON COLUMN platform.dataset_publication_requests.metric_candidate_result IS
  '审批前内部暂存的规则事实与 LLM 语义结果';
