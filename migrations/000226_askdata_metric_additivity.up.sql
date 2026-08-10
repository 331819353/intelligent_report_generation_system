-- ADD-001: replace the legacy, prematurely-required additivity flag with a
-- complete, confirm-before-certification contract. Legacy values are retained
-- only as suggestions; they never become certified facts automatically.

ALTER TABLE askdata.measures
  DROP CONSTRAINT measures_additivity_check;
ALTER TABLE askdata.measures
  RENAME COLUMN additivity TO legacy_additivity;

ALTER TABLE askdata.metric_versions
  DROP CONSTRAINT metric_versions_additivity_check;
ALTER TABLE askdata.metric_versions
  RENAME COLUMN additivity TO legacy_additivity;

ALTER TABLE askdata.measures
  ADD COLUMN additivity text,
  ADD COLUMN semi_additive_time_aggregation text,
  ADD COLUMN aggregation_restriction text,
  ADD COLUMN non_additive_dimensions uuid[] NOT NULL DEFAULT '{}',
  ADD COLUMN currency text,
  ADD COLUMN zero_denominator_policy text NOT NULL DEFAULT 'NULL',
  ADD COLUMN display_precision smallint NOT NULL DEFAULT 2,
  ADD COLUMN additivity_suggestion text,
  ADD COLUMN additivity_confirmed_by uuid,
  ADD COLUMN additivity_confirmed_at timestamptz;

ALTER TABLE askdata.metric_versions
  ADD COLUMN additivity text,
  ADD COLUMN semi_additive_time_aggregation text,
  ADD COLUMN aggregation_restriction text,
  ADD COLUMN non_additive_dimensions uuid[] NOT NULL DEFAULT '{}',
  ADD COLUMN currency text,
  ADD COLUMN zero_denominator_policy text NOT NULL DEFAULT 'NULL',
  ADD COLUMN display_precision smallint NOT NULL DEFAULT 2,
  ADD COLUMN additivity_suggestion text,
  ADD COLUMN additivity_confirmed_by uuid,
  ADD COLUMN additivity_confirmed_at timestamptz;

-- The old enum was required on every draft and was sometimes inferred by the
-- importer. Preserve it as an explicitly non-authoritative suggestion while
-- leaving the new fact column NULL for human confirmation.
UPDATE askdata.measures SET additivity_suggestion=CASE legacy_additivity
  WHEN 'ADDITIVE' THEN 'FULLY_ADDITIVE'
  WHEN 'SEMI_ADDITIVE' THEN 'SEMI_ADDITIVE'
  WHEN 'NON_ADDITIVE' THEN 'NON_ADDITIVE'
END;

UPDATE askdata.metric_versions SET additivity_suggestion=CASE legacy_additivity
  WHEN 'ADDITIVE' THEN 'FULLY_ADDITIVE'
  WHEN 'SEMI_ADDITIVE' THEN 'SEMI_ADDITIVE'
  WHEN 'NON_ADDITIVE' THEN 'NON_ADDITIVE'
END;

ALTER TABLE askdata.measures DROP COLUMN legacy_additivity;
ALTER TABLE askdata.metric_versions DROP COLUMN legacy_additivity;

