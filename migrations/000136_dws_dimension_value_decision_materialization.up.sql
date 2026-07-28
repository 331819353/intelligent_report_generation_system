BEGIN;

-- Query-observed decisions remain valid evidence, but a DWS-wide build does not
-- have a query plan. Exact member decisions reuse the governed member vector
-- document instead of duplicating a 2560-dimensional vector for every
-- dimension × metric edge.
ALTER TABLE platform.dimension_member_semantic_documents
  ADD CONSTRAINT dimension_member_semantic_documents_identity_tenant_key
  UNIQUE(id,tenant_id);

ALTER TABLE platform.dimension_where_decisions
  ALTER COLUMN latest_query_plan_id DROP NOT NULL,
  ALTER COLUMN embedding DROP NOT NULL,
  ADD COLUMN dimension_member_id uuid,
  ADD COLUMN embedding_document_id uuid,
  ADD COLUMN source_type text NOT NULL DEFAULT 'QUERY_OBSERVED',
  ADD COLUMN source_input_hash text NOT NULL DEFAULT '';

ALTER TABLE platform.dimension_where_decisions
  DROP CONSTRAINT dimension_where_decisions_llm_prompt_version_check,
  DROP CONSTRAINT dimension_where_decisions_selected_member_count_check;

ALTER TABLE platform.dimension_where_decisions
  ADD CONSTRAINT dimension_where_decisions_llm_prompt_version_check
    CHECK(llm_prompt_version IN (
      'semantic-query-where-design-v2',
      'dws-dimension-where-policy-v1'
    )),
  ADD CONSTRAINT dimension_where_decisions_selected_member_count_check
    CHECK(selected_member_count BETWEEN 1 AND 1000000),
  ADD CONSTRAINT dimension_where_decisions_source_type_check
    CHECK(source_type IN ('QUERY_OBSERVED','DWS_PRECOMPUTED')),
  ADD CONSTRAINT dimension_where_decisions_source_input_hash_check
    CHECK(
      source_input_hash=''
      OR source_input_hash ~ '^[0-9a-f]{64}$'
    ),
  ADD CONSTRAINT dimension_where_decisions_embedding_source_check
    CHECK(
      (embedding IS NOT NULL AND embedding_document_id IS NULL)
      OR (embedding IS NULL AND embedding_document_id IS NOT NULL)
    ),
  ADD CONSTRAINT dimension_where_decisions_member_fk
    FOREIGN KEY(dimension_member_id,dimension_id,tenant_id)
    REFERENCES platform.dimension_members(id,dimension_id,tenant_id)
    ON DELETE CASCADE,
  ADD CONSTRAINT dimension_where_decisions_embedding_document_fk
    FOREIGN KEY(embedding_document_id,tenant_id)
    REFERENCES platform.dimension_member_semantic_documents(id,tenant_id)
    ON DELETE CASCADE;

CREATE INDEX dimension_where_decisions_source_idx
  ON platform.dimension_where_decisions(
    tenant_id,source_type,dimension_id,last_seen_at DESC
  );

