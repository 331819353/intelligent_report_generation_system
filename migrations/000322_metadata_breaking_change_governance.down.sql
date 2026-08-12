BEGIN;

DROP TRIGGER IF EXISTS metadata_diffs_propagate_breaking_change
  ON platform.metadata_diffs;
DROP FUNCTION IF EXISTS platform.propagate_breaking_metadata_diff();
DROP FUNCTION IF EXISTS platform.metadata_diff_is_breaking(
  text,platform.metadata_change_type,jsonb,jsonb
);

COMMIT;
