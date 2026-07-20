-- 数据集发布后以持久任务提取可审核的指标候选；候选不是正式指标，接受后只创建指标草稿。
ALTER TABLE platform.dataset_versions
  ADD CONSTRAINT dataset_versions_metric_extraction_identity_key
    UNIQUE(id,dataset_id,tenant_id,schema_hash);

CREATE TABLE platform.metric_extraction_jobs(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id),
  dataset_id uuid NOT NULL,
  dataset_version_id uuid NOT NULL,
  dsl_hash text NOT NULL CHECK(dsl_hash ~ '^[0-9a-f]{64}$'),
  requested_by uuid,
  extractor_version text NOT NULL CHECK(btrim(extractor_version)<>''),
  status text NOT NULL DEFAULT 'PENDING'
    CHECK(status IN ('PENDING','RUNNING','SUCCEEDED','PARTIAL','FAILED')),
  total integer NOT NULL DEFAULT 0 CHECK(total>=0),
  ready_count integer NOT NULL DEFAULT 0 CHECK(ready_count>=0),
  review_count integer NOT NULL DEFAULT 0 CHECK(review_count>=0),
  blocked_count integer NOT NULL DEFAULT 0 CHECK(blocked_count>=0),
  error_code text NOT NULL DEFAULT '',
  error_message text NOT NULL DEFAULT '',
  lease_owner text NOT NULL DEFAULT '',
  lease_expires_at timestamptz,
  heartbeat_at timestamptz,
  attempt integer NOT NULL DEFAULT 0 CHECK(attempt>=0),
  next_attempt_at timestamptz NOT NULL DEFAULT now(),
  created_at timestamptz NOT NULL DEFAULT now(),
  started_at timestamptz,
  completed_at timestamptz,
  CONSTRAINT metric_extraction_jobs_dataset_version_fk
    FOREIGN KEY(dataset_version_id,dataset_id,tenant_id,dsl_hash)
    REFERENCES platform.dataset_versions(id,dataset_id,tenant_id,schema_hash) ON DELETE RESTRICT,
  CONSTRAINT metric_extraction_jobs_requested_by_fk
    FOREIGN KEY(requested_by,tenant_id)
    REFERENCES platform.users(id,tenant_id) ON DELETE SET NULL (requested_by),
  CONSTRAINT metric_extraction_jobs_version_key
    UNIQUE(tenant_id,dataset_version_id,extractor_version),
  CONSTRAINT metric_extraction_jobs_identity_tenant_key UNIQUE(id,tenant_id),
  CONSTRAINT metric_extraction_jobs_candidate_identity_key
    UNIQUE(id,dataset_id,dataset_version_id,dsl_hash,tenant_id),
  CONSTRAINT metric_extraction_jobs_count_shape_check
    CHECK(ready_count+review_count+blocked_count<=total)
);

