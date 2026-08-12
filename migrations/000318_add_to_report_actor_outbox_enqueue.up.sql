BEGIN;

-- The actor may confirm only its own intent, so it must be able to enqueue
-- exactly that intent for the privileged delivery worker. The previous
-- worker-only RLS policy made every legitimate confirmation roll back.
CREATE POLICY add_to_report_outbox_actor_enqueue
  ON askdata.add_to_report_outbox
  FOR INSERT
  WITH CHECK(
    tenant_id=platform.current_tenant_id()
    AND EXISTS(
      SELECT 1 FROM askdata.add_to_report_intents AS intent
      WHERE intent.id=intent_id
        AND intent.tenant_id=add_to_report_outbox.tenant_id
        AND intent.actor_user_id=platform.current_user_id()
        AND intent.state='PENDING'
    )
  );

COMMIT;
