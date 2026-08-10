-- P15 / M20: governed decision support. This schema stores immutable analysis
-- references, approvals, action events and outcome reviews. It does not store
-- result rows and is not an external transaction executor.
CREATE SCHEMA decision;
REVOKE ALL ON SCHEMA decision FROM PUBLIC;

CREATE OR REPLACE FUNCTION decision.current_tenant_id() RETURNS uuid
LANGUAGE sql STABLE AS $$ SELECT platform.current_tenant_id() $$;
CREATE OR REPLACE FUNCTION decision.current_actor_id() RETURNS uuid
LANGUAGE sql STABLE AS $$ SELECT platform.current_user_id() $$;
CREATE OR REPLACE FUNCTION decision.current_domain_id() RETURNS uuid
LANGUAGE sql STABLE AS $$ SELECT platform.current_domain_id() $$;
CREATE OR REPLACE FUNCTION decision.system_access() RETURNS boolean
LANGUAGE sql STABLE AS $$ SELECT platform.is_system_access() $$;

CREATE TABLE decision.approval_policies(
  id text NOT NULL CHECK(length(btrim(id)) BETWEEN 1 AND 128),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id) ON DELETE RESTRICT,
  domain_id uuid NOT NULL,
  name text NOT NULL CHECK(length(btrim(name)) BETWEEN 1 AND 256),
  required_approvals smallint NOT NULL CHECK(required_approvals BETWEEN 1 AND 16),
  status text NOT NULL DEFAULT 'ACTIVE' CHECK(status IN ('ACTIVE','DISABLED')),
  record_version bigint NOT NULL DEFAULT 1 CHECK(record_version>0),
  created_by uuid NOT NULL, created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY(id,domain_id,tenant_id),
  FOREIGN KEY(domain_id,tenant_id) REFERENCES platform.business_domains(id,tenant_id) ON DELETE RESTRICT,
  FOREIGN KEY(created_by,tenant_id) REFERENCES platform.users(id,tenant_id) ON DELETE RESTRICT
);
CREATE TABLE decision.approval_policy_approvers(
  tenant_id uuid NOT NULL, domain_id uuid NOT NULL, policy_id text NOT NULL,
  approver_user_id uuid NOT NULL, sequence_no smallint NOT NULL CHECK(sequence_no BETWEEN 1 AND 32),
  PRIMARY KEY(tenant_id,domain_id,policy_id,approver_user_id),
  UNIQUE(tenant_id,domain_id,policy_id,sequence_no),
  FOREIGN KEY(policy_id,domain_id,tenant_id) REFERENCES decision.approval_policies(id,domain_id,tenant_id) ON DELETE CASCADE,
  FOREIGN KEY(approver_user_id,tenant_id) REFERENCES platform.users(id,tenant_id) ON DELETE RESTRICT
);

