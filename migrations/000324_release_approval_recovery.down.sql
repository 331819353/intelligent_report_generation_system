BEGIN;

DROP FUNCTION IF EXISTS askdata.escalate_release_approval(uuid,text,text,uuid);
DROP FUNCTION IF EXISTS askdata.reset_rejected_release_approvals(uuid,text,text,uuid);
DROP FUNCTION IF EXISTS askdata.withdraw_release_approval(uuid,text,text,text,uuid);
DROP FUNCTION IF EXISTS askdata.submit_release_approval_v2(uuid,uuid,uuid,text,text,text,text,uuid,uuid);
DROP TABLE IF EXISTS askdata.release_approval_escalations;
DROP TABLE IF EXISTS askdata.release_approval_withdrawals;
DROP INDEX IF EXISTS askdata.release_approvals_reviewer_idx;
DROP INDEX IF EXISTS askdata.release_approvals_slot_idx;
ALTER TABLE askdata.release_approvals
  ADD CONSTRAINT askdata_release_approvals_slot_key UNIQUE(
    tenant_id,release_id,gate_receipt_hash,review_slot
  ),
  ADD CONSTRAINT askdata_release_approvals_reviewer_key UNIQUE(
    tenant_id,release_id,gate_receipt_hash,reviewer_id
  );

COMMIT;
