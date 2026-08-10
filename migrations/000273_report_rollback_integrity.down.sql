ALTER TABLE platform.report_versions
  DROP CONSTRAINT report_v2_rollback_target_fk,
  DROP CONSTRAINT report_v2_rollback_shape_check,
  ADD CONSTRAINT report_v2_rollback_shape_check CHECK(
    (rollback_of_version_no IS NULL AND rollback_reason IS NULL)
    OR (rollback_of_version_no>0 AND length(btrim(rollback_reason)) BETWEEN 1 AND 1000)
  );

COMMENT ON COLUMN platform.report_versions.rollback_of_version_no IS NULL;
COMMENT ON COLUMN platform.report_versions.rollback_reason IS NULL;
