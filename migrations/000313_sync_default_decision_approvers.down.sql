BEGIN;

DROP TRIGGER IF EXISTS domain_memberships_sync_default_decision_policy
  ON platform.domain_memberships;
DROP FUNCTION IF EXISTS decision.sync_default_domain_approval_policy_from_membership();
DROP FUNCTION IF EXISTS decision.sync_default_domain_approval_policy(uuid,uuid);

COMMIT;
