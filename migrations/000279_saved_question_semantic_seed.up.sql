-- SQ-001 / FUSE-003: a saved question is an immutable semantic seed, not a
-- request to reinterpret its display text. Historical Release use is allowed
-- only after the current viewer proves access to the still-active source.
ALTER TABLE askdata.conversations
  DROP CONSTRAINT conversations_pin_source_check,
  DROP CONSTRAINT askdata_conversations_report_source_shape,
  ADD COLUMN saved_question_id uuid,
  ADD CONSTRAINT askdata_conversations_saved_question_tenant_fk
    FOREIGN KEY(saved_question_id,tenant_id)
    REFERENCES askdata.saved_questions(id,tenant_id) ON DELETE RESTRICT,
  ADD CONSTRAINT conversations_pin_source_check CHECK(
    pin_source IN ('ASKDATA','REPORT_COMPONENT','SAVED_QUESTION')
  ),
  ADD CONSTRAINT askdata_conversations_report_source_shape CHECK(
    (pin_source='ASKDATA' AND report_version_id IS NULL
      AND report_component_id IS NULL AND saved_question_id IS NULL)
    OR (pin_source='REPORT_COMPONENT' AND pinned_release_id IS NOT NULL
      AND report_version_id IS NOT NULL AND report_component_id IS NOT NULL
      AND saved_question_id IS NULL)
    OR (pin_source='SAVED_QUESTION' AND pinned_release_id IS NOT NULL
      AND report_version_id IS NULL AND report_component_id IS NULL
      AND saved_question_id IS NOT NULL)
  );

CREATE TABLE askdata.question_saved_seed_contexts(
  tenant_id uuid NOT NULL,
  question_run_id uuid NOT NULL,
  saved_question_id uuid NOT NULL,
  semantic_ir_json jsonb NOT NULL CHECK(
    jsonb_typeof(semantic_ir_json)='object'
    AND pg_column_size(semantic_ir_json)<=262144
    AND askdata.json_is_safe(semantic_ir_json)
  ),
  semantic_ir_hash text NOT NULL CHECK(semantic_ir_hash ~ '^[0-9a-f]{64}$'),
  pinned_release_id uuid NOT NULL,
  created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
  PRIMARY KEY(tenant_id,question_run_id),
  FOREIGN KEY(question_run_id,tenant_id)
    REFERENCES askdata.question_runs(id,tenant_id) ON DELETE CASCADE,
  FOREIGN KEY(saved_question_id,tenant_id)
    REFERENCES askdata.saved_questions(id,tenant_id) ON DELETE RESTRICT,
  FOREIGN KEY(pinned_release_id,tenant_id)
    REFERENCES askdata.releases(id,tenant_id) ON DELETE RESTRICT
);

ALTER TABLE askdata.question_saved_seed_contexts ENABLE ROW LEVEL SECURITY;
ALTER TABLE askdata.question_saved_seed_contexts FORCE ROW LEVEL SECURITY;
CREATE POLICY question_saved_seed_contexts_actor_scope
  ON askdata.question_saved_seed_contexts
  USING(EXISTS(
    SELECT 1 FROM askdata.question_runs AS run
    WHERE run.tenant_id=question_saved_seed_contexts.tenant_id
      AND run.id=question_saved_seed_contexts.question_run_id
      AND askdata.question_runtime_can_access(run.tenant_id,run.domain_id,run.actor_id)
  ))
  WITH CHECK(EXISTS(
    SELECT 1 FROM askdata.question_runs AS run
    WHERE run.tenant_id=question_saved_seed_contexts.tenant_id
      AND run.id=question_saved_seed_contexts.question_run_id
      AND askdata.question_runtime_can_access(run.tenant_id,run.domain_id,run.actor_id)
  ));

