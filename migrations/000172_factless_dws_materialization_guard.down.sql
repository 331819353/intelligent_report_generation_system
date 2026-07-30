-- 回滚到仅允许 DWS 使用 DWD 事实输入的旧栅栏。
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

COMMENT ON FUNCTION platform.enforce_build_run_required_input_layers() IS
  '延迟校验构建输入根层：ODS<-SOURCE、DIM<-ODS、DWD<-ODS、DWS<-DWD、ADS<-DWS';
