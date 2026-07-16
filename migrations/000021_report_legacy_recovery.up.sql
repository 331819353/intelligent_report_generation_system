-- 旧会话草稿只能通过显式恢复语义迁入，避免把宽范围差异伪装成撤销或普通布局操作。
ALTER TABLE platform.report_revisions
  DROP CONSTRAINT report_revisions_operation_type_check,
  ADD CONSTRAINT report_revisions_operation_type_check CHECK(operation_type IN (
    'REPORT_CREATE','BLOCK_MOVE','BLOCK_RESIZE','BLOCK_CREATE','BLOCK_CLEAR','BLOCK_DELETE','BLOCK_STICKY_UPDATE',
    'COMPONENT_MOVE','COMPONENT_RESIZE','COMPONENT_CREATE','COMPONENT_COPY','COMPONENT_DELETE','COMPONENT_STICKY_UPDATE',
    'LEGACY_DRAFT_RECOVERY','UNDO','REDO'
  ));

COMMENT ON TABLE platform.report_revisions IS '每个服务端已验证语义变更对应一条不可变报告修订';
