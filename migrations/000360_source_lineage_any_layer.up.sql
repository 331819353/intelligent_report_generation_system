-- 层级定义重构：layer 只描述数据集的粒度合同（ODS 贴源 / DIM 实体 / DWD 明细 /
-- DWS 汇总 / ADS 应用），血缘方式由 DSL 拓扑推导：
--   * SOURCE  源表直落：恰好一个物理 TABLE 节点、无 Join。物理表（含导入表、既有
--             宽表）可以按声明层级直接进入数仓并保持源表既有粒度；五个层级都允许。
--   * MODELED 分层加工：全部节点引用已发布数据集版本，上游层级必须满足
--             ODS→DIM/DWD→DWS→ADS 方向。
-- 早期只有 ODS 与带 sourceMode=PRE_AGGREGATED 标记的 DWS 可以直落；下面的数据库
-- 栅栏一律改为按拓扑判断，PRE_AGGREGATED 只作为历史标记继续被接受。
BEGIN;

CREATE OR REPLACE FUNCTION platform.dataset_version_is_source_lineage(
  candidate platform.dataset_versions
)
RETURNS boolean
LANGUAGE sql
IMMUTABLE
SET search_path=pg_catalog,platform
AS $$
  SELECT jsonb_typeof(candidate.dsl_json->'nodes')='array'
    AND jsonb_array_length(candidate.dsl_json->'nodes')=1
    AND candidate.dsl_json#>>'{nodes,0,type}'='TABLE'
    AND COALESCE(jsonb_array_length(
      CASE WHEN jsonb_typeof(candidate.dsl_json->'joins')='array'
           THEN candidate.dsl_json->'joins' ELSE '[]'::jsonb END
    ),0)=0
$$;

REVOKE ALL ON FUNCTION
  platform.dataset_version_is_source_lineage(platform.dataset_versions)
FROM PUBLIC;

COMMENT ON FUNCTION
  platform.dataset_version_is_source_lineage(platform.dataset_versions)
IS 'SOURCE 血缘判定：单个物理 TABLE 节点且无 Join 的数据集版本，可直落任意层级';

-- 系统映射发布来源证明：任何单表源直落都是平台自己生成的映射文档形状，
-- 层级由分类器（或用户）声明，不再要求 ODS / PRE_AGGREGATED DWS。
CREATE OR REPLACE FUNCTION platform.system_mapped_source_layer_allowed(
  candidate platform.dataset_versions
)
RETURNS boolean
LANGUAGE sql
IMMUTABLE
SET search_path=pg_catalog,platform
AS $$
  SELECT candidate.layer IN ('ODS','DIM','DWD','DWS','ADS')
    AND platform.dataset_version_is_source_lineage(candidate)
$$;

REVOKE ALL ON FUNCTION
  platform.system_mapped_source_layer_allowed(platform.dataset_versions)
FROM PUBLIC;

COMMENT ON FUNCTION
  platform.system_mapped_source_layer_allowed(platform.dataset_versions)
IS '系统映射发布只允许单表源直落文档；层级由分类器或用户声明';

-- 领域血缘栅栏：SOURCE 血缘直接继承物理资产领域；MODELED 血缘必须与所有已发布
-- 上游领域一致。
CREATE OR REPLACE FUNCTION platform.enforce_dataset_domain_lineage()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,platform
AS $$
DECLARE
  target_domain_id uuid;
  dependency_count integer;
  mismatched_count integer;
BEGIN
  IF NEW.status NOT IN ('PUBLISHING','PUBLISHED') THEN
    RETURN NEW;
  END IF;

  SELECT dataset.domain_id
  INTO target_domain_id
  FROM platform.datasets AS dataset
  WHERE dataset.tenant_id=NEW.tenant_id
    AND dataset.id=NEW.dataset_id
    AND dataset.deleted_at IS NULL;
  IF target_domain_id IS NULL THEN
    RAISE EXCEPTION 'published dataset domain is required'
      USING ERRCODE='23514',CONSTRAINT='dataset_versions_domain_required';
  END IF;
  IF platform.dataset_version_is_source_lineage(NEW) THEN
    RETURN NEW;
  END IF;

  SELECT count(*)::integer,
    count(*) FILTER(
      WHERE upstream.id IS NULL
         OR upstream_dataset.id IS NULL
         OR upstream_dataset.domain_id<>target_domain_id
    )::integer
  INTO dependency_count,mismatched_count
  FROM jsonb_array_elements(
    COALESCE(NEW.dsl_json->'nodes','[]'::jsonb)
  ) AS node
  LEFT JOIN platform.dataset_versions AS upstream
    ON upstream.tenant_id=NEW.tenant_id
   AND upstream.id=(node->>'datasetVersionId')::uuid
   AND upstream.status='PUBLISHED'
  LEFT JOIN platform.datasets AS upstream_dataset
    ON upstream_dataset.tenant_id=upstream.tenant_id
   AND upstream_dataset.id=upstream.dataset_id
   AND upstream_dataset.deleted_at IS NULL
  WHERE node->>'type'='DATASET';

  IF dependency_count=0 OR mismatched_count>0 THEN
    RAISE EXCEPTION 'downstream dataset domain must equal every published upstream domain'
      USING ERRCODE='23514',CONSTRAINT='dataset_versions_domain_lineage_match';
  END IF;
  RETURN NEW;
