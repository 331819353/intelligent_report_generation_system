BEGIN;

CREATE EXTENSION IF NOT EXISTS vector;

-- Independent dimension-definition index.
-- Vector key: dimension name.
-- Governed value: dimension field + dimension name/description.
CREATE TABLE platform.dimension_semantic_documents(
  id uuid PRIMARY KEY DEFAULT public.gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id),
  dimension_id uuid NOT NULL,
  dataset_id uuid NOT NULL,
  dataset_version_id uuid NOT NULL,
  field_id text NOT NULL CHECK(length(field_id) BETWEEN 1 AND 256),
  dimension_code citext NOT NULL,
  dimension_name text NOT NULL CHECK(
    length(dimension_name) BETWEEN 1 AND 256
    AND dimension_name=btrim(dimension_name)
  ),
  dimension_description text NOT NULL DEFAULT '' CHECK(
    length(dimension_description)<=4096
  ),
  dimension_type text NOT NULL CHECK(dimension_type IN (
    'STANDARD','TIME','GEOGRAPHY','ORGANIZATION',
    'PRODUCT','CUSTOMER','OTHER'
  )),
  document text NOT NULL CHECK(
    length(document) BETWEEN 1 AND 256
    AND document=btrim(document)
  ),
  input_hash text NOT NULL CHECK(input_hash ~ '^[0-9a-f]{64}$'),
  embedding halfvec(2560),
  embedding_model text NOT NULL DEFAULT '',
  embedding_input_hash text NOT NULL DEFAULT '' CHECK(
    embedding_input_hash=''
    OR embedding_input_hash ~ '^[0-9a-f]{64}$'
  ),
  embedding_status text NOT NULL DEFAULT 'PENDING' CHECK(
    embedding_status IN ('PENDING','RUNNING','SUCCEEDED','FAILED')
  ),
  embedding_attempt integer NOT NULL DEFAULT 0 CHECK(embedding_attempt>=0),
  embedding_error_code text NOT NULL DEFAULT '',
  next_attempt_at timestamptz NOT NULL DEFAULT now(),
  lease_owner text NOT NULL DEFAULT '',
  lease_expires_at timestamptz,
  embedded_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT dimension_semantic_documents_dimension_fk
    FOREIGN KEY(dimension_id,tenant_id)
    REFERENCES platform.semantic_dimensions(id,tenant_id) ON DELETE CASCADE,
  CONSTRAINT dimension_semantic_documents_dataset_version_fk
    FOREIGN KEY(dataset_version_id,dataset_id,tenant_id)
    REFERENCES platform.dataset_versions(id,dataset_id,tenant_id)
    ON DELETE CASCADE,
  CONSTRAINT dimension_semantic_documents_identity_key
    UNIQUE(tenant_id,dimension_id),
  CONSTRAINT dimension_semantic_documents_embedding_shape_check CHECK(
    (
      embedding_status='SUCCEEDED'
      AND embedding IS NOT NULL
      AND btrim(embedding_model)<>''
      AND embedding_input_hash<>''
      AND embedded_at IS NOT NULL
      AND lease_owner=''
      AND lease_expires_at IS NULL
      AND embedding_error_code=''
    )
    OR
    (
      embedding_status='RUNNING'
      AND embedding IS NULL
      AND lease_owner<>''
      AND lease_expires_at IS NOT NULL
      AND embedded_at IS NULL
    )
    OR
    (
      embedding_status IN ('PENDING','FAILED')
      AND embedding IS NULL
      AND lease_owner=''
      AND lease_expires_at IS NULL
      AND embedded_at IS NULL
    )
  )
);

CREATE INDEX dimension_semantic_documents_claim_idx
  ON platform.dimension_semantic_documents(
    tenant_id,embedding_status,next_attempt_at,lease_expires_at,created_at
  );
CREATE INDEX dimension_semantic_documents_scope_idx
  ON platform.dimension_semantic_documents(
    tenant_id,dataset_version_id,field_id
  );
CREATE INDEX dimension_semantic_documents_name_trgm_idx
  ON platform.dimension_semantic_documents
  USING gin(dimension_name gin_trgm_ops);
