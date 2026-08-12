BEGIN;

DROP INDEX IF EXISTS platform.dataset_publication_requests_one_pending_revision_idx;

ALTER TABLE platform.dataset_publication_requests
  ADD CONSTRAINT dataset_publication_requests_exact_revision_key
  UNIQUE(tenant_id,dataset_id,draft_version_id,expected_draft_record_version);

COMMIT;
