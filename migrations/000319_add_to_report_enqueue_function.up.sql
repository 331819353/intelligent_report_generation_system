BEGIN;

-- Confirmation stays actor-scoped, but the outbox insert crosses a worker-only
-- FORCE-RLS table. A narrow SECURITY DEFINER function rechecks the live tenant,
-- actor and PENDING intent before inserting one immutable queue row.
DROP POLICY add_to_report_outbox_actor_enqueue
  ON askdata.add_to_report_outbox;

CREATE OR REPLACE FUNCTION askdata.enqueue_add_to_report_intent(selected_intent_id uuid)
RETURNS boolean
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,platform,askdata
AS $$
DECLARE
  selected_tenant_id uuid;
BEGIN
  IF selected_intent_id IS NULL
    OR platform.is_system_access()
    OR platform.current_tenant_id() IS NULL
    OR platform.current_user_id() IS NULL THEN
    RETURN false;
  END IF;

  SELECT intent.tenant_id INTO selected_tenant_id
  FROM askdata.add_to_report_intents AS intent
  WHERE intent.id=selected_intent_id
    AND intent.tenant_id=platform.current_tenant_id()
    AND intent.actor_user_id=platform.current_user_id()
    AND intent.state='PENDING';

  IF selected_tenant_id IS NULL THEN
    RETURN false;
  END IF;

  INSERT INTO askdata.add_to_report_outbox(tenant_id,intent_id)
  VALUES(selected_tenant_id,selected_intent_id)
  ON CONFLICT(intent_id) DO NOTHING;
  RETURN true;
END
$$;

REVOKE ALL ON FUNCTION askdata.enqueue_add_to_report_intent(uuid) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION askdata.enqueue_add_to_report_intent(uuid) TO report_app;
REVOKE INSERT ON askdata.add_to_report_outbox FROM report_app;

COMMIT;
