BEGIN;

ALTER TABLE askdata.quality_rules
  DROP CONSTRAINT IF EXISTS askdata_quality_rules_binding_shape_check;

COMMENT ON COLUMN askdata.quality_rules.rule_ast IS NULL;
COMMENT ON TABLE askdata.quality_rules IS NULL;

COMMIT;
