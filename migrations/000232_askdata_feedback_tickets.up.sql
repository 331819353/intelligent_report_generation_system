-- Governed feedback tickets and active-learning drafts. Feedback may propose
-- review work, but these tables have no path that can mutate an ACTIVE
-- semantic object.

CREATE TABLE askdata.feedback_tickets(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id) ON DELETE RESTRICT,
  domain_id uuid NOT NULL,
  query_feedback_id uuid NOT NULL,
  question_run_id uuid NOT NULL,
  reporter_user_id uuid NOT NULL,
  issue_type text NOT NULL CHECK(issue_type IN(
    'METRIC','DIMENSION','MEMBER','TIME','COMPARISON','RESULT','NARRATIVE',
    'UNDERSTANDING','PERMISSION','OTHER'
  )),
  severity text NOT NULL CHECK(severity IN('P0','P1','P2')),
  suggested_stage text NOT NULL CHECK(suggested_stage IN(
    'UNDERSTANDING','RETRIEVAL','BINDING','GRAPH','COMPILE','EXECUTION','DATA','NARRATIVE'
  )),
  attributed_stage text CHECK(attributed_stage IS NULL OR attributed_stage IN(
    'UNDERSTANDING','RETRIEVAL','BINDING','GRAPH','COMPILE','EXECUTION','DATA','NARRATIVE'
  )),
  status text NOT NULL DEFAULT 'NEW' CHECK(status IN(
    'NEW','TRIAGED','ACCEPTED','REJECTED','FIX_PROPOSED','FIX_APPROVED',
    'IN_RELEASE','VERIFIED','CLOSED'
  )),
  owner_user_id uuid,
  sla_due_at timestamptz NOT NULL,
  linked_release_id uuid,
  linked_evaluation_case_id uuid,
  resolution_note text NOT NULL DEFAULT '' CHECK(length(resolution_note)<=4000 AND resolution_note !~ '[[:cntrl:]]'),
  user_response text NOT NULL DEFAULT '' CHECK(length(user_response)<=4000 AND user_response !~ '[[:cntrl:]]'),
  fix_candidate_type text CHECK(fix_candidate_type IS NULL OR fix_candidate_type IN(
    'BUSINESS_TERM','CERTIFIED_EXAMPLE','METRIC','DIMENSION','MEMBER_ALIAS','RELATIONSHIP','EVALUATION_CASE'
  )),
  fix_candidate_id uuid,
  fix_candidate_state text CHECK(fix_candidate_state IS NULL OR fix_candidate_state='DRAFT'),
  record_version bigint NOT NULL DEFAULT 1 CHECK(record_version>0),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  closed_at timestamptz,
  UNIQUE(id,tenant_id),
  UNIQUE(tenant_id,query_feedback_id),
  FOREIGN KEY(query_feedback_id,tenant_id) REFERENCES askdata.query_feedback(id,tenant_id) ON DELETE RESTRICT,
  FOREIGN KEY(question_run_id,tenant_id) REFERENCES askdata.question_runs(id,tenant_id) ON DELETE RESTRICT,
  FOREIGN KEY(reporter_user_id,tenant_id) REFERENCES platform.users(id,tenant_id) ON DELETE RESTRICT,
  FOREIGN KEY(owner_user_id,tenant_id) REFERENCES platform.users(id,tenant_id) ON DELETE RESTRICT,
  FOREIGN KEY(linked_release_id) REFERENCES askdata.releases(id) ON DELETE RESTRICT,
  FOREIGN KEY(linked_evaluation_case_id) REFERENCES askdata.evaluation_cases(id) ON DELETE RESTRICT,
  CHECK((fix_candidate_type IS NULL)=(fix_candidate_id IS NULL) AND (fix_candidate_id IS NULL)=(fix_candidate_state IS NULL)),
  CHECK((status='CLOSED')=(closed_at IS NOT NULL)),
  CHECK(status NOT IN('TRIAGED','ACCEPTED','FIX_PROPOSED','FIX_APPROVED','IN_RELEASE','VERIFIED','CLOSED') OR owner_user_id IS NOT NULL),
  CHECK(status<>'REJECTED' OR (length(btrim(resolution_note))>0 AND length(btrim(user_response))>0)),
  CHECK(status<>'FIX_PROPOSED' OR fix_candidate_id IS NOT NULL),
  CHECK(status NOT IN('IN_RELEASE','VERIFIED','CLOSED') OR linked_release_id IS NOT NULL),
  CHECK(status NOT IN('VERIFIED','CLOSED') OR linked_evaluation_case_id IS NOT NULL)
);

