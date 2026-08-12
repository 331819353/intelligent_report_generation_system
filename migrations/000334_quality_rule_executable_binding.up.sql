-- SEM-QUALITY-001: askdata.quality_rules.rule_ast becomes a typed binding to a
-- check the materialization pipeline actually executes.
--
-- The column previously accepted any safe JSON object under 64KB, and no code
-- anywhere read or evaluated it. Rather than invent a second rule language and
-- a second evaluator beside the one the platform already runs, a semantic
-- quality rule now NAMES an executing dataset quality check:
--
--   what it expresses  a check from the executing catalog, at a scope, on a target
--   who executes it    the materialization worker, never the semantic layer
--   when               during the dataset build run that produces a materialization
--   against which      the ACTIVE materialization pinned by the semantic model
--
-- The executing catalog currently contains ROW_COUNT_NONNEGATIVE and
-- OUTPUT_GRAIN_UNIQUE_NOT_NULL at DATASET scope (see
-- internal/materialization/model.go, which is the single source of truth and is
-- enforced in Go on write, on certification and at the release gate). This
-- constraint is the database's own floor: it keeps a direct SQL write from
-- creating a rule that no component could ever evaluate, without duplicating
-- the catalog here where it would drift.
BEGIN;

ALTER TABLE askdata.quality_rules
  ADD CONSTRAINT askdata_quality_rules_binding_shape_check CHECK(
    rule_ast->>'type'='DATASET_QUALITY_BINDING'
    AND (rule_ast->>'version')::int=1
    AND btrim(COALESCE(rule_ast->>'datasetRuleCode',''))<>''
    AND rule_ast->>'scope' IN ('DATASET','FIELD','RELATIONSHIP')
    AND (
      (rule_ast->>'scope'='FIELD' AND btrim(COALESCE(rule_ast->>'fieldId',''))<>'')
      OR (rule_ast->>'scope'<>'FIELD' AND COALESCE(rule_ast->>'fieldId','')='')
    )
  );

COMMENT ON COLUMN askdata.quality_rules.rule_ast IS
  'DATASET_QUALITY_BINDING document naming an executing dataset quality check (code, scope, optional fieldId and maxAgeHours); the semantic layer reads its outcome and never evaluates it';
COMMENT ON TABLE askdata.quality_rules IS
  'Versioned governed bindings from a semantic object to a dataset quality check executed by materialization; a rule bound to a non-executing check is rejected on write, on certification and at the release gate';

COMMIT;
