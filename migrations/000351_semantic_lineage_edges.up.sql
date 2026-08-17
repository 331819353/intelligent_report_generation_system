-- SEM-4S-002: 语义资产血缘边（lineage_edges）。
--
-- 四分区架构（docs/09 §3.3）要求物理血缘与语义依赖是两个可分开遍历的边族：
--   * PHYSICAL —— 由数据集构建与模型绑定推导（COMPUTED），从不手工维护；
--   * SEMANTIC —— 由治理定义蕴含（COMPUTED）或显式声明/导入（DECLARED/IMPORTED）。
--
-- 血缘边是“出处”，不是“连接语义”：askdata.relationships 里的 join 合同会被
-- 编译进 SQL，血缘边永远不会。两者分表保持“结构化字段是执行事实、血缘只做
-- 影响分析与图浏览”的边界。
--
-- COMPUTED 边由投影器幂等重建（按 tenant+domain+family 整体替换）；
-- DECLARED/IMPORTED 边是治理事实，重建时保留。边不删除而是关闭有效期，
-- 影响分析可以回答“当时依赖什么”。
BEGIN;

CREATE TABLE askdata.lineage_edges(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id) ON DELETE RESTRICT,
  domain_id uuid NOT NULL,
  family text NOT NULL CHECK(family IN ('PHYSICAL','SEMANTIC')),
  kind text NOT NULL CHECK(kind IN (
    -- PHYSICAL：模型与数据集之间的读依赖，数据集之间的构建派生。
    'MODEL_READS_DATASET','DATASET_DERIVES_DATASET',
    -- SEMANTIC：治理定义蕴含的依赖。
    'METRIC_USES_MODEL','METRIC_USES_MEASURE','MEASURE_USES_FIELD',
    'METRIC_DEPENDS_METRIC','METRIC_ALLOWS_DIMENSION','DIMENSION_BINDS_FIELD',
    'DIMENSION_USES_MODEL','HIERARCHY_LEVEL','MODEL_JOINS_MODEL',
    'KNOWLEDGE_DESCRIBES'
  )),
  from_type text NOT NULL CHECK(from_type IN (
    'DATASET_VERSION','MODEL','MODEL_FIELD','MEASURE','METRIC','DIMENSION',
    'HIERARCHY','KNOWLEDGE'
  )),
  from_id text NOT NULL CHECK(
    length(from_id) BETWEEN 1 AND 512 AND from_id=btrim(from_id)
    AND from_id !~ '[[:cntrl:]]'
  ),
  from_code text NOT NULL DEFAULT '' CHECK(
    length(from_code)<=512 AND from_code !~ '[[:cntrl:]]'
  ),
  to_type text NOT NULL CHECK(to_type IN (
    'DATASET_VERSION','MODEL','MODEL_FIELD','MEASURE','METRIC','DIMENSION',
    'HIERARCHY','KNOWLEDGE'
  )),
  to_id text NOT NULL CHECK(
    length(to_id) BETWEEN 1 AND 512 AND to_id=btrim(to_id)
    AND to_id !~ '[[:cntrl:]]'
  ),
  to_code text NOT NULL DEFAULT '' CHECK(
    length(to_code)<=512 AND to_code !~ '[[:cntrl:]]'
  ),
  derivation text NOT NULL CHECK(derivation IN ('COMPUTED','DECLARED','IMPORTED')),
  -- 证据：构建运行 ID、公式 AST 路径、join AST 路径等，可回放来源。
  evidence jsonb NOT NULL DEFAULT '{}'::jsonb CHECK(
    jsonb_typeof(evidence)='object' AND pg_column_size(evidence)<=16384
  ),
  valid_from timestamptz NOT NULL DEFAULT now(),
  valid_to timestamptz CHECK(valid_to IS NULL OR valid_to>valid_from),
  created_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT askdata_lineage_edges_identity_tenant_key UNIQUE(id,tenant_id),
  CONSTRAINT askdata_lineage_edges_domain_fk
    FOREIGN KEY(domain_id,tenant_id)
    REFERENCES askdata.domains(id,tenant_id) ON DELETE RESTRICT
);

-- 双向遍历索引：影响分析走 from → to（下游），溯源走 to → from（上游）。
CREATE INDEX askdata_lineage_edges_forward_idx
  ON askdata.lineage_edges(tenant_id,domain_id,from_type,from_id,family)
  WHERE valid_to IS NULL;
CREATE INDEX askdata_lineage_edges_backward_idx
  ON askdata.lineage_edges(tenant_id,domain_id,to_type,to_id,family)
  WHERE valid_to IS NULL;

-- 同一活跃事实只有一条边：COMPUTED 重建按此幂等。
CREATE UNIQUE INDEX askdata_lineage_edges_active_fact_key
  ON askdata.lineage_edges(
    tenant_id,domain_id,kind,from_type,from_id,to_type,to_id
  )
  WHERE valid_to IS NULL;

ALTER TABLE askdata.lineage_edges ENABLE ROW LEVEL SECURITY;
ALTER TABLE askdata.lineage_edges FORCE ROW LEVEL SECURITY;
CREATE POLICY askdata_lineage_edges_domain_isolation
  ON askdata.lineage_edges
  USING(
    askdata.tenant_matches(tenant_id)
    AND askdata.domain_can_access(domain_id)
  )
  WITH CHECK(
    askdata.tenant_matches(tenant_id)
    AND askdata.domain_can_access(domain_id)
  );

COMMIT;
