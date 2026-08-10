ALTER TABLE askdata.metric_versions
  DROP CONSTRAINT askdata_metric_additivity_suggestion_pair_check,
  DROP CONSTRAINT askdata_metric_additivity_suggestion_rule_check,
  DROP COLUMN additivity_suggestion_rule_id;
