-- FUSE-005: immutable compiler artifacts produced by an explicitly confirmed
-- report semantic upgrade. Preview never writes this table. Runtime access is
-- possible only when an immutable READY report version references the exact
-- compilation ID and query-plan hash.
CREATE TABLE platform.report_semantic_compilations(
  id uuid PRIMARY KEY,
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id) ON DELETE RESTRICT,
  domain_id uuid NOT NULL,
  report_id uuid NOT NULL,
  component_id text NOT NULL CHECK(
    length(component_id) BETWEEN 1 AND 128
    AND component_id ~ '^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$'
  ),
  semantic_release_id uuid NOT NULL,
  semantic_content_hash text NOT NULL CHECK(semantic_content_hash ~ '^[0-9a-f]{64}$'),
  semantic_ir_hash text NOT NULL CHECK(semantic_ir_hash ~ '^[0-9a-f]{64}$'),
  semantic_ir_json jsonb NOT NULL CHECK(jsonb_typeof(semantic_ir_json)='object'),
  query_plan_hash text NOT NULL CHECK(query_plan_hash ~ '^[0-9a-f]{64}$'),
  artifact_json jsonb NOT NULL CHECK(jsonb_typeof(artifact_json)='object'),
  created_by uuid NOT NULL,
  created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
  CONSTRAINT report_semantic_compilations_identity_tenant_key UNIQUE(id,tenant_id),
  CONSTRAINT report_semantic_compilations_report_fk FOREIGN KEY(report_id,tenant_id)
    REFERENCES platform.reports(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT report_semantic_compilations_domain_fk FOREIGN KEY(domain_id,tenant_id)
    REFERENCES platform.business_domains(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT report_semantic_compilations_actor_fk FOREIGN KEY(created_by,tenant_id)
    REFERENCES platform.users(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT report_semantic_compilations_release_fk
    FOREIGN KEY(semantic_release_id,domain_id,tenant_id)
    REFERENCES askdata.releases(id,domain_id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT report_semantic_compilations_exact_key UNIQUE(
    tenant_id,report_id,component_id,semantic_ir_hash,query_plan_hash
  )
);

CREATE INDEX report_semantic_compilations_runtime_idx
  ON platform.report_semantic_compilations(tenant_id,report_id,id,query_plan_hash);

ALTER TABLE platform.report_semantic_compilations ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform.report_semantic_compilations FORCE ROW LEVEL SECURITY;
CREATE POLICY report_semantic_compilations_read
  ON platform.report_semantic_compilations FOR SELECT
  USING(
    tenant_id=platform.current_tenant_id()
    AND domain_id=platform.current_domain_id()
    AND platform.report_v2_can_access(report_id,ARRAY['VIEW','EDIT','PUBLISH']::text[])
  );
CREATE POLICY report_semantic_compilations_insert
  ON platform.report_semantic_compilations FOR INSERT
  WITH CHECK(
    tenant_id=platform.current_tenant_id()
    AND domain_id=platform.current_domain_id()
    AND created_by=platform.current_user_id()
    AND platform.report_v2_can_access(report_id,ARRAY['EDIT','PUBLISH']::text[])
  );

CREATE TRIGGER report_semantic_compilations_immutable
BEFORE UPDATE OR DELETE ON platform.report_semantic_compilations
FOR EACH ROW EXECUTE FUNCTION platform.reject_report_v2_immutable_mutation();

CREATE OR REPLACE FUNCTION platform.load_report_runtime_compilation_artifact(
  selected_report_version_id uuid,
  selected_compilation_id uuid,
  expected_query_plan_hash text
)
RETURNS jsonb
LANGUAGE plpgsql
STABLE
SECURITY DEFINER
SET search_path=pg_catalog,platform
AS $$
DECLARE
  selected_tenant_id uuid := platform.current_tenant_id();
  selected_user_id uuid := platform.current_user_id();
  selected_domain_id uuid := platform.current_domain_id();
  selected_payload jsonb;
BEGIN
  IF selected_tenant_id IS NULL OR selected_user_id IS NULL OR selected_domain_id IS NULL
    OR selected_report_version_id IS NULL OR selected_compilation_id IS NULL
    OR expected_query_plan_hash !~ '^[0-9a-f]{64}$' THEN
    RAISE EXCEPTION 'report runtime compilation request is invalid' USING ERRCODE='22023';
  END IF;

  SELECT DISTINCT compilation.artifact_json INTO STRICT selected_payload
  FROM platform.report_versions AS version
  JOIN platform.reports AS report
    ON report.id=version.report_id AND report.tenant_id=version.tenant_id
  CROSS JOIN LATERAL jsonb_array_elements(version.definition_json->'components') AS component(value)
  JOIN platform.report_semantic_compilations AS compilation
    ON compilation.id=selected_compilation_id
   AND compilation.tenant_id=version.tenant_id
   AND compilation.domain_id=selected_domain_id
   AND compilation.report_id=version.report_id
   AND compilation.component_id=component.value->>'id'
   AND compilation.semantic_release_id=(component.value#>>'{dataBinding,semanticQueryRef,semanticReleaseId}')::uuid
   AND compilation.semantic_content_hash=component.value#>>'{dataBinding,semanticQueryRef,semanticContentHash}'
   AND compilation.semantic_ir_json=component.value#>'{dataBinding,semanticQueryRef,semanticIr}'
   AND compilation.query_plan_hash=expected_query_plan_hash
   AND compilation.artifact_json->>'planHash'=expected_query_plan_hash
  WHERE version.id=selected_report_version_id
    AND version.tenant_id=selected_tenant_id
    AND version.artifact_state='READY'
    AND report.status='ACTIVE'
    AND report.domain_id=selected_domain_id
    AND platform.report_v2_can_access(report.id,ARRAY['VIEW','EDIT','PUBLISH']::text[])
    AND component.value#>>'{dataBinding,bindingMode}'='SEMANTIC_IR'
    AND component.value#>>'{dataBinding,semanticQueryRef,compilationArtifactId}'=selected_compilation_id::text
    AND component.value#>>'{dataBinding,semanticQueryRef,queryPlanHash}'=expected_query_plan_hash
    AND component.value#>>'{dataBinding,semanticQueryRef,semanticIr,domainId}'=selected_domain_id::text;

  IF jsonb_typeof(selected_payload)<>'object' THEN
    RAISE EXCEPTION 'report runtime compilation artifact is invalid' USING ERRCODE='22023';
  END IF;
  RETURN selected_payload;
EXCEPTION
  WHEN no_data_found OR too_many_rows THEN
    RAISE EXCEPTION 'report runtime compilation artifact is unavailable' USING ERRCODE='42501';
END
$$;

REVOKE ALL ON TABLE platform.report_semantic_compilations FROM PUBLIC;
REVOKE ALL ON FUNCTION platform.load_report_runtime_compilation_artifact(uuid,uuid,text) FROM PUBLIC;
