-- Versioned time contracts turn incomplete-period policy and fiscal calendar
-- dependencies into immutable semantic facts. No enterprise calendar values
-- are seeded here; platform default MTD is resolved by the application.
CREATE TABLE askdata.time_contracts(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id) ON DELETE RESTRICT,
  domain_id uuid NOT NULL,
  code citext NOT NULL,
  name text NOT NULL CHECK(length(btrim(name)) BETWEEN 1 AND 200),
  owner_user_id uuid NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT askdata_time_contracts_identity_tenant_key UNIQUE(id,tenant_id),
  CONSTRAINT askdata_time_contracts_identity_domain_tenant_key UNIQUE(id,domain_id,tenant_id),
  CONSTRAINT askdata_time_contracts_code_key UNIQUE(tenant_id,domain_id,code),
  CONSTRAINT askdata_time_contracts_domain_fk
    FOREIGN KEY(domain_id,tenant_id)
    REFERENCES askdata.domains(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT askdata_time_contracts_owner_fk
    FOREIGN KEY(owner_user_id,tenant_id)
    REFERENCES platform.users(id,tenant_id) ON DELETE RESTRICT
);

CREATE TABLE askdata.time_contract_versions(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id) ON DELETE RESTRICT,
  domain_id uuid NOT NULL,
  time_contract_id uuid NOT NULL,
  version_no integer NOT NULL CHECK(version_no>0),
  status text NOT NULL DEFAULT 'DRAFT'
    CHECK(status IN ('DRAFT','CERTIFIED','DEPRECATED')),
  timezone text NOT NULL CHECK(
    length(timezone) BETWEEN 1 AND 64
    AND timezone=btrim(timezone)
    AND timezone !~ '[[:cntrl:]]'
  ),
  week_start text NOT NULL CHECK(week_start IN ('MONDAY','SUNDAY')),
  week_numbering text NOT NULL CHECK(week_numbering IN ('ISO','US')),
  fiscal_year_start_month smallint NOT NULL
    CHECK(fiscal_year_start_month BETWEEN 1 AND 12),
  fiscal_month_rule text NOT NULL
    CHECK(fiscal_month_rule IN ('CALENDAR','FOUR_FOUR_FIVE','CUSTOM_TABLE')),
  incomplete_period_policy text
    CHECK(incomplete_period_policy IN ('MTD','FULL_PERIOD','LAST_COMPLETE')),
  comparison_alignment text NOT NULL
    CHECK(comparison_alignment IN ('SAME_DAY_COUNT','SAME_CALENDAR_RANGE')),
  month_end_overflow_rule text NOT NULL
    CHECK(month_end_overflow_rule IN ('CLAMP_TO_LAST_DAY','SKIP')),
  supported_grains text[] NOT NULL CHECK(
    cardinality(supported_grains) BETWEEN 1 AND 8
    AND array_position(supported_grains,NULL) IS NULL
    AND supported_grains <@ ARRAY[
      'DAY','WEEK','MONTH','QUARTER','YEAR',
      'FISCAL_MONTH','FISCAL_QUARTER','FISCAL_YEAR'
    ]::text[]
  ),
  data_available_through_expr text NOT NULL CHECK(
    length(data_available_through_expr) BETWEEN 1 AND 512
    AND data_available_through_expr=btrim(data_available_through_expr)
    AND data_available_through_expr !~ '[[:cntrl:]]'
  ),
  expected_lag_hours integer NOT NULL CHECK(expected_lag_hours>=0),
  calendar_dataset_version_id uuid,
  content_hash text NOT NULL CHECK(content_hash ~ '^[0-9a-f]{64}$'),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT askdata_time_contract_versions_identity_key
    UNIQUE(tenant_id,time_contract_id,version_no),
  CONSTRAINT askdata_time_contract_versions_identity_tenant_key
    UNIQUE(id,tenant_id),
  CONSTRAINT askdata_time_contract_versions_identity_domain_tenant_key
    UNIQUE(id,domain_id,tenant_id),
  CONSTRAINT askdata_time_contract_versions_contract_fk
    FOREIGN KEY(time_contract_id,domain_id,tenant_id)
    REFERENCES askdata.time_contracts(id,domain_id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT askdata_time_contract_versions_domain_fk
    FOREIGN KEY(domain_id,tenant_id)
    REFERENCES askdata.domains(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT askdata_time_contract_versions_calendar_fk
    FOREIGN KEY(calendar_dataset_version_id,tenant_id)
    REFERENCES platform.dataset_versions(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT askdata_time_contract_versions_calendar_shape_check CHECK(
    fiscal_month_rule='CALENDAR' OR calendar_dataset_version_id IS NOT NULL
  )
);

ALTER TABLE askdata.semantic_models
  ADD COLUMN time_contract_version_id uuid,
  ADD CONSTRAINT askdata_semantic_models_time_contract_fk
    FOREIGN KEY(time_contract_version_id,domain_id,tenant_id)
    REFERENCES askdata.time_contract_versions(id,domain_id,tenant_id)
    ON DELETE RESTRICT;

ALTER TABLE askdata.domains
  ADD COLUMN default_incomplete_period_policy text,
  ADD CONSTRAINT askdata_domains_incomplete_period_policy_check CHECK(
    default_incomplete_period_policy IS NULL
    OR default_incomplete_period_policy IN ('MTD','FULL_PERIOD','LAST_COMPLETE')
  );

ALTER TABLE askdata.metric_versions
  ADD COLUMN incomplete_period_policy_override text,
  ADD CONSTRAINT askdata_metric_versions_incomplete_period_policy_check CHECK(
    incomplete_period_policy_override IS NULL
    OR incomplete_period_policy_override IN ('MTD','FULL_PERIOD','LAST_COMPLETE')
  );

CREATE OR REPLACE FUNCTION askdata.validate_time_contract_version()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,askdata,platform
AS $$
DECLARE distinct_grain_count integer;
DECLARE calendar_is_active boolean := false;
BEGIN
  SELECT count(DISTINCT grain) INTO distinct_grain_count
  FROM unnest(NEW.supported_grains) AS grain;
  IF distinct_grain_count<>cardinality(NEW.supported_grains) THEN
    RAISE EXCEPTION 'TIME_UNSUPPORTED_GRAIN: supported_grains contains duplicates'
      USING ERRCODE='23514';
  END IF;
  IF (
    NEW.fiscal_month_rule<>'CALENDAR'
    OR NEW.supported_grains && ARRAY[
      'FISCAL_MONTH','FISCAL_QUARTER','FISCAL_YEAR'
    ]::text[]
  ) AND NEW.calendar_dataset_version_id IS NULL THEN
    RAISE EXCEPTION 'TIME_CALENDAR_REQUIRED: fiscal rules require a calendar dataset version'
      USING ERRCODE='23514';
  END IF;
  IF NEW.calendar_dataset_version_id IS NOT NULL THEN
    SELECT true INTO calendar_is_active
    FROM platform.dataset_versions AS version
    JOIN platform.datasets AS dataset
      ON dataset.id=version.dataset_id AND dataset.tenant_id=version.tenant_id
    WHERE version.id=NEW.calendar_dataset_version_id
      AND version.tenant_id=NEW.tenant_id
      AND version.status='PUBLISHED'
      AND dataset.domain_id=NEW.domain_id
      AND dataset.status='PUBLISHED'
      AND dataset.deleted_at IS NULL
      AND dataset.current_published_version_id=version.id;
    IF NOT COALESCE(calendar_is_active,false) THEN
      RAISE EXCEPTION 'TIME_CALENDAR_NOT_ACTIVE: calendar dataset must be the current PUBLISHED version in the same domain'
        USING ERRCODE='23514';
    END IF;
  END IF;
  RETURN NEW;
END
$$;

CREATE OR REPLACE FUNCTION askdata.protect_time_contract_version()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
  IF OLD.status='CERTIFIED' THEN
    RAISE EXCEPTION 'TIME_CONTRACT_VERSION_IMMUTABLE: certified time contract versions cannot be changed'
      USING ERRCODE='55000';
  END IF;
  RETURN CASE WHEN TG_OP='DELETE' THEN OLD ELSE NEW END;
END
$$;

CREATE OR REPLACE FUNCTION askdata.validate_semantic_model_time_contract()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,askdata
AS $$
BEGIN
  IF NEW.status='CERTIFIED' AND (
    NEW.time_contract_version_id IS NULL
    OR NOT EXISTS(
      SELECT 1
      FROM askdata.time_contract_versions AS contract
      WHERE contract.id=NEW.time_contract_version_id
        AND contract.tenant_id=NEW.tenant_id
        AND contract.domain_id=NEW.domain_id
        AND contract.status='CERTIFIED'
    )
  ) THEN
    RAISE EXCEPTION 'TIME_CONTRACT_MISSING: certified semantic model requires a certified time contract version in the same domain'
      USING ERRCODE='23514';
  END IF;
  RETURN NEW;
END
$$;

REVOKE ALL ON FUNCTION
  askdata.validate_time_contract_version(),
  askdata.protect_time_contract_version(),
  askdata.validate_semantic_model_time_contract()
FROM PUBLIC;

CREATE TRIGGER askdata_time_contracts_set_updated_at
BEFORE UPDATE ON askdata.time_contracts
FOR EACH ROW EXECUTE FUNCTION askdata.set_updated_at();
CREATE TRIGGER askdata_time_contract_versions_00_validate
BEFORE INSERT OR UPDATE ON askdata.time_contract_versions
FOR EACH ROW EXECUTE FUNCTION askdata.validate_time_contract_version();
CREATE TRIGGER askdata_time_contract_versions_10_protect_certified
BEFORE UPDATE OR DELETE ON askdata.time_contract_versions
FOR EACH ROW EXECUTE FUNCTION askdata.protect_time_contract_version();
CREATE TRIGGER askdata_time_contract_versions_set_updated_at
BEFORE UPDATE ON askdata.time_contract_versions
FOR EACH ROW EXECUTE FUNCTION askdata.set_updated_at();
CREATE TRIGGER askdata_semantic_models_00_validate_time_contract
BEFORE INSERT OR UPDATE ON askdata.semantic_models
FOR EACH ROW EXECUTE FUNCTION askdata.validate_semantic_model_time_contract();

ALTER TABLE askdata.release_objects
  DROP CONSTRAINT release_objects_object_type_check,
  ADD CONSTRAINT release_objects_object_type_check CHECK(object_type IN (
    'DOMAIN','ENTITY','SEMANTIC_MODEL','MEASURE','METRIC','DIMENSION','MEMBER',
    'HIERARCHY','RELATIONSHIP','QUALITY_RULE','BUSINESS_TERM',
    'CERTIFIED_EXAMPLE','TIME_CONTRACT'
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
      FROM askdata.measures
      WHERE tenant_id=NEW.tenant_id AND domain_id=NEW.domain_id
        AND id=NEW.object_version_id AND status='CERTIFIED';
    WHEN 'METRIC' THEN
      SELECT true,metric_id,content_hash INTO source_valid,source_object_id,source_hash
      FROM askdata.metric_versions
      WHERE tenant_id=NEW.tenant_id AND domain_id=NEW.domain_id
        AND id=NEW.object_version_id AND status='CERTIFIED';
    WHEN 'DIMENSION' THEN
      SELECT true,dimension_id,content_hash INTO source_valid,source_object_id,source_hash
      FROM askdata.dimensions
      WHERE tenant_id=NEW.tenant_id AND domain_id=NEW.domain_id
        AND id=NEW.object_version_id AND status='CERTIFIED';
    WHEN 'MEMBER' THEN
      SELECT true,member_id,content_hash INTO source_valid,source_object_id,source_hash
      FROM askdata.dimension_members
      WHERE tenant_id=NEW.tenant_id AND domain_id=NEW.domain_id
        AND id=NEW.object_version_id AND status='CERTIFIED';
    WHEN 'HIERARCHY' THEN
      SELECT true,hierarchy_id,content_hash INTO source_valid,source_object_id,source_hash
      FROM askdata.hierarchies
      WHERE tenant_id=NEW.tenant_id AND domain_id=NEW.domain_id
        AND id=NEW.object_version_id AND status='CERTIFIED';
    WHEN 'RELATIONSHIP' THEN
      SELECT true,relationship_id,content_hash INTO source_valid,source_object_id,source_hash
      FROM askdata.relationships
      WHERE tenant_id=NEW.tenant_id AND domain_id=NEW.domain_id
        AND id=NEW.object_version_id AND status='CERTIFIED';
    WHEN 'QUALITY_RULE' THEN
      SELECT true,quality_rule_id,content_hash INTO source_valid,source_object_id,source_hash
      FROM askdata.quality_rules
      WHERE tenant_id=NEW.tenant_id AND domain_id=NEW.domain_id
        AND id=NEW.object_version_id AND status='CERTIFIED';
    WHEN 'BUSINESS_TERM' THEN
      SELECT true,term_id,content_hash INTO source_valid,source_object_id,source_hash
      FROM askdata.business_terms
      WHERE tenant_id=NEW.tenant_id AND domain_id=NEW.domain_id
        AND id=NEW.object_version_id AND status='CERTIFIED';
    WHEN 'TIME_CONTRACT' THEN
      SELECT true,contract.time_contract_id,contract.content_hash
      INTO source_valid,source_object_id,source_hash
      FROM askdata.time_contract_versions AS contract
      WHERE contract.tenant_id=NEW.tenant_id
        AND contract.domain_id=NEW.domain_id
        AND contract.id=NEW.object_version_id
        AND contract.status='CERTIFIED'
        AND (
          contract.calendar_dataset_version_id IS NULL
          OR EXISTS(
            SELECT 1
            FROM platform.dataset_versions AS version
            JOIN platform.datasets AS dataset
              ON dataset.id=version.dataset_id
             AND dataset.tenant_id=version.tenant_id
            WHERE version.id=contract.calendar_dataset_version_id
              AND version.tenant_id=contract.tenant_id
              AND version.status='PUBLISHED'
              AND dataset.domain_id=contract.domain_id
              AND dataset.status='PUBLISHED'
              AND dataset.deleted_at IS NULL
              AND dataset.current_published_version_id=version.id
          )
        );
    WHEN 'CERTIFIED_EXAMPLE' THEN
      source_valid := false;
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

CREATE OR REPLACE FUNCTION askdata.validate_release_time_contract_closure()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,askdata
AS $$
BEGIN
  IF OLD.status='DRAFT' AND NEW.status='VALIDATING' AND EXISTS(
    SELECT 1
    FROM askdata.release_objects AS model
    WHERE model.tenant_id=NEW.tenant_id
      AND model.release_id=NEW.id
      AND model.object_type='SEMANTIC_MODEL'
      AND (
        NOT model.contract_json ? 'timeContractVersionId'
        OR NOT EXISTS(
          SELECT 1
          FROM askdata.release_objects AS contract
          WHERE contract.tenant_id=model.tenant_id
            AND contract.release_id=model.release_id
            AND contract.object_type='TIME_CONTRACT'
            AND contract.object_version_id::text=
                model.contract_json->>'timeContractVersionId'
        )
      )
  ) THEN
    RAISE EXCEPTION 'TIME_CONTRACT_MISSING: release omits a semantic model time-contract dependency'
      USING ERRCODE='23514';
  END IF;
  RETURN NEW;
END
$$;

REVOKE ALL ON FUNCTION
  askdata.validate_release_object(),
  askdata.validate_release_time_contract_closure()
FROM PUBLIC;

CREATE TRIGGER askdata_releases_00_validate_time_contract_closure
BEFORE UPDATE OF status ON askdata.releases
FOR EACH ROW EXECUTE FUNCTION askdata.validate_release_time_contract_closure();

CREATE INDEX askdata_time_contracts_lookup_idx
  ON askdata.time_contracts(tenant_id,domain_id,code);
CREATE INDEX askdata_time_contract_versions_lookup_idx
  ON askdata.time_contract_versions(
    tenant_id,domain_id,time_contract_id,status,version_no DESC
  );

ALTER TABLE askdata.time_contracts ENABLE ROW LEVEL SECURITY;
ALTER TABLE askdata.time_contracts FORCE ROW LEVEL SECURITY;
CREATE POLICY askdata_time_contracts_domain_isolation
  ON askdata.time_contracts
  USING(
    askdata.tenant_matches(tenant_id)
    AND askdata.domain_can_access(domain_id)
  )
  WITH CHECK(
    askdata.tenant_matches(tenant_id)
    AND askdata.domain_can_access(domain_id)
  );

ALTER TABLE askdata.time_contract_versions ENABLE ROW LEVEL SECURITY;
ALTER TABLE askdata.time_contract_versions FORCE ROW LEVEL SECURITY;
CREATE POLICY askdata_time_contract_versions_domain_isolation
  ON askdata.time_contract_versions
  USING(
    askdata.tenant_matches(tenant_id)
    AND askdata.domain_can_access(domain_id)
  )
  WITH CHECK(
    askdata.tenant_matches(tenant_id)
    AND askdata.domain_can_access(domain_id)
  );

COMMENT ON TABLE askdata.time_contracts IS
  'Stable domain-owned time contract identities; policy changes create immutable version rows';
COMMENT ON TABLE askdata.time_contract_versions IS
  'Versioned timezone, calendar, incomplete-period and comparison alignment contracts included in semantic releases';
COMMENT ON COLUMN askdata.domains.default_incomplete_period_policy IS
  'Optional approved domain override; NULL resolves to the platform MTD default';
COMMENT ON COLUMN askdata.metric_versions.incomplete_period_policy_override IS
  'Optional approved metric override; resolution order is metric, time contract, domain, platform MTD';
