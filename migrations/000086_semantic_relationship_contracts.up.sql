-- 语义关系合同与成员检索优化。
--
-- 语义维度和指标继续固定在已发布 DWS 上；ADS 只消费 DWS，不反向成为指标
-- 或维度的事实来源。本迁移把原先“任意安全 JSON”收紧为有界、可拓扑验证的
-- Join hop，并为租户级成员/别名精确检索增加以 normalized value 开头的索引。

CREATE OR REPLACE FUNCTION platform.semantic_join_path_is_valid(
  selected_path jsonb,
  selected_compatibility_type text,
  selected_fanout_policy text
)
RETURNS boolean
LANGUAGE plpgsql
IMMUTABLE
STRICT
AS $$
DECLARE
  path_length integer;
  path_index integer;
  hop jsonb;
  previous_to_dataset_version_id text := '';
  cardinality text;
BEGIN
  IF jsonb_typeof(selected_path)<>'array'
    OR selected_compatibility_type NOT IN ('DIRECT','BRIDGE','DERIVED')
    OR selected_fanout_policy NOT IN ('SAFE','DEDUPLICATE','UNSAFE') THEN
    RETURN false;
  END IF;

  path_length := jsonb_array_length(selected_path);
  IF path_length>8
    OR (selected_compatibility_type='DIRECT' AND path_length>1)
    OR (selected_compatibility_type='BRIDGE' AND path_length<2)
    OR (path_length=0 AND selected_compatibility_type NOT IN ('DIRECT','DERIVED')) THEN
    RETURN false;
  END IF;

  FOR path_index IN 0..path_length-1 LOOP
    hop := selected_path->path_index;
    IF jsonb_typeof(hop)<>'object'
      OR (SELECT count(*) FROM jsonb_object_keys(hop))<>5
      OR NOT hop ?& ARRAY[
        'fromDatasetVersionId','fromFieldId',
        'toDatasetVersionId','toFieldId','cardinality'
      ] THEN
      RETURN false;
    END IF;
    IF (hop->>'fromDatasetVersionId') !~
         '^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[1-5][0-9a-fA-F]{3}-[89aAbB][0-9a-fA-F]{3}-[0-9a-fA-F]{12}$'
      OR (hop->>'toDatasetVersionId') !~
         '^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[1-5][0-9a-fA-F]{3}-[89aAbB][0-9a-fA-F]{3}-[0-9a-fA-F]{12}$'
      OR length(hop->>'fromFieldId') NOT BETWEEN 1 AND 256
      OR length(hop->>'toFieldId') NOT BETWEEN 1 AND 256
      OR (hop->>'fromFieldId')<>btrim(hop->>'fromFieldId')
      OR (hop->>'toFieldId')<>btrim(hop->>'toFieldId')
      OR (hop->>'fromFieldId') ~ '[[:cntrl:]]'
      OR (hop->>'toFieldId') ~ '[[:cntrl:]]'
      OR (
        hop->>'fromDatasetVersionId'=hop->>'toDatasetVersionId'
        AND hop->>'fromFieldId'=hop->>'toFieldId'
      ) THEN
      RETURN false;
    END IF;

    cardinality := hop->>'cardinality';
    IF cardinality NOT IN (
      'ONE_TO_ONE','MANY_TO_ONE','ONE_TO_MANY','MANY_TO_MANY'
    ) OR (
      selected_fanout_policy='SAFE'
      AND cardinality NOT IN ('ONE_TO_ONE','MANY_TO_ONE')
    ) THEN
      RETURN false;
    END IF;
    IF previous_to_dataset_version_id<>''
      AND hop->>'fromDatasetVersionId'<>previous_to_dataset_version_id THEN
      RETURN false;
    END IF;
    previous_to_dataset_version_id := hop->>'toDatasetVersionId';
  END LOOP;
  RETURN true;
END
$$;

REVOKE ALL ON FUNCTION platform.semantic_join_path_is_valid(
  jsonb,text,text
) FROM PUBLIC;

ALTER TABLE platform.dimension_metric_compatibility
  ADD CONSTRAINT dimension_metric_compatibility_join_path_contract_check
  CHECK(platform.semantic_join_path_is_valid(
    join_path_json,compatibility_type,fanout_policy
  )) NOT VALID;

-- NOT VALID 有意保留已有关系供治理人员查看；新写入和任何后续更新都必须遵守
-- 新合同。历史不合格关系不能绕过现有 VERIFIED 主体/物化复核继续演进。

-- 同一精确 DWS 上，指标定义已经列入 allowedDimensions 的一级语义维度是
-- 可确定的 DIRECT + SAFE 关系。优先用规则生成待审关系，避免让 LLM 猜测已经
-- 存在于控制面的事实；跨 DWS/桥接/派生关系仍由 LLM 或人工提议。
CREATE OR REPLACE FUNCTION
  platform.propose_metric_semantic_dimension_compatibility()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,platform
