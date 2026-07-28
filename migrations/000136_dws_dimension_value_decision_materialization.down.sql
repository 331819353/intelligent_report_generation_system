BEGIN;

DROP TRIGGER IF EXISTS dimension_metric_compatibility_auto_verify_rule
  ON platform.dimension_metric_compatibility;
DROP FUNCTION IF EXISTS
  platform.auto_verify_rule_dimension_metric_compatibility();

DROP TABLE IF EXISTS platform.dimension_where_design_policies;

DELETE FROM platform.dimension_where_decisions
WHERE source_type='DWS_PRECOMPUTED';

ALTER TABLE platform.dimension_where_decisions
  DROP CONSTRAINT IF EXISTS
    dimension_where_decisions_embedding_document_fk,
  DROP CONSTRAINT IF EXISTS dimension_where_decisions_member_fk,
  DROP CONSTRAINT IF EXISTS dimension_where_decisions_embedding_source_check,
  DROP CONSTRAINT IF EXISTS
    dimension_where_decisions_source_input_hash_check,
  DROP CONSTRAINT IF EXISTS dimension_where_decisions_source_type_check,
  DROP CONSTRAINT IF EXISTS
    dimension_where_decisions_selected_member_count_check,
  DROP CONSTRAINT IF EXISTS
    dimension_where_decisions_llm_prompt_version_check,
  DROP COLUMN IF EXISTS source_input_hash,
  DROP COLUMN IF EXISTS source_type,
  DROP COLUMN IF EXISTS embedding_document_id,
  DROP COLUMN IF EXISTS dimension_member_id;

ALTER TABLE platform.dimension_where_decisions
  ALTER COLUMN embedding SET NOT NULL,
  ALTER COLUMN latest_query_plan_id SET NOT NULL,
  ADD CONSTRAINT dimension_where_decisions_llm_prompt_version_check
    CHECK(llm_prompt_version='semantic-query-where-design-v2'),
  ADD CONSTRAINT dimension_where_decisions_selected_member_count_check
    CHECK(selected_member_count BETWEEN 1 AND 128);

ALTER TABLE platform.dimension_member_semantic_documents
  DROP CONSTRAINT IF EXISTS
    dimension_member_semantic_documents_identity_tenant_key;

COMMIT;
