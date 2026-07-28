-- 独立于 DWS 维度治理的“常用词 → 映射值”语义资产。向量输入严格只取
-- common_term，映射值与知识类型保留为确定性解析结果，不混入 embedding 文本。
CREATE EXTENSION IF NOT EXISTS vector;

CREATE TABLE platform.semantic_term_assets(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id),
  common_term citext NOT NULL CHECK(
    length(common_term::text) BETWEEN 1 AND 256
    AND common_term::text=btrim(common_term::text)
    AND common_term::text !~ '[[:cntrl:]]'
  ),
  mapping_value text NOT NULL CHECK(
    length(mapping_value) BETWEEN 1 AND 1024
    AND mapping_value=btrim(mapping_value)
    AND mapping_value !~ '[[:cntrl:]]'
  ),
  knowledge_type text NOT NULL CHECK(
    length(knowledge_type) BETWEEN 1 AND 128
    AND knowledge_type=btrim(knowledge_type)
    AND knowledge_type !~ '[[:cntrl:]]'
  ),
  status text NOT NULL DEFAULT 'ACTIVE'
    CHECK(status IN ('ACTIVE','DEPRECATED')),
  version bigint NOT NULL DEFAULT 1 CHECK(version>0),
  embedding halfvec(2560),
  embedding_model text NOT NULL DEFAULT '',
  embedding_input_hash text NOT NULL DEFAULT ''
    CHECK(
      embedding_input_hash=''
      OR embedding_input_hash ~ '^[0-9a-f]{64}$'
    ),
  embedding_status text NOT NULL DEFAULT 'PENDING'
    CHECK(embedding_status IN ('PENDING','SUCCEEDED','FAILED','SKIPPED')),
  embedding_error_code text NOT NULL DEFAULT ''
    CHECK(length(embedding_error_code)<=128),
  embedded_at timestamptz,
  created_by uuid NOT NULL,
  updated_by uuid NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT semantic_term_assets_created_by_fk
    FOREIGN KEY(created_by,tenant_id)
    REFERENCES platform.users(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT semantic_term_assets_updated_by_fk
    FOREIGN KEY(updated_by,tenant_id)
    REFERENCES platform.users(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT semantic_term_assets_embedding_shape_check CHECK(
    (
      embedding_status='SUCCEEDED'
      AND embedding IS NOT NULL
      AND btrim(embedding_model)<>''
      AND embedding_input_hash ~ '^[0-9a-f]{64}$'
      AND embedded_at IS NOT NULL
      AND embedding_error_code=''
    )
    OR
    (
      embedding_status='PENDING'
      AND embedding IS NULL
      AND embedding_model=''
      AND embedded_at IS NULL
      AND embedding_error_code=''
    )
    OR
    (
      embedding_status IN ('FAILED','SKIPPED')
      AND embedding IS NULL
      AND embedding_model=''
      AND embedded_at IS NULL
      AND btrim(embedding_error_code)<>''
    )
  ),
  CONSTRAINT semantic_term_assets_identity_tenant_key
    UNIQUE(id,tenant_id),
  CONSTRAINT semantic_term_assets_term_type_key
    UNIQUE(tenant_id,knowledge_type,common_term)
);

CREATE INDEX semantic_term_assets_directory_idx
  ON platform.semantic_term_assets(
    tenant_id,status,knowledge_type,common_term
  );
CREATE INDEX semantic_term_assets_mapping_idx
  ON platform.semantic_term_assets(
    tenant_id,knowledge_type,mapping_value
  );
CREATE INDEX semantic_term_assets_embedding_hnsw_idx
  ON platform.semantic_term_assets
  USING hnsw(embedding halfvec_cosine_ops)
  WITH (m=16,ef_construction=64)
  WHERE status='ACTIVE'
    AND embedding_status='SUCCEEDED';

ALTER TABLE platform.semantic_term_assets ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform.semantic_term_assets FORCE ROW LEVEL SECURITY;
CREATE POLICY semantic_term_assets_tenant_isolation
  ON platform.semantic_term_assets
  USING(tenant_id=platform.current_tenant_id())
  WITH CHECK(tenant_id=platform.current_tenant_id());

CREATE TABLE platform.semantic_term_embedding_outbox(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id),
  semantic_term_asset_id uuid NOT NULL,
  event_version bigint NOT NULL DEFAULT 1 CHECK(event_version>0),
  status text NOT NULL DEFAULT 'PENDING'
    CHECK(status IN ('PENDING','RUNNING','SUCCEEDED','FAILED','SKIPPED')),
  attempt integer NOT NULL DEFAULT 0 CHECK(attempt BETWEEN 0 AND 3),
  error_code text NOT NULL DEFAULT '' CHECK(length(error_code)<=128),
  next_attempt_at timestamptz NOT NULL DEFAULT now(),
  lease_owner text NOT NULL DEFAULT '' CHECK(length(lease_owner)<=128),
  lease_expires_at timestamptz,
  completed_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT semantic_term_embedding_outbox_asset_fk
    FOREIGN KEY(semantic_term_asset_id,tenant_id)
    REFERENCES platform.semantic_term_assets(id,tenant_id) ON DELETE CASCADE,
  CONSTRAINT semantic_term_embedding_outbox_lease_shape_check CHECK(
    (
      status='RUNNING'
      AND attempt>0
      AND lease_owner<>''
      AND lease_expires_at IS NOT NULL
      AND completed_at IS NULL
      AND error_code=''
    )
    OR
    (
      status<>'RUNNING'
      AND lease_owner=''
      AND lease_expires_at IS NULL
    )
  ),
  CONSTRAINT semantic_term_embedding_outbox_completion_shape_check CHECK(
    (
      status IN ('SUCCEEDED','SKIPPED')
      AND completed_at IS NOT NULL
      AND error_code=''
    )
    OR
    (
      status='FAILED'
      AND completed_at IS NOT NULL
      AND btrim(error_code)<>''
    )
    OR
    (
      status IN ('PENDING','RUNNING')
      AND completed_at IS NULL
    )
  ),
  CONSTRAINT semantic_term_embedding_outbox_asset_key
    UNIQUE(tenant_id,semantic_term_asset_id)
);

