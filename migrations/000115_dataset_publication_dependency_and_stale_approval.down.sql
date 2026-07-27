DROP TRIGGER IF EXISTS dws_modeling_jobs_normalize_error_message
  ON platform.dws_modeling_jobs;
DROP FUNCTION IF EXISTS platform.normalize_dws_modeling_error_message();
ALTER TABLE platform.dws_modeling_jobs DROP COLUMN error_message;

DROP TRIGGER IF EXISTS dataset_versions_cancel_stale_publication_requests
  ON platform.dataset_versions;
DROP FUNCTION IF EXISTS
  platform.cancel_stale_dataset_publication_requests();

ALTER TABLE platform.dataset_publication_requests
  DISABLE TRIGGER dataset_publication_requests_enforce_review;
UPDATE platform.dataset_publication_requests
SET status='REJECTED',
    reviewer_user_id=requester_user_id,
    review_note=COALESCE(
      NULLIF(btrim(review_note),''),
      '数据集草稿变更后原发布审批申请已失效'
    ),
    reviewed_at=COALESCE(reviewed_at,clock_timestamp()),
    updated_at=clock_timestamp()
WHERE status='CANCELLED';
ALTER TABLE platform.dataset_publication_requests
  ENABLE TRIGGER dataset_publication_requests_enforce_review;

ALTER TABLE platform.dataset_publication_requests
  DROP CONSTRAINT dataset_publication_requests_status_check,
  DROP CONSTRAINT dataset_publication_requests_decision_shape;

ALTER TABLE platform.dataset_publication_requests
  ADD CONSTRAINT dataset_publication_requests_status_check
    CHECK(status IN ('PENDING','APPROVED','REJECTED')),
  ADD CONSTRAINT dataset_publication_requests_decision_shape CHECK(
    (status='PENDING'
      AND reviewer_user_id IS NULL AND reviewed_at IS NULL
      AND published_version_id IS NULL AND review_note='')
    OR (status='APPROVED'
      AND reviewer_user_id IS NOT NULL AND reviewed_at IS NOT NULL
      AND published_version_id IS NOT NULL)
    OR (status='REJECTED'
      AND reviewer_user_id IS NOT NULL AND reviewed_at IS NOT NULL
      AND published_version_id IS NULL AND btrim(review_note)<>'')
  );

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

REVOKE ALL ON FUNCTION
  platform.enforce_dataset_publication_request_review()
FROM PUBLIC;