CREATE TABLE askdata.feedback_ticket_events(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id) ON DELETE RESTRICT,
  ticket_id uuid NOT NULL,
  event_no bigint NOT NULL CHECK(event_no>0),
  from_status text,
  to_status text NOT NULL CHECK(to_status IN(
    'NEW','TRIAGED','ACCEPTED','REJECTED','FIX_PROPOSED','FIX_APPROVED',
    'IN_RELEASE','VERIFIED','CLOSED'
  )),
  actor_user_id uuid NOT NULL,
  details_json jsonb NOT NULL DEFAULT '{}'::jsonb CHECK(jsonb_typeof(details_json)='object' AND pg_column_size(details_json)<=65536),
  created_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE(tenant_id,ticket_id,event_no),
  FOREIGN KEY(ticket_id,tenant_id) REFERENCES askdata.feedback_tickets(id,tenant_id) ON DELETE RESTRICT,
  FOREIGN KEY(actor_user_id,tenant_id) REFERENCES platform.users(id,tenant_id) ON DELETE RESTRICT
);

CREATE TABLE askdata.active_learning_candidates(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id) ON DELETE RESTRICT,
  domain_id uuid NOT NULL,
  task_type text NOT NULL CHECK(task_type IN(
    'UNRESOLVED_EXPRESSION','FREQUENT_CLARIFICATION','CONFUSABLE_METRIC',
    'CONFUSABLE_MEMBER','RETRIEVAL_MISS','REPORT_METRIC_COMBINATION','FEEDBACK_CLUSTER'
  )),
  candidate_key_hash text NOT NULL CHECK(candidate_key_hash ~ '^[0-9a-f]{64}$'),
  candidate_type text NOT NULL CHECK(candidate_type IN(
    'BUSINESS_TERM','NEGATIVE_CONTEXT','CERTIFIED_EXAMPLE','HARD_NEGATIVE',
    'SEARCH_DOCUMENT','KPI_BUNDLE','FIX_PRIORITY'
  )),
  candidate_state text NOT NULL DEFAULT 'DRAFT' CHECK(candidate_state='DRAFT'),
  review_status text NOT NULL DEFAULT 'PENDING' CHECK(review_status IN('PENDING','APPROVED','REJECTED')),
  normalized_summary_json jsonb NOT NULL CHECK(jsonb_typeof(normalized_summary_json)='object' AND pg_column_size(normalized_summary_json)<=65536),
  evidence_json jsonb NOT NULL CHECK(jsonb_typeof(evidence_json)='object' AND pg_column_size(evidence_json)<=131072),
  occurrence_count bigint NOT NULL CHECK(occurrence_count>0),
  representative_run_ids uuid[] NOT NULL DEFAULT '{}'::uuid[] CHECK(cardinality(representative_run_ids)<=20 AND array_position(representative_run_ids,NULL) IS NULL),
  first_seen_at timestamptz NOT NULL,
  last_seen_at timestamptz NOT NULL,
  rejected_at timestamptz,
  reviewed_by uuid,
  reviewed_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE(id,tenant_id),
  UNIQUE(tenant_id,domain_id,task_type,candidate_key_hash),
  FOREIGN KEY(domain_id,tenant_id) REFERENCES askdata.domains(id,tenant_id) ON DELETE RESTRICT,
  FOREIGN KEY(reviewed_by,tenant_id) REFERENCES platform.users(id,tenant_id) ON DELETE RESTRICT,
  CHECK(first_seen_at<=last_seen_at),
  CHECK((review_status='REJECTED')=(rejected_at IS NOT NULL)),
  CHECK((reviewed_by IS NULL AND reviewed_at IS NULL) OR (reviewed_by IS NOT NULL AND reviewed_at IS NOT NULL))
);

