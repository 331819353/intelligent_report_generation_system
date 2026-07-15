-- 持久化 AI 元数据补全任务、结构化建议及人工决策状态。
CREATE TABLE platform.ai_metadata_jobs(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id),
  table_id uuid NOT NULL,
  purpose text NOT NULL DEFAULT 'METADATA_COMPLETION' CHECK(purpose='METADATA_COMPLETION'),
  provider text NOT NULL CHECK(btrim(provider)<>''),
  model_name text NOT NULL CHECK(btrim(model_name)<>''),
  model_version text NOT NULL DEFAULT '',
  prompt_version text NOT NULL CHECK(btrim(prompt_version)<>''),
  input_hash text NOT NULL CHECK(length(input_hash)=64),
  parsed_result jsonb,
  status text NOT NULL CHECK(status IN ('RUNNING','SUCCEEDED','FAILED')),
  error_code text NOT NULL DEFAULT '',
  prompt_tokens integer NOT NULL DEFAULT 0 CHECK(prompt_tokens>=0),
  completion_tokens integer NOT NULL DEFAULT 0 CHECK(completion_tokens>=0),
  total_tokens integer NOT NULL DEFAULT 0 CHECK(total_tokens>=0),
  latency_ms bigint NOT NULL DEFAULT 0 CHECK(latency_ms>=0),
  created_by uuid,
  created_at timestamptz NOT NULL DEFAULT now(),
  completed_at timestamptz,
  FOREIGN KEY(table_id,tenant_id) REFERENCES platform.metadata_tables(id,tenant_id),
  FOREIGN KEY(created_by,tenant_id) REFERENCES platform.users(id,tenant_id) ON DELETE SET NULL (created_by),
  UNIQUE(id,tenant_id)
);

CREATE TABLE platform.ai_metadata_suggestions(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id),
  job_id uuid NOT NULL,
  target_type text NOT NULL CHECK(target_type IN ('TABLE','COLUMN')),
  target_id uuid NOT NULL,
  proposed_value jsonb NOT NULL,
  confidence numeric(5,4) NOT NULL CHECK(confidence>=0 AND confidence<=1),
  expected_business_version bigint NOT NULL CHECK(expected_business_version>0),
  status text NOT NULL CHECK(status IN ('PENDING','APPLIED','ACCEPTED','REJECTED')),
  pending_reason text NOT NULL DEFAULT '' CHECK(pending_reason IN ('','LOW_CONFIDENCE','MANUAL_LOCKED','VERSION_CHANGED')),
  decided_by uuid,
  created_at timestamptz NOT NULL DEFAULT now(),
  decided_at timestamptz,
  FOREIGN KEY(job_id,tenant_id) REFERENCES platform.ai_metadata_jobs(id,tenant_id),
  FOREIGN KEY(decided_by,tenant_id) REFERENCES platform.users(id,tenant_id) ON DELETE SET NULL (decided_by),
  UNIQUE(tenant_id,job_id,target_type,target_id)
);

CREATE INDEX ai_metadata_jobs_table_time_idx ON platform.ai_metadata_jobs(tenant_id,table_id,created_at DESC);
CREATE INDEX ai_metadata_jobs_status_idx ON platform.ai_metadata_jobs(tenant_id,status,created_at DESC);
CREATE INDEX ai_metadata_suggestions_status_idx ON platform.ai_metadata_suggestions(tenant_id,status,created_at DESC);
CREATE INDEX ai_metadata_suggestions_target_idx ON platform.ai_metadata_suggestions(tenant_id,target_type,target_id,created_at DESC);

ALTER TABLE platform.ai_metadata_jobs ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform.ai_metadata_jobs FORCE ROW LEVEL SECURITY;
ALTER TABLE platform.ai_metadata_suggestions ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform.ai_metadata_suggestions FORCE ROW LEVEL SECURITY;
CREATE POLICY ai_metadata_jobs_tenant_isolation ON platform.ai_metadata_jobs
  USING(tenant_id=platform.current_tenant_id()) WITH CHECK(tenant_id=platform.current_tenant_id());
CREATE POLICY ai_metadata_suggestions_tenant_isolation ON platform.ai_metadata_suggestions
  USING(tenant_id=platform.current_tenant_id()) WITH CHECK(tenant_id=platform.current_tenant_id());

COMMENT ON TABLE platform.ai_metadata_jobs IS 'Audited AI metadata calls; credentials and raw prompts are never stored';
COMMENT ON TABLE platform.ai_metadata_suggestions IS 'Validated AI metadata proposals and their review/application state';
