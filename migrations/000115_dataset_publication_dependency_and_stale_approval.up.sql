-- DWD 发布以已发布 DIM 为硬依赖；审批申请则只对提交时冻结的精确草稿
-- 有效。草稿发生任何结构修订后，旧申请自动取消，避免审批中心继续处理
-- 已失效快照。DWS 等待 DWD 物化属于可恢复等待态，不再显示成无详情错误。

ALTER TABLE platform.dataset_publication_requests
  DROP CONSTRAINT dataset_publication_requests_status_check,
  DROP CONSTRAINT dataset_publication_requests_decision_shape;

ALTER TABLE platform.dataset_publication_requests
  ADD CONSTRAINT dataset_publication_requests_status_check
    CHECK(status IN ('PENDING','APPROVED','REJECTED','CANCELLED')),
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
    OR (status='CANCELLED'
      AND reviewer_user_id IS NULL AND reviewed_at IS NOT NULL
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
    OR NEW.status NOT IN ('APPROVED','REJECTED','CANCELLED')
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

CREATE OR REPLACE FUNCTION
  platform.cancel_stale_dataset_publication_requests()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,platform
SET row_security=off
AS $$
DECLARE
  cancelled_request record;
  cancellation_note constant text :=
    '数据集草稿已变更，原发布审批申请已自动取消';
BEGIN
  IF NEW.status<>'DRAFT'
     OR (
       NEW.record_version IS NOT DISTINCT FROM OLD.record_version
       AND NEW.schema_hash IS NOT DISTINCT FROM OLD.schema_hash
       AND NEW.plan_hash IS NOT DISTINCT FROM OLD.plan_hash
     ) THEN
    RETURN NEW;
  END IF;

  FOR cancelled_request IN
    UPDATE platform.dataset_publication_requests AS request
    SET status='CANCELLED',
        review_note=cancellation_note,
        reviewed_at=clock_timestamp(),
        version=request.version+1,
        updated_at=clock_timestamp()
    WHERE request.tenant_id=NEW.tenant_id
      AND request.dataset_id=NEW.dataset_id
      AND request.draft_version_id=NEW.id
      AND request.status='PENDING'
      AND (
        request.expected_draft_record_version<>NEW.record_version
        OR request.expected_dsl_hash<>NEW.schema_hash
        OR request.expected_plan_hash<>NEW.plan_hash
      )
    RETURNING request.id,request.dataset_id
  LOOP
    UPDATE platform.metric_candidate_preparation_jobs
    SET status='CANCELLED',
        error_code='PUBLICATION_REQUEST_CANCELLED',
        error_message=cancellation_note,
        completed_at=clock_timestamp(),
        updated_at=clock_timestamp(),
        lease_owner='',
        lease_expires_at=NULL
    WHERE tenant_id=NEW.tenant_id
      AND publication_request_id=cancelled_request.id
      AND status IN ('PENDING','RUNNING');

    INSERT INTO platform.audit_logs(
      tenant_id,actor_user_id,action,resource_type,resource_id,detail
    ) VALUES(
      NEW.tenant_id,NEW.updated_by,'AUTO_CANCEL',
      'DATASET_PUBLICATION_REQUEST',cancelled_request.id::text,
      jsonb_build_object(
        'datasetId',cancelled_request.dataset_id::text,
        'draftVersionId',NEW.id::text,
        'draftRecordVersion',NEW.record_version,
        'reason','DRAFT_CHANGED'
      )
    );
  END LOOP;
  RETURN NEW;
END
$$;

REVOKE ALL ON FUNCTION
  platform.cancel_stale_dataset_publication_requests()
FROM PUBLIC;

DROP TRIGGER IF EXISTS dataset_versions_cancel_stale_publication_requests
  ON platform.dataset_versions;
CREATE TRIGGER dataset_versions_cancel_stale_publication_requests
AFTER UPDATE OF record_version,schema_hash,plan_hash
ON platform.dataset_versions
FOR EACH ROW
EXECUTE FUNCTION platform.cancel_stale_dataset_publication_requests();

-- 修复迁移执行前已经失效但仍停留在待审批队列中的申请。
UPDATE platform.dataset_publication_requests AS request
SET status='CANCELLED',
    review_note='数据集草稿已变更，原发布审批申请已自动取消',
    reviewed_at=clock_timestamp(),
    version=request.version+1,
    updated_at=clock_timestamp()
FROM platform.dataset_versions AS draft
WHERE request.tenant_id=draft.tenant_id
  AND request.draft_version_id=draft.id
  AND request.status='PENDING'
  AND (
    request.expected_draft_record_version<>draft.record_version
    OR request.expected_dsl_hash<>draft.schema_hash
    OR request.expected_plan_hash<>draft.plan_hash
  );

ALTER TABLE platform.dws_modeling_jobs
  ADD COLUMN error_message text NOT NULL DEFAULT ''
  CHECK(
    length(error_message)<=1024
    AND error_message !~ '[[:cntrl:]]'
  );

UPDATE platform.dws_modeling_jobs
SET error_message=
  '等待 DWD 发布版本完成物化；物化转为可用后，主题建模会自动继续'
WHERE status='WAITING_DEPENDENCY'
  AND error_code='WAITING_ACTIVE_DWD_MATERIALIZATION';

CREATE OR REPLACE FUNCTION platform.normalize_dws_modeling_error_message()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
  IF NEW.error_code='' THEN
    NEW.error_message='';
  ELSIF NEW.error_code='WAITING_ACTIVE_DWD_MATERIALIZATION'
        AND btrim(NEW.error_message)='' THEN
    NEW.error_message=
      '等待 DWD 发布版本完成物化；物化转为可用后，主题建模会自动继续';
  ELSIF btrim(NEW.error_message)='' THEN
    NEW.error_message='DWS 建模未完成，请在任务运行中心查看状态或重试';
  END IF;
  RETURN NEW;
END
$$;

REVOKE ALL ON FUNCTION platform.normalize_dws_modeling_error_message()
FROM PUBLIC;

CREATE TRIGGER dws_modeling_jobs_normalize_error_message
BEFORE INSERT OR UPDATE OF status,error_code,error_message
ON platform.dws_modeling_jobs
FOR EACH ROW
EXECUTE FUNCTION platform.normalize_dws_modeling_error_message();

COMMENT ON COLUMN platform.dws_modeling_jobs.error_message IS
  '任务中心可展示的安全状态说明；等待 DWD 物化时提供自动恢复提示';
COMMENT ON FUNCTION
  platform.cancel_stale_dataset_publication_requests() IS
  '草稿结构修订后自动取消冻结旧草稿的待审批申请';