CREATE OR REPLACE FUNCTION askdata.enforce_conversation_release_pin()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,askdata,platform
AS $$
DECLARE old_status text;
DECLARE new_status text;
DECLARE report_pin_valid boolean := false;
DECLARE saved_pin_valid boolean := false;
BEGIN
  IF TG_OP='DELETE' THEN
    RAISE EXCEPTION 'question conversation cannot be deleted' USING ERRCODE='55000';
  END IF;
  IF TG_OP='INSERT' THEN
    IF NEW.pinned_release_id IS NOT NULL THEN
      SELECT status INTO new_status FROM askdata.releases
      WHERE tenant_id=NEW.tenant_id AND domain_id=NEW.domain_id
        AND id=NEW.pinned_release_id FOR SHARE;
      IF NEW.pin_source='REPORT_COMPONENT' THEN
        SELECT true INTO report_pin_valid
        FROM platform.report_versions AS version
        JOIN platform.reports AS report
          ON report.id=version.report_id AND report.tenant_id=version.tenant_id
        JOIN platform.report_version_dependencies AS dependency
          ON dependency.report_version_id=version.id AND dependency.report_id=version.report_id
         AND dependency.tenant_id=version.tenant_id
        WHERE version.id=NEW.report_version_id AND version.tenant_id=NEW.tenant_id
          AND version.artifact_state='READY' AND report.status='ACTIVE'
          AND report.domain_id=NEW.domain_id
          AND dependency.dependency_type='SEMANTIC_RELEASE'
          AND dependency.dependency_id=NEW.pinned_release_id::text
          AND NEW.report_component_id=ANY(dependency.component_ids)
        FOR SHARE OF version,report;
      ELSIF NEW.pin_source='SAVED_QUESTION' THEN
        SELECT true INTO saved_pin_valid
        FROM askdata.saved_questions AS question
        WHERE question.id=NEW.saved_question_id
          AND question.tenant_id=NEW.tenant_id
          AND question.domain_id=NEW.domain_id
          AND question.semantic_release_id=NEW.pinned_release_id
          AND question.status='ACTIVE'
          AND askdata.saved_question_can_read(
            question.tenant_id,question.domain_id,question.owner_user_id,
            question.id,question.visibility
          )
        FOR SHARE OF question;
      END IF;
      IF (NEW.pin_source='ASKDATA' AND new_status<>'ACTIVE')
         OR (NEW.pin_source='REPORT_COMPONENT' AND (
           new_status NOT IN ('ACTIVE','SUPERSEDED','RETAINED')
           OR NOT COALESCE(report_pin_valid,false)
         ))
         OR (NEW.pin_source='SAVED_QUESTION' AND (
           new_status NOT IN ('ACTIVE','SUPERSEDED','RETAINED')
           OR NOT COALESCE(saved_pin_valid,false)
         )) OR NEW.pin_drift_acknowledged THEN
        RAISE EXCEPTION 'initial conversation pin is not runnable for its source'
          USING ERRCODE='23514';
      END IF;
      NEW.pinned_at=COALESCE(NEW.pinned_at,clock_timestamp());
    ELSIF NEW.pin_source<>'ASKDATA' THEN
      RAISE EXCEPTION 'governed semantic source requires a release pin'
        USING ERRCODE='23514';
    END IF;
    NEW.created_at=clock_timestamp();
    NEW.updated_at=NEW.created_at;
    RETURN NEW;
  END IF;
  IF NEW.id IS DISTINCT FROM OLD.id OR NEW.tenant_id IS DISTINCT FROM OLD.tenant_id
    OR NEW.domain_id IS DISTINCT FROM OLD.domain_id OR NEW.actor_id IS DISTINCT FROM OLD.actor_id
    OR NEW.created_at IS DISTINCT FROM OLD.created_at OR NEW.pin_source IS DISTINCT FROM OLD.pin_source
    OR NEW.report_version_id IS DISTINCT FROM OLD.report_version_id
    OR NEW.report_component_id IS DISTINCT FROM OLD.report_component_id
    OR NEW.saved_question_id IS DISTINCT FROM OLD.saved_question_id THEN
    RAISE EXCEPTION 'conversation identity is immutable' USING ERRCODE='55000';
  END IF;
  IF NEW.pinned_release_id IS DISTINCT FROM OLD.pinned_release_id THEN
    IF NEW.pinned_release_id IS NULL THEN
      RAISE EXCEPTION 'conversation release pin cannot be cleared' USING ERRCODE='55000';
    END IF;
    SELECT status INTO new_status FROM askdata.releases
    WHERE tenant_id=NEW.tenant_id AND domain_id=NEW.domain_id
      AND id=NEW.pinned_release_id FOR SHARE;
    IF new_status<>'ACTIVE' THEN
      RAISE EXCEPTION 'conversation can only switch to the current ACTIVE release'
        USING ERRCODE='23514';
    END IF;
    IF OLD.pinned_release_id IS NULL THEN
      IF NEW.pin_drift_acknowledged THEN
        RAISE EXCEPTION 'first successful binding is not a drift acknowledgement'
          USING ERRCODE='23514';
      END IF;
    ELSE
      SELECT status INTO old_status FROM askdata.releases
      WHERE tenant_id=OLD.tenant_id AND domain_id=OLD.domain_id
        AND id=OLD.pinned_release_id FOR SHARE;
      IF old_status NOT IN ('SUPERSEDED','RETAINED','RETIRED')
        OR NOT NEW.pin_drift_acknowledged THEN
        RAISE EXCEPTION 'release drift switch requires an acknowledged stale pin'
          USING ERRCODE='23514';
      END IF;
    END IF;
    NEW.pinned_at=clock_timestamp();
  ELSIF NEW.pin_drift_acknowledged IS DISTINCT FROM OLD.pin_drift_acknowledged THEN
    RAISE EXCEPTION 'drift acknowledgement can only change with the release pin'
      USING ERRCODE='55000';
  END IF;
  NEW.updated_at=clock_timestamp();
  RETURN NEW;
END
$$;

