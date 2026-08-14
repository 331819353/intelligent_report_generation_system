CREATE OR REPLACE FUNCTION platform.enforce_build_run_input_layer()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,platform
AS $$
DECLARE
  target_layer text;
  source_backed_dws boolean := false;
  source_matches boolean := false;
BEGIN
  SELECT run.layer,
    COALESCE(version.dsl_json->'dataset'->>'sourceMode','')='PRE_AGGREGATED'
  INTO target_layer,source_backed_dws
  FROM platform.dataset_build_runs AS run
  JOIN platform.dataset_versions AS version
    ON version.id=run.dataset_version_id
   AND version.dataset_id=run.dataset_id
   AND version.tenant_id=run.tenant_id
  WHERE run.id=NEW.build_run_id AND run.tenant_id=NEW.tenant_id
  FOR SHARE OF run,version;

  IF target_layer IS NULL
    OR (target_layer='ODS' AND NEW.input_layer<>'SOURCE')
    OR (target_layer='DIM' AND NEW.input_layer<>'ODS')
    OR (target_layer='DWD' AND NEW.input_layer NOT IN ('ODS','DIM'))
    OR (target_layer='DWS' AND NOT (
      NEW.input_layer IN ('DWD','DIM')
      OR (NEW.input_layer='SOURCE' AND source_backed_dws)
    ))
    OR (target_layer='ADS' AND NEW.input_layer<>'DWS') THEN
    RAISE EXCEPTION
      '构建输入层级不满足 ODS <- SOURCE、DIM <- ODS、DWD <- ODS+DIM、DWS <- DWD|DIM|PRE_AGGREGATED SOURCE、ADS <- DWS'
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
