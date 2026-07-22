-- 表/字段业务元数据的确定性检索文档、pgvector 向量及事务 outbox。
CREATE EXTENSION IF NOT EXISTS vector;

CREATE TABLE platform.asset_embeddings(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id),
  asset_type text NOT NULL CHECK(asset_type IN ('TABLE','COLUMN')),
  asset_id uuid NOT NULL,
  table_id uuid NOT NULL,
  document_version text NOT NULL CHECK(btrim(document_version)<>''),
  document text NOT NULL CHECK(length(document) BETWEEN 1 AND 262144),
  input_hash text NOT NULL CHECK(input_hash ~ '^[0-9a-f]{64}$'),
  embedding halfvec(2560),
  embedding_model text NOT NULL DEFAULT '',
  model_version text NOT NULL DEFAULT '',
  status text NOT NULL DEFAULT 'PENDING' CHECK(status IN ('PENDING','SUCCEEDED','FAILED')),
  error_code text NOT NULL DEFAULT '',
  embedded_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY(table_id,tenant_id) REFERENCES platform.metadata_tables(id,tenant_id) ON DELETE CASCADE,
  CONSTRAINT asset_embeddings_subject_shape_check CHECK(asset_type<>'TABLE' OR asset_id=table_id),
  CONSTRAINT asset_embeddings_status_shape_check CHECK(
    (status='SUCCEEDED' AND embedding IS NOT NULL AND btrim(embedding_model)<>''
      AND embedded_at IS NOT NULL AND error_code='')
    OR
    (status IN ('PENDING','FAILED') AND embedding IS NULL AND embedded_at IS NULL)
  ),
  UNIQUE(tenant_id,asset_type,asset_id)
);

CREATE INDEX asset_embeddings_table_idx
  ON platform.asset_embeddings(tenant_id,table_id,asset_type,status);
CREATE INDEX asset_embeddings_input_hash_idx
  ON platform.asset_embeddings(tenant_id,input_hash);
CREATE INDEX asset_embeddings_hnsw_idx
  ON platform.asset_embeddings USING hnsw(embedding halfvec_cosine_ops)
  WITH (m=16,ef_construction=64)
  WHERE status='SUCCEEDED';

ALTER TABLE platform.asset_embeddings ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform.asset_embeddings FORCE ROW LEVEL SECURITY;
CREATE POLICY asset_embeddings_tenant_isolation ON platform.asset_embeddings
  USING(tenant_id=platform.current_tenant_id())
  WITH CHECK(tenant_id=platform.current_tenant_id());

CREATE TABLE platform.asset_embedding_outbox(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id),
  asset_type text NOT NULL CHECK(asset_type IN ('TABLE','COLUMN')),
  asset_id uuid NOT NULL,
  table_id uuid NOT NULL,
  status text NOT NULL DEFAULT 'PENDING'
    CHECK(status IN ('PENDING','RUNNING','SUCCEEDED','FAILED','SKIPPED')),
  event_version bigint NOT NULL DEFAULT 1 CHECK(event_version>0),
  attempt integer NOT NULL DEFAULT 0 CHECK(attempt>=0),
  error_code text NOT NULL DEFAULT '',
  next_attempt_at timestamptz NOT NULL DEFAULT now(),
  lease_owner text NOT NULL DEFAULT '',
  lease_expires_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  completed_at timestamptz,
  FOREIGN KEY(table_id,tenant_id) REFERENCES platform.metadata_tables(id,tenant_id) ON DELETE CASCADE,
  CONSTRAINT asset_embedding_outbox_subject_shape_check CHECK(asset_type<>'TABLE' OR asset_id=table_id),
  CONSTRAINT asset_embedding_outbox_lease_shape_check CHECK(
    (status='RUNNING' AND lease_owner<>'' AND lease_expires_at IS NOT NULL AND completed_at IS NULL)
    OR
    (status<>'RUNNING' AND lease_owner='' AND lease_expires_at IS NULL)
  ),
  UNIQUE(tenant_id,asset_type,asset_id)
);

CREATE INDEX asset_embedding_outbox_claim_idx
  ON platform.asset_embedding_outbox(tenant_id,status,next_attempt_at,lease_expires_at,updated_at);

ALTER TABLE platform.asset_embedding_outbox ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform.asset_embedding_outbox FORCE ROW LEVEL SECURITY;
CREATE POLICY asset_embedding_outbox_tenant_isolation ON platform.asset_embedding_outbox
  USING(tenant_id=platform.current_tenant_id())
  WITH CHECK(tenant_id=platform.current_tenant_id());