CREATE TABLE platform.metric_candidates(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id),
  job_id uuid NOT NULL,
  dataset_id uuid NOT NULL,
  dataset_version_id uuid NOT NULL,
  dsl_hash text NOT NULL CHECK(dsl_hash ~ '^[0-9a-f]{64}$'),
  name text NOT NULL CHECK(btrim(name)<>''),
  code citext NOT NULL CHECK(btrim(code::text)<>''),
  description text NOT NULL DEFAULT '',
  status text NOT NULL CHECK(status IN ('READY','NEEDS_REVIEW','BLOCKED','ACCEPTED','REJECTED')),
  method text NOT NULL DEFAULT 'RULE' CHECK(method IN ('RULE','LLM','HYBRID')),
  confidence numeric(5,4) NOT NULL CHECK(confidence>=0 AND confidence<=1),
  proposed_definition jsonb NOT NULL CHECK(jsonb_typeof(proposed_definition)='object'),
  source_field_ids text[] NOT NULL DEFAULT '{}',
  evidence jsonb NOT NULL DEFAULT '[]'::jsonb CHECK(jsonb_typeof(evidence)='array'),
  assumptions text[] NOT NULL DEFAULT '{}',
  warnings text[] NOT NULL DEFAULT '{}',
  block_reasons text[] NOT NULL DEFAULT '{}',
  fingerprint text NOT NULL CHECK(fingerprint ~ '^[0-9a-f]{64}$'),
  version bigint NOT NULL DEFAULT 1 CHECK(version>0),
  accepted_metric_id uuid,
  decision_reason text NOT NULL DEFAULT '',
  reviewed_by uuid,
  reviewed_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT metric_candidates_job_fk
    FOREIGN KEY(job_id,dataset_id,dataset_version_id,dsl_hash,tenant_id)
    REFERENCES platform.metric_extraction_jobs(id,dataset_id,dataset_version_id,dsl_hash,tenant_id)
    ON DELETE RESTRICT,
  CONSTRAINT metric_candidates_dataset_version_fk
    FOREIGN KEY(dataset_version_id,dataset_id,tenant_id,dsl_hash)
    REFERENCES platform.dataset_versions(id,dataset_id,tenant_id,schema_hash) ON DELETE RESTRICT,
  CONSTRAINT metric_candidates_reviewed_by_fk
    FOREIGN KEY(reviewed_by,tenant_id)
    REFERENCES platform.users(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT metric_candidates_fingerprint_key UNIQUE(tenant_id,fingerprint),
  CONSTRAINT metric_candidates_identity_tenant_key UNIQUE(id,tenant_id),
  CONSTRAINT metric_candidates_identity_dataset_tenant_key UNIQUE(id,dataset_id,tenant_id),
  CONSTRAINT metric_candidates_decision_shape_check CHECK(
    (status IN ('READY','NEEDS_REVIEW','BLOCKED')
      AND accepted_metric_id IS NULL AND reviewed_by IS NULL AND reviewed_at IS NULL)
    OR (status='ACCEPTED'
      AND accepted_metric_id IS NOT NULL AND reviewed_by IS NOT NULL AND reviewed_at IS NOT NULL)
    OR (status='REJECTED'
      AND accepted_metric_id IS NULL AND reviewed_by IS NOT NULL AND reviewed_at IS NOT NULL
      AND btrim(decision_reason)<>'')
  )
);

-- 候选接受和指标草稿创建处于同一事务；origin 身份让提交后丢失响应的重试找回同一草稿。
ALTER TABLE platform.metrics
  ADD COLUMN origin_candidate_id uuid,
  ADD CONSTRAINT metrics_origin_candidate_fk
    FOREIGN KEY(origin_candidate_id,dataset_id,tenant_id)
    REFERENCES platform.metric_candidates(id,dataset_id,tenant_id) ON DELETE RESTRICT;

CREATE UNIQUE INDEX metrics_origin_candidate_idx
  ON platform.metrics(tenant_id,origin_candidate_id)
  WHERE origin_candidate_id IS NOT NULL;

ALTER TABLE platform.metric_candidates
  ADD CONSTRAINT metric_candidates_accepted_metric_fk
    FOREIGN KEY(accepted_metric_id,dataset_id,tenant_id)
    REFERENCES platform.metrics(id,dataset_id,tenant_id) ON DELETE RESTRICT;

CREATE INDEX metric_extraction_jobs_claim_idx
  ON platform.metric_extraction_jobs(tenant_id,status,next_attempt_at,lease_expires_at,created_at);
CREATE INDEX metric_extraction_jobs_dataset_time_idx
  ON platform.metric_extraction_jobs(tenant_id,dataset_id,created_at DESC);
CREATE INDEX metric_candidates_review_queue_idx
  ON platform.metric_candidates(tenant_id,status,updated_at DESC,id);
CREATE INDEX metric_candidates_dataset_idx
  ON platform.metric_candidates(tenant_id,dataset_id,updated_at DESC,id);

