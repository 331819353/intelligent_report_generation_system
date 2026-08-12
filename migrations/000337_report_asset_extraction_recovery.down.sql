-- This migration only repairs retry bookkeeping for historical outbox rows.
-- Reverting executable schema does not recreate obsolete failure receipts.
BEGIN;
COMMIT;
