-- 从已发布 DWS/ADS 数据集同步得到的指标属于数据集派生资产，不再停留在
-- 候选接受后的草稿态。为既有同步指标补齐不可变发布版本；后续版本由指标
-- 仓储在候选接受或编辑保存事务中自动生成。
--
-- RULE 兼容关系只有在同一 DWS 的维度、指标和物化都可用时才自动 VERIFIED；
-- 未完成物化不应阻断指标发布，而是保留 PROPOSED 等待后续治理。
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
  IF actor_id IS NULL OR NOT EXISTS(
    SELECT 1
    FROM platform.semantic_dimensions AS dimension
    JOIN platform.dataset_versions AS dimension_version
      ON dimension_version.tenant_id=dimension.tenant_id
     AND dimension_version.id=dimension.dataset_version_id
     AND dimension_version.dataset_id=dimension.dataset_id
    JOIN platform.metric_versions AS metric_version
      ON metric_version.tenant_id=dimension.tenant_id
     AND metric_version.id=NEW.metric_version_id
     AND metric_version.metric_id=NEW.metric_id
     AND metric_version.dataset_version_id=NEW.metric_dataset_version_id
    JOIN platform.metrics AS metric
      ON metric.tenant_id=metric_version.tenant_id
     AND metric.id=metric_version.metric_id
    WHERE dimension.id=NEW.dimension_id
      AND dimension.tenant_id=NEW.tenant_id
      AND dimension.status='PUBLISHED'
      AND dimension_version.layer='DWS'
      AND dimension_version.status='PUBLISHED'
      AND metric_version.status='PUBLISHED'
      AND metric.status='PUBLISHED'
      AND metric.current_published_version_id=metric_version.id
      AND metric.deleted_at IS NULL
      AND EXISTS(
        SELECT 1
        FROM platform.dataset_materializations AS materialization
        WHERE materialization.tenant_id=dimension.tenant_id
          AND materialization.dataset_id=dimension.dataset_id
          AND materialization.dataset_version_id=dimension.dataset_version_id
          AND materialization.layer='DWS'
          AND materialization.status='ACTIVE'
      )
      AND EXISTS(
        SELECT 1
        FROM platform.dataset_materializations AS materialization
        WHERE materialization.tenant_id=metric_version.tenant_id
          AND materialization.dataset_id=metric_version.dataset_id
          AND materialization.dataset_version_id=metric_version.dataset_version_id
          AND materialization.layer='DWS'
          AND materialization.status='ACTIVE'
      )
  ) THEN
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

WITH accepted AS (
  SELECT DISTINCT ON (candidate.tenant_id,candidate.accepted_metric_id)
    candidate.tenant_id,candidate.accepted_metric_id,candidate.id,candidate.reviewed_by
  FROM platform.metric_candidates AS candidate
  WHERE candidate.status='ACCEPTED'
    AND candidate.accepted_metric_id IS NOT NULL
  ORDER BY
    candidate.tenant_id,candidate.accepted_metric_id,
    candidate.updated_at DESC,candidate.id
)
UPDATE platform.metrics AS metric
SET origin_candidate_id=accepted.id,
    updated_by=COALESCE(metric.updated_by,accepted.reviewed_by,metric.created_by)
FROM accepted
WHERE metric.origin_candidate_id IS NULL
  AND metric.deleted_at IS NULL
  AND accepted.tenant_id=metric.tenant_id
  AND accepted.accepted_metric_id=metric.id;

CREATE TEMP TABLE synchronized_metric_publication_backfill
ON COMMIT DROP
AS
SELECT
  gen_random_uuid() AS published_version_id,
  metric.tenant_id,
  metric.id AS metric_id,
  metric.dataset_id,
  draft.dataset_version_id,
  draft.id AS draft_version_id,
  draft.version_no,
  draft.record_version AS draft_record_version,
  draft.definition_version,
  draft.definition_json,
  draft.definition_hash,
  COALESCE(
    metric.updated_by,
    metric.created_by,
    candidate.reviewed_by,
    dataset_version.published_by
  ) AS actor_user_id
FROM platform.metrics AS metric
JOIN LATERAL (
  SELECT accepted.*
  FROM platform.metric_candidates AS accepted
  WHERE accepted.tenant_id=metric.tenant_id
    AND accepted.status='ACCEPTED'
    AND accepted.accepted_metric_id=metric.id
  ORDER BY
    (accepted.id=metric.origin_candidate_id) DESC,
    accepted.updated_at DESC,
    accepted.id
  LIMIT 1
) AS candidate ON true
JOIN platform.metric_versions AS draft
  ON draft.tenant_id=metric.tenant_id
 AND draft.metric_id=metric.id
 AND draft.id=metric.current_draft_version_id
 AND draft.status='DRAFT'