ALTER TABLE askdata.measures
  ADD CONSTRAINT askdata_measures_additivity_enum CHECK(
    additivity IS NULL OR additivity IN ('FULLY_ADDITIVE','SEMI_ADDITIVE','NON_ADDITIVE')
  ),
  ADD CONSTRAINT askdata_measures_semi_additive_agg CHECK(
    additivity IS DISTINCT FROM 'SEMI_ADDITIVE'
    OR (semi_additive_time_aggregation IS NOT NULL
      AND semi_additive_time_aggregation IN ('PERIOD_END','PERIOD_BEGIN','PERIOD_AVERAGE'))
  ),
  ADD CONSTRAINT askdata_measures_non_additive_restriction CHECK(
    additivity IS DISTINCT FROM 'NON_ADDITIVE'
    OR aggregation_restriction IS NOT DISTINCT FROM 'POST_AGGREGATE'
  ),
  ADD CONSTRAINT askdata_measures_aggregation_restriction_enum CHECK(
    aggregation_restriction IS NULL OR aggregation_restriction IN ('PRE_AGGREGATE','POST_AGGREGATE')
  ),
  ADD CONSTRAINT askdata_measures_zero_denominator_check CHECK(
    zero_denominator_policy IN ('NULL','ZERO')
  ),
  ADD CONSTRAINT askdata_measures_display_precision_check CHECK(
    display_precision BETWEEN 0 AND 12
  ),
  ADD CONSTRAINT askdata_measures_additivity_suggestion_check CHECK(
    additivity_suggestion IS NULL
    OR additivity_suggestion IN ('FULLY_ADDITIVE','SEMI_ADDITIVE','NON_ADDITIVE')
  ),
  ADD CONSTRAINT askdata_measures_confirmation_pair_check CHECK(
    (additivity_confirmed_by IS NULL)=(additivity_confirmed_at IS NULL)
  ),
  ADD CONSTRAINT askdata_measures_currency_check CHECK(
    currency IS NULL OR length(btrim(currency)) BETWEEN 1 AND 16
  ),
  ADD CONSTRAINT askdata_measures_certified_requires_additivity CHECK(
    status<>'CERTIFIED' OR (
      additivity IS NOT NULL AND length(btrim(unit))>0
      AND (upper(unit)<>'CURRENCY' OR currency IS NOT NULL)
    )
  ) NOT VALID,
  ADD CONSTRAINT askdata_measures_additivity_confirmer_fk
    FOREIGN KEY(additivity_confirmed_by,tenant_id)
    REFERENCES platform.users(id,tenant_id) ON DELETE RESTRICT;

ALTER TABLE askdata.metric_versions
  ADD CONSTRAINT askdata_metric_versions_additivity_enum CHECK(
    additivity IS NULL OR additivity IN ('FULLY_ADDITIVE','SEMI_ADDITIVE','NON_ADDITIVE')
  ),
  ADD CONSTRAINT askdata_metric_versions_semi_additive_agg CHECK(
    additivity IS DISTINCT FROM 'SEMI_ADDITIVE'
    OR (semi_additive_time_aggregation IS NOT NULL
      AND semi_additive_time_aggregation IN ('PERIOD_END','PERIOD_BEGIN','PERIOD_AVERAGE'))
  ),
  ADD CONSTRAINT askdata_metric_versions_non_additive_restriction CHECK(
    additivity IS DISTINCT FROM 'NON_ADDITIVE'
    OR aggregation_restriction IS NOT DISTINCT FROM 'POST_AGGREGATE'
  ),
  ADD CONSTRAINT askdata_metric_versions_aggregation_restriction_enum CHECK(
    aggregation_restriction IS NULL OR aggregation_restriction IN ('PRE_AGGREGATE','POST_AGGREGATE')
  ),
  ADD CONSTRAINT askdata_metric_versions_zero_denominator_check CHECK(
    zero_denominator_policy IN ('NULL','ZERO')
  ),
  ADD CONSTRAINT askdata_metric_versions_display_precision_check CHECK(
    display_precision BETWEEN 0 AND 12
  ),
  ADD CONSTRAINT askdata_metric_versions_additivity_suggestion_check CHECK(
    additivity_suggestion IS NULL
    OR additivity_suggestion IN ('FULLY_ADDITIVE','SEMI_ADDITIVE','NON_ADDITIVE')
  ),
  ADD CONSTRAINT askdata_metric_versions_confirmation_pair_check CHECK(
    (additivity_confirmed_by IS NULL)=(additivity_confirmed_at IS NULL)
  ),
  ADD CONSTRAINT askdata_metric_versions_currency_check CHECK(
    currency IS NULL OR length(btrim(currency)) BETWEEN 1 AND 16
  ),
  ADD CONSTRAINT askdata_metric_versions_certified_requires_additivity CHECK(
    status<>'CERTIFIED' OR (
      additivity IS NOT NULL AND length(btrim(unit))>0
      AND (upper(unit)<>'CURRENCY' OR currency IS NOT NULL)
    )
  ) NOT VALID,
  ADD CONSTRAINT askdata_metric_versions_additivity_confirmer_fk
    FOREIGN KEY(additivity_confirmed_by,tenant_id)
    REFERENCES platform.users(id,tenant_id) ON DELETE RESTRICT;

