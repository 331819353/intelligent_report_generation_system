BEGIN;

DROP TRIGGER IF EXISTS domain_access_escalations_immutable ON platform.domain_access_escalations;
DROP TRIGGER IF EXISTS domain_access_approvals_immutable ON platform.domain_access_approvals;
DROP TABLE IF EXISTS platform.domain_access_escalations;
DROP TABLE IF EXISTS platform.domain_access_approvals;
DROP FUNCTION IF EXISTS platform.reject_domain_access_ledger_mutation();

ALTER TABLE platform.domain_access_applications
  DROP COLUMN IF EXISTS escalation_level,
  DROP COLUMN IF EXISTS sla_due_at,
  DROP COLUMN IF EXISTS requires_dual_approval;
ALTER TABLE platform.domain_access_applications
  DROP CONSTRAINT IF EXISTS domain_access_applications_identity_tenant_key;

ALTER TABLE platform.business_domains DROP COLUMN IF EXISTS access_sensitivity;

COMMIT;
