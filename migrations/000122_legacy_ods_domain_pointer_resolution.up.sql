-- 历史 DWD 固定的旧 ODS 版本可能早于 domain 字段。ODS 的数据集身份不会跨
-- 领域迁移，因此仅在历史叶版本缺少 domain 时，回退读取同一 ODS 数据集当前
-- 发布版本的领域；非 ODS 版本仍绝不跟随可变指针。
CREATE OR REPLACE FUNCTION platform.dataset_version_effective_domain(
  target_version_id uuid
)
RETURNS text
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path=pg_catalog,platform
AS $$
  WITH RECURSIVE lineage AS (
    SELECT version.id,version.dataset_id,version.layer,version.dsl_json
    FROM platform.dataset_versions AS version
    WHERE version.tenant_id=platform.current_tenant_id()
      AND version.id=target_version_id
    UNION
    SELECT upstream.id,upstream.dataset_id,upstream.layer,upstream.dsl_json
    FROM lineage
    CROSS JOIN LATERAL jsonb_array_elements(
      COALESCE(lineage.dsl_json->'nodes','[]'::jsonb)
    ) AS node
    JOIN platform.dataset_versions AS upstream
      ON upstream.tenant_id=platform.current_tenant_id()
     AND upstream.id=(node->>'datasetVersionId')::uuid
    WHERE node->>'type'='DATASET'
  ), ods_domains AS (
    SELECT DISTINCT btrim(COALESCE(
      NULLIF(lineage.dsl_json#>>'{dataset,domain}',''),
      NULLIF(current_version.dsl_json#>>'{dataset,domain}','')
    )) AS domain
    FROM lineage
    JOIN platform.datasets AS dataset
      ON dataset.tenant_id=platform.current_tenant_id()
     AND dataset.id=lineage.dataset_id
    LEFT JOIN platform.dataset_versions AS current_version
      ON current_version.tenant_id=dataset.tenant_id
     AND current_version.id=dataset.current_published_version_id
    WHERE lineage.layer='ODS'
      AND btrim(COALESCE(
        NULLIF(lineage.dsl_json#>>'{dataset,domain}',''),
        NULLIF(current_version.dsl_json#>>'{dataset,domain}',''),
        ''
      ))<>''
  ), own_domain AS (
    SELECT NULLIF(btrim(dsl_json#>>'{dataset,domain}'),'') AS domain
    FROM lineage WHERE id=target_version_id
  )
  SELECT CASE
    WHEN (SELECT count(*) FROM ods_domains)=1
      THEN (SELECT domain FROM ods_domains LIMIT 1)
    ELSE COALESCE((SELECT domain FROM own_domain),'general')
  END
$$;

WITH effective AS (
  SELECT document.id,
    platform.dataset_version_effective_domain(
      document.dataset_version_id
    ) AS domain_name
  FROM platform.metric_semantic_documents AS document
  WHERE document.subject_type='METRIC_VERSION'
), enriched AS (
  SELECT effective.*,
    concat(
      '业务领域：',effective.domain_name,E'\n',
      regexp_replace(document.document,'^业务领域：[^\n]*\n','','g')
    ) AS enriched_document
  FROM effective
  JOIN platform.metric_semantic_documents AS document
    ON document.id=effective.id
)
UPDATE platform.metric_semantic_documents AS document
SET document=enriched.enriched_document,
    tags=ARRAY(
      SELECT DISTINCT value
      FROM unnest(array_append(
        ARRAY(
          SELECT existing FROM unnest(document.tags) AS existing
          WHERE existing !~ '^领域[：:]'
        ),
        '领域:'||enriched.domain_name
      )) AS value
      WHERE btrim(value)<>''
      ORDER BY value
    ),
    semantic_input_hash=encode(public.digest(
      convert_to(enriched.enriched_document,'UTF8'),'sha256'
    ),'hex'),
    embedding=NULL,embedding_model='',embedding_input_hash='',
    embedding_status='PENDING',embedding_attempt=0,
    embedding_error_code='',next_attempt_at=now(),
    lease_owner='',lease_expires_at=NULL,embedded_at=NULL,updated_at=now()
FROM enriched
WHERE document.id=enriched.id
  AND document.document IS DISTINCT FROM enriched.enriched_document;
