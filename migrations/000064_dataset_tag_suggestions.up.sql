-- 已发布 ODS/DWD/DWS 数据集的自动标签建议。
--
-- dataset_tag_suggestion_jobs 本身是发布事务的 durable outbox；模型输出只能
-- 解析成不可变 item，并最多创建 SUGGESTED 绑定。APPROVED/REJECTED 仍由现有
-- semantic management API 完成人工治理，随后由 v60 的触发器异步重建向量。

ALTER TABLE platform.ai_requests
  DROP CONSTRAINT ai_requests_purpose_check;

ALTER TABLE platform.ai_requests
  ADD CONSTRAINT ai_requests_purpose_check CHECK(purpose IN (
    'METADATA_COMPLETION',
    'REPORT_GENERATION',
    'BLOCK_EDIT',
    'CONCLUSION_GENERATION',
    'DATASET_DAG_GENERATION',
    'METRIC_AUTHORING',
    'DATASET_TAG_SUGGESTION'
  ));

CREATE TABLE platform.dataset_tag_suggestion_jobs(
  id uuid PRIMARY KEY DEFAULT public.gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id),
  dataset_id uuid NOT NULL,
  dataset_version_id uuid NOT NULL,
  schema_hash text NOT NULL CHECK(schema_hash ~ '^[0-9a-f]{64}$'),
  source_version_snapshot jsonb NOT NULL CHECK(
    jsonb_typeof(source_version_snapshot)='array'
    AND pg_column_size(source_version_snapshot)<=65536
    AND platform.materialization_json_is_safe(source_version_snapshot)
  ),
  source_version_snapshot_hash text NOT NULL CHECK(
    source_version_snapshot_hash ~ '^[0-9a-f]{64}$'
  ),
  layer text NOT NULL CHECK(layer IN ('ODS','DWD','DWS')),
  prompt_version text NOT NULL CHECK(
    length(prompt_version) BETWEEN 1 AND 128
    AND prompt_version=btrim(prompt_version)
    AND prompt_version !~ '[[:cntrl:]]'
  ),
  status text NOT NULL DEFAULT 'PENDING'
    CHECK(status IN ('PENDING','RUNNING','SUCCEEDED','FAILED','SKIPPED')),
  requested_by uuid,
  attempt integer NOT NULL DEFAULT 0 CHECK(attempt>=0),
  max_attempts integer NOT NULL DEFAULT 3 CHECK(max_attempts BETWEEN 1 AND 5),
  next_attempt_at timestamptz NOT NULL DEFAULT now(),
  lease_owner text NOT NULL DEFAULT '' CHECK(
    length(lease_owner)<=128
    AND lease_owner=btrim(lease_owner)
    AND lease_owner !~ '[[:cntrl:]]'
  ),
  lease_token uuid,
  lease_expires_at timestamptz,
  ai_request_id uuid,
  input_hash text NOT NULL DEFAULT '' CHECK(
    input_hash='' OR input_hash ~ '^[0-9a-f]{64}$'
  ),
  output_hash text NOT NULL DEFAULT '' CHECK(
    output_hash='' OR output_hash ~ '^[0-9a-f]{64}$'
  ),
  suggestion_count integer NOT NULL DEFAULT 0 CHECK(suggestion_count>=0),
  binding_count integer NOT NULL DEFAULT 0 CHECK(
    binding_count>=0 AND binding_count<=suggestion_count
  ),
  error_code text NOT NULL DEFAULT '' CHECK(
    error_code='' OR error_code ~ '^[A-Z][A-Z0-9_]{1,127}$'
  ),
  error_message text NOT NULL DEFAULT '' CHECK(
    length(error_message)<=1024
    AND error_message !~ '[[:cntrl:]]'
  ),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  started_at timestamptz,
  completed_at timestamptz,
  CONSTRAINT dataset_tag_suggestion_jobs_version_fk
    FOREIGN KEY(dataset_version_id,dataset_id,tenant_id)
    REFERENCES platform.dataset_versions(id,dataset_id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT dataset_tag_suggestion_jobs_actor_fk
    FOREIGN KEY(requested_by,tenant_id)
    REFERENCES platform.users(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT dataset_tag_suggestion_jobs_ai_request_fk
    FOREIGN KEY(ai_request_id,tenant_id)
    REFERENCES platform.ai_requests(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT dataset_tag_suggestion_jobs_attempt_budget_check
    CHECK(attempt<=max_attempts),
  CONSTRAINT dataset_tag_suggestion_jobs_result_shape_check CHECK(
    (status='PENDING'
      AND lease_owner='' AND lease_token IS NULL AND lease_expires_at IS NULL
      AND completed_at IS NULL AND ai_request_id IS NULL
      AND output_hash='' AND suggestion_count=0 AND binding_count=0)
    OR
    (status='RUNNING'
      AND attempt>0 AND started_at IS NOT NULL AND completed_at IS NULL
      AND lease_owner<>'' AND lease_token IS NOT NULL AND lease_expires_at IS NOT NULL
      AND error_code='' AND error_message=''
      AND ai_request_id IS NULL AND output_hash=''
      AND suggestion_count=0 AND binding_count=0)
    OR
    (status='SUCCEEDED'
      AND attempt>0 AND started_at IS NOT NULL AND completed_at IS NOT NULL
      AND completed_at>=started_at
      AND lease_owner='' AND lease_token IS NULL AND lease_expires_at IS NULL
      AND ai_request_id IS NOT NULL
      AND input_hash ~ '^[0-9a-f]{64}$'
      AND output_hash ~ '^[0-9a-f]{64}$'
      AND error_code='' AND error_message='')
    OR
    (status='FAILED'
      AND attempt>0 AND started_at IS NOT NULL AND completed_at IS NOT NULL
      AND completed_at>=started_at
      AND lease_owner='' AND lease_token IS NULL AND lease_expires_at IS NULL
      AND error_code<>''
      AND output_hash='' AND suggestion_count=0 AND binding_count=0)
    OR
    (status='SKIPPED'
      AND completed_at IS NOT NULL
      AND (started_at IS NULL OR completed_at>=started_at)
      AND lease_owner='' AND lease_token IS NULL AND lease_expires_at IS NULL
      AND error_code<>''
      AND ai_request_id IS NULL AND output_hash=''
      AND suggestion_count=0 AND binding_count=0)
  ),
  CONSTRAINT dataset_tag_suggestion_jobs_idempotency_key
    UNIQUE(tenant_id,dataset_version_id,prompt_version),
  CONSTRAINT dataset_tag_suggestion_jobs_identity_tenant_key UNIQUE(id,tenant_id)
);

ALTER TABLE platform.asset_tag_bindings
  ADD CONSTRAINT asset_tag_bindings_identity_tenant_key UNIQUE(id,tenant_id);

CREATE TABLE platform.dataset_tag_suggestion_items(
  id uuid PRIMARY KEY DEFAULT public.gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id),
  job_id uuid NOT NULL,
  lease_token uuid NOT NULL,
  ordinal_position integer NOT NULL CHECK(ordinal_position>0 AND ordinal_position<=256),
  tag_id uuid NOT NULL,
  tag_code text NOT NULL CHECK(
    length(tag_code) BETWEEN 1 AND 128
    AND tag_code=btrim(tag_code)
    AND tag_code !~ '[[:cntrl:]]'
  ),
  tag_name text NOT NULL CHECK(
    length(tag_name) BETWEEN 1 AND 256
    AND tag_name=btrim(tag_name)
    AND tag_name !~ '[[:cntrl:]]'
  ),
  category text NOT NULL CHECK(category IN (
    'BUSINESS_DOMAIN','BUSINESS_ENTITY','TABLE_FUNCTION',
    'USAGE_SCOPE','DATA_GRAIN','JOIN_ROLE'
  )),
  confidence numeric(5,4) NOT NULL CHECK(confidence BETWEEN 0 AND 1),
  rationale text NOT NULL DEFAULT '' CHECK(
    length(rationale)<=1024
    AND rationale !~ '[[:cntrl:]]'
  ),
  resolution text NOT NULL CHECK(resolution IN (
    'CREATED_SUGGESTION',
    'EXISTING_SUGGESTION',
    'EXISTING_APPROVED',
    'EXISTING_REJECTED'
  )),
  binding_id uuid NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT dataset_tag_suggestion_items_job_fk
    FOREIGN KEY(job_id,tenant_id)
    REFERENCES platform.dataset_tag_suggestion_jobs(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT dataset_tag_suggestion_items_tag_fk
    FOREIGN KEY(tag_id,tenant_id)
    REFERENCES platform.semantic_tags(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT dataset_tag_suggestion_items_binding_fk
    FOREIGN KEY(binding_id,tenant_id)
    REFERENCES platform.asset_tag_bindings(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT dataset_tag_suggestion_items_job_ordinal_key
    UNIQUE(tenant_id,job_id,ordinal_position),
  CONSTRAINT dataset_tag_suggestion_items_job_tag_key
    UNIQUE(tenant_id,job_id,tag_id),
  CONSTRAINT dataset_tag_suggestion_items_identity_tenant_key UNIQUE(id,tenant_id)
);

CREATE INDEX dataset_tag_suggestion_jobs_claim_idx
  ON platform.dataset_tag_suggestion_jobs(
    tenant_id,status,next_attempt_at,lease_expires_at,created_at,id
  )
  WHERE status IN ('PENDING','RUNNING');

CREATE INDEX dataset_tag_suggestion_jobs_dataset_idx
  ON platform.dataset_tag_suggestion_jobs(
    tenant_id,dataset_id,dataset_version_id,created_at DESC
  );

CREATE INDEX dataset_tag_suggestion_items_binding_idx
  ON platform.dataset_tag_suggestion_items(tenant_id,binding_id);

CREATE OR REPLACE FUNCTION platform.enqueue_dataset_tag_suggestion()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,platform
AS $$
DECLARE
  source_snapshot jsonb;
BEGIN
  IF NEW.status='PUBLISHED' AND OLD.status IS DISTINCT FROM 'PUBLISHED' THEN
    SELECT COALESCE(
      jsonb_agg(
        jsonb_build_object(
          'dataSourceId',source_fact.data_source_id,
          'dataSourceVersionId',source_fact.data_source_version_id
        )
        ORDER BY source_fact.data_source_id
      ),
      '[]'::jsonb
    )
    INTO source_snapshot
    FROM (
      SELECT DISTINCT
        source.id::text AS data_source_id,
        COALESCE(source.current_published_version_id::text,'') AS data_source_version_id
      FROM platform.dataset_dependencies AS dependency
      JOIN platform.metadata_tables AS source_table
        ON dependency.source_type='TABLE'
       AND source_table.id::text=dependency.source_id
       AND source_table.tenant_id=dependency.tenant_id
      JOIN platform.data_sources AS source
        ON source.id=source_table.data_source_id
       AND source.tenant_id=source_table.tenant_id
      WHERE dependency.tenant_id=NEW.tenant_id
        AND dependency.dataset_version_id=NEW.id
    ) AS source_fact;

    INSERT INTO platform.dataset_tag_suggestion_jobs(
      tenant_id,dataset_id,dataset_version_id,schema_hash,
      source_version_snapshot,source_version_snapshot_hash,layer,
      prompt_version,requested_by
    ) VALUES(
      NEW.tenant_id,NEW.dataset_id,NEW.id,NEW.schema_hash,
      source_snapshot,encode(public.digest(source_snapshot::text,'sha256'),'hex'),NEW.layer,
      'dataset-tag-suggestion-v1',NEW.published_by
    )
    ON CONFLICT(tenant_id,dataset_version_id,prompt_version) DO NOTHING;
  END IF;
  RETURN NEW;
END
$$;

REVOKE ALL ON FUNCTION platform.enqueue_dataset_tag_suggestion() FROM PUBLIC;

CREATE TRIGGER dataset_versions_enqueue_tag_suggestion
AFTER UPDATE OF status ON platform.dataset_versions
FOR EACH ROW EXECUTE FUNCTION platform.enqueue_dataset_tag_suggestion();

-- 当前发布指针是迁移时唯一可安全自动回填的版本；旧历史版本不应在缺少
-- 当前性保证时重新送入模型。
INSERT INTO platform.dataset_tag_suggestion_jobs(
  tenant_id,dataset_id,dataset_version_id,schema_hash,
  source_version_snapshot,source_version_snapshot_hash,layer,
  prompt_version,requested_by
)
SELECT
  version.tenant_id,version.dataset_id,version.id,version.schema_hash,
  source_facts.snapshot,
  encode(public.digest(source_facts.snapshot::text,'sha256'),'hex'),
  version.layer,
  'dataset-tag-suggestion-v1',version.published_by
FROM platform.dataset_versions AS version
JOIN platform.datasets AS dataset
 ON dataset.id=version.dataset_id
 AND dataset.tenant_id=version.tenant_id
 AND dataset.current_published_version_id=version.id
CROSS JOIN LATERAL (
  SELECT COALESCE(
    jsonb_agg(
      jsonb_build_object(
        'dataSourceId',source_fact.data_source_id,
        'dataSourceVersionId',source_fact.data_source_version_id
      )
      ORDER BY source_fact.data_source_id
    ),
    '[]'::jsonb
  ) AS snapshot
  FROM (
    SELECT DISTINCT
      source.id::text AS data_source_id,
      COALESCE(source.current_published_version_id::text,'') AS data_source_version_id
    FROM platform.dataset_dependencies AS dependency
    JOIN platform.metadata_tables AS source_table
      ON dependency.source_type='TABLE'
     AND source_table.id::text=dependency.source_id
     AND source_table.tenant_id=dependency.tenant_id
    JOIN platform.data_sources AS source
      ON source.id=source_table.data_source_id
     AND source.tenant_id=source_table.tenant_id
    WHERE dependency.tenant_id=version.tenant_id
      AND dependency.dataset_version_id=version.id
  ) AS source_fact
) AS source_facts
WHERE version.status='PUBLISHED'
  AND dataset.status='PUBLISHED'
  AND dataset.deleted_at IS NULL
ON CONFLICT(tenant_id,dataset_version_id,prompt_version) DO NOTHING;

CREATE OR REPLACE FUNCTION platform.enforce_dataset_tag_suggestion_job_transition()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
  IF TG_OP='DELETE' THEN
    RAISE EXCEPTION '数据集标签建议任务不可删除';
  END IF;
  IF ROW(
    NEW.id,NEW.tenant_id,NEW.dataset_id,NEW.dataset_version_id,
    NEW.schema_hash,NEW.source_version_snapshot,NEW.source_version_snapshot_hash,
    NEW.layer,NEW.prompt_version,NEW.requested_by,
    NEW.max_attempts,NEW.created_at
  ) IS DISTINCT FROM ROW(
    OLD.id,OLD.tenant_id,OLD.dataset_id,OLD.dataset_version_id,
    OLD.schema_hash,OLD.source_version_snapshot,OLD.source_version_snapshot_hash,
    OLD.layer,OLD.prompt_version,OLD.requested_by,
    OLD.max_attempts,OLD.created_at
  ) THEN
    RAISE EXCEPTION '数据集标签建议任务身份不可修改';
  END IF;
  IF OLD.status IN ('SUCCEEDED','FAILED','SKIPPED') THEN
    RAISE EXCEPTION '已终结的数据集标签建议任务不可修改';
  END IF;
  IF NOT (
    (OLD.status='PENDING' AND NEW.status IN ('PENDING','RUNNING','FAILED','SKIPPED'))
    OR
    (OLD.status='RUNNING' AND NEW.status IN (
      'PENDING','RUNNING','SUCCEEDED','FAILED','SKIPPED'
    ))
  ) THEN
    RAISE EXCEPTION '数据集标签建议任务状态转换无效';
  END IF;
  IF NEW.attempt<OLD.attempt THEN
    RAISE EXCEPTION '数据集标签建议任务尝试次数不可回退';
  END IF;
  RETURN NEW;
END
$$;

CREATE TRIGGER dataset_tag_suggestion_jobs_transition
BEFORE UPDATE OR DELETE ON platform.dataset_tag_suggestion_jobs
FOR EACH ROW EXECUTE FUNCTION platform.enforce_dataset_tag_suggestion_job_transition();

CREATE OR REPLACE FUNCTION platform.enforce_dataset_tag_suggestion_item_insert()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
  IF TG_OP<>'INSERT' THEN
    RAISE EXCEPTION '数据集标签建议项不可修改或删除';
  END IF;
  IF NOT EXISTS(
    SELECT 1
    FROM platform.dataset_tag_suggestion_jobs AS job
    WHERE job.id=NEW.job_id
      AND job.tenant_id=NEW.tenant_id
      AND job.status='RUNNING'
      AND job.lease_token=NEW.lease_token
      AND job.lease_expires_at>now()
  ) THEN
    RAISE EXCEPTION '只能为持有有效租约的任务写入标签建议项';
  END IF;
  RETURN NEW;
END
$$;

CREATE TRIGGER dataset_tag_suggestion_items_immutable
BEFORE INSERT OR UPDATE OR DELETE ON platform.dataset_tag_suggestion_items
FOR EACH ROW EXECUTE FUNCTION platform.enforce_dataset_tag_suggestion_item_insert();

CREATE TRIGGER dataset_tag_suggestion_jobs_set_updated_at
BEFORE UPDATE ON platform.dataset_tag_suggestion_jobs
FOR EACH ROW EXECUTE FUNCTION platform.set_updated_at();

ALTER TABLE platform.dataset_tag_suggestion_jobs ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform.dataset_tag_suggestion_jobs FORCE ROW LEVEL SECURITY;
ALTER TABLE platform.dataset_tag_suggestion_items ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform.dataset_tag_suggestion_items FORCE ROW LEVEL SECURITY;

CREATE POLICY dataset_tag_suggestion_jobs_tenant_isolation
  ON platform.dataset_tag_suggestion_jobs
  USING(tenant_id=platform.current_tenant_id())
  WITH CHECK(tenant_id=platform.current_tenant_id());

CREATE POLICY dataset_tag_suggestion_items_tenant_isolation
  ON platform.dataset_tag_suggestion_items
  USING(tenant_id=platform.current_tenant_id())
  WITH CHECK(tenant_id=platform.current_tenant_id());

COMMENT ON TABLE platform.dataset_tag_suggestion_jobs IS
  '发布事务写入的精确数据集版本标签建议 outbox；带租约、fencing 和有界重试';
COMMENT ON TABLE platform.dataset_tag_suggestion_items IS
  'LLM 从 ACTIVE CONTROLLED taxonomy 中选择的不可变建议证据；不包含业务样本';
COMMENT ON COLUMN platform.dataset_tag_suggestion_jobs.schema_hash IS
  '入队时的精确目标版本 schema hash；处理与写回前均需重新核对';
COMMENT ON COLUMN platform.dataset_tag_suggestion_jobs.source_version_snapshot IS
  '发布事务冻结的物理数据源发布版本指针；不含连接配置、凭据或业务样本';
COMMENT ON COLUMN platform.dataset_tag_suggestion_items.resolution IS
  '建议创建了新 SUGGESTED 绑定，或命中了已有绑定及其人工治理状态';
