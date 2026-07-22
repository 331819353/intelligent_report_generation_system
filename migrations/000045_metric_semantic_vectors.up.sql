-- 指标候选和正式发布指标的语义文档、标签及 pgvector 检索索引。
CREATE EXTENSION IF NOT EXISTS vector;

CREATE TABLE platform.metric_semantic_documents(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id),
  subject_type text NOT NULL CHECK(subject_type IN ('CANDIDATE','METRIC_VERSION')),
  candidate_id uuid,
  metric_id uuid,
  metric_version_id uuid,
  dataset_id uuid NOT NULL,
  dataset_version_id uuid NOT NULL,
  name text NOT NULL CHECK(btrim(name)<>''),
  description text NOT NULL DEFAULT '',
  caliber text NOT NULL CHECK(btrim(caliber)<>''),
  dimensions text[] NOT NULL DEFAULT '{}',
  period text NOT NULL DEFAULT 'NONE' CHECK(btrim(period)<>''),
  period_description text NOT NULL CHECK(btrim(period_description)<>''),
  lineage jsonb NOT NULL CHECK(jsonb_typeof(lineage)='object'),
  lineage_summary text NOT NULL CHECK(btrim(lineage_summary)<>''),
  tags text[] NOT NULL DEFAULT '{}',
  document text NOT NULL CHECK(length(document) BETWEEN 1 AND 65535),
  semantic_source text NOT NULL CHECK(semantic_source IN ('RULE','HYBRID','RULE_FALLBACK')),
  llm_model text NOT NULL DEFAULT '',
  prompt_version text NOT NULL CHECK(btrim(prompt_version)<>''),
  semantic_input_hash text NOT NULL CHECK(semantic_input_hash ~ '^[0-9a-f]{64}$'),
  ai_request_id uuid,
  enrichment_error_code text NOT NULL DEFAULT '',
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
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  embedded_at timestamptz,
  CONSTRAINT metric_semantic_documents_candidate_fk
    FOREIGN KEY(candidate_id,tenant_id)
    REFERENCES platform.metric_candidates(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT metric_semantic_documents_metric_version_fk
    FOREIGN KEY(metric_version_id,metric_id,dataset_version_id,tenant_id)
    REFERENCES platform.metric_versions(id,metric_id,dataset_version_id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT metric_semantic_documents_dataset_version_fk
    FOREIGN KEY(dataset_version_id,dataset_id,tenant_id)
    REFERENCES platform.dataset_versions(id,dataset_id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT metric_semantic_documents_subject_shape_check CHECK(
    (subject_type='CANDIDATE' AND candidate_id IS NOT NULL AND metric_id IS NULL AND metric_version_id IS NULL)
    OR
    (subject_type='METRIC_VERSION' AND metric_id IS NOT NULL AND metric_version_id IS NOT NULL)
  ),
  CONSTRAINT metric_semantic_documents_embedding_shape_check CHECK(
    (embedding_status='SUCCEEDED' AND embedding IS NOT NULL AND btrim(embedding_model)<>''
      AND embedding_input_hash<>'' AND embedded_at IS NOT NULL AND lease_owner=''
      AND lease_expires_at IS NULL AND embedding_error_code='')
    OR
    (embedding_status='RUNNING' AND embedding IS NULL AND lease_owner<>'' AND lease_expires_at IS NOT NULL
      AND embedded_at IS NULL)
    OR
    (embedding_status IN ('PENDING','FAILED') AND embedding IS NULL AND lease_owner=''
      AND lease_expires_at IS NULL AND embedded_at IS NULL)
  )
);

CREATE UNIQUE INDEX metric_semantic_candidate_identity_idx
  ON platform.metric_semantic_documents(tenant_id,candidate_id)
  WHERE subject_type='CANDIDATE';
CREATE UNIQUE INDEX metric_semantic_version_identity_idx
  ON platform.metric_semantic_documents(tenant_id,metric_version_id)
  WHERE subject_type='METRIC_VERSION';
CREATE INDEX metric_semantic_embedding_claim_idx
  ON platform.metric_semantic_documents(tenant_id,embedding_status,next_attempt_at,lease_expires_at,created_at);
CREATE INDEX metric_semantic_dataset_idx
  ON platform.metric_semantic_documents(tenant_id,dataset_id,dataset_version_id,subject_type);
CREATE INDEX metric_semantic_tags_gin_idx
  ON platform.metric_semantic_documents USING gin(tags);
CREATE INDEX metric_semantic_embedding_hnsw_idx
  ON platform.metric_semantic_documents USING hnsw(embedding halfvec_cosine_ops)
  WITH (m=16,ef_construction=64)
  WHERE embedding_status='SUCCEEDED';

ALTER TABLE platform.metric_semantic_documents ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform.metric_semantic_documents FORCE ROW LEVEL SECURITY;
CREATE POLICY metric_semantic_documents_tenant_isolation ON platform.metric_semantic_documents
  USING(tenant_id=platform.current_tenant_id())
  WITH CHECK(tenant_id=platform.current_tenant_id());

-- 正式指标发布后生成可绑定的精确版本语义文档。来自候选的 LLM 语义可继承；
-- 手工创建的指标则使用发布定义生成确定性语义，之后统一进入 embedding 队列。
CREATE OR REPLACE FUNCTION platform.enqueue_published_metric_semantic_document()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
  owner platform.metrics%ROWTYPE;
  candidate_doc platform.metric_semantic_documents%ROWTYPE;
  metric_name text;
  metric_description text;
  metric_aggregation text;
  metric_period text;
  metric_dimensions text[];
  metric_tags text[];
  metric_caliber text;
  metric_lineage jsonb;
  metric_lineage_summary text;
  metric_period_description text;
  metric_document text;
  metric_source text;
  metric_model text;
  metric_prompt_version text;
  metric_semantic_hash text;
  metric_ai_request uuid;
BEGIN
  IF NEW.status<>'PUBLISHED' OR OLD.status='PUBLISHED' THEN
    RETURN NEW;
  END IF;
  SELECT * INTO owner FROM platform.metrics
  WHERE tenant_id=NEW.tenant_id AND id=NEW.metric_id;
  IF owner.origin_candidate_id IS NOT NULL THEN
    SELECT * INTO candidate_doc FROM platform.metric_semantic_documents
    WHERE tenant_id=NEW.tenant_id AND subject_type='CANDIDATE'
      AND candidate_id=owner.origin_candidate_id;
  END IF;

  metric_name := COALESCE(NULLIF(candidate_doc.name,''),NEW.definition_json#>>'{metric,name}',owner.name);
  metric_description := COALESCE(NULLIF(candidate_doc.description,''),NEW.definition_json#>>'{metric,description}',owner.description,'');
  metric_aggregation := COALESCE(NULLIF(NEW.definition_json->>'aggregation',''),'UNKNOWN');
  metric_period := COALESCE(NULLIF(NEW.definition_json->>'timeGrain',''),'NONE');
  SELECT COALESCE(array_agg(value->>'name' ORDER BY ordinal),'{}'::text[])
    INTO metric_dimensions
  FROM jsonb_array_elements(COALESCE(NEW.definition_json->'allowedDimensions','[]'::jsonb))
    WITH ORDINALITY AS item(value,ordinal);
  metric_caliber := COALESCE(NULLIF(candidate_doc.caliber,''),
    format('基于发布指标定义按 %s 聚合；空值处理为 %s',metric_aggregation,
      COALESCE(NULLIF(NEW.definition_json->>'nullHandling',''),'IGNORE')));
  metric_lineage := jsonb_build_object(
    'datasetId',NEW.dataset_id::text,
    'datasetVersionId',NEW.dataset_version_id::text,
    'metricId',NEW.metric_id::text,
    'metricVersionId',NEW.id::text,
    'aggregation',metric_aggregation,
    'dimensionFieldIds',COALESCE(
      (SELECT jsonb_agg(value->>'fieldId' ORDER BY ordinal)
       FROM jsonb_array_elements(COALESCE(NEW.definition_json->'allowedDimensions','[]'::jsonb))
       WITH ORDINALITY AS item(value,ordinal)),'[]'::jsonb)
  );
  metric_lineage_summary := COALESCE(NULLIF(candidate_doc.lineage_summary,''),
    format('来自发布数据集版本 %s，并固定为指标版本 %s',NEW.dataset_version_id,NEW.id));
  metric_period_description := COALESCE(NULLIF(candidate_doc.period_description,''),
    CASE metric_period WHEN 'DAY' THEN '按日' WHEN 'WEEK' THEN '按周' WHEN 'MONTH' THEN '按月'
      WHEN 'QUARTER' THEN '按季度' WHEN 'YEAR' THEN '按年' ELSE '无固定统计周期' END);
  metric_tags := CASE WHEN cardinality(candidate_doc.tags)>0 THEN candidate_doc.tags ELSE
    array_remove(ARRAY[metric_name,owner.code::text,'原子指标',metric_aggregation,metric_period] || metric_dimensions,'') END;
  metric_document := concat_ws(E'\n',
    '指标名称：'||metric_name,
    '指标说明：'||metric_description,
    '统计口径：'||metric_caliber,
    '分析维度：'||array_to_string(metric_dimensions,'、'),
    '统计周期：'||metric_period_description||'（'||metric_period||'）',
    '数据血缘：'||metric_lineage_summary,
    '检索标签：'||array_to_string(metric_tags,'、'));
  metric_source := COALESCE(NULLIF(candidate_doc.semantic_source,''),'RULE');
  metric_model := COALESCE(candidate_doc.llm_model,'');
  metric_prompt_version := COALESCE(NULLIF(candidate_doc.prompt_version,''),'published-metric-semantic-v1');
  metric_semantic_hash := encode(digest(metric_document,'sha256'),'hex');
  metric_ai_request := candidate_doc.ai_request_id;

  INSERT INTO platform.metric_semantic_documents(
    tenant_id,subject_type,candidate_id,metric_id,metric_version_id,dataset_id,dataset_version_id,
    name,description,caliber,dimensions,period,period_description,lineage,lineage_summary,tags,
    document,semantic_source,llm_model,prompt_version,semantic_input_hash,ai_request_id
  ) VALUES(
    NEW.tenant_id,'METRIC_VERSION',owner.origin_candidate_id,NEW.metric_id,NEW.id,NEW.dataset_id,NEW.dataset_version_id,
    metric_name,metric_description,metric_caliber,metric_dimensions,metric_period,metric_period_description,
    metric_lineage,metric_lineage_summary,metric_tags,metric_document,metric_source,metric_model,
    metric_prompt_version,metric_semantic_hash,metric_ai_request
  ) ON CONFLICT(tenant_id,metric_version_id) WHERE subject_type='METRIC_VERSION' DO NOTHING;
  RETURN NEW;
END
$$;

REVOKE ALL ON FUNCTION platform.enqueue_published_metric_semantic_document() FROM PUBLIC;
CREATE TRIGGER metric_versions_enqueue_semantic_document
AFTER UPDATE OF status ON platform.metric_versions
FOR EACH ROW EXECUTE FUNCTION platform.enqueue_published_metric_semantic_document();

-- 为迁移前已经发布的指标版本补建确定性文档，不修改不可变发布快照。
WITH facts AS (
  SELECT version.tenant_id,version.id AS metric_version_id,version.metric_id,
    version.dataset_id,version.dataset_version_id,
    COALESCE(NULLIF(version.definition_json#>>'{metric,name}',''),metric.name) AS name,
    COALESCE(version.definition_json#>>'{metric,description}',metric.description,'') AS description,
    COALESCE(NULLIF(version.definition_json->>'aggregation',''),'UNKNOWN') AS aggregation,
    COALESCE(NULLIF(version.definition_json->>'timeGrain',''),'NONE') AS period,
    COALESCE(NULLIF(version.definition_json->>'nullHandling',''),'IGNORE') AS null_handling,
    metric.code::text AS code,
    COALESCE((SELECT array_agg(value->>'name' ORDER BY ordinal)
      FROM jsonb_array_elements(COALESCE(version.definition_json->'allowedDimensions','[]'::jsonb))
      WITH ORDINALITY AS item(value,ordinal)),'{}'::text[]) AS dimensions,
    COALESCE((SELECT jsonb_agg(value->>'fieldId' ORDER BY ordinal)
      FROM jsonb_array_elements(COALESCE(version.definition_json->'allowedDimensions','[]'::jsonb))
      WITH ORDINALITY AS item(value,ordinal)),'[]'::jsonb) AS dimension_ids
  FROM platform.metric_versions AS version
  JOIN platform.metrics AS metric
    ON metric.tenant_id=version.tenant_id AND metric.id=version.metric_id
  WHERE version.status='PUBLISHED'
), documents AS (
  SELECT facts.*,
    format('基于发布指标定义按 %s 聚合；空值处理为 %s',aggregation,null_handling) AS caliber,
    CASE period WHEN 'DAY' THEN '按日' WHEN 'WEEK' THEN '按周' WHEN 'MONTH' THEN '按月'
      WHEN 'QUARTER' THEN '按季度' WHEN 'YEAR' THEN '按年' ELSE '无固定统计周期' END AS period_description,
    format('来自发布数据集版本 %s，并固定为指标版本 %s',dataset_version_id,metric_version_id) AS lineage_summary,
    array_remove(ARRAY[name,code,'原子指标',aggregation,period] || dimensions,'') AS tags
  FROM facts
), canonical AS (
  SELECT documents.*,
    concat_ws(E'\n','指标名称：'||name,'指标说明：'||description,'统计口径：'||caliber,
      '分析维度：'||array_to_string(dimensions,'、'),
      '统计周期：'||period_description||'（'||period||'）',
      '数据血缘：'||lineage_summary,'检索标签：'||array_to_string(tags,'、')) AS document
  FROM documents
)
INSERT INTO platform.metric_semantic_documents(
  tenant_id,subject_type,metric_id,metric_version_id,dataset_id,dataset_version_id,
  name,description,caliber,dimensions,period,period_description,lineage,lineage_summary,tags,
  document,semantic_source,prompt_version,semantic_input_hash
)
SELECT tenant_id,'METRIC_VERSION',metric_id,metric_version_id,dataset_id,dataset_version_id,
  name,description,caliber,dimensions,period,period_description,
  jsonb_build_object('datasetId',dataset_id::text,'datasetVersionId',dataset_version_id::text,
    'metricId',metric_id::text,'metricVersionId',metric_version_id::text,
    'aggregation',aggregation,'dimensionFieldIds',dimension_ids),
  lineage_summary,tags,document,'RULE','published-metric-semantic-v1',encode(digest(document,'sha256'),'hex')
FROM canonical
ON CONFLICT(tenant_id,metric_version_id) WHERE subject_type='METRIC_VERSION' DO NOTHING;

COMMENT ON TABLE platform.metric_semantic_documents IS
  '指标候选及精确发布指标版本的口径、维度、周期、血缘、标签和 pgvector 向量；不保存业务样本值';
