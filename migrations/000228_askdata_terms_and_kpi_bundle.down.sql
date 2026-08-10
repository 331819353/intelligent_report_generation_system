DO $rollback_guard$
BEGIN
  IF EXISTS(
    SELECT 1 FROM askdata.release_objects
    WHERE object_type IN ('METRIC_DIMENSION','KPI_BUNDLE','EVAL_CASE')
  ) THEN
    RAISE EXCEPTION 'cannot roll back 000228 while releases reference governed import objects';
  END IF;
END
$rollback_guard$;

ALTER TABLE askdata.release_objects
  DROP CONSTRAINT release_objects_object_type_check,
  ADD CONSTRAINT release_objects_object_type_check CHECK(object_type IN (
    'DOMAIN','ENTITY','SEMANTIC_MODEL','MEASURE','METRIC','DIMENSION','MEMBER',
    'HIERARCHY','RELATIONSHIP','QUALITY_RULE','BUSINESS_TERM',
    'CERTIFIED_EXAMPLE','TIME_CONTRACT'
  ));

DROP TRIGGER askdata_release_objects_validate ON askdata.release_objects;
DROP FUNCTION askdata.validate_release_object();

ALTER TABLE askdata.relationships
  DROP CONSTRAINT askdata_relationships_validity_check,
  DROP COLUMN valid_to,
  DROP COLUMN valid_from;

ALTER TABLE askdata.dimensions
  DROP COLUMN sortable,
  DROP COLUMN filterable,
  DROP COLUMN groupable;

ALTER TABLE askdata.metric_versions
  DROP COLUMN negative_examples,
  DROP COLUMN positive_examples,
  DROP COLUMN dedup_key;

ALTER TABLE askdata.measures DROP COLUMN null_policy;

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

CREATE TABLE askdata.business_terms_legacy_000228(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id) ON DELETE RESTRICT,
  domain_id uuid NOT NULL,
  term_id uuid NOT NULL,
  version_no integer NOT NULL CHECK(version_no>0),
  code citext NOT NULL,
  name text NOT NULL CHECK(length(btrim(name)) BETWEEN 1 AND 200),
  definition text NOT NULL CHECK(length(btrim(definition)) BETWEEN 1 AND 4000),
  aliases text[] NOT NULL DEFAULT '{}'::text[] CHECK(cardinality(aliases)<=64),
  status text NOT NULL DEFAULT 'DRAFT' CHECK(status IN ('DRAFT','CERTIFIED','DEPRECATED')),
  content_hash text NOT NULL CHECK(content_hash ~ '^[0-9a-f]{64}$'),
  owner_id uuid NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT askdata_business_terms_legacy_identity_key UNIQUE(tenant_id,term_id,version_no),
  CONSTRAINT askdata_business_terms_legacy_identity_tenant_key UNIQUE(id,tenant_id),
  CONSTRAINT askdata_business_terms_legacy_identity_domain_tenant_key UNIQUE(id,domain_id,tenant_id),
  CONSTRAINT askdata_business_terms_legacy_domain_fk
    FOREIGN KEY(domain_id,tenant_id) REFERENCES askdata.domains(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT askdata_business_terms_legacy_owner_fk
    FOREIGN KEY(owner_id,tenant_id) REFERENCES platform.users(id,tenant_id) ON DELETE RESTRICT
);

INSERT INTO askdata.business_terms_legacy_000228(
  id,tenant_id,domain_id,term_id,version_no,code,name,definition,aliases,
  status,content_hash,owner_id,created_at,updated_at
)
SELECT id,tenant_id,domain_id,business_term_id,version_no,code,name,
  CASE WHEN btrim(definition)='' THEN name ELSE definition END,
  aliases,status,content_hash,owner_id,created_at,updated_at
FROM askdata.business_term_versions;

DROP TABLE askdata.evaluation_case_versions;
DROP TABLE askdata.evaluation_case_assets;
DROP TABLE askdata.kpi_bundle_versions;
DROP TABLE askdata.kpi_bundles;
DROP TABLE askdata.certified_example_versions;
DROP TABLE askdata.certified_examples;
DROP TABLE askdata.metric_dimension_versions;
DROP TABLE askdata.metric_dimensions;
DROP TABLE askdata.business_term_versions;
DROP TABLE askdata.business_terms;

ALTER TABLE askdata.business_terms_legacy_000228 RENAME TO business_terms;

CREATE TRIGGER askdata_business_terms_set_updated_at
BEFORE UPDATE ON askdata.business_terms
FOR EACH ROW EXECUTE FUNCTION askdata.set_updated_at();
CREATE TRIGGER askdata_business_terms_protect_certified
BEFORE UPDATE OR DELETE ON askdata.business_terms
FOR EACH ROW EXECUTE FUNCTION askdata.protect_certified_version();
CREATE INDEX askdata_business_terms_lookup_idx
  ON askdata.business_terms(tenant_id,domain_id,status,code);
