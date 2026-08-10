-- Complete the governed storage surface required by the twelve semantic import
-- asset contracts. Business terms are split into stable identities and immutable
-- versions; the legacy version rows are migrated without changing their ids.

ALTER TABLE askdata.measures
  ADD COLUMN null_policy text NOT NULL DEFAULT 'PRESERVE'
  CHECK(null_policy IN ('PRESERVE','ZERO','REJECT'));

CREATE OR REPLACE FUNCTION askdata.enforce_semantic_import_transition()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE row_total integer;
DECLARE row_valid integer;
DECLARE row_invalid integer;
DECLARE row_skipped integer;
DECLARE row_committed integer;
BEGIN
  IF (NEW.id,NEW.tenant_id,NEW.domain_id,NEW.asset_type,NEW.file_object_uri,
      NEW.file_hash,NEW.file_name,NEW.created_by,NEW.created_at)
    IS DISTINCT FROM
     (OLD.id,OLD.tenant_id,OLD.domain_id,OLD.asset_type,OLD.file_object_uri,
      OLD.file_hash,OLD.file_name,OLD.created_by,OLD.created_at) THEN
    RAISE EXCEPTION 'semantic import identity is immutable' USING ERRCODE='55000';
  END IF;
  IF NOT (
    (OLD.state='UPLOADED' AND NEW.state='VALIDATING')
    OR (OLD.state='VALIDATING' AND NEW.state IN ('VALIDATING','VALIDATED','FAILED'))
    OR (OLD.state='VALIDATED' AND NEW.state IN ('PARTIALLY_COMMITTED','COMMITTED','WITHDRAWN'))
    OR (OLD.state='PARTIALLY_COMMITTED' AND NEW.state IN ('PARTIALLY_COMMITTED','COMMITTED','WITHDRAWN'))
    OR (OLD.state='COMMITTED' AND NEW.state='WITHDRAWN')
  ) THEN
    RAISE EXCEPTION 'illegal semantic import transition % -> %',OLD.state,NEW.state
      USING ERRCODE='55000';
  END IF;
  IF NEW.state IN ('VALIDATED','PARTIALLY_COMMITTED','COMMITTED','WITHDRAWN') THEN
    SELECT count(*)::integer,
      count(*) FILTER(WHERE validation_state IN ('VALID','COMMITTED'))::integer,
      count(*) FILTER(WHERE validation_state='INVALID')::integer,
      count(*) FILTER(WHERE validation_state='SKIPPED')::integer,
      count(*) FILTER(WHERE validation_state='COMMITTED')::integer
    INTO row_total,row_valid,row_invalid,row_skipped,row_committed
    FROM askdata.semantic_import_rows
    WHERE import_id=NEW.id AND tenant_id=NEW.tenant_id;
    IF NEW.total_rows<>row_total OR NEW.valid_rows<>row_valid
      OR NEW.invalid_rows<>row_invalid THEN
      RAISE EXCEPTION 'semantic import counters do not match row facts'
        USING ERRCODE='23514';
    END IF;
    IF NEW.state='PARTIALLY_COMMITTED'
      AND (row_committed=0 OR row_committed=row_valid AND row_invalid=0) THEN
      RAISE EXCEPTION 'partial semantic import commit shape is invalid'
        USING ERRCODE='23514';
    END IF;
    IF NEW.state='COMMITTED'
      AND (row_committed<>row_valid OR row_invalid<>0) THEN
      RAISE EXCEPTION 'complete semantic import contains uncommitted valid or invalid rows'
        USING ERRCODE='23514';
    END IF;
  END IF;
  RETURN NEW;
END
$$;

ALTER TABLE askdata.metric_versions
  ADD COLUMN dedup_key text NOT NULL DEFAULT '' CHECK(length(dedup_key)<=512),
  ADD COLUMN positive_examples text[] NOT NULL DEFAULT '{}'::text[]
    CHECK(cardinality(positive_examples)<=64),
  ADD COLUMN negative_examples text[] NOT NULL DEFAULT '{}'::text[]
    CHECK(cardinality(negative_examples)<=64);

ALTER TABLE askdata.dimensions
  ADD COLUMN groupable boolean NOT NULL DEFAULT true,
  ADD COLUMN filterable boolean NOT NULL DEFAULT true,
  ADD COLUMN sortable boolean NOT NULL DEFAULT true;

ALTER TABLE askdata.relationships
  ADD COLUMN valid_from timestamptz,
  ADD COLUMN valid_to timestamptz,
  ADD CONSTRAINT askdata_relationships_validity_check
    CHECK(valid_to IS NULL OR valid_from IS NULL OR valid_to>valid_from);

UPDATE askdata.relationships SET valid_from=created_at WHERE valid_from IS NULL;
ALTER TABLE askdata.relationships
  ALTER COLUMN valid_from SET NOT NULL,
  ALTER COLUMN valid_from SET DEFAULT now();

ALTER TABLE askdata.business_terms RENAME TO business_terms_legacy_000228;

CREATE TABLE askdata.business_terms(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id) ON DELETE RESTRICT,
  domain_id uuid NOT NULL,
  term text NOT NULL CHECK(
    length(btrim(term)) BETWEEN 1 AND 512
    AND term=btrim(term)
    AND term !~ '[[:cntrl:]]'
  ),
  term_type text NOT NULL CHECK(term_type IN ('METRIC','DIMENSION','MEMBER','TIME','OPERATOR')),
  created_by uuid NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT askdata_business_term_identities_identity_tenant_key UNIQUE(id,tenant_id),
  CONSTRAINT askdata_business_term_identities_identity_domain_tenant_key UNIQUE(id,domain_id,tenant_id),
  CONSTRAINT askdata_business_term_identities_term_key UNIQUE(tenant_id,domain_id,term,term_type),
  CONSTRAINT askdata_business_term_identities_domain_fk
    FOREIGN KEY(domain_id,tenant_id)
    REFERENCES askdata.domains(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT askdata_business_term_identities_creator_fk
    FOREIGN KEY(created_by,tenant_id)
    REFERENCES platform.users(id,tenant_id) ON DELETE RESTRICT
);

