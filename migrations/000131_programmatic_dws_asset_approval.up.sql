-- DWS 保存时已经完成字段角色、名称和描述治理。资产发现阶段不再调用 LLM，
-- 也不再把系统识别结果送入统一审批中心；旧识别记录保留为终态审计证据。

-- 一次性清空迁移执行时已有的审批条目。审批中心本身和后续新提交的
-- 数据源/数据集发布审批保持可用。
UPDATE platform.metric_candidate_preparation_jobs AS job
SET status='CANCELLED',
    error_code='APPROVAL_QUEUE_CLEARED',
    error_message='迁移已清理现有审批条目',
    completed_at=clock_timestamp(),
    updated_at=clock_timestamp(),
    lease_owner='',
    lease_expires_at=NULL
FROM platform.dataset_publication_requests AS request
WHERE request.tenant_id=job.tenant_id
  AND request.id=job.publication_request_id
  AND request.status='PENDING'
  AND job.status IN ('PENDING','RUNNING');

UPDATE platform.data_source_publication_requests AS request
SET status='WITHDRAWN',
    reviewer_user_id=request.requester_user_id,
    review_note='迁移已清理现有审批条目',
    reviewed_at=clock_timestamp(),
    version=request.version+1,
    updated_at=clock_timestamp()
WHERE request.status='PENDING';

UPDATE platform.dataset_publication_requests AS request
SET status='CANCELLED',
    review_note='迁移已清理现有审批条目',
    reviewed_at=clock_timestamp(),
    version=request.version+1,
    updated_at=clock_timestamp()
WHERE request.status='PENDING';

UPDATE platform.metric_extraction_jobs AS job
SET status='FAILED',
    error_code='LEGACY_DISCOVERY_RETIRED',
    error_message='旧版 DWS 指标候选审批已停用；请重新执行程序化 DWS 资产入库',
    lease_owner='',lease_expires_at=NULL,
    completed_at=COALESCE(completed_at,now()),heartbeat_at=now()
FROM platform.dataset_versions AS version
WHERE version.tenant_id=job.tenant_id
  AND version.id=job.dataset_version_id
  AND version.layer='DWS'
  AND job.extractor_version<>'metric-candidate-code-v1'
  AND job.status IN ('PENDING','RUNNING');

WITH review_scope AS (
  SELECT candidate.id,
    COALESCE(
      job.requested_by,
      version.published_by,
      fallback_actor.id
    ) AS reviewer_id
  FROM platform.metric_candidates AS candidate
  JOIN platform.metric_extraction_jobs AS job
    ON job.tenant_id=candidate.tenant_id
   AND job.id=candidate.job_id
  JOIN platform.dataset_versions AS version
    ON version.tenant_id=candidate.tenant_id
   AND version.id=candidate.dataset_version_id
   AND version.layer='DWS'
  LEFT JOIN LATERAL (
    SELECT actor.id
    FROM platform.users AS actor
    WHERE actor.tenant_id=candidate.tenant_id
      AND actor.status='ACTIVE'
      AND actor.deleted_at IS NULL
    ORDER BY actor.created_at,actor.id
    LIMIT 1
  ) AS fallback_actor ON true
  WHERE candidate.status IN ('READY','NEEDS_REVIEW','BLOCKED')
    AND job.extractor_version<>'metric-candidate-code-v1'
)
UPDATE platform.metric_candidates AS candidate
SET status='REJECTED',
    decision_reason='旧版自动识别审批已取消；DWS 资产将按已保存字段角色重新入库',
    reviewed_by=review_scope.reviewer_id,
    reviewed_at=now(),version=candidate.version+1,
    updated_at=clock_timestamp()
FROM review_scope
WHERE candidate.id=review_scope.id
  AND review_scope.reviewer_id IS NOT NULL;

UPDATE platform.dimension_survey_candidates AS candidate
SET status='STALE',version=candidate.version+1,
    decision_reason='PROGRAM_FIELD_CLASSIFICATION_ENABLED',
    updated_at=clock_timestamp()
WHERE candidate.status='SUGGESTED';

