-- 保留 1.0.0 约束以避免已有 Card DSL 数据使回滚失败，仅撤销新写入路径。
ALTER TABLE platform.report_revisions
  DROP CONSTRAINT report_revisions_operation_type_check,
  ADD CONSTRAINT report_revisions_operation_type_check CHECK(operation_type IN (
    'REPORT_CREATE','TEMPLATE_UPDATE','BLOCK_MOVE','BLOCK_RESIZE','BLOCK_CREATE','BLOCK_CLEAR','BLOCK_DELETE','BLOCK_STICKY_UPDATE',
    'COMPONENT_MOVE','COMPONENT_RESIZE','COMPONENT_CREATE','COMPONENT_COPY','COMPONENT_DELETE','COMPONENT_STICKY_UPDATE',
    'LEGACY_DRAFT_RECOVERY','UNDO','REDO'
  ));

ALTER TABLE platform.report_versions DROP COLUMN object_uri;
