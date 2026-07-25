BEGIN;

-- A graph generation is immutable and may already be referenced by historical
-- query evidence. Rolling back code does not delete or rewrite it.

COMMIT;