-- One constrained LLM decision is made per governed dimension/metric/table
-- scope. The resulting policy is then applied to every non-empty governed
-- member. This avoids one LLM call per member while retaining auditable LLM
-- predicate design.
CREATE TABLE platform.dimension_where_design_policies(
  id uuid PRIMARY KEY DEFAULT public.gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id),
  dimension_id uuid NOT NULL,
  metric_id uuid NOT NULL,
  metric_version_id uuid NOT NULL,
  dataset_version_id uuid NOT NULL,
  materialization_id uuid NOT NULL,
  input_hash text NOT NULL CHECK(input_hash ~ '^[0-9a-f]{64}$'),
  dimension_field_name text NOT NULL CHECK(
    length(dimension_field_name) BETWEEN 1 AND 256
    AND dimension_field_name=btrim(dimension_field_name)
  ),
  dimension_description text NOT NULL CHECK(
    length(dimension_description) BETWEEN 1 AND 2048
    AND dimension_description=btrim(dimension_description)
  ),
  metric_code text NOT NULL CHECK(
    length(metric_code) BETWEEN 1 AND 256
    AND metric_code=btrim(metric_code)
  ),
  metric_field_id text NOT NULL CHECK(
    length(metric_field_id) BETWEEN 1 AND 256
  ),
  table_schema text NOT NULL CHECK(
    length(table_schema) BETWEEN 1 AND 63
    AND table_schema ~ '^[a-z][a-z0-9_]{0,62}$'
  ),
  table_name text NOT NULL CHECK(
    length(table_name) BETWEEN 1 AND 63
    AND table_name ~ '^[a-z][a-z0-9_]{0,62}$'
  ),
  actor_id uuid NOT NULL,
  sample_values text[] NOT NULL DEFAULT '{}',
  status text NOT NULL DEFAULT 'PENDING' CHECK(
    status IN ('PENDING','RUNNING','SUCCEEDED','FAILED')
  ),
  predicate_operator text NOT NULL DEFAULT '' CHECK(
    predicate_operator='' OR predicate_operator IN ('EQUALS','CONTAINS')
  ),
  llm_model text NOT NULL DEFAULT '',
  llm_prompt_version text NOT NULL
    DEFAULT 'dws-dimension-where-policy-v1'
    CHECK(llm_prompt_version='dws-dimension-where-policy-v1'),
  llm_reason text NOT NULL DEFAULT '',
  confidence numeric(5,4) CHECK(
    confidence IS NULL OR confidence BETWEEN 0 AND 1
  ),
  attempt integer NOT NULL DEFAULT 0 CHECK(attempt BETWEEN 0 AND 3),
  next_attempt_at timestamptz NOT NULL DEFAULT now(),
  lease_owner text NOT NULL DEFAULT '',
  lease_token uuid,
  lease_expires_at timestamptz,
  error_code text NOT NULL DEFAULT '',
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  completed_at timestamptz,
  CONSTRAINT dimension_where_design_policies_dimension_fk
    FOREIGN KEY(dimension_id,tenant_id)
    REFERENCES platform.semantic_dimensions(id,tenant_id)
    ON DELETE CASCADE,
  CONSTRAINT dimension_where_design_policies_metric_fk
    FOREIGN KEY(metric_version_id,metric_id,dataset_version_id,tenant_id)
    REFERENCES platform.metric_versions(
      id,metric_id,dataset_version_id,tenant_id
    ) ON DELETE CASCADE,
  CONSTRAINT dimension_where_design_policies_materialization_fk
    FOREIGN KEY(materialization_id,tenant_id)
    REFERENCES platform.dataset_materializations(id,tenant_id)
    ON DELETE CASCADE,
  CONSTRAINT dimension_where_design_policies_actor_fk
    FOREIGN KEY(actor_id,tenant_id)
    REFERENCES platform.users(id,tenant_id)
    ON DELETE RESTRICT,
  CONSTRAINT dimension_where_design_policies_scope_key UNIQUE(
    tenant_id,dimension_id,metric_version_id,materialization_id
  ),
  CONSTRAINT dimension_where_design_policies_state_check CHECK(
    (
      status='PENDING'
      AND predicate_operator='' AND llm_model='' AND llm_reason=''
      AND confidence IS NULL AND lease_owner='' AND lease_token IS NULL
      AND lease_expires_at IS NULL AND completed_at IS NULL
    )
    OR (
      status='RUNNING'
      AND predicate_operator='' AND llm_model='' AND llm_reason=''
      AND confidence IS NULL AND lease_owner<>'' AND lease_token IS NOT NULL
      AND lease_expires_at IS NOT NULL AND completed_at IS NULL
    )
    OR (
      status='SUCCEEDED'
      AND predicate_operator IN ('EQUALS','CONTAINS')
      AND btrim(llm_model)<>'' AND btrim(llm_reason)<>''
      AND confidence IS NOT NULL AND confidence>=0.80
      AND lease_owner='' AND lease_token IS NULL
      AND lease_expires_at IS NULL AND error_code=''
      AND completed_at IS NOT NULL
    )
    OR (
      status='FAILED'
      AND predicate_operator='' AND llm_model='' AND llm_reason=''
      AND confidence IS NULL AND lease_owner='' AND lease_token IS NULL
      AND lease_expires_at IS NULL AND btrim(error_code)<>''
      AND completed_at IS NOT NULL
    )
  )
);

CREATE INDEX dimension_where_design_policies_claim_idx
  ON platform.dimension_where_design_policies(
    tenant_id,status,next_attempt_at,lease_expires_at,created_at
  );

ALTER TABLE platform.dimension_where_design_policies ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform.dimension_where_design_policies FORCE ROW LEVEL SECURITY;
CREATE POLICY dimension_where_design_policies_tenant_isolation
  ON platform.dimension_where_design_policies
  USING(tenant_id=platform.current_tenant_id())
  WITH CHECK(tenant_id=platform.current_tenant_id());

-- The exact retrieval key required by the decision graph is
-- "dimension description:canonical value". Domain/name remain separate
-- columns for scoping and display, but they are no longer mixed into the
-- embedding input.
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
    btrim(dimension_row.description),':',NEW.canonical_label
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

