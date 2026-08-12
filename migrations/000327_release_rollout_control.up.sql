-- Durable semantic Release rollout control. A READY candidate is evaluated
-- against the current ACTIVE control through Shadow and stable actor canary
-- buckets before the final atomic activation is allowed.
BEGIN;

CREATE TABLE askdata.release_rollouts(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id) ON DELETE RESTRICT,
  domain_id uuid NOT NULL,
  candidate_release_id uuid NOT NULL,
  control_release_id uuid NOT NULL,
  stage text NOT NULL CHECK(stage IN (
    'SHADOW','CANARY_5','CANARY_20','CANARY_50','ACCEPTED_95'
  )),
  state text NOT NULL CHECK(state IN (
    'RUNNING','PAUSED','STOPPED','ACCEPTED','COMPLETED','ROLLED_BACK'
  )),
  canary_percent smallint NOT NULL CHECK(canary_percent IN (0,5,20,50,95)),
  salt_hash text NOT NULL CHECK(salt_hash ~ '^[0-9a-f]{64}$'),
  reason_hash text NOT NULL CHECK(reason_hash ~ '^[0-9a-f]{64}$'),
  started_by uuid NOT NULL,
  updated_by uuid NOT NULL,
  version bigint NOT NULL DEFAULT 1 CHECK(version>0),
  started_at timestamptz NOT NULL DEFAULT now(),
  stage_started_at timestamptz NOT NULL DEFAULT now(),
  paused_at timestamptz,
  stopped_at timestamptz,
  accepted_at timestamptz,
  completed_at timestamptz,
  rolled_back_at timestamptz,
  updated_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT askdata_release_rollouts_identity_tenant_key UNIQUE(id,tenant_id),
  CONSTRAINT askdata_release_rollouts_candidate_fk
    FOREIGN KEY(candidate_release_id,domain_id,tenant_id)
    REFERENCES askdata.releases(id,domain_id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT askdata_release_rollouts_control_fk
    FOREIGN KEY(control_release_id,domain_id,tenant_id)
    REFERENCES askdata.releases(id,domain_id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT askdata_release_rollouts_started_by_fk
    FOREIGN KEY(started_by,tenant_id)
    REFERENCES platform.users(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT askdata_release_rollouts_updated_by_fk
    FOREIGN KEY(updated_by,tenant_id)
    REFERENCES platform.users(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT askdata_release_rollouts_reason_hash_key UNIQUE(
    tenant_id,candidate_release_id,reason_hash
  ),
  CONSTRAINT askdata_release_rollouts_distinct_release_check
    CHECK(candidate_release_id<>control_release_id),
  CONSTRAINT askdata_release_rollouts_stage_percent_check CHECK(
    (stage='SHADOW' AND canary_percent=0)
    OR (stage='CANARY_5' AND canary_percent=5)
    OR (stage='CANARY_20' AND canary_percent=20)
    OR (stage='CANARY_50' AND canary_percent=50)
    OR (stage='ACCEPTED_95' AND canary_percent=95)
  ),
  CONSTRAINT askdata_release_rollouts_state_time_check CHECK(
    (state='PAUSED' AND paused_at IS NOT NULL)
    OR (state='STOPPED' AND stopped_at IS NOT NULL)
    OR (state='ACCEPTED' AND accepted_at IS NOT NULL)
    OR (state='COMPLETED' AND completed_at IS NOT NULL)
    OR (state='ROLLED_BACK' AND rolled_back_at IS NOT NULL)
    OR state='RUNNING'
  )
);

CREATE UNIQUE INDEX askdata_release_rollouts_one_open_domain_idx
  ON askdata.release_rollouts(tenant_id,domain_id)
  WHERE state IN ('RUNNING','PAUSED','ACCEPTED');
CREATE INDEX askdata_release_rollouts_candidate_idx
  ON askdata.release_rollouts(tenant_id,domain_id,candidate_release_id,updated_at DESC,id);

CREATE TABLE askdata.release_rollout_events(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id) ON DELETE RESTRICT,
  domain_id uuid NOT NULL,
  rollout_id uuid NOT NULL,
  candidate_release_id uuid NOT NULL,
  event_type text NOT NULL CHECK(event_type IN (
    'STARTED','ADVANCED','PAUSED','RESUMED','STOPPED','ACCEPTED','ACTIVATED','ROLLED_BACK'
  )),
  from_stage text,
  to_stage text,
  actor_id uuid NOT NULL,
  reason_hash text NOT NULL CHECK(reason_hash ~ '^[0-9a-f]{64}$'),
  detail jsonb NOT NULL DEFAULT '{}'::jsonb CHECK(
    jsonb_typeof(detail)='object' AND pg_column_size(detail)<=65536
    AND askdata.json_is_safe(detail)
  ),
  created_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT askdata_release_rollout_events_rollout_fk
    FOREIGN KEY(rollout_id,tenant_id)
    REFERENCES askdata.release_rollouts(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT askdata_release_rollout_events_release_fk
    FOREIGN KEY(candidate_release_id,domain_id,tenant_id)
    REFERENCES askdata.releases(id,domain_id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT askdata_release_rollout_events_actor_fk
    FOREIGN KEY(actor_id,tenant_id)
    REFERENCES platform.users(id,tenant_id) ON DELETE RESTRICT
);
CREATE INDEX askdata_release_rollout_events_timeline_idx
  ON askdata.release_rollout_events(tenant_id,domain_id,rollout_id,created_at,id);

CREATE OR REPLACE FUNCTION askdata.enforce_release_rollout_identity()
RETURNS trigger
LANGUAGE plpgsql
SET search_path=pg_catalog,askdata
AS $$
BEGIN
  IF NEW.id IS DISTINCT FROM OLD.id
    OR NEW.tenant_id IS DISTINCT FROM OLD.tenant_id
    OR NEW.domain_id IS DISTINCT FROM OLD.domain_id
    OR NEW.candidate_release_id IS DISTINCT FROM OLD.candidate_release_id
    OR NEW.control_release_id IS DISTINCT FROM OLD.control_release_id
    OR NEW.salt_hash IS DISTINCT FROM OLD.salt_hash
    OR NEW.started_by IS DISTINCT FROM OLD.started_by
    OR NEW.started_at IS DISTINCT FROM OLD.started_at THEN
    RAISE EXCEPTION 'release rollout identity is immutable' USING ERRCODE='55000';
  END IF;
  RETURN NEW;
END
$$;

CREATE TRIGGER askdata_release_rollouts_identity_immutable
BEFORE UPDATE ON askdata.release_rollouts
FOR EACH ROW EXECUTE FUNCTION askdata.enforce_release_rollout_identity();

CREATE TRIGGER askdata_release_rollout_events_immutable
BEFORE UPDATE OR DELETE ON askdata.release_rollout_events
FOR EACH ROW EXECUTE FUNCTION askdata.reject_immutable_mutation();

ALTER TABLE askdata.release_rollouts ENABLE ROW LEVEL SECURITY;
ALTER TABLE askdata.release_rollouts FORCE ROW LEVEL SECURITY;
CREATE POLICY askdata_release_rollouts_management_isolation
  ON askdata.release_rollouts
  USING(askdata.evaluation_control_can_access(tenant_id,domain_id))
  WITH CHECK(askdata.evaluation_control_can_access(tenant_id,domain_id));
ALTER TABLE askdata.release_rollout_events ENABLE ROW LEVEL SECURITY;
ALTER TABLE askdata.release_rollout_events FORCE ROW LEVEL SECURITY;
CREATE POLICY askdata_release_rollout_events_management_isolation
  ON askdata.release_rollout_events
  USING(askdata.evaluation_control_can_access(tenant_id,domain_id))
  WITH CHECK(askdata.evaluation_control_can_access(tenant_id,domain_id));

CREATE OR REPLACE FUNCTION askdata.release_rollout_bucket(
  selected_salt_hash text, selected_actor_id uuid
)
RETURNS integer
LANGUAGE sql IMMUTABLE STRICT
SET search_path=pg_catalog,public
AS $$
  SELECT ((
    get_byte(public.digest(selected_salt_hash||':'||selected_actor_id::text,'sha256'),0)::bigint*16777216
    + get_byte(public.digest(selected_salt_hash||':'||selected_actor_id::text,'sha256'),1)::bigint*65536
    + get_byte(public.digest(selected_salt_hash||':'||selected_actor_id::text,'sha256'),2)::bigint*256
    + get_byte(public.digest(selected_salt_hash||':'||selected_actor_id::text,'sha256'),3)::bigint
  ) % 100)::integer
$$;

CREATE OR REPLACE FUNCTION askdata.resolve_question_release(
  selected_tenant_id uuid, selected_domain_id uuid, selected_actor_id uuid
)
RETURNS TABLE(release_id uuid,content_hash text,cohort text,rollout_id uuid)
LANGUAGE plpgsql STABLE SECURITY DEFINER
SET search_path=pg_catalog,askdata
AS $$
DECLARE selected_control askdata.releases%ROWTYPE;
DECLARE selected_rollout askdata.release_rollouts%ROWTYPE;
DECLARE selected_candidate askdata.releases%ROWTYPE;
BEGIN
  IF selected_actor_id IS NULL OR selected_actor_id<>askdata.current_actor_id()
    OR NOT askdata.tenant_matches(selected_tenant_id)
    OR NOT askdata.domain_can_access(selected_domain_id) THEN
    RETURN;
  END IF;
  SELECT release.* INTO selected_control
  FROM askdata.release_state AS state
  JOIN askdata.releases AS release
    ON release.tenant_id=state.tenant_id AND release.domain_id=state.domain_id
   AND release.id=state.active_release_id AND release.status='ACTIVE'
  WHERE state.tenant_id=selected_tenant_id AND state.domain_id=selected_domain_id;
  IF selected_control.id IS NULL THEN RETURN; END IF;

  SELECT rollout.* INTO selected_rollout
  FROM askdata.release_rollouts AS rollout
  WHERE rollout.tenant_id=selected_tenant_id AND rollout.domain_id=selected_domain_id
    AND rollout.control_release_id=selected_control.id
    AND rollout.state='RUNNING'
  ORDER BY rollout.updated_at DESC,rollout.id DESC LIMIT 1;
  IF selected_rollout.id IS NOT NULL
    AND selected_rollout.stage IN ('CANARY_5','CANARY_20','CANARY_50','ACCEPTED_95')
    AND askdata.release_rollout_bucket(selected_rollout.salt_hash,selected_actor_id)<selected_rollout.canary_percent THEN
    SELECT release.* INTO selected_candidate FROM askdata.releases AS release
    WHERE release.tenant_id=selected_tenant_id AND release.domain_id=selected_domain_id
      AND release.id=selected_rollout.candidate_release_id AND release.status='READY';
    IF selected_candidate.id IS NOT NULL THEN
      RETURN QUERY SELECT selected_candidate.id,selected_candidate.content_hash,
        'CANDIDATE'::text,selected_rollout.id;
      RETURN;
    END IF;
  END IF;
  RETURN QUERY SELECT selected_control.id,selected_control.content_hash,
    CASE WHEN selected_rollout.stage='SHADOW' THEN 'SHADOW_CONTROL' ELSE 'CONTROL' END,
    selected_rollout.id;
END
$$;

CREATE OR REPLACE FUNCTION askdata.lock_active_question_release(
  selected_tenant_id uuid, selected_domain_id uuid,
  selected_release_id uuid, selected_content_hash text
)
RETURNS boolean
LANGUAGE plpgsql VOLATILE SECURITY DEFINER
SET search_path=pg_catalog,askdata
AS $$
DECLARE release_status text;
DECLARE release_valid boolean := false;
DECLARE routed_release_id uuid;
DECLARE routed_content_hash text;
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
  IF release_status='ACTIVE' THEN RETURN true; END IF;

  SELECT route.release_id,route.content_hash INTO routed_release_id,routed_content_hash
  FROM askdata.resolve_question_release(
    selected_tenant_id,selected_domain_id,askdata.current_actor_id()
  ) AS route;
  IF routed_release_id=selected_release_id AND routed_content_hash=selected_content_hash THEN
    RETURN true;
  END IF;
  IF release_status NOT IN ('SUPERSEDED','RETAINED') THEN RETURN false; END IF;
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

CREATE OR REPLACE FUNCTION askdata.enforce_release_retention_lifecycle()
RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER
SET search_path=pg_catalog,askdata
AS $$
DECLARE active_reference_count bigint;
DECLARE retained_timestamp timestamptz;
DECLARE rollback_release_id text;
BEGIN
  rollback_release_id := current_setting('askdata.rollback_release_id',true);
  IF OLD.status='RETIRED' THEN
    RAISE EXCEPTION 'retired release is immutable' USING ERRCODE='55000';
  END IF;
  IF NEW.status IS NOT DISTINCT FROM OLD.status THEN RETURN NEW; END IF;
  SELECT count(*) INTO active_reference_count FROM askdata.release_references
  WHERE tenant_id=OLD.tenant_id AND release_id=OLD.id AND released_at IS NULL;
  IF OLD.status='RETAINED' AND NEW.status='ACTIVE' THEN
    IF rollback_release_id IS DISTINCT FROM OLD.id::text THEN
      RAISE EXCEPTION 'RETAINED release can only reactivate through governed rollback'
        USING ERRCODE='23514';
    END IF;
    NEW.retained_at=NULL; NEW.retention_until=NULL; NEW.retired_at=NULL;
  ELSIF NEW.status='SUPERSEDED' AND active_reference_count>0 THEN
    retained_timestamp=clock_timestamp(); NEW.status='RETAINED';
    NEW.retained_at=retained_timestamp;
    NEW.retention_until=retained_timestamp+interval '24 months'; NEW.retired_at=NULL;
  ELSIF NEW.status='RETAINED' THEN
    IF OLD.status<>'SUPERSEDED' OR active_reference_count=0 THEN
      RAISE EXCEPTION 'RETAINED requires a referenced SUPERSEDED release' USING ERRCODE='23514';
    END IF;
    retained_timestamp=clock_timestamp(); NEW.retained_at=retained_timestamp;
    NEW.retention_until=retained_timestamp+interval '24 months'; NEW.retired_at=NULL;
  ELSIF NEW.status='RETIRED' THEN
    IF OLD.status NOT IN ('SUPERSEDED','RETAINED') THEN
      RAISE EXCEPTION 'release can only retire after SUPERSEDED or RETAINED' USING ERRCODE='23514';
    END IF;
    IF active_reference_count>0 THEN
      RAISE EXCEPTION 'RELEASE_RETIRE_BLOCKED: release has active references' USING ERRCODE='23514';
    END IF;
    IF OLD.status='RETAINED' AND clock_timestamp()<OLD.retention_until THEN
      RAISE EXCEPTION 'RELEASE_RETENTION_NOT_EXPIRED: retained release is still within its retention window' USING ERRCODE='23514';
    END IF;
    NEW.retained_at=OLD.retained_at; NEW.retention_until=OLD.retention_until;
    NEW.retired_at=clock_timestamp();
  ELSIF OLD.status='RETAINED' THEN
    RAISE EXCEPTION 'RETAINED release can only transition to RETIRED' USING ERRCODE='23514';
  ELSE
    NEW.retained_at=NULL; NEW.retention_until=NULL; NEW.retired_at=NULL;
  END IF;
  RETURN NEW;
END
$$;

CREATE OR REPLACE FUNCTION askdata.enforce_conversation_release_pin()
RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER
SET search_path=pg_catalog,askdata,platform
AS $$
DECLARE old_status text;
DECLARE new_status text;
DECLARE report_pin_valid boolean := false;
DECLARE saved_pin_valid boolean := false;
DECLARE routed_release_id uuid;
BEGIN
  IF TG_OP='DELETE' THEN RAISE EXCEPTION 'question conversation cannot be deleted' USING ERRCODE='55000'; END IF;
  IF TG_OP='INSERT' THEN
    IF NEW.pinned_release_id IS NOT NULL THEN
      SELECT status INTO new_status FROM askdata.releases
      WHERE tenant_id=NEW.tenant_id AND domain_id=NEW.domain_id AND id=NEW.pinned_release_id FOR SHARE;
      IF NEW.pin_source='REPORT_COMPONENT' THEN
        SELECT true INTO report_pin_valid FROM platform.report_versions AS version
        JOIN platform.reports AS report ON report.id=version.report_id AND report.tenant_id=version.tenant_id
        JOIN platform.report_version_dependencies AS dependency
          ON dependency.report_version_id=version.id AND dependency.report_id=version.report_id AND dependency.tenant_id=version.tenant_id
        WHERE version.id=NEW.report_version_id AND version.tenant_id=NEW.tenant_id
          AND version.artifact_state='READY' AND report.status='ACTIVE' AND report.domain_id=NEW.domain_id
          AND dependency.dependency_type='SEMANTIC_RELEASE'
          AND dependency.dependency_id=NEW.pinned_release_id::text
          AND NEW.report_component_id=ANY(dependency.component_ids) FOR SHARE OF version,report;
      ELSIF NEW.pin_source='SAVED_QUESTION' THEN
        SELECT true INTO saved_pin_valid FROM askdata.saved_questions AS question
        WHERE question.id=NEW.saved_question_id AND question.tenant_id=NEW.tenant_id
          AND question.domain_id=NEW.domain_id AND question.semantic_release_id=NEW.pinned_release_id
          AND question.status='ACTIVE' AND askdata.saved_question_can_read(
            question.tenant_id,question.domain_id,question.owner_user_id,question.id,question.visibility
          ) FOR SHARE OF question;
      ELSE
        SELECT route.release_id INTO routed_release_id FROM askdata.resolve_question_release(
          NEW.tenant_id,NEW.domain_id,NEW.actor_id
        ) AS route;
      END IF;
      IF (NEW.pin_source='ASKDATA' AND NEW.pinned_release_id IS DISTINCT FROM routed_release_id)
        OR (NEW.pin_source='REPORT_COMPONENT' AND (new_status NOT IN ('ACTIVE','SUPERSEDED','RETAINED') OR NOT COALESCE(report_pin_valid,false)))
        OR (NEW.pin_source='SAVED_QUESTION' AND (new_status NOT IN ('ACTIVE','SUPERSEDED','RETAINED') OR NOT COALESCE(saved_pin_valid,false)))
        OR NEW.pin_drift_acknowledged THEN
        RAISE EXCEPTION 'initial conversation pin is not runnable for its source' USING ERRCODE='23514';
      END IF;
      NEW.pinned_at=COALESCE(NEW.pinned_at,clock_timestamp());
    ELSIF NEW.pin_source<>'ASKDATA' THEN
      RAISE EXCEPTION 'governed semantic source requires a release pin' USING ERRCODE='23514';
    END IF;
    NEW.created_at=clock_timestamp(); NEW.updated_at=NEW.created_at; RETURN NEW;
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
    IF NEW.pinned_release_id IS NULL THEN RAISE EXCEPTION 'conversation release pin cannot be cleared' USING ERRCODE='55000'; END IF;
    SELECT status INTO new_status FROM askdata.releases
    WHERE tenant_id=NEW.tenant_id AND domain_id=NEW.domain_id AND id=NEW.pinned_release_id FOR SHARE;
    IF OLD.pinned_release_id IS NULL AND NEW.pin_source='ASKDATA' THEN
      SELECT route.release_id INTO routed_release_id FROM askdata.resolve_question_release(
        NEW.tenant_id,NEW.domain_id,NEW.actor_id
      ) AS route;
      IF NEW.pinned_release_id IS DISTINCT FROM routed_release_id OR NEW.pin_drift_acknowledged THEN
        RAISE EXCEPTION 'conversation can only pin its current routed release' USING ERRCODE='23514';
      END IF;
    ELSE
      IF new_status<>'ACTIVE' THEN RAISE EXCEPTION 'conversation can only switch to the current ACTIVE release' USING ERRCODE='23514'; END IF;
      SELECT status INTO old_status FROM askdata.releases
      WHERE tenant_id=OLD.tenant_id AND domain_id=OLD.domain_id AND id=OLD.pinned_release_id FOR SHARE;
      IF old_status NOT IN ('SUPERSEDED','RETAINED','RETIRED') OR NOT NEW.pin_drift_acknowledged THEN
        RAISE EXCEPTION 'release drift switch requires an acknowledged stale pin' USING ERRCODE='23514';
      END IF;
    END IF;
    NEW.pinned_at=clock_timestamp();
  ELSIF NEW.pin_drift_acknowledged IS DISTINCT FROM OLD.pin_drift_acknowledged THEN
    RAISE EXCEPTION 'drift acknowledgement can only change with the release pin' USING ERRCODE='55000';
  END IF;
  NEW.updated_at=clock_timestamp(); RETURN NEW;
END
$$;

ALTER TABLE askdata.release_events
  DROP CONSTRAINT askdata_release_events_event_type_check;
ALTER TABLE askdata.release_events
  ADD CONSTRAINT askdata_release_events_event_type_check CHECK(event_type IN (
    'CREATED','VALIDATING','PROJECTING','PROJECTION_READY','PROJECTION_FAILED',
    'PROJECTION_RETRIED','READY','ACTIVATED','SUPERSEDED','RETAINED','RETIRED',
    'ROLLED_BACK','BLOCKED'
  ));

REVOKE ALL ON FUNCTION
  askdata.release_rollout_bucket(text,uuid),
  askdata.resolve_question_release(uuid,uuid,uuid),
  askdata.lock_active_question_release(uuid,uuid,uuid,text),
  askdata.enforce_release_rollout_identity(),
  askdata.enforce_release_retention_lifecycle(),
  askdata.enforce_conversation_release_pin()
FROM PUBLIC;
REVOKE ALL ON TABLE askdata.release_rollouts,askdata.release_rollout_events FROM PUBLIC;

COMMIT;
