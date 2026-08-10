DO $query_008_rollback_guard$
BEGIN
  IF EXISTS(
    SELECT 1 FROM askdata.relationships
    WHERE cardinality IS NULL OR fanout_policy IS NULL
       OR fanout_policy='BRIDGE_REQUIRED'
  ) THEN
    RAISE EXCEPTION 'cannot restore legacy relationship enums while unresolved or bridge-required relationships exist';
  END IF;
END
$query_008_rollback_guard$;

ALTER TABLE askdata.relationships
  DROP CONSTRAINT rel_bridge_required,
  DROP CONSTRAINT rel_combination_valid,
  DROP CONSTRAINT rel_fanout_enum,
  DROP CONSTRAINT rel_cardinality_enum,
  DROP CONSTRAINT askdata_relationships_bridge_model_fk;

UPDATE askdata.relationships
SET fanout_policy='CERTIFIED_PREAGG'
WHERE fanout_policy='PRE_AGGREGATE_REQUIRED';

ALTER TABLE askdata.relationships
  DROP COLUMN bridge_model_version_id,
  ALTER COLUMN cardinality SET NOT NULL,
  ALTER COLUMN fanout_policy SET NOT NULL,
  ALTER COLUMN fanout_policy SET DEFAULT 'BLOCK',
  ADD CONSTRAINT relationships_cardinality_check CHECK(
    cardinality IN ('ONE_TO_ONE','MANY_TO_ONE','ONE_TO_MANY','MANY_TO_MANY')
  ),
  ADD CONSTRAINT relationships_fanout_policy_check CHECK(
    fanout_policy IN ('BLOCK','CERTIFIED_PREAGG','SAFE')
  );

DO $$
BEGIN
  IF EXISTS(SELECT 1 FROM askdata.measures WHERE additivity IS NULL)
     OR EXISTS(SELECT 1 FROM askdata.metric_versions WHERE additivity IS NULL) THEN
    RAISE EXCEPTION 'cannot restore legacy required additivity while unconfirmed rows exist';
  END IF;
END $$;

ALTER TABLE askdata.measures
  DROP CONSTRAINT askdata_measures_additivity_confirmer_fk,
  DROP CONSTRAINT askdata_measures_certified_requires_additivity,
  DROP CONSTRAINT askdata_measures_currency_check,
  DROP CONSTRAINT askdata_measures_confirmation_pair_check,
  DROP CONSTRAINT askdata_measures_additivity_suggestion_check,
  DROP CONSTRAINT askdata_measures_display_precision_check,
  DROP CONSTRAINT askdata_measures_zero_denominator_check,
  DROP CONSTRAINT askdata_measures_aggregation_restriction_enum,
  DROP CONSTRAINT askdata_measures_non_additive_restriction,
  DROP CONSTRAINT askdata_measures_semi_additive_agg,
  DROP CONSTRAINT askdata_measures_additivity_enum,
  ADD COLUMN legacy_additivity text;

ALTER TABLE askdata.metric_versions
  DROP CONSTRAINT askdata_metric_versions_additivity_confirmer_fk,
  DROP CONSTRAINT askdata_metric_versions_certified_requires_additivity,
  DROP CONSTRAINT askdata_metric_versions_currency_check,
  DROP CONSTRAINT askdata_metric_versions_confirmation_pair_check,
  DROP CONSTRAINT askdata_metric_versions_additivity_suggestion_check,
  DROP CONSTRAINT askdata_metric_versions_display_precision_check,
  DROP CONSTRAINT askdata_metric_versions_zero_denominator_check,
  DROP CONSTRAINT askdata_metric_versions_aggregation_restriction_enum,
  DROP CONSTRAINT askdata_metric_versions_non_additive_restriction,
  DROP CONSTRAINT askdata_metric_versions_semi_additive_agg,
  DROP CONSTRAINT askdata_metric_versions_additivity_enum,
  ADD COLUMN legacy_additivity text;

UPDATE askdata.measures SET legacy_additivity=CASE additivity
  WHEN 'FULLY_ADDITIVE' THEN 'ADDITIVE'
  WHEN 'SEMI_ADDITIVE' THEN 'SEMI_ADDITIVE'
  WHEN 'NON_ADDITIVE' THEN 'NON_ADDITIVE'
END;

UPDATE askdata.metric_versions SET legacy_additivity=CASE additivity
  WHEN 'FULLY_ADDITIVE' THEN 'ADDITIVE'
  WHEN 'SEMI_ADDITIVE' THEN 'SEMI_ADDITIVE'
  WHEN 'NON_ADDITIVE' THEN 'NON_ADDITIVE'
END;

ALTER TABLE askdata.measures
  DROP COLUMN additivity,
  DROP COLUMN semi_additive_time_aggregation,
  DROP COLUMN aggregation_restriction,
  DROP COLUMN non_additive_dimensions,
  DROP COLUMN currency,
  DROP COLUMN zero_denominator_policy,
  DROP COLUMN display_precision,
  DROP COLUMN additivity_suggestion,
  DROP COLUMN additivity_confirmed_by,
  DROP COLUMN additivity_confirmed_at;
ALTER TABLE askdata.measures
  RENAME COLUMN legacy_additivity TO additivity;

ALTER TABLE askdata.metric_versions
  DROP COLUMN additivity,
  DROP COLUMN semi_additive_time_aggregation,
  DROP COLUMN aggregation_restriction,
  DROP COLUMN non_additive_dimensions,
  DROP COLUMN currency,
  DROP COLUMN zero_denominator_policy,
  DROP COLUMN display_precision,
  DROP COLUMN additivity_suggestion,
  DROP COLUMN additivity_confirmed_by,
  DROP COLUMN additivity_confirmed_at;
ALTER TABLE askdata.metric_versions
  RENAME COLUMN legacy_additivity TO additivity;

ALTER TABLE askdata.measures
  ALTER COLUMN additivity SET NOT NULL,
  ADD CONSTRAINT measures_additivity_check CHECK(
    additivity IN ('ADDITIVE','SEMI_ADDITIVE','NON_ADDITIVE')
  );

ALTER TABLE askdata.metric_versions
  ALTER COLUMN additivity SET NOT NULL,
  ADD CONSTRAINT metric_versions_additivity_check CHECK(
    additivity IN ('ADDITIVE','SEMI_ADDITIVE','NON_ADDITIVE')
  );
