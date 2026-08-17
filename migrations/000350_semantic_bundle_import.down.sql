BEGIN;

DROP INDEX IF EXISTS askdata.askdata_business_term_versions_knowledge_code_idx;

ALTER TABLE askdata.business_term_versions
  DROP COLUMN IF EXISTS relation,
  DROP COLUMN IF EXISTS authority,
  DROP COLUMN IF EXISTS knowledge_kind;

DELETE FROM askdata.business_term_versions WHERE target_object_type='CONCEPT';
DELETE FROM askdata.business_terms WHERE term_type='CONCEPT';

ALTER TABLE askdata.business_term_versions
  DROP CONSTRAINT business_term_versions_target_object_type_check;
ALTER TABLE askdata.business_term_versions
  ADD CONSTRAINT business_term_versions_target_object_type_check
  CHECK(target_object_type IN (
    'METRIC','DIMENSION','MEMBER','TIME_CONTRACT','OPERATOR','LEGACY'
  ));

ALTER TABLE askdata.business_terms
  DROP CONSTRAINT business_terms_term_type_check;
ALTER TABLE askdata.business_terms
  ADD CONSTRAINT business_terms_term_type_check CHECK(term_type IN (
    'METRIC','DIMENSION','MEMBER','TIME','OPERATOR'
  ));

DELETE FROM askdata.semantic_import_rows WHERE import_id IN (
  SELECT id FROM askdata.semantic_imports WHERE asset_type IN ('BUNDLE','KNOWLEDGE')
);
DELETE FROM askdata.semantic_imports WHERE asset_type IN ('BUNDLE','KNOWLEDGE');

ALTER TABLE askdata.semantic_imports
  DROP CONSTRAINT semantic_imports_asset_type_check;
ALTER TABLE askdata.semantic_imports
  ADD CONSTRAINT semantic_imports_asset_type_check CHECK(asset_type IN (
    'MODEL','MEASURE','METRIC','METRIC_DIMENSION','DIMENSION','MEMBER',
    'HIERARCHY','RELATIONSHIP','TERM','CERTIFIED_EXAMPLE','KPI_BUNDLE','EVAL_CASE'
  ));

COMMIT;
