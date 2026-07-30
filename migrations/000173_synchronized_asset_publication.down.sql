-- 发布版本和审计记录是不可变事实，回滚代码版本时不删除已经补齐的同步指标
-- 发布快照。旧代码仍可正常读取这些标准指标版本。
CREATE OR REPLACE FUNCTION
  platform.auto_verify_rule_dimension_metric_compatibility()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,platform
AS $$
DECLARE
  actor_id uuid;
BEGIN
  IF NEW.status<>'PROPOSED'
     OR NEW.evidence_source<>'RULE'
     OR NEW.compatibility_type<>'DIRECT'
     OR NEW.fanout_policy<>'SAFE'
     OR NEW.confidence IS DISTINCT FROM 1.0000
     OR NEW.join_path_json<>'[]'::jsonb THEN
    RETURN NEW;
  END IF;

  actor_id := COALESCE(NEW.updated_by,NEW.created_by);
  IF actor_id IS NULL THEN
    RETURN NEW;
  END IF;

  UPDATE platform.dimension_metric_compatibility
  SET status='VERIFIED',verified_by=actor_id,verified_at=now(),
      version=NEW.version+1,updated_by=actor_id
  WHERE id=NEW.id AND tenant_id=NEW.tenant_id AND status='PROPOSED';

  IF FOUND THEN
    INSERT INTO platform.audit_logs(
      tenant_id,actor_user_id,action,resource_type,resource_id,detail
    ) VALUES(
      NEW.tenant_id,actor_id,
      'DIMENSION_METRIC_COMPATIBILITY_RULE_VERIFY',
      'DIMENSION_METRIC_COMPATIBILITY',NEW.id::text,
      jsonb_build_object(
        'dimensionId',NEW.dimension_id::text,
        'metricVersionId',NEW.metric_version_id::text,
        'evidenceSource','RULE',
        'decision','DIRECT_SAFE_SAME_DWS'
      )
    );
  END IF;
  RETURN NEW;
END
$$;

REVOKE ALL ON FUNCTION
  platform.auto_verify_rule_dimension_metric_compatibility()
FROM PUBLIC;
