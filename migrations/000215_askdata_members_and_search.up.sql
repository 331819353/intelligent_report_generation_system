CREATE EXTENSION IF NOT EXISTS vector;

-- Dimension values are versioned independently from their display aliases so
-- exact matching can remain available even when vectorization is forbidden.
CREATE TABLE askdata.dimension_members(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id) ON DELETE RESTRICT,
  domain_id uuid NOT NULL,
  member_id uuid NOT NULL,
  version_no integer NOT NULL CHECK(version_no>0),
  dimension_version_id uuid NOT NULL,
  member_key text NOT NULL CHECK(
    length(member_key) BETWEEN 1 AND 512
    AND member_key=btrim(member_key)
    AND member_key !~ '[[:cntrl:]]'
  ),
  member_key_hash text NOT NULL CHECK(member_key_hash ~ '^[0-9a-f]{64}$'),
  canonical_label text NOT NULL CHECK(
    length(canonical_label) BETWEEN 1 AND 512
    AND canonical_label=btrim(canonical_label)
    AND canonical_label !~ '[[:cntrl:]]'
  ),
  parent_member_version_id uuid,
  sensitivity text NOT NULL DEFAULT 'INTERNAL' CHECK(sensitivity IN ('PUBLIC','INTERNAL','CONFIDENTIAL','RESTRICTED')),
  valid_from timestamptz NOT NULL,
  valid_to timestamptz,
  status text NOT NULL DEFAULT 'DRAFT' CHECK(status IN ('DRAFT','CERTIFIED','DEPRECATED')),
  content_hash text NOT NULL CHECK(content_hash ~ '^[0-9a-f]{64}$'),
  created_by uuid NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT askdata_dimension_members_validity_check CHECK(valid_to IS NULL OR valid_to>valid_from),
  CONSTRAINT askdata_dimension_members_identity_key UNIQUE(tenant_id,member_id,version_no),
  CONSTRAINT askdata_dimension_members_key_version_key UNIQUE(tenant_id,dimension_version_id,member_key_hash,version_no),
  CONSTRAINT askdata_dimension_members_identity_tenant_key UNIQUE(id,tenant_id),
  CONSTRAINT askdata_dimension_members_identity_domain_tenant_key UNIQUE(id,domain_id,tenant_id),
  CONSTRAINT askdata_dimension_members_domain_fk
    FOREIGN KEY(domain_id,tenant_id)
    REFERENCES askdata.domains(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT askdata_dimension_members_dimension_fk
    FOREIGN KEY(dimension_version_id,domain_id,tenant_id)
    REFERENCES askdata.dimensions(id,domain_id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT askdata_dimension_members_parent_fk
    FOREIGN KEY(parent_member_version_id,domain_id,tenant_id)
    REFERENCES askdata.dimension_members(id,domain_id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT askdata_dimension_members_created_by_fk
    FOREIGN KEY(created_by,tenant_id)
    REFERENCES platform.users(id,tenant_id) ON DELETE RESTRICT
);

CREATE TABLE askdata.dimension_member_aliases(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id) ON DELETE RESTRICT,
  domain_id uuid NOT NULL,
  dimension_version_id uuid NOT NULL,
  member_version_id uuid NOT NULL,
  alias text NOT NULL CHECK(
    length(alias) BETWEEN 1 AND 512
    AND alias=btrim(alias)
    AND alias !~ '[[:cntrl:]]'
  ),
  normalized_alias text NOT NULL CHECK(
    length(normalized_alias) BETWEEN 1 AND 512
    AND normalized_alias=btrim(normalized_alias)
    AND normalized_alias !~ '[[:cntrl:]]'
  ),
  source text NOT NULL CHECK(source IN ('CANONICAL','MANUAL','IMPORT','CERTIFIED_FEEDBACK')),
  priority integer NOT NULL DEFAULT 100 CHECK(priority BETWEEN 0 AND 1000),
  status text NOT NULL DEFAULT 'DRAFT' CHECK(status IN ('DRAFT','CERTIFIED','DEPRECATED')),
  content_hash text NOT NULL CHECK(content_hash ~ '^[0-9a-f]{64}$'),
  created_by uuid NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT askdata_dimension_member_aliases_identity_tenant_key UNIQUE(id,tenant_id),
  CONSTRAINT askdata_dimension_member_aliases_identity_domain_tenant_key UNIQUE(id,domain_id,tenant_id),
  CONSTRAINT askdata_dimension_member_aliases_match_key UNIQUE(tenant_id,dimension_version_id,normalized_alias,member_version_id),
  CONSTRAINT askdata_dimension_member_aliases_domain_fk
    FOREIGN KEY(domain_id,tenant_id)
    REFERENCES askdata.domains(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT askdata_dimension_member_aliases_dimension_fk
    FOREIGN KEY(dimension_version_id,domain_id,tenant_id)
    REFERENCES askdata.dimensions(id,domain_id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT askdata_dimension_member_aliases_member_fk
    FOREIGN KEY(member_version_id,domain_id,tenant_id)
    REFERENCES askdata.dimension_members(id,domain_id,tenant_id) ON DELETE CASCADE,
  CONSTRAINT askdata_dimension_member_aliases_created_by_fk
    FOREIGN KEY(created_by,tenant_id)
    REFERENCES platform.users(id,tenant_id) ON DELETE RESTRICT
);

-- High-frequency mapping words for metrics, dimensions, models and terms.
-- Feedback may create DRAFT candidates, but only CERTIFIED rows are eligible
-- for production exact matching.
CREATE TABLE askdata.semantic_aliases(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id) ON DELETE RESTRICT,
  domain_id uuid NOT NULL,
  object_type text NOT NULL CHECK(object_type IN ('ENTITY','SEMANTIC_MODEL','MEASURE','METRIC','DIMENSION','BUSINESS_TERM')),
  object_version_id uuid NOT NULL,
  alias text NOT NULL CHECK(length(btrim(alias)) BETWEEN 1 AND 512 AND alias !~ '[[:cntrl:]]'),
  normalized_alias text NOT NULL CHECK(length(btrim(normalized_alias)) BETWEEN 1 AND 512 AND normalized_alias !~ '[[:cntrl:]]'),
  source text NOT NULL CHECK(source IN ('MANUAL','IMPORT','CERTIFIED_EXAMPLE','FEEDBACK_CANDIDATE')),
  priority integer NOT NULL DEFAULT 100 CHECK(priority BETWEEN 0 AND 1000),
  status text NOT NULL DEFAULT 'DRAFT' CHECK(status IN ('DRAFT','CERTIFIED','REJECTED','DEPRECATED')),
  content_hash text NOT NULL CHECK(content_hash ~ '^[0-9a-f]{64}$'),
  created_by uuid NOT NULL,
  reviewed_by uuid,
  created_at timestamptz NOT NULL DEFAULT now(),
  reviewed_at timestamptz,
  updated_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT askdata_semantic_aliases_identity_tenant_key UNIQUE(id,tenant_id),
  CONSTRAINT askdata_semantic_aliases_match_key UNIQUE(tenant_id,domain_id,object_type,normalized_alias,object_version_id),
  CONSTRAINT askdata_semantic_aliases_review_shape_check CHECK(
    (status='CERTIFIED' AND reviewed_by IS NOT NULL AND reviewed_at IS NOT NULL)
    OR status<>'CERTIFIED'
  ),
  CONSTRAINT askdata_semantic_aliases_domain_fk
    FOREIGN KEY(domain_id,tenant_id)
    REFERENCES askdata.domains(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT askdata_semantic_aliases_created_by_fk
    FOREIGN KEY(created_by,tenant_id)
    REFERENCES platform.users(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT askdata_semantic_aliases_reviewed_by_fk
    FOREIGN KEY(reviewed_by,tenant_id)
    REFERENCES platform.users(id,tenant_id) ON DELETE RESTRICT
);

CREATE TABLE askdata.search_documents(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id) ON DELETE RESTRICT,
  domain_id uuid NOT NULL,
  object_type text NOT NULL CHECK(object_type IN ('ENTITY','SEMANTIC_MODEL','MEASURE','METRIC','DIMENSION','MEMBER','BUSINESS_TERM','RELATIONSHIP','CERTIFIED_EXAMPLE')),
  object_version_id uuid NOT NULL,
  view_type text NOT NULL CHECK(view_type IN ('NAME_ALIAS','DEFINITION_QUESTION','DIMENSION_VALUE','EXAMPLE_INTENT')),
  sensitivity text NOT NULL DEFAULT 'INTERNAL' CHECK(sensitivity IN ('PUBLIC','INTERNAL','CONFIDENTIAL')),
  index_policy text NOT NULL CHECK(index_policy IN ('LEXICAL','VECTOR','HYBRID')),
  document text NOT NULL CHECK(
    length(document) BETWEEN 1 AND 32768
    AND document !~ '[[:cntrl:]]'
  ),
  document_tsv tsvector GENERATED ALWAYS AS (to_tsvector('simple',document)) STORED,
  metadata jsonb NOT NULL DEFAULT '{}'::jsonb CHECK(
    jsonb_typeof(metadata)='object'
    AND pg_column_size(metadata)<=65536
    AND askdata.json_is_safe(metadata)
  ),
  input_hash text NOT NULL CHECK(input_hash ~ '^[0-9a-f]{64}$'),
  embedding halfvec(2560),
  embedding_model text NOT NULL DEFAULT '' CHECK(length(embedding_model)<=128),
  embedding_version text NOT NULL DEFAULT '' CHECK(length(embedding_version)<=128),
  embedding_status text NOT NULL DEFAULT 'PENDING' CHECK(embedding_status IN ('PENDING','RUNNING','SUCCEEDED','FAILED','SKIPPED')),
  embedding_error_code text NOT NULL DEFAULT '' CHECK(length(embedding_error_code)<=128),
  embedded_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT askdata_search_documents_identity_tenant_key UNIQUE(id,tenant_id),
  CONSTRAINT askdata_search_documents_identity_domain_tenant_key UNIQUE(id,domain_id,tenant_id),
  CONSTRAINT askdata_search_documents_object_view_key UNIQUE(tenant_id,object_type,object_version_id,view_type),
  CONSTRAINT askdata_search_documents_embedding_shape_check CHECK(
    (embedding_status='SUCCEEDED' AND embedding IS NOT NULL
      AND embedding_model<>'' AND embedding_version<>''
      AND embedded_at IS NOT NULL AND embedding_error_code='')
    OR
    (embedding_status IN ('PENDING','RUNNING','FAILED','SKIPPED')
      AND embedding IS NULL AND embedded_at IS NULL)
  ),
  CONSTRAINT askdata_search_documents_domain_fk
    FOREIGN KEY(domain_id,tenant_id)
    REFERENCES askdata.domains(id,tenant_id) ON DELETE RESTRICT
);

CREATE TABLE askdata.embedding_outbox(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id) ON DELETE RESTRICT,
  domain_id uuid NOT NULL,
  search_document_id uuid NOT NULL,
  input_hash text NOT NULL CHECK(input_hash ~ '^[0-9a-f]{64}$'),
  status text NOT NULL DEFAULT 'PENDING' CHECK(status IN ('PENDING','RUNNING','SUCCEEDED','FAILED','SKIPPED')),
  attempt integer NOT NULL DEFAULT 0 CHECK(attempt BETWEEN 0 AND 20),
  max_attempts integer NOT NULL DEFAULT 8 CHECK(max_attempts BETWEEN 1 AND 20),
  next_attempt_at timestamptz NOT NULL DEFAULT now(),
  lease_owner text NOT NULL DEFAULT '' CHECK(length(lease_owner)<=128),
  lease_token uuid,
  lease_expires_at timestamptz,
  error_code text NOT NULL DEFAULT '' CHECK(length(error_code)<=128),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  completed_at timestamptz,
  CONSTRAINT askdata_embedding_outbox_identity_tenant_key UNIQUE(id,tenant_id),
  CONSTRAINT askdata_embedding_outbox_document_key UNIQUE(tenant_id,search_document_id,input_hash),
  CONSTRAINT askdata_embedding_outbox_lease_shape_check CHECK(
    (status='RUNNING' AND lease_owner<>'' AND lease_token IS NOT NULL
      AND lease_expires_at IS NOT NULL AND completed_at IS NULL)
    OR
    (status<>'RUNNING' AND lease_owner='' AND lease_token IS NULL
      AND lease_expires_at IS NULL)
  ),
  CONSTRAINT askdata_embedding_outbox_domain_fk
    FOREIGN KEY(domain_id,tenant_id)
    REFERENCES askdata.domains(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT askdata_embedding_outbox_document_fk
    FOREIGN KEY(search_document_id,domain_id,tenant_id)
    REFERENCES askdata.search_documents(id,domain_id,tenant_id) ON DELETE CASCADE
);

CREATE OR REPLACE FUNCTION askdata.validate_search_document_subject()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE subject_valid boolean;
BEGIN
  subject_valid := false;
  CASE NEW.object_type
    WHEN 'ENTITY' THEN
      SELECT EXISTS(SELECT 1 FROM askdata.entities WHERE tenant_id=NEW.tenant_id AND domain_id=NEW.domain_id AND id=NEW.object_version_id AND status='CERTIFIED') INTO subject_valid;
    WHEN 'SEMANTIC_MODEL' THEN
      SELECT EXISTS(SELECT 1 FROM askdata.semantic_models WHERE tenant_id=NEW.tenant_id AND domain_id=NEW.domain_id AND id=NEW.object_version_id AND status='CERTIFIED') INTO subject_valid;
    WHEN 'MEASURE' THEN
      SELECT EXISTS(SELECT 1 FROM askdata.measures WHERE tenant_id=NEW.tenant_id AND domain_id=NEW.domain_id AND id=NEW.object_version_id AND status='CERTIFIED') INTO subject_valid;
    WHEN 'METRIC' THEN
      SELECT EXISTS(SELECT 1 FROM askdata.metric_versions WHERE tenant_id=NEW.tenant_id AND domain_id=NEW.domain_id AND id=NEW.object_version_id AND status='CERTIFIED') INTO subject_valid;
    WHEN 'DIMENSION' THEN
      SELECT EXISTS(
        SELECT 1 FROM askdata.dimensions
        WHERE tenant_id=NEW.tenant_id AND domain_id=NEW.domain_id
          AND id=NEW.object_version_id AND status='CERTIFIED'
          AND sensitivity<>'RESTRICTED'
          AND sensitivity=NEW.sensitivity
      ) INTO subject_valid;
    WHEN 'MEMBER' THEN
      SELECT EXISTS(
        SELECT 1 FROM askdata.dimension_members AS member
        JOIN askdata.dimensions AS dimension
          ON dimension.id=member.dimension_version_id AND dimension.tenant_id=member.tenant_id
        WHERE member.tenant_id=NEW.tenant_id AND member.domain_id=NEW.domain_id
          AND member.id=NEW.object_version_id AND member.status='CERTIFIED'
          AND member.sensitivity IN ('PUBLIC','INTERNAL')
          AND dimension.sensitivity IN ('PUBLIC','INTERNAL')
          AND dimension.member_index_policy='FULL'
          AND NOT dimension.high_cardinality
          AND NEW.sensitivity=CASE
            WHEN member.sensitivity='INTERNAL' OR dimension.sensitivity='INTERNAL'
              THEN 'INTERNAL'
            ELSE 'PUBLIC'
          END
      ) INTO subject_valid;
    WHEN 'BUSINESS_TERM' THEN
      SELECT EXISTS(SELECT 1 FROM askdata.business_terms WHERE tenant_id=NEW.tenant_id AND domain_id=NEW.domain_id AND id=NEW.object_version_id AND status='CERTIFIED') INTO subject_valid;
    WHEN 'RELATIONSHIP' THEN
      SELECT EXISTS(SELECT 1 FROM askdata.relationships WHERE tenant_id=NEW.tenant_id AND domain_id=NEW.domain_id AND id=NEW.object_version_id AND status='CERTIFIED') INTO subject_valid;
    WHEN 'CERTIFIED_EXAMPLE' THEN
      -- Certified examples are introduced by REG-006. Until then no row can
      -- satisfy this subject and inserts fail closed.
      subject_valid := false;
  END CASE;
  IF NOT subject_valid THEN
    RAISE EXCEPTION 'search document subject is not a certified indexable object'
      USING ERRCODE='23514';
  END IF;
  RETURN NEW;
END
$$;

CREATE OR REPLACE FUNCTION askdata.validate_member_dependency()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE dependency_valid boolean := false;
BEGIN
  IF TG_TABLE_NAME='dimension_members' THEN
    SELECT EXISTS(
      SELECT 1 FROM askdata.dimensions
      WHERE id=NEW.dimension_version_id AND domain_id=NEW.domain_id
        AND tenant_id=NEW.tenant_id
        AND member_index_policy IN ('FULL','EXACT_ONLY')
        AND status=CASE WHEN NEW.status='CERTIFIED' THEN 'CERTIFIED' ELSE status END
        AND status<>'DEPRECATED'
    ) AND (
      NEW.parent_member_version_id IS NULL OR EXISTS(
        SELECT 1 FROM askdata.dimension_members AS parent
        WHERE parent.id=NEW.parent_member_version_id
          AND parent.dimension_version_id=NEW.dimension_version_id
          AND parent.domain_id=NEW.domain_id AND parent.tenant_id=NEW.tenant_id
          AND parent.status=CASE WHEN NEW.status='CERTIFIED' THEN 'CERTIFIED' ELSE parent.status END
          AND parent.status<>'DEPRECATED'
      )
    ) INTO dependency_valid;
  ELSE
    SELECT EXISTS(
      SELECT 1
      FROM askdata.dimension_members AS member
      JOIN askdata.dimensions AS dimension
        ON dimension.id=member.dimension_version_id
       AND dimension.domain_id=member.domain_id
       AND dimension.tenant_id=member.tenant_id
      WHERE member.id=NEW.member_version_id
        AND member.dimension_version_id=NEW.dimension_version_id
        AND member.domain_id=NEW.domain_id AND member.tenant_id=NEW.tenant_id
        AND member.status=CASE WHEN NEW.status='CERTIFIED' THEN 'CERTIFIED' ELSE member.status END
        AND dimension.status=CASE WHEN NEW.status='CERTIFIED' THEN 'CERTIFIED' ELSE dimension.status END
        AND member.status<>'DEPRECATED' AND dimension.status<>'DEPRECATED'
    ) INTO dependency_valid;
  END IF;
  IF NOT COALESCE(dependency_valid,false) THEN
    RAISE EXCEPTION 'dimension member or alias dependency is invalid or uncertified'
      USING ERRCODE='23514';
  END IF;
  RETURN NEW;
END
$$;

CREATE OR REPLACE FUNCTION askdata.validate_semantic_alias_subject()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE subject_valid boolean := false;
BEGIN
  CASE NEW.object_type
    WHEN 'ENTITY' THEN
      SELECT EXISTS(SELECT 1 FROM askdata.entities WHERE id=NEW.object_version_id AND domain_id=NEW.domain_id AND tenant_id=NEW.tenant_id AND status=CASE WHEN NEW.status='CERTIFIED' THEN 'CERTIFIED' ELSE status END AND status<>'DEPRECATED') INTO subject_valid;
    WHEN 'SEMANTIC_MODEL' THEN
      SELECT EXISTS(SELECT 1 FROM askdata.semantic_models WHERE id=NEW.object_version_id AND domain_id=NEW.domain_id AND tenant_id=NEW.tenant_id AND status=CASE WHEN NEW.status='CERTIFIED' THEN 'CERTIFIED' ELSE status END AND status<>'DEPRECATED') INTO subject_valid;
    WHEN 'MEASURE' THEN
      SELECT EXISTS(SELECT 1 FROM askdata.measures WHERE id=NEW.object_version_id AND domain_id=NEW.domain_id AND tenant_id=NEW.tenant_id AND status=CASE WHEN NEW.status='CERTIFIED' THEN 'CERTIFIED' ELSE status END AND status<>'DEPRECATED') INTO subject_valid;
    WHEN 'METRIC' THEN
      SELECT EXISTS(SELECT 1 FROM askdata.metric_versions WHERE id=NEW.object_version_id AND domain_id=NEW.domain_id AND tenant_id=NEW.tenant_id AND status=CASE WHEN NEW.status='CERTIFIED' THEN 'CERTIFIED' ELSE status END AND status<>'DEPRECATED') INTO subject_valid;
    WHEN 'DIMENSION' THEN
      SELECT EXISTS(SELECT 1 FROM askdata.dimensions WHERE id=NEW.object_version_id AND domain_id=NEW.domain_id AND tenant_id=NEW.tenant_id AND status=CASE WHEN NEW.status='CERTIFIED' THEN 'CERTIFIED' ELSE status END AND status<>'DEPRECATED') INTO subject_valid;
    WHEN 'BUSINESS_TERM' THEN
      SELECT EXISTS(SELECT 1 FROM askdata.business_terms WHERE id=NEW.object_version_id AND domain_id=NEW.domain_id AND tenant_id=NEW.tenant_id AND status=CASE WHEN NEW.status='CERTIFIED' THEN 'CERTIFIED' ELSE status END AND status<>'DEPRECATED') INTO subject_valid;
  END CASE;
  IF NOT COALESCE(subject_valid,false) THEN
    RAISE EXCEPTION 'semantic alias subject is missing, deprecated, cross-domain, or uncertified'
      USING ERRCODE='23514';
  END IF;
  RETURN NEW;
END
$$;

CREATE OR REPLACE FUNCTION askdata.enqueue_embedding_document()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,askdata
AS $$
BEGIN
  UPDATE askdata.embedding_outbox SET
    status='SKIPPED',lease_owner='',lease_token=NULL,lease_expires_at=NULL,
    error_code='',completed_at=now(),updated_at=now()
  WHERE tenant_id=NEW.tenant_id AND search_document_id=NEW.id
    AND (input_hash<>NEW.input_hash OR NEW.index_policy='LEXICAL')
    AND status IN ('PENDING','RUNNING','FAILED');
  IF NEW.index_policy IN ('VECTOR','HYBRID') THEN
    INSERT INTO askdata.embedding_outbox(
      tenant_id,domain_id,search_document_id,input_hash
    ) VALUES(NEW.tenant_id,NEW.domain_id,NEW.id,NEW.input_hash)
    ON CONFLICT(tenant_id,search_document_id,input_hash) DO UPDATE SET
      status='PENDING',attempt=0,next_attempt_at=now(),
      lease_owner='',lease_token=NULL,lease_expires_at=NULL,
      error_code='',completed_at=NULL,updated_at=now();
  END IF;
  RETURN NEW;
END
$$;

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

REVOKE ALL ON FUNCTION
  askdata.validate_search_document_subject(),
  askdata.validate_member_dependency(),
  askdata.validate_semantic_alias_subject(),
  askdata.enqueue_embedding_document(),
  askdata.prepare_search_document_embedding()
FROM PUBLIC;

CREATE TRIGGER askdata_search_documents_validate_subject
BEFORE INSERT OR UPDATE OF object_type,object_version_id,domain_id,index_policy,sensitivity
ON askdata.search_documents
FOR EACH ROW EXECUTE FUNCTION askdata.validate_search_document_subject();

CREATE TRIGGER askdata_search_documents_prepare_embedding
BEFORE INSERT OR UPDATE OF document,input_hash,index_policy,sensitivity
ON askdata.search_documents
FOR EACH ROW EXECUTE FUNCTION askdata.prepare_search_document_embedding();

CREATE TRIGGER askdata_search_documents_enqueue_embedding
AFTER INSERT OR UPDATE OF document,input_hash,index_policy,sensitivity
ON askdata.search_documents
FOR EACH ROW EXECUTE FUNCTION askdata.enqueue_embedding_document();

CREATE TRIGGER askdata_dimension_members_validate_dependency
BEFORE INSERT OR UPDATE ON askdata.dimension_members
FOR EACH ROW EXECUTE FUNCTION askdata.validate_member_dependency();
CREATE TRIGGER askdata_dimension_member_aliases_validate_dependency
BEFORE INSERT OR UPDATE ON askdata.dimension_member_aliases
FOR EACH ROW EXECUTE FUNCTION askdata.validate_member_dependency();
CREATE TRIGGER askdata_semantic_aliases_validate_subject
BEFORE INSERT OR UPDATE ON askdata.semantic_aliases
FOR EACH ROW EXECUTE FUNCTION askdata.validate_semantic_alias_subject();

CREATE TRIGGER askdata_dimension_members_set_updated_at BEFORE UPDATE ON askdata.dimension_members
FOR EACH ROW EXECUTE FUNCTION askdata.set_updated_at();
CREATE TRIGGER askdata_dimension_member_aliases_set_updated_at BEFORE UPDATE ON askdata.dimension_member_aliases
FOR EACH ROW EXECUTE FUNCTION askdata.set_updated_at();
CREATE TRIGGER askdata_semantic_aliases_set_updated_at BEFORE UPDATE ON askdata.semantic_aliases
FOR EACH ROW EXECUTE FUNCTION askdata.set_updated_at();
CREATE TRIGGER askdata_search_documents_set_updated_at BEFORE UPDATE ON askdata.search_documents
FOR EACH ROW EXECUTE FUNCTION askdata.set_updated_at();
CREATE TRIGGER askdata_embedding_outbox_set_updated_at BEFORE UPDATE ON askdata.embedding_outbox
FOR EACH ROW EXECUTE FUNCTION askdata.set_updated_at();

CREATE TRIGGER askdata_dimension_members_protect_certified BEFORE UPDATE OR DELETE ON askdata.dimension_members
FOR EACH ROW EXECUTE FUNCTION askdata.protect_certified_version();
CREATE TRIGGER askdata_dimension_member_aliases_protect_certified BEFORE UPDATE OR DELETE ON askdata.dimension_member_aliases
FOR EACH ROW EXECUTE FUNCTION askdata.protect_certified_version();

CREATE INDEX askdata_dimension_members_lookup_idx
  ON askdata.dimension_members(tenant_id,domain_id,dimension_version_id,member_key_hash,status,valid_from DESC);
CREATE INDEX askdata_dimension_member_aliases_exact_idx
  ON askdata.dimension_member_aliases(tenant_id,domain_id,dimension_version_id,normalized_alias,priority,status);
CREATE INDEX askdata_semantic_aliases_exact_idx
  ON askdata.semantic_aliases(tenant_id,domain_id,object_type,normalized_alias,priority,status);
CREATE INDEX askdata_search_documents_fts_idx
  ON askdata.search_documents USING gin(document_tsv);
CREATE INDEX askdata_search_documents_lookup_idx
  ON askdata.search_documents(tenant_id,domain_id,object_type,view_type,embedding_status,id);
CREATE INDEX askdata_search_documents_hnsw_idx
  ON askdata.search_documents USING hnsw(embedding halfvec_cosine_ops)
  WITH (m=16,ef_construction=64)
  WHERE embedding_status='SUCCEEDED';
CREATE INDEX askdata_embedding_outbox_claim_idx
  ON askdata.embedding_outbox(status,next_attempt_at,lease_expires_at,tenant_id,domain_id,created_at,id);

DO $rls$
DECLARE relation_name text;
BEGIN
  FOREACH relation_name IN ARRAY ARRAY[
    'dimension_members','dimension_member_aliases','semantic_aliases',
    'search_documents','embedding_outbox'
  ] LOOP
    EXECUTE format('ALTER TABLE askdata.%I ENABLE ROW LEVEL SECURITY',relation_name);
    EXECUTE format('ALTER TABLE askdata.%I FORCE ROW LEVEL SECURITY',relation_name);
    EXECUTE format(
      'CREATE POLICY %I ON askdata.%I USING(askdata.tenant_matches(tenant_id) AND askdata.domain_can_access(domain_id)) WITH CHECK(askdata.tenant_matches(tenant_id) AND askdata.domain_can_access(domain_id))',
      'askdata_'||relation_name||'_domain_isolation',relation_name
    );
  END LOOP;
END
$rls$;

COMMENT ON TABLE askdata.semantic_aliases IS
  'Governed high-frequency mapping words; feedback candidates never become production aliases without review';
COMMENT ON TABLE askdata.search_documents IS
  'Separated lexical/vector views for certified semantic objects; sensitive, high-cardinality and non-FULL values never enter embedding work';
COMMENT ON TABLE askdata.embedding_outbox IS
  'Durable idempotent embedding work; API cannot directly complete worker state';
