-- DWS/ADS 混合检索以版本化业务领域为第一层边界。领域是 DSL 快照事实，
-- 不能再从可变标签、表名或物理编码临时猜测。
CREATE TABLE platform.dimension_member_semantic_documents(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id),
  dimension_id uuid NOT NULL,
  dimension_member_id uuid NOT NULL,
  dataset_id uuid NOT NULL,
  dataset_version_id uuid NOT NULL,
  domain text NOT NULL CHECK(length(domain) BETWEEN 1 AND 128),
  dimension_code citext NOT NULL,
  dimension_name text NOT NULL,
  member_label text NOT NULL,
  document text NOT NULL CHECK(length(document) BETWEEN 1 AND 4096),
  input_hash text NOT NULL CHECK(input_hash ~ '^[0-9a-f]{64}$'),
  embedding halfvec(2560),
  embedding_model text NOT NULL DEFAULT '',
  embedding_input_hash text NOT NULL DEFAULT ''
    CHECK(embedding_input_hash='' OR embedding_input_hash ~ '^[0-9a-f]{64}$'),
  embedding_status text NOT NULL DEFAULT 'PENDING'
    CHECK(embedding_status IN ('PENDING','RUNNING','SUCCEEDED','FAILED')),
  embedding_attempt integer NOT NULL DEFAULT 0 CHECK(embedding_attempt>=0),
  embedding_error_code text NOT NULL DEFAULT '',
  next_attempt_at timestamptz NOT NULL DEFAULT now(),
  lease_owner text NOT NULL DEFAULT '',
  lease_expires_at timestamptz,
  embedded_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT dimension_member_semantic_member_fk
    FOREIGN KEY(dimension_member_id,dimension_id,tenant_id)
    REFERENCES platform.dimension_members(id,dimension_id,tenant_id) ON DELETE CASCADE,
  CONSTRAINT dimension_member_semantic_dimension_fk
    FOREIGN KEY(dimension_id,tenant_id)
    REFERENCES platform.semantic_dimensions(id,tenant_id) ON DELETE CASCADE,
  CONSTRAINT dimension_member_semantic_dataset_version_fk
    FOREIGN KEY(dataset_version_id,dataset_id,tenant_id)
    REFERENCES platform.dataset_versions(id,dataset_id,tenant_id) ON DELETE CASCADE,
  CONSTRAINT dimension_member_semantic_identity_key
    UNIQUE(tenant_id,dimension_member_id),
  CONSTRAINT dimension_member_semantic_embedding_shape_check CHECK(
    (embedding_status='SUCCEEDED' AND embedding IS NOT NULL
      AND btrim(embedding_model)<>'' AND embedding_input_hash<>''
      AND embedded_at IS NOT NULL AND lease_owner='' AND lease_expires_at IS NULL
      AND embedding_error_code='')
    OR
    (embedding_status='RUNNING' AND embedding IS NULL
      AND lease_owner<>'' AND lease_expires_at IS NOT NULL
      AND embedded_at IS NULL)
    OR
    (embedding_status IN ('PENDING','FAILED') AND embedding IS NULL
      AND lease_owner='' AND lease_expires_at IS NULL
      AND embedded_at IS NULL)
  )
);

CREATE INDEX dimension_member_semantic_claim_idx
  ON platform.dimension_member_semantic_documents(
    tenant_id,embedding_status,next_attempt_at,lease_expires_at,created_at
  );
CREATE INDEX dimension_member_semantic_scope_idx
  ON platform.dimension_member_semantic_documents(
    tenant_id,domain,dataset_version_id,dimension_id
  );
CREATE INDEX dimension_member_semantic_label_trgm_idx
  ON platform.dimension_member_semantic_documents
  USING gin(member_label gin_trgm_ops);
CREATE INDEX dimension_member_semantic_embedding_hnsw_idx
  ON platform.dimension_member_semantic_documents
  USING hnsw(embedding halfvec_cosine_ops)
  WITH (m=16,ef_construction=64)
  WHERE embedding_status='SUCCEEDED';

