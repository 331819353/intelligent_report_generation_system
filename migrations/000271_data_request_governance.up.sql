-- DR-002/DR-003: make field sensitivity a governed derived index and enforce
-- data-request approval independently at the database boundary.
ALTER TABLE platform.dataset_fields
  ADD COLUMN sensitivity_level platform.asset_sensitivity NOT NULL DEFAULT 'INTERNAL';

WITH declared AS (
  SELECT version.id AS dataset_version_id,field_document->>'id' AS field_id,
    (field_document->>'sensitivityLevel')::platform.asset_sensitivity AS sensitivity_level
  FROM platform.dataset_versions AS version
  CROSS JOIN LATERAL jsonb_array_elements(COALESCE(version.dsl_json->'fields','[]'::jsonb)) AS field_document
  WHERE field_document->>'sensitivityLevel' IN ('PUBLIC','INTERNAL','CONFIDENTIAL','RESTRICTED')
)
UPDATE platform.dataset_fields AS field
SET sensitivity_level=declared.sensitivity_level
FROM declared
WHERE declared.dataset_version_id=field.dataset_version_id
  AND declared.field_id=field.field_id;

WITH physical AS (
  SELECT field.id,column_asset.sensitivity_level
  FROM platform.dataset_fields AS field
  JOIN platform.dataset_versions AS version
    ON version.id=field.dataset_version_id AND version.tenant_id=field.tenant_id
  JOIN platform.datasets AS dataset
    ON dataset.id=version.dataset_id AND dataset.tenant_id=version.tenant_id
  JOIN platform.metadata_columns AS column_asset
    ON column_asset.tenant_id=dataset.tenant_id AND column_asset.table_id=dataset.origin_table_id
   AND column_asset.asset_status='ACTIVE'
   AND column_asset.column_name=COALESCE(field.expression_json->>'field',field.expression_json->>'column')
)
UPDATE platform.dataset_fields AS field
SET sensitivity_level=CASE
  WHEN physical.sensitivity_level='RESTRICTED' THEN 'RESTRICTED'::platform.asset_sensitivity
  WHEN physical.sensitivity_level='CONFIDENTIAL' AND field.sensitivity_level<>'RESTRICTED'
    THEN 'CONFIDENTIAL'::platform.asset_sensitivity
  WHEN physical.sensitivity_level='INTERNAL' AND field.sensitivity_level='PUBLIC'
    THEN 'INTERNAL'::platform.asset_sensitivity
  ELSE field.sensitivity_level END
FROM physical WHERE physical.id=field.id;

COMMENT ON COLUMN platform.dataset_fields.sensitivity_level IS
  'Governed field sensitivity derived from immutable DSL and source-column metadata; request callers cannot override it';

ALTER TABLE platform.data_request_events
  ADD COLUMN details_json jsonb NOT NULL DEFAULT '{}'::jsonb,
  ADD COLUMN event_type text NOT NULL DEFAULT 'STATE_TRANSITION' CHECK(event_type IN(
    'STATE_TRANSITION','EXPORT_ENQUEUED','EXPORT_DOWNLOADED'
  )),
  ADD COLUMN audit_no bigint,
  ADD CONSTRAINT platform_data_request_events_details_check CHECK(
    jsonb_typeof(details_json)='object' AND pg_column_size(details_json)<=16384
  );

UPDATE platform.data_request_events SET audit_no=sequence_no;
ALTER TABLE platform.data_request_events
  ALTER COLUMN audit_no SET NOT NULL,
  ALTER COLUMN sequence_no DROP NOT NULL,
  ADD CONSTRAINT platform_data_request_events_audit_no_check CHECK(audit_no>0),
  ADD CONSTRAINT platform_data_request_events_request_audit_key UNIQUE(data_request_id,audit_no);

