-- 把发布层级身份和 ODS 源输入的可信边界前移到数据库。服务端仍负责
-- 派生计划，但直接写控制表也不能绕过精确发布版本或改写冻结输入。

CREATE OR REPLACE FUNCTION platform.enforce_dataset_build_run_source()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,platform
AS $$
BEGIN
  PERFORM 1
  FROM platform.dataset_versions AS version
  JOIN platform.datasets AS owner
    ON owner.id=version.dataset_id AND owner.tenant_id=version.tenant_id
  WHERE version.id=NEW.dataset_version_id
    AND version.dataset_id=NEW.dataset_id
    AND version.tenant_id=NEW.tenant_id
    AND version.layer=NEW.layer
    AND version.status='PUBLISHED'
    AND owner.status='PUBLISHED'
    AND owner.current_published_version_id=version.id
    AND owner.deleted_at IS NULL
  FOR SHARE OF version,owner;
  IF NOT FOUND THEN
    RAISE EXCEPTION '构建运行只能固定当前发布且同层级的数据集版本'
      USING ERRCODE='23514';
  END IF;
  RETURN NEW;
END
$$;

REVOKE ALL ON FUNCTION platform.enforce_dataset_build_run_source() FROM PUBLIC;

-- v60 的 SOURCE 输入只有表/文件身份，无法证明构建使用的是哪一个不可变连接
-- 配置；数据源再次发布后会形成 TOCTOU。两列共同固定租户内的数据源身份和
-- 精确配置版本，并由复合外键防止串接其他数据源的版本。
ALTER TABLE platform.build_run_inputs
  ADD COLUMN input_data_source_id uuid,
  ADD COLUMN input_data_source_version_id uuid;

DO $$
BEGIN
  IF EXISTS(
    SELECT 1
    FROM platform.build_run_inputs
    WHERE source_type IN ('SOURCE_TABLE','FILE_VERSION')
  ) THEN
    RAISE EXCEPTION
      '已有 SOURCE 构建输入缺少精确数据源版本；请在升级前按原始发布快照重新登记';
  END IF;
END
$$;

ALTER TABLE platform.build_run_inputs
  ADD CONSTRAINT build_run_inputs_data_source_version_fk
    FOREIGN KEY(input_data_source_version_id,input_data_source_id,tenant_id)
    REFERENCES platform.data_source_versions(id,data_source_id,tenant_id)
    ON DELETE RESTRICT,
  ADD CONSTRAINT build_run_inputs_source_version_shape_check CHECK(
    (source_type IN ('SOURCE_TABLE','FILE_VERSION')
      AND input_data_source_id IS NOT NULL
      AND input_data_source_version_id IS NOT NULL)
    OR
    (source_type IN ('DATASET_VERSION','MATERIALIZATION')
      AND input_data_source_id IS NULL
      AND input_data_source_version_id IS NULL)
  );

CREATE OR REPLACE FUNCTION platform.enforce_build_run_input_layer()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,platform
AS $$
DECLARE
  target_layer text;
  source_matches boolean := false;
BEGIN
  SELECT layer INTO target_layer
  FROM platform.dataset_build_runs
  WHERE id=NEW.build_run_id AND tenant_id=NEW.tenant_id
  FOR SHARE;
  IF target_layer IS NULL
    OR (target_layer='ODS' AND NEW.input_layer<>'SOURCE')
    OR (target_layer='DWD' AND NEW.input_layer<>'ODS')
    OR (target_layer='DWS' AND NEW.input_layer<>'DWD') THEN
    RAISE EXCEPTION '构建输入层级不满足 ODS <- SOURCE、DWD <- ODS、DWS <- DWD'
      USING ERRCODE='23514';
  END IF;

  IF NEW.source_type='SOURCE_TABLE' THEN
    SELECT EXISTS(
      SELECT 1
      FROM platform.metadata_tables AS metadata_table
      JOIN platform.data_sources AS source
        ON source.id=metadata_table.data_source_id
       AND source.tenant_id=metadata_table.tenant_id
      JOIN platform.data_source_versions AS source_version
        ON source_version.id=NEW.input_data_source_version_id
       AND source_version.data_source_id=NEW.input_data_source_id
       AND source_version.data_source_id=source.id
       AND source_version.tenant_id=source.tenant_id
      WHERE metadata_table.id=NEW.metadata_table_id
        AND metadata_table.tenant_id=NEW.tenant_id
        AND metadata_table.data_source_id=NEW.input_data_source_id
        AND metadata_table.asset_status='ACTIVE'
        AND metadata_table.management_status='ENABLED'
        AND metadata_table.structure_hash=NEW.schema_hash
        AND source.status='ACTIVE'
        AND source.publication_status='PUBLISHED'
        AND source.current_published_version_id=source_version.id
        AND source_version.source_type IN ('MYSQL','ORACLE')
    ) INTO source_matches;
  ELSIF NEW.source_type='FILE_VERSION' THEN
    SELECT EXISTS(
      SELECT 1
      FROM platform.file_asset_versions AS file_version
      JOIN platform.data_source_versions AS source_version
        ON source_version.id=NEW.input_data_source_version_id
       AND source_version.data_source_id=NEW.input_data_source_id
       AND source_version.file_version_id=file_version.id
       AND source_version.file_asset_id=file_version.file_asset_id
       AND source_version.tenant_id=file_version.tenant_id
      JOIN platform.data_sources AS source
        ON source.id=NEW.input_data_source_id
       AND source.id=source_version.data_source_id
       AND source.tenant_id=source_version.tenant_id
       AND source.current_published_version_id=source_version.id
      WHERE file_version.id=NEW.file_version_id
        AND file_version.tenant_id=NEW.tenant_id
        AND file_version.sha256=NEW.snapshot_hash
        AND source.status='ACTIVE'
        AND source.publication_status='PUBLISHED'
        AND source_version.source_type='EXCEL'
    ) INTO source_matches;
  ELSIF NEW.source_type='DATASET_VERSION' THEN
    SELECT EXISTS(
      SELECT 1
      FROM platform.dataset_versions AS version
      JOIN platform.datasets AS owner
        ON owner.id=version.dataset_id AND owner.tenant_id=version.tenant_id
      WHERE version.id=NEW.input_dataset_version_id
        AND version.dataset_id=NEW.input_dataset_id
        AND version.tenant_id=NEW.tenant_id
        AND version.layer=NEW.input_layer
        AND version.status='PUBLISHED'
        AND owner.status='PUBLISHED'
        AND owner.current_published_version_id=version.id
        AND owner.deleted_at IS NULL
    ) INTO source_matches;
  ELSIF NEW.source_type='MATERIALIZATION' THEN
    SELECT EXISTS(
      SELECT 1
      FROM platform.dataset_materializations AS materialization
      JOIN platform.dataset_versions AS version
        ON version.id=materialization.dataset_version_id
       AND version.dataset_id=materialization.dataset_id
       AND version.tenant_id=materialization.tenant_id
      JOIN platform.datasets AS owner
        ON owner.id=materialization.dataset_id
       AND owner.tenant_id=materialization.tenant_id
      WHERE materialization.id=NEW.input_materialization_id
        AND materialization.tenant_id=NEW.tenant_id
        AND materialization.layer=NEW.input_layer
        AND materialization.status='ACTIVE'
        AND version.layer=NEW.input_layer
        AND version.status='PUBLISHED'
        AND owner.status='PUBLISHED'
        AND owner.current_published_version_id=version.id
        AND owner.deleted_at IS NULL
    ) INTO source_matches;
  END IF;

  IF NOT source_matches THEN
    RAISE EXCEPTION '构建输入未绑定精确的当前发布源、文件或上游物化'
      USING ERRCODE='23514';
  END IF;
  RETURN NEW;