CREATE OR REPLACE FUNCTION askdata.report_seed_release_is_runnable(
  selected_tenant_id uuid,
  selected_domain_id uuid,
  selected_actor_id uuid,
  selected_conversation_id uuid,
  selected_release_id uuid,
  selected_content_hash text
)
RETURNS boolean
LANGUAGE plpgsql
VOLATILE
SECURITY DEFINER
SET search_path=pg_catalog,askdata,platform
AS $$
DECLARE release_status text;
DECLARE valid_source_pin boolean := false;
BEGIN
  IF NOT askdata.tenant_matches(selected_tenant_id)
    OR NOT askdata.domain_can_access(selected_domain_id)
    OR (NOT askdata.system_access() AND askdata.current_actor_id() IS DISTINCT FROM selected_actor_id) THEN
    RETURN false;
  END IF;
  SELECT status INTO release_status FROM askdata.releases AS release
  WHERE release.id=selected_release_id AND release.tenant_id=selected_tenant_id
    AND release.domain_id=selected_domain_id AND release.content_hash=selected_content_hash
  FOR SHARE OF release;
  IF release_status='ACTIVE' THEN
    RETURN true;
  END IF;
  IF release_status NOT IN ('SUPERSEDED','RETAINED') OR selected_conversation_id IS NULL THEN
    RETURN false;
  END IF;
  SELECT true INTO valid_source_pin
  FROM askdata.conversations AS conversation
  JOIN platform.report_versions AS version ON version.id=conversation.report_version_id
  JOIN platform.reports AS report
    ON report.id=version.report_id AND report.tenant_id=version.tenant_id
  JOIN platform.report_version_dependencies AS dependency
    ON dependency.report_version_id=version.id AND dependency.report_id=version.report_id
   AND dependency.tenant_id=version.tenant_id
  WHERE conversation.tenant_id=selected_tenant_id
    AND conversation.domain_id=selected_domain_id
    AND conversation.actor_id=selected_actor_id
    AND conversation.id=selected_conversation_id
    AND conversation.pin_source='REPORT_COMPONENT'
    AND conversation.pinned_release_id=selected_release_id
    AND version.artifact_state='READY' AND report.status='ACTIVE'
    AND report.domain_id=selected_domain_id
    AND dependency.dependency_type='SEMANTIC_RELEASE'
    AND dependency.dependency_id=selected_release_id::text
    AND conversation.report_component_id=ANY(dependency.component_ids)
  FOR SHARE OF conversation,version,report;
  IF COALESCE(valid_source_pin,false) THEN
    RETURN true;
  END IF;
  SELECT true INTO valid_source_pin
  FROM askdata.conversations AS conversation
  JOIN askdata.saved_questions AS question
    ON question.id=conversation.saved_question_id
   AND question.tenant_id=conversation.tenant_id
  WHERE conversation.tenant_id=selected_tenant_id
    AND conversation.domain_id=selected_domain_id
    AND conversation.actor_id=selected_actor_id
    AND conversation.id=selected_conversation_id
    AND conversation.pin_source='SAVED_QUESTION'
    AND conversation.pinned_release_id=selected_release_id
    AND question.domain_id=selected_domain_id
    AND question.semantic_release_id=selected_release_id
    AND question.status='ACTIVE'
    AND askdata.saved_question_can_read(
      question.tenant_id,question.domain_id,question.owner_user_id,
      question.id,question.visibility
    )
  FOR SHARE OF conversation,question;
  RETURN COALESCE(valid_source_pin,false);
END
$$;

CREATE OR REPLACE FUNCTION askdata.lock_active_question_release(
  selected_tenant_id uuid,
  selected_domain_id uuid,
  selected_release_id uuid,
  selected_content_hash text
)
RETURNS boolean
LANGUAGE plpgsql
VOLATILE
SECURITY DEFINER
SET search_path=pg_catalog,askdata
AS $$
DECLARE release_status text;
DECLARE release_valid boolean := false;
BEGIN
  IF NOT askdata.tenant_matches(selected_tenant_id)
    OR NOT askdata.domain_can_access(selected_domain_id)
    OR (NOT askdata.system_access() AND askdata.current_actor_id() IS NULL) THEN
    RETURN false;
  END IF;
  SELECT status INTO release_status FROM askdata.releases AS release
  WHERE release.id=selected_release_id AND release.domain_id=selected_domain_id
    AND release.tenant_id=selected_tenant_id AND release.content_hash=selected_content_hash
  FOR SHARE OF release;
  IF release_status='ACTIVE' THEN
    RETURN true;
  END IF;
  IF release_status NOT IN ('SUPERSEDED','RETAINED') THEN
    RETURN false;
  END IF;
  SELECT true INTO release_valid FROM askdata.conversations AS conversation
  WHERE conversation.tenant_id=selected_tenant_id
    AND conversation.domain_id=selected_domain_id
    AND conversation.actor_id=askdata.current_actor_id()
    AND conversation.pinned_release_id=selected_release_id
    AND conversation.pin_source IN ('REPORT_COMPONENT','SAVED_QUESTION')
  LIMIT 1;
  RETURN COALESCE(release_valid,false);
END
$$;

REVOKE ALL ON FUNCTION
  askdata.enforce_conversation_release_pin(),
  askdata.report_seed_release_is_runnable(uuid,uuid,uuid,uuid,uuid,text),
  askdata.lock_active_question_release(uuid,uuid,uuid,text)
FROM PUBLIC;

COMMENT ON TABLE askdata.question_saved_seed_contexts IS
  'Immutable saved-question Semantic IR seed for a new viewer-scoped run; contains no result rows.';