CREATE TABLE askdata.business_term_versions(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id) ON DELETE RESTRICT,
  domain_id uuid NOT NULL,
  business_term_id uuid NOT NULL,
  version_no integer NOT NULL CHECK(version_no>0),
  status text NOT NULL DEFAULT 'DRAFT' CHECK(status IN ('DRAFT','CERTIFIED','DEPRECATED')),
  target_object_type text NOT NULL CHECK(target_object_type IN (
    'METRIC','DIMENSION','MEMBER','TIME_CONTRACT','OPERATOR','LEGACY'
  )),
  target_version_id uuid NOT NULL,
  target_code text NOT NULL DEFAULT '' CHECK(
    length(target_code)<=512 AND target_code=btrim(target_code)
    AND target_code !~ '[[:cntrl:]]'
  ),
  match_mode text NOT NULL CHECK(match_mode IN ('EXACT','PREFIX','SUFFIX','REGEX_SAFE','VECTOR')),
  match_pattern text CHECK(
    match_pattern IS NULL OR (
      length(match_pattern) BETWEEN 1 AND 1024
      AND match_pattern=btrim(match_pattern)
      AND match_pattern !~ '[[:cntrl:]]'
    )
  ),
  priority integer NOT NULL DEFAULT 100 CHECK(priority BETWEEN 0 AND 1000),
  negative_contexts text[] NOT NULL DEFAULT '{}'::text[] CHECK(cardinality(negative_contexts)<=64),
  applicable_role_ids uuid[] NOT NULL DEFAULT '{}'::uuid[] CHECK(cardinality(applicable_role_ids)<=64),
  valid_from timestamptz,
  valid_to timestamptz,
  source text NOT NULL CHECK(source IN (
    'MANUAL','IMPORT','FEEDBACK','ACTIVE_LEARNING','REPORT_ASSET',
    'CERTIFIED_EXAMPLE','FEEDBACK_CANDIDATE'
  )),
  review_status text NOT NULL DEFAULT 'PENDING' CHECK(review_status IN ('PENDING','APPROVED','REJECTED')),
  reviewed_by uuid,
  reviewed_at timestamptz,
  code citext NOT NULL,
  name text NOT NULL CHECK(length(btrim(name)) BETWEEN 1 AND 200),
  definition text NOT NULL DEFAULT '' CHECK(length(definition)<=4000 AND definition !~ '[[:cntrl:]]'),
  aliases text[] NOT NULL DEFAULT '{}'::text[] CHECK(cardinality(aliases)<=64),
  content_hash text NOT NULL CHECK(content_hash ~ '^[0-9a-f]{64}$'),
  owner_id uuid NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT askdata_business_term_versions_identity_key UNIQUE(tenant_id,business_term_id,version_no),
  CONSTRAINT askdata_business_term_versions_identity_tenant_key UNIQUE(id,tenant_id),
  CONSTRAINT askdata_business_term_versions_identity_domain_tenant_key UNIQUE(id,domain_id,tenant_id),
  CONSTRAINT askdata_business_term_versions_term_fk
    FOREIGN KEY(business_term_id,domain_id,tenant_id)
    REFERENCES askdata.business_terms(id,domain_id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT askdata_business_term_versions_domain_fk
    FOREIGN KEY(domain_id,tenant_id)
    REFERENCES askdata.domains(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT askdata_business_term_versions_owner_fk
    FOREIGN KEY(owner_id,tenant_id)
    REFERENCES platform.users(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT askdata_business_term_versions_reviewer_fk
    FOREIGN KEY(reviewed_by,tenant_id)
    REFERENCES platform.users(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT askdata_business_term_versions_match_pattern_check CHECK(
    match_mode<>'REGEX_SAFE' OR match_pattern IS NOT NULL
  ),
  CONSTRAINT askdata_business_term_versions_validity_check CHECK(
    valid_to IS NULL OR valid_from IS NULL OR valid_to>valid_from
  ),
  CONSTRAINT askdata_business_term_versions_review_check CHECK(
    (review_status='PENDING' AND reviewed_by IS NULL AND reviewed_at IS NULL)
    OR (review_status IN ('APPROVED','REJECTED') AND reviewed_by IS NOT NULL AND reviewed_at IS NOT NULL)
  )
);

INSERT INTO askdata.business_terms(
  id,tenant_id,domain_id,term,term_type,created_by,created_at,updated_at
)
SELECT DISTINCT ON (legacy.tenant_id,legacy.term_id)
  legacy.term_id,legacy.tenant_id,legacy.domain_id,legacy.code::text,'OPERATOR',
  legacy.owner_id,legacy.created_at,legacy.updated_at
FROM askdata.business_terms_legacy_000228 AS legacy
ORDER BY legacy.tenant_id,legacy.term_id,legacy.version_no;

INSERT INTO askdata.business_term_versions(
  id,tenant_id,domain_id,business_term_id,version_no,status,
  target_object_type,target_version_id,target_code,match_mode,priority,
  source,review_status,reviewed_by,reviewed_at,code,name,definition,aliases,
  content_hash,owner_id,created_at,updated_at
)
SELECT legacy.id,legacy.tenant_id,legacy.domain_id,legacy.term_id,legacy.version_no,
  legacy.status,'LEGACY',legacy.id,legacy.code::text,'EXACT',100,'MANUAL',
  CASE WHEN legacy.status='CERTIFIED' THEN 'APPROVED' ELSE 'PENDING' END,
  CASE WHEN legacy.status='CERTIFIED' THEN legacy.owner_id ELSE NULL END,
  CASE WHEN legacy.status='CERTIFIED' THEN legacy.updated_at ELSE NULL END,
  legacy.code,legacy.name,legacy.definition,legacy.aliases,legacy.content_hash,
  legacy.owner_id,legacy.created_at,legacy.updated_at
FROM askdata.business_terms_legacy_000228 AS legacy;

DROP TABLE askdata.business_terms_legacy_000228;

CREATE TABLE askdata.metric_dimensions(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id) ON DELETE RESTRICT,
  domain_id uuid NOT NULL,
  metric_id uuid NOT NULL,
  dimension_id uuid NOT NULL,
  created_by uuid NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT askdata_metric_dimensions_pair_key UNIQUE(tenant_id,domain_id,metric_id,dimension_id),
  CONSTRAINT askdata_metric_dimensions_identity_tenant_key UNIQUE(id,tenant_id),
  CONSTRAINT askdata_metric_dimensions_identity_domain_tenant_key UNIQUE(id,domain_id,tenant_id),
  CONSTRAINT askdata_metric_dimensions_domain_fk
    FOREIGN KEY(domain_id,tenant_id) REFERENCES askdata.domains(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT askdata_metric_dimensions_metric_fk
    FOREIGN KEY(metric_id,domain_id,tenant_id) REFERENCES askdata.metrics(id,domain_id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT askdata_metric_dimensions_creator_fk
    FOREIGN KEY(created_by,tenant_id) REFERENCES platform.users(id,tenant_id) ON DELETE RESTRICT
);

CREATE TABLE askdata.metric_dimension_versions(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id) ON DELETE RESTRICT,
  domain_id uuid NOT NULL,
  metric_dimension_id uuid NOT NULL,
  version_no integer NOT NULL CHECK(version_no>0),
  metric_version_id uuid NOT NULL,
  dimension_version_id uuid NOT NULL,
  compatible boolean NOT NULL,
  role text NOT NULL CHECK(role IN ('FILTER','GROUP_BY','ORDER_BY','BREAKDOWN')),
  status text NOT NULL DEFAULT 'DRAFT' CHECK(status IN ('DRAFT','CERTIFIED','DEPRECATED')),
  content_hash text NOT NULL CHECK(content_hash ~ '^[0-9a-f]{64}$'),
  owner_id uuid NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT askdata_metric_dimension_versions_identity_key UNIQUE(tenant_id,metric_dimension_id,version_no),
  CONSTRAINT askdata_metric_dimension_versions_identity_tenant_key UNIQUE(id,tenant_id),
  CONSTRAINT askdata_metric_dimension_versions_identity_domain_tenant_key UNIQUE(id,domain_id,tenant_id),
  CONSTRAINT askdata_metric_dimension_versions_identity_fk
    FOREIGN KEY(metric_dimension_id,domain_id,tenant_id)
    REFERENCES askdata.metric_dimensions(id,domain_id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT askdata_metric_dimension_versions_metric_fk
    FOREIGN KEY(metric_version_id,domain_id,tenant_id)
    REFERENCES askdata.metric_versions(id,domain_id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT askdata_metric_dimension_versions_dimension_fk
    FOREIGN KEY(dimension_version_id,domain_id,tenant_id)
    REFERENCES askdata.dimensions(id,domain_id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT askdata_metric_dimension_versions_owner_fk
    FOREIGN KEY(owner_id,tenant_id) REFERENCES platform.users(id,tenant_id) ON DELETE RESTRICT
);

CREATE TABLE askdata.certified_examples(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id) ON DELETE RESTRICT,
  domain_id uuid NOT NULL,
  question_hash text NOT NULL CHECK(question_hash ~ '^[0-9a-f]{64}$'),
  created_by uuid NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT askdata_certified_examples_question_key UNIQUE(tenant_id,domain_id,question_hash),
  CONSTRAINT askdata_certified_examples_identity_tenant_key UNIQUE(id,tenant_id),
  CONSTRAINT askdata_certified_examples_identity_domain_tenant_key UNIQUE(id,domain_id,tenant_id),
  CONSTRAINT askdata_certified_examples_domain_fk
    FOREIGN KEY(domain_id,tenant_id) REFERENCES askdata.domains(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT askdata_certified_examples_creator_fk
    FOREIGN KEY(created_by,tenant_id) REFERENCES platform.users(id,tenant_id) ON DELETE RESTRICT
);

CREATE TABLE askdata.certified_example_versions(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id) ON DELETE RESTRICT,
  domain_id uuid NOT NULL,
  certified_example_id uuid NOT NULL,
  version_no integer NOT NULL CHECK(version_no>0),
  question text NOT NULL CHECK(length(btrim(question)) BETWEEN 1 AND 4000 AND question !~ '[[:cntrl:]]'),
  expected_metric_version_ids uuid[] NOT NULL DEFAULT '{}'::uuid[] CHECK(cardinality(expected_metric_version_ids)<=64),
  expected_dimension_version_ids uuid[] NOT NULL DEFAULT '{}'::uuid[] CHECK(cardinality(expected_dimension_version_ids)<=64),
  expected_member_values jsonb NOT NULL DEFAULT '[]'::jsonb CHECK(
    jsonb_typeof(expected_member_values)='array' AND pg_column_size(expected_member_values)<=65536
    AND askdata.json_is_safe(expected_member_values)
  ),
  expected_time_expression text NOT NULL DEFAULT '' CHECK(length(expected_time_expression)<=512),
  applicable_role_ids uuid[] NOT NULL DEFAULT '{}'::uuid[] CHECK(cardinality(applicable_role_ids)<=64),
  notes text NOT NULL DEFAULT '' CHECK(length(notes)<=4000 AND notes !~ '[[:cntrl:]]'),
  status text NOT NULL DEFAULT 'DRAFT' CHECK(status IN ('DRAFT','CERTIFIED','DEPRECATED')),
  content_hash text NOT NULL CHECK(content_hash ~ '^[0-9a-f]{64}$'),
  owner_id uuid NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT askdata_certified_example_versions_identity_key UNIQUE(tenant_id,certified_example_id,version_no),
  CONSTRAINT askdata_certified_example_versions_identity_tenant_key UNIQUE(id,tenant_id),
  CONSTRAINT askdata_certified_example_versions_identity_domain_tenant_key UNIQUE(id,domain_id,tenant_id),
  CONSTRAINT askdata_certified_example_versions_example_fk
    FOREIGN KEY(certified_example_id,domain_id,tenant_id)
    REFERENCES askdata.certified_examples(id,domain_id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT askdata_certified_example_versions_domain_fk
    FOREIGN KEY(domain_id,tenant_id) REFERENCES askdata.domains(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT askdata_certified_example_versions_owner_fk
    FOREIGN KEY(owner_id,tenant_id) REFERENCES platform.users(id,tenant_id) ON DELETE RESTRICT
);

CREATE TABLE askdata.kpi_bundles(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id) ON DELETE RESTRICT,
  domain_id uuid NOT NULL,
  code citext NOT NULL,
  name text NOT NULL CHECK(length(btrim(name)) BETWEEN 1 AND 200),
  owner_user_id uuid NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT askdata_kpi_bundles_code_key UNIQUE(tenant_id,domain_id,code),
  CONSTRAINT askdata_kpi_bundles_identity_tenant_key UNIQUE(id,tenant_id),
  CONSTRAINT askdata_kpi_bundles_identity_domain_tenant_key UNIQUE(id,domain_id,tenant_id),
  CONSTRAINT askdata_kpi_bundles_domain_fk
    FOREIGN KEY(domain_id,tenant_id) REFERENCES askdata.domains(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT askdata_kpi_bundles_owner_fk
    FOREIGN KEY(owner_user_id,tenant_id) REFERENCES platform.users(id,tenant_id) ON DELETE RESTRICT
);

CREATE TABLE askdata.kpi_bundle_versions(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id) ON DELETE RESTRICT,
  domain_id uuid NOT NULL,
  kpi_bundle_id uuid NOT NULL,
  version_no integer NOT NULL CHECK(version_no>0),
  status text NOT NULL DEFAULT 'DRAFT' CHECK(status IN ('DRAFT','CERTIFIED','DEPRECATED')),
  items jsonb NOT NULL CHECK(
    jsonb_typeof(items)='array' AND jsonb_array_length(items) BETWEEN 1 AND 8
    AND pg_column_size(items)<=131072 AND askdata.json_is_safe(items)
  ),
  default_dimension_version_ids uuid[] NOT NULL DEFAULT '{}'::uuid[] CHECK(cardinality(default_dimension_version_ids)<=64),
  default_time_expression text NOT NULL CHECK(length(btrim(default_time_expression)) BETWEEN 1 AND 512),
  default_chart_types text[] NOT NULL DEFAULT '{}'::text[] CHECK(cardinality(default_chart_types)<=16),
  role_mapping jsonb NOT NULL DEFAULT '{}'::jsonb CHECK(
    jsonb_typeof(role_mapping)='object' AND pg_column_size(role_mapping)<=65536
    AND askdata.json_is_safe(role_mapping)
  ),
  applicable_question_patterns text[] NOT NULL DEFAULT '{}'::text[] CHECK(cardinality(applicable_question_patterns)<=64),
  content_hash text NOT NULL CHECK(content_hash ~ '^[0-9a-f]{64}$'),
  owner_id uuid NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT askdata_kpi_bundle_versions_identity_key UNIQUE(tenant_id,kpi_bundle_id,version_no),
  CONSTRAINT askdata_kpi_bundle_versions_identity_tenant_key UNIQUE(id,tenant_id),
  CONSTRAINT askdata_kpi_bundle_versions_identity_domain_tenant_key UNIQUE(id,domain_id,tenant_id),
  CONSTRAINT askdata_kpi_bundle_versions_bundle_fk
    FOREIGN KEY(kpi_bundle_id,domain_id,tenant_id)
    REFERENCES askdata.kpi_bundles(id,domain_id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT askdata_kpi_bundle_versions_domain_fk
    FOREIGN KEY(domain_id,tenant_id) REFERENCES askdata.domains(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT askdata_kpi_bundle_versions_owner_fk
    FOREIGN KEY(owner_id,tenant_id) REFERENCES platform.users(id,tenant_id) ON DELETE RESTRICT
);

-- Imported evaluation cases are governed/versioned before they are copied into
-- the sealed evaluation-set runtime owned by 000218.
CREATE TABLE askdata.evaluation_case_assets(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id) ON DELETE RESTRICT,
  domain_id uuid NOT NULL,
  question_hash text NOT NULL CHECK(question_hash ~ '^[0-9a-f]{64}$'),
  created_by uuid NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT askdata_evaluation_case_assets_question_key UNIQUE(tenant_id,domain_id,question_hash),
  CONSTRAINT askdata_evaluation_case_assets_identity_tenant_key UNIQUE(id,tenant_id),
  CONSTRAINT askdata_evaluation_case_assets_identity_domain_tenant_key UNIQUE(id,domain_id,tenant_id),
  CONSTRAINT askdata_evaluation_case_assets_domain_fk
    FOREIGN KEY(domain_id,tenant_id) REFERENCES askdata.domains(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT askdata_evaluation_case_assets_creator_fk
    FOREIGN KEY(created_by,tenant_id) REFERENCES platform.users(id,tenant_id) ON DELETE RESTRICT
);

CREATE TABLE askdata.evaluation_case_versions(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id) ON DELETE RESTRICT,
  domain_id uuid NOT NULL,
  evaluation_case_asset_id uuid NOT NULL,
  version_no integer NOT NULL CHECK(version_no>0),
  question text NOT NULL CHECK(length(btrim(question)) BETWEEN 1 AND 4000 AND question !~ '[[:cntrl:]]'),
  actor_role text NOT NULL CHECK(length(btrim(actor_role)) BETWEEN 1 AND 128),
  expected_outcome text NOT NULL CHECK(expected_outcome IN ('DIRECT','CLARIFY','REFUSE')),
  expected_metric_version_ids uuid[] NOT NULL DEFAULT '{}'::uuid[] CHECK(cardinality(expected_metric_version_ids)<=64),
  expected_dimension_version_ids uuid[] NOT NULL DEFAULT '{}'::uuid[] CHECK(cardinality(expected_dimension_version_ids)<=64),
  expected_member_values jsonb NOT NULL DEFAULT '[]'::jsonb CHECK(
    jsonb_typeof(expected_member_values)='array' AND pg_column_size(expected_member_values)<=65536
    AND askdata.json_is_safe(expected_member_values)
  ),
  expected_time_expression text NOT NULL DEFAULT '' CHECK(length(expected_time_expression)<=512),
  expected_result_hint text NOT NULL DEFAULT '' CHECK(length(expected_result_hint)<=4000 AND expected_result_hint !~ '[[:cntrl:]]'),
  set_type text NOT NULL CHECK(set_type IN ('TRAIN','VALIDATION','SEALED','PRODUCTION_REGRESSION')),
  shard_id smallint NOT NULL CHECK(shard_id BETWEEN 1 AND 4),
  status text NOT NULL DEFAULT 'DRAFT' CHECK(status IN ('DRAFT','CERTIFIED','DEPRECATED')),
  content_hash text NOT NULL CHECK(content_hash ~ '^[0-9a-f]{64}$'),
  owner_id uuid NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT askdata_evaluation_case_versions_identity_key UNIQUE(tenant_id,evaluation_case_asset_id,version_no),
  CONSTRAINT askdata_evaluation_case_versions_identity_tenant_key UNIQUE(id,tenant_id),
  CONSTRAINT askdata_evaluation_case_versions_identity_domain_tenant_key UNIQUE(id,domain_id,tenant_id),
  CONSTRAINT askdata_evaluation_case_versions_asset_fk
    FOREIGN KEY(evaluation_case_asset_id,domain_id,tenant_id)
    REFERENCES askdata.evaluation_case_assets(id,domain_id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT askdata_evaluation_case_versions_domain_fk
    FOREIGN KEY(domain_id,tenant_id) REFERENCES askdata.domains(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT askdata_evaluation_case_versions_owner_fk
    FOREIGN KEY(owner_id,tenant_id) REFERENCES platform.users(id,tenant_id) ON DELETE RESTRICT
);

CREATE OR REPLACE FUNCTION askdata.validate_governed_import_version()
RETURNS trigger
LANGUAGE plpgsql
SET search_path=pg_catalog,askdata
AS $$
BEGIN
  IF TG_OP='UPDATE' AND OLD.status='CERTIFIED' THEN
    RAISE EXCEPTION 'certified askdata versions are immutable' USING ERRCODE='55000';
  END IF;
  IF TG_OP='DELETE' AND OLD.status='CERTIFIED' THEN
    RAISE EXCEPTION 'certified askdata versions are immutable' USING ERRCODE='55000';
  END IF;
  RETURN CASE WHEN TG_OP='DELETE' THEN OLD ELSE NEW END;
END
$$;

REVOKE ALL ON FUNCTION askdata.validate_governed_import_version() FROM PUBLIC;

-- Governance imports may resolve an exact member only through an opaque,
-- dimension-bound hash. Runtime roles still cannot SELECT member values or
-- aliases, and non-admin USER contexts receive no rows.
CREATE OR REPLACE FUNCTION askdata.resolve_governed_import_member(
  selected_domain_id uuid,
  selected_dimension_version_id uuid,
  selected_lookup_key_hash text
)
RETURNS TABLE(member_version_id uuid,member_id uuid)
LANGUAGE sql
STABLE
STRICT
SECURITY DEFINER
SET search_path=pg_catalog,askdata,platform
AS $$
  WITH request_context AS (
    SELECT askdata.current_tenant_id() AS tenant_id,
      askdata.current_domain_id() AS domain_id
  ), authorized_context AS (
    SELECT context.tenant_id
    FROM request_context AS context
    WHERE context.tenant_id IS NOT NULL
      AND selected_lookup_key_hash ~ '^[0-9a-f]{64}$'
      AND (
        askdata.system_access()
        OR (
          context.domain_id=selected_domain_id
          AND askdata.current_actor_id() IS NOT NULL
          AND (
            platform.user_is_platform_administrator()
            OR platform.user_is_domain_administrator(selected_domain_id)
          )
        )
      )
  ), eligible AS (
    SELECT member.id AS member_version_id,member.member_id
    FROM authorized_context AS context
    JOIN askdata.dimensions AS dimension
      ON dimension.tenant_id=context.tenant_id
     AND dimension.domain_id=selected_domain_id
     AND dimension.id=selected_dimension_version_id
     AND dimension.status='CERTIFIED'
     AND dimension.member_index_policy IN ('FULL','EXACT_ONLY')
    JOIN askdata.dimension_members AS member
      ON member.tenant_id=dimension.tenant_id
     AND member.domain_id=dimension.domain_id
     AND member.dimension_version_id=dimension.id
     AND member.member_key_hash=selected_lookup_key_hash
     AND member.status='CERTIFIED'
     AND member.valid_from<=pg_catalog.transaction_timestamp()
     AND (member.valid_to IS NULL OR pg_catalog.transaction_timestamp()<member.valid_to)
  )
  SELECT candidate.member_version_id,candidate.member_id
  FROM eligible AS candidate
  WHERE (SELECT pg_catalog.count(*) FROM eligible)=1
$$;

REVOKE ALL ON FUNCTION askdata.resolve_governed_import_member(uuid,uuid,text) FROM PUBLIC;

CREATE TRIGGER askdata_business_terms_set_updated_at
BEFORE UPDATE ON askdata.business_terms FOR EACH ROW EXECUTE FUNCTION askdata.set_updated_at();
CREATE TRIGGER askdata_business_term_versions_set_updated_at
BEFORE UPDATE ON askdata.business_term_versions FOR EACH ROW EXECUTE FUNCTION askdata.set_updated_at();
CREATE TRIGGER askdata_business_term_versions_protect_certified
BEFORE UPDATE OR DELETE ON askdata.business_term_versions
FOR EACH ROW EXECUTE FUNCTION askdata.validate_governed_import_version();
CREATE TRIGGER askdata_metric_dimensions_set_updated_at
BEFORE UPDATE ON askdata.metric_dimensions FOR EACH ROW EXECUTE FUNCTION askdata.set_updated_at();
CREATE TRIGGER askdata_metric_dimension_versions_set_updated_at
BEFORE UPDATE ON askdata.metric_dimension_versions FOR EACH ROW EXECUTE FUNCTION askdata.set_updated_at();
CREATE TRIGGER askdata_metric_dimension_versions_protect_certified
BEFORE UPDATE OR DELETE ON askdata.metric_dimension_versions
FOR EACH ROW EXECUTE FUNCTION askdata.validate_governed_import_version();
CREATE TRIGGER askdata_certified_examples_set_updated_at
BEFORE UPDATE ON askdata.certified_examples FOR EACH ROW EXECUTE FUNCTION askdata.set_updated_at();
CREATE TRIGGER askdata_certified_example_versions_set_updated_at
BEFORE UPDATE ON askdata.certified_example_versions FOR EACH ROW EXECUTE FUNCTION askdata.set_updated_at();
CREATE TRIGGER askdata_certified_example_versions_protect_certified
BEFORE UPDATE OR DELETE ON askdata.certified_example_versions
FOR EACH ROW EXECUTE FUNCTION askdata.validate_governed_import_version();
CREATE TRIGGER askdata_kpi_bundles_set_updated_at
BEFORE UPDATE ON askdata.kpi_bundles FOR EACH ROW EXECUTE FUNCTION askdata.set_updated_at();
CREATE TRIGGER askdata_kpi_bundle_versions_set_updated_at
BEFORE UPDATE ON askdata.kpi_bundle_versions FOR EACH ROW EXECUTE FUNCTION askdata.set_updated_at();
CREATE TRIGGER askdata_kpi_bundle_versions_protect_certified
BEFORE UPDATE OR DELETE ON askdata.kpi_bundle_versions
FOR EACH ROW EXECUTE FUNCTION askdata.validate_governed_import_version();
CREATE TRIGGER askdata_evaluation_case_assets_set_updated_at
BEFORE UPDATE ON askdata.evaluation_case_assets FOR EACH ROW EXECUTE FUNCTION askdata.set_updated_at();
CREATE TRIGGER askdata_evaluation_case_versions_set_updated_at
BEFORE UPDATE ON askdata.evaluation_case_versions FOR EACH ROW EXECUTE FUNCTION askdata.set_updated_at();
CREATE TRIGGER askdata_evaluation_case_versions_protect_certified
BEFORE UPDATE OR DELETE ON askdata.evaluation_case_versions
FOR EACH ROW EXECUTE FUNCTION askdata.validate_governed_import_version();

CREATE INDEX askdata_business_terms_lookup_idx
  ON askdata.business_terms(tenant_id,domain_id,term,term_type);
CREATE INDEX askdata_business_term_versions_lookup_idx
  ON askdata.business_term_versions(tenant_id,domain_id,business_term_id,status,version_no DESC);
CREATE INDEX askdata_metric_dimension_versions_lookup_idx
  ON askdata.metric_dimension_versions(tenant_id,domain_id,metric_version_id,dimension_version_id,status);
CREATE INDEX askdata_certified_example_versions_lookup_idx
  ON askdata.certified_example_versions(tenant_id,domain_id,status,created_at DESC);
CREATE INDEX askdata_kpi_bundle_versions_lookup_idx
  ON askdata.kpi_bundle_versions(tenant_id,domain_id,kpi_bundle_id,status,version_no DESC);
CREATE INDEX askdata_evaluation_case_versions_lookup_idx
  ON askdata.evaluation_case_versions(tenant_id,domain_id,evaluation_case_asset_id,status,version_no DESC);

ALTER TABLE askdata.release_objects
  DROP CONSTRAINT release_objects_object_type_check,
  ADD CONSTRAINT release_objects_object_type_check CHECK(object_type IN (
    'DOMAIN','ENTITY','SEMANTIC_MODEL','MEASURE','METRIC','METRIC_DIMENSION',
    'DIMENSION','MEMBER','HIERARCHY','RELATIONSHIP','QUALITY_RULE','BUSINESS_TERM',
    'CERTIFIED_EXAMPLE','TIME_CONTRACT','KPI_BUNDLE','EVAL_CASE'
  ));

CREATE OR REPLACE FUNCTION askdata.validate_release_object()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,askdata,platform
AS $$
DECLARE release_domain_id uuid;
DECLARE source_valid boolean := false;
DECLARE source_object_id uuid;
DECLARE source_hash text;
BEGIN
  SELECT domain_id INTO release_domain_id
  FROM askdata.releases
  WHERE tenant_id=NEW.tenant_id AND id=NEW.release_id AND status='DRAFT';
  IF release_domain_id IS NULL OR release_domain_id<>NEW.domain_id THEN
    RAISE EXCEPTION 'release object requires a DRAFT release in the same domain'
      USING ERRCODE='23514';
  END IF;
  CASE NEW.object_type
    WHEN 'DOMAIN' THEN
      SELECT true,id,NULL INTO source_valid,source_object_id,source_hash
      FROM askdata.domains
      WHERE tenant_id=NEW.tenant_id AND id=NEW.object_version_id AND status='ACTIVE';
    WHEN 'ENTITY' THEN
      SELECT true,entity_id,content_hash INTO source_valid,source_object_id,source_hash
      FROM askdata.entities
      WHERE tenant_id=NEW.tenant_id AND domain_id=NEW.domain_id
        AND id=NEW.object_version_id AND status='CERTIFIED';
    WHEN 'SEMANTIC_MODEL' THEN
      SELECT true,model.model_id,model.content_hash
      INTO source_valid,source_object_id,source_hash
      FROM askdata.semantic_models AS model
      JOIN platform.datasets AS dataset
        ON dataset.id=model.dataset_id AND dataset.tenant_id=model.tenant_id
      JOIN platform.dataset_versions AS version
        ON version.id=model.dataset_version_id
       AND version.dataset_id=dataset.id AND version.tenant_id=dataset.tenant_id
      JOIN platform.dataset_materializations AS materialization
        ON materialization.id=model.materialization_id
       AND materialization.dataset_id=dataset.id
       AND materialization.dataset_version_id=version.id
       AND materialization.tenant_id=dataset.tenant_id
      WHERE model.tenant_id=NEW.tenant_id AND model.domain_id=NEW.domain_id
        AND model.id=NEW.object_version_id AND model.status='CERTIFIED'
        AND model.time_contract_version_id IS NOT NULL
        AND dataset.domain_id=NEW.domain_id AND dataset.deleted_at IS NULL
        AND dataset.status='PUBLISHED'
        AND dataset.current_published_version_id=version.id
        AND version.status='PUBLISHED' AND version.layer IN ('DWS','ADS')
        AND version.layer=model.layer AND version.schema_hash=model.dataset_schema_hash
        AND materialization.status='ACTIVE' AND materialization.layer=version.layer
        AND materialization.schema_hash=version.schema_hash;
    WHEN 'MEASURE' THEN
      SELECT true,measure_id,content_hash INTO source_valid,source_object_id,source_hash
      FROM askdata.measures WHERE tenant_id=NEW.tenant_id AND domain_id=NEW.domain_id
        AND id=NEW.object_version_id AND status='CERTIFIED';
    WHEN 'METRIC' THEN
      SELECT true,metric_id,content_hash INTO source_valid,source_object_id,source_hash
      FROM askdata.metric_versions WHERE tenant_id=NEW.tenant_id AND domain_id=NEW.domain_id
        AND id=NEW.object_version_id AND status='CERTIFIED';
    WHEN 'METRIC_DIMENSION' THEN
      SELECT true,metric_dimension_id,content_hash INTO source_valid,source_object_id,source_hash
      FROM askdata.metric_dimension_versions
      WHERE tenant_id=NEW.tenant_id AND domain_id=NEW.domain_id
        AND id=NEW.object_version_id AND status='CERTIFIED';
    WHEN 'DIMENSION' THEN
      SELECT true,dimension_id,content_hash INTO source_valid,source_object_id,source_hash
      FROM askdata.dimensions WHERE tenant_id=NEW.tenant_id AND domain_id=NEW.domain_id
        AND id=NEW.object_version_id AND status='CERTIFIED';
    WHEN 'MEMBER' THEN
      SELECT true,member_id,content_hash INTO source_valid,source_object_id,source_hash
      FROM askdata.dimension_members WHERE tenant_id=NEW.tenant_id AND domain_id=NEW.domain_id
        AND id=NEW.object_version_id AND status='CERTIFIED';
    WHEN 'HIERARCHY' THEN
      SELECT true,hierarchy_id,content_hash INTO source_valid,source_object_id,source_hash
      FROM askdata.hierarchies WHERE tenant_id=NEW.tenant_id AND domain_id=NEW.domain_id
        AND id=NEW.object_version_id AND status='CERTIFIED';
    WHEN 'RELATIONSHIP' THEN
      SELECT true,relationship_id,content_hash INTO source_valid,source_object_id,source_hash
      FROM askdata.relationships WHERE tenant_id=NEW.tenant_id AND domain_id=NEW.domain_id
        AND id=NEW.object_version_id AND status='CERTIFIED';
    WHEN 'QUALITY_RULE' THEN
      SELECT true,quality_rule_id,content_hash INTO source_valid,source_object_id,source_hash
      FROM askdata.quality_rules WHERE tenant_id=NEW.tenant_id AND domain_id=NEW.domain_id
        AND id=NEW.object_version_id AND status='CERTIFIED';
    WHEN 'BUSINESS_TERM' THEN
      SELECT true,business_term_id,content_hash INTO source_valid,source_object_id,source_hash
      FROM askdata.business_term_versions
      WHERE tenant_id=NEW.tenant_id AND domain_id=NEW.domain_id
        AND id=NEW.object_version_id AND status='CERTIFIED' AND review_status='APPROVED';
    WHEN 'CERTIFIED_EXAMPLE' THEN
      SELECT true,certified_example_id,content_hash INTO source_valid,source_object_id,source_hash
      FROM askdata.certified_example_versions
      WHERE tenant_id=NEW.tenant_id AND domain_id=NEW.domain_id
        AND id=NEW.object_version_id AND status='CERTIFIED';
    WHEN 'TIME_CONTRACT' THEN
      SELECT true,contract.time_contract_id,contract.content_hash
      INTO source_valid,source_object_id,source_hash
      FROM askdata.time_contract_versions AS contract
      WHERE contract.tenant_id=NEW.tenant_id AND contract.domain_id=NEW.domain_id
        AND contract.id=NEW.object_version_id AND contract.status='CERTIFIED'
        AND (
          contract.calendar_dataset_version_id IS NULL OR EXISTS(
            SELECT 1 FROM platform.dataset_versions AS version
            JOIN platform.datasets AS dataset
              ON dataset.id=version.dataset_id AND dataset.tenant_id=version.tenant_id
            WHERE version.id=contract.calendar_dataset_version_id
              AND version.tenant_id=contract.tenant_id AND version.status='PUBLISHED'
              AND dataset.domain_id=contract.domain_id AND dataset.status='PUBLISHED'
              AND dataset.deleted_at IS NULL AND dataset.current_published_version_id=version.id
          )
        );
    WHEN 'KPI_BUNDLE' THEN
      SELECT true,kpi_bundle_id,content_hash INTO source_valid,source_object_id,source_hash
      FROM askdata.kpi_bundle_versions
      WHERE tenant_id=NEW.tenant_id AND domain_id=NEW.domain_id
        AND id=NEW.object_version_id AND status='CERTIFIED';
    WHEN 'EVAL_CASE' THEN
      SELECT true,evaluation_case_asset_id,content_hash INTO source_valid,source_object_id,source_hash
      FROM askdata.evaluation_case_versions
      WHERE tenant_id=NEW.tenant_id AND domain_id=NEW.domain_id
        AND id=NEW.object_version_id AND status='CERTIFIED';
  END CASE;
  IF NOT COALESCE(source_valid,false)
    OR source_object_id<>NEW.object_id
    OR (source_hash IS NOT NULL AND source_hash<>NEW.content_hash) THEN
    RAISE EXCEPTION 'release object does not match a certified immutable source'
      USING ERRCODE='23514';
  END IF;
  RETURN NEW;
END
$$;

REVOKE ALL ON FUNCTION askdata.validate_release_object() FROM PUBLIC;

CREATE OR REPLACE FUNCTION askdata.validate_search_document_subject()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,askdata
AS $$
DECLARE subject_valid boolean := false;
BEGIN
  CASE NEW.object_type
    WHEN 'ENTITY' THEN
      SELECT EXISTS(SELECT 1 FROM askdata.entities WHERE tenant_id=NEW.tenant_id AND domain_id=NEW.domain_id AND id=NEW.object_version_id AND status='CERTIFIED') INTO subject_valid;
    WHEN 'SEMANTIC_MODEL' THEN
      SELECT EXISTS(SELECT 1 FROM askdata.semantic_models WHERE tenant_id=NEW.tenant_id AND domain_id=NEW.domain_id AND id=NEW.object_version_id AND status='CERTIFIED') INTO subject_valid;
    WHEN 'MEASURE' THEN
      SELECT EXISTS(SELECT 1 FROM askdata.measures WHERE tenant_id=NEW.tenant_id AND domain_id=NEW.domain_id AND id=NEW.object_version_id AND status='CERTIFIED') INTO subject_valid;
    WHEN 'METRIC' THEN
      SELECT EXISTS(SELECT 1 FROM askdata.metric_versions WHERE tenant_id=NEW.tenant_id AND domain_id=NEW.domain_id AND id=NEW.object_version_id AND status='CERTIFIED') INTO subject_valid;
    WHEN 'DIMENSION' THEN
      SELECT EXISTS(SELECT 1 FROM askdata.dimensions WHERE tenant_id=NEW.tenant_id AND domain_id=NEW.domain_id AND id=NEW.object_version_id AND status='CERTIFIED' AND sensitivity<>'RESTRICTED' AND sensitivity=NEW.sensitivity) INTO subject_valid;
    WHEN 'MEMBER' THEN
      SELECT EXISTS(
        SELECT 1 FROM askdata.dimension_members AS member
        JOIN askdata.dimensions AS dimension ON dimension.id=member.dimension_version_id AND dimension.tenant_id=member.tenant_id
        WHERE member.tenant_id=NEW.tenant_id AND member.domain_id=NEW.domain_id
          AND member.id=NEW.object_version_id AND member.status='CERTIFIED'
          AND member.sensitivity IN ('PUBLIC','INTERNAL') AND dimension.sensitivity IN ('PUBLIC','INTERNAL')
          AND dimension.member_index_policy='FULL' AND NOT dimension.high_cardinality
          AND NEW.sensitivity=CASE WHEN member.sensitivity='INTERNAL' OR dimension.sensitivity='INTERNAL' THEN 'INTERNAL' ELSE 'PUBLIC' END
      ) INTO subject_valid;
    WHEN 'BUSINESS_TERM' THEN
      SELECT EXISTS(SELECT 1 FROM askdata.business_term_versions WHERE tenant_id=NEW.tenant_id AND domain_id=NEW.domain_id AND id=NEW.object_version_id AND status='CERTIFIED' AND review_status='APPROVED') INTO subject_valid;
    WHEN 'RELATIONSHIP' THEN
      SELECT EXISTS(SELECT 1 FROM askdata.relationships WHERE tenant_id=NEW.tenant_id AND domain_id=NEW.domain_id AND id=NEW.object_version_id AND status='CERTIFIED') INTO subject_valid;
    WHEN 'CERTIFIED_EXAMPLE' THEN
      SELECT EXISTS(SELECT 1 FROM askdata.certified_example_versions WHERE tenant_id=NEW.tenant_id AND domain_id=NEW.domain_id AND id=NEW.object_version_id AND status='CERTIFIED') INTO subject_valid;
  END CASE;
  IF NOT subject_valid THEN
    RAISE EXCEPTION 'search document subject is not a certified indexable object' USING ERRCODE='23514';
  END IF;
  RETURN NEW;
END
$$;

CREATE OR REPLACE FUNCTION askdata.validate_semantic_alias_subject()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE subject_valid boolean := false;
BEGIN
  CASE NEW.object_type
    WHEN 'ENTITY' THEN
      SELECT EXISTS(SELECT 1 FROM askdata.entities WHERE id=NEW.object_version_id AND domain_id=NEW.domain_id AND tenant_id=NEW.tenant_id AND status=CASE WHEN NEW.status='CERTIFIED' THEN 'CERTIFIED' ELSE status END AND status<>'DEPRECATED') INTO subject_valid;
    WHEN 'SEMANTIC_MODEL' THEN
      SELECT EXISTS(SELECT 1 FROM askdata.semantic_models WHERE id=NEW.object_version_id AND domain_id=NEW.domain_id AND tenant_id=NEW.tenant_id AND status=CASE WHEN NEW.status='CERTIFIED' THEN 'CERTIFIED' ELSE status END AND status<>'DEPRECATED') INTO subject_valid;
    WHEN 'MEASURE' THEN
      SELECT EXISTS(SELECT 1 FROM askdata.measures WHERE id=NEW.object_version_id AND domain_id=NEW.domain_id AND tenant_id=NEW.tenant_id AND status=CASE WHEN NEW.status='CERTIFIED' THEN 'CERTIFIED' ELSE status END AND status<>'DEPRECATED') INTO subject_valid;
    WHEN 'METRIC' THEN
      SELECT EXISTS(SELECT 1 FROM askdata.metric_versions WHERE id=NEW.object_version_id AND domain_id=NEW.domain_id AND tenant_id=NEW.tenant_id AND status=CASE WHEN NEW.status='CERTIFIED' THEN 'CERTIFIED' ELSE status END AND status<>'DEPRECATED') INTO subject_valid;
    WHEN 'DIMENSION' THEN
      SELECT EXISTS(SELECT 1 FROM askdata.dimensions WHERE id=NEW.object_version_id AND domain_id=NEW.domain_id AND tenant_id=NEW.tenant_id AND status=CASE WHEN NEW.status='CERTIFIED' THEN 'CERTIFIED' ELSE status END AND status<>'DEPRECATED') INTO subject_valid;
    WHEN 'BUSINESS_TERM' THEN
      SELECT EXISTS(SELECT 1 FROM askdata.business_term_versions WHERE id=NEW.object_version_id AND domain_id=NEW.domain_id AND tenant_id=NEW.tenant_id AND status=CASE WHEN NEW.status='CERTIFIED' THEN 'CERTIFIED' ELSE status END AND status<>'DEPRECATED') INTO subject_valid;
  END CASE;
  IF NOT COALESCE(subject_valid,false) THEN
    RAISE EXCEPTION 'semantic alias subject is missing, deprecated, cross-domain, or uncertified' USING ERRCODE='23514';
  END IF;
  RETURN NEW;
END
$$;

DO $rls$
DECLARE relation_name text;
BEGIN
  FOREACH relation_name IN ARRAY ARRAY[
    'business_terms','business_term_versions','metric_dimensions',
    'metric_dimension_versions','certified_examples','certified_example_versions',
    'kpi_bundles','kpi_bundle_versions','evaluation_case_assets','evaluation_case_versions'
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

COMMENT ON TABLE askdata.business_terms IS 'Stable governed business-term identities';
COMMENT ON TABLE askdata.business_term_versions IS 'Immutable governed business-term versions with conflict and review policy';
COMMENT ON TABLE askdata.metric_dimension_versions IS 'Versioned metric-to-dimension compatibility declarations imported as DRAFT';
COMMENT ON TABLE askdata.certified_example_versions IS 'Versioned certified question examples; only CERTIFIED rows may enter a release';
COMMENT ON TABLE askdata.kpi_bundle_versions IS 'Versioned governed KPI answer bundles; imports never bypass certification';
COMMENT ON TABLE askdata.evaluation_case_versions IS 'Governed imported evaluation definitions before sealed-set materialization';
COMMENT ON FUNCTION askdata.resolve_governed_import_member(uuid,uuid,text) IS
  'Admin/worker-only exact member resolver for governance imports; returns opaque IDs and never member labels, values or aliases';
