DROP TRIGGER IF EXISTS datasets_deactivate_deleted_ods_metadata_delete
  ON platform.datasets;
DROP TRIGGER IF EXISTS datasets_deactivate_deleted_ods_metadata_update
  ON platform.datasets;
DROP FUNCTION IF EXISTS platform.deactivate_deleted_ods_metadata();