END
$$;

COMMENT ON FUNCTION platform.enforce_dataset_domain_lineage() IS
  '发布时按 datasets.domain_id 校验精确上游领域；单表源直落按物理来源直接继承资产领域';

-- 构建输入层级栅栏：SOURCE 输入只允许出现在 SOURCE 血缘版本的构建里（任意层级）；
-- 仓库输入按 MODELED 方向 DIM<-ODS、DWD<-ODS|DIM、DWS<-DWD|DIM、ADS<-DWS。
CREATE OR REPLACE FUNCTION platform.enforce_build_run_input_layer()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,platform
AS $$
DECLARE
  target_layer text;
  source_lineage boolean := false;
  source_matches boolean := false;
BEGIN
  SELECT run.layer,
    platform.dataset_version_is_source_lineage(version)
  INTO target_layer,source_lineage
  FROM platform.dataset_build_runs AS run
  JOIN platform.dataset_versions AS version
    ON version.id=run.dataset_version_id
   AND version.dataset_id=run.dataset_id
   AND version.tenant_id=run.tenant_id
  WHERE run.id=NEW.build_run_id AND run.tenant_id=NEW.tenant_id
  FOR SHARE OF run,version;

  IF target_layer IS NULL
    OR target_layer NOT IN ('ODS','DIM','DWD','DWS','ADS')
    OR (NEW.input_layer='SOURCE' AND NOT source_lineage)
    OR (NEW.input_layer<>'SOURCE' AND source_lineage)
    OR (target_layer='ODS' AND NEW.input_layer<>'SOURCE')
    OR (target_layer='DIM' AND NEW.input_layer NOT IN ('SOURCE','ODS'))
    OR (target_layer='DWD' AND NEW.input_layer NOT IN ('SOURCE','ODS','DIM'))
    OR (target_layer='DWS' AND NEW.input_layer NOT IN ('SOURCE','DWD','DIM'))
    OR (target_layer='ADS' AND NEW.input_layer NOT IN ('SOURCE','DWS')) THEN
    RAISE EXCEPTION
      '构建输入层级不满足：单表源直落任意层级 <- SOURCE；分层加工 DIM <- ODS、DWD <- ODS+DIM、DWS <- DWD|DIM、ADS <- DWS'
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
        AND source_version.source_type<>'EXCEL'
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

COMMENT ON FUNCTION platform.enforce_build_run_input_layer() IS
  '构建输入层级栅栏：SOURCE 输入只服务单表源直落版本（任意层级）；仓库输入按 ODS→DIM/DWD→DWS→ADS 方向';

-- 延迟根输入栅栏：每个层级都接受 SOURCE 作为根输入（单表源直落）；MODELED 血缘
-- 继续要求 DIM<-ODS、DWD<-ODS、DWS<-DWD|DIM、ADS<-DWS。SOURCE 输入本身已由上面的
-- 行级栅栏限定只能出现在 SOURCE 血缘版本中。
CREATE OR REPLACE FUNCTION platform.enforce_build_run_required_input_layers()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,platform
AS $$
DECLARE
  target_layer text;
  has_required_input boolean := false;
BEGIN
  SELECT layer INTO target_layer
  FROM platform.dataset_build_runs
  WHERE id=NEW.build_run_id AND tenant_id=NEW.tenant_id
  FOR SHARE;

  SELECT EXISTS(
    SELECT 1
    FROM platform.build_run_inputs
    WHERE build_run_id=NEW.build_run_id
      AND tenant_id=NEW.tenant_id
      AND (
        input_layer='SOURCE'
        OR (target_layer='DIM' AND input_layer='ODS')
        OR (target_layer='DWD' AND input_layer='ODS')
        OR (target_layer='DWS' AND input_layer IN ('DWD','DIM'))
        OR (target_layer='ADS' AND input_layer='DWS')
      )
  ) INTO has_required_input;

  IF NOT has_required_input THEN
    RAISE EXCEPTION '构建运行缺少目标层要求的事实、维度或来源输入'
      USING ERRCODE='23514';
  END IF;
  RETURN NULL;
END
$$;

REVOKE ALL ON FUNCTION
  platform.enforce_build_run_required_input_layers() FROM PUBLIC;

COMMENT ON FUNCTION platform.enforce_build_run_required_input_layers() IS
  '延迟校验构建输入根层：任意层级<-SOURCE（单表源直落）；DIM<-ODS、DWD<-ODS、DWS<-DWD|DIM、ADS<-DWS';

COMMIT;
