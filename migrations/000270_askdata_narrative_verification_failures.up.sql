CREATE TABLE askdata.narrative_verification_failures(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id) ON DELETE RESTRICT,
  domain_id uuid NOT NULL,
  actor_id uuid NOT NULL,
  run_id uuid NOT NULL,
  run_type text NOT NULL CHECK(run_type IN ('ASKDATA','REPORT')),
  attempt smallint NOT NULL CHECK(attempt IN (1,2)),
  failure_code text NOT NULL CHECK(failure_code ~ '^[A-Z][A-Z0-9_]{0,127}$'),
  failure_span_start integer NOT NULL CHECK(failure_span_start>=0),
  failure_span_end integer NOT NULL CHECK(failure_span_end>=failure_span_start),
  rejected_text_hash text NOT NULL CHECK(rejected_text_hash ~ '^[0-9a-f]{64}$'),
  metric_version_ids text[] NOT NULL DEFAULT '{}'::text[] CHECK(
    cardinality(metric_version_ids)<=64
    AND array_position(metric_version_ids,NULL) IS NULL
  ),
  dimension_version_ids text[] NOT NULL DEFAULT '{}'::text[] CHECK(
    cardinality(dimension_version_ids)<=64
    AND array_position(dimension_version_ids,NULL) IS NULL
  ),
  created_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT askdata_narrative_failures_identity_tenant_key UNIQUE(id,tenant_id),
  CONSTRAINT askdata_narrative_failures_sample_key UNIQUE(
    tenant_id,run_id,run_type,attempt,failure_code,
    failure_span_start,failure_span_end,rejected_text_hash
  ),
  CONSTRAINT askdata_narrative_failures_domain_fk
    FOREIGN KEY(domain_id,tenant_id)
    REFERENCES askdata.domains(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT askdata_narrative_failures_actor_fk
    FOREIGN KEY(actor_id,tenant_id)
    REFERENCES platform.users(id,tenant_id) ON DELETE RESTRICT
);

CREATE INDEX askdata_narrative_failures_cluster_idx
  ON askdata.narrative_verification_failures(
    tenant_id,domain_id,run_type,failure_code,created_at DESC,id
  );

CREATE OR REPLACE FUNCTION askdata.validate_narrative_failure_sample()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,askdata
AS $$
DECLARE selected_value text;
BEGIN
  IF NEW.run_type='ASKDATA' AND NOT EXISTS(
    SELECT 1 FROM askdata.question_runs AS run
    WHERE run.tenant_id=NEW.tenant_id AND run.domain_id=NEW.domain_id
      AND run.actor_id=NEW.actor_id AND run.id=NEW.run_id
  ) THEN
    RAISE EXCEPTION 'narrative failure sample run identity is invalid'
      USING ERRCODE='23514';
  END IF;
  FOREACH selected_value IN ARRAY NEW.metric_version_ids||NEW.dimension_version_ids LOOP
    IF length(selected_value) NOT BETWEEN 1 AND 256
      OR selected_value ~ '[[:cntrl:]]' THEN
      RAISE EXCEPTION 'narrative failure semantic ID is invalid'
        USING ERRCODE='22023';
    END IF;
  END LOOP;
  NEW.metric_version_ids=ARRAY(
    SELECT DISTINCT value FROM unnest(NEW.metric_version_ids) AS value ORDER BY value
  );
  NEW.dimension_version_ids=ARRAY(
    SELECT DISTINCT value FROM unnest(NEW.dimension_version_ids) AS value ORDER BY value
  );
  NEW.created_at=clock_timestamp();
  RETURN NEW;
END
$$;

CREATE TRIGGER askdata_narrative_failures_validate
BEFORE INSERT ON askdata.narrative_verification_failures
FOR EACH ROW EXECUTE FUNCTION askdata.validate_narrative_failure_sample();
CREATE TRIGGER askdata_narrative_failures_immutable
BEFORE UPDATE OR DELETE ON askdata.narrative_verification_failures
FOR EACH ROW EXECUTE FUNCTION askdata.reject_immutable_mutation();

ALTER TABLE askdata.narrative_verification_failures ENABLE ROW LEVEL SECURITY;
ALTER TABLE askdata.narrative_verification_failures FORCE ROW LEVEL SECURITY;
CREATE POLICY askdata_narrative_failures_actor_domain_isolation
  ON askdata.narrative_verification_failures
  USING(askdata.question_runtime_can_access(tenant_id,domain_id,actor_id))
  WITH CHECK(askdata.question_runtime_can_access(tenant_id,domain_id,actor_id));

REVOKE ALL ON FUNCTION askdata.validate_narrative_failure_sample() FROM PUBLIC;
REVOKE ALL ON TABLE askdata.narrative_verification_failures FROM PUBLIC;

COMMENT ON TABLE askdata.narrative_verification_failures IS
  'Append-only rejected narrative evidence: hashes, spans, stable codes and semantic IDs only; prose is prohibited by schema';
