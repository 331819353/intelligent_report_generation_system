-- 把分层合同扩展为：
--   ODS <- SOURCE
--   DIM <- ODS
--   DWD <- at least one ODS + optional DIM
--   DWS <- DWD
--   ADS <- DWS
--
-- LLM 只生成结构化数据集 DSL 与 DAG；本迁移扩展控制面白名单，物理 SQL
-- 仍只能由受控 PostgreSQL 开发引擎从已发布 DSL 编译执行。

ALTER TABLE platform.dataset_versions
  DROP CONSTRAINT dataset_versions_layer_check,
  ADD CONSTRAINT dataset_versions_layer_check
    CHECK(layer IN ('ODS','DIM','DWD','DWS','ADS'));

ALTER TABLE platform.datasets
  DROP CONSTRAINT datasets_layer_check,
  ADD CONSTRAINT datasets_layer_check
    CHECK(layer IN ('ODS','DIM','DWD','DWS','ADS'));

ALTER TABLE platform.dataset_build_runs
  DROP CONSTRAINT dataset_build_runs_layer_check,
  ADD CONSTRAINT dataset_build_runs_layer_check
    CHECK(layer IN ('ODS','DIM','DWD','DWS','ADS'));

ALTER TABLE platform.dataset_materializations
  DROP CONSTRAINT dataset_materializations_layer_check,
  ADD CONSTRAINT dataset_materializations_layer_check
    CHECK(layer IN ('ODS','DIM','DWD','DWS','ADS')),
  DROP CONSTRAINT dataset_materializations_physical_schema_check,
  ADD CONSTRAINT dataset_materializations_physical_schema_check CHECK(
    physical_schema IN (
      'warehouse_ods','warehouse_dim','warehouse_dwd','warehouse_dws','warehouse_ads'
    )
  ),
  DROP CONSTRAINT dataset_materializations_layer_schema_check,
  ADD CONSTRAINT dataset_materializations_layer_schema_check CHECK(
    physical_schema=CASE layer
      WHEN 'ODS' THEN 'warehouse_ods'
      WHEN 'DIM' THEN 'warehouse_dim'
      WHEN 'DWD' THEN 'warehouse_dwd'
      WHEN 'DWS' THEN 'warehouse_dws'
      WHEN 'ADS' THEN 'warehouse_ads'
    END
  );

ALTER TABLE platform.build_run_inputs
  DROP CONSTRAINT build_run_inputs_input_layer_check,
  ADD CONSTRAINT build_run_inputs_input_layer_check
    CHECK(input_layer IN ('SOURCE','ODS','DIM','DWD','DWS','ADS')),
  DROP CONSTRAINT build_run_inputs_source_shape_check,
  ADD CONSTRAINT build_run_inputs_source_shape_check CHECK(
    (source_type='SOURCE_TABLE' AND input_layer='SOURCE'
      AND metadata_table_id IS NOT NULL AND file_version_id IS NULL
      AND input_dataset_id IS NULL AND input_dataset_version_id IS NULL
      AND input_materialization_id IS NULL)
    OR
    (source_type='FILE_VERSION' AND input_layer='SOURCE'
      AND metadata_table_id IS NULL AND file_version_id IS NOT NULL
      AND input_dataset_id IS NULL AND input_dataset_version_id IS NULL
      AND input_materialization_id IS NULL)
    OR
    (source_type='DATASET_VERSION'
      AND input_layer IN ('ODS','DIM','DWD','DWS','ADS')
      AND metadata_table_id IS NULL AND file_version_id IS NULL
      AND input_dataset_id IS NOT NULL AND input_dataset_version_id IS NOT NULL
      AND input_materialization_id IS NULL)
    OR
    (source_type='MATERIALIZATION'
      AND input_layer IN ('ODS','DIM','DWD','DWS','ADS')
      AND metadata_table_id IS NULL AND file_version_id IS NULL
      AND input_dataset_id IS NULL AND input_dataset_version_id IS NULL
      AND input_materialization_id IS NOT NULL)
  );

ALTER TABLE platform.dataset_tag_suggestion_jobs
  DROP CONSTRAINT dataset_tag_suggestion_jobs_layer_check,
  ADD CONSTRAINT dataset_tag_suggestion_jobs_layer_check
    CHECK(layer IN ('ODS','DIM','DWD','DWS','ADS'));

