-- The question worker has no user session by design, so forced RLS prevents a
-- direct platform.user_roles lookup. Resolve only the actor attached to the
-- exact claimed run; all three identifiers must match the durable run row.

BEGIN;

CREATE OR REPLACE FUNCTION askdata.list_question_run_actor_roles(
  selected_tenant_id uuid,
  selected_actor_id uuid,
  selected_run_id uuid
)
RETURNS TABLE(role_id uuid)
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path=pg_catalog,askdata,platform
AS $$
  SELECT role.id
  FROM askdata.question_runs AS run
  JOIN platform.user_roles AS assignment
    ON assignment.tenant_id=run.tenant_id
   AND assignment.user_id=run.actor_id
  JOIN platform.roles AS role
    ON role.tenant_id=assignment.tenant_id
   AND role.id=assignment.role_id
  WHERE run.tenant_id=selected_tenant_id
    AND run.actor_id=selected_actor_id
    AND run.id=selected_run_id
    AND role.status='ACTIVE'
    AND role.deleted_at IS NULL
  ORDER BY role.id
  LIMIT 65
$$;

REVOKE ALL ON FUNCTION askdata.list_question_run_actor_roles(uuid,uuid,uuid)
  FROM PUBLIC;

COMMENT ON FUNCTION askdata.list_question_run_actor_roles(uuid,uuid,uuid) IS
  'RLS-safe current role lookup bound to one durable question run';

COMMIT;