-- 替换 000098 的旧 DWS 候选触发器：今后的发布只登记程序分类任务，
-- 不再创建会进入人工审批的 metric-candidate-v4 任务。
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
    WITH classified AS (
      SELECT field.field_id,field.field_code::text AS code,
        field.field_name AS name,field.description,
        field.field_role,field.semantic_type,
        (
          field.field_role='IDENTIFIER'
          OR upper(field.semantic_type)='IDENTIFIER'
        ) AS high_cardinality,
        EXISTS(
          SELECT 1
          FROM platform.asset_tag_bindings AS binding
          JOIN platform.semantic_tags AS tag
            ON tag.tenant_id=binding.tenant_id
           AND tag.id=binding.tag_id
           AND tag.category='SENSITIVITY'
           AND tag.status='ACTIVE'
          WHERE binding.tenant_id=field.tenant_id
            AND binding.asset_type='DATASET_FIELD'
            AND binding.dataset_id=NEW.dataset_id
            AND binding.dataset_version_id=field.dataset_version_id
            AND binding.dataset_field_id=field.field_id
            AND binding.status='APPROVED'
        ) AS sensitive
      FROM platform.dataset_fields AS field
      WHERE field.tenant_id=NEW.tenant_id
        AND field.dataset_version_id=NEW.id
        AND field.field_role IN (
          'DIMENSION','ATTRIBUTE','TIME','IDENTIFIER'
        )
    ), inserted AS (
      INSERT INTO platform.semantic_dimensions(
        tenant_id,dataset_id,dataset_version_id,field_id,
        code,name,description,dimension_type,member_index_policy,
        high_cardinality,sensitive,status,definition_hash,
        created_by,updated_by
      )
      SELECT NEW.tenant_id,NEW.dataset_id,NEW.id,classified.field_id,
        classified.code,classified.name,classified.description,
        CASE WHEN classified.field_role='TIME'
          THEN 'TIME' ELSE 'STANDARD' END,
        'NONE',classified.high_cardinality,classified.sensitive,
        'PUBLISHED',
        encode(public.digest(convert_to(concat_ws(E'\x1f',
          NEW.dataset_id::text,NEW.id::text,classified.field_id,
          classified.code,classified.name,classified.description,
          classified.field_role,classified.semantic_type,'NONE',
          classified.high_cardinality::text,
          classified.sensitive::text,'PUBLISHED'
        ),'UTF8'),'sha256'),'hex'),
        NEW.published_by,NEW.published_by
      FROM classified
      ON CONFLICT(tenant_id,dataset_version_id,field_id) DO NOTHING
      RETURNING id,dataset_id,dataset_version_id,field_id
    )
    INSERT INTO platform.audit_logs(
      tenant_id,actor_user_id,action,resource_type,resource_id,detail
    )
    SELECT NEW.tenant_id,NEW.published_by,
      'PROGRAM_APPROVE_DWS_DIMENSION','SEMANTIC_DIMENSION',
      inserted.id::text,jsonb_build_object(
        'datasetId',inserted.dataset_id::text,
        'datasetVersionId',inserted.dataset_version_id::text,
        'fieldId',inserted.field_id,
        'classification','DATASET_FIELD_ROLE'
      )
    FROM inserted;

    INSERT INTO platform.metric_extraction_jobs(
      tenant_id,dataset_id,dataset_version_id,dsl_hash,
      requested_by,extractor_version
    ) VALUES(
      NEW.tenant_id,NEW.dataset_id,NEW.id,NEW.schema_hash,
      NEW.published_by,'metric-candidate-code-v1'
    )
    ON CONFLICT(tenant_id,dataset_version_id,extractor_version) DO NOTHING;
  END IF;
  RETURN NEW;
END
$$;

REVOKE ALL ON FUNCTION platform.enqueue_dws_metric_discovery() FROM PUBLIC;

COMMENT ON FUNCTION platform.enqueue_dws_metric_discovery() IS
  'DWS 发布时幂等登记程序字段分类；MEASURE 自动接收为指标，其余字段直接成为维度资产';

