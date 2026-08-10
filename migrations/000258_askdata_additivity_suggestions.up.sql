-- ADD-002: retain the deterministic rule that produced a non-authoritative
-- additivity suggestion. The confirmed additivity fact remains independent.

ALTER TABLE askdata.metric_versions
  ADD COLUMN additivity_suggestion_rule_id text;

UPDATE askdata.metric_versions
SET additivity_suggestion_rule_id='LEGACY_IMPORT'
WHERE additivity_suggestion IS NOT NULL;

ALTER TABLE askdata.metric_versions
  ADD CONSTRAINT askdata_metric_additivity_suggestion_rule_check CHECK(
    additivity_suggestion_rule_id IS NULL OR (
      length(additivity_suggestion_rule_id) BETWEEN 1 AND 64
      AND additivity_suggestion_rule_id ~ '^[A-Z][A-Z0-9_]{0,63}$'
    )
  ),
  ADD CONSTRAINT askdata_metric_additivity_suggestion_pair_check CHECK(
    (additivity_suggestion IS NULL)=(additivity_suggestion_rule_id IS NULL)
  );

COMMENT ON COLUMN askdata.metric_versions.additivity_suggestion_rule_id IS
  'Auditable deterministic heuristic rule; advisory only and never consumed by compilation or release';
