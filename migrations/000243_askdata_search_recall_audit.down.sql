DROP FUNCTION IF EXISTS askdata.record_search_query_sample(
  uuid,uuid,text,text,text,text,integer,text
);
DROP TABLE IF EXISTS askdata.search_recall_audits;
DROP TABLE IF EXISTS askdata.search_query_samples;

ALTER TABLE askdata.search_documents
  DROP CONSTRAINT IF EXISTS askdata_search_documents_embedding_shape_check;

ALTER TABLE askdata.search_documents
  DROP COLUMN IF EXISTS embedding_dim;

ALTER TABLE askdata.search_documents
  ADD CONSTRAINT askdata_search_documents_embedding_shape_check CHECK(
    (embedding_status='SUCCEEDED' AND embedding IS NOT NULL
      AND embedding_model<>'' AND embedding_version<>''
      AND embedded_at IS NOT NULL AND embedding_error_code='')
    OR
    (embedding_status IN ('PENDING','RUNNING','FAILED','SKIPPED')
      AND embedding IS NULL AND embedded_at IS NULL)
  );

CREATE OR REPLACE FUNCTION askdata.prepare_search_document_embedding()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
  NEW.embedding=NULL;
  NEW.embedding_model='';
  NEW.embedding_version='';
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
