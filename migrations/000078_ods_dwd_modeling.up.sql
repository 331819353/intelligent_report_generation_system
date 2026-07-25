-- ODS 发布五分钟后启动同领域 DWD 建模。任务只生成/更新可审阅的 DWD 草稿；
-- 发布、物化和标签建议继续走现有审批、build outbox 与 LLM 标签治理链路。
CREATE TABLE platform.dwd_modeling_jobs(
  id uuid PRIMARY KEY DEFAULT public.gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id),
  trigger_dataset_id uuid NOT NULL,
  trigger_dataset_version_id uuid NOT NULL,
  requested_by uuid,
  prompt_version text NOT NULL DEFAULT 'dwd-modeling-v1' CHECK(
    length(prompt_version) BETWEEN 1 AND 128
    AND prompt_version=btrim(prompt_version)
    AND prompt_version !~ '[[:cntrl:]]'
  ),
  ai_request_id uuid,
  status text NOT NULL DEFAULT 'PENDING'
    CHECK(status IN ('PENDING','RUNNING','SUCCEEDED','PARTIAL','FAILED','SKIPPED')),
  not_before timestamptz NOT NULL,
  attempt integer NOT NULL DEFAULT 0 CHECK(attempt>=0),
  max_attempts integer NOT NULL DEFAULT 3 CHECK(max_attempts BETWEEN 1 AND 5),
  next_attempt_at timestamptz NOT NULL,
  lease_owner text NOT NULL DEFAULT '' CHECK(
    length(lease_owner)<=128
    AND lease_owner=btrim(lease_owner)
    AND lease_owner !~ '[[:cntrl:]]'
  ),
  lease_token uuid,
  lease_expires_at timestamptz,
  domain_key text NOT NULL DEFAULT '' CHECK(
    length(domain_key)<=256
    AND domain_key !~ '[[:cntrl:]]'
  ),
  trigger_role text NOT NULL DEFAULT ''
    CHECK(trigger_role IN ('','FACT','DIMENSION','MASTER')),
  generated_count integer NOT NULL DEFAULT 0 CHECK(generated_count>=0),
  updated_count integer NOT NULL DEFAULT 0 CHECK(updated_count>=0),
  skipped_count integer NOT NULL DEFAULT 0 CHECK(skipped_count>=0),
  result_json jsonb NOT NULL DEFAULT '{}'::jsonb CHECK(
    jsonb_typeof(result_json)='object'
    AND pg_column_size(result_json)<=262144
    AND platform.materialization_json_is_safe(result_json)
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
  CONSTRAINT dwd_modeling_jobs_trigger_version_fk
    FOREIGN KEY(trigger_dataset_version_id,trigger_dataset_id,tenant_id)
    REFERENCES platform.dataset_versions(id,dataset_id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT dwd_modeling_jobs_actor_fk
    FOREIGN KEY(requested_by,tenant_id)
    REFERENCES platform.users(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT dwd_modeling_jobs_ai_request_fk
    FOREIGN KEY(ai_request_id,tenant_id)
    REFERENCES platform.ai_requests(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT dwd_modeling_jobs_attempt_budget_check CHECK(attempt<=max_attempts),
  CONSTRAINT dwd_modeling_jobs_version_key
    UNIQUE(tenant_id,trigger_dataset_version_id),
  CONSTRAINT dwd_modeling_jobs_identity_tenant_key UNIQUE(id,tenant_id)
);

-- 该映射是“哪些 DWD 草稿允许后台继续增量更新”的所有权边界。若当前草稿
-- hash 已偏离 last_generated_schema_hash，worker 不覆盖人工修改，只记录跳过。
CREATE TABLE platform.dwd_modeling_outputs(
  id uuid PRIMARY KEY DEFAULT public.gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id),
  fact_dataset_id uuid NOT NULL,
  dwd_dataset_id uuid NOT NULL,
  domain_key text NOT NULL CHECK(
    length(domain_key) BETWEEN 1 AND 256
    AND domain_key=btrim(domain_key)
    AND domain_key !~ '[[:cntrl:]]'
  ),
  last_job_id uuid NOT NULL,
  last_input_hash text NOT NULL CHECK(last_input_hash ~ '^[0-9a-f]{64}$'),
  last_generated_schema_hash text NOT NULL
    CHECK(last_generated_schema_hash ~ '^[0-9a-f]{64}$'),
  last_action text NOT NULL CHECK(last_action IN ('CREATED','UPDATED','UNCHANGED')),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT dwd_modeling_outputs_fact_fk
    FOREIGN KEY(fact_dataset_id,tenant_id)
    REFERENCES platform.datasets(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT dwd_modeling_outputs_dwd_fk
    FOREIGN KEY(dwd_dataset_id,tenant_id)
    REFERENCES platform.datasets(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT dwd_modeling_outputs_job_fk
    FOREIGN KEY(last_job_id,tenant_id)
    REFERENCES platform.dwd_modeling_jobs(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT dwd_modeling_outputs_fact_key UNIQUE(tenant_id,fact_dataset_id),
  CONSTRAINT dwd_modeling_outputs_dwd_key UNIQUE(tenant_id,dwd_dataset_id),
  CONSTRAINT dwd_modeling_outputs_identity_tenant_key UNIQUE(id,tenant_id)
);

CREATE INDEX dwd_modeling_jobs_claim_idx
  ON platform.dwd_modeling_jobs(
    tenant_id,status,next_attempt_at,lease_expires_at,created_at,id
  )
  WHERE status IN ('PENDING','RUNNING');

CREATE INDEX dwd_modeling_outputs_domain_idx
  ON platform.dwd_modeling_outputs(tenant_id,domain_key,fact_dataset_id);

CREATE OR REPLACE FUNCTION platform.enqueue_ods_dwd_modeling()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,platform
AS $$
DECLARE
  scheduled_at timestamptz;
BEGIN
  IF NEW.layer='ODS'
     AND NEW.status='PUBLISHED'
     AND OLD.status IS DISTINCT FROM 'PUBLISHED' THEN
    scheduled_at := COALESCE(NEW.published_at,now())+interval '5 minutes';
    INSERT INTO platform.dwd_modeling_jobs(
      tenant_id,trigger_dataset_id,trigger_dataset_version_id,requested_by,
      not_before,next_attempt_at
    ) VALUES(
      NEW.tenant_id,NEW.dataset_id,NEW.id,NEW.published_by,
      scheduled_at,scheduled_at
    )
    ON CONFLICT(tenant_id,trigger_dataset_version_id) DO NOTHING;
  END IF;
  RETURN NEW;
END
$$;

REVOKE ALL ON FUNCTION platform.enqueue_ods_dwd_modeling() FROM PUBLIC;

CREATE TRIGGER dataset_versions_enqueue_dwd_modeling
AFTER UPDATE OF status ON platform.dataset_versions
FOR EACH ROW EXECUTE FUNCTION platform.enqueue_ods_dwd_modeling();

-- 对当前已发布 ODS 做安全回填；已发布超过五分钟的版本可以立即进入建模。
INSERT INTO platform.dwd_modeling_jobs(
  tenant_id,trigger_dataset_id,trigger_dataset_version_id,requested_by,
  not_before,next_attempt_at
)
SELECT version.tenant_id,version.dataset_id,version.id,version.published_by,
  version.published_at+interval '5 minutes',
  greatest(version.published_at+interval '5 minutes',now())
FROM platform.dataset_versions AS version
JOIN platform.datasets AS dataset
  ON dataset.id=version.dataset_id
 AND dataset.tenant_id=version.tenant_id
 AND dataset.current_published_version_id=version.id
WHERE version.layer='ODS'
  AND version.status='PUBLISHED'
  AND version.published_at IS NOT NULL
  AND dataset.deleted_at IS NULL
ON CONFLICT(tenant_id,trigger_dataset_version_id) DO NOTHING;

-- 应用层已经在删除前做占用检查；数据库触发器补上并发窗口，确保任何入口都
-- 不能软删除仍被未删除 DWD 数据集的草稿、发布或历史版本引用的 ODS。
CREATE OR REPLACE FUNCTION platform.prevent_referenced_ods_soft_delete()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,platform
AS $$
BEGIN
  IF OLD.layer='ODS'
     AND OLD.deleted_at IS NULL
     AND NEW.deleted_at IS NOT NULL
     AND EXISTS(
       SELECT 1
       FROM platform.dataset_versions AS source_version
       JOIN platform.dataset_dependencies AS dependency
         ON dependency.tenant_id=source_version.tenant_id
        AND dependency.source_type='DATASET_VERSION'
        AND dependency.source_id=source_version.id::text
       JOIN platform.dataset_versions AS downstream_version
         ON downstream_version.id=dependency.dataset_version_id
        AND downstream_version.tenant_id=dependency.tenant_id
        AND downstream_version.layer='DWD'
       JOIN platform.datasets AS downstream_dataset
         ON downstream_dataset.id=downstream_version.dataset_id
        AND downstream_dataset.tenant_id=downstream_version.tenant_id
        AND downstream_dataset.deleted_at IS NULL
       WHERE source_version.tenant_id=OLD.tenant_id
         AND source_version.dataset_id=OLD.id
     ) THEN
    RAISE EXCEPTION 'ODS 数据集仍被 DWD 数据集引用'
      USING ERRCODE='23503',
        CONSTRAINT='datasets_ods_dwd_reference_guard';
  END IF;
  RETURN NEW;
END
$$;

REVOKE ALL ON FUNCTION platform.prevent_referenced_ods_soft_delete() FROM PUBLIC;

CREATE TRIGGER datasets_prevent_referenced_ods_soft_delete
BEFORE UPDATE OF deleted_at ON platform.datasets
FOR EACH ROW EXECUTE FUNCTION platform.prevent_referenced_ods_soft_delete();

CREATE TRIGGER dwd_modeling_jobs_set_updated_at
BEFORE UPDATE ON platform.dwd_modeling_jobs
FOR EACH ROW EXECUTE FUNCTION platform.set_updated_at();

CREATE TRIGGER dwd_modeling_outputs_set_updated_at
BEFORE UPDATE ON platform.dwd_modeling_outputs
FOR EACH ROW EXECUTE FUNCTION platform.set_updated_at();

ALTER TABLE platform.dwd_modeling_jobs ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform.dwd_modeling_jobs FORCE ROW LEVEL SECURITY;
ALTER TABLE platform.dwd_modeling_outputs ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform.dwd_modeling_outputs FORCE ROW LEVEL SECURITY;

CREATE POLICY dwd_modeling_jobs_tenant_isolation
  ON platform.dwd_modeling_jobs
  USING(tenant_id=platform.current_tenant_id())
  WITH CHECK(tenant_id=platform.current_tenant_id());

CREATE POLICY dwd_modeling_outputs_tenant_isolation
  ON platform.dwd_modeling_outputs
  USING(tenant_id=platform.current_tenant_id())
  WITH CHECK(tenant_id=platform.current_tenant_id());

COMMENT ON TABLE platform.dwd_modeling_jobs IS
  'ODS 发布五分钟后执行的同领域 DWD 自动建模 outbox；带租约、幂等和有界重试';
COMMENT ON TABLE platform.dwd_modeling_outputs IS
  '事实 ODS 与后台生成 DWD 草稿的稳定映射及人工修改保护 fence';
COMMENT ON COLUMN platform.dwd_modeling_outputs.last_generated_schema_hash IS
  '只有当前 DWD 草稿仍等于该 hash 时，后台才允许因新维度发布而增量扩展';