WITH desired AS (
  SELECT document.id,
    platform.dataset_version_effective_domain(
      document.dataset_version_id
    ) AS domain_name,
    concat(btrim(dimension.description),':',member.canonical_label)
      AS vector_key
  FROM platform.dimension_member_semantic_documents AS document
  JOIN platform.semantic_dimensions AS dimension
    ON dimension.tenant_id=document.tenant_id
   AND dimension.id=document.dimension_id
  JOIN platform.dimension_members AS member
    ON member.tenant_id=document.tenant_id
   AND member.id=document.dimension_member_id
)
UPDATE platform.dimension_member_semantic_documents AS document
SET domain=desired.domain_name,
    document=desired.vector_key,
    input_hash=encode(public.digest(
      convert_to(desired.vector_key,'UTF8'),'sha256'
    ),'hex'),
    embedding=NULL,embedding_model='',embedding_input_hash='',
    embedding_status='PENDING',embedding_attempt=0,
    embedding_error_code='',next_attempt_at=now(),
    lease_owner='',lease_expires_at=NULL,embedded_at=NULL,updated_at=now()
FROM desired
WHERE document.id=desired.id
  AND (
    document.document IS DISTINCT FROM desired.vector_key
    OR document.domain IS DISTINCT FROM desired.domain_name
  );

-- A RULE + DIRECT + SAFE + confidence=1 relation on the same published DWS is
-- a deterministic metadata fact. Auto-verifying it removes an artificial
-- manual bottleneck before the full member decision build.
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

CREATE TRIGGER dimension_metric_compatibility_auto_verify_rule
AFTER INSERT ON platform.dimension_metric_compatibility
FOR EACH ROW EXECUTE FUNCTION
  platform.auto_verify_rule_dimension_metric_compatibility();

UPDATE platform.dimension_metric_compatibility
SET status='VERIFIED',
    verified_by=COALESCE(updated_by,created_by),
    verified_at=now(),
    version=version+1,
    updated_by=COALESCE(updated_by,created_by)
WHERE status='PROPOSED'
  AND evidence_source='RULE'
  AND compatibility_type='DIRECT'
  AND fanout_policy='SAFE'
  AND confidence=1.0000
  AND join_path_json='[]'::jsonb
  AND COALESCE(updated_by,created_by) IS NOT NULL;

-- Repair historical rows that persisted the logical dimension code instead of
-- the actual DWS field code. Values remain parameterized; only the trusted
-- leading identifier changes.
WITH corrected AS (
  SELECT decision.id,decision.dimension_field_name AS previous_name,
    field.field_code::text AS current_name
  FROM platform.dimension_where_decisions AS decision
  JOIN platform.semantic_dimensions AS dimension
    ON dimension.tenant_id=decision.tenant_id
   AND dimension.id=decision.dimension_id
  JOIN platform.dataset_fields AS field
    ON field.tenant_id=dimension.tenant_id
   AND field.dataset_version_id=dimension.dataset_version_id
   AND field.field_id=dimension.field_id
  WHERE decision.dimension_field_name<>field.field_code::text
)
UPDATE platform.dimension_where_decisions AS decision
SET dimension_field_name=corrected.current_name,
    where_condition=CASE
      WHEN left(
        decision.where_condition,length(corrected.previous_name)
      )=corrected.previous_name
      THEN corrected.current_name||substr(
        decision.where_condition,length(corrected.previous_name)+1
      )
      ELSE decision.where_condition
    END,
    compiled_condition=CASE
      WHEN left(
        decision.compiled_condition,length(corrected.previous_name)
      )=corrected.previous_name
      THEN corrected.current_name||substr(
        decision.compiled_condition,length(corrected.previous_name)+1
      )
      ELSE decision.compiled_condition
    END
FROM corrected
WHERE decision.id=corrected.id;

COMMENT ON TABLE platform.dimension_where_design_policies IS
  '逐维度、指标版本和DWS物化范围的受约束LLM WHERE策略；成功策略批量物化全部非空治理成员';
COMMENT ON COLUMN platform.dimension_where_decisions.source_type IS
  'QUERY_OBSERVED为真实问答证据；DWS_PRECOMPUTED为全量DWS成员预计算关系';
COMMENT ON COLUMN platform.dimension_where_decisions.embedding_document_id IS
  '全量成员决策复用“维度描述:规范值”的成员向量文档，避免重复存储大向量';
COMMENT ON TABLE platform.dimension_member_semantic_documents IS
  '非敏感、低基数、FULL策略维度值向量文档；embedding输入严格为“维度描述:规范值”';

COMMIT;
