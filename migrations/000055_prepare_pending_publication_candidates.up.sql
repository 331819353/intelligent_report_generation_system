-- 000054 已经执行过的环境可能把尚未审批的历史申请标为 LEGACY。
-- 将它们恢复为可同步生成状态；前端允许申请人对同一冻结草稿执行幂等重试。
UPDATE platform.dataset_publication_requests
SET metric_candidate_generation_status='PENDING',
    metric_candidate_error_code='',
    updated_at=now()
WHERE status='PENDING'
  AND metric_candidate_generation_status='LEGACY'
  AND metric_candidate_result IS NULL;