CREATE INDEX askdata_business_terms_aliases_idx
  ON askdata.business_terms USING gin(aliases);

ALTER TABLE askdata.business_terms ENABLE ROW LEVEL SECURITY;
ALTER TABLE askdata.business_terms FORCE ROW LEVEL SECURITY;
CREATE POLICY askdata_business_terms_domain_isolation
  ON askdata.business_terms
  USING(askdata.tenant_matches(tenant_id) AND askdata.domain_can_access(domain_id))
  WITH CHECK(askdata.tenant_matches(tenant_id) AND askdata.domain_can_access(domain_id));

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
  SELECT domain_id INTO release_domain_id FROM askdata.releases
  WHERE tenant_id=NEW.tenant_id AND id=NEW.release_id AND status='DRAFT';
  IF release_domain_id IS NULL OR release_domain_id<>NEW.domain_id THEN
    RAISE EXCEPTION 'release object requires a DRAFT release in the same domain' USING ERRCODE='23514';
  END IF;
  CASE NEW.object_type
    WHEN 'DOMAIN' THEN SELECT true,id,NULL INTO source_valid,source_object_id,source_hash FROM askdata.domains WHERE tenant_id=NEW.tenant_id AND id=NEW.object_version_id AND status='ACTIVE';
    WHEN 'ENTITY' THEN SELECT true,entity_id,content_hash INTO source_valid,source_object_id,source_hash FROM askdata.entities WHERE tenant_id=NEW.tenant_id AND domain_id=NEW.domain_id AND id=NEW.object_version_id AND status='CERTIFIED';
    WHEN 'SEMANTIC_MODEL' THEN
      SELECT true,model.model_id,model.content_hash INTO source_valid,source_object_id,source_hash
      FROM askdata.semantic_models AS model
      JOIN platform.datasets AS dataset ON dataset.id=model.dataset_id AND dataset.tenant_id=model.tenant_id
      JOIN platform.dataset_versions AS version ON version.id=model.dataset_version_id AND version.dataset_id=dataset.id AND version.tenant_id=dataset.tenant_id
      JOIN platform.dataset_materializations AS materialization ON materialization.id=model.materialization_id AND materialization.dataset_id=dataset.id AND materialization.dataset_version_id=version.id AND materialization.tenant_id=dataset.tenant_id
      WHERE model.tenant_id=NEW.tenant_id AND model.domain_id=NEW.domain_id AND model.id=NEW.object_version_id AND model.status='CERTIFIED'
        AND model.time_contract_version_id IS NOT NULL AND dataset.domain_id=NEW.domain_id AND dataset.deleted_at IS NULL
        AND dataset.status='PUBLISHED' AND dataset.current_published_version_id=version.id
        AND version.status='PUBLISHED' AND version.layer IN ('DWS','ADS') AND version.layer=model.layer
        AND version.schema_hash=model.dataset_schema_hash AND materialization.status='ACTIVE'
        AND materialization.layer=version.layer AND materialization.schema_hash=version.schema_hash;
    WHEN 'MEASURE' THEN SELECT true,measure_id,content_hash INTO source_valid,source_object_id,source_hash FROM askdata.measures WHERE tenant_id=NEW.tenant_id AND domain_id=NEW.domain_id AND id=NEW.object_version_id AND status='CERTIFIED';
    WHEN 'METRIC' THEN SELECT true,metric_id,content_hash INTO source_valid,source_object_id,source_hash FROM askdata.metric_versions WHERE tenant_id=NEW.tenant_id AND domain_id=NEW.domain_id AND id=NEW.object_version_id AND status='CERTIFIED';
    WHEN 'DIMENSION' THEN SELECT true,dimension_id,content_hash INTO source_valid,source_object_id,source_hash FROM askdata.dimensions WHERE tenant_id=NEW.tenant_id AND domain_id=NEW.domain_id AND id=NEW.object_version_id AND status='CERTIFIED';
    WHEN 'MEMBER' THEN SELECT true,member_id,content_hash INTO source_valid,source_object_id,source_hash FROM askdata.dimension_members WHERE tenant_id=NEW.tenant_id AND domain_id=NEW.domain_id AND id=NEW.object_version_id AND status='CERTIFIED';
    WHEN 'HIERARCHY' THEN SELECT true,hierarchy_id,content_hash INTO source_valid,source_object_id,source_hash FROM askdata.hierarchies WHERE tenant_id=NEW.tenant_id AND domain_id=NEW.domain_id AND id=NEW.object_version_id AND status='CERTIFIED';
    WHEN 'RELATIONSHIP' THEN SELECT true,relationship_id,content_hash INTO source_valid,source_object_id,source_hash FROM askdata.relationships WHERE tenant_id=NEW.tenant_id AND domain_id=NEW.domain_id AND id=NEW.object_version_id AND status='CERTIFIED';
    WHEN 'QUALITY_RULE' THEN SELECT true,quality_rule_id,content_hash INTO source_valid,source_object_id,source_hash FROM askdata.quality_rules WHERE tenant_id=NEW.tenant_id AND domain_id=NEW.domain_id AND id=NEW.object_version_id AND status='CERTIFIED';
    WHEN 'BUSINESS_TERM' THEN SELECT true,term_id,content_hash INTO source_valid,source_object_id,source_hash FROM askdata.business_terms WHERE tenant_id=NEW.tenant_id AND domain_id=NEW.domain_id AND id=NEW.object_version_id AND status='CERTIFIED';
    WHEN 'TIME_CONTRACT' THEN
      SELECT true,contract.time_contract_id,contract.content_hash INTO source_valid,source_object_id,source_hash
      FROM askdata.time_contract_versions AS contract
      WHERE contract.tenant_id=NEW.tenant_id AND contract.domain_id=NEW.domain_id
        AND contract.id=NEW.object_version_id AND contract.status='CERTIFIED'
        AND (contract.calendar_dataset_version_id IS NULL OR EXISTS(
          SELECT 1 FROM platform.dataset_versions AS version
          JOIN platform.datasets AS dataset ON dataset.id=version.dataset_id AND dataset.tenant_id=version.tenant_id
          WHERE version.id=contract.calendar_dataset_version_id AND version.tenant_id=contract.tenant_id
            AND version.status='PUBLISHED' AND dataset.domain_id=contract.domain_id
            AND dataset.status='PUBLISHED' AND dataset.deleted_at IS NULL
            AND dataset.current_published_version_id=version.id));
    WHEN 'CERTIFIED_EXAMPLE' THEN source_valid := false;
  END CASE;
  IF NOT COALESCE(source_valid,false) OR source_object_id<>NEW.object_id
    OR (source_hash IS NOT NULL AND source_hash<>NEW.content_hash) THEN
    RAISE EXCEPTION 'release object does not match a certified immutable source' USING ERRCODE='23514';
  END IF;
  RETURN NEW;
