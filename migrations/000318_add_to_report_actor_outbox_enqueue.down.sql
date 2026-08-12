BEGIN;

DROP POLICY add_to_report_outbox_actor_enqueue
  ON askdata.add_to_report_outbox;

COMMIT;