AS $$
BEGIN
  IF NEW.status='PUBLISHED'
    AND (
      TG_OP='INSERT'
      OR OLD.status IS DISTINCT FROM 'PUBLISHED'
    ) THEN
    WITH inserted AS (
      INSERT INTO platform.dimension_metric_compatibility(
        tenant_id,dimension_id,metric_id,metric_version_id,
        metric_dataset_version_id,compatibility_type,fanout_policy,
        join_path_json,evidence_source,confidence,status,created_by,updated_by
      )
      SELECT
        NEW.tenant_id,dimension.id,NEW.metric_id,NEW.id,
        NEW.dataset_version_id,'DIRECT','SAFE','[]'::jsonb,
        'RULE',1.0000,'PROPOSED',NEW.published_by,NEW.published_by
      FROM platform.metric_dimensions AS metric_dimension
      JOIN platform.semantic_dimensions AS dimension
        ON dimension.tenant_id=metric_dimension.tenant_id
       AND dimension.dataset_version_id=metric_dimension.dataset_version_id
       AND dimension.field_id=metric_dimension.field_id
       AND dimension.status='PUBLISHED'
      JOIN platform.dataset_versions AS version
        ON version.tenant_id=dimension.tenant_id
       AND version.id=dimension.dataset_version_id
       AND version.dataset_id=dimension.dataset_id
       AND version.layer='DWS'
       AND version.status='PUBLISHED'
      JOIN platform.datasets AS dataset
        ON dataset.tenant_id=version.tenant_id
       AND dataset.id=version.dataset_id
       AND dataset.layer='DWS'
       AND dataset.status='PUBLISHED'
       AND dataset.current_published_version_id=version.id
       AND dataset.deleted_at IS NULL
      WHERE metric_dimension.tenant_id=NEW.tenant_id
        AND metric_dimension.metric_version_id=NEW.id
        AND metric_dimension.metric_id=NEW.metric_id
        AND metric_dimension.dataset_version_id=NEW.dataset_version_id
      ON CONFLICT(
        tenant_id,dimension_id,metric_version_id
      ) DO NOTHING
      RETURNING id,dimension_id,metric_version_id,created_by
    )
    INSERT INTO platform.audit_logs(
      tenant_id,actor_user_id,action,resource_type,resource_id,detail
    )
    SELECT
      NEW.tenant_id,inserted.created_by,
      'DIMENSION_METRIC_COMPATIBILITY_RULE_PROPOSE',
      'DIMENSION_METRIC_COMPATIBILITY',inserted.id::text,
      jsonb_build_object(
        'dimensionId',inserted.dimension_id::text,
        'metricVersionId',inserted.metric_version_id::text,
        'evidenceSource','RULE'
      )
    FROM inserted;
  END IF;
  RETURN NEW;
END
$$;

REVOKE ALL ON FUNCTION
  platform.propose_metric_semantic_dimension_compatibility()
FROM PUBLIC;

CREATE TRIGGER metric_versions_propose_semantic_dimension_compatibility
AFTER INSERT OR UPDATE OF status ON platform.metric_versions
FOR EACH ROW EXECUTE FUNCTION
  platform.propose_metric_semantic_dimension_compatibility();

CREATE OR REPLACE FUNCTION
  platform.propose_dimension_metric_compatibility()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,platform
AS $$
BEGIN
  IF NEW.status='PUBLISHED'
    AND (
      TG_OP='INSERT'
      OR OLD.status IS DISTINCT FROM 'PUBLISHED'
    ) THEN
    WITH inserted AS (
      INSERT INTO platform.dimension_metric_compatibility(
        tenant_id,dimension_id,metric_id,metric_version_id,
        metric_dataset_version_id,compatibility_type,fanout_policy,
        join_path_json,evidence_source,confidence,status,created_by,updated_by
      )
      SELECT
        NEW.tenant_id,NEW.id,metric_version.metric_id,metric_version.id,
        metric_version.dataset_version_id,'DIRECT','SAFE','[]'::jsonb,
        'RULE',1.0000,'PROPOSED',NEW.updated_by,NEW.updated_by
      FROM platform.metric_dimensions AS metric_dimension
      JOIN platform.metric_versions AS metric_version
        ON metric_version.tenant_id=metric_dimension.tenant_id
       AND metric_version.id=metric_dimension.metric_version_id
       AND metric_version.metric_id=metric_dimension.metric_id
       AND metric_version.dataset_version_id=
         metric_dimension.dataset_version_id
       AND metric_version.status='PUBLISHED'
      JOIN platform.metrics AS metric
        ON metric.tenant_id=metric_version.tenant_id
       AND metric.id=metric_version.metric_id
       AND metric.status='PUBLISHED'
       AND metric.current_published_version_id=metric_version.id
       AND metric.deleted_at IS NULL
      WHERE metric_dimension.tenant_id=NEW.tenant_id
        AND metric_dimension.dataset_version_id=NEW.dataset_version_id
        AND metric_dimension.field_id=NEW.field_id
      ON CONFLICT(
        tenant_id,dimension_id,metric_version_id
      ) DO NOTHING
      RETURNING id,dimension_id,metric_version_id,created_by
    )
    INSERT INTO platform.audit_logs(
      tenant_id,actor_user_id,action,resource_type,resource_id,detail
    )
    SELECT
      NEW.tenant_id,inserted.created_by,
      'DIMENSION_METRIC_COMPATIBILITY_RULE_PROPOSE',
      'DIMENSION_METRIC_COMPATIBILITY',inserted.id::text,
      jsonb_build_object(
        'dimensionId',inserted.dimension_id::text,
        'metricVersionId',inserted.metric_version_id::text,
        'evidenceSource','RULE'
      )
    FROM inserted;
  END IF;
  RETURN NEW;
