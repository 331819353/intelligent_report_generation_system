DROP TRIGGER IF EXISTS metric_versions_zz_enrich_semantic_domain
  ON platform.metric_versions;
DROP FUNCTION IF EXISTS platform.enrich_metric_semantic_domain();

DROP TRIGGER IF EXISTS dataset_versions_domain_lineage_guard
  ON platform.dataset_versions;
DROP FUNCTION IF EXISTS platform.enforce_dataset_domain_lineage();

DROP TRIGGER IF EXISTS dimension_members_sync_semantic_document
  ON platform.dimension_members;
DROP FUNCTION IF EXISTS platform.sync_dimension_member_semantic_document();
DROP TABLE IF EXISTS platform.dimension_member_semantic_documents;
