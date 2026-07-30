-- DWS 建模已支持直接基于治理 DIM 生成无事实实体计数主题。控制面与查询
-- 运行时均允许 DWS <- DWD|DIM，这里同步修正仍只接受 DWD 的延迟数据库栅栏。
CREATE OR REPLACE FUNCTION platform.enforce_build_run_required_input_layers()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,platform
AS $$
DECLARE
  target_layer text;
  required_layer text;
  has_required_input boolean := false;
BEGIN
  SELECT layer INTO target_layer
  FROM platform.dataset_build_runs
  WHERE id=NEW.build_run_id AND tenant_id=NEW.tenant_id
  FOR SHARE;

  IF target_layer='DWS' THEN
    SELECT EXISTS(
      SELECT 1
      FROM platform.build_run_inputs
      WHERE build_run_id=NEW.build_run_id
        AND tenant_id=NEW.tenant_id
        AND input_layer IN ('DWD','DIM')
    ) INTO has_required_input;
  ELSE
    required_layer := CASE target_layer
      WHEN 'ODS' THEN 'SOURCE'
      WHEN 'DIM' THEN 'ODS'
      WHEN 'DWD' THEN 'ODS'
      WHEN 'ADS' THEN 'DWS'
    END;
    IF required_layer IS NOT NULL THEN
      SELECT EXISTS(
        SELECT 1
        FROM platform.build_run_inputs
        WHERE build_run_id=NEW.build_run_id
          AND tenant_id=NEW.tenant_id
          AND input_layer=required_layer
      ) INTO has_required_input;
    END IF;
  END IF;

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
  '延迟校验构建输入根层：ODS<-SOURCE、DIM<-ODS、DWD<-ODS、DWS<-DWD|DIM、ADS<-DWS';