-- 迁移现有当前 DWS：已保存的非 MEASURE 字段直接成为正式维度资产。
WITH current_dws AS (
  SELECT dataset.tenant_id,dataset.id AS dataset_id,
    version.id AS dataset_version_id,version.published_by
  FROM platform.datasets AS dataset
  JOIN platform.dataset_versions AS version
    ON version.tenant_id=dataset.tenant_id
   AND version.dataset_id=dataset.id
   AND version.id=dataset.current_published_version_id
   AND version.status='PUBLISHED'
   AND version.layer='DWS'
  WHERE dataset.status='PUBLISHED'
    AND dataset.deleted_at IS NULL
), classified AS (
  SELECT current_dws.tenant_id,current_dws.dataset_id,
    current_dws.dataset_version_id,current_dws.published_by,
    field.field_id,field.field_code::text AS code,field.field_name AS name,
    field.description,field.field_role,field.semantic_type,
    (
      field.field_role='IDENTIFIER'
      OR upper(field.semantic_type)='IDENTIFIER'
    ) AS high_cardinality,
    EXISTS(
      SELECT 1
      FROM platform.asset_tag_bindings AS binding
      JOIN platform.semantic_tags AS tag
        ON tag.tenant_id=binding.tenant_id
       AND tag.id=binding.tag_id
       AND tag.category='SENSITIVITY'
       AND tag.status='ACTIVE'
      WHERE binding.tenant_id=field.tenant_id
        AND binding.asset_type='DATASET_FIELD'
        AND binding.dataset_id=current_dws.dataset_id
        AND binding.dataset_version_id=field.dataset_version_id
        AND binding.dataset_field_id=field.field_id
        AND binding.status='APPROVED'
    ) AS sensitive
  FROM current_dws
  JOIN platform.dataset_fields AS field
    ON field.tenant_id=current_dws.tenant_id
   AND field.dataset_version_id=current_dws.dataset_version_id
  WHERE field.field_role IN (
    'DIMENSION','ATTRIBUTE','TIME','IDENTIFIER'
  )
), inserted_dimensions AS (
  INSERT INTO platform.semantic_dimensions(
    tenant_id,dataset_id,dataset_version_id,field_id,
    code,name,description,dimension_type,member_index_policy,
    high_cardinality,sensitive,status,definition_hash,
    created_by,updated_by
  )
  SELECT classified.tenant_id,classified.dataset_id,
    classified.dataset_version_id,classified.field_id,
    classified.code,classified.name,classified.description,
    CASE WHEN classified.field_role='TIME'
      THEN 'TIME' ELSE 'STANDARD' END,
    -- 程序审批只确认维度身份；成员枚举继续按 NONE 失败关闭，
    -- 由独立画像/索引治理流程决定是否放开。
    'NONE',
    classified.high_cardinality,classified.sensitive,'PUBLISHED',
    encode(public.digest(convert_to(concat_ws(E'\x1f',
      classified.dataset_id::text,
      classified.dataset_version_id::text,
      classified.field_id,classified.code,classified.name,
      classified.description,classified.field_role,classified.semantic_type,
      'NONE',
      classified.high_cardinality::text,
      classified.sensitive::text,'PUBLISHED'
    ),'UTF8'),'sha256'),'hex'),
    classified.published_by,classified.published_by
  FROM classified
  ON CONFLICT(tenant_id,dataset_version_id,field_id) DO NOTHING
  RETURNING tenant_id,id,dataset_id,dataset_version_id,field_id,created_by
), audited_dimensions AS (
  INSERT INTO platform.audit_logs(
    tenant_id,actor_user_id,action,resource_type,resource_id,detail
  )
  SELECT inserted_dimensions.tenant_id,inserted_dimensions.created_by,
    'PROGRAM_APPROVE_DWS_DIMENSION','SEMANTIC_DIMENSION',
    inserted_dimensions.id::text,jsonb_build_object(
      'datasetId',inserted_dimensions.dataset_id::text,
      'datasetVersionId',inserted_dimensions.dataset_version_id::text,
      'fieldId',inserted_dimensions.field_id,
      'classification','DATASET_FIELD_ROLE',
      'migration','000131'
    )
  FROM inserted_dimensions
)
SELECT count(*) FROM inserted_dimensions;

-- 现有当前 DWS 的 MEASURE 字段由 worker 规则提取并自动接收为指标资产。
INSERT INTO platform.metric_extraction_jobs(
  tenant_id,dataset_id,dataset_version_id,dsl_hash,
  requested_by,extractor_version
)
SELECT dataset.tenant_id,dataset.id,version.id,version.schema_hash,
  version.published_by,'metric-candidate-code-v1'
FROM platform.datasets AS dataset
JOIN platform.dataset_versions AS version
  ON version.tenant_id=dataset.tenant_id
 AND version.dataset_id=dataset.id
 AND version.id=dataset.current_published_version_id
 AND version.status='PUBLISHED'
 AND version.layer='DWS'
WHERE dataset.status='PUBLISHED'
  AND dataset.deleted_at IS NULL
ON CONFLICT(tenant_id,dataset_version_id,extractor_version) DO NOTHING;

COMMENT ON TABLE platform.metric_candidates IS
  'DWS 字段程序入库的规则证据；新规则候选由 worker 自动接收，不进入人工审批中心';
COMMENT ON TABLE platform.dimension_survey_candidates IS
  '历史 DWS 维度勘测证据；当前 DWS 维度按已保存字段角色直接写入正式资产';
