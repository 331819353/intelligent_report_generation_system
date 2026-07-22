-- 文件原始版本保持不可变；新增数据表步骤生成的解析方案独立保存并与版本一对一关联。
CREATE TABLE platform.file_asset_inspections(
  file_version_id uuid PRIMARY KEY,
  tenant_id uuid NOT NULL,
  parse_config jsonb NOT NULL,
  workbook_summary jsonb NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY(file_version_id,tenant_id)
    REFERENCES platform.file_asset_versions(id,tenant_id) ON DELETE CASCADE
);

CREATE TRIGGER file_asset_inspections_set_updated_at
BEFORE UPDATE ON platform.file_asset_inspections
FOR EACH ROW EXECUTE FUNCTION platform.set_updated_at();

ALTER TABLE platform.file_asset_inspections ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform.file_asset_inspections FORCE ROW LEVEL SECURITY;
CREATE POLICY file_asset_inspections_tenant_isolation
  ON platform.file_asset_inspections
  USING(tenant_id=platform.current_tenant_id())
  WITH CHECK(tenant_id=platform.current_tenant_id());

COMMENT ON TABLE platform.file_asset_inspections IS
  'Deterministic parse plan finalized during add-table flow; raw file_asset_versions remain immutable';