ALTER TABLE platform.query_run_materializations
  DROP CONSTRAINT query_run_materializations_layer_check,
  ADD CONSTRAINT query_run_materializations_layer_check
    CHECK(layer IN ('ODS','DIM','DWD','DWS','ADS')),
  DROP CONSTRAINT query_run_materializations_published_name_check,
  ADD CONSTRAINT query_run_materializations_published_name_check CHECK(
    published_name ~ '^(ods|dim|dwd|dws|ads)_t[0-9a-f]{12}_d[0-9a-f]{12}$'
  );

ALTER TABLE platform.query_candidate_run_materializations
  DROP CONSTRAINT query_candidate_run_materializations_layer_check,
  ADD CONSTRAINT query_candidate_run_materializations_layer_check
    CHECK(layer IN ('ODS','DIM','DWD','DWS','ADS')),
  DROP CONSTRAINT query_candidate_run_materializations_published_name_check,
  ADD CONSTRAINT query_candidate_run_materializations_published_name_check CHECK(
    published_name ~ '^(ods|dim|dwd|dws|ads)_t[0-9a-f]{12}_d[0-9a-f]{12}$'
  );

ALTER TABLE platform.dataset_materialization_cleanup_jobs
  DROP CONSTRAINT dataset_materialization_cleanup_jobs_layer_check,
  ADD CONSTRAINT dataset_materialization_cleanup_jobs_layer_check
    CHECK(layer IN ('DIM','DWD','DWS','ADS'));

