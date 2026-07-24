-- 数据源身份与连接配置解耦：草稿配置可继续编辑，运行时始终读取精确发布版本。
ALTER TABLE platform.data_sources
  ADD COLUMN description text NOT NULL DEFAULT '',
  ADD COLUMN owner_user_id uuid,
  ADD COLUMN visibility platform.asset_visibility NOT NULL DEFAULT 'PRIVATE',
  ADD COLUMN created_by uuid,
  ADD COLUMN updated_by uuid,
  ADD COLUMN validation_status text NOT NULL DEFAULT 'UNTESTED'
    CHECK(validation_status IN ('UNTESTED','PASSED','FAILED')),
  ADD COLUMN publication_status text NOT NULL DEFAULT 'UNPUBLISHED'
    CHECK(publication_status IN ('UNPUBLISHED','PUBLISHED')),
  ADD COLUMN current_draft_version_id uuid,
  ADD COLUMN current_published_version_id uuid,
  ADD COLUMN last_tested_version_id uuid,
  ADD COLUMN last_tested_config_hash text,
  ADD COLUMN test_expires_at timestamptz,
  ADD CONSTRAINT data_sources_owner_tenant_fk
    FOREIGN KEY(owner_user_id,tenant_id) REFERENCES platform.users(id,tenant_id)
    ON DELETE SET NULL (owner_user_id),
  ADD CONSTRAINT data_sources_created_by_tenant_fk
    FOREIGN KEY(created_by,tenant_id) REFERENCES platform.users(id,tenant_id)
    ON DELETE SET NULL (created_by),
  ADD CONSTRAINT data_sources_updated_by_tenant_fk
    FOREIGN KEY(updated_by,tenant_id) REFERENCES platform.users(id,tenant_id)
    ON DELETE SET NULL (updated_by);

ALTER TABLE platform.file_asset_versions
  ADD CONSTRAINT file_asset_versions_identity_asset_tenant_key
    UNIQUE(id,file_asset_id,tenant_id);

CREATE TABLE platform.data_source_versions(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id),
  data_source_id uuid NOT NULL,
  version_no bigint NOT NULL CHECK(version_no>0),
  source_type platform.data_source_type NOT NULL,
  config jsonb NOT NULL DEFAULT '{}',
  secret_ref text,
  file_asset_id uuid,
  file_version_id uuid,
  config_hash text NOT NULL CHECK(config_hash ~ '^[0-9a-f]{64}$'),
  created_by uuid,
  created_at timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY(data_source_id,tenant_id)
    REFERENCES platform.data_sources(id,tenant_id),
  FOREIGN KEY(file_version_id,file_asset_id,tenant_id)
    REFERENCES platform.file_asset_versions(id,file_asset_id,tenant_id),
  FOREIGN KEY(created_by,tenant_id)
    REFERENCES platform.users(id,tenant_id) ON DELETE SET NULL (created_by),
  CONSTRAINT data_source_version_secret_or_file CHECK(
    (source_type='EXCEL' AND file_asset_id IS NOT NULL AND file_version_id IS NOT NULL AND secret_ref IS NULL)
    OR
    (source_type IN ('MYSQL','ORACLE') AND secret_ref IS NOT NULL AND file_asset_id IS NULL AND file_version_id IS NULL)
  ),
  CONSTRAINT data_source_version_config_no_plaintext_credentials
    CHECK (NOT (config ?| ARRAY['password','passwd','secretRef','secret_ref','jdbcUrl','jdbc_url'])),
  UNIQUE(data_source_id,version_no),
  UNIQUE(id,tenant_id),
  UNIQUE(id,data_source_id,tenant_id)
);

INSERT INTO platform.data_source_versions(
  tenant_id,data_source_id,version_no,source_type,config,secret_ref,file_asset_id,file_version_id,config_hash
)
SELECT source.tenant_id,source.id,1,source.source_type,source.config,source.secret_ref,source.file_asset_id,
       current_file.id,
       encode(digest(
         jsonb_build_object(
           'type',source.source_type::text,
           'config',source.config,
           'secretRef',COALESCE(source.secret_ref,''),
           'fileAssetId',COALESCE(source.file_asset_id::text,''),
           'fileVersionId',COALESCE(current_file.id::text,'')
         )::text,
         'sha256'
       ),'hex')
FROM platform.data_sources AS source
LEFT JOIN platform.file_assets AS file_asset
  ON file_asset.id=source.file_asset_id AND file_asset.tenant_id=source.tenant_id
LEFT JOIN platform.file_asset_versions AS current_file
  ON current_file.file_asset_id=file_asset.id
 AND current_file.tenant_id=file_asset.tenant_id
 AND current_file.version=file_asset.current_version;

UPDATE platform.data_sources AS source
SET current_draft_version_id=version.id,
    current_published_version_id=CASE
      WHEN source.status IN ('ACTIVE','SYNCING','DISABLED')
        OR (source.status='ERROR' AND source.last_synced_at IS NOT NULL)
      THEN version.id
      ELSE NULL
    END,
    last_tested_version_id=CASE WHEN source.last_tested_at IS NOT NULL THEN version.id ELSE NULL END,
    last_tested_config_hash=CASE WHEN source.last_tested_at IS NOT NULL THEN version.config_hash ELSE NULL END,
    validation_status=CASE
      WHEN source.status='ERROR' THEN 'FAILED'
      WHEN source.last_tested_at IS NOT NULL THEN 'PASSED'
      ELSE 'UNTESTED'
    END,
    publication_status=CASE
      WHEN source.status IN ('ACTIVE','SYNCING','DISABLED')
        OR (source.status='ERROR' AND source.last_synced_at IS NOT NULL)
      THEN 'PUBLISHED'
      ELSE 'UNPUBLISHED'
    END
