-- RPT-006/RPT-007: a report viewer may rehydrate only the exact non-executable
-- AskData query artifact referenced by an immutable report version. Formal
-- report executions keep summary-only audit facts; SQL, parameters, EXPLAIN
-- JSON and result rows are never persisted here.
CREATE OR REPLACE FUNCTION platform.load_report_runtime_query_artifact(
  selected_report_version_id uuid,
  selected_question_run_id uuid,
  expected_query_plan_hash text
)
RETURNS jsonb
LANGUAGE plpgsql
STABLE
SECURITY DEFINER
SET search_path=pg_catalog,platform,askdata
AS $$
DECLARE
  selected_tenant_id uuid := platform.current_tenant_id();
  selected_user_id uuid := platform.current_user_id();
  selected_domain_id uuid := platform.current_domain_id();
  selected_payload jsonb;
BEGIN
  IF selected_tenant_id IS NULL OR selected_user_id IS NULL OR selected_domain_id IS NULL
    OR selected_report_version_id IS NULL OR selected_question_run_id IS NULL
    OR expected_query_plan_hash !~ '^[0-9a-f]{64}$' THEN
    RAISE EXCEPTION 'report runtime artifact request is invalid' USING ERRCODE='22023';
  END IF;

  SELECT DISTINCT artifact.payload_json INTO STRICT selected_payload
  FROM platform.report_versions AS version
  JOIN platform.reports AS report
    ON report.id=version.report_id AND report.tenant_id=version.tenant_id
  CROSS JOIN LATERAL jsonb_array_elements(version.definition_json->'components') AS component(value)
  JOIN askdata.question_runs AS run
    ON run.id=selected_question_run_id
   AND run.tenant_id=version.tenant_id
   AND run.domain_id=selected_domain_id
   AND run.current_state='ANSWERED'
   AND run.query_plan_hash=expected_query_plan_hash
   AND run.release_id=(component.value#>>'{dataBinding,semanticQueryRef,semanticReleaseId}')::uuid
   AND run.release_content_hash=component.value#>>'{dataBinding,semanticQueryRef,semanticContentHash}'
  JOIN askdata.question_artifacts AS artifact
    ON artifact.tenant_id=run.tenant_id
   AND artifact.domain_id=run.domain_id
   AND artifact.question_run_id=run.id
   AND artifact.artifact_type='QUERY_PLAN'
   AND artifact.release_id=run.release_id
   AND artifact.release_content_hash=run.release_content_hash
   AND artifact.payload_json->>'planHash'=expected_query_plan_hash
  WHERE version.id=selected_report_version_id
    AND version.tenant_id=selected_tenant_id
    AND version.artifact_state='READY'
    AND report.status='ACTIVE'
    AND report.domain_id=selected_domain_id
    AND platform.report_v2_can_access(report.id,ARRAY['VIEW','EDIT','PUBLISH']::text[])
    AND component.value#>>'{dataBinding,bindingMode}'='SEMANTIC_IR'
    AND component.value#>>'{dataBinding,semanticQueryRef,sourceQuestionRunId}'=selected_question_run_id::text
    AND component.value#>>'{dataBinding,semanticQueryRef,queryPlanHash}'=expected_query_plan_hash
    AND component.value#>>'{dataBinding,semanticQueryRef,semanticIr,domainId}'=selected_domain_id::text;

  IF jsonb_typeof(selected_payload)<>'object' THEN
    RAISE EXCEPTION 'report runtime query artifact is invalid' USING ERRCODE='22023';
  END IF;
  RETURN selected_payload;
EXCEPTION
  WHEN no_data_found OR too_many_rows THEN
    RAISE EXCEPTION 'report runtime query artifact is unavailable' USING ERRCODE='42501';
END
$$;

REVOKE ALL ON FUNCTION platform.load_report_runtime_query_artifact(uuid,uuid,text) FROM PUBLIC;

CREATE TABLE platform.semantic_query_execution_runs(
  id uuid PRIMARY KEY,
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id) ON DELETE RESTRICT,
  domain_id uuid NOT NULL,
  actor_id uuid NOT NULL,
  run_type text NOT NULL CHECK(run_type='SEMANTIC_QUESTION'),
  query_plan_hash text NOT NULL CHECK(query_plan_hash ~ '^[0-9a-f]{64}$'),
  validation_hash text NOT NULL CHECK(validation_hash ~ '^[0-9a-f]{64}$'),
  plan_count integer NOT NULL CHECK(plan_count BETWEEN 1 AND 2),
  max_rows integer NOT NULL CHECK(max_rows BETWEEN 1 AND 10000),
  timeout_ms integer NOT NULL CHECK(timeout_ms BETWEEN 100 AND 25000),
  max_explain_cost double precision NOT NULL CHECK(
    max_explain_cost>=0 AND max_explain_cost<'Infinity'::double precision
  ),
  status text NOT NULL DEFAULT 'RUNNING' CHECK(status IN (
    'RUNNING','SUCCEEDED','FAILED','TIMEOUT','CANCELED'
  )),
  result_hash text NOT NULL DEFAULT '' CHECK(result_hash='' OR result_hash ~ '^[0-9a-f]{64}$'),
  row_count integer NOT NULL DEFAULT 0 CHECK(row_count BETWEEN 0 AND 20000),
  duration_ms bigint NOT NULL DEFAULT 0 CHECK(duration_ms BETWEEN 0 AND 600000),
  error_code text NOT NULL DEFAULT '' CHECK(
    error_code='' OR error_code ~ '^[A-Z][A-Z0-9_]{0,127}$'
  ),
  started_at timestamptz NOT NULL DEFAULT clock_timestamp(),
  completed_at timestamptz,
  UNIQUE(id,tenant_id),
  FOREIGN KEY(domain_id,tenant_id)
    REFERENCES platform.business_domains(id,tenant_id) ON DELETE RESTRICT,
  FOREIGN KEY(actor_id,tenant_id)
    REFERENCES platform.users(id,tenant_id) ON DELETE RESTRICT,
  CHECK(
    (status='RUNNING' AND result_hash='' AND row_count=0 AND duration_ms=0
      AND error_code='' AND completed_at IS NULL)
    OR (status='SUCCEEDED' AND result_hash<>'' AND error_code=''
      AND completed_at IS NOT NULL)
    OR (status IN ('FAILED','TIMEOUT','CANCELED') AND result_hash=''
      AND row_count=0 AND error_code<>'' AND completed_at IS NOT NULL)
  )
);

