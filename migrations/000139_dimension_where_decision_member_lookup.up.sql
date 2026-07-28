BEGIN;

CREATE INDEX dimension_where_decisions_member_target_idx
  ON platform.dimension_where_decisions(
    tenant_id,dimension_member_id,metric_version_id,materialization_id
  )
  WHERE dimension_member_id IS NOT NULL;

COMMENT ON INDEX platform.dimension_where_decisions_member_target_idx IS
  '按规范成员定位已预计算或问答观察的WHERE决策，支撑DWS全量增量回填';

COMMIT;
