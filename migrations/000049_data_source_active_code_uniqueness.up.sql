-- 逻辑删除的数据源不再永久占用租户内编码；当前有效数据源仍保持大小写不敏感唯一。
ALTER TABLE platform.data_sources
  DROP CONSTRAINT data_sources_tenant_id_code_key;

CREATE UNIQUE INDEX data_sources_tenant_code_active_key
  ON platform.data_sources(tenant_id,code)
  WHERE deleted_at IS NULL;
