-- 000120 makes the domain mandatory for every new publication. Legacy
-- DIM/DWD/DWS snapshots are immutable and may predate dataset.domain, so query
-- planning resolves their effective domain from the ODS leaves without
-- rewriting historical versions.
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
    SELECT version.id,version.layer,version.dsl_json
    FROM platform.dataset_versions AS version
    WHERE version.tenant_id=platform.current_tenant_id()
      AND version.id=target_version_id
    UNION
    SELECT upstream.id,upstream.layer,upstream.dsl_json
    FROM lineage
    CROSS JOIN LATERAL jsonb_array_elements(
      COALESCE(lineage.dsl_json->'nodes','[]'::jsonb)
    ) AS node
    JOIN platform.dataset_versions AS upstream
      ON upstream.tenant_id=platform.current_tenant_id()
     AND upstream.id=(node->>'datasetVersionId')::uuid
    WHERE node->>'type'='DATASET'
  ), ods_domains AS (
    SELECT DISTINCT btrim(dsl_json#>>'{dataset,domain}') AS domain
    FROM lineage
    WHERE layer='ODS'
      AND btrim(COALESCE(dsl_json#>>'{dataset,domain}',''))<>''
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

REVOKE ALL ON FUNCTION
  platform.dataset_version_effective_domain(uuid)
FROM PUBLIC;

CREATE OR REPLACE FUNCTION platform.enrich_metric_semantic_domain()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,platform
AS $$
DECLARE
  domain_name text;
BEGIN
  IF NEW.status<>'PUBLISHED' OR OLD.status='PUBLISHED' THEN
    RETURN NEW;
  END IF;
  domain_name := platform.dataset_version_effective_domain(
    NEW.dataset_version_id
  );
  UPDATE platform.metric_semantic_documents AS document
  SET document=concat(
        '业务领域：',domain_name,E'\n',
        regexp_replace(document.document,'^业务领域：[^\n]*\n','','g')
      ),
      tags=ARRAY(
        SELECT DISTINCT value
        FROM unnest(
          array_append(
            ARRAY(
              SELECT existing FROM unnest(document.tags) AS existing
              WHERE existing !~ '^领域[：:]'
            ),
            '领域:'||domain_name
          )
        ) AS value
        WHERE btrim(value)<>''
        ORDER BY value
      ),
      semantic_input_hash=encode(public.digest(
        convert_to(concat(
          '业务领域：',domain_name,E'\n',
          regexp_replace(document.document,'^业务领域：[^\n]*\n','','g')
        ),'UTF8'),'sha256'
      ),'hex'),
      embedding=NULL,embedding_model='',embedding_input_hash='',
      embedding_status='PENDING',embedding_attempt=0,
      embedding_error_code='',next_attempt_at=now(),
      lease_owner='',lease_expires_at=NULL,embedded_at=NULL,updated_at=now()
  WHERE document.tenant_id=NEW.tenant_id
    AND document.subject_type='METRIC_VERSION'
    AND document.metric_version_id=NEW.id;
  RETURN NEW;
END
$$;

CREATE OR REPLACE FUNCTION platform.sync_dimension_member_semantic_document()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,platform
AS $$
DECLARE
  dimension_row platform.semantic_dimensions%ROWTYPE;
  domain_name text;
  member_document text;
  member_hash text;
BEGIN
  SELECT * INTO dimension_row
  FROM platform.semantic_dimensions
  WHERE tenant_id=NEW.tenant_id AND id=NEW.dimension_id;
  IF NEW.status<>'ACTIVE'
     OR dimension_row.status<>'PUBLISHED'
     OR dimension_row.member_index_policy<>'FULL'
     OR dimension_row.sensitive
     OR dimension_row.high_cardinality THEN
    DELETE FROM platform.dimension_member_semantic_documents
    WHERE tenant_id=NEW.tenant_id AND dimension_member_id=NEW.id;
    RETURN NEW;
  END IF;
  domain_name := platform.dataset_version_effective_domain(
    dimension_row.dataset_version_id
  );
  member_document := concat(
    '业务领域：',domain_name,E'\n',
    '维度名称：',dimension_row.name,E'\n',
    '维度值：',NEW.canonical_label
  );
  member_hash := encode(public.digest(
    convert_to(member_document,'UTF8'),'sha256'
  ),'hex');
  INSERT INTO platform.dimension_member_semantic_documents(
    tenant_id,dimension_id,dimension_member_id,dataset_id,dataset_version_id,
    domain,dimension_code,dimension_name,member_label,document,input_hash
  ) VALUES(
    NEW.tenant_id,NEW.dimension_id,NEW.id,dimension_row.dataset_id,
    dimension_row.dataset_version_id,domain_name,dimension_row.code,
    dimension_row.name,NEW.canonical_label,member_document,member_hash
  )
  ON CONFLICT(tenant_id,dimension_member_id) DO UPDATE SET
    domain=EXCLUDED.domain,dimension_code=EXCLUDED.dimension_code,
    dimension_name=EXCLUDED.dimension_name,member_label=EXCLUDED.member_label,
    document=EXCLUDED.document,input_hash=EXCLUDED.input_hash,
    embedding=NULL,embedding_model='',embedding_input_hash='',
    embedding_status='PENDING',embedding_attempt=0,embedding_error_code='',
    next_attempt_at=now(),lease_owner='',lease_expires_at=NULL,
    embedded_at=NULL,updated_at=now()
  WHERE platform.dimension_member_semantic_documents.input_hash
    IS DISTINCT FROM EXCLUDED.input_hash;
  RETURN NEW;
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

UPDATE platform.dimension_member_semantic_documents AS document
SET domain=platform.dataset_version_effective_domain(
      document.dataset_version_id
    ),
    document=concat(
      '业务领域：',
      platform.dataset_version_effective_domain(document.dataset_version_id),
      E'\n维度名称：',document.dimension_name,
      E'\n维度值：',document.member_label
    ),
    input_hash=encode(public.digest(convert_to(concat(
      '业务领域：',
      platform.dataset_version_effective_domain(document.dataset_version_id),
      E'\n维度名称：',document.dimension_name,
      E'\n维度值：',document.member_label
    ),'UTF8'),'sha256'),'hex'),
    embedding=NULL,embedding_model='',embedding_input_hash='',
    embedding_status='PENDING',embedding_attempt=0,
    embedding_error_code='',next_attempt_at=now(),
    lease_owner='',lease_expires_at=NULL,embedded_at=NULL,updated_at=now()
WHERE document.domain IS DISTINCT FROM
  platform.dataset_version_effective_domain(document.dataset_version_id);

COMMENT ON FUNCTION platform.dataset_version_effective_domain(uuid) IS
  '返回精确版本从 ODS 叶节点继承的唯一领域；仅供兼容未携带 domain 的不可变历史快照';
