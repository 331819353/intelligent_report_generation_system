BEGIN;

CREATE OR REPLACE FUNCTION decision.sync_default_domain_approval_policy(
  selected_tenant_id uuid,
  selected_domain_id uuid
) RETURNS void
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,decision,platform
AS $$
BEGIN
  INSERT INTO decision.approval_policies(
    id,tenant_id,domain_id,name,required_approvals,status,created_by
  )
  SELECT 'domain-owner-single',domain.tenant_id,domain.id,
    '领域负责人审批',1,'ACTIVE',domain.created_by
  FROM platform.business_domains domain
  WHERE domain.tenant_id=selected_tenant_id
    AND domain.id=selected_domain_id
    AND domain.status='ACTIVE'
    AND EXISTS(
      SELECT 1
      FROM platform.domain_memberships membership
      JOIN platform.users user_account
        ON user_account.tenant_id=membership.tenant_id
       AND user_account.id=membership.user_id
       AND user_account.status='ACTIVE'
       AND user_account.deleted_at IS NULL
      WHERE membership.tenant_id=domain.tenant_id
        AND membership.domain_id=domain.id
        AND membership.status='ACTIVE'
    )
  ON CONFLICT(id,domain_id,tenant_id) DO UPDATE
  SET status='ACTIVE',updated_at=now();

  DELETE FROM decision.approval_policy_approvers approver
  WHERE approver.tenant_id=selected_tenant_id
    AND approver.domain_id=selected_domain_id
    AND approver.policy_id='domain-owner-single';

  INSERT INTO decision.approval_policy_approvers(
    tenant_id,domain_id,policy_id,approver_user_id,sequence_no
  )
  SELECT membership.tenant_id,membership.domain_id,'domain-owner-single',
    membership.user_id,
    row_number() OVER(
      ORDER BY CASE membership.member_role WHEN 'DOMAIN_ADMIN' THEN 0 ELSE 1 END,
        membership.created_at,membership.user_id
    )::integer
  FROM platform.domain_memberships membership
  JOIN platform.users user_account
    ON user_account.tenant_id=membership.tenant_id
   AND user_account.id=membership.user_id
   AND user_account.status='ACTIVE'
   AND user_account.deleted_at IS NULL
  WHERE membership.tenant_id=selected_tenant_id
    AND membership.domain_id=selected_domain_id
    AND membership.status='ACTIVE'
  ORDER BY CASE membership.member_role WHEN 'DOMAIN_ADMIN' THEN 0 ELSE 1 END,
    membership.created_at,membership.user_id;
END
$$;

DO $$
DECLARE
  domain_record record;
BEGIN
  FOR domain_record IN
    SELECT tenant_id,id FROM platform.business_domains
  LOOP
    PERFORM decision.sync_default_domain_approval_policy(
      domain_record.tenant_id,domain_record.id
    );
  END LOOP;
END
$$;

COMMIT;
