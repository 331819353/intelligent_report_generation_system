DROP TRIGGER IF EXISTS askdata_releases_00_validate_time_contract_closure
  ON askdata.releases;
DROP FUNCTION IF EXISTS askdata.validate_release_time_contract_closure();

DO $rollback_guard$
BEGIN
  IF EXISTS(
    SELECT 1 FROM askdata.release_objects
    WHERE object_type='TIME_CONTRACT'
  ) THEN
    RAISE EXCEPTION 'cannot roll back 000225 while releases reference time contracts';
  END IF;
END
$rollback_guard$;

ALTER TABLE askdata.release_objects
  DROP CONSTRAINT release_objects_object_type_check,
  ADD CONSTRAINT release_objects_object_type_check CHECK(object_type IN (
    'DOMAIN','ENTITY','SEMANTIC_MODEL','MEASURE','METRIC','DIMENSION','MEMBER',
    'HIERARCHY','RELATIONSHIP','QUALITY_RULE','BUSINESS_TERM','CERTIFIED_EXAMPLE'
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

DROP TRIGGER IF EXISTS askdata_semantic_models_00_validate_time_contract
  ON askdata.semantic_models;
DROP TRIGGER IF EXISTS askdata_time_contract_versions_set_updated_at
  ON askdata.time_contract_versions;
DROP TRIGGER IF EXISTS askdata_time_contract_versions_10_protect_certified
  ON askdata.time_contract_versions;
DROP TRIGGER IF EXISTS askdata_time_contract_versions_00_validate
  ON askdata.time_contract_versions;
DROP TRIGGER IF EXISTS askdata_time_contracts_set_updated_at
  ON askdata.time_contracts;

DROP FUNCTION IF EXISTS askdata.validate_semantic_model_time_contract();
DROP FUNCTION IF EXISTS askdata.protect_time_contract_version();
DROP FUNCTION IF EXISTS askdata.validate_time_contract_version();

ALTER TABLE askdata.semantic_models
  DROP CONSTRAINT askdata_semantic_models_time_contract_fk,
  DROP COLUMN time_contract_version_id;

ALTER TABLE askdata.metric_versions
  DROP CONSTRAINT askdata_metric_versions_incomplete_period_policy_check,
  DROP COLUMN incomplete_period_policy_override;

ALTER TABLE askdata.domains
  DROP CONSTRAINT askdata_domains_incomplete_period_policy_check,
  DROP COLUMN default_incomplete_period_policy;

DROP TABLE askdata.time_contract_versions;
DROP TABLE askdata.time_contracts;