-- 候选提取事实不可被审核动作改写；审核只允许从待处理态一次性进入终态。
CREATE OR REPLACE FUNCTION platform.enforce_metric_candidate_review()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
  IF TG_OP='DELETE' THEN
    RAISE EXCEPTION '指标候选不可删除';
  END IF;
  IF NEW.id IS DISTINCT FROM OLD.id
    OR NEW.tenant_id IS DISTINCT FROM OLD.tenant_id
    OR NEW.job_id IS DISTINCT FROM OLD.job_id
    OR NEW.dataset_id IS DISTINCT FROM OLD.dataset_id
    OR NEW.dataset_version_id IS DISTINCT FROM OLD.dataset_version_id
    OR NEW.dsl_hash IS DISTINCT FROM OLD.dsl_hash
    OR NEW.name IS DISTINCT FROM OLD.name
    OR NEW.code IS DISTINCT FROM OLD.code
    OR NEW.description IS DISTINCT FROM OLD.description
    OR NEW.method IS DISTINCT FROM OLD.method
    OR NEW.confidence IS DISTINCT FROM OLD.confidence
    OR NEW.proposed_definition IS DISTINCT FROM OLD.proposed_definition
    OR NEW.source_field_ids IS DISTINCT FROM OLD.source_field_ids
    OR NEW.evidence IS DISTINCT FROM OLD.evidence
    OR NEW.assumptions IS DISTINCT FROM OLD.assumptions
    OR NEW.warnings IS DISTINCT FROM OLD.warnings
    OR NEW.block_reasons IS DISTINCT FROM OLD.block_reasons
    OR NEW.fingerprint IS DISTINCT FROM OLD.fingerprint
    OR NEW.created_at IS DISTINCT FROM OLD.created_at THEN
    RAISE EXCEPTION '指标候选提取事实不可修改' USING ERRCODE='23514';
  END IF;
  IF OLD.status NOT IN ('READY','NEEDS_REVIEW','BLOCKED')
    OR NEW.status NOT IN ('ACCEPTED','REJECTED')
    OR NEW.version<>OLD.version+1
    OR NEW.updated_at IS NOT DISTINCT FROM OLD.updated_at THEN
    RAISE EXCEPTION '指标候选审核状态迁移无效' USING ERRCODE='23514';
  END IF;
  IF NEW.status='ACCEPTED' AND OLD.status='BLOCKED' THEN
    RAISE EXCEPTION '阻塞候选不能被接受' USING ERRCODE='23514';
  END IF;
  RETURN NEW;
END
$$;

REVOKE ALL ON FUNCTION platform.enforce_metric_candidate_review() FROM PUBLIC;

CREATE TRIGGER metric_candidates_enforce_review
BEFORE UPDATE OR DELETE ON platform.metric_candidates
FOR EACH ROW EXECUTE FUNCTION platform.enforce_metric_candidate_review();

ALTER TABLE platform.metric_extraction_jobs ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform.metric_extraction_jobs FORCE ROW LEVEL SECURITY;
ALTER TABLE platform.metric_candidates ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform.metric_candidates FORCE ROW LEVEL SECURITY;

CREATE POLICY metric_extraction_jobs_tenant_isolation ON platform.metric_extraction_jobs
  USING(tenant_id=platform.current_tenant_id())
  WITH CHECK(tenant_id=platform.current_tenant_id());
CREATE POLICY metric_candidates_tenant_isolation ON platform.metric_candidates
  USING(tenant_id=platform.current_tenant_id())
  WITH CHECK(tenant_id=platform.current_tenant_id());

-- 为迁移前已经发布且当前仍可用的精确版本补提取任务；规则版本保证重复迁移不会重复入队。
INSERT INTO platform.metric_extraction_jobs(
  tenant_id,dataset_id,dataset_version_id,dsl_hash,requested_by,extractor_version
)
SELECT version.tenant_id,version.dataset_id,version.id,version.schema_hash,
  version.published_by,'metric-candidate-v1'
FROM platform.dataset_versions AS version
JOIN platform.datasets AS dataset
  ON dataset.id=version.dataset_id AND dataset.tenant_id=version.tenant_id
 AND dataset.current_published_version_id=version.id
WHERE version.status='PUBLISHED' AND dataset.status='PUBLISHED' AND dataset.deleted_at IS NULL
ON CONFLICT(tenant_id,dataset_version_id,extractor_version) DO NOTHING;

COMMENT ON TABLE platform.metric_extraction_jobs IS '数据集精确发布版本的持久化指标候选提取任务';
COMMENT ON TABLE platform.metric_candidates IS '待人工审核的指标定义建议；接受前不属于正式指标目录';
COMMENT ON COLUMN platform.metric_candidates.proposed_definition IS '已通过服务端结构校验但尚未成为指标草稿的定义';
COMMENT ON COLUMN platform.metrics.origin_candidate_id IS '创建该指标草稿的候选标识，用于接受操作幂等恢复';
