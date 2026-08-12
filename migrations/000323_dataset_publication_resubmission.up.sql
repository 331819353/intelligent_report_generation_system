-- 审批撤回或驳回后必须允许同一精确草稿重新提交，同时仍只允许一个待审申请。
BEGIN;

ALTER TABLE platform.dataset_publication_requests
  DROP CONSTRAINT dataset_publication_requests_exact_revision_key;

CREATE UNIQUE INDEX dataset_publication_requests_one_pending_revision_idx
  ON platform.dataset_publication_requests(
    tenant_id,dataset_id,draft_version_id,expected_draft_record_version
  )
  WHERE status='PENDING';

COMMIT;