END
$$;

REVOKE ALL ON FUNCTION askdata.validate_release_object() FROM PUBLIC;
CREATE TRIGGER askdata_release_objects_validate
BEFORE INSERT OR UPDATE ON askdata.release_objects
FOR EACH ROW EXECUTE FUNCTION askdata.validate_release_object();

CREATE OR REPLACE FUNCTION askdata.validate_search_document_subject()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,askdata
AS $$
DECLARE subject_valid boolean := false;
BEGIN
  CASE NEW.object_type
    WHEN 'ENTITY' THEN SELECT EXISTS(SELECT 1 FROM askdata.entities WHERE tenant_id=NEW.tenant_id AND domain_id=NEW.domain_id AND id=NEW.object_version_id AND status='CERTIFIED') INTO subject_valid;
    WHEN 'SEMANTIC_MODEL' THEN SELECT EXISTS(SELECT 1 FROM askdata.semantic_models WHERE tenant_id=NEW.tenant_id AND domain_id=NEW.domain_id AND id=NEW.object_version_id AND status='CERTIFIED') INTO subject_valid;
    WHEN 'MEASURE' THEN SELECT EXISTS(SELECT 1 FROM askdata.measures WHERE tenant_id=NEW.tenant_id AND domain_id=NEW.domain_id AND id=NEW.object_version_id AND status='CERTIFIED') INTO subject_valid;
    WHEN 'METRIC' THEN SELECT EXISTS(SELECT 1 FROM askdata.metric_versions WHERE tenant_id=NEW.tenant_id AND domain_id=NEW.domain_id AND id=NEW.object_version_id AND status='CERTIFIED') INTO subject_valid;
    WHEN 'DIMENSION' THEN SELECT EXISTS(SELECT 1 FROM askdata.dimensions WHERE tenant_id=NEW.tenant_id AND domain_id=NEW.domain_id AND id=NEW.object_version_id AND status='CERTIFIED' AND sensitivity<>'RESTRICTED' AND sensitivity=NEW.sensitivity) INTO subject_valid;
    WHEN 'MEMBER' THEN SELECT EXISTS(SELECT 1 FROM askdata.dimension_members AS member JOIN askdata.dimensions AS dimension ON dimension.id=member.dimension_version_id AND dimension.tenant_id=member.tenant_id WHERE member.tenant_id=NEW.tenant_id AND member.domain_id=NEW.domain_id AND member.id=NEW.object_version_id AND member.status='CERTIFIED' AND member.sensitivity IN ('PUBLIC','INTERNAL') AND dimension.sensitivity IN ('PUBLIC','INTERNAL') AND dimension.member_index_policy='FULL' AND NOT dimension.high_cardinality AND NEW.sensitivity=CASE WHEN member.sensitivity='INTERNAL' OR dimension.sensitivity='INTERNAL' THEN 'INTERNAL' ELSE 'PUBLIC' END) INTO subject_valid;
    WHEN 'BUSINESS_TERM' THEN SELECT EXISTS(SELECT 1 FROM askdata.business_terms WHERE tenant_id=NEW.tenant_id AND domain_id=NEW.domain_id AND id=NEW.object_version_id AND status='CERTIFIED') INTO subject_valid;
    WHEN 'RELATIONSHIP' THEN SELECT EXISTS(SELECT 1 FROM askdata.relationships WHERE tenant_id=NEW.tenant_id AND domain_id=NEW.domain_id AND id=NEW.object_version_id AND status='CERTIFIED') INTO subject_valid;
    WHEN 'CERTIFIED_EXAMPLE' THEN subject_valid := false;
  END CASE;
  IF NOT subject_valid THEN RAISE EXCEPTION 'search document subject is not a certified indexable object' USING ERRCODE='23514'; END IF;
  RETURN NEW;
