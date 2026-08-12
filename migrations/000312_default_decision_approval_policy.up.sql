-- Every business domain needs one usable decision approval path on day one.
-- The domain owner is the conservative bootstrap approver; administrators may
-- add stricter policies later without changing existing decisions.

BEGIN;

CREATE OR REPLACE FUNCTION decision.ensure_default_domain_approval_policy()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,decision,platform
AS $$
BEGIN
  INSERT INTO decision.approval_policies(
    id,tenant_id,domain_id,name,required_approvals,status,created_by
  ) VALUES(
    'domain-owner-single',NEW.tenant_id,NEW.id,'领域负责人审批',1,'ACTIVE',NEW.created_by
  ) ON CONFLICT(id,domain_id,tenant_id) DO NOTHING;

  INSERT INTO decision.approval_policy_approvers(
    tenant_id,domain_id,policy_id,approver_user_id,sequence_no
  ) VALUES(
    NEW.tenant_id,NEW.id,'domain-owner-single',NEW.created_by,1
  ) ON CONFLICT(tenant_id,domain_id,policy_id,approver_user_id) DO NOTHING;
  RETURN NEW;
END
$$;

REVOKE ALL ON FUNCTION decision.ensure_default_domain_approval_policy() FROM PUBLIC;

DROP TRIGGER IF EXISTS business_domains_default_decision_policy
  ON platform.business_domains;
CREATE TRIGGER business_domains_default_decision_policy
AFTER INSERT ON platform.business_domains
FOR EACH ROW EXECUTE FUNCTION decision.ensure_default_domain_approval_policy();

INSERT INTO decision.approval_policies(
  id,tenant_id,domain_id,name,required_approvals,status,created_by
)
SELECT 'domain-owner-single',domain.tenant_id,domain.id,
  '领域负责人审批',1,'ACTIVE',domain.created_by
FROM platform.business_domains domain
ON CONFLICT(id,domain_id,tenant_id) DO NOTHING;

INSERT INTO decision.approval_policy_approvers(
  tenant_id,domain_id,policy_id,approver_user_id,sequence_no
)
SELECT domain.tenant_id,domain.id,'domain-owner-single',domain.created_by,1
FROM platform.business_domains domain
JOIN decision.approval_policies policy
  ON policy.tenant_id=domain.tenant_id AND policy.domain_id=domain.id
 AND policy.id='domain-owner-single'
ON CONFLICT(tenant_id,domain_id,policy_id,approver_user_id) DO NOTHING;

COMMIT;
