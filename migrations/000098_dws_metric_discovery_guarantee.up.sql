-- DWS 发布后自动登记指标识别任务。应用层发布回调仍保留；数据库触发器作为
-- 同事务兜底，确保任何合法发布入口都不会遗漏资产管理中心的指标候选。
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

CREATE TRIGGER dataset_versions_enqueue_dws_metric_discovery
AFTER UPDATE OF status ON platform.dataset_versions
FOR EACH ROW EXECUTE FUNCTION platform.enqueue_dws_metric_discovery();

-- 为迁移时已经发布的当前 DWS 版本补齐缺失的指标识别任务。
INSERT INTO platform.metric_extraction_jobs(
  tenant_id,dataset_id,dataset_version_id,dsl_hash,
  requested_by,extractor_version
)
SELECT
  version.tenant_id,version.dataset_id,version.id,version.schema_hash,
  version.published_by,'metric-candidate-v4'
FROM platform.dataset_versions AS version
JOIN platform.datasets AS dataset
  ON dataset.tenant_id=version.tenant_id
 AND dataset.id=version.dataset_id
 AND dataset.current_published_version_id=version.id
WHERE version.layer='DWS'
  AND version.status='PUBLISHED'
  AND dataset.status='PUBLISHED'
  AND dataset.deleted_at IS NULL
ON CONFLICT(tenant_id,dataset_version_id,extractor_version) DO NOTHING;

COMMENT ON FUNCTION platform.enqueue_dws_metric_discovery() IS
  'DWS 发布时幂等登记 metric-candidate-v4 指标候选识别；维度候选、画像和成员映射继续由 DWS 物化治理链路处理';
