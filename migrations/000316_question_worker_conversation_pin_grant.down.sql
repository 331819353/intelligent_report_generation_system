BEGIN;

REVOKE UPDATE(
  pinned_release_id,
  pinned_at,
  pin_drift_acknowledged,
  updated_at
) ON askdata.conversations FROM report_worker;

COMMIT;
