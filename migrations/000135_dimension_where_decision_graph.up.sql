BEGIN;

CREATE EXTENSION IF NOT EXISTS vector;

-- A durable, query-derived decision edge. The human-readable vector_key is
-- exactly "dimension description:canonical dimension value". Its embedding
-- is the retrieval key; table/metric/WHERE metadata is the governed value.
CREATE TABLE platform.dimension_where_decisions(
  id uuid PRIMARY KEY DEFAULT public.gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id),
  vector_key text NOT NULL CHECK(
    length(vector_key) BETWEEN 3 AND 4096
    AND vector_key=btrim(vector_key)
    AND vector_key !~ '[[:cntrl:]]'
  ),
  vector_key_hash text NOT NULL CHECK(vector_key_hash ~ '^[0-9a-f]{64}$'),
  embedding halfvec(2560) NOT NULL,
  embedding_model text NOT NULL CHECK(
    length(embedding_model) BETWEEN 1 AND 256
    AND embedding_model=btrim(embedding_model)
  ),
  dimension_id uuid NOT NULL,
  dimension_field_id text NOT NULL CHECK(
    length(dimension_field_id) BETWEEN 1 AND 256
  ),
  dimension_field_name text NOT NULL CHECK(
    length(dimension_field_name) BETWEEN 1 AND 256
    AND dimension_field_name=btrim(dimension_field_name)
  ),
  dimension_description text NOT NULL CHECK(
    length(dimension_description) BETWEEN 1 AND 2048
    AND dimension_description=btrim(dimension_description)
  ),
  canonical_value text NOT NULL CHECK(
    length(canonical_value) BETWEEN 1 AND 1024
    AND canonical_value=btrim(canonical_value)
    AND canonical_value !~ '[[:cntrl:]]'
  ),
  aliases text[] NOT NULL DEFAULT '{}',
  selected_member_set_hash text NOT NULL CHECK(
    selected_member_set_hash ~ '^[0-9a-f]{64}$'
  ),
  selected_member_count integer NOT NULL CHECK(
    selected_member_count BETWEEN 1 AND 128
  ),
  metric_id uuid NOT NULL,
  metric_version_id uuid NOT NULL,
  dataset_version_id uuid NOT NULL,
  metric_code text NOT NULL CHECK(
    length(metric_code) BETWEEN 1 AND 256
    AND metric_code=btrim(metric_code)
  ),
  metric_name text NOT NULL CHECK(
    length(metric_name) BETWEEN 1 AND 512
    AND metric_name=btrim(metric_name)
  ),
  metric_field_id text NOT NULL CHECK(
    length(metric_field_id) BETWEEN 1 AND 256
  ),
  materialization_id uuid NOT NULL,
  table_schema text NOT NULL CHECK(
    length(table_schema) BETWEEN 1 AND 63
    AND table_schema ~ '^[a-z][a-z0-9_]{0,62}$'
  ),
  table_name text NOT NULL CHECK(
    length(table_name) BETWEEN 1 AND 63
    AND table_name ~ '^[a-z][a-z0-9_]{0,62}$'
  ),
  predicate_operator text NOT NULL CHECK(
    predicate_operator IN ('EQUALS','IN','CONTAINS')
  ),
  where_condition text NOT NULL CHECK(
    length(where_condition) BETWEEN 1 AND 16384
    AND where_condition !~ '[[:cntrl:]]'
  ),
  compiled_condition text NOT NULL CHECK(
    length(compiled_condition) BETWEEN 1 AND 16384
    AND compiled_condition !~ '[[:cntrl:]]'
  ),
  llm_model text NOT NULL CHECK(
    length(llm_model) BETWEEN 1 AND 256
    AND llm_model=btrim(llm_model)
  ),
  llm_prompt_version text NOT NULL CHECK(
    llm_prompt_version='semantic-query-where-design-v2'
  ),
  llm_reason text NOT NULL CHECK(
    length(llm_reason) BETWEEN 1 AND 1024
    AND llm_reason=btrim(llm_reason)
  ),
  latest_query_plan_id uuid NOT NULL,
  observation_count bigint NOT NULL DEFAULT 1 CHECK(observation_count>0),
  first_seen_at timestamptz NOT NULL DEFAULT now(),
  last_seen_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT dimension_where_decisions_dimension_fk
    FOREIGN KEY(dimension_id,tenant_id)
    REFERENCES platform.semantic_dimensions(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT dimension_where_decisions_metric_version_fk
    FOREIGN KEY(metric_version_id,metric_id,dataset_version_id,tenant_id)
    REFERENCES platform.metric_versions(
      id,metric_id,dataset_version_id,tenant_id
    ) ON DELETE RESTRICT,
  CONSTRAINT dimension_where_decisions_materialization_fk
    FOREIGN KEY(materialization_id,tenant_id)
    REFERENCES platform.dataset_materializations(id,tenant_id)
    ON DELETE RESTRICT,
  CONSTRAINT dimension_where_decisions_query_plan_fk
    FOREIGN KEY(latest_query_plan_id,tenant_id)
    REFERENCES platform.semantic_query_plans(id,tenant_id)
    ON DELETE RESTRICT,
  CONSTRAINT dimension_where_decisions_aliases_check CHECK(
    cardinality(aliases)<=64
    AND array_position(aliases,'') IS NULL
    AND array_to_string(aliases,'') !~ '[[:cntrl:]]'
  ),
  CONSTRAINT dimension_where_decisions_semantic_target_key UNIQUE(
    tenant_id,dimension_id,selected_member_set_hash,
    metric_version_id,materialization_id
  )
);

CREATE INDEX dimension_where_decisions_vector_key_idx
  ON platform.dimension_where_decisions(
    tenant_id,vector_key_hash,last_seen_at DESC
  );
CREATE INDEX dimension_where_decisions_table_idx
  ON platform.dimension_where_decisions(
    tenant_id,table_schema,table_name,last_seen_at DESC
  );
CREATE INDEX dimension_where_decisions_dimension_idx
  ON platform.dimension_where_decisions(
    tenant_id,dimension_id,last_seen_at DESC
  );
CREATE INDEX dimension_where_decisions_embedding_hnsw_idx
  ON platform.dimension_where_decisions
  USING hnsw(embedding halfvec_cosine_ops)
  WITH (m=16,ef_construction=64);

ALTER TABLE platform.dimension_where_decisions ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform.dimension_where_decisions FORCE ROW LEVEL SECURITY;
CREATE POLICY dimension_where_decisions_tenant_isolation
  ON platform.dimension_where_decisions
  USING(tenant_id=platform.current_tenant_id())
  WITH CHECK(tenant_id=platform.current_tenant_id());

COMMENT ON TABLE platform.dimension_where_decisions IS
  '逐维度 LLM 决策图：向量键映射到表、指标和经服务端验证的 WHERE；同义值按治理成员集合合并并保留别名';
COMMENT ON COLUMN platform.dimension_where_decisions.where_condition IS
  '供用户审计的业务 WHERE；来自受约束 LLM AST，经服务端重新生成';
COMMENT ON COLUMN platform.dimension_where_decisions.compiled_condition IS
  '实际执行使用的参数化逻辑字段条件，不含原始 LLM SQL';

COMMIT;
