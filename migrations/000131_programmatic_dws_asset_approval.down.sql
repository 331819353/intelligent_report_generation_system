-- 历史审批/候选终态和审计信息不可安全回滚；恢复旧触发器和表说明。
CREATE OR REPLACE FUNCTION platform.enqueue_dws_metric_discovery()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,platform
AS $$
BEGIN
  IF NEW.layer='DWS'
     AND NEW.status='PUBLISHED'
     AND OLD.status IS DISTINCT FROM 'PUBLISHED' THEN
    INSERT INTO platform.metric_extraction_jobs(
      tenant_id,dataset_id,dataset_version_id,dsl_hash,
      requested_by,extractor_version
    ) VALUES(
      NEW.tenant_id,NEW.dataset_id,NEW.id,NEW.schema_hash,
      NEW.published_by,'metric-candidate-v4'
    )
    ON CONFLICT(tenant_id,dataset_version_id,extractor_version) DO NOTHING;
  END IF;
  RETURN NEW;
END
$$;

REVOKE ALL ON FUNCTION platform.enqueue_dws_metric_discovery() FROM PUBLIC;

COMMENT ON FUNCTION platform.enqueue_dws_metric_discovery() IS
  'DWS 发布时幂等登记 metric-candidate-v4 指标候选识别；维度候选、画像和成员映射继续由 DWS 物化治理链路处理';

COMMENT ON TABLE platform.metric_candidates IS
  '待人工审核的指标定义建议；接受前不属于正式指标目录';
COMMENT ON TABLE platform.dimension_survey_candidates IS
  'DWS 精确物化快照上的维度治理候选';
