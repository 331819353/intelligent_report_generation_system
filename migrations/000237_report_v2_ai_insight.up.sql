ALTER TABLE platform.ai_tenant_policies DROP CONSTRAINT ai_tenant_policies_purposes_check;
ALTER TABLE platform.ai_tenant_policies ADD CONSTRAINT ai_tenant_policies_purposes_check CHECK(
  cardinality(allowed_purposes) BETWEEN 1 AND 9
  AND array_position(allowed_purposes,NULL) IS NULL
  AND allowed_purposes <@ ARRAY[
    'METADATA_COMPLETION','DATASET_DAG_GENERATION','DATASET_TAG_SUGGESTION',
    'DATASET_SEMANTIC_NAMING','DATA_SOURCE_CONFIGURATION','SEMANTIC_QUESTION',
    'REPORT_GENERATION','BLOCK_EDIT','CONCLUSION_GENERATION'
  ]::text[]
);

CREATE TABLE platform.report_ai_runs(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(), tenant_id uuid NOT NULL, report_id uuid NOT NULL,
  kind text NOT NULL CHECK(kind IN ('PLAN','GENERATE_DRAFT','SCOPED_EDIT','INSIGHT')),
  actor_user_id uuid NOT NULL, prompt_version text NOT NULL, model_policy text NOT NULL,
  request_summary_json jsonb NOT NULL CHECK(jsonb_typeof(request_summary_json)='object' AND pg_column_size(request_summary_json)<=65536),
  response_summary_json jsonb CHECK(response_summary_json IS NULL OR jsonb_typeof(response_summary_json)='object'),
  base_revision_no bigint, scope_json jsonb,
  state text NOT NULL CHECK(state IN ('RUNNING','SUCCEEDED','FAILED','REJECTED')),
  error_code text, created_at timestamptz NOT NULL DEFAULT now(), finished_at timestamptz,
  UNIQUE(id,tenant_id),
  FOREIGN KEY(report_id,tenant_id) REFERENCES platform.reports(id,tenant_id) ON DELETE CASCADE,
  FOREIGN KEY(actor_user_id,tenant_id) REFERENCES platform.users(id,tenant_id) ON DELETE RESTRICT,
  CHECK((state='RUNNING' AND finished_at IS NULL) OR (state<>'RUNNING' AND finished_at IS NOT NULL))
);
CREATE TABLE platform.report_ai_operations(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(), tenant_id uuid NOT NULL, ai_run_id uuid NOT NULL,
  operation_json jsonb NOT NULL CHECK(jsonb_typeof(operation_json)='object'),
  validation_state text NOT NULL CHECK(validation_state IN ('VALID','REJECTED')),
  rejection_code text, applied_revision_no bigint, created_at timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY(ai_run_id,tenant_id) REFERENCES platform.report_ai_runs(id,tenant_id) ON DELETE CASCADE,
  CHECK((validation_state='REJECTED')=(rejection_code IS NOT NULL))
);
CREATE TABLE platform.report_evidence_artifacts(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(), tenant_id uuid NOT NULL, report_id uuid NOT NULL,
  component_id text NOT NULL, evidence_json jsonb NOT NULL CHECK(jsonb_typeof(evidence_json)='object'),
  evidence_hash text NOT NULL CHECK(evidence_hash ~ '^[0-9a-f]{64}$'),
  created_at timestamptz NOT NULL DEFAULT now(), UNIQUE(id,tenant_id),
  FOREIGN KEY(report_id,tenant_id) REFERENCES platform.reports(id,tenant_id) ON DELETE CASCADE,
  UNIQUE(report_id,component_id,evidence_hash)
);
CREATE TABLE platform.report_insight_artifacts(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(), tenant_id uuid NOT NULL, report_id uuid NOT NULL,
  component_id text NOT NULL, evidence_id uuid NOT NULL, evidence_hash text NOT NULL,
  artifact_json jsonb NOT NULL CHECK(jsonb_typeof(artifact_json)='object'),
  status text NOT NULL CHECK(status IN ('CURRENT','STALE','FAILED')),
  human_edited boolean NOT NULL DEFAULT false,
  human_edited_by uuid, human_edited_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY(report_id,tenant_id) REFERENCES platform.reports(id,tenant_id) ON DELETE CASCADE,
  FOREIGN KEY(evidence_id,tenant_id) REFERENCES platform.report_evidence_artifacts(id,tenant_id) ON DELETE RESTRICT,
  FOREIGN KEY(human_edited_by,tenant_id) REFERENCES platform.users(id,tenant_id) ON DELETE RESTRICT,
  CHECK((human_edited_by IS NULL AND human_edited_at IS NULL AND NOT human_edited)
    OR (human_edited_by IS NOT NULL AND human_edited_at IS NOT NULL AND human_edited))
);
CREATE UNIQUE INDEX report_current_insight_key ON platform.report_insight_artifacts(report_id,component_id) WHERE status='CURRENT';
CREATE INDEX report_ai_runs_report_idx ON platform.report_ai_runs(tenant_id,report_id,created_at DESC);
CREATE INDEX report_insights_report_idx ON platform.report_insight_artifacts(tenant_id,report_id,component_id,created_at DESC);

CREATE OR REPLACE FUNCTION platform.guard_report_ai_summary()
RETURNS trigger LANGUAGE plpgsql SET search_path=pg_catalog,platform AS $$
DECLARE forbidden text;
BEGIN
  FOREACH forbidden IN ARRAY ARRAY['prompt','rawPrompt','sampleRows','rawData','resultRows'] LOOP
    IF NEW.request_summary_json ? forbidden THEN
      RAISE EXCEPTION 'request summary contains forbidden field %',forbidden USING ERRCODE='23514';
    END IF;
  END LOOP;
  RETURN NEW;
END
$$;
CREATE TRIGGER report_ai_summary_guard BEFORE INSERT OR UPDATE OF request_summary_json
ON platform.report_ai_runs FOR EACH ROW EXECUTE FUNCTION platform.guard_report_ai_summary();

DO $$
DECLARE table_name text;
BEGIN
  FOREACH table_name IN ARRAY ARRAY['report_ai_runs','report_ai_operations','report_evidence_artifacts','report_insight_artifacts'] LOOP
    EXECUTE format('ALTER TABLE platform.%I ENABLE ROW LEVEL SECURITY',table_name);
    EXECUTE format('ALTER TABLE platform.%I FORCE ROW LEVEL SECURITY',table_name);
    EXECUTE format('CREATE POLICY %I ON platform.%I USING(tenant_id=platform.current_tenant_id()) WITH CHECK(tenant_id=platform.current_tenant_id())',table_name||'_tenant',table_name);
  END LOOP;
END
$$;
REVOKE ALL ON FUNCTION platform.guard_report_ai_summary() FROM PUBLIC;