END
$$;

REVOKE ALL ON FUNCTION platform.enforce_build_run_input_layer() FROM PUBLIC;

CREATE OR REPLACE FUNCTION platform.reject_build_run_input_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
  RAISE EXCEPTION '构建输入快照不可修改或删除' USING ERRCODE='23514';
END
$$;

REVOKE ALL ON FUNCTION platform.reject_build_run_input_mutation() FROM PUBLIC;

CREATE TRIGGER build_run_inputs_immutable
BEFORE UPDATE OR DELETE ON platform.build_run_inputs
FOR EACH ROW EXECUTE FUNCTION platform.reject_build_run_input_mutation();

CREATE OR REPLACE FUNCTION platform.enforce_dataset_layer_identity_after_publication()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,platform
AS $$
DECLARE
  owner_id uuid;
  owner_tenant_id uuid;
  candidate_layer text;
  published_version_id uuid;
  published_layer text;
BEGIN
  IF TG_TABLE_NAME='datasets' THEN
    owner_id=NEW.id;
    owner_tenant_id=NEW.tenant_id;
    candidate_layer=NEW.layer;
    -- 一旦存在旧发布版本，层级身份以旧发布事实为准。否则攻击者可以在
    -- 同一条 UPDATE 中同时替换发布指针和 layer，绕过首次发布后的锁定。
    published_version_id=COALESCE(
      OLD.current_published_version_id,
      NEW.current_published_version_id
    );
  ELSE
    owner_id=NEW.dataset_id;
    owner_tenant_id=NEW.tenant_id;
    candidate_layer=NEW.layer;
    SELECT current_published_version_id
    INTO published_version_id
    FROM platform.datasets
    WHERE id=owner_id AND tenant_id=owner_tenant_id
    FOR SHARE;
  END IF;

  IF published_version_id IS NULL THEN
    RETURN NEW;
  END IF;
  SELECT layer
  INTO published_layer
  FROM platform.dataset_versions
  WHERE id=published_version_id
    AND dataset_id=owner_id
    AND tenant_id=owner_tenant_id;
  IF NOT FOUND OR candidate_layer IS DISTINCT FROM published_layer THEN
    RAISE EXCEPTION '已发布数据集不能原地变更层级；请新建数据集并建立血缘'
      USING ERRCODE='23514';
  END IF;
  RETURN NEW;
END
$$;

REVOKE ALL ON FUNCTION platform.enforce_dataset_layer_identity_after_publication() FROM PUBLIC;

CREATE TRIGGER datasets_enforce_published_layer_identity
BEFORE UPDATE OF layer,current_published_version_id ON platform.datasets
FOR EACH ROW EXECUTE FUNCTION platform.enforce_dataset_layer_identity_after_publication();

CREATE TRIGGER dataset_versions_enforce_published_layer_identity
BEFORE INSERT OR UPDATE OF tenant_id,dataset_id,layer,status ON platform.dataset_versions
FOR EACH ROW EXECUTE FUNCTION platform.enforce_dataset_layer_identity_after_publication();

COMMENT ON FUNCTION platform.enforce_build_run_input_layer() IS
  '验证构建输入层级及 SOURCE_TABLE/FILE_VERSION/上游版本的精确当前发布状态';
COMMENT ON FUNCTION platform.enforce_dataset_layer_identity_after_publication() IS
  '首次发布后锁定 dataset 层级；跨 ODS/DWD/DWS 迁移必须使用新的 dataset 身份';
COMMENT ON COLUMN platform.build_run_inputs.input_data_source_version_id IS
  'SOURCE 输入使用的精确不可变数据源配置版本；禁止在构建期间跟随发布指针漂移';
