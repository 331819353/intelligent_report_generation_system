BEGIN;

ALTER TABLE askdata.search_query_samples
  DROP CONSTRAINT search_query_samples_doc_type_check,
  ADD CONSTRAINT search_query_samples_doc_type_check CHECK(doc_type IN (
    'METRIC','DIMENSION','MEMBER','BUSINESS_TERM','CERTIFIED_EXAMPLE','REPORT_ASSET','SEMANTIC_MODEL'
  ));

ALTER TABLE askdata.search_recall_audits
  DROP CONSTRAINT search_recall_audits_doc_type_check,
  ADD CONSTRAINT search_recall_audits_doc_type_check CHECK(doc_type IN (
    'METRIC','DIMENSION','MEMBER','BUSINESS_TERM','CERTIFIED_EXAMPLE','REPORT_ASSET','SEMANTIC_MODEL'
  ));

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
      'METRIC','DIMENSION','MEMBER','BUSINESS_TERM','CERTIFIED_EXAMPLE','REPORT_ASSET','SEMANTIC_MODEL'
    )
    OR length(btrim(selected_embedding_model)) NOT BETWEEN 1 AND 128
    OR selected_embedding_model<>btrim(selected_embedding_model)
    OR selected_embedding_dim<>2560
    OR selected_sample_hash !~ '^[0-9a-f]{64}$'
    OR octet_length(selected_embedding)>131072
  THEN
    RAISE EXCEPTION 'invalid search recall sample' USING ERRCODE='22023';
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

COMMIT;
