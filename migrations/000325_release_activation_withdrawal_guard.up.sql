-- Activation counts only active, non-withdrawn approvals from the latest round.
BEGIN;

CREATE OR REPLACE FUNCTION askdata.active_release_approval_count(
  selected_release_id uuid, selected_gate_receipt_hash text
)
RETURNS integer
LANGUAGE sql STABLE SECURITY DEFINER SET search_path=pg_catalog,askdata
AS $$
  SELECT count(*)::integer
  FROM askdata.release_approvals AS approval
  LEFT JOIN askdata.release_approval_withdrawals AS withdrawal
    ON withdrawal.tenant_id=approval.tenant_id AND withdrawal.approval_id=approval.id
  WHERE approval.tenant_id=askdata.current_tenant_id()
    AND approval.domain_id=askdata.current_domain_id()
    AND approval.release_id=selected_release_id
    AND approval.gate_receipt_hash=selected_gate_receipt_hash
    AND approval.decision='APPROVED' AND withdrawal.id IS NULL
$$;

REVOKE ALL ON FUNCTION askdata.active_release_approval_count(uuid,text) FROM PUBLIC;

COMMIT;
