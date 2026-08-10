ALTER TABLE askdata.conversations
  ADD COLUMN pin_source text NOT NULL DEFAULT 'ASKDATA'
    CHECK(pin_source IN ('ASKDATA','REPORT_COMPONENT')),
  ADD COLUMN report_version_id uuid REFERENCES platform.report_versions(id) ON DELETE RESTRICT,
  ADD COLUMN report_component_id text CHECK(
    report_component_id IS NULL OR length(report_component_id) BETWEEN 1 AND 128
  ),
  ADD CONSTRAINT askdata_conversations_report_source_shape CHECK(
    (pin_source='ASKDATA' AND report_version_id IS NULL AND report_component_id IS NULL)
    OR (
      pin_source='REPORT_COMPONENT' AND pinned_release_id IS NOT NULL
      AND report_version_id IS NOT NULL AND report_component_id IS NOT NULL
    )
  );

CREATE OR REPLACE FUNCTION askdata.enforce_conversation_release_pin()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,askdata
AS $$
DECLARE old_status text;
DECLARE new_status text;
DECLARE report_pin_valid boolean := false;
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
      END IF;
      IF (NEW.pin_source='ASKDATA' AND new_status<>'ACTIVE')
         OR (NEW.pin_source='REPORT_COMPONENT' AND (
           new_status NOT IN ('ACTIVE','SUPERSEDED','RETAINED') OR NOT report_pin_valid
         )) OR NEW.pin_drift_acknowledged THEN
        RAISE EXCEPTION 'initial conversation pin is not runnable for its source'
          USING ERRCODE='23514';
      END IF;
      NEW.pinned_at=COALESCE(NEW.pinned_at,clock_timestamp());
    ELSIF NEW.pin_source<>'ASKDATA' THEN
      RAISE EXCEPTION 'report component conversation requires a release pin'
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
    OR NEW.report_component_id IS DISTINCT FROM OLD.report_component_id THEN
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

CREATE TABLE askdata.question_seed_contexts(
  tenant_id uuid NOT NULL,
  question_run_id uuid NOT NULL,
  report_version_id uuid NOT NULL,
  component_id text NOT NULL CHECK(length(component_id) BETWEEN 1 AND 128),
  semantic_ir_json jsonb NOT NULL CHECK(jsonb_typeof(semantic_ir_json)='object' AND askdata.json_is_safe(semantic_ir_json)),
  semantic_ir_hash text NOT NULL CHECK(semantic_ir_hash ~ '^[0-9a-f]{64}$'),
  pinned_release_id uuid NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY(tenant_id,question_run_id),
  FOREIGN KEY(question_run_id,tenant_id) REFERENCES askdata.question_runs(id,tenant_id) ON DELETE CASCADE,
  FOREIGN KEY(report_version_id) REFERENCES platform.report_versions(id) ON DELETE RESTRICT,
  FOREIGN KEY(pinned_release_id) REFERENCES askdata.releases(id) ON DELETE RESTRICT
);

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
DECLARE valid_report_pin boolean := false;
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
  SELECT true INTO valid_report_pin
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
  RETURN COALESCE(valid_report_pin,false);
END
$$;

CREATE OR REPLACE FUNCTION askdata.reject_non_runnable_question_release()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,askdata
AS $$
BEGIN
  IF NOT askdata.report_seed_release_is_runnable(
    NEW.tenant_id,NEW.domain_id,NEW.actor_id,NEW.conversation_id,
    NEW.release_id,NEW.release_content_hash
  ) THEN
    RAISE EXCEPTION 'RELEASE_NOT_RUNNABLE: semantic release cannot create this question run'
      USING ERRCODE='23514';
  END IF;
  RETURN NEW;
END
$$;

-- The lifecycle trigger still calls the four-argument helper introduced by
-- 000217. The earlier, exact per-row trigger has already proved the selected
-- conversation before this helper is reached; this helper keeps the release
-- row locked across insertion and accepts a historical pin only when the same
-- actor owns a governed report-component conversation for it.
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
    AND conversation.pin_source='REPORT_COMPONENT'
  LIMIT 1;
  RETURN COALESCE(release_valid,false);
END
$$;

ALTER TABLE askdata.question_seed_contexts ENABLE ROW LEVEL SECURITY;
ALTER TABLE askdata.question_seed_contexts FORCE ROW LEVEL SECURITY;
CREATE POLICY question_seed_contexts_actor_scope ON askdata.question_seed_contexts
  USING(EXISTS(
    SELECT 1 FROM askdata.question_runs AS run
    WHERE run.tenant_id=question_seed_contexts.tenant_id AND run.id=question_seed_contexts.question_run_id
      AND askdata.question_runtime_can_access(run.tenant_id,run.domain_id,run.actor_id)
  ))
  WITH CHECK(EXISTS(
    SELECT 1 FROM askdata.question_runs AS run
    WHERE run.tenant_id=question_seed_contexts.tenant_id AND run.id=question_seed_contexts.question_run_id
      AND askdata.question_runtime_can_access(run.tenant_id,run.domain_id,run.actor_id)
  ));

REVOKE ALL ON FUNCTION
  askdata.enforce_conversation_release_pin(),
  askdata.report_seed_release_is_runnable(uuid,uuid,uuid,uuid,uuid,text),
  askdata.reject_non_runnable_question_release(),
  askdata.lock_active_question_release(uuid,uuid,uuid,text)
FROM PUBLIC;
