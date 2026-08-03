-- 报表设计器 2.0：允许 Card DSL 草稿/制品，记录内容寻址对象位置，并审计卡片级操作。
ALTER TABLE platform.report_drafts
  DROP CONSTRAINT report_drafts_schema_version_check,
  ADD CONSTRAINT report_drafts_schema_version_check
    CHECK(schema_version IN ('1.0','1.0.0'));

ALTER TABLE platform.report_versions
  DROP CONSTRAINT report_versions_schema_version_check,
  ADD CONSTRAINT report_versions_schema_version_check
    CHECK(schema_version IN ('1.0','1.0.0')),
  ADD COLUMN object_uri text NOT NULL DEFAULT ''
    CHECK(object_uri='' OR object_uri ~ '^s3://[a-z0-9][a-z0-9.-]+/.+$');

ALTER TABLE platform.report_revisions
  DROP CONSTRAINT report_revisions_operation_type_check,
  ADD CONSTRAINT report_revisions_operation_type_check CHECK(operation_type IN (
    'REPORT_CREATE','TEMPLATE_UPDATE','REPORT_SETTINGS_UPDATE','FILTER_UPDATE',
    'CARD_CREATE','CARD_DELETE','CARD_LAYOUT_UPDATE','CARD_CONFIG_UPDATE',
    'BLOCK_MOVE','BLOCK_RESIZE','BLOCK_CREATE','BLOCK_CLEAR','BLOCK_DELETE','BLOCK_STICKY_UPDATE',
    'COMPONENT_MOVE','COMPONENT_RESIZE','COMPONENT_CREATE','COMPONENT_COPY','COMPONENT_DELETE','COMPONENT_STICKY_UPDATE',
    'LEGACY_DRAFT_RECOVERY','UNDO','REDO'
  ));

COMMENT ON COLUMN platform.report_versions.object_uri IS
  '内容寻址的发布 JSON 对象；definition_bytes 是灾备副本，运行时优先校验并读取该对象';
