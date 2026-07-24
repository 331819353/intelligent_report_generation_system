-- 数据集提交发布审批时同步完成规则提取和 LLM 语义补全，但在审批通过前
-- 不把结果写入指标候选目录。预留的发布版本 ID 使暂存定义可以在审批后
-- 原样绑定到最终不可变版本，避免再次调用 LLM 或改写候选事实。
ALTER TABLE platform.dataset_publication_requests
  ADD COLUMN reserved_published_version_id uuid,
  ADD COLUMN metric_candidate_generation_status text,
  ADD COLUMN metric_candidate_result jsonb,
  ADD COLUMN metric_candidate_total integer NOT NULL DEFAULT 0,
  ADD COLUMN metric_candidate_ready_count integer NOT NULL DEFAULT 0,
  ADD COLUMN metric_candidate_review_count integer NOT NULL DEFAULT 0,
  ADD COLUMN metric_candidate_blocked_count integer NOT NULL DEFAULT 0,
  ADD COLUMN metric_candidate_warning text NOT NULL DEFAULT '',
  ADD COLUMN metric_candidate_error_code text NOT NULL DEFAULT '',
  ADD COLUMN metric_candidate_generated_at timestamptz;

-- 遗留审批申请保持原有“审批后异步生成”行为；新申请使用 PENDING 并在提交
-- 请求返回前完成同步生成。
ALTER TABLE platform.dataset_publication_requests
  DISABLE TRIGGER dataset_publication_requests_enforce_review;

UPDATE platform.dataset_publication_requests
SET reserved_published_version_id=COALESCE(published_version_id,gen_random_uuid()),
    metric_candidate_generation_status=CASE WHEN status='PENDING' THEN 'PENDING' ELSE 'LEGACY' END;

ALTER TABLE platform.dataset_publication_requests
  ENABLE TRIGGER dataset_publication_requests_enforce_review;

ALTER TABLE platform.dataset_publication_requests
  ALTER COLUMN reserved_published_version_id SET NOT NULL,
  ALTER COLUMN reserved_published_version_id SET DEFAULT gen_random_uuid(),
  ALTER COLUMN metric_candidate_generation_status SET NOT NULL,
  ALTER COLUMN metric_candidate_generation_status SET DEFAULT 'PENDING',
  ADD CONSTRAINT dataset_publication_requests_reserved_version_key
    UNIQUE(tenant_id,dataset_id,reserved_published_version_id),
  ADD CONSTRAINT dataset_publication_requests_metric_candidate_status_check
    CHECK(metric_candidate_generation_status IN ('LEGACY','PENDING','SUCCEEDED','PARTIAL','FAILED')),
  ADD CONSTRAINT dataset_publication_requests_metric_candidate_result_check
    CHECK(
      metric_candidate_generation_status IN ('LEGACY','PENDING','FAILED')
        AND metric_candidate_result IS NULL
      OR metric_candidate_generation_status IN ('SUCCEEDED','PARTIAL')
        AND jsonb_typeof(metric_candidate_result)='object'
        AND metric_candidate_generated_at IS NOT NULL
    ),
  ADD CONSTRAINT dataset_publication_requests_metric_candidate_count_check
    CHECK(
      metric_candidate_total>=0
      AND metric_candidate_ready_count>=0
      AND metric_candidate_review_count>=0
      AND metric_candidate_blocked_count>=0
      AND metric_candidate_ready_count+metric_candidate_review_count+metric_candidate_blocked_count
        <=metric_candidate_total
    );

-- Worker 只消费审批通过后创建的任务。prepared_result 已在提交审批时完成 LLM
-- 补全，Worker 仅验证精确发布版本并原子落候选，不再重复请求模型。
ALTER TABLE platform.metric_extraction_jobs
  ADD COLUMN prepared_result jsonb,
  ADD CONSTRAINT metric_extraction_jobs_prepared_result_check
    CHECK(prepared_result IS NULL OR jsonb_typeof(prepared_result)='object');

CREATE OR REPLACE FUNCTION platform.enforce_dataset_publication_request_review()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
  IF TG_OP='DELETE' THEN
    RAISE EXCEPTION '数据集发布审批申请不可删除' USING ERRCODE='23514';
  END IF;
  IF NEW.id IS DISTINCT FROM OLD.id
    OR NEW.tenant_id IS DISTINCT FROM OLD.tenant_id
    OR NEW.dataset_id IS DISTINCT FROM OLD.dataset_id
    OR NEW.draft_version_id IS DISTINCT FROM OLD.draft_version_id
    OR NEW.expected_dataset_version IS DISTINCT FROM OLD.expected_dataset_version
    OR NEW.expected_draft_record_version IS DISTINCT FROM OLD.expected_draft_record_version
    OR NEW.expected_dsl_hash IS DISTINCT FROM OLD.expected_dsl_hash
    OR NEW.expected_plan_hash IS DISTINCT FROM OLD.expected_plan_hash
    OR NEW.validation_parameters IS DISTINCT FROM OLD.validation_parameters
    OR NEW.requester_user_id IS DISTINCT FROM OLD.requester_user_id
    OR NEW.request_note IS DISTINCT FROM OLD.request_note
    OR NEW.submitted_at IS DISTINCT FROM OLD.submitted_at
    OR NEW.reserved_published_version_id IS DISTINCT FROM OLD.reserved_published_version_id THEN
    RAISE EXCEPTION '数据集发布审批申请事实不可修改' USING ERRCODE='23514';
  END IF;

  -- 审批仍为 PENDING 时，只允许同步生成过程更新内部候选暂存字段。
  IF OLD.status='PENDING' AND NEW.status='PENDING' THEN
    IF NEW.version IS DISTINCT FROM OLD.version
      OR NEW.reviewer_user_id IS DISTINCT FROM OLD.reviewer_user_id
      OR NEW.review_note IS DISTINCT FROM OLD.review_note
      OR NEW.published_version_id IS DISTINCT FROM OLD.published_version_id
      OR NEW.reviewed_at IS DISTINCT FROM OLD.reviewed_at
      OR NEW.updated_at IS NOT DISTINCT FROM OLD.updated_at THEN
      RAISE EXCEPTION '指标候选同步生成状态迁移无效' USING ERRCODE='23514';
    END IF;
    RETURN NEW;
  END IF;

  IF OLD.status<>'PENDING'
    OR NEW.status NOT IN ('APPROVED','REJECTED')
    OR NEW.version<>OLD.version+1
    OR NEW.updated_at IS NOT DISTINCT FROM OLD.updated_at THEN
    RAISE EXCEPTION '数据集发布审批状态迁移无效' USING ERRCODE='23514';
  END IF;
  RETURN NEW;
END
$$;

REVOKE ALL ON FUNCTION platform.enforce_dataset_publication_request_review() FROM PUBLIC;

COMMENT ON COLUMN platform.dataset_publication_requests.reserved_published_version_id IS
  '提交审批时预留的不可变发布版本 ID；同步候选生成和最终发布共同使用';
COMMENT ON COLUMN platform.dataset_publication_requests.metric_candidate_result IS
  '审批前内部暂存的规则事实与 LLM 语义结果；API 不返回正文，审批通过后才进入候选目录';
COMMENT ON COLUMN platform.metric_extraction_jobs.prepared_result IS
  '提交发布审批时已完成 LLM 补全的候选批次；Worker 不得再次请求模型';
