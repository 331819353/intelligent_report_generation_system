BEGIN;

DROP TRIGGER IF EXISTS business_domains_default_decision_policy
  ON platform.business_domains;
DROP FUNCTION IF EXISTS decision.ensure_default_domain_approval_policy();

DELETE FROM decision.approval_policy_approvers approver
WHERE approver.policy_id='domain-owner-single'
  AND NOT EXISTS(
    SELECT 1 FROM decision.decisions value
    WHERE value.tenant_id=approver.tenant_id
      AND value.domain_id=approver.domain_id
      AND value.approval_policy_id=approver.policy_id
  );

DELETE FROM decision.approval_policies policy
WHERE policy.id='domain-owner-single'
  AND NOT EXISTS(
    SELECT 1 FROM decision.decisions value
    WHERE value.tenant_id=policy.tenant_id
      AND value.domain_id=policy.domain_id
      AND value.approval_policy_id=policy.id
  );

COMMIT;