JOIN platform.datasets AS dataset
  ON dataset.tenant_id=metric.tenant_id
 AND dataset.id=metric.dataset_id
 AND dataset.current_published_version_id=draft.dataset_version_id
 AND dataset.status='PUBLISHED'
 AND dataset.deleted_at IS NULL
JOIN platform.dataset_versions AS dataset_version
  ON dataset_version.tenant_id=dataset.tenant_id
 AND dataset_version.dataset_id=dataset.id
 AND dataset_version.id=draft.dataset_version_id
 AND dataset_version.status='PUBLISHED'
 AND dataset_version.layer IN ('DWS','ADS')
LEFT JOIN platform.metric_versions AS current_published
  ON current_published.tenant_id=metric.tenant_id
 AND current_published.metric_id=metric.id
 AND current_published.id=metric.current_published_version_id
WHERE metric.deleted_at IS NULL
  AND (
    current_published.id IS NULL
    OR current_published.definition_hash IS DISTINCT FROM draft.definition_hash
    OR current_published.source_draft_record_version IS DISTINCT FROM draft.record_version
  );

UPDATE platform.metric_versions AS draft
SET version_no=draft.version_no+1,
    updated_by=backfill.actor_user_id
FROM synchronized_metric_publication_backfill AS backfill
WHERE draft.id=backfill.draft_version_id
  AND draft.status='DRAFT'
  AND draft.record_version=backfill.draft_record_version;

INSERT INTO platform.metric_versions(
  id,tenant_id,metric_id,dataset_id,dataset_version_id,version_no,status,
  definition_version,definition_json,definition_hash,record_version,
  created_by,updated_by,published_at,published_by,
  source_draft_version_id,source_draft_record_version
)
SELECT
  published_version_id,tenant_id,metric_id,dataset_id,dataset_version_id,
  version_no,'PUBLISHING',definition_version,definition_json,definition_hash,1,
  actor_user_id,actor_user_id,now(),actor_user_id,
  draft_version_id,draft_record_version
FROM synchronized_metric_publication_backfill;

INSERT INTO platform.metric_dimensions(
  tenant_id,metric_version_id,metric_id,dataset_version_id,field_id,
  dimension_name,hierarchy_field_ids,sort_direction,null_label,ordinal_position
)
SELECT
  dimension.tenant_id,backfill.published_version_id,dimension.metric_id,
  dimension.dataset_version_id,dimension.field_id,dimension.dimension_name,
  dimension.hierarchy_field_ids,dimension.sort_direction,dimension.null_label,
  dimension.ordinal_position
FROM synchronized_metric_publication_backfill AS backfill
JOIN platform.metric_dimensions AS dimension
  ON dimension.tenant_id=backfill.tenant_id
 AND dimension.metric_version_id=backfill.draft_version_id;

INSERT INTO platform.metric_dependencies(
  tenant_id,metric_version_id,metric_id,dataset_version_id,
  dependency_metric_version_id,dependency_metric_id
)
SELECT
  dependency.tenant_id,backfill.published_version_id,dependency.metric_id,
  dependency.dataset_version_id,dependency.dependency_metric_version_id,
  dependency.dependency_metric_id
FROM synchronized_metric_publication_backfill AS backfill
JOIN platform.metric_dependencies AS dependency
  ON dependency.tenant_id=backfill.tenant_id
 AND dependency.metric_version_id=backfill.draft_version_id;

UPDATE platform.metrics AS metric
SET current_published_version_id=backfill.published_version_id,
    status='PUBLISHED',
    version=metric.version+1,
    updated_by=backfill.actor_user_id
FROM synchronized_metric_publication_backfill AS backfill
WHERE metric.id=backfill.metric_id
  AND metric.tenant_id=backfill.tenant_id;

UPDATE platform.metric_versions AS published
SET status='PUBLISHED'
FROM synchronized_metric_publication_backfill AS backfill
WHERE published.id=backfill.published_version_id
  AND published.status='PUBLISHING';

INSERT INTO platform.audit_logs(
  tenant_id,actor_user_id,action,resource_type,resource_id,detail
)
SELECT
  tenant_id,actor_user_id,'SYNC_PUBLISH','METRIC',metric_id::text,
  jsonb_build_object(
    'metricVersionId',published_version_id::text,
    'versionNo',version_no,
    'draftVersionId',draft_version_id::text,
    'draftRecordVersion',draft_record_version,
    'datasetVersionId',dataset_version_id::text,
    'definitionHash',definition_hash,
    'backfill',true
  )
FROM synchronized_metric_publication_backfill;
