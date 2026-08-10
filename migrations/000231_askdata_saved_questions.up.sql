-- Saved questions persist only governed Semantic IR and stable references.
-- Result rows, result hashes, narratives and another viewer's execution cache
-- are intentionally absent from this schema.
CREATE TABLE askdata.saved_questions(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id) ON DELETE RESTRICT,
  domain_id uuid NOT NULL,
  owner_user_id uuid NOT NULL,
  visibility text NOT NULL CHECK(visibility IN ('PRIVATE','TEAM','CERTIFIED_CANDIDATE')),
  name text NOT NULL CHECK(length(btrim(name)) BETWEEN 1 AND 200 AND name !~ '[[:cntrl:]]'),
  question_text text NOT NULL CHECK(length(btrim(question_text)) BETWEEN 1 AND 4000 AND question_text !~ '[[:cntrl:]]'),
  semantic_ir_json jsonb NOT NULL CHECK(
    jsonb_typeof(semantic_ir_json)='object' AND pg_column_size(semantic_ir_json)<=262144
    AND askdata.json_is_safe(semantic_ir_json)
  ),
  semantic_ir_hash text NOT NULL CHECK(semantic_ir_hash ~ '^[0-9a-f]{64}$'),
  semantic_release_id uuid NOT NULL,
  semantic_release_content_hash text NOT NULL CHECK(semantic_release_content_hash ~ '^[0-9a-f]{64}$'),
  source_question_run_id uuid,
  status text NOT NULL DEFAULT 'ACTIVE' CHECK(status IN ('ACTIVE','NEEDS_MIGRATION','ARCHIVED')),
  migration_reason text CHECK(
    migration_reason IS NULL OR (length(btrim(migration_reason)) BETWEEN 1 AND 1000 AND migration_reason !~ '[[:cntrl:]]')
  ),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT askdata_saved_questions_identity_tenant_key UNIQUE(id,tenant_id),
  CONSTRAINT askdata_saved_questions_owner_fk FOREIGN KEY(owner_user_id,tenant_id)
    REFERENCES platform.users(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT askdata_saved_questions_domain_fk FOREIGN KEY(domain_id,tenant_id)
    REFERENCES askdata.domains(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT askdata_saved_questions_release_fk FOREIGN KEY(semantic_release_id,domain_id,tenant_id)
    REFERENCES askdata.releases(id,domain_id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT askdata_saved_questions_run_fk FOREIGN KEY(source_question_run_id,owner_user_id,tenant_id)
    REFERENCES askdata.question_runs(id,actor_id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT askdata_saved_questions_migration_shape CHECK(
    (status='NEEDS_MIGRATION' AND migration_reason IS NOT NULL)
    OR (status<>'NEEDS_MIGRATION' AND migration_reason IS NULL)
  )
);

CREATE TABLE askdata.saved_question_dependencies(
  tenant_id uuid NOT NULL,
  saved_question_id uuid NOT NULL,
  dependency_type text NOT NULL CHECK(dependency_type IN (
    'SEMANTIC_RELEASE','METRIC_VERSION','DIMENSION_VERSION','MEMBER_VERSION','DATASET_VERSION'
  )),
  dependency_id text NOT NULL CHECK(length(btrim(dependency_id)) BETWEEN 1 AND 512),
  PRIMARY KEY(saved_question_id,dependency_type,dependency_id),
  FOREIGN KEY(saved_question_id,tenant_id)
    REFERENCES askdata.saved_questions(id,tenant_id) ON DELETE CASCADE
);

CREATE TABLE askdata.saved_question_shares(
  saved_question_id uuid NOT NULL,
  tenant_id uuid NOT NULL,
  principal_type text NOT NULL CHECK(principal_type IN ('USER','ROLE','DOMAIN')),
  principal_id uuid NOT NULL,
  granted_by uuid NOT NULL,
  granted_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY(saved_question_id,principal_type,principal_id),
  FOREIGN KEY(saved_question_id,tenant_id)
    REFERENCES askdata.saved_questions(id,tenant_id) ON DELETE CASCADE,
  FOREIGN KEY(granted_by,tenant_id) REFERENCES platform.users(id,tenant_id) ON DELETE RESTRICT
);

CREATE INDEX askdata_saved_questions_owner_idx
  ON askdata.saved_questions(tenant_id,domain_id,owner_user_id,status,updated_at DESC,id);
CREATE INDEX askdata_saved_question_dependencies_impact_idx
  ON askdata.saved_question_dependencies(tenant_id,dependency_type,dependency_id,saved_question_id);
CREATE INDEX askdata_saved_question_shares_principal_idx
  ON askdata.saved_question_shares(tenant_id,principal_type,principal_id,saved_question_id);

CREATE OR REPLACE FUNCTION askdata.saved_question_can_read(
  selected_tenant_id uuid, selected_domain_id uuid, selected_owner_id uuid,
  selected_question_id uuid, selected_visibility text
)
RETURNS boolean
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path=pg_catalog,askdata,platform
AS $$
  SELECT askdata.tenant_matches(selected_tenant_id)
    AND askdata.domain_can_access(selected_domain_id)
    AND (
      askdata.system_access()
      OR selected_owner_id=askdata.current_actor_id()
      OR (
        selected_visibility<>'PRIVATE'
        AND EXISTS(
          SELECT 1 FROM askdata.saved_question_shares AS share
          WHERE share.tenant_id=selected_tenant_id
            AND share.saved_question_id=selected_question_id
            AND (
              (share.principal_type='USER' AND share.principal_id=askdata.current_actor_id())
              OR (share.principal_type='DOMAIN' AND share.principal_id=selected_domain_id)
              OR (share.principal_type='ROLE' AND EXISTS(
                SELECT 1 FROM platform.user_roles AS assignment
                JOIN platform.roles AS role
                  ON role.id=assignment.role_id AND role.tenant_id=assignment.tenant_id
                WHERE assignment.tenant_id=selected_tenant_id
                  AND assignment.user_id=askdata.current_actor_id()
                  AND assignment.role_id=share.principal_id
                  AND role.status='ACTIVE' AND role.deleted_at IS NULL
              ))
            )
        )
      )
    )
$$;

CREATE OR REPLACE FUNCTION askdata.enforce_saved_question()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,askdata
AS $$
DECLARE selected_release_hash text;
BEGIN
  IF TG_OP='DELETE' THEN
    RAISE EXCEPTION 'saved questions must be archived, not deleted' USING ERRCODE='55000';
  END IF;
  IF TG_OP='INSERT' THEN
    SELECT content_hash INTO selected_release_hash FROM askdata.releases
    WHERE id=NEW.semantic_release_id AND tenant_id=NEW.tenant_id AND domain_id=NEW.domain_id
      AND status IN ('ACTIVE','SUPERSEDED','RETAINED') FOR SHARE;
    IF selected_release_hash IS DISTINCT FROM NEW.semantic_release_content_hash THEN
      RAISE EXCEPTION 'saved question release pin is not governed' USING ERRCODE='23514';
    END IF;
    IF NEW.status<>'ACTIVE' OR NEW.migration_reason IS NOT NULL THEN
      RAISE EXCEPTION 'saved question initial state is invalid' USING ERRCODE='23514';
    END IF;
    NEW.created_at=clock_timestamp(); NEW.updated_at=NEW.created_at;
    RETURN NEW;
  END IF;
  IF NEW.id IS DISTINCT FROM OLD.id OR NEW.tenant_id IS DISTINCT FROM OLD.tenant_id
    OR NEW.domain_id IS DISTINCT FROM OLD.domain_id OR NEW.owner_user_id IS DISTINCT FROM OLD.owner_user_id
    OR NEW.semantic_ir_json IS DISTINCT FROM OLD.semantic_ir_json
    OR NEW.semantic_ir_hash IS DISTINCT FROM OLD.semantic_ir_hash
    OR NEW.semantic_release_id IS DISTINCT FROM OLD.semantic_release_id
    OR NEW.semantic_release_content_hash IS DISTINCT FROM OLD.semantic_release_content_hash
    OR NEW.source_question_run_id IS DISTINCT FROM OLD.source_question_run_id
    OR NEW.created_at IS DISTINCT FROM OLD.created_at THEN
    RAISE EXCEPTION 'saved question semantic identity is immutable' USING ERRCODE='55000';
  END IF;
  IF OLD.status='ARCHIVED' THEN
    RAISE EXCEPTION 'archived saved question is immutable' USING ERRCODE='55000';
  END IF;
  IF NEW.visibility IS DISTINCT FROM OLD.visibility
    AND NEW.visibility<>'CERTIFIED_CANDIDATE' THEN
    RAISE EXCEPTION 'saved question visibility can only be promoted' USING ERRCODE='23514';
  END IF;
  IF NEW.status IS DISTINCT FROM OLD.status AND NOT (
    (OLD.status='ACTIVE' AND NEW.status IN ('NEEDS_MIGRATION','ARCHIVED'))
    OR (OLD.status='NEEDS_MIGRATION' AND NEW.status='ARCHIVED')
  ) THEN
    RAISE EXCEPTION 'invalid saved question state transition' USING ERRCODE='23514';
  END IF;
  NEW.updated_at=clock_timestamp();
  RETURN NEW;
END
$$;

CREATE OR REPLACE FUNCTION askdata.sync_saved_question_release_reference()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,askdata
AS $$
BEGIN
  IF NEW.status='ARCHIVED' THEN
    UPDATE askdata.release_references SET released_at=clock_timestamp()
    WHERE tenant_id=NEW.tenant_id AND release_id=NEW.semantic_release_id
      AND reference_type='SAVED_QUESTION' AND reference_id=NEW.id AND released_at IS NULL;
  ELSIF TG_OP='INSERT' THEN
    PERFORM askdata.upsert_release_reference(
      NEW.tenant_id,NEW.semantic_release_id,'SAVED_QUESTION',NEW.id,NEW.name,NEW.owner_user_id
    );
  END IF;
  RETURN NEW;
END
$$;

CREATE TRIGGER askdata_saved_questions_10_lifecycle
BEFORE INSERT OR UPDATE OR DELETE ON askdata.saved_questions
FOR EACH ROW EXECUTE FUNCTION askdata.enforce_saved_question();
CREATE TRIGGER askdata_saved_questions_90_release_reference
AFTER INSERT OR UPDATE OF status ON askdata.saved_questions
FOR EACH ROW EXECUTE FUNCTION askdata.sync_saved_question_release_reference();

ALTER TABLE askdata.saved_questions ENABLE ROW LEVEL SECURITY;
ALTER TABLE askdata.saved_questions FORCE ROW LEVEL SECURITY;
ALTER TABLE askdata.saved_question_dependencies ENABLE ROW LEVEL SECURITY;
ALTER TABLE askdata.saved_question_dependencies FORCE ROW LEVEL SECURITY;
ALTER TABLE askdata.saved_question_shares ENABLE ROW LEVEL SECURITY;
ALTER TABLE askdata.saved_question_shares FORCE ROW LEVEL SECURITY;

CREATE POLICY askdata_saved_questions_scope ON askdata.saved_questions
  USING(askdata.saved_question_can_read(tenant_id,domain_id,owner_user_id,id,visibility))
  WITH CHECK(
    tenant_id=askdata.current_tenant_id()
    AND (
      askdata.system_access()
      OR (domain_id=askdata.current_domain_id() AND owner_user_id=askdata.current_actor_id())
    )
  );
CREATE POLICY askdata_saved_question_dependencies_scope ON askdata.saved_question_dependencies
  USING(EXISTS(
    SELECT 1 FROM askdata.saved_questions AS question
    WHERE question.id=saved_question_dependencies.saved_question_id
      AND question.tenant_id=saved_question_dependencies.tenant_id
      AND askdata.saved_question_can_read(question.tenant_id,question.domain_id,
        question.owner_user_id,question.id,question.visibility)
  ))
  WITH CHECK(EXISTS(
    SELECT 1 FROM askdata.saved_questions AS question
    WHERE question.id=saved_question_dependencies.saved_question_id
      AND question.tenant_id=saved_question_dependencies.tenant_id
      AND question.owner_user_id=askdata.current_actor_id()
  ));
CREATE POLICY askdata_saved_question_shares_scope ON askdata.saved_question_shares
  USING(EXISTS(
    SELECT 1 FROM askdata.saved_questions AS question
    WHERE question.id=saved_question_shares.saved_question_id
      AND question.tenant_id=saved_question_shares.tenant_id
      AND askdata.saved_question_can_read(question.tenant_id,question.domain_id,
        question.owner_user_id,question.id,question.visibility)
  ))
  WITH CHECK(EXISTS(
    SELECT 1 FROM askdata.saved_questions AS question
    WHERE question.id=saved_question_shares.saved_question_id
      AND question.tenant_id=saved_question_shares.tenant_id
      AND question.owner_user_id=askdata.current_actor_id()
      AND saved_question_shares.granted_by=askdata.current_actor_id()
  ));

REVOKE ALL ON FUNCTION
  askdata.saved_question_can_read(uuid,uuid,uuid,uuid,text),
  askdata.enforce_saved_question(),
  askdata.sync_saved_question_release_reference()
FROM PUBLIC;

COMMENT ON TABLE askdata.saved_questions IS
  'Saved governed Semantic IR only; opening always creates a fresh viewer-scoped question run and never reuses result data';
