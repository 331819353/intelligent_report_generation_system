-- 将增量完善边界收缩到表头和字段技术结构，未变化业务元数据不再进入模型输出目标。
ALTER TABLE platform.metadata_tables
  ADD COLUMN table_structure_hash text NOT NULL DEFAULT ''
    CHECK(table_structure_hash='' OR length(table_structure_hash)=64),
  ADD COLUMN last_enriched_table_structure_hash text NOT NULL DEFAULT ''
    CHECK(last_enriched_table_structure_hash='' OR length(last_enriched_table_structure_hash)=64);

ALTER TABLE platform.metadata_columns
  ADD COLUMN last_enriched_structure_hash text NOT NULL DEFAULT ''
    CHECK(last_enriched_structure_hash='' OR length(last_enriched_structure_hash)=64);

-- 只有父表当前完整结构已经成功完善时，历史活动字段才可安全视为已完善。
UPDATE platform.metadata_columns c
SET last_enriched_structure_hash=c.structure_hash
FROM platform.metadata_tables t
WHERE t.id=c.table_id
  AND t.tenant_id=c.tenant_id
  AND t.asset_status='ACTIVE'
  AND c.asset_status='ACTIVE'
  -- 旧测试或人工数据可能没有使用标准 SHA-256；这类记录保持待完善，不能写入新 fence。
  AND length(c.structure_hash)=64
  AND t.last_enriched_structure_hash<>''
  AND t.last_enriched_structure_hash=t.structure_hash;

ALTER TABLE platform.ai_metadata_suggestions
  ADD COLUMN expected_structure_hash text NOT NULL DEFAULT ''
    CHECK(expected_structure_hash='' OR length(expected_structure_hash)=64);

COMMENT ON COLUMN platform.metadata_tables.table_structure_hash IS 'Technical table-header hash excluding columns and volatile row estimates';
COMMENT ON COLUMN platform.metadata_tables.last_enriched_table_structure_hash IS 'Table-header hash most recently evaluated by metadata AI';
COMMENT ON COLUMN platform.metadata_columns.last_enriched_structure_hash IS 'Column structure hash most recently evaluated by metadata AI';
COMMENT ON COLUMN platform.ai_metadata_suggestions.expected_structure_hash IS 'Target technical hash required when accepting a pending suggestion; legacy blank suggestions fail closed';
