CREATE TABLE askdata.quotas(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(), tenant_id uuid NOT NULL,
  scope_type text NOT NULL CHECK(scope_type IN ('TENANT','DOMAIN','USER','RUN')),
  scope_id uuid NOT NULL, period text NOT NULL CHECK(period IN ('DAY','MONTH','RUN')),
  llm_token_limit bigint CHECK(llm_token_limit IS NULL OR llm_token_limit>0),
  run_limit bigint CHECK(run_limit IS NULL OR run_limit>0),
  cost_limit_cents bigint CHECK(cost_limit_cents IS NULL OR cost_limit_cents>0),
  created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE(tenant_id,scope_type,scope_id,period),
  CHECK(llm_token_limit IS NOT NULL OR run_limit IS NOT NULL OR cost_limit_cents IS NOT NULL)
);
CREATE TABLE askdata.cost_records(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(), run_id uuid NOT NULL,
  tenant_id uuid NOT NULL, domain_id uuid NOT NULL, actor_id uuid NOT NULL,
  question_type text NOT NULL, provider text NOT NULL, model text NOT NULL,
  prompt_tokens bigint NOT NULL DEFAULT 0 CHECK(prompt_tokens>=0),
  completion_tokens bigint NOT NULL DEFAULT 0 CHECK(completion_tokens>=0),
  cost_cents bigint NOT NULL DEFAULT 0 CHECK(cost_cents>=0),
  query_scan_bytes bigint NOT NULL DEFAULT 0 CHECK(query_scan_bytes>=0),
  created_at timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY(run_id,actor_id,tenant_id) REFERENCES askdata.question_runs(id,actor_id,tenant_id) ON DELETE CASCADE
);
CREATE INDEX cost_records_tenant_time_idx ON askdata.cost_records(tenant_id,created_at);
CREATE INDEX cost_records_domain_time_idx ON askdata.cost_records(tenant_id,domain_id,created_at);
CREATE INDEX cost_records_actor_time_idx ON askdata.cost_records(tenant_id,actor_id,created_at);
CREATE INDEX cost_records_question_type_idx ON askdata.cost_records(tenant_id,question_type,created_at);
ALTER TABLE askdata.quotas ENABLE ROW LEVEL SECURITY;
ALTER TABLE askdata.quotas FORCE ROW LEVEL SECURITY;
ALTER TABLE askdata.cost_records ENABLE ROW LEVEL SECURITY;
ALTER TABLE askdata.cost_records FORCE ROW LEVEL SECURITY;
CREATE POLICY quotas_tenant ON askdata.quotas USING(tenant_id=platform.current_tenant_id()) WITH CHECK(tenant_id=platform.current_tenant_id());
CREATE POLICY cost_records_scope ON askdata.cost_records
  USING(tenant_id=platform.current_tenant_id() AND (platform.is_system_access() OR actor_id=platform.current_user_id()))
  WITH CHECK(tenant_id=platform.current_tenant_id() AND (platform.is_system_access() OR actor_id=platform.current_user_id()));
