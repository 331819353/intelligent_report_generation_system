BEGIN;

-- The question worker owns the BINDING -> GRAPH_VALIDATING transition and
-- pins the conversation to the run's already-authorized release at that exact
-- boundary. Keep the worker read-only on every other conversation column.
GRANT UPDATE(
  pinned_release_id,
  pinned_at,
  pin_drift_acknowledged,
  updated_at
) ON askdata.conversations TO report_worker;

COMMIT;