CREATE INDEX semantic_query_execution_runs_actor_idx
  ON platform.semantic_query_execution_runs(
    tenant_id,domain_id,actor_id,started_at DESC,id
  );

ALTER TABLE platform.semantic_query_execution_runs ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform.semantic_query_execution_runs FORCE ROW LEVEL SECURITY;
CREATE POLICY semantic_query_execution_runs_actor_policy
  ON platform.semantic_query_execution_runs
  USING(
    tenant_id=platform.current_tenant_id()
    AND domain_id=platform.current_domain_id()
    AND actor_id=platform.current_user_id()
  )
  WITH CHECK(
    tenant_id=platform.current_tenant_id()
    AND domain_id=platform.current_domain_id()
    AND actor_id=platform.current_user_id()
  );

CREATE OR REPLACE FUNCTION platform.guard_semantic_query_execution_run()
RETURNS trigger
LANGUAGE plpgsql
SET search_path=pg_catalog,platform
AS $$
BEGIN
  IF TG_OP='DELETE' THEN
    RAISE EXCEPTION 'semantic query execution audit is immutable' USING ERRCODE='55000';
  END IF;
  IF ROW(OLD.id,OLD.tenant_id,OLD.domain_id,OLD.actor_id,OLD.run_type,
         OLD.query_plan_hash,OLD.validation_hash,OLD.plan_count,OLD.max_rows,
         OLD.timeout_ms,OLD.max_explain_cost,OLD.started_at)
     IS DISTINCT FROM
     ROW(NEW.id,NEW.tenant_id,NEW.domain_id,NEW.actor_id,NEW.run_type,
         NEW.query_plan_hash,NEW.validation_hash,NEW.plan_count,NEW.max_rows,
         NEW.timeout_ms,NEW.max_explain_cost,NEW.started_at)
     OR OLD.status<>'RUNNING' OR NEW.status='RUNNING' THEN
    RAISE EXCEPTION 'invalid semantic query execution audit transition' USING ERRCODE='55000';
  END IF;
  NEW.completed_at=clock_timestamp();
  RETURN NEW;
END
$$;

CREATE TRIGGER semantic_query_execution_runs_guard
BEFORE UPDATE OR DELETE ON platform.semantic_query_execution_runs
FOR EACH ROW EXECUTE FUNCTION platform.guard_semantic_query_execution_run();

REVOKE ALL ON FUNCTION platform.guard_semantic_query_execution_run() FROM PUBLIC;
COMMENT ON TABLE platform.semantic_query_execution_runs IS
  'Summary-only audit for report and AskData formal semantic executions; never stores SQL, parameters or result rows.';