END
$$;

CREATE OR REPLACE FUNCTION askdata.validate_semantic_alias_subject()
RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE subject_valid boolean := false;
BEGIN
  CASE NEW.object_type
    WHEN 'ENTITY' THEN SELECT EXISTS(SELECT 1 FROM askdata.entities WHERE id=NEW.object_version_id AND domain_id=NEW.domain_id AND tenant_id=NEW.tenant_id AND status=CASE WHEN NEW.status='CERTIFIED' THEN 'CERTIFIED' ELSE status END AND status<>'DEPRECATED') INTO subject_valid;
    WHEN 'SEMANTIC_MODEL' THEN SELECT EXISTS(SELECT 1 FROM askdata.semantic_models WHERE id=NEW.object_version_id AND domain_id=NEW.domain_id AND tenant_id=NEW.tenant_id AND status=CASE WHEN NEW.status='CERTIFIED' THEN 'CERTIFIED' ELSE status END AND status<>'DEPRECATED') INTO subject_valid;
    WHEN 'MEASURE' THEN SELECT EXISTS(SELECT 1 FROM askdata.measures WHERE id=NEW.object_version_id AND domain_id=NEW.domain_id AND tenant_id=NEW.tenant_id AND status=CASE WHEN NEW.status='CERTIFIED' THEN 'CERTIFIED' ELSE status END AND status<>'DEPRECATED') INTO subject_valid;
    WHEN 'METRIC' THEN SELECT EXISTS(SELECT 1 FROM askdata.metric_versions WHERE id=NEW.object_version_id AND domain_id=NEW.domain_id AND tenant_id=NEW.tenant_id AND status=CASE WHEN NEW.status='CERTIFIED' THEN 'CERTIFIED' ELSE status END AND status<>'DEPRECATED') INTO subject_valid;
    WHEN 'DIMENSION' THEN SELECT EXISTS(SELECT 1 FROM askdata.dimensions WHERE id=NEW.object_version_id AND domain_id=NEW.domain_id AND tenant_id=NEW.tenant_id AND status=CASE WHEN NEW.status='CERTIFIED' THEN 'CERTIFIED' ELSE status END AND status<>'DEPRECATED') INTO subject_valid;
    WHEN 'BUSINESS_TERM' THEN SELECT EXISTS(SELECT 1 FROM askdata.business_terms WHERE id=NEW.object_version_id AND domain_id=NEW.domain_id AND tenant_id=NEW.tenant_id AND status=CASE WHEN NEW.status='CERTIFIED' THEN 'CERTIFIED' ELSE status END AND status<>'DEPRECATED') INTO subject_valid;
  END CASE;
  IF NOT COALESCE(subject_valid,false) THEN RAISE EXCEPTION 'semantic alias subject is missing, deprecated, cross-domain, or uncertified' USING ERRCODE='23514'; END IF;
  RETURN NEW;
END
$$;

DROP FUNCTION IF EXISTS askdata.resolve_governed_import_member(uuid,uuid,text);
DROP FUNCTION askdata.validate_governed_import_version();