CREATE INDEX semantic_term_embedding_outbox_claim_idx
  ON platform.semantic_term_embedding_outbox(
    tenant_id,status,next_attempt_at,lease_expires_at,updated_at,id
  );

ALTER TABLE platform.semantic_term_embedding_outbox ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform.semantic_term_embedding_outbox FORCE ROW LEVEL SECURITY;
CREATE POLICY semantic_term_embedding_outbox_tenant_isolation
  ON platform.semantic_term_embedding_outbox
  USING(tenant_id=platform.current_tenant_id())
  WITH CHECK(tenant_id=platform.current_tenant_id());

CREATE OR REPLACE FUNCTION platform.enqueue_semantic_term_embedding()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,platform
AS $$
BEGIN
  INSERT INTO platform.semantic_term_embedding_outbox(
    tenant_id,semantic_term_asset_id
  ) VALUES(
    NEW.tenant_id,NEW.id
  )
  ON CONFLICT(tenant_id,semantic_term_asset_id) DO UPDATE SET
    event_version=platform.semantic_term_embedding_outbox.event_version+1,
    status='PENDING',attempt=0,error_code='',next_attempt_at=now(),
    lease_owner='',lease_expires_at=NULL,completed_at=NULL,updated_at=now();
  RETURN NEW;
END
$$;

REVOKE ALL ON FUNCTION platform.enqueue_semantic_term_embedding()
  FROM PUBLIC;

CREATE TRIGGER semantic_term_assets_enqueue_embedding
AFTER INSERT OR UPDATE OF common_term,status
ON platform.semantic_term_assets
FOR EACH ROW
EXECUTE FUNCTION platform.enqueue_semantic_term_embedding();

CREATE TRIGGER semantic_term_assets_set_updated_at
BEFORE UPDATE ON platform.semantic_term_assets
FOR EACH ROW
EXECUTE FUNCTION platform.set_updated_at();

CREATE TRIGGER semantic_term_embedding_outbox_set_updated_at
BEFORE UPDATE ON platform.semantic_term_embedding_outbox
FOR EACH ROW
EXECUTE FUNCTION platform.set_updated_at();

COMMENT ON TABLE platform.semantic_term_assets IS
  '独立语义词库资产；common_term 是唯一向量输入，mapping_value 是解析结果，knowledge_type 是业务类型';
COMMENT ON TABLE platform.semantic_term_embedding_outbox IS
  '语义常用词向量化的租户隔离、可恢复合并 outbox';
