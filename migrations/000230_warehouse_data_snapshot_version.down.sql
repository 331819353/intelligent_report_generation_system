DROP TRIGGER IF EXISTS materialization_snapshots_notify_completion
  ON platform.materialization_snapshots;
DROP TRIGGER IF EXISTS materialization_snapshots_immutable
  ON platform.materialization_snapshots;
DROP FUNCTION IF EXISTS platform.notify_materialization_snapshot_completed();
DROP FUNCTION IF EXISTS platform.enforce_materialization_snapshot_transition();

DO $rollback_guard$
BEGIN
  IF EXISTS(
    SELECT 1
    FROM platform.data_quality_results AS quality
    JOIN platform.dataset_materializations AS materialization
      ON materialization.id=quality.materialization_id
     AND materialization.tenant_id=quality.tenant_id
    WHERE quality.materialization_id IS NOT NULL
      AND quality.build_run_id<>materialization.build_run_id
  ) THEN
    RAISE EXCEPTION 'cannot roll back 000230 after a stable materialization has served multiple refresh runs';
  END IF;
END
$rollback_guard$;

ALTER TABLE platform.data_quality_results
  DROP CONSTRAINT data_quality_results_materialization_fk,
  ADD CONSTRAINT data_quality_results_materialization_fk
    FOREIGN KEY(materialization_id,build_run_id,tenant_id)
    REFERENCES platform.dataset_materializations(id,build_run_id,tenant_id)
    ON DELETE RESTRICT;

DROP TABLE platform.materialization_snapshots;

CREATE OR REPLACE FUNCTION platform.enforce_materialization_transition()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
  IF TG_OP='DELETE' THEN
    RAISE EXCEPTION '物化记录不可删除' USING ERRCODE='23514';
  END IF;
  IF NEW.id IS DISTINCT FROM OLD.id
    OR NEW.tenant_id IS DISTINCT FROM OLD.tenant_id
    OR NEW.dataset_id IS DISTINCT FROM OLD.dataset_id
    OR NEW.dataset_version_id IS DISTINCT FROM OLD.dataset_version_id
    OR NEW.build_run_id IS DISTINCT FROM OLD.build_run_id
    OR NEW.layer IS DISTINCT FROM OLD.layer
    OR NEW.relation_kind IS DISTINCT FROM OLD.relation_kind
    OR NEW.refresh_mode IS DISTINCT FROM OLD.refresh_mode
    OR NEW.physical_schema IS DISTINCT FROM OLD.physical_schema
    OR NEW.physical_name IS DISTINCT FROM OLD.physical_name
    OR NEW.published_schema IS DISTINCT FROM OLD.published_schema
    OR NEW.published_name IS DISTINCT FROM OLD.published_name
    OR NEW.schema_hash IS DISTINCT FROM OLD.schema_hash
    OR NEW.snapshot_hash IS DISTINCT FROM OLD.snapshot_hash
    OR NEW.row_count IS DISTINCT FROM OLD.row_count
    OR NEW.size_bytes IS DISTINCT FROM OLD.size_bytes
    OR NEW.watermark_json IS DISTINCT FROM OLD.watermark_json
    OR NEW.created_at IS DISTINCT FROM OLD.created_at THEN
    RAISE EXCEPTION '物化身份、位置和快照不可修改' USING ERRCODE='23514';
  END IF;
  IF NOT (
    (OLD.status='BUILDING' AND NEW.status IN ('ACTIVE','FAILED'))
    OR (OLD.status='ACTIVE' AND NEW.status='RETIRED')
  ) THEN
    RAISE EXCEPTION '非法的物化状态转换：% -> %',OLD.status,NEW.status
      USING ERRCODE='23514';
  END IF;
  RETURN NEW;
END
$$;

REVOKE ALL ON FUNCTION platform.enforce_materialization_transition() FROM PUBLIC;
