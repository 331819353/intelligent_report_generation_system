-- 数据集发布改为“保存草稿 -> 提交申请 -> 人工审批 -> 原子发布”。
-- 申请固定精确草稿修订；审批通过与不可变版本、发布指针、审计和指标候选任务同事务提交。

CREATE TABLE platform.dataset_publication_requests(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id),
  dataset_id uuid NOT NULL,
  draft_version_id uuid NOT NULL,
  expected_dataset_version bigint NOT NULL CHECK(expected_dataset_version>0),
  expected_draft_record_version bigint NOT NULL CHECK(expected_draft_record_version>0),
  expected_dsl_hash text NOT NULL CHECK(expected_dsl_hash ~ '^[0-9a-f]{64}$'),
  expected_plan_hash text NOT NULL CHECK(expected_plan_hash ~ '^[0-9a-f]{64}$'),
  validation_parameters jsonb NOT NULL DEFAULT '{}'::jsonb
    CHECK(jsonb_typeof(validation_parameters)='object'),
  status text NOT NULL DEFAULT 'PENDING'
    CHECK(status IN ('PENDING','APPROVED','REJECTED')),
  version bigint NOT NULL DEFAULT 1 CHECK(version>0),
  requester_user_id uuid NOT NULL,
  request_note text NOT NULL DEFAULT '' CHECK(length(request_note)<=1000),
  reviewer_user_id uuid,
  review_note text NOT NULL DEFAULT '' CHECK(length(review_note)<=1000),
  published_version_id uuid,
  submitted_at timestamptz NOT NULL DEFAULT now(),
  reviewed_at timestamptz,
  updated_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT dataset_publication_requests_dataset_fk
    FOREIGN KEY(dataset_id,tenant_id)
    REFERENCES platform.datasets(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT dataset_publication_requests_draft_fk
    FOREIGN KEY(draft_version_id,dataset_id,tenant_id)
    REFERENCES platform.dataset_versions(id,dataset_id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT dataset_publication_requests_requester_fk
    FOREIGN KEY(requester_user_id,tenant_id)
    REFERENCES platform.users(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT dataset_publication_requests_reviewer_fk
    FOREIGN KEY(reviewer_user_id,tenant_id)
    REFERENCES platform.users(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT dataset_publication_requests_published_fk
    FOREIGN KEY(published_version_id,dataset_id,tenant_id)
    REFERENCES platform.dataset_versions(id,dataset_id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT dataset_publication_requests_exact_revision_key
    UNIQUE(tenant_id,dataset_id,draft_version_id,expected_draft_record_version),
  CONSTRAINT dataset_publication_requests_identity_tenant_key UNIQUE(id,tenant_id),
  CONSTRAINT dataset_publication_requests_decision_shape CHECK(
    (status='PENDING'
      AND reviewer_user_id IS NULL AND reviewed_at IS NULL
      AND published_version_id IS NULL AND review_note='')
    OR (status='APPROVED'
      AND reviewer_user_id IS NOT NULL AND reviewed_at IS NOT NULL
      AND published_version_id IS NOT NULL)
    OR (status='REJECTED'
      AND reviewer_user_id IS NOT NULL AND reviewed_at IS NOT NULL
      AND published_version_id IS NULL AND btrim(review_note)<>'')
  )
);

CREATE INDEX dataset_publication_requests_queue_idx
  ON platform.dataset_publication_requests(tenant_id,status,submitted_at,id);
CREATE INDEX dataset_publication_requests_dataset_idx
  ON platform.dataset_publication_requests(tenant_id,dataset_id,submitted_at DESC,id DESC);

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
    OR NEW.submitted_at IS DISTINCT FROM OLD.submitted_at THEN
    RAISE EXCEPTION '数据集发布审批申请事实不可修改' USING ERRCODE='23514';
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

CREATE TRIGGER dataset_publication_requests_enforce_review
BEFORE UPDATE OR DELETE ON platform.dataset_publication_requests
FOR EACH ROW EXECUTE FUNCTION platform.enforce_dataset_publication_request_review();

ALTER TABLE platform.dataset_publication_requests ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform.dataset_publication_requests FORCE ROW LEVEL SECURITY;
CREATE POLICY dataset_publication_requests_tenant_isolation
  ON platform.dataset_publication_requests
  USING(tenant_id=platform.current_tenant_id())
  WITH CHECK(tenant_id=platform.current_tenant_id());

UPDATE platform.permissions
SET name='审批发布数据集',description='审核数据集发布申请，并在通过后生成不可变发布版本'
WHERE code='dataset.publish' AND resource_type='DATASET' AND action='PUBLISH';

COMMENT ON TABLE platform.dataset_publication_requests IS
  '数据集精确草稿修订的人工发布审批；通过状态与不可变发布版本原子绑定';
COMMENT ON COLUMN platform.dataset_publication_requests.validation_parameters IS
  '提交时冻结的发布试跑参数；API 响应不返回具体值';
