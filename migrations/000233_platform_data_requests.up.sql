-- Platform-native detail data request workflow. The request contains governed
-- object/field references and business intent, never result rows.
CREATE OR REPLACE FUNCTION platform.data_request_context_valid(document jsonb)
RETURNS boolean
LANGUAGE plpgsql
IMMUTABLE
SET search_path=pg_catalog,platform
AS $$
DECLARE key_name text;
DECLARE item jsonb;
DECLARE range_value jsonb;
BEGIN
  IF jsonb_typeof(document)<>'object' OR pg_column_size(document)>32768 THEN
    RETURN false;
  END IF;
  FOR key_name IN SELECT jsonb_object_keys(document)
  LOOP
    IF key_name NOT IN ('metricIds','dimensionIds','memberIds','timeRange') THEN
      RETURN false;
    END IF;
  END LOOP;
  FOREACH key_name IN ARRAY ARRAY['metricIds','dimensionIds','memberIds']
  LOOP
    IF document ? key_name THEN
      IF jsonb_typeof(document->key_name)<>'array'
        OR jsonb_array_length(document->key_name)>
          (CASE key_name WHEN 'metricIds' THEN 20 WHEN 'dimensionIds' THEN 30 ELSE 50 END) THEN
        RETURN false;
      END IF;
      FOR item IN SELECT value FROM jsonb_array_elements(document->key_name)
      LOOP
        IF jsonb_typeof(item)<>'string'
          OR NOT (item#>>'{}' ~* '^[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$') THEN
          RETURN false;
        END IF;
      END LOOP;
      IF (SELECT count(*) FROM jsonb_array_elements(document->key_name)) <>
         (SELECT count(DISTINCT value) FROM jsonb_array_elements(document->key_name)) THEN
        RETURN false;
      END IF;
    END IF;
  END LOOP;
  IF document ? 'timeRange' THEN
    range_value := document->'timeRange';
    IF jsonb_typeof(range_value)<>'object'
      OR NOT (range_value ?& ARRAY['start','endExclusive','timezone'])
      OR EXISTS(
        SELECT 1 FROM jsonb_object_keys(range_value) AS keys(key_value)
        WHERE key_value NOT IN ('start','endExclusive','timezone','grain')
      )
      OR jsonb_typeof(range_value->'start')<>'string'
      OR jsonb_typeof(range_value->'endExclusive')<>'string'
      OR jsonb_typeof(range_value->'timezone')<>'string'
      OR length(range_value->>'timezone') NOT BETWEEN 1 AND 64
      OR COALESCE(range_value->>'grain','') NOT IN (
        '','DAY','WEEK','MONTH','QUARTER','YEAR',
        'FISCAL_MONTH','FISCAL_QUARTER','FISCAL_YEAR'
      ) THEN
      RETURN false;
    END IF;
    BEGIN
      IF (range_value->>'endExclusive')::timestamptz <=
         (range_value->>'start')::timestamptz
        OR (range_value->>'endExclusive')::timestamptz -
           (range_value->>'start')::timestamptz > interval '10 years' THEN
        RETURN false;
      END IF;
    EXCEPTION WHEN others THEN
      RETURN false;
    END;
  END IF;
  RETURN true;
END
$$;

CREATE OR REPLACE FUNCTION platform.data_request_fields_valid(document jsonb)
RETURNS boolean
LANGUAGE plpgsql
IMMUTABLE
SET search_path=pg_catalog,platform
AS $$
DECLARE item jsonb;
BEGIN
  IF jsonb_typeof(document)<>'array' OR jsonb_array_length(document) NOT BETWEEN 1 AND 100
    OR pg_column_size(document)>65536 THEN
    RETURN false;
  END IF;
  FOR item IN SELECT value FROM jsonb_array_elements(document)
  LOOP
    IF jsonb_typeof(item)<>'object'
      OR NOT (item ?& ARRAY['datasetVersionId','fieldId'])
      OR (SELECT count(*) FROM jsonb_object_keys(item))<>2
      OR jsonb_typeof(item->'datasetVersionId')<>'string'
      OR jsonb_typeof(item->'fieldId')<>'string'
      OR NOT (item->>'datasetVersionId' ~* '^[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$')
      OR length(item->>'fieldId') NOT BETWEEN 1 AND 128
      OR item->>'fieldId' ~ '[[:cntrl:]]' THEN
      RETURN false;
    END IF;
  END LOOP;
  IF (SELECT count(*) FROM jsonb_array_elements(document)) <>
     (SELECT count(DISTINCT value) FROM jsonb_array_elements(document)) THEN
    RETURN false;
  END IF;
  RETURN true;
END
$$;

CREATE OR REPLACE FUNCTION platform.data_request_actor_is_domain_admin(
  selected_tenant_id uuid,
  selected_domain_id uuid,
  selected_user_id uuid
)
RETURNS boolean
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path=pg_catalog,platform
AS $$
  SELECT EXISTS(
    SELECT 1 FROM platform.domain_memberships AS membership
    JOIN platform.business_domains AS domain
      ON domain.id=membership.domain_id AND domain.tenant_id=membership.tenant_id
    JOIN platform.users AS user_account
      ON user_account.id=membership.user_id AND user_account.tenant_id=membership.tenant_id
    WHERE membership.tenant_id=selected_tenant_id
      AND membership.domain_id=selected_domain_id
      AND membership.user_id=selected_user_id
      AND membership.member_role='DOMAIN_ADMIN'
      AND membership.status='ACTIVE'
      AND domain.status='ACTIVE' AND domain.deleted_at IS NULL
      AND user_account.status='ACTIVE' AND user_account.deleted_at IS NULL
  ) OR EXISTS(
    SELECT 1 FROM platform.user_roles AS assignment
    JOIN platform.roles AS role
      ON role.id=assignment.role_id AND role.tenant_id=assignment.tenant_id
    WHERE assignment.tenant_id=selected_tenant_id
      AND assignment.user_id=selected_user_id
      AND role.code::text IN ('platform_admin','tenant_admin')
      AND role.status='ACTIVE' AND role.deleted_at IS NULL
  )
$$;

CREATE OR REPLACE FUNCTION platform.data_request_can_access(
  selected_tenant_id uuid,
  selected_domain_id uuid,
  requester_id uuid,
  approver_ids uuid[],
  cosign_id uuid,
  assignee_id uuid
)
RETURNS boolean
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path=pg_catalog,platform
AS $$
  SELECT selected_tenant_id=platform.current_tenant_id()
    AND (
      platform.is_system_access()
      OR (
        selected_domain_id=platform.current_domain_id()
        AND platform.user_has_active_domain_membership(selected_domain_id)
        AND (
          requester_id=platform.current_user_id()
          OR platform.current_user_id()=ANY(approver_ids)
          OR cosign_id=platform.current_user_id()
          OR assignee_id=platform.current_user_id()
          OR platform.data_request_actor_is_domain_admin(
            selected_tenant_id,selected_domain_id,platform.current_user_id()
          )
        )
      )
    )
$$;

CREATE TABLE platform.data_requests(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id) ON DELETE RESTRICT,
  domain_id uuid NOT NULL,
  requester_user_id uuid NOT NULL,
  source_question_run_id uuid,
  request_text text NOT NULL CHECK(
    length(btrim(request_text)) BETWEEN 1 AND 4096
    AND request_text !~ '[[:cntrl:]]'
  ),
  parsed_context_json jsonb NOT NULL DEFAULT '{}'::jsonb
    CHECK(platform.data_request_context_valid(parsed_context_json)),
  business_purpose text NOT NULL CHECK(
    length(btrim(business_purpose)) BETWEEN 1 AND 2000
    AND business_purpose !~ '[[:cntrl:]]'
  ),
  required_fields_json jsonb NOT NULL
    CHECK(platform.data_request_fields_valid(required_fields_json)),
  sensitivity_level platform.asset_sensitivity NOT NULL DEFAULT 'INTERNAL',
  state text NOT NULL DEFAULT 'DRAFT' CHECK(state IN (
    'DRAFT','SUBMITTED','APPROVED','REJECTED','IN_PROGRESS','DELIVERED','CLOSED'
  )),
  approver_user_ids uuid[] NOT NULL CHECK(cardinality(approver_user_ids)>0),
  security_cosign_user_id uuid,
  assignee_user_id uuid,
  sla_due_at timestamptz NOT NULL,
  delivery_type text CHECK(delivery_type IS NULL OR delivery_type IN (
    'EXISTING_REPORT','NEW_DATASET','ONE_TIME_EXPORT'
  )),
  delivery_ref text NOT NULL DEFAULT '' CHECK(
    length(delivery_ref)<=500 AND delivery_ref !~ '[[:cntrl:]]'
  ),
  status_note text NOT NULL DEFAULT '' CHECK(
    length(status_note)<=2000 AND status_note !~ '[[:cntrl:]]'
  ),
  record_version bigint NOT NULL DEFAULT 1 CHECK(record_version>0),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  submitted_at timestamptz,
  approved_at timestamptz,
  rejected_at timestamptz,
  started_at timestamptz,
  delivered_at timestamptz,
  closed_at timestamptz,
  CONSTRAINT platform_data_requests_identity_tenant_key UNIQUE(id,tenant_id),
  CONSTRAINT platform_data_requests_identity_domain_tenant_key UNIQUE(id,domain_id,tenant_id),
  CONSTRAINT platform_data_requests_domain_fk FOREIGN KEY(domain_id,tenant_id)
    REFERENCES platform.business_domains(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT platform_data_requests_requester_fk FOREIGN KEY(requester_user_id,tenant_id)
    REFERENCES platform.users(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT platform_data_requests_source_actor_fk
    FOREIGN KEY(source_question_run_id,requester_user_id,tenant_id)
    REFERENCES askdata.question_runs(id,actor_id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT platform_data_requests_source_domain_fk
    FOREIGN KEY(source_question_run_id,domain_id,tenant_id)
    REFERENCES askdata.question_runs(id,domain_id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT platform_data_requests_cosign_fk FOREIGN KEY(security_cosign_user_id,tenant_id)
    REFERENCES platform.users(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT platform_data_requests_assignee_fk FOREIGN KEY(assignee_user_id,tenant_id)
    REFERENCES platform.users(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT platform_data_requests_source_context_check CHECK(
    source_question_run_id IS NOT NULL OR parsed_context_json='{}'::jsonb
  ),
  CONSTRAINT platform_data_requests_state_shape_check CHECK(
    (state='DRAFT' AND submitted_at IS NULL AND approved_at IS NULL AND rejected_at IS NULL
      AND started_at IS NULL AND delivered_at IS NULL AND closed_at IS NULL
      AND assignee_user_id IS NULL AND delivery_type IS NULL AND delivery_ref='')
    OR (state='SUBMITTED' AND submitted_at IS NOT NULL AND approved_at IS NULL AND rejected_at IS NULL
      AND started_at IS NULL AND delivered_at IS NULL AND closed_at IS NULL
      AND assignee_user_id IS NULL AND delivery_type IS NULL AND delivery_ref='')
    OR (state='APPROVED' AND submitted_at IS NOT NULL AND approved_at IS NOT NULL AND rejected_at IS NULL
      AND started_at IS NULL AND delivered_at IS NULL AND closed_at IS NULL
      AND assignee_user_id IS NULL AND delivery_type IS NULL AND delivery_ref='')
    OR (state='REJECTED' AND submitted_at IS NOT NULL AND rejected_at IS NOT NULL
      AND approved_at IS NULL AND started_at IS NULL AND delivered_at IS NULL AND closed_at IS NULL
      AND status_note<>'' AND assignee_user_id IS NULL AND delivery_type IS NULL AND delivery_ref='')
    OR (state='IN_PROGRESS' AND submitted_at IS NOT NULL AND approved_at IS NOT NULL
      AND started_at IS NOT NULL AND assignee_user_id IS NOT NULL
      AND rejected_at IS NULL AND delivered_at IS NULL AND closed_at IS NULL
      AND delivery_type IS NULL AND delivery_ref='')
    OR (state='DELIVERED' AND submitted_at IS NOT NULL AND approved_at IS NOT NULL
      AND started_at IS NOT NULL AND delivered_at IS NOT NULL AND assignee_user_id IS NOT NULL
      AND rejected_at IS NULL AND closed_at IS NULL
      AND delivery_type IS NOT NULL AND delivery_ref<>'')
    OR (state='CLOSED' AND submitted_at IS NOT NULL AND approved_at IS NOT NULL
      AND started_at IS NOT NULL AND delivered_at IS NOT NULL AND closed_at IS NOT NULL
      AND assignee_user_id IS NOT NULL AND rejected_at IS NULL
      AND delivery_type IS NOT NULL AND delivery_ref<>'')
  )
);

CREATE TABLE platform.data_request_events(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id) ON DELETE RESTRICT,
  domain_id uuid NOT NULL,
  data_request_id uuid NOT NULL,
  from_state text CHECK(from_state IS NULL OR from_state IN (
    'DRAFT','SUBMITTED','APPROVED','REJECTED','IN_PROGRESS','DELIVERED','CLOSED'
  )),
  to_state text NOT NULL CHECK(to_state IN (
    'DRAFT','SUBMITTED','APPROVED','REJECTED','IN_PROGRESS','DELIVERED','CLOSED'
  )),
  actor_user_id uuid NOT NULL,
  note text NOT NULL DEFAULT '' CHECK(length(note)<=2000 AND note !~ '[[:cntrl:]]'),
  created_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT platform_data_request_events_request_fk
    FOREIGN KEY(data_request_id,domain_id,tenant_id)
    REFERENCES platform.data_requests(id,domain_id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT platform_data_request_events_actor_fk FOREIGN KEY(actor_user_id,tenant_id)
    REFERENCES platform.users(id,tenant_id) ON DELETE RESTRICT
);

CREATE INDEX platform_data_requests_requester_recent_idx
  ON platform.data_requests(tenant_id,domain_id,requester_user_id,updated_at DESC,id);
CREATE INDEX platform_data_requests_actionable_idx
  ON platform.data_requests(tenant_id,domain_id,state,sla_due_at,id)
  WHERE state NOT IN ('REJECTED','CLOSED');
CREATE INDEX platform_data_request_events_request_idx
  ON platform.data_request_events(tenant_id,domain_id,data_request_id,created_at,id);

CREATE OR REPLACE FUNCTION platform.guard_data_request_mutation()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,platform
AS $$
DECLARE actor_id uuid := platform.current_user_id();
DECLARE approver_valid boolean;
BEGIN
  IF platform.is_system_access() THEN
    RETURN NEW;
  END IF;
  IF actor_id IS NULL OR NEW.tenant_id<>platform.current_tenant_id()
    OR NEW.domain_id<>platform.current_domain_id()
    OR NOT platform.user_has_active_domain_membership(NEW.domain_id) THEN
    RAISE EXCEPTION 'data request access context is invalid' USING ERRCODE='42501';
  END IF;
  IF TG_OP='INSERT' THEN
    IF NEW.requester_user_id<>actor_id OR NEW.state<>'DRAFT' OR NEW.record_version<>1 THEN
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
    OR NEW.security_cosign_user_id IS DISTINCT FROM OLD.security_cosign_user_id
    OR NEW.sla_due_at<>OLD.sla_due_at OR NEW.created_at<>OLD.created_at
    OR NEW.record_version<>OLD.record_version+1 THEN
    RAISE EXCEPTION 'data request immutable facts changed' USING ERRCODE='23514';
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

CREATE OR REPLACE FUNCTION platform.guard_data_request_event()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,platform
AS $$
DECLARE current_state text;
DECLARE latest_state text;
DECLARE requester_id uuid;
BEGIN
  IF TG_OP<>'INSERT' THEN
    RAISE EXCEPTION 'data request events are append only' USING ERRCODE='23514';
  END IF;
  SELECT request.state,request.requester_user_id
  INTO current_state,requester_id
  FROM platform.data_requests AS request
  WHERE request.id=NEW.data_request_id AND request.tenant_id=NEW.tenant_id
    AND request.domain_id=NEW.domain_id;
  SELECT event.to_state INTO latest_state
  FROM platform.data_request_events AS event
  WHERE event.data_request_id=NEW.data_request_id AND event.tenant_id=NEW.tenant_id
  ORDER BY event.created_at DESC,event.id DESC LIMIT 1;
  IF NEW.to_state<>current_state OR NEW.actor_user_id IS DISTINCT FROM platform.current_user_id()
    OR (latest_state IS NULL AND (NEW.from_state IS NOT NULL OR NEW.to_state<>'DRAFT'
      OR NEW.actor_user_id<>requester_id))
    OR (latest_state IS NOT NULL AND (NEW.from_state IS NULL OR NEW.from_state<>latest_state
      OR NEW.to_state=NEW.from_state)) THEN
    RAISE EXCEPTION 'data request event chain is invalid' USING ERRCODE='23514';
  END IF;
  RETURN NEW;
END
$$;

CREATE OR REPLACE FUNCTION platform.data_request_event_can_access(
  selected_tenant_id uuid,
  selected_domain_id uuid,
  selected_request_id uuid
)
RETURNS boolean
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path=pg_catalog,platform
AS $$
  SELECT EXISTS(
    SELECT 1 FROM platform.data_requests AS request
    WHERE request.id=selected_request_id
      AND request.tenant_id=selected_tenant_id
      AND request.domain_id=selected_domain_id
      AND platform.data_request_can_access(
        request.tenant_id,request.domain_id,request.requester_user_id,
        request.approver_user_ids,request.security_cosign_user_id,request.assignee_user_id
      )
  )
$$;

CREATE TRIGGER platform_data_requests_guard
BEFORE INSERT OR UPDATE ON platform.data_requests
FOR EACH ROW EXECUTE FUNCTION platform.guard_data_request_mutation();
CREATE TRIGGER platform_data_request_events_guard
BEFORE INSERT OR UPDATE OR DELETE ON platform.data_request_events
FOR EACH ROW EXECUTE FUNCTION platform.guard_data_request_event();

ALTER TABLE platform.data_requests ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform.data_requests FORCE ROW LEVEL SECURITY;
ALTER TABLE platform.data_request_events ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform.data_request_events FORCE ROW LEVEL SECURITY;

CREATE POLICY platform_data_requests_access ON platform.data_requests
  USING(platform.data_request_can_access(
    tenant_id,domain_id,requester_user_id,approver_user_ids,
    security_cosign_user_id,assignee_user_id
  ))
  WITH CHECK(platform.data_request_can_access(
    tenant_id,domain_id,requester_user_id,approver_user_ids,
    security_cosign_user_id,assignee_user_id
  ));
CREATE POLICY platform_data_request_events_access ON platform.data_request_events
  USING(platform.data_request_event_can_access(tenant_id,domain_id,data_request_id))
  WITH CHECK(platform.data_request_event_can_access(tenant_id,domain_id,data_request_id));

REVOKE ALL ON FUNCTION
  platform.data_request_context_valid(jsonb),
  platform.data_request_fields_valid(jsonb),
  platform.data_request_actor_is_domain_admin(uuid,uuid,uuid),
  platform.data_request_can_access(uuid,uuid,uuid,uuid[],uuid,uuid),
  platform.data_request_event_can_access(uuid,uuid,uuid),
  platform.guard_data_request_mutation(),
  platform.guard_data_request_event()
FROM PUBLIC;

COMMENT ON TABLE platform.data_requests IS
  'Tenant/domain-scoped detail data request workflow; parsed context stores governed IDs and time only';
COMMENT ON TABLE platform.data_request_events IS
  'Append-only state transition audit for detail data requests';
