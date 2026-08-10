-- RPT-005: a rollback always points at a real immutable version of the same
-- report, while its human reason is stored in a canonical audit-safe shape.
ALTER TABLE platform.report_versions
  DROP CONSTRAINT report_v2_rollback_shape_check,
  ADD CONSTRAINT report_v2_rollback_shape_check CHECK(
    (rollback_of_version_no IS NULL AND rollback_reason IS NULL)
    OR (
      rollback_of_version_no>0
      AND length(rollback_reason) BETWEEN 1 AND 1000
      AND rollback_reason=btrim(rollback_reason)
      AND rollback_reason !~ '[[:cntrl:]]'
    )
  ),
  ADD CONSTRAINT report_v2_rollback_target_fk
    FOREIGN KEY(report_id,rollback_of_version_no)
    REFERENCES platform.report_versions(report_id,version_no)
    ON DELETE RESTRICT;

COMMENT ON COLUMN platform.report_versions.rollback_of_version_no IS
  'RPT-005 immutable lineage: the historical version revalidated and republished as this new version';

COMMENT ON COLUMN platform.report_versions.rollback_reason IS
  'RPT-005 required bounded human audit reason; never changes after version creation';