CREATE OR REPLACE FUNCTION platform.enqueue_asset_embedding_change()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
  changed_table_id uuid;
BEGIN
  IF TG_TABLE_NAME='metadata_tables' THEN
    changed_table_id := NEW.id;
    INSERT INTO platform.asset_embedding_outbox(tenant_id,asset_type,asset_id,table_id)
    VALUES(NEW.tenant_id,'TABLE',NEW.id,NEW.id)
    ON CONFLICT(tenant_id,asset_type,asset_id) DO UPDATE SET
      table_id=EXCLUDED.table_id,status='PENDING',event_version=platform.asset_embedding_outbox.event_version+1,
      attempt=0,error_code='',next_attempt_at=now(),lease_owner='',lease_expires_at=NULL,
      completed_at=NULL,updated_at=now();

    -- 字段文档包含所属表业务名称；表变化、停用或恢复时字段文档也必须重算或清除。
    INSERT INTO platform.asset_embedding_outbox(tenant_id,asset_type,asset_id,table_id)
    SELECT NEW.tenant_id,'COLUMN',column_asset.id,NEW.id
    FROM platform.metadata_columns AS column_asset
    WHERE column_asset.tenant_id=NEW.tenant_id AND column_asset.table_id=NEW.id
    ON CONFLICT(tenant_id,asset_type,asset_id) DO UPDATE SET
      table_id=EXCLUDED.table_id,status='PENDING',event_version=platform.asset_embedding_outbox.event_version+1,
      attempt=0,error_code='',next_attempt_at=now(),lease_owner='',lease_expires_at=NULL,
      completed_at=NULL,updated_at=now();
  ELSE
    changed_table_id := NEW.table_id;
    INSERT INTO platform.asset_embedding_outbox(tenant_id,asset_type,asset_id,table_id)
    VALUES(NEW.tenant_id,'COLUMN',NEW.id,NEW.table_id)
    ON CONFLICT(tenant_id,asset_type,asset_id) DO UPDATE SET
      table_id=EXCLUDED.table_id,status='PENDING',event_version=platform.asset_embedding_outbox.event_version+1,
      attempt=0,error_code='',next_attempt_at=now(),lease_owner='',lease_expires_at=NULL,
      completed_at=NULL,updated_at=now();

    -- 表文档聚合全部活动字段，所以任一字段变化都只额外重建所属表。
    INSERT INTO platform.asset_embedding_outbox(tenant_id,asset_type,asset_id,table_id)
    VALUES(NEW.tenant_id,'TABLE',NEW.table_id,NEW.table_id)
    ON CONFLICT(tenant_id,asset_type,asset_id) DO UPDATE SET
      table_id=EXCLUDED.table_id,status='PENDING',event_version=platform.asset_embedding_outbox.event_version+1,
      attempt=0,error_code='',next_attempt_at=now(),lease_owner='',lease_expires_at=NULL,
      completed_at=NULL,updated_at=now();
  END IF;
  RETURN NEW;
END
$$;

REVOKE ALL ON FUNCTION platform.enqueue_asset_embedding_change() FROM PUBLIC;
CREATE TRIGGER metadata_tables_enqueue_asset_embedding
AFTER INSERT OR UPDATE ON platform.metadata_tables
FOR EACH ROW EXECUTE FUNCTION platform.enqueue_asset_embedding_change();
CREATE TRIGGER metadata_columns_enqueue_asset_embedding
AFTER INSERT OR UPDATE ON platform.metadata_columns
FOR EACH ROW EXECUTE FUNCTION platform.enqueue_asset_embedding_change();

-- 存量资产回填；Worker 会在生成前再次复核启用、结构补全和语义类型质量门。
INSERT INTO platform.asset_embedding_outbox(tenant_id,asset_type,asset_id,table_id)
SELECT tenant_id,'TABLE',id,id FROM platform.metadata_tables
ON CONFLICT(tenant_id,asset_type,asset_id) DO NOTHING;
INSERT INTO platform.asset_embedding_outbox(tenant_id,asset_type,asset_id,table_id)
SELECT tenant_id,'COLUMN',id,table_id FROM platform.metadata_columns
ON CONFLICT(tenant_id,asset_type,asset_id) DO NOTHING;

COMMENT ON TABLE platform.asset_embeddings IS
  '映射表和字段的无样本、无凭据确定性检索文档及 halfvec(2560) 向量';
COMMENT ON TABLE platform.asset_embedding_outbox IS
  '元数据事务内幂等登记的资产向量重建事件；同一资产只保留最新 event_version';
