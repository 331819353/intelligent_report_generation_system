-- DWD 发布后登记市场分析 DWS 草稿任务。任务按精确 DWD 版本和模板独立产生
-- 可评审草稿；不会自动发布、激活物化或创建 ADS。
CREATE TABLE platform.dws_modeling_jobs(
  id uuid PRIMARY KEY DEFAULT public.gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id),
  source_dwd_dataset_id uuid NOT NULL,
  source_dwd_version_id uuid NOT NULL,
  requested_by uuid NOT NULL,
  status text NOT NULL DEFAULT 'PENDING' CHECK(status IN (
    'PENDING','WAITING_DEPENDENCY','RUNNING','SUCCEEDED','PARTIAL',
    'FAILED','SKIPPED'
  )),
  not_before timestamptz NOT NULL DEFAULT now(),
  next_attempt_at timestamptz NOT NULL DEFAULT now(),
  attempt integer NOT NULL DEFAULT 0 CHECK(attempt BETWEEN 0 AND 8),
  max_attempts integer NOT NULL DEFAULT 5 CHECK(max_attempts BETWEEN 1 AND 8),
  lease_owner text NOT NULL DEFAULT '' CHECK(
    length(lease_owner)<=128 AND lease_owner !~ '[[:cntrl:]]'
  ),
  lease_token uuid,
  lease_expires_at timestamptz,
  input_hash text NOT NULL DEFAULT ''
    CHECK(input_hash='' OR input_hash ~ '^[0-9a-f]{64}$'),
  prompt_version text NOT NULL DEFAULT 'dws-analysis-selection-v1'
    CHECK(length(prompt_version) BETWEEN 1 AND 128),
  ai_request_id uuid,
  selection_json jsonb NOT NULL DEFAULT '[]'::jsonb CHECK(
    jsonb_typeof(selection_json)='array'
    AND pg_column_size(selection_json)<=65536
    AND platform.materialization_json_is_safe(selection_json)
  ),
  generated_count integer NOT NULL DEFAULT 0 CHECK(generated_count>=0),
  updated_count integer NOT NULL DEFAULT 0 CHECK(updated_count>=0),
  skipped_count integer NOT NULL DEFAULT 0 CHECK(skipped_count>=0),
  result_json jsonb NOT NULL DEFAULT '{}'::jsonb CHECK(
    jsonb_typeof(result_json)='object'
    AND pg_column_size(result_json)<=262144
    AND platform.materialization_json_is_safe(result_json)
  ),
  error_code text NOT NULL DEFAULT '' CHECK(length(error_code)<=128),
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
  CONSTRAINT dws_modeling_jobs_version_key
    UNIQUE(tenant_id,source_dwd_version_id),
  CONSTRAINT dws_modeling_jobs_identity_tenant_key UNIQUE(id,tenant_id),
  CONSTRAINT dws_modeling_jobs_lease_shape_check CHECK(
    (status='RUNNING' AND lease_owner<>'' AND lease_token IS NOT NULL
      AND lease_expires_at IS NOT NULL AND completed_at IS NULL)
    OR
    (status<>'RUNNING' AND lease_owner='' AND lease_token IS NULL
      AND lease_expires_at IS NULL)
  )
);

CREATE TABLE platform.dws_modeling_outputs(
  id uuid PRIMARY KEY DEFAULT public.gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id),
  source_dwd_dataset_id uuid NOT NULL,
  template_code text NOT NULL CHECK(
    template_code IN (
      'TREND','PERIOD_COMPARISON','DISTRIBUTION',
      'RANKING','DRILLDOWN','ANOMALY'
    )
  ),
  dws_dataset_id uuid NOT NULL,
  last_source_dwd_version_id uuid NOT NULL,
  last_job_id uuid NOT NULL,
  last_generated_dsl_hash text NOT NULL
    CHECK(last_generated_dsl_hash ~ '^[0-9a-f]{64}$'),
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
  CONSTRAINT dws_modeling_outputs_source_template_key
    UNIQUE(tenant_id,source_dwd_dataset_id,template_code),
  CONSTRAINT dws_modeling_outputs_dws_key UNIQUE(tenant_id,dws_dataset_id),
  CONSTRAINT dws_modeling_outputs_identity_tenant_key UNIQUE(id,tenant_id)
);

CREATE INDEX dws_modeling_jobs_claim_idx
  ON platform.dws_modeling_jobs(
    tenant_id,status,next_attempt_at,lease_expires_at,created_at,id
  )
  WHERE status IN ('PENDING','WAITING_DEPENDENCY','RUNNING');

CREATE TRIGGER dws_modeling_jobs_set_updated_at
BEFORE UPDATE ON platform.dws_modeling_jobs
FOR EACH ROW EXECUTE FUNCTION platform.set_updated_at();
CREATE TRIGGER dws_modeling_outputs_set_updated_at
BEFORE UPDATE ON platform.dws_modeling_outputs
FOR EACH ROW EXECUTE FUNCTION platform.set_updated_at();

CREATE OR REPLACE FUNCTION platform.enqueue_dws_modeling()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,platform
AS $$
BEGIN
  IF NEW.layer='DWD' AND NEW.status='PUBLISHED'
    AND (TG_OP='INSERT' OR OLD.status IS DISTINCT FROM 'PUBLISHED') THEN
    INSERT INTO platform.dws_modeling_jobs(
      tenant_id,source_dwd_dataset_id,source_dwd_version_id,requested_by,
      not_before,next_attempt_at
    ) VALUES(
      NEW.tenant_id,NEW.dataset_id,NEW.id,NEW.published_by,
      now()+interval '1 minute',now()+interval '1 minute'
    )
    ON CONFLICT(tenant_id,source_dwd_version_id) DO NOTHING;
  END IF;
  RETURN NEW;
END
$$;

REVOKE ALL ON FUNCTION platform.enqueue_dws_modeling() FROM PUBLIC;

CREATE TRIGGER dataset_versions_enqueue_dws_modeling
AFTER INSERT OR UPDATE OF status ON platform.dataset_versions
FOR EACH ROW EXECUTE FUNCTION platform.enqueue_dws_modeling();

INSERT INTO platform.dws_modeling_jobs(
  tenant_id,source_dwd_dataset_id,source_dwd_version_id,requested_by,
  not_before,next_attempt_at
)
SELECT version.tenant_id,version.dataset_id,version.id,version.published_by,
  now(),now()
FROM platform.dataset_versions AS version
JOIN platform.datasets AS dataset
  ON dataset.tenant_id=version.tenant_id
 AND dataset.id=version.dataset_id
 AND dataset.current_published_version_id=version.id
WHERE version.layer='DWD' AND version.status='PUBLISHED'
  AND dataset.status='PUBLISHED' AND dataset.deleted_at IS NULL
ON CONFLICT(tenant_id,source_dwd_version_id) DO NOTHING;

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
  'DWD 发布后可恢复的市场分析模板选择与 DWS 草稿任务；依赖等待不消耗失败预算';
COMMENT ON TABLE platform.dws_modeling_outputs IS
  '自动 DWS 草稿的人工修改保护边界；只有仍等于上次生成 hash 的草稿可更新';