CREATE TABLE decision.decisions(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id) ON DELETE RESTRICT,
  domain_id uuid NOT NULL,
  owner_user_id uuid NOT NULL,
  created_by uuid NOT NULL,
  title text NOT NULL CHECK(length(btrim(title)) BETWEEN 1 AND 256),
  question text NOT NULL CHECK(length(btrim(question)) BETWEEN 1 AND 4096),
  decision_text text NOT NULL DEFAULT '' CHECK(length(decision_text)<=8192),
  expected_effect text NOT NULL DEFAULT '' CHECK(length(expected_effect)<=4096),
  risks_json jsonb NOT NULL DEFAULT '[]' CHECK(jsonb_typeof(risks_json)='array' AND pg_column_size(risks_json)<=65536),
  evidence_mode text NOT NULL CHECK(evidence_mode IN ('PLATFORM_VERIFIED','MANUAL_WITHOUT_PLATFORM_EVIDENCE')),
  approval_policy_id text NOT NULL CHECK(length(btrim(approval_policy_id)) BETWEEN 1 AND 128),
  required_approvals smallint NOT NULL CHECK(required_approvals BETWEEN 1 AND 16),
  status text NOT NULL DEFAULT 'DRAFT' CHECK(status IN (
    'DRAFT','IN_REVIEW','APPROVED','REJECTED','IN_EXECUTION','REVIEW_DUE','CLOSED','REOPENED','CANCELED'
  )),
  review_at timestamptz NOT NULL,
  terminal_reason text NOT NULL DEFAULT '' CHECK(length(terminal_reason)<=4096),
  record_version bigint NOT NULL DEFAULT 1 CHECK(record_version>0),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT decision_identity_tenant_key UNIQUE(id,tenant_id),
  CONSTRAINT decision_identity_domain_tenant_key UNIQUE(id,domain_id,tenant_id),
  CONSTRAINT decision_domain_fk FOREIGN KEY(domain_id,tenant_id)
    REFERENCES platform.business_domains(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT decision_owner_fk FOREIGN KEY(owner_user_id,tenant_id)
    REFERENCES platform.users(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT decision_creator_fk FOREIGN KEY(created_by,tenant_id)
    REFERENCES platform.users(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT decision_approval_policy_fk FOREIGN KEY(approval_policy_id,domain_id,tenant_id)
    REFERENCES decision.approval_policies(id,domain_id,tenant_id) ON DELETE RESTRICT
);

CREATE TABLE decision.decision_options(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(), tenant_id uuid NOT NULL, domain_id uuid NOT NULL,
  decision_id uuid NOT NULL, title text NOT NULL CHECK(length(btrim(title)) BETWEEN 1 AND 256),
  description text NOT NULL DEFAULT '' CHECK(length(description)<=4096), selected boolean NOT NULL DEFAULT false,
  created_at timestamptz NOT NULL DEFAULT now(), UNIQUE(id,tenant_id),
  FOREIGN KEY(decision_id,domain_id,tenant_id) REFERENCES decision.decisions(id,domain_id,tenant_id) ON DELETE RESTRICT
);

CREATE TABLE decision.decision_evidence(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(), tenant_id uuid NOT NULL, domain_id uuid NOT NULL,
  decision_id uuid NOT NULL, source_type text NOT NULL CHECK(source_type IN ('ANSWER_ARTIFACT','REPORT_VERSION','INSIGHT_ARTIFACT')),
  source_id uuid NOT NULL, source_hash text NOT NULL CHECK(source_hash ~ '^[0-9a-f]{64}$'),
  semantic_release_id uuid NOT NULL, semantic_release_hash text NOT NULL CHECK(semantic_release_hash ~ '^[0-9a-f]{64}$'),
  data_snapshot_id uuid, as_of timestamptz NOT NULL,
  policy_scope_hash text NOT NULL CHECK(policy_scope_hash ~ '^[0-9a-f]{64}$'),
  summary text NOT NULL CHECK(length(btrim(summary)) BETWEEN 1 AND 2048),
  verified boolean NOT NULL CHECK(verified), created_by uuid NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(), UNIQUE(id,tenant_id),
  UNIQUE(tenant_id,decision_id,source_type,source_id,source_hash),
  FOREIGN KEY(decision_id,domain_id,tenant_id) REFERENCES decision.decisions(id,domain_id,tenant_id) ON DELETE RESTRICT,
  FOREIGN KEY(created_by,tenant_id) REFERENCES platform.users(id,tenant_id) ON DELETE RESTRICT,
  FOREIGN KEY(semantic_release_id,domain_id,tenant_id) REFERENCES askdata.releases(id,domain_id,tenant_id) ON DELETE RESTRICT
);

CREATE TABLE decision.decision_approvals(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(), tenant_id uuid NOT NULL, domain_id uuid NOT NULL,
  decision_id uuid NOT NULL, approver_user_id uuid NOT NULL,
  sequence_no smallint NOT NULL CHECK(sequence_no BETWEEN 1 AND 32),
  status text NOT NULL DEFAULT 'PENDING' CHECK(status IN ('PENDING','APPROVED','REJECTED','CANCELED')),
  comment text NOT NULL DEFAULT '' CHECK(length(comment)<=4096), decided_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(), UNIQUE(id,tenant_id),
  UNIQUE(tenant_id,decision_id,approver_user_id), UNIQUE(tenant_id,decision_id,sequence_no),
  FOREIGN KEY(decision_id,domain_id,tenant_id) REFERENCES decision.decisions(id,domain_id,tenant_id) ON DELETE RESTRICT,
  FOREIGN KEY(approver_user_id,tenant_id) REFERENCES platform.users(id,tenant_id) ON DELETE RESTRICT,
  CHECK((status='PENDING' AND decided_at IS NULL) OR (status<>'PENDING' AND decided_at IS NOT NULL))
);

CREATE TABLE decision.action_items(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(), tenant_id uuid NOT NULL, domain_id uuid NOT NULL,
  decision_id uuid NOT NULL, title text NOT NULL CHECK(length(btrim(title)) BETWEEN 1 AND 256),
  description text NOT NULL DEFAULT '' CHECK(length(description)<=4096), assignee_user_id uuid NOT NULL,
  due_at timestamptz NOT NULL, status text NOT NULL DEFAULT 'TODO' CHECK(status IN ('TODO','DOING','BLOCKED','DONE','CANCELED')),
  block_reason text NOT NULL DEFAULT '' CHECK(length(block_reason)<=2048),
  completion_evidence text NOT NULL DEFAULT '' CHECK(length(completion_evidence)<=2048),
  deliverable_refs jsonb NOT NULL DEFAULT '[]' CHECK(jsonb_typeof(deliverable_refs)='array' AND pg_column_size(deliverable_refs)<=32768),
  record_version bigint NOT NULL DEFAULT 1 CHECK(record_version>0), created_by uuid NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE(id,tenant_id),
  FOREIGN KEY(decision_id,domain_id,tenant_id) REFERENCES decision.decisions(id,domain_id,tenant_id) ON DELETE RESTRICT,
  FOREIGN KEY(assignee_user_id,tenant_id) REFERENCES platform.users(id,tenant_id) ON DELETE RESTRICT,
  FOREIGN KEY(created_by,tenant_id) REFERENCES platform.users(id,tenant_id) ON DELETE RESTRICT,
  CHECK((status='BLOCKED')=(length(btrim(block_reason))>0)),
  CHECK((status='DONE')=(length(btrim(completion_evidence))>0))
);

CREATE TABLE decision.action_events(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(), tenant_id uuid NOT NULL, domain_id uuid NOT NULL,
  decision_id uuid NOT NULL, action_id uuid NOT NULL, event_no bigint NOT NULL CHECK(event_no>0),
  event_type text NOT NULL CHECK(event_type ~ '^[A-Z][A-Z0-9_]{0,63}$'),
  from_status text NOT NULL, to_status text NOT NULL, actor_user_id uuid NOT NULL,
  reason text NOT NULL DEFAULT '' CHECK(length(reason)<=4096), payload_json jsonb NOT NULL DEFAULT '{}'
    CHECK(jsonb_typeof(payload_json)='object' AND pg_column_size(payload_json)<=65536),
  created_at timestamptz NOT NULL DEFAULT now(), UNIQUE(id,tenant_id), UNIQUE(tenant_id,action_id,event_no),
  FOREIGN KEY(action_id,tenant_id) REFERENCES decision.action_items(id,tenant_id) ON DELETE RESTRICT,
  FOREIGN KEY(decision_id,domain_id,tenant_id) REFERENCES decision.decisions(id,domain_id,tenant_id) ON DELETE RESTRICT,
  FOREIGN KEY(actor_user_id,tenant_id) REFERENCES platform.users(id,tenant_id) ON DELETE RESTRICT
);

CREATE TABLE decision.outcome_metrics(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(), tenant_id uuid NOT NULL, domain_id uuid NOT NULL,
  decision_id uuid NOT NULL, metric_version_id text NOT NULL CHECK(length(btrim(metric_version_id)) BETWEEN 1 AND 128),
  semantic_ir_json jsonb NOT NULL CHECK(jsonb_typeof(semantic_ir_json)='object' AND pg_column_size(semantic_ir_json)<=262144),
  semantic_ir_hash text NOT NULL CHECK(semantic_ir_hash ~ '^[0-9a-f]{64}$'),
  semantic_release_id uuid NOT NULL, semantic_release_hash text NOT NULL CHECK(semantic_release_hash ~ '^[0-9a-f]{64}$'),
  baseline_value numeric NOT NULL, target_direction text NOT NULL CHECK(target_direction IN ('INCREASE','DECREASE','AT_LEAST','AT_MOST','RANGE')),
  target_value numeric, target_upper_value numeric, review_at timestamptz NOT NULL,
  attribution_note text NOT NULL DEFAULT '' CHECK(length(attribution_note)<=4096),
  current_value numeric, current_result_hash text CHECK(current_result_hash IS NULL OR current_result_hash ~ '^[0-9a-f]{64}$'),
  current_policy_scope_hash text CHECK(current_policy_scope_hash IS NULL OR current_policy_scope_hash ~ '^[0-9a-f]{64}$'),
  current_as_of timestamptz, drifted boolean NOT NULL DEFAULT false, refresh_status text NOT NULL DEFAULT 'PENDING'
    CHECK(refresh_status IN ('PENDING','SUCCEEDED','NO_DATA','FAILED','NO_PERMISSION')),
  record_version bigint NOT NULL DEFAULT 1 CHECK(record_version>0), created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(), UNIQUE(id,tenant_id), UNIQUE(tenant_id,decision_id,metric_version_id),
  FOREIGN KEY(decision_id,domain_id,tenant_id) REFERENCES decision.decisions(id,domain_id,tenant_id) ON DELETE RESTRICT,
  FOREIGN KEY(semantic_release_id,domain_id,tenant_id) REFERENCES askdata.releases(id,domain_id,tenant_id) ON DELETE RESTRICT,
  CHECK((target_direction='RANGE')=(target_upper_value IS NOT NULL)),
  CHECK(target_upper_value IS NULL OR target_value IS NULL OR target_upper_value>=target_value),
  CHECK((current_value IS NULL AND current_result_hash IS NULL AND current_as_of IS NULL)
    OR (current_value IS NOT NULL AND current_result_hash IS NOT NULL AND current_policy_scope_hash IS NOT NULL AND current_as_of IS NOT NULL))
);

CREATE TABLE decision.outcome_reviews(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(), tenant_id uuid NOT NULL, domain_id uuid NOT NULL,
  decision_id uuid NOT NULL, status text NOT NULL CHECK(status IN ('PENDING','GENERATED','CONFIRMED','INCONCLUSIVE')),
  conclusion text CHECK(conclusion IS NULL OR conclusion IN ('ACHIEVED','PARTIALLY_ACHIEVED','NOT_ACHIEVED','INCONCLUSIVE')),
  notes text NOT NULL DEFAULT '' CHECK(length(notes)<=4096), generated_at timestamptz,
  confirmed_by uuid, confirmed_at timestamptz, record_version bigint NOT NULL DEFAULT 1 CHECK(record_version>0),
  created_at timestamptz NOT NULL DEFAULT now(), UNIQUE(id,tenant_id), UNIQUE(tenant_id,decision_id),
  FOREIGN KEY(decision_id,domain_id,tenant_id) REFERENCES decision.decisions(id,domain_id,tenant_id) ON DELETE RESTRICT,
  FOREIGN KEY(confirmed_by,tenant_id) REFERENCES platform.users(id,tenant_id) ON DELETE RESTRICT,
  CHECK((status IN ('CONFIRMED','INCONCLUSIVE'))=(conclusion IS NOT NULL AND confirmed_by IS NOT NULL AND confirmed_at IS NOT NULL))
);

CREATE TABLE decision.decision_events(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(), tenant_id uuid NOT NULL, domain_id uuid NOT NULL,
  decision_id uuid NOT NULL, event_no bigint NOT NULL CHECK(event_no>0),
  event_type text NOT NULL CHECK(event_type ~ '^[A-Z][A-Z0-9_]{0,63}$'), actor_user_id uuid NOT NULL,
  from_status text NOT NULL, to_status text NOT NULL, reason text NOT NULL DEFAULT '' CHECK(length(reason)<=4096),
  payload_json jsonb NOT NULL DEFAULT '{}' CHECK(jsonb_typeof(payload_json)='object' AND pg_column_size(payload_json)<=65536),
  record_version bigint NOT NULL CHECK(record_version>0), created_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE(id,tenant_id), UNIQUE(tenant_id,decision_id,event_no),
  FOREIGN KEY(decision_id,domain_id,tenant_id) REFERENCES decision.decisions(id,domain_id,tenant_id) ON DELETE RESTRICT,
  FOREIGN KEY(actor_user_id,tenant_id) REFERENCES platform.users(id,tenant_id) ON DELETE RESTRICT
);

CREATE TABLE decision.decision_notifications(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(), tenant_id uuid NOT NULL, domain_id uuid NOT NULL,
  decision_id uuid NOT NULL, action_id uuid, recipient_user_id uuid NOT NULL,
  notification_type text NOT NULL CHECK(notification_type ~ '^[A-Z][A-Z0-9_]{0,63}$'),
  dedup_key text NOT NULL CHECK(length(btrim(dedup_key)) BETWEEN 1 AND 256),
  summary text NOT NULL CHECK(length(btrim(summary)) BETWEEN 1 AND 512),
  read_at timestamptz, resolved_at timestamptz, created_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE(id,tenant_id), UNIQUE(tenant_id,recipient_user_id,dedup_key),
  FOREIGN KEY(decision_id,domain_id,tenant_id) REFERENCES decision.decisions(id,domain_id,tenant_id) ON DELETE RESTRICT,
  FOREIGN KEY(action_id,tenant_id) REFERENCES decision.action_items(id,tenant_id) ON DELETE RESTRICT,
  FOREIGN KEY(recipient_user_id,tenant_id) REFERENCES platform.users(id,tenant_id) ON DELETE RESTRICT
);

CREATE OR REPLACE FUNCTION decision.can_access(selected_decision_id uuid)
RETURNS boolean LANGUAGE sql STABLE SECURITY DEFINER
SET search_path=pg_catalog,platform,decision AS $$
  SELECT decision.system_access() OR EXISTS(
    SELECT 1 FROM decision.decisions d
    WHERE d.id=selected_decision_id AND d.tenant_id=decision.current_tenant_id()
      AND d.domain_id=decision.current_domain_id()
      AND (d.owner_user_id=decision.current_actor_id() OR d.created_by=decision.current_actor_id()
        OR EXISTS(SELECT 1 FROM decision.decision_approvals a WHERE a.decision_id=d.id AND a.tenant_id=d.tenant_id AND a.approver_user_id=decision.current_actor_id())
        OR EXISTS(SELECT 1 FROM decision.action_items i WHERE i.decision_id=d.id AND i.tenant_id=d.tenant_id AND i.assignee_user_id=decision.current_actor_id()))
  )
$$;

CREATE OR REPLACE FUNCTION decision.reject_append_only_mutation() RETURNS trigger
LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'decision event facts are immutable' USING ERRCODE='55000'; END $$;
CREATE TRIGGER decision_evidence_immutable BEFORE UPDATE OR DELETE ON decision.decision_evidence FOR EACH ROW EXECUTE FUNCTION decision.reject_append_only_mutation();
CREATE TRIGGER decision_options_immutable BEFORE UPDATE OR DELETE ON decision.decision_options FOR EACH ROW EXECUTE FUNCTION decision.reject_append_only_mutation();
CREATE TRIGGER action_events_immutable BEFORE UPDATE OR DELETE ON decision.action_events FOR EACH ROW EXECUTE FUNCTION decision.reject_append_only_mutation();
CREATE TRIGGER decision_events_immutable BEFORE UPDATE OR DELETE ON decision.decision_events FOR EACH ROW EXECUTE FUNCTION decision.reject_append_only_mutation();

CREATE INDEX decisions_participant_idx ON decision.decisions(tenant_id,domain_id,owner_user_id,status,updated_at DESC,id);
CREATE INDEX approvals_inbox_idx ON decision.decision_approvals(tenant_id,domain_id,approver_user_id,status,created_at,id);
CREATE INDEX actions_inbox_idx ON decision.action_items(tenant_id,domain_id,assignee_user_id,status,due_at,id);
CREATE INDEX outcome_due_idx ON decision.outcome_metrics(tenant_id,domain_id,review_at,refresh_status,id);
CREATE INDEX notifications_inbox_idx ON decision.decision_notifications(tenant_id,domain_id,recipient_user_id,resolved_at,read_at,created_at DESC,id);

DO $$ DECLARE table_name text; BEGIN
  FOREACH table_name IN ARRAY ARRAY['approval_policies','approval_policy_approvers','decisions','decision_options','decision_evidence','decision_approvals','action_items','action_events','outcome_metrics','outcome_reviews','decision_events','decision_notifications'] LOOP
    EXECUTE format('ALTER TABLE decision.%I ENABLE ROW LEVEL SECURITY',table_name);
    EXECUTE format('ALTER TABLE decision.%I FORCE ROW LEVEL SECURITY',table_name);
  END LOOP;
END $$;
CREATE POLICY approval_policies_access ON decision.approval_policies
  USING(tenant_id=decision.current_tenant_id() AND domain_id=decision.current_domain_id())
  WITH CHECK(tenant_id=decision.current_tenant_id() AND domain_id=decision.current_domain_id() AND decision.system_access());
CREATE POLICY approval_policy_approvers_access ON decision.approval_policy_approvers
  USING(tenant_id=decision.current_tenant_id() AND domain_id=decision.current_domain_id())
  WITH CHECK(tenant_id=decision.current_tenant_id() AND domain_id=decision.current_domain_id() AND decision.system_access());
CREATE POLICY decisions_access ON decision.decisions
  USING(tenant_id=decision.current_tenant_id() AND decision.can_access(id))
  WITH CHECK(tenant_id=decision.current_tenant_id() AND domain_id=decision.current_domain_id()
    AND (decision.system_access() OR owner_user_id=decision.current_actor_id() OR created_by=decision.current_actor_id()));
DO $$ DECLARE table_name text; BEGIN
  FOREACH table_name IN ARRAY ARRAY['decision_options','decision_evidence','decision_approvals','action_items','action_events','outcome_metrics','outcome_reviews','decision_events','decision_notifications'] LOOP
    EXECUTE format('CREATE POLICY %I ON decision.%I USING(tenant_id=decision.current_tenant_id() AND decision.can_access(decision_id)) WITH CHECK(tenant_id=decision.current_tenant_id() AND domain_id=decision.current_domain_id() AND decision.can_access(decision_id))',table_name||'_access',table_name);
  END LOOP;
END $$;

REVOKE ALL ON FUNCTION decision.can_access(uuid),decision.reject_append_only_mutation() FROM PUBLIC;
ALTER DEFAULT PRIVILEGES IN SCHEMA decision REVOKE ALL ON TABLES FROM PUBLIC;
ALTER DEFAULT PRIVILEGES IN SCHEMA decision REVOKE ALL ON FUNCTIONS FROM PUBLIC;
COMMENT ON SCHEMA decision IS 'Tenant/domain-scoped decision support, action tracking and outcome review; no external transaction execution';
