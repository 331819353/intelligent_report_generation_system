-- Restore the dataset-center DWS draft workflow that was removed together with
-- the retired semantic-Q&A subsystem in 000195. This schema is deliberately
-- limited to LLM DAG draft generation; it does not restore metrics, reports,
-- semantic query tables, automatic publication, or automatic materialization.

CREATE TABLE platform.dws_modeling_jobs(
  id uuid PRIMARY KEY DEFAULT public.gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id),
  source_dwd_dataset_id uuid NOT NULL,
  source_dwd_version_id uuid NOT NULL,
  requested_by uuid NOT NULL,
  status text NOT NULL DEFAULT 'PENDING' CHECK(status IN (
    'PENDING','RUNNING','SUCCEEDED','PARTIAL','FAILED','SKIPPED'
  )),
  not_before timestamptz NOT NULL DEFAULT now(),
  next_attempt_at timestamptz NOT NULL DEFAULT now(),
  attempt integer NOT NULL DEFAULT 0 CHECK(attempt BETWEEN 0 AND 5),
  max_attempts integer NOT NULL DEFAULT 3 CHECK(max_attempts BETWEEN 1 AND 5),
  lease_owner text NOT NULL DEFAULT '' CHECK(
    length(lease_owner)<=128 AND lease_owner !~ '[[:cntrl:]]'
  ),
  lease_token uuid,
  lease_expires_at timestamptz,
  group_key text NOT NULL CHECK(
    length(group_key) BETWEEN 1 AND 128 AND group_key !~ '[[:cntrl:]]'
  ),
  source_scope jsonb NOT NULL CHECK(
    jsonb_typeof(source_scope)='object'
    AND jsonb_typeof(source_scope->'dwd')='array'
    AND jsonb_array_length(source_scope->'dwd')=1
    AND jsonb_typeof(source_scope->'dim')='array'
    AND jsonb_array_length(source_scope->'dim') BETWEEN 0 AND 64
    AND pg_column_size(source_scope)<=131072
    AND platform.materialization_json_is_safe(source_scope)
  ),
  scope_hash text NOT NULL CHECK(scope_hash ~ '^[0-9a-f]{64}$'),
  ai_request_id uuid,
  generated_count integer NOT NULL DEFAULT 0 CHECK(generated_count>=0),
  updated_count integer NOT NULL DEFAULT 0 CHECK(updated_count>=0),
  skipped_count integer NOT NULL DEFAULT 0 CHECK(skipped_count>=0),
  result_json jsonb NOT NULL DEFAULT '{}'::jsonb CHECK(
    jsonb_typeof(result_json)='object'
    AND pg_column_size(result_json)<=65536
    AND platform.materialization_json_is_safe(result_json)
  ),
  error_code text NOT NULL DEFAULT '' CHECK(length(error_code)<=128),
  error_message text NOT NULL DEFAULT '' CHECK(
    length(error_message)<=1024 AND error_message !~ '[[:cntrl:]]'
  ),
  requested_at timestamptz NOT NULL DEFAULT now(),
  started_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  completed_at timestamptz,
  CONSTRAINT dws_modeling_jobs_source_version_fk
    FOREIGN KEY(source_dwd_version_id,source_dwd_dataset_id,tenant_id)
    REFERENCES platform.dataset_versions(id,dataset_id,tenant_id)
    ON DELETE RESTRICT,
  CONSTRAINT dws_modeling_jobs_actor_fk
    FOREIGN KEY(requested_by,tenant_id)
    REFERENCES platform.users(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT dws_modeling_jobs_ai_request_fk
    FOREIGN KEY(ai_request_id,tenant_id)
    REFERENCES platform.ai_requests(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT dws_modeling_jobs_scope_key
    UNIQUE(tenant_id,source_dwd_version_id,scope_hash),
  CONSTRAINT dws_modeling_jobs_identity_tenant_key UNIQUE(id,tenant_id),
  CONSTRAINT dws_modeling_jobs_lease_shape_check CHECK(
    (status='RUNNING' AND lease_owner<>'' AND lease_token IS NOT NULL
      AND lease_expires_at IS NOT NULL AND completed_at IS NULL)
    OR
    (status<>'RUNNING' AND lease_owner='' AND lease_token IS NULL
      AND lease_expires_at IS NULL)
  )
);

CREATE INDEX dws_modeling_jobs_claim_idx
  ON platform.dws_modeling_jobs(
    tenant_id,status,next_attempt_at,lease_expires_at,created_at,id
  )
  WHERE status IN ('PENDING','RUNNING');

CREATE TABLE platform.dws_modeling_outputs(
  id uuid PRIMARY KEY DEFAULT public.gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id),
  source_dwd_dataset_id uuid NOT NULL,
  dws_dataset_id uuid NOT NULL,
  last_source_dwd_version_id uuid NOT NULL,
  last_job_id uuid NOT NULL,
  last_generated_dsl_hash text NOT NULL CHECK(
    last_generated_dsl_hash ~ '^[0-9a-f]{64}$'
  ),
  last_action text NOT NULL CHECK(
    last_action IN ('CREATED','UPDATED','UNCHANGED','MANUAL_OWNED')
  ),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT dws_modeling_outputs_source_fk
    FOREIGN KEY(source_dwd_dataset_id,tenant_id)
    REFERENCES platform.datasets(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT dws_modeling_outputs_dws_fk
    FOREIGN KEY(dws_dataset_id,tenant_id)
    REFERENCES platform.datasets(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT dws_modeling_outputs_source_version_fk
    FOREIGN KEY(last_source_dwd_version_id,source_dwd_dataset_id,tenant_id)
    REFERENCES platform.dataset_versions(id,dataset_id,tenant_id)
    ON DELETE RESTRICT,
  CONSTRAINT dws_modeling_outputs_job_fk
    FOREIGN KEY(last_job_id,tenant_id)
    REFERENCES platform.dws_modeling_jobs(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT dws_modeling_outputs_source_key
    UNIQUE(tenant_id,source_dwd_dataset_id),
  CONSTRAINT dws_modeling_outputs_dws_key UNIQUE(tenant_id,dws_dataset_id),
  CONSTRAINT dws_modeling_outputs_identity_tenant_key UNIQUE(id,tenant_id)
);

CREATE TRIGGER dws_modeling_jobs_set_updated_at
BEFORE UPDATE ON platform.dws_modeling_jobs
FOR EACH ROW EXECUTE FUNCTION platform.set_updated_at();

CREATE TRIGGER dws_modeling_outputs_set_updated_at
BEFORE UPDATE ON platform.dws_modeling_outputs
FOR EACH ROW EXECUTE FUNCTION platform.set_updated_at();

ALTER TABLE platform.dws_modeling_jobs ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform.dws_modeling_jobs FORCE ROW LEVEL SECURITY;
ALTER TABLE platform.dws_modeling_outputs ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform.dws_modeling_outputs FORCE ROW LEVEL SECURITY;

CREATE POLICY dws_modeling_jobs_tenant_isolation
  ON platform.dws_modeling_jobs
  USING(tenant_id=platform.current_tenant_id())
  WITH CHECK(tenant_id=platform.current_tenant_id());

CREATE POLICY dws_modeling_outputs_tenant_isolation
  ON platform.dws_modeling_outputs
  USING(tenant_id=platform.current_tenant_id())
  WITH CHECK(tenant_id=platform.current_tenant_id());

COMMENT ON TABLE platform.dws_modeling_jobs IS
  '数据集中心主题建模任务；LLM 只生成可评审 DWS DAG 草稿，不自动发布或物化';
COMMENT ON TABLE platform.dws_modeling_outputs IS
  'DWD 到系统生成 DWS 草稿的所有权映射；人工修改后不被自动覆盖';
