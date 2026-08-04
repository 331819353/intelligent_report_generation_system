-- 000195 retired the semantic-Q&A runtime and dropped its outbox, but several
-- trigger functions with an "enqueue_" prefix survived that migration. Writes
-- to datasets and tags therefore still attempted to use the removed relation.
-- Drop every remaining producer; CASCADE also removes its table trigger.
DROP FUNCTION IF EXISTS platform.enqueue_asset_tag_binding_change() CASCADE;
DROP FUNCTION IF EXISTS platform.enqueue_dataset_field_change() CASCADE;
DROP FUNCTION IF EXISTS platform.enqueue_dataset_owner_change() CASCADE;
DROP FUNCTION IF EXISTS platform.enqueue_dataset_version_change() CASCADE;
DROP FUNCTION IF EXISTS platform.enqueue_dimension_change() CASCADE;
DROP FUNCTION IF EXISTS platform.enqueue_semantic_document_change() CASCADE;
DROP FUNCTION IF EXISTS platform.enqueue_semantic_materialization_change() CASCADE;
DROP FUNCTION IF EXISTS platform.enqueue_semantic_tag_change() CASCADE;
DROP FUNCTION IF EXISTS platform.enqueue_semantic_change(uuid,text,text,text) CASCADE;

-- Partial-output save failures could leave their AI audit row RUNNING even
-- after the owning table item had reached a terminal state. Close only those
-- provably orphaned rows; active metadata jobs are untouched.
UPDATE platform.ai_metadata_jobs AS ai_job
SET status='FAILED',error_code='PERSISTENCE_ERROR',completed_at=now()
FROM platform.data_source_metadata_job_items AS item
JOIN platform.data_source_metadata_jobs AS job
  ON job.id=item.job_id AND job.tenant_id=item.tenant_id
WHERE ai_job.data_source_metadata_job_item_id=item.id
  AND ai_job.tenant_id=item.tenant_id
  AND ai_job.status='RUNNING'
  AND (
    item.status IN ('SUCCEEDED','SKIPPED','FAILED')
    OR job.status IN ('SUCCEEDED','PARTIAL','FAILED')
  );
