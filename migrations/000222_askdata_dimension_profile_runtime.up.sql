-- Bounded AskData dimension profiling runtime.
--
-- The worker records warehouse observations separately from governed
-- dimension_members. A scan result is evidence for later review; it never
-- becomes a certified semantic member merely because a background job ran.

CREATE TABLE askdata.dimension_profile_jobs(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id) ON DELETE RESTRICT,
  domain_id uuid NOT NULL,
  dimension_version_id uuid NOT NULL,
  generation bigint NOT NULL CHECK(generation>0),
  semantic_model_version_id uuid NOT NULL,
  dataset_id uuid NOT NULL,
  dataset_version_id uuid NOT NULL,
  materialization_id uuid NOT NULL,
  source_snapshot_hash text NOT NULL CHECK(source_snapshot_hash ~ '^[0-9a-f]{64}$'),
  dataset_schema_hash text NOT NULL CHECK(dataset_schema_hash ~ '^[0-9a-f]{64}$'),
  published_schema text NOT NULL CHECK(published_schema='warehouse_published'),
  published_name text NOT NULL CHECK(published_name ~ '^[a-z][a-z0-9_]{0,62}$'),
  field_code text NOT NULL CHECK(
    length(field_code) BETWEEN 1 AND 128
    AND field_code=btrim(field_code)
    AND field_code !~ '[[:cntrl:]]'
  ),
  expected_row_count bigint NOT NULL CHECK(expected_row_count>=0),
  sensitivity text NOT NULL CHECK(sensitivity IN ('PUBLIC','INTERNAL','CONFIDENTIAL','RESTRICTED')),
  member_index_policy text NOT NULL CHECK(member_index_policy IN ('FULL','EXACT_ONLY','ON_DEMAND','NONE')),
  high_cardinality_hint boolean NOT NULL DEFAULT false,
  max_rows bigint NOT NULL CHECK(max_rows BETWEEN 1 AND 100000000),
  max_distinct_values bigint NOT NULL CHECK(max_distinct_values BETWEEN 1 AND 1000000),
  max_sample_bytes bigint NOT NULL CHECK(max_sample_bytes BETWEEN 1024 AND 268435456),
  statement_timeout_ms integer NOT NULL CHECK(statement_timeout_ms BETWEEN 100 AND 120000),
  policy_version text NOT NULL CHECK(
    length(policy_version) BETWEEN 1 AND 64
    AND policy_version=btrim(policy_version)
    AND policy_version !~ '[[:cntrl:]]'
  ),
  input_hash text NOT NULL CHECK(input_hash ~ '^[0-9a-f]{64}$'),
  status text NOT NULL DEFAULT 'PENDING' CHECK(
    status IN ('PENDING','RUNNING','SUCCEEDED','SKIPPED','FAILED','STALE')
  ),
  attempt integer NOT NULL DEFAULT 0 CHECK(attempt>=0),
  max_attempts integer NOT NULL DEFAULT 3 CHECK(max_attempts BETWEEN 1 AND 10),
  next_attempt_at timestamptz NOT NULL DEFAULT now(),
  lease_owner text NOT NULL DEFAULT '' CHECK(length(lease_owner)<=128),
  lease_token uuid,
  lease_expires_at timestamptz,
  error_code text NOT NULL DEFAULT '' CHECK(
    error_code='' OR error_code ~ '^[A-Z][A-Z0-9_]{0,127}$'
  ),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  started_at timestamptz,
  completed_at timestamptz,
  CONSTRAINT askdata_dimension_profile_jobs_dimension_fk
    FOREIGN KEY(dimension_version_id,domain_id,tenant_id)
    REFERENCES askdata.dimensions(id,domain_id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT askdata_dimension_profile_jobs_model_fk
    FOREIGN KEY(semantic_model_version_id,domain_id,tenant_id)
    REFERENCES askdata.semantic_models(id,domain_id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT askdata_dimension_profile_jobs_dataset_version_fk
    FOREIGN KEY(dataset_version_id,dataset_id,tenant_id,dataset_schema_hash)
    REFERENCES platform.dataset_versions(id,dataset_id,tenant_id,schema_hash) ON DELETE RESTRICT,
  CONSTRAINT askdata_dimension_profile_jobs_materialization_fk
    FOREIGN KEY(materialization_id,dataset_id,dataset_version_id,tenant_id)
    REFERENCES platform.dataset_materializations(id,dataset_id,dataset_version_id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT askdata_dimension_profile_jobs_generation_key
    UNIQUE(tenant_id,dimension_version_id,generation),
  CONSTRAINT askdata_dimension_profile_jobs_input_key
    UNIQUE(tenant_id,dimension_version_id,input_hash),
  CONSTRAINT askdata_dimension_profile_jobs_identity_tenant_key UNIQUE(id,tenant_id),
  CONSTRAINT askdata_dimension_profile_jobs_identity_domain_tenant_key UNIQUE(id,domain_id,tenant_id),
  CONSTRAINT askdata_dimension_profile_jobs_attempt_check CHECK(attempt<=max_attempts),
  CONSTRAINT askdata_dimension_profile_jobs_status_shape_check CHECK(
    (
      status='PENDING' AND completed_at IS NULL AND error_code=''
      AND lease_owner='' AND lease_token IS NULL AND lease_expires_at IS NULL
    ) OR (
      status='RUNNING' AND attempt>0 AND started_at IS NOT NULL
      AND completed_at IS NULL AND error_code=''
      AND lease_owner<>'' AND lease_token IS NOT NULL AND lease_expires_at IS NOT NULL
    ) OR (
      status='SUCCEEDED' AND attempt>0 AND started_at IS NOT NULL
      AND completed_at IS NOT NULL AND completed_at>=started_at AND error_code=''
      AND lease_owner='' AND lease_token IS NULL AND lease_expires_at IS NULL
    ) OR (
      status IN ('SKIPPED','FAILED','STALE') AND completed_at IS NOT NULL
      AND (started_at IS NULL OR completed_at>=started_at) AND error_code<>''
      AND lease_owner='' AND lease_token IS NULL AND lease_expires_at IS NULL
    )
  )
);

CREATE TABLE askdata.dimension_profiles(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id) ON DELETE RESTRICT,
  domain_id uuid NOT NULL,
  dimension_version_id uuid NOT NULL,
  job_id uuid NOT NULL,
  generation bigint NOT NULL CHECK(generation>0),
  source_snapshot_hash text NOT NULL CHECK(source_snapshot_hash ~ '^[0-9a-f]{64}$'),
  profile_json jsonb NOT NULL CHECK(
    jsonb_typeof(profile_json)='object'
    AND pg_column_size(profile_json)<=262144
    AND askdata.json_is_safe(profile_json)
  ),
  profile_hash text NOT NULL CHECK(profile_hash ~ '^[0-9a-f]{64}$'),
  policy_decision_json jsonb NOT NULL CHECK(
    jsonb_typeof(policy_decision_json)='object'
    AND pg_column_size(policy_decision_json)<=131072
    AND askdata.json_is_safe(policy_decision_json)
  ),
  policy_decision_hash text NOT NULL CHECK(policy_decision_hash ~ '^[0-9a-f]{64}$'),
  created_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT askdata_dimension_profiles_dimension_fk
    FOREIGN KEY(dimension_version_id,domain_id,tenant_id)
    REFERENCES askdata.dimensions(id,domain_id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT askdata_dimension_profiles_job_fk
    FOREIGN KEY(job_id,domain_id,tenant_id)
    REFERENCES askdata.dimension_profile_jobs(id,domain_id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT askdata_dimension_profiles_job_key UNIQUE(tenant_id,job_id),
  CONSTRAINT askdata_dimension_profiles_generation_key UNIQUE(tenant_id,dimension_version_id,generation),
  CONSTRAINT askdata_dimension_profiles_identity_tenant_key UNIQUE(id,tenant_id),
  CONSTRAINT askdata_dimension_profiles_identity_domain_tenant_key UNIQUE(id,domain_id,tenant_id)
);

CREATE TABLE askdata.dimension_profile_members(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id) ON DELETE RESTRICT,
  domain_id uuid NOT NULL,
  dimension_version_id uuid NOT NULL,
  profile_id uuid NOT NULL,
  generation bigint NOT NULL CHECK(generation>0),
  member_key_hash text NOT NULL CHECK(member_key_hash ~ '^[0-9a-f]{64}$'),
  canonical_label text NOT NULL CHECK(
    length(canonical_label) BETWEEN 1 AND 512
    AND canonical_label=btrim(canonical_label)
    AND canonical_label !~ '[[:cntrl:]]'
  ),
  normalized_value text NOT NULL CHECK(
    length(normalized_value) BETWEEN 1 AND 512
    AND normalized_value=btrim(normalized_value)
    AND normalized_value !~ '[[:cntrl:]]'
  ),
  observed_aliases text[] NOT NULL DEFAULT '{}'::text[] CHECK(
    cardinality(observed_aliases)<=64 AND array_position(observed_aliases,NULL) IS NULL
  ),
  observed_count bigint NOT NULL CHECK(observed_count>0),
  sensitivity text NOT NULL CHECK(sensitivity IN ('PUBLIC','INTERNAL','CONFIDENTIAL','RESTRICTED')),
  eligible_for_llm boolean NOT NULL,
  content_hash text NOT NULL CHECK(content_hash ~ '^[0-9a-f]{64}$'),
  created_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT askdata_dimension_profile_members_sensitive_llm_check CHECK(
    sensitivity IN ('PUBLIC','INTERNAL') OR NOT eligible_for_llm
  ),
  CONSTRAINT askdata_dimension_profile_members_dimension_fk
    FOREIGN KEY(dimension_version_id,domain_id,tenant_id)
    REFERENCES askdata.dimensions(id,domain_id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT askdata_dimension_profile_members_profile_fk
    FOREIGN KEY(profile_id,domain_id,tenant_id)
    REFERENCES askdata.dimension_profiles(id,domain_id,tenant_id) ON DELETE CASCADE,
  CONSTRAINT askdata_dimension_profile_members_key
    UNIQUE(tenant_id,profile_id,member_key_hash),
  CONSTRAINT askdata_dimension_profile_members_identity_tenant_key UNIQUE(id,tenant_id)
);

CREATE INDEX askdata_dimension_profile_jobs_claim_idx
  ON askdata.dimension_profile_jobs(
    status,next_attempt_at,lease_expires_at,tenant_id,domain_id,created_at,id
  ) WHERE status IN ('PENDING','RUNNING');
CREATE INDEX askdata_dimension_profiles_latest_idx
  ON askdata.dimension_profiles(tenant_id,domain_id,dimension_version_id,generation DESC,id);
CREATE INDEX askdata_dimension_profile_members_lookup_idx
  ON askdata.dimension_profile_members(
    tenant_id,domain_id,dimension_version_id,generation,member_key_hash
  );

CREATE TRIGGER askdata_dimension_profile_jobs_set_updated_at
BEFORE UPDATE ON askdata.dimension_profile_jobs
FOR EACH ROW EXECUTE FUNCTION askdata.set_updated_at();

CREATE TRIGGER askdata_dimension_profiles_immutable
BEFORE UPDATE OR DELETE ON askdata.dimension_profiles
FOR EACH ROW EXECUTE FUNCTION askdata.reject_immutable_mutation();

CREATE TRIGGER askdata_dimension_profile_members_immutable
BEFORE UPDATE OR DELETE ON askdata.dimension_profile_members
FOR EACH ROW EXECUTE FUNCTION askdata.reject_immutable_mutation();

DO $rls$
DECLARE relation_name text;
BEGIN
  FOREACH relation_name IN ARRAY ARRAY[
    'dimension_profile_jobs','dimension_profiles','dimension_profile_members'
  ] LOOP
    EXECUTE format('ALTER TABLE askdata.%I ENABLE ROW LEVEL SECURITY',relation_name);
    EXECUTE format('ALTER TABLE askdata.%I FORCE ROW LEVEL SECURITY',relation_name);
    EXECUTE format(
      'CREATE POLICY %I ON askdata.%I USING(askdata.tenant_matches(tenant_id) AND askdata.domain_can_access(domain_id)) WITH CHECK(askdata.tenant_matches(tenant_id) AND askdata.domain_can_access(domain_id))',
      'askdata_'||relation_name||'_domain_isolation',relation_name
    );
  END LOOP;
END
$rls$;

COMMENT ON TABLE askdata.dimension_profile_jobs IS
  'Leased, bounded warehouse member scans pinned to an exact DWS/ADS materialization snapshot';
COMMENT ON TABLE askdata.dimension_profiles IS
  'Append-only auditable profile and deterministic member-index policy decisions';
COMMENT ON TABLE askdata.dimension_profile_members IS
  'Append-only observed normalized members for one profile generation; not certified semantic members';