FROM platform.data_source_versions AS version
WHERE version.data_source_id=source.id AND version.version_no=1;

ALTER TABLE platform.data_sources
  ALTER COLUMN current_draft_version_id SET NOT NULL,
  ADD CONSTRAINT data_sources_draft_version_fk
    FOREIGN KEY(current_draft_version_id,id,tenant_id)
    REFERENCES platform.data_source_versions(id,data_source_id,tenant_id)
    DEFERRABLE INITIALLY DEFERRED,
  ADD CONSTRAINT data_sources_published_version_fk
    FOREIGN KEY(current_published_version_id,id,tenant_id)
    REFERENCES platform.data_source_versions(id,data_source_id,tenant_id)
    DEFERRABLE INITIALLY DEFERRED,
  ADD CONSTRAINT data_sources_last_tested_version_fk
    FOREIGN KEY(last_tested_version_id,id,tenant_id)
    REFERENCES platform.data_source_versions(id,data_source_id,tenant_id)
    DEFERRABLE INITIALLY DEFERRED,
  ADD CONSTRAINT data_sources_publication_pointer_check CHECK(
    (publication_status='UNPUBLISHED' AND current_published_version_id IS NULL)
    OR
    (publication_status='PUBLISHED' AND current_published_version_id IS NOT NULL)
  ),
  ADD CONSTRAINT data_sources_test_binding_check CHECK(
    (last_tested_version_id IS NULL AND last_tested_config_hash IS NULL AND test_expires_at IS NULL)
    OR
    (last_tested_version_id IS NOT NULL AND last_tested_config_hash ~ '^[0-9a-f]{64}$')
  );

CREATE TABLE platform.data_source_test_runs(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id),
  data_source_id uuid NOT NULL,
  data_source_version_id uuid NOT NULL,
  config_hash text NOT NULL CHECK(config_hash ~ '^[0-9a-f]{64}$'),
  status text NOT NULL CHECK(status IN ('PASSED','FAILED')),
  server_version text NOT NULL DEFAULT '',
  latency_ms bigint NOT NULL DEFAULT 0 CHECK(latency_ms>=0),
  error_message text NOT NULL DEFAULT '',
  started_at timestamptz NOT NULL,
  completed_at timestamptz NOT NULL,
  expires_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY(data_source_version_id,data_source_id,tenant_id)
    REFERENCES platform.data_source_versions(id,data_source_id,tenant_id),
  CONSTRAINT data_source_test_run_expiry_check CHECK(
    (status='PASSED' AND expires_at IS NOT NULL AND expires_at>completed_at)
    OR
    (status='FAILED' AND expires_at IS NULL)
  ),
  UNIQUE(id,tenant_id)
);

CREATE INDEX data_source_versions_source_idx
  ON platform.data_source_versions(tenant_id,data_source_id,version_no DESC);
CREATE INDEX data_source_test_runs_publish_idx
  ON platform.data_source_test_runs(
    tenant_id,data_source_id,data_source_version_id,config_hash,completed_at DESC
  )
  WHERE status='PASSED';
CREATE INDEX data_sources_owner_visibility_idx
  ON platform.data_sources(tenant_id,owner_user_id,visibility)
  WHERE deleted_at IS NULL;

CREATE OR REPLACE FUNCTION platform.reject_data_source_version_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
  RAISE EXCEPTION 'data source versions are immutable';
END
$$;

CREATE TRIGGER data_source_versions_immutable
BEFORE UPDATE OR DELETE ON platform.data_source_versions
FOR EACH ROW EXECUTE FUNCTION platform.reject_data_source_version_mutation();

ALTER TABLE platform.data_source_versions ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform.data_source_versions FORCE ROW LEVEL SECURITY;
ALTER TABLE platform.data_source_test_runs ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform.data_source_test_runs FORCE ROW LEVEL SECURITY;

CREATE POLICY data_source_versions_tenant_isolation ON platform.data_source_versions
  USING(tenant_id=platform.current_tenant_id())
  WITH CHECK(tenant_id=platform.current_tenant_id());
CREATE POLICY data_source_test_runs_tenant_isolation ON platform.data_source_test_runs
  USING(tenant_id=platform.current_tenant_id())
  WITH CHECK(tenant_id=platform.current_tenant_id());

COMMENT ON TABLE platform.data_source_versions IS
  'Immutable connection configuration snapshots; secret_ref is internal and never returned by APIs';
COMMENT ON TABLE platform.data_source_test_runs IS
  'Connection tests bound to an exact data source version and configuration hash';
COMMENT ON COLUMN platform.data_sources.current_draft_version_id IS
  'Editable configuration head used by management and connection testing';
COMMENT ON COLUMN platform.data_sources.current_published_version_id IS
  'Exact configuration snapshot used by runtime queries and metadata synchronization';
