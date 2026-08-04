-- The semantic-Q&A runtime was irreversibly retired by migration 000195.
-- Restoring these producers without restoring the complete retired subsystem
-- would reintroduce writes to a missing outbox.
SELECT 1;