-- ODS 分类为 DIMENSION/MASTER 后生成的 DIM 草稿使用独立所有权栅栏。
-- 当前草稿 hash 一旦被人工修改，后台只能跳过，不能覆盖。
CREATE TABLE platform.dim_modeling_outputs(
  id uuid PRIMARY KEY DEFAULT public.gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id),
  source_dataset_id uuid NOT NULL,
  dim_dataset_id uuid NOT NULL,
  domain_key text NOT NULL CHECK(
    length(domain_key) BETWEEN 1 AND 256
    AND domain_key=btrim(domain_key)
    AND domain_key !~ '[[:cntrl:]]'
  ),
  last_job_id uuid NOT NULL,
  last_input_hash text NOT NULL CHECK(last_input_hash ~ '^[0-9a-f]{64}$'),
  last_generated_schema_hash text NOT NULL
    CHECK(last_generated_schema_hash ~ '^[0-9a-f]{64}$'),
  last_action text NOT NULL CHECK(last_action IN ('CREATED','UPDATED','UNCHANGED')),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT dim_modeling_outputs_source_fk
    FOREIGN KEY(source_dataset_id,tenant_id)
    REFERENCES platform.datasets(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT dim_modeling_outputs_dim_fk
    FOREIGN KEY(dim_dataset_id,tenant_id)
    REFERENCES platform.datasets(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT dim_modeling_outputs_job_fk
    FOREIGN KEY(last_job_id,tenant_id)
    REFERENCES platform.dwd_modeling_jobs(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT dim_modeling_outputs_source_key UNIQUE(tenant_id,source_dataset_id),
  CONSTRAINT dim_modeling_outputs_dim_key UNIQUE(tenant_id,dim_dataset_id),
  CONSTRAINT dim_modeling_outputs_identity_tenant_key UNIQUE(id,tenant_id)
);

CREATE INDEX dim_modeling_outputs_domain_idx
  ON platform.dim_modeling_outputs(tenant_id,domain_key,source_dataset_id);

CREATE TRIGGER dim_modeling_outputs_set_updated_at
BEFORE UPDATE ON platform.dim_modeling_outputs
FOR EACH ROW EXECUTE FUNCTION platform.set_updated_at();

ALTER TABLE platform.dim_modeling_outputs ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform.dim_modeling_outputs FORCE ROW LEVEL SECURITY;

CREATE POLICY dim_modeling_outputs_tenant_isolation
  ON platform.dim_modeling_outputs
  USING(tenant_id=platform.current_tenant_id())
  WITH CHECK(tenant_id=platform.current_tenant_id());

COMMENT ON TABLE platform.dim_modeling_outputs IS
  'ODS 实体说明源与后台生成 DIM 草稿的稳定映射及人工修改保护栅栏';
COMMENT ON TABLE platform.dwd_modeling_jobs IS
  'ODS 发布后执行的同领域 DIM/DWD 分层建模 outbox；带租约、幂等和有界重试';

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
    OR (target_layer='DIM' AND NEW.input_layer<>'ODS')
    OR (target_layer='DWD' AND NEW.input_layer NOT IN ('ODS','DIM'))
    OR (target_layer='DWS' AND NEW.input_layer<>'DWD')
    OR (target_layer='ADS' AND NEW.input_layer<>'DWS') THEN
    RAISE EXCEPTION
      '构建输入层级不满足 ODS <- SOURCE、DIM <- ODS、DWD <- ODS+DIM、DWS <- DWD、ADS <- DWS'
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

-- DWD 可以关联任意数量的 DIM，但仍必须以至少一个 ODS 事实输入为根。
-- DWS 的物理输入只允许 DWD。使用 DEFERRABLE 约束触发器，是因为一次构建的
-- 多个冻结输入由同一事务逐行写入，不能在第一行插入时提前判断完整集合。
CREATE OR REPLACE FUNCTION platform.enforce_build_run_required_input_layers()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,platform
AS $$
DECLARE
  target_layer text;
  required_layer text;
BEGIN
  SELECT layer INTO target_layer
  FROM platform.dataset_build_runs
  WHERE id=NEW.build_run_id AND tenant_id=NEW.tenant_id
  FOR SHARE;

  required_layer := CASE target_layer
    WHEN 'ODS' THEN 'SOURCE'
    WHEN 'DIM' THEN 'ODS'
    WHEN 'DWD' THEN 'ODS'
    WHEN 'DWS' THEN 'DWD'
    WHEN 'ADS' THEN 'DWS'
  END;
  IF required_layer IS NULL OR NOT EXISTS(
    SELECT 1
    FROM platform.build_run_inputs
    WHERE build_run_id=NEW.build_run_id
      AND tenant_id=NEW.tenant_id
      AND input_layer=required_layer
  ) THEN
    RAISE EXCEPTION '构建运行缺少目标层要求的事实或来源输入'
      USING ERRCODE='23514';
  END IF;
  RETURN NULL;
END
$$;

REVOKE ALL ON FUNCTION
  platform.enforce_build_run_required_input_layers() FROM PUBLIC;

CREATE CONSTRAINT TRIGGER build_run_inputs_require_layer
AFTER INSERT ON platform.build_run_inputs
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION
  platform.enforce_build_run_required_input_layers();

-- ODS 是物理源映射。任何仍存在的 DIM 或 DWD 历史血缘都应阻止删除它，
-- 避免“只删除当前版本”破坏可审计 DAG。
CREATE OR REPLACE FUNCTION platform.prevent_referenced_ods_soft_delete()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,platform
AS $$
BEGIN
  IF OLD.layer='ODS'
     AND OLD.deleted_at IS NULL
     AND NEW.deleted_at IS NOT NULL
     AND EXISTS(
       SELECT 1
       FROM platform.dataset_versions AS source_version
       JOIN platform.dataset_dependencies AS dependency
         ON dependency.tenant_id=source_version.tenant_id
        AND dependency.source_type='DATASET_VERSION'
        AND dependency.source_id=source_version.id::text
       JOIN platform.dataset_versions AS downstream_version
         ON downstream_version.id=dependency.dataset_version_id
        AND downstream_version.tenant_id=dependency.tenant_id
        AND downstream_version.layer IN ('DIM','DWD')
       JOIN platform.datasets AS downstream_dataset
         ON downstream_dataset.id=downstream_version.dataset_id
        AND downstream_dataset.tenant_id=downstream_version.tenant_id
        AND downstream_dataset.deleted_at IS NULL
       WHERE source_version.tenant_id=OLD.tenant_id
         AND source_version.dataset_id=OLD.id
     ) THEN
    RAISE EXCEPTION 'ODS 数据集仍被 DIM 或 DWD 数据集引用'
      USING ERRCODE='23503',
        CONSTRAINT='datasets_ods_dwd_reference_guard';
  END IF;
  RETURN NEW;
END
$$;

REVOKE ALL ON FUNCTION platform.prevent_referenced_ods_soft_delete() FROM PUBLIC;

COMMENT ON COLUMN platform.datasets.layer IS
  '当前草稿数仓加工层级：ODS、DIM、DWD、DWS 或 ADS';
COMMENT ON COLUMN platform.dataset_versions.layer IS
  '精确版本不可变数仓加工层级：ODS、DIM、DWD、DWS 或 ADS';
