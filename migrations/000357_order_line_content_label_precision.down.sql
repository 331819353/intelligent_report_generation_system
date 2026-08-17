-- This migration only removes a false-positive label. Rolling it back would
-- deliberately restore incorrect metadata, so the safe reverse is a no-op.
BEGIN;
COMMIT;