ALTER TABLE platform.dimension_member_semantic_documents ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform.dimension_member_semantic_documents FORCE ROW LEVEL SECURITY;
CREATE POLICY dimension_member_semantic_documents_tenant_isolation
  ON platform.dimension_member_semantic_documents
  USING(tenant_id=platform.current_tenant_id())
  WITH CHECK(tenant_id=platform.current_tenant_id());

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

  SELECT COALESCE(NULLIF(btrim(version.dsl_json#>>'{dataset,domain}'),''),'general')
  INTO domain_name
  FROM platform.dataset_versions AS version
  WHERE version.tenant_id=NEW.tenant_id
    AND version.id=dimension_row.dataset_version_id;
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

REVOKE ALL ON FUNCTION platform.sync_dimension_member_semantic_document() FROM PUBLIC;

CREATE TRIGGER dimension_members_sync_semantic_document
AFTER INSERT OR UPDATE OF canonical_label,status ON platform.dimension_members
FOR EACH ROW EXECUTE FUNCTION platform.sync_dimension_member_semantic_document();

INSERT INTO platform.dimension_member_semantic_documents(
  tenant_id,dimension_id,dimension_member_id,dataset_id,dataset_version_id,
  domain,dimension_code,dimension_name,member_label,document,input_hash
)
SELECT member.tenant_id,dimension.id,member.id,dimension.dataset_id,
  dimension.dataset_version_id,
  COALESCE(NULLIF(btrim(version.dsl_json#>>'{dataset,domain}'),''),'general'),
  dimension.code,dimension.name,member.canonical_label,
  concat(
    '业务领域：',
    COALESCE(NULLIF(btrim(version.dsl_json#>>'{dataset,domain}'),''),'general'),
    E'\n维度名称：',dimension.name,E'\n维度值：',member.canonical_label
  ),
  encode(public.digest(convert_to(concat(
    '业务领域：',
    COALESCE(NULLIF(btrim(version.dsl_json#>>'{dataset,domain}'),''),'general'),
    E'\n维度名称：',dimension.name,E'\n维度值：',member.canonical_label
  ),'UTF8'),'sha256'),'hex')
FROM platform.dimension_members AS member
JOIN platform.semantic_dimensions AS dimension
  ON dimension.tenant_id=member.tenant_id
 AND dimension.id=member.dimension_id
JOIN platform.dataset_versions AS version
  ON version.tenant_id=dimension.tenant_id
 AND version.id=dimension.dataset_version_id
WHERE member.status='ACTIVE'
  AND dimension.status='PUBLISHED'
  AND dimension.member_index_policy='FULL'
  AND NOT dimension.sensitive
  AND NOT dimension.high_cardinality
ON CONFLICT(tenant_id,dimension_member_id) DO NOTHING;

CREATE OR REPLACE FUNCTION platform.enforce_dataset_domain_lineage()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,platform
AS $$
DECLARE
  target_domain text;
  dependency_count integer;
  mismatched_count integer;
BEGIN
  IF NEW.status NOT IN ('PUBLISHING','PUBLISHED') THEN
    RETURN NEW;
  END IF;

  target_domain := btrim(COALESCE(NEW.dsl_json#>>'{dataset,domain}',''));
  IF target_domain='' THEN
    RAISE EXCEPTION 'published dataset domain is required'
      USING ERRCODE='23514',CONSTRAINT='dataset_versions_domain_required';
  END IF;
  IF NEW.layer='ODS' THEN
    RETURN NEW;
  END IF;

  SELECT count(*)::integer,
    count(*) FILTER(
      WHERE btrim(COALESCE(upstream.dsl_json#>>'{dataset,domain}',''))=''
         OR lower(btrim(upstream.dsl_json#>>'{dataset,domain}'))<>lower(target_domain)
    )::integer
  INTO dependency_count,mismatched_count
  FROM jsonb_array_elements(COALESCE(NEW.dsl_json->'nodes','[]'::jsonb)) AS node
  LEFT JOIN platform.dataset_versions AS upstream
    ON upstream.tenant_id=NEW.tenant_id
   AND upstream.id=(node->>'datasetVersionId')::uuid
   AND upstream.status='PUBLISHED'
  WHERE node->>'type'='DATASET';

  IF dependency_count=0 OR mismatched_count>0 THEN
    RAISE EXCEPTION 'downstream dataset domain must equal every published upstream domain'
      USING ERRCODE='23514',CONSTRAINT='dataset_versions_domain_lineage_match';
  END IF;
  RETURN NEW;
END
$$;

REVOKE ALL ON FUNCTION platform.enforce_dataset_domain_lineage() FROM PUBLIC;

CREATE TRIGGER dataset_versions_domain_lineage_guard
BEFORE INSERT OR UPDATE OF status,dsl_json,layer ON platform.dataset_versions
FOR EACH ROW EXECUTE FUNCTION platform.enforce_dataset_domain_lineage();

-- 正式指标向量文档加入领域文本。维度成员使用独立向量表；敏感、
-- EXACT_ONLY/NONE 和高基数成员不会进入 embedding provider。
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
  SELECT COALESCE(NULLIF(btrim(dataset_version.dsl_json#>>'{dataset,domain}'),''),'general')
  INTO domain_name
  FROM platform.dataset_versions AS dataset_version
  WHERE dataset_version.tenant_id=NEW.tenant_id
    AND dataset_version.id=NEW.dataset_version_id;

  UPDATE platform.metric_semantic_documents AS document
  SET document=concat(
        '业务领域：',domain_name,E'\n',
        regexp_replace(document.document,'^业务领域：[^\n]*\n','','g')
      ),
      tags=ARRAY(
        SELECT DISTINCT value
        FROM unnest(
          array_append(
            array_remove(document.tags,'领域:'||domain_name),
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

REVOKE ALL ON FUNCTION platform.enrich_metric_semantic_domain() FROM PUBLIC;

CREATE TRIGGER metric_versions_zz_enrich_semantic_domain
AFTER UPDATE OF status ON platform.metric_versions
FOR EACH ROW EXECUTE FUNCTION platform.enrich_metric_semantic_domain();

WITH enriched AS (
  SELECT document.id,
    COALESCE(NULLIF(btrim(dataset_version.dsl_json#>>'{dataset,domain}'),''),'general') AS domain_name,
    concat(
      '业务领域：',
      COALESCE(NULLIF(btrim(dataset_version.dsl_json#>>'{dataset,domain}'),''),'general'),
      E'\n',
      regexp_replace(document.document,'^业务领域：[^\n]*\n','','g')
    ) AS enriched_document
  FROM platform.metric_semantic_documents AS document
  JOIN platform.dataset_versions AS dataset_version
    ON dataset_version.tenant_id=document.tenant_id
   AND dataset_version.id=document.dataset_version_id
  WHERE document.subject_type='METRIC_VERSION'
)
UPDATE platform.metric_semantic_documents AS document
SET document=enriched.enriched_document,
    tags=ARRAY(
      SELECT DISTINCT value
      FROM unnest(array_append(document.tags,'领域:'||enriched.domain_name)) AS value
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

COMMENT ON FUNCTION platform.enforce_dataset_domain_lineage() IS
  '要求 ODS 明确领域，并强制 DIM/DWD/DWS/ADS 的领域与所有精确上游发布版本一致';
COMMENT ON FUNCTION platform.enrich_metric_semantic_domain() IS
  '把指标所属领域加入向量语义文档，供领域分区后的指标混合检索';
COMMENT ON TABLE platform.dimension_member_semantic_documents IS
  '非敏感、低基数、FULL 策略维度值的租户隔离向量文档；精确 member_key 仍由 dimension_members 管理';
