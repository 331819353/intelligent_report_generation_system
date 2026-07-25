-- 数据集提交发布申请时只冻结草稿；全部候选提取和 DWD/DWS 物化必须在
-- 人工批准产生精确发布版本后，分别通过发布事务 outbox 启动。
WITH cancelled AS (
  UPDATE platform.metric_candidate_preparation_jobs
  SET status='CANCELLED',
      error_code='MOVED_AFTER_APPROVAL',
      error_message='候选提取已调整为审批通过后执行',
      completed_at=clock_timestamp(),
      updated_at=clock_timestamp(),
      lease_owner='',
      lease_expires_at=NULL
  WHERE status IN ('PENDING','RUNNING')
  RETURNING tenant_id,publication_request_id
)
UPDATE platform.dataset_publication_requests AS request
SET metric_candidate_generation_status='PENDING',
    metric_candidate_result=NULL,
    metric_candidate_total=0,
    metric_candidate_ready_count=0,
    metric_candidate_review_count=0,
    metric_candidate_blocked_count=0,
    metric_candidate_warning='',
    metric_candidate_error_code='',
    metric_candidate_generated_at=NULL,
    updated_at=clock_timestamp()
FROM cancelled
WHERE request.tenant_id=cancelled.tenant_id
  AND request.id=cancelled.publication_request_id
  AND request.status='PENDING'
  AND request.metric_candidate_generation_status IN ('PENDING','FAILED');

COMMENT ON TABLE platform.metric_candidate_preparation_jobs IS
  '历史发布前候选准备任务；v83 起不再创建，活动遗留任务已迁移为审批后提取';
COMMENT ON COLUMN platform.dataset_publication_requests.metric_candidate_generation_status IS
  '历史候选准备状态；PENDING 新申请表示批准后由发布版本任务异步加工';
COMMENT ON COLUMN platform.dataset_publication_requests.metric_candidate_result IS
  '历史发布前候选暂存结果；v83 后新申请不再写入';