CREATE INDEX askdata_feedback_ticket_queue_idx ON askdata.feedback_tickets(tenant_id,domain_id,status,severity,sla_due_at);
CREATE INDEX askdata_feedback_ticket_sla_idx ON askdata.feedback_tickets(tenant_id,sla_due_at) WHERE status NOT IN('REJECTED','CLOSED');
CREATE INDEX askdata_feedback_ticket_events_idx ON askdata.feedback_ticket_events(tenant_id,ticket_id,event_no);
CREATE INDEX askdata_active_learning_queue_idx ON askdata.active_learning_candidates(tenant_id,domain_id,review_status,task_type,occurrence_count DESC);

CREATE OR REPLACE FUNCTION askdata.feedback_ticket_transition_allowed(previous text, next text)
RETURNS boolean LANGUAGE sql IMMUTABLE AS $$
  SELECT CASE previous
    WHEN 'NEW' THEN next='TRIAGED'
    WHEN 'TRIAGED' THEN next IN('ACCEPTED','REJECTED')
    WHEN 'ACCEPTED' THEN next='FIX_PROPOSED'
    WHEN 'FIX_PROPOSED' THEN next='FIX_APPROVED'
    WHEN 'FIX_APPROVED' THEN next='IN_RELEASE'
    WHEN 'IN_RELEASE' THEN next='VERIFIED'
    WHEN 'VERIFIED' THEN next='CLOSED'
    ELSE false END
$$;

CREATE OR REPLACE FUNCTION askdata.guard_feedback_ticket()
RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,askdata AS $$
BEGIN
  IF TG_OP='INSERT' THEN
    IF NEW.status<>'NEW' OR NEW.record_version<>1 OR NEW.closed_at IS NOT NULL THEN
      RAISE EXCEPTION 'feedback ticket initial state is invalid' USING ERRCODE='23514';
    END IF;
    NEW.created_at=clock_timestamp(); NEW.updated_at=NEW.created_at;
    RETURN NEW;
  END IF;
  IF TG_OP='DELETE' THEN
    RAISE EXCEPTION 'feedback tickets are retained audit records' USING ERRCODE='55000';
  END IF;
  IF ROW(NEW.id,NEW.tenant_id,NEW.domain_id,NEW.query_feedback_id,NEW.question_run_id,
    NEW.reporter_user_id,NEW.issue_type,NEW.suggested_stage,NEW.created_at)
    IS DISTINCT FROM ROW(OLD.id,OLD.tenant_id,OLD.domain_id,OLD.query_feedback_id,OLD.question_run_id,
    OLD.reporter_user_id,OLD.issue_type,OLD.suggested_stage,OLD.created_at) THEN
    RAISE EXCEPTION 'feedback ticket source identity is immutable' USING ERRCODE='55000';
  END IF;
  IF NEW.record_version<>OLD.record_version+1 THEN
    RAISE EXCEPTION 'feedback ticket record version conflict' USING ERRCODE='40001';
  END IF;
  IF NEW.status<>OLD.status AND NOT askdata.feedback_ticket_transition_allowed(OLD.status,NEW.status) THEN
    RAISE EXCEPTION 'illegal feedback ticket transition' USING ERRCODE='23514';
  END IF;
  IF NEW.status=OLD.status AND ROW(NEW.severity,NEW.attributed_stage,NEW.owner_user_id,
    NEW.resolution_note,NEW.user_response,NEW.fix_candidate_type,NEW.fix_candidate_id,NEW.fix_candidate_state,
    NEW.linked_release_id,NEW.linked_evaluation_case_id)
    IS NOT DISTINCT FROM ROW(OLD.severity,OLD.attributed_stage,OLD.owner_user_id,
    OLD.resolution_note,OLD.user_response,OLD.fix_candidate_type,OLD.fix_candidate_id,OLD.fix_candidate_state,
    OLD.linked_release_id,OLD.linked_evaluation_case_id) THEN
    RAISE EXCEPTION 'feedback ticket update made no progress' USING ERRCODE='23514';
  END IF;
  IF NEW.status='CLOSED' THEN NEW.closed_at=clock_timestamp(); ELSE NEW.closed_at=NULL; END IF;
  NEW.updated_at=clock_timestamp();
  RETURN NEW;