END
$$;

REVOKE ALL ON FUNCTION
  platform.propose_dimension_metric_compatibility()
FROM PUBLIC;

CREATE TRIGGER semantic_dimensions_propose_metric_compatibility
AFTER INSERT OR UPDATE OF status ON platform.semantic_dimensions
FOR EACH ROW EXECUTE FUNCTION
  platform.propose_dimension_metric_compatibility();

-- 为迁移前已同时发布的一级维度和当前指标补齐同一 DWS 的规则关系。
WITH inserted AS (
  INSERT INTO platform.dimension_metric_compatibility(
    tenant_id,dimension_id,metric_id,metric_version_id,
    metric_dataset_version_id,compatibility_type,fanout_policy,
    join_path_json,evidence_source,confidence,status,created_by,updated_by
  )
  SELECT
    dimension.tenant_id,dimension.id,metric_version.metric_id,
    metric_version.id,metric_version.dataset_version_id,
    'DIRECT','SAFE','[]'::jsonb,'RULE',1.0000,'PROPOSED',
    COALESCE(metric_version.published_by,dimension.updated_by),
    COALESCE(metric_version.published_by,dimension.updated_by)
  FROM platform.semantic_dimensions AS dimension
  JOIN platform.metric_dimensions AS metric_dimension
    ON metric_dimension.tenant_id=dimension.tenant_id
   AND metric_dimension.dataset_version_id=dimension.dataset_version_id
   AND metric_dimension.field_id=dimension.field_id
  JOIN platform.metric_versions AS metric_version
    ON metric_version.tenant_id=metric_dimension.tenant_id
   AND metric_version.id=metric_dimension.metric_version_id
   AND metric_version.metric_id=metric_dimension.metric_id
   AND metric_version.dataset_version_id=metric_dimension.dataset_version_id
   AND metric_version.status='PUBLISHED'
  JOIN platform.metrics AS metric
    ON metric.tenant_id=metric_version.tenant_id
   AND metric.id=metric_version.metric_id
   AND metric.status='PUBLISHED'
   AND metric.current_published_version_id=metric_version.id
   AND metric.deleted_at IS NULL
  JOIN platform.dataset_versions AS version
    ON version.tenant_id=dimension.tenant_id
   AND version.id=dimension.dataset_version_id
   AND version.dataset_id=dimension.dataset_id
   AND version.layer='DWS'
   AND version.status='PUBLISHED'
  JOIN platform.datasets AS dataset
    ON dataset.tenant_id=version.tenant_id
   AND dataset.id=version.dataset_id
   AND dataset.layer='DWS'
   AND dataset.status='PUBLISHED'
   AND dataset.current_published_version_id=version.id
   AND dataset.deleted_at IS NULL
  WHERE dimension.status='PUBLISHED'
  ON CONFLICT(
    tenant_id,dimension_id,metric_version_id
  ) DO NOTHING
  RETURNING tenant_id,id,dimension_id,metric_version_id,created_by
)
INSERT INTO platform.audit_logs(
  tenant_id,actor_user_id,action,resource_type,resource_id,detail
)
SELECT
  inserted.tenant_id,inserted.created_by,
  'DIMENSION_METRIC_COMPATIBILITY_RULE_BACKFILL',
  'DIMENSION_METRIC_COMPATIBILITY',inserted.id::text,
  jsonb_build_object(
    'dimensionId',inserted.dimension_id::text,
    'metricVersionId',inserted.metric_version_id::text,
    'evidenceSource','RULE'
  )
FROM inserted;

CREATE INDEX dimension_members_tenant_normalized_dimension_active_idx
  ON platform.dimension_members(
    tenant_id,normalized_value,dimension_id,id
  )
  WHERE status='ACTIVE';

CREATE INDEX dimension_member_aliases_tenant_normalized_dimension_idx
  ON platform.dimension_member_aliases(
    tenant_id,normalized_alias,dimension_id,dimension_member_id,id
  );

COMMENT ON FUNCTION platform.semantic_join_path_is_valid(jsonb,text,text) IS
  '校验最多 8 跳的结构化 DWS 语义 Join 路径、拓扑连续性和 SAFE 扇出下限';
COMMENT ON CONSTRAINT
  dimension_metric_compatibility_join_path_contract_check
  ON platform.dimension_metric_compatibility IS
  '语义关系只允许精确版本/字段、明确基数且拓扑连续的有界 Join hop';
