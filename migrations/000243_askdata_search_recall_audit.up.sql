-- SEARCH-006: keep ANN as a performance optimization by comparing it with
-- exact KNN on recent, label-free query embeddings.

ALTER TABLE askdata.search_documents
  ADD COLUMN embedding_dim integer NOT NULL DEFAULT 0
  CHECK(embedding_dim IN (0,2560));

UPDATE askdata.search_documents
SET embedding_dim=2560
WHERE embedding_status='SUCCEEDED';

ALTER TABLE askdata.search_documents
  DROP CONSTRAINT askdata_search_documents_embedding_shape_check;

ALTER TABLE askdata.search_documents
  ADD CONSTRAINT askdata_search_documents_embedding_shape_check CHECK(
    (embedding_status='SUCCEEDED' AND embedding IS NOT NULL
      AND embedding_model<>'' AND embedding_version<>''
      AND embedding_dim=2560
      AND embedded_at IS NOT NULL AND embedding_error_code='')
    OR
    (embedding_status IN ('PENDING','RUNNING','FAILED','SKIPPED')
      AND embedding IS NULL AND embedding_dim=0 AND embedded_at IS NULL)
  );

CREATE OR REPLACE FUNCTION askdata.prepare_search_document_embedding()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
  NEW.embedding=NULL;
  NEW.embedding_model='';
  NEW.embedding_version='';
  NEW.embedding_dim=0;
  NEW.embedding_error_code='';
  NEW.embedded_at=NULL;
  IF NEW.index_policy IN ('VECTOR','HYBRID') THEN
    IF NEW.sensitivity NOT IN ('PUBLIC','INTERNAL') THEN
      RAISE EXCEPTION 'sensitive search documents cannot enter the embedding outbox'
        USING ERRCODE='23514';
    END IF;
    NEW.embedding_status='PENDING';
  ELSE
    NEW.embedding_status='SKIPPED';
  END IF;
  RETURN NEW;
END
$$;

REVOKE ALL ON FUNCTION askdata.prepare_search_document_embedding() FROM PUBLIC;

CREATE TABLE askdata.search_query_samples(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id) ON DELETE RESTRICT,
  domain_id uuid NOT NULL,
  release_id uuid NOT NULL,
  release_hash text NOT NULL CHECK(release_hash ~ '^[0-9a-f]{64}$'),
  doc_type text NOT NULL CHECK(doc_type IN (
    'METRIC','DIMENSION','MEMBER','BUSINESS_TERM','CERTIFIED_EXAMPLE'
  )),
  embedding halfvec(2560) NOT NULL,
  embedding_model text NOT NULL CHECK(
    length(embedding_model) BETWEEN 1 AND 128
    AND embedding_model=btrim(embedding_model)
  ),
  embedding_dim integer NOT NULL CHECK(embedding_dim=2560),
  sample_hash text NOT NULL CHECK(sample_hash ~ '^[0-9a-f]{64}$'),
  created_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT askdata_search_query_samples_identity_tenant_key
    UNIQUE(id,tenant_id),
  CONSTRAINT askdata_search_query_samples_dedup_key
    UNIQUE(tenant_id,domain_id,release_id,doc_type,embedding_model,sample_hash),
  CONSTRAINT askdata_search_query_samples_domain_fk
    FOREIGN KEY(domain_id,tenant_id)
    REFERENCES askdata.domains(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT askdata_search_query_samples_release_fk
    FOREIGN KEY(release_id,domain_id,tenant_id)
    REFERENCES askdata.releases(id,domain_id,tenant_id) ON DELETE RESTRICT
);

CREATE TABLE askdata.search_recall_audits(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id) ON DELETE RESTRICT,
  domain_id uuid NOT NULL,
  run_at timestamptz NOT NULL,
  doc_type text NOT NULL CHECK(doc_type IN (
    'METRIC','DIMENSION','MEMBER','BUSINESS_TERM','CERTIFIED_EXAMPLE'
  )),
  k integer NOT NULL CHECK(k IN (10,20,30)),
  sample_size integer NOT NULL CHECK(sample_size BETWEEN 1 AND 10000),
  recall double precision NOT NULL CHECK(recall BETWEEN 0 AND 1),
  p95_latency_ann bigint NOT NULL CHECK(p95_latency_ann>=0),
  p95_latency_exact bigint NOT NULL CHECK(p95_latency_exact>=0),
  embedding_model text NOT NULL CHECK(
    length(embedding_model) BETWEEN 1 AND 128
    AND embedding_model=btrim(embedding_model)
  ),
  embedding_dim integer NOT NULL CHECK(embedding_dim=2560),
  ef_search integer NOT NULL CHECK(ef_search BETWEEN 1 AND 10000),
  threshold double precision NOT NULL CHECK(threshold>0 AND threshold<=1),
  below_threshold boolean NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT askdata_search_recall_audits_identity_tenant_key
    UNIQUE(id,tenant_id),
  CONSTRAINT askdata_search_recall_audits_run_key
    UNIQUE(tenant_id,domain_id,run_at,doc_type,k),
  CONSTRAINT askdata_search_recall_audits_domain_fk
    FOREIGN KEY(domain_id,tenant_id)
    REFERENCES askdata.domains(id,tenant_id) ON DELETE RESTRICT
);