CREATE INDEX dimension_semantic_documents_embedding_hnsw_idx
  ON platform.dimension_semantic_documents
  USING hnsw(embedding halfvec_cosine_ops)
  WITH (m=16,ef_construction=64)
  WHERE embedding_status='SUCCEEDED';

ALTER TABLE platform.dimension_semantic_documents ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform.dimension_semantic_documents FORCE ROW LEVEL SECURITY;
CREATE POLICY dimension_semantic_documents_tenant_isolation
  ON platform.dimension_semantic_documents
  USING(tenant_id=platform.current_tenant_id())
  WITH CHECK(tenant_id=platform.current_tenant_id());

CREATE OR REPLACE FUNCTION platform.sync_dimension_semantic_document()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,platform
AS $$
DECLARE
  semantic_hash text;
BEGIN
  IF NEW.status<>'PUBLISHED' THEN
    DELETE FROM platform.dimension_semantic_documents
    WHERE tenant_id=NEW.tenant_id AND dimension_id=NEW.id;
    RETURN NEW;
  END IF;

  semantic_hash := encode(public.digest(
    convert_to(NEW.name,'UTF8'),'sha256'
  ),'hex');

  INSERT INTO platform.dimension_semantic_documents(
    tenant_id,dimension_id,dataset_id,dataset_version_id,field_id,
    dimension_code,dimension_name,dimension_description,dimension_type,
    document,input_hash
  ) VALUES(
    NEW.tenant_id,NEW.id,NEW.dataset_id,NEW.dataset_version_id,NEW.field_id,
    NEW.code,NEW.name,NEW.description,NEW.dimension_type,
    NEW.name,semantic_hash
  )
  ON CONFLICT(tenant_id,dimension_id) DO UPDATE SET
    dataset_id=EXCLUDED.dataset_id,
    dataset_version_id=EXCLUDED.dataset_version_id,
    field_id=EXCLUDED.field_id,
    dimension_code=EXCLUDED.dimension_code,
    dimension_name=EXCLUDED.dimension_name,
    dimension_description=EXCLUDED.dimension_description,
    dimension_type=EXCLUDED.dimension_type,
    document=EXCLUDED.document,
    input_hash=EXCLUDED.input_hash,
    embedding=NULL,embedding_model='',embedding_input_hash='',
    embedding_status='PENDING',embedding_attempt=0,embedding_error_code='',
    next_attempt_at=now(),lease_owner='',lease_expires_at=NULL,
    embedded_at=NULL,updated_at=now()
  WHERE platform.dimension_semantic_documents.input_hash
      IS DISTINCT FROM EXCLUDED.input_hash
     OR platform.dimension_semantic_documents.field_id
      IS DISTINCT FROM EXCLUDED.field_id
     OR platform.dimension_semantic_documents.dimension_description
      IS DISTINCT FROM EXCLUDED.dimension_description
     OR platform.dimension_semantic_documents.dimension_type
      IS DISTINCT FROM EXCLUDED.dimension_type;
  RETURN NEW;
END
$$;

REVOKE ALL ON FUNCTION platform.sync_dimension_semantic_document()
  FROM PUBLIC;

CREATE TRIGGER semantic_dimensions_sync_definition_vector
AFTER INSERT OR UPDATE OF
  name,description,field_id,status,dataset_id,dataset_version_id,dimension_type
ON platform.semantic_dimensions
FOR EACH ROW EXECUTE FUNCTION platform.sync_dimension_semantic_document();

INSERT INTO platform.dimension_semantic_documents(
  tenant_id,dimension_id,dataset_id,dataset_version_id,field_id,
  dimension_code,dimension_name,dimension_description,dimension_type,
  document,input_hash
)
SELECT dimension.tenant_id,dimension.id,dimension.dataset_id,
  dimension.dataset_version_id,dimension.field_id,dimension.code,
  dimension.name,dimension.description,dimension.dimension_type,
  dimension.name,
  encode(public.digest(convert_to(dimension.name,'UTF8'),'sha256'),'hex')
FROM platform.semantic_dimensions AS dimension
WHERE dimension.status='PUBLISHED'
ON CONFLICT(tenant_id,dimension_id) DO NOTHING;

COMMENT ON TABLE platform.dimension_semantic_documents IS
  '维度名称向量索引：向量输入仅为维度名称，结果固定映射到维度字段、名称、描述和类型';

COMMIT;
