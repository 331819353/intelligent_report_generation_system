DROP TRIGGER IF EXISTS add_to_report_outbox_guard ON askdata.add_to_report_outbox;
DROP TRIGGER IF EXISTS add_to_report_intents_guard ON askdata.add_to_report_intents;
DROP FUNCTION IF EXISTS askdata.guard_add_to_report_outbox();
DROP FUNCTION IF EXISTS askdata.guard_add_to_report_intent();
DROP FUNCTION IF EXISTS askdata.list_add_to_report_tenants();
ALTER TABLE askdata.add_to_report_outbox
  DROP CONSTRAINT IF EXISTS add_to_report_outbox_lease_shape_check;
DROP INDEX IF EXISTS askdata.add_to_report_outbox_claim_idx;
CREATE INDEX add_to_report_outbox_claim_idx
  ON askdata.add_to_report_outbox(state,next_attempt_at,lease_expires_at)
  WHERE state IN ('PENDING','RUNNING');
