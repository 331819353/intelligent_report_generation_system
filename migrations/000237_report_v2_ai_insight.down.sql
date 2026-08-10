DROP TABLE IF EXISTS platform.report_insight_artifacts;
DROP TABLE IF EXISTS platform.report_evidence_artifacts;
DROP TABLE IF EXISTS platform.report_ai_operations;
DROP TABLE IF EXISTS platform.report_ai_runs;
DROP FUNCTION IF EXISTS platform.guard_report_ai_summary();
UPDATE platform.ai_tenant_policies SET allowed_purposes=array_remove(
  array_remove(array_remove(allowed_purposes,'REPORT_GENERATION'),'BLOCK_EDIT'),'CONCLUSION_GENERATION'
);
ALTER TABLE platform.ai_tenant_policies DROP CONSTRAINT ai_tenant_policies_purposes_check;
ALTER TABLE platform.ai_tenant_policies ADD CONSTRAINT ai_tenant_policies_purposes_check CHECK(
  cardinality(allowed_purposes) BETWEEN 1 AND 6
  AND array_position(allowed_purposes,NULL) IS NULL
  AND allowed_purposes <@ ARRAY[
    'METADATA_COMPLETION','DATASET_DAG_GENERATION','DATASET_TAG_SUGGESTION',
    'DATASET_SEMANTIC_NAMING','DATA_SOURCE_CONFIGURATION','SEMANTIC_QUESTION'
  ]::text[]
);
