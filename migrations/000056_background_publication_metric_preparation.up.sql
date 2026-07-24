-- 发布审批提交只冻结草稿并登记 outbox；规则提取和 LLM 语义补全由 worker
-- 后台完成。任务与审批申请一一对应，崩溃后可通过租约和重试继续执行。
CREATE TABLE platform.metric_candidate_preparation_jobs(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id),
  publication_request_id uuid NOT NULL,
  dataset_id uuid NOT NULL,
  status text NOT NULL DEFAULT 'PENDING'
    CHECK(status IN ('PENDING','RUNNING','SUCCEEDED','PARTIAL','FAILED','CANCELLED')),
  attempt integer NOT NULL DEFAULT 0 CHECK(attempt>=0),
  next_attempt_at timestamptz NOT NULL DEFAULT now(),
  lease_owner text NOT NULL DEFAULT '',
  lease_expires_at timestamptz,
  error_code text NOT NULL DEFAULT '',
  error_message text NOT NULL DEFAULT '',
  created_at timestamptz NOT NULL DEFAULT now(),
  started_at timestamptz,
  completed_at timestamptz,
  updated_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT metric_candidate_preparation_jobs_request_fk
    FOREIGN KEY(publication_request_id,tenant_id)
    REFERENCES platform.dataset_publication_requests(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT metric_candidate_preparation_jobs_dataset_fk
    FOREIGN KEY(dataset_id,tenant_id)
    REFERENCES platform.datasets(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT metric_candidate_preparation_jobs_request_key
    UNIQUE(tenant_id,publication_request_id),
  CONSTRAINT metric_candidate_preparation_jobs_identity_key
    UNIQUE(id,tenant_id),
  CONSTRAINT metric_candidate_preparation_jobs_lease_shape_check CHECK(
    (status='RUNNING' AND lease_owner<>'' AND lease_expires_at IS NOT NULL AND completed_at IS NULL)
    OR
    (status<>'RUNNING' AND lease_owner='' AND lease_expires_at IS NULL)
  )
);

CREATE INDEX metric_candidate_preparation_jobs_claim_idx
  ON platform.metric_candidate_preparation_jobs(
    tenant_id,status,next_attempt_at,lease_expires_at,created_at,id
  );

ALTER TABLE platform.metric_candidate_preparation_jobs ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform.metric_candidate_preparation_jobs FORCE ROW LEVEL SECURITY;
CREATE POLICY metric_candidate_preparation_jobs_tenant_isolation
  ON platform.metric_candidate_preparation_jobs
  USING(tenant_id=platform.current_tenant_id())
  WITH CHECK(tenant_id=platform.current_tenant_id());

-- 把已经提交但尚未生成完的审批申请接入后台队列。FAILED 也允许用户或迁移
-- 触发一次新的、仍受三次 worker 尝试预算约束的后台重试。
INSERT INTO platform.metric_candidate_preparation_jobs(
  tenant_id,publication_request_id,dataset_id,status
)
SELECT tenant_id,id,dataset_id,'PENDING'
FROM platform.dataset_publication_requests
WHERE status='PENDING'
  AND metric_candidate_generation_status IN ('PENDING','FAILED')
  AND metric_candidate_result IS NULL
ON CONFLICT(tenant_id,publication_request_id) DO NOTHING;

ALTER TABLE platform.dataset_publication_requests
  DISABLE TRIGGER dataset_publication_requests_enforce_review;

UPDATE platform.dataset_publication_requests
SET metric_candidate_generation_status='PENDING',
    metric_candidate_error_code='',
    updated_at=now()
WHERE status='PENDING'
  AND metric_candidate_generation_status='FAILED'
  AND metric_candidate_result IS NULL;

ALTER TABLE platform.dataset_publication_requests
  ENABLE TRIGGER dataset_publication_requests_enforce_review;

COMMENT ON TABLE platform.metric_candidate_preparation_jobs IS
  '数据集发布审批前后台生成指标候选暂存批次的可恢复 outbox';