CREATE TABLE platform.data_request_export_jobs(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id) ON DELETE RESTRICT,
  domain_id uuid NOT NULL,
  data_request_id uuid NOT NULL,
  requested_by uuid NOT NULL,
  request_hash text NOT NULL CHECK(request_hash~'^[0-9a-f]{64}$'),
  required_fields_json jsonb NOT NULL CHECK(platform.data_request_fields_valid(required_fields_json)),
  sensitivity_level platform.asset_sensitivity NOT NULL,
  state text NOT NULL DEFAULT 'PENDING' CHECK(state IN('PENDING','READY','FAILED','EXPIRED')),
  expires_at timestamptz NOT NULL,
  max_downloads integer NOT NULL CHECK(max_downloads BETWEEN 1 AND 20),
  download_count integer NOT NULL DEFAULT 0 CHECK(download_count>=0 AND download_count<=max_downloads),
  storage_key text NOT NULL DEFAULT '' CHECK(length(storage_key)<=500 AND storage_key!~'[[:cntrl:]]'),
  content_hash text CHECK(content_hash IS NULL OR content_hash~'^[0-9a-f]{64}$'),
  byte_size bigint CHECK(byte_size IS NULL OR byte_size>=0),
  failure_code text NOT NULL DEFAULT '' CHECK(length(failure_code)<=100 AND failure_code!~'[[:cntrl:]]'),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  ready_at timestamptz,
  CONSTRAINT platform_data_request_export_jobs_request_fk
    FOREIGN KEY(data_request_id,domain_id,tenant_id)
    REFERENCES platform.data_requests(id,domain_id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT platform_data_request_export_jobs_requester_fk FOREIGN KEY(requested_by,tenant_id)
    REFERENCES platform.users(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT platform_data_request_export_jobs_state_shape CHECK(
    (state='PENDING' AND storage_key='' AND content_hash IS NULL AND byte_size IS NULL AND ready_at IS NULL)
    OR (state='READY' AND storage_key<>'' AND content_hash IS NOT NULL AND byte_size IS NOT NULL AND ready_at IS NOT NULL)
    OR state IN('FAILED','EXPIRED')
  ),
  UNIQUE(id,tenant_id),
  UNIQUE(tenant_id,data_request_id,request_hash)
);
CREATE INDEX platform_data_request_export_jobs_queue_idx
  ON platform.data_request_export_jobs(tenant_id,state,created_at,id) WHERE state='PENDING';
CREATE INDEX platform_data_request_export_jobs_expiry_idx
  ON platform.data_request_export_jobs(tenant_id,expires_at,id) WHERE state IN('PENDING','READY');

ALTER TABLE platform.data_request_export_jobs ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform.data_request_export_jobs FORCE ROW LEVEL SECURITY;
CREATE POLICY platform_data_request_export_jobs_access ON platform.data_request_export_jobs
  USING(platform.data_request_event_can_access(tenant_id,domain_id,data_request_id))
  WITH CHECK(platform.data_request_event_can_access(tenant_id,domain_id,data_request_id));

CREATE OR REPLACE FUNCTION platform.derive_data_request_sensitivity(
  target_tenant_id uuid,
  target_domain_id uuid,
  target_source_run_id uuid,
  target_fields jsonb,
  target_context jsonb
)
RETURNS platform.asset_sensitivity
LANGUAGE plpgsql
STABLE
SECURITY DEFINER
SET search_path=pg_catalog,platform,askdata
AS $$
DECLARE expected_fields integer;
DECLARE matched_fields integer;
DECLARE expected_dimensions integer;
DECLARE matched_dimensions integer;
DECLARE maximum_rank integer := 0;
DECLARE dimension_rank integer := 0;
BEGIN
  IF jsonb_typeof(target_fields)<>'array' OR jsonb_array_length(target_fields)=0
    OR jsonb_typeof(target_context)<>'object' THEN
    RAISE EXCEPTION 'data request governed sensitivity inputs are invalid'
      USING ERRCODE='23514',CONSTRAINT='platform_data_requests_sensitivity_derived';
  END IF;
  expected_fields=jsonb_array_length(target_fields);
  SELECT count(*),COALESCE(max(CASE field.sensitivity_level
    WHEN 'PUBLIC' THEN 0 WHEN 'INTERNAL' THEN 1 WHEN 'CONFIDENTIAL' THEN 2
    WHEN 'RESTRICTED' THEN 3 END),0)
  INTO matched_fields,maximum_rank
  FROM jsonb_array_elements(target_fields) AS requested
  JOIN platform.dataset_fields AS field
    ON field.tenant_id=target_tenant_id
   AND field.dataset_version_id=(requested->>'datasetVersionId')::uuid
   AND field.field_id=requested->>'fieldId' AND field.visible
  JOIN platform.dataset_versions AS version
    ON version.id=field.dataset_version_id AND version.tenant_id=field.tenant_id
   AND version.status='PUBLISHED'
  JOIN platform.datasets AS dataset
    ON dataset.id=version.dataset_id AND dataset.tenant_id=version.tenant_id
   AND dataset.domain_id=target_domain_id AND dataset.deleted_at IS NULL;
  IF matched_fields<>expected_fields THEN
    RAISE EXCEPTION 'data request fields lack governed sensitivity'
      USING ERRCODE='23514',CONSTRAINT='platform_data_requests_sensitivity_derived';
  END IF;

  expected_dimensions=COALESCE(jsonb_array_length(target_context->'dimensionIds'),0);
  IF expected_dimensions>0 THEN
    IF target_source_run_id IS NULL THEN
      RAISE EXCEPTION 'data request dimensions require a source release'
        USING ERRCODE='23514',CONSTRAINT='platform_data_requests_sensitivity_derived';
    END IF;
    SELECT count(*),COALESCE(max(CASE dimension.sensitivity
      WHEN 'PUBLIC' THEN 0 WHEN 'INTERNAL' THEN 1 WHEN 'CONFIDENTIAL' THEN 2
      WHEN 'RESTRICTED' THEN 3 END),0)
    INTO matched_dimensions,dimension_rank
    FROM jsonb_array_elements_text(target_context->'dimensionIds') AS requested(dimension_id)
    JOIN askdata.question_runs AS run
      ON run.id=target_source_run_id AND run.tenant_id=target_tenant_id
     AND run.domain_id=target_domain_id
    JOIN askdata.release_objects AS released
      ON released.tenant_id=run.tenant_id AND released.domain_id=run.domain_id
     AND released.release_id=run.release_id AND released.object_type='DIMENSION'
     AND released.object_id=requested.dimension_id::uuid
    JOIN askdata.dimensions AS dimension
      ON dimension.id=released.object_version_id AND dimension.tenant_id=released.tenant_id
     AND dimension.domain_id=released.domain_id;
    IF matched_dimensions<>expected_dimensions THEN
      RAISE EXCEPTION 'data request dimensions lack governed sensitivity'
        USING ERRCODE='23514',CONSTRAINT='platform_data_requests_sensitivity_derived';
    END IF;
    maximum_rank=GREATEST(maximum_rank,dimension_rank);
  END IF;
  RETURN CASE maximum_rank WHEN 0 THEN 'PUBLIC'::platform.asset_sensitivity
    WHEN 1 THEN 'INTERNAL'::platform.asset_sensitivity
    WHEN 2 THEN 'CONFIDENTIAL'::platform.asset_sensitivity
    ELSE 'RESTRICTED'::platform.asset_sensitivity END;
END
$$;

DROP TRIGGER platform_data_requests_guard ON platform.data_requests;

UPDATE platform.data_requests AS request
SET sensitivity_level=platform.derive_data_request_sensitivity(
  request.tenant_id,request.domain_id,request.source_question_run_id,
  request.required_fields_json,request.parsed_context_json
);

CREATE OR REPLACE FUNCTION platform.guard_data_request_mutation()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,platform
AS $$
DECLARE actor_id uuid := platform.current_user_id();
DECLARE approver_valid boolean;
DECLARE derived_sensitivity platform.asset_sensitivity;
DECLARE cosigner_valid boolean;
BEGIN
  IF platform.is_system_access() THEN
    RETURN NEW;
  END IF;
  IF actor_id IS NULL OR NEW.tenant_id<>platform.current_tenant_id()
    OR NEW.domain_id<>platform.current_domain_id()
    OR NOT platform.user_has_active_domain_membership(NEW.domain_id) THEN
    RAISE EXCEPTION 'data request access context is invalid' USING ERRCODE='42501';
  END IF;
  derived_sensitivity=platform.derive_data_request_sensitivity(
    NEW.tenant_id,NEW.domain_id,NEW.source_question_run_id,
    NEW.required_fields_json,NEW.parsed_context_json
  );
  IF NEW.sensitivity_level<>derived_sensitivity THEN
    RAISE EXCEPTION 'data request sensitivity must be derived'
      USING ERRCODE='23514',CONSTRAINT='platform_data_requests_sensitivity_derived';
  END IF;
  IF TG_OP='INSERT' THEN
    IF NEW.requester_user_id<>actor_id OR NEW.state<>'DRAFT' OR NEW.record_version<>1
      OR NEW.security_cosign_user_id IS NOT NULL THEN
      RAISE EXCEPTION 'data request creation identity is invalid' USING ERRCODE='42501';
    END IF;
    SELECT bool_and(platform.data_request_actor_is_domain_admin(
      NEW.tenant_id,NEW.domain_id,approver_id
    )) INTO approver_valid
    FROM unnest(NEW.approver_user_ids) AS approver_id;
    IF NOT COALESCE(approver_valid,false) THEN
      RAISE EXCEPTION 'data request approver set is invalid' USING ERRCODE='23514';
    END IF;
    RETURN NEW;
  END IF;
  IF NEW.id<>OLD.id OR NEW.tenant_id<>OLD.tenant_id OR NEW.domain_id<>OLD.domain_id
    OR NEW.requester_user_id<>OLD.requester_user_id
    OR NEW.source_question_run_id IS DISTINCT FROM OLD.source_question_run_id
    OR NEW.request_text<>OLD.request_text OR NEW.parsed_context_json<>OLD.parsed_context_json
    OR NEW.business_purpose<>OLD.business_purpose
    OR NEW.required_fields_json<>OLD.required_fields_json
    OR NEW.sensitivity_level<>OLD.sensitivity_level
    OR NEW.approver_user_ids<>OLD.approver_user_ids
    OR NEW.sla_due_at<>OLD.sla_due_at OR NEW.created_at<>OLD.created_at
    OR NEW.record_version<>OLD.record_version+1 THEN
    RAISE EXCEPTION 'data request immutable facts changed' USING ERRCODE='23514';
  END IF;
  IF NEW.security_cosign_user_id IS DISTINCT FROM OLD.security_cosign_user_id
    AND NOT (OLD.state='SUBMITTED' AND NEW.state='APPROVED') THEN
    RAISE EXCEPTION 'data request security cosign changed outside approval'
      USING ERRCODE='23514',CONSTRAINT='platform_data_requests_security_cosign_required';
  END IF;
  IF OLD.state='SUBMITTED' AND NEW.state='APPROVED' THEN
    IF NEW.sensitivity_level IN ('CONFIDENTIAL','RESTRICTED')
      AND NEW.security_cosign_user_id IS NULL THEN
      RAISE EXCEPTION 'data request security cosign is required'
        USING ERRCODE='23514',CONSTRAINT='platform_data_requests_security_cosign_required';
    END IF;
    IF NEW.security_cosign_user_id IS NOT NULL THEN
      SELECT EXISTS(
        SELECT 1 FROM platform.domain_memberships AS membership
        JOIN platform.users AS user_account
          ON user_account.id=membership.user_id AND user_account.tenant_id=membership.tenant_id
        WHERE membership.tenant_id=NEW.tenant_id AND membership.domain_id=NEW.domain_id
          AND membership.user_id=NEW.security_cosign_user_id
          AND membership.status='ACTIVE' AND user_account.status='ACTIVE'
          AND user_account.deleted_at IS NULL
      ) INTO cosigner_valid;
      IF NOT cosigner_valid OR NEW.security_cosign_user_id=NEW.requester_user_id
        OR NEW.security_cosign_user_id=actor_id THEN
        RAISE EXCEPTION 'data request security cosign is not independent and active'
          USING ERRCODE='23514',CONSTRAINT='platform_data_requests_security_cosign_required';
      END IF;
    END IF;
  END IF;
  IF NOT (
    (OLD.state='DRAFT' AND NEW.state='SUBMITTED' AND actor_id=OLD.requester_user_id)
    OR (OLD.state='SUBMITTED' AND NEW.state IN ('APPROVED','REJECTED')
      AND actor_id=ANY(OLD.approver_user_ids))
    OR (OLD.state='APPROVED' AND NEW.state='IN_PROGRESS'
      AND actor_id=ANY(OLD.approver_user_ids))
    OR (OLD.state='IN_PROGRESS' AND NEW.state='DELIVERED'
      AND (actor_id=OLD.assignee_user_id OR actor_id=ANY(OLD.approver_user_ids)))
    OR (OLD.state='DELIVERED' AND NEW.state='CLOSED'
      AND (actor_id=OLD.requester_user_id OR actor_id=ANY(OLD.approver_user_ids)))
  ) THEN
    RAISE EXCEPTION 'data request transition is not permitted' USING ERRCODE='42501';
  END IF;
  RETURN NEW;
END
$$;

CREATE TRIGGER platform_data_requests_guard
BEFORE INSERT OR UPDATE ON platform.data_requests
FOR EACH ROW EXECUTE FUNCTION platform.guard_data_request_mutation();

DROP TRIGGER platform_data_request_events_guard ON platform.data_request_events;
CREATE OR REPLACE FUNCTION platform.guard_data_request_event()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,platform
AS $$
DECLARE current_state text;
DECLARE current_version bigint;
DECLARE latest_state text;
DECLARE latest_sequence bigint;
DECLARE latest_audit bigint;
DECLARE requester_id uuid;
DECLARE export_job_valid boolean;
BEGIN
  IF TG_OP<>'INSERT' THEN
    RAISE EXCEPTION 'data request events are append only' USING ERRCODE='23514';
  END IF;
  SELECT request.state,request.record_version,request.requester_user_id
  INTO current_state,current_version,requester_id
  FROM platform.data_requests AS request
  WHERE request.id=NEW.data_request_id AND request.tenant_id=NEW.tenant_id
    AND request.domain_id=NEW.domain_id
  FOR UPDATE;
  SELECT COALESCE(max(event.audit_no),0) INTO latest_audit
  FROM platform.data_request_events AS event
  WHERE event.data_request_id=NEW.data_request_id AND event.tenant_id=NEW.tenant_id;
  IF NEW.audit_no<>latest_audit+1
    OR NEW.actor_user_id IS DISTINCT FROM platform.current_user_id()
    OR NEW.to_state<>current_state THEN
    RAISE EXCEPTION 'data request audit event identity is invalid' USING ERRCODE='23514';
  END IF;
  IF NEW.event_type='STATE_TRANSITION' THEN
    SELECT event.to_state,event.sequence_no INTO latest_state,latest_sequence
    FROM platform.data_request_events AS event
    WHERE event.data_request_id=NEW.data_request_id AND event.tenant_id=NEW.tenant_id
      AND event.event_type='STATE_TRANSITION'
    ORDER BY event.sequence_no DESC LIMIT 1;
    IF NEW.sequence_no<>current_version
      OR (latest_sequence IS NULL AND (NEW.sequence_no<>1 OR NEW.from_state IS NOT NULL
        OR NEW.to_state<>'DRAFT' OR NEW.actor_user_id<>requester_id))
      OR (latest_sequence IS NOT NULL AND (NEW.sequence_no<>latest_sequence+1
        OR NEW.from_state IS NULL OR NEW.from_state<>latest_state OR NEW.to_state=NEW.from_state)) THEN
      RAISE EXCEPTION 'data request state event chain is invalid' USING ERRCODE='23514';
    END IF;
  ELSE
    IF NEW.sequence_no IS NOT NULL OR NEW.from_state IS NULL OR NEW.from_state<>NEW.to_state
      OR NOT (NEW.details_json ? 'exportJobId') THEN
      RAISE EXCEPTION 'data request export audit shape is invalid' USING ERRCODE='23514';
    END IF;
    SELECT EXISTS(
      SELECT 1 FROM platform.data_request_export_jobs AS export_job
      WHERE export_job.id=(NEW.details_json->>'exportJobId')::uuid
        AND export_job.tenant_id=NEW.tenant_id AND export_job.domain_id=NEW.domain_id
        AND export_job.data_request_id=NEW.data_request_id
    ) INTO export_job_valid;
    IF NOT export_job_valid THEN
      RAISE EXCEPTION 'data request export audit job is invalid' USING ERRCODE='23514';
    END IF;
  END IF;
  RETURN NEW;
END
$$;
CREATE TRIGGER platform_data_request_events_guard
BEFORE INSERT OR UPDATE OR DELETE ON platform.data_request_events
FOR EACH ROW EXECUTE FUNCTION platform.guard_data_request_event();

REVOKE ALL ON FUNCTION platform.derive_data_request_sensitivity(uuid,uuid,uuid,jsonb,jsonb) FROM PUBLIC;
REVOKE ALL ON FUNCTION platform.guard_data_request_mutation() FROM PUBLIC;
REVOKE ALL ON FUNCTION platform.guard_data_request_event() FROM PUBLIC;

COMMENT ON COLUMN platform.data_request_events.details_json IS
  'Append-only governed audit details: derived sensitivity, independent cosigner and controlled export job identity';

ALTER TABLE askdata.active_learning_candidates
  DROP CONSTRAINT active_learning_candidates_task_type_check,
  DROP CONSTRAINT active_learning_candidates_candidate_type_check,
  ADD CONSTRAINT active_learning_candidates_task_type_check CHECK(task_type IN(
    'UNRESOLVED_EXPRESSION','FREQUENT_CLARIFICATION','CONFUSABLE_METRIC',
    'CONFUSABLE_MEMBER','RETRIEVAL_MISS','REPORT_METRIC_COMBINATION',
    'FEEDBACK_CLUSTER','DATA_REQUEST_CLUSTER'
  )),
  ADD CONSTRAINT active_learning_candidates_candidate_type_check CHECK(candidate_type IN(
    'BUSINESS_TERM','NEGATIVE_CONTEXT','CERTIFIED_EXAMPLE','HARD_NEGATIVE',
    'SEARCH_DOCUMENT','KPI_BUNDLE','FIX_PRIORITY','SEMANTIC_ASSET'
  ));