END
$$;

CREATE OR REPLACE FUNCTION askdata.guard_feedback_ticket_event()
RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,askdata AS $$
DECLARE expected_no bigint; current_status text;
BEGIN
  IF TG_OP<>'INSERT' THEN
    RAISE EXCEPTION 'feedback ticket events are append only' USING ERRCODE='55000';
  END IF;
  SELECT status INTO current_status FROM askdata.feedback_tickets
    WHERE id=NEW.ticket_id AND tenant_id=NEW.tenant_id FOR SHARE;
  SELECT COALESCE(max(event_no),0)+1 INTO expected_no FROM askdata.feedback_ticket_events
    WHERE ticket_id=NEW.ticket_id AND tenant_id=NEW.tenant_id;
  IF NEW.event_no<>expected_no OR NEW.to_status<>current_status THEN
    RAISE EXCEPTION 'feedback ticket event sequence is invalid' USING ERRCODE='23514';
  END IF;
  RETURN NEW;
END
$$;

CREATE TRIGGER askdata_feedback_ticket_guard BEFORE INSERT OR UPDATE OR DELETE ON askdata.feedback_tickets
FOR EACH ROW EXECUTE FUNCTION askdata.guard_feedback_ticket();
CREATE TRIGGER askdata_feedback_ticket_event_guard BEFORE INSERT OR UPDATE OR DELETE ON askdata.feedback_ticket_events
FOR EACH ROW EXECUTE FUNCTION askdata.guard_feedback_ticket_event();

CREATE OR REPLACE FUNCTION askdata.feedback_ticket_can_access(selected_tenant uuid, selected_domain uuid, reporter uuid, owner uuid)
RETURNS boolean LANGUAGE sql STABLE SECURITY DEFINER SET search_path=pg_catalog,platform,askdata AS $$
  SELECT askdata.tenant_matches(selected_tenant) AND askdata.domain_can_access(selected_domain)
    AND (askdata.system_access() OR reporter=askdata.current_actor_id() OR owner=askdata.current_actor_id()
      OR platform.user_is_domain_administrator(selected_domain) OR platform.user_is_platform_administrator())
$$;

ALTER TABLE askdata.feedback_tickets ENABLE ROW LEVEL SECURITY;
ALTER TABLE askdata.feedback_tickets FORCE ROW LEVEL SECURITY;
CREATE POLICY askdata_feedback_tickets_policy ON askdata.feedback_tickets
  USING(askdata.feedback_ticket_can_access(tenant_id,domain_id,reporter_user_id,owner_user_id))
  WITH CHECK(askdata.feedback_ticket_can_access(tenant_id,domain_id,reporter_user_id,owner_user_id));
ALTER TABLE askdata.feedback_ticket_events ENABLE ROW LEVEL SECURITY;
ALTER TABLE askdata.feedback_ticket_events FORCE ROW LEVEL SECURITY;
CREATE POLICY askdata_feedback_ticket_events_policy ON askdata.feedback_ticket_events
  USING(EXISTS(SELECT 1 FROM askdata.feedback_tickets AS ticket
    WHERE ticket.id=feedback_ticket_events.ticket_id
      AND ticket.tenant_id=feedback_ticket_events.tenant_id));
ALTER TABLE askdata.active_learning_candidates ENABLE ROW LEVEL SECURITY;
ALTER TABLE askdata.active_learning_candidates FORCE ROW LEVEL SECURITY;
CREATE POLICY askdata_active_learning_policy ON askdata.active_learning_candidates
  USING(askdata.evaluation_control_can_access(tenant_id,domain_id))
  WITH CHECK(askdata.evaluation_control_can_access(tenant_id,domain_id));

COMMENT ON TABLE askdata.feedback_tickets IS 'Governed feedback lifecycle; suggested and human attribution are retained independently';
COMMENT ON TABLE askdata.active_learning_candidates IS 'Deduplicated DRAFT/PENDING review candidates; never an ACTIVE semantic write path';