COMMENT ON COLUMN askdata.measures.additivity IS
  'Human-confirmed FULLY_ADDITIVE, SEMI_ADDITIVE or NON_ADDITIVE fact; NULL drafts are not certifiable';
COMMENT ON COLUMN askdata.metric_versions.additivity IS
  'Human-confirmed FULLY_ADDITIVE, SEMI_ADDITIVE or NON_ADDITIVE fact; NULL drafts are not certifiable';
COMMENT ON COLUMN askdata.measures.additivity_suggestion IS
  'Non-authoritative migration/heuristic suggestion; never consumed by certification, release or compilation';
COMMENT ON COLUMN askdata.metric_versions.additivity_suggestion IS
  'Non-authoritative migration/heuristic suggestion; never consumed by certification, release or compilation';

-- QUERY-008: cardinality is a data-shape fact; fanout_policy is an execution
-- decision. Keep them orthogonal and never infer SAFE for legacy rows. NULL is
-- permitted only so pre-existing relationships can await manual review; Go and
-- certification both fail closed until the pair is complete.
ALTER TABLE askdata.relationships
  DROP CONSTRAINT relationships_cardinality_check,
  DROP CONSTRAINT relationships_fanout_policy_check,
  ALTER COLUMN cardinality DROP NOT NULL,
  ALTER COLUMN fanout_policy DROP NOT NULL,
  ALTER COLUMN fanout_policy DROP DEFAULT,
  ADD COLUMN bridge_model_version_id uuid,
  ADD CONSTRAINT askdata_relationships_bridge_model_fk
    FOREIGN KEY(bridge_model_version_id,domain_id,tenant_id)
    REFERENCES askdata.semantic_models(id,domain_id,tenant_id) ON DELETE RESTRICT;

-- This is an enum spelling migration, not a safety inference: the old value
-- already meant that a certified pre-aggregation was required.
UPDATE askdata.relationships
SET fanout_policy='PRE_AGGREGATE_REQUIRED'
WHERE fanout_policy='CERTIFIED_PREAGG';

-- Historical storage allowed every cardinality to be paired with every old
-- policy. Pairs outside the new proof matrix have no safe mechanical answer:
-- move them into the explicit manual-review holding state instead of guessing.
UPDATE askdata.relationships
SET cardinality=NULL,fanout_policy=NULL
WHERE NOT (
  (cardinality IN ('ONE_TO_ONE','MANY_TO_ONE') AND fanout_policy IN ('SAFE','BLOCK'))
  OR (cardinality='ONE_TO_MANY' AND fanout_policy IN ('PRE_AGGREGATE_REQUIRED','BLOCK'))
  OR (cardinality='MANY_TO_MANY' AND fanout_policy='BLOCK')
);

ALTER TABLE askdata.relationships
  ADD CONSTRAINT rel_cardinality_enum CHECK(
    cardinality IN ('ONE_TO_ONE','ONE_TO_MANY','MANY_TO_ONE','MANY_TO_MANY')
  ),
  ADD CONSTRAINT rel_fanout_enum CHECK(
    fanout_policy IN ('SAFE','PRE_AGGREGATE_REQUIRED','BRIDGE_REQUIRED','BLOCK')
  ),
  ADD CONSTRAINT rel_combination_valid CHECK(
    (cardinality IN ('ONE_TO_ONE','MANY_TO_ONE') AND fanout_policy IN ('SAFE','BLOCK'))
    OR (cardinality='ONE_TO_MANY' AND fanout_policy IN ('PRE_AGGREGATE_REQUIRED','BLOCK'))
    OR (cardinality='MANY_TO_MANY' AND fanout_policy IN ('BRIDGE_REQUIRED','BLOCK'))
  ),
  ADD CONSTRAINT rel_bridge_required CHECK(
    fanout_policy<>'BRIDGE_REQUIRED' OR bridge_model_version_id IS NOT NULL
  );

COMMENT ON COLUMN askdata.relationships.cardinality IS
  'Observed relationship shape; NULL legacy drafts are blocked until manually reviewed';
COMMENT ON COLUMN askdata.relationships.fanout_policy IS
  'Certified execution policy; never defaulted to SAFE';
COMMENT ON COLUMN askdata.relationships.bridge_model_version_id IS
  'Required certified bridge model for BRIDGE_REQUIRED many-to-many relationships';