CREATE INDEX askdata_search_query_samples_recent_idx
  ON askdata.search_query_samples(
    tenant_id,domain_id,doc_type,embedding_model,created_at DESC,id
  );
CREATE INDEX askdata_search_recall_audits_recent_idx
  ON askdata.search_recall_audits(
    tenant_id,domain_id,doc_type,run_at DESC,k,id
  );

CREATE TRIGGER askdata_search_query_samples_immutable_update
BEFORE UPDATE ON askdata.search_query_samples
FOR EACH ROW EXECUTE FUNCTION askdata.reject_immutable_mutation();

CREATE TRIGGER askdata_search_recall_audits_immutable
BEFORE UPDATE OR DELETE ON askdata.search_recall_audits
FOR EACH ROW EXECUTE FUNCTION askdata.reject_immutable_mutation();

ALTER TABLE askdata.search_query_samples ENABLE ROW LEVEL SECURITY;
ALTER TABLE askdata.search_query_samples FORCE ROW LEVEL SECURITY;
CREATE POLICY askdata_search_query_samples_domain_isolation
  ON askdata.search_query_samples
  USING(
    askdata.tenant_matches(tenant_id)
    AND askdata.domain_can_access(domain_id)
  )
  WITH CHECK(
    askdata.tenant_matches(tenant_id)
    AND askdata.domain_can_access(domain_id)
  );

ALTER TABLE askdata.search_recall_audits ENABLE ROW LEVEL SECURITY;
ALTER TABLE askdata.search_recall_audits FORCE ROW LEVEL SECURITY;
CREATE POLICY askdata_search_recall_audits_domain_isolation
  ON askdata.search_recall_audits
  USING(
    askdata.tenant_matches(tenant_id)
    AND askdata.domain_can_access(domain_id)
  )
  WITH CHECK(
    askdata.tenant_matches(tenant_id)
    AND askdata.domain_can_access(domain_id)
  );

CREATE OR REPLACE FUNCTION askdata.record_search_query_sample(
  selected_domain_id uuid,
  selected_release_id uuid,
  selected_release_hash text,
  selected_doc_type text,
  selected_embedding text,
  selected_embedding_model text,
  selected_embedding_dim integer,
  selected_sample_hash text
)
RETURNS boolean
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,askdata,public
AS $$
DECLARE inserted_count bigint;
BEGIN
  IF askdata.current_tenant_id() IS NULL
    OR askdata.current_actor_id() IS NULL
    OR selected_domain_id IS NULL
    OR selected_domain_id<>askdata.current_domain_id()
    OR NOT askdata.domain_can_access(selected_domain_id)
    OR selected_release_hash !~ '^[0-9a-f]{64}$'
    OR selected_doc_type NOT IN (
      'METRIC','DIMENSION','MEMBER','BUSINESS_TERM','CERTIFIED_EXAMPLE'
    )
    OR length(btrim(selected_embedding_model)) NOT BETWEEN 1 AND 128
    OR selected_embedding_model<>btrim(selected_embedding_model)
    OR selected_embedding_dim<>2560
    OR selected_sample_hash !~ '^[0-9a-f]{64}$'
    OR octet_length(selected_embedding)>131072
  THEN
    RAISE EXCEPTION 'invalid search recall sample'
      USING ERRCODE='22023';
  END IF;
  IF NOT EXISTS(
    SELECT 1 FROM askdata.releases AS release
    WHERE release.tenant_id=askdata.current_tenant_id()
      AND release.domain_id=selected_domain_id
      AND release.id=selected_release_id
      AND release.content_hash=selected_release_hash
      AND release.status IN ('READY','ACTIVE')
  ) THEN
    RAISE EXCEPTION 'search recall sample release is not runnable'
      USING ERRCODE='23514';
  END IF;
  INSERT INTO askdata.search_query_samples(
    tenant_id,domain_id,release_id,release_hash,doc_type,embedding,
    embedding_model,embedding_dim,sample_hash
  ) VALUES(
    askdata.current_tenant_id(),selected_domain_id,selected_release_id,
    selected_release_hash,selected_doc_type,
    selected_embedding::halfvec(2560),selected_embedding_model,
    selected_embedding_dim,selected_sample_hash
  )
  ON CONFLICT(
    tenant_id,domain_id,release_id,doc_type,embedding_model,sample_hash
  ) DO NOTHING;
  GET DIAGNOSTICS inserted_count=ROW_COUNT;
  RETURN inserted_count=1;
END
$$;

REVOKE ALL ON FUNCTION askdata.record_search_query_sample(
  uuid,uuid,text,text,text,text,integer,text
) FROM PUBLIC;

COMMENT ON TABLE askdata.search_query_samples IS
  'Recent label-free query embeddings used only for ANN versus exact recall audits; no question text is stored';
COMMENT ON TABLE askdata.search_recall_audits IS
  'Append-only ANN recall@K and latency evidence; latency columns are integer microseconds';

REVOKE ALL ON TABLE
  askdata.search_query_samples,
  askdata.search_recall_audits
FROM PUBLIC;
