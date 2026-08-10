CREATE TABLE askdata.add_to_report_intents(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(), tenant_id uuid NOT NULL,
  question_run_id uuid NOT NULL, actor_user_id uuid NOT NULL,
  idempotency_key text NOT NULL CHECK(length(idempotency_key) BETWEEN 8 AND 128),
  target_report_id uuid NOT NULL, target_page_id text, target_section_id text, target_block_id text,
  operation_bundle_json jsonb NOT NULL CHECK(jsonb_typeof(operation_bundle_json)='object' AND askdata.json_is_safe(operation_bundle_json)),
  preview_hash text NOT NULL CHECK(preview_hash ~ '^[0-9a-f]{64}$'),
  state text NOT NULL DEFAULT 'PENDING_CONFIRMATION' CHECK(state IN ('PENDING_CONFIRMATION','PENDING','APPLIED','REJECTED','EXPIRED')),
  applied_revision_no bigint, rejection_code text, rejection_detail text,
  confirmed_at timestamptz, created_at timestamptz NOT NULL DEFAULT now(),
  expires_at timestamptz NOT NULL DEFAULT now()+interval '7 days', updated_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE(tenant_id,actor_user_id,idempotency_key), UNIQUE(id,tenant_id),
  FOREIGN KEY(question_run_id,actor_user_id,tenant_id) REFERENCES askdata.question_runs(id,actor_id,tenant_id) ON DELETE RESTRICT,
  FOREIGN KEY(target_report_id,tenant_id) REFERENCES platform.reports(id,tenant_id) ON DELETE CASCADE,
  CHECK(expires_at>created_at AND expires_at<=created_at+interval '7 days'),
  CHECK((state='APPLIED')=(applied_revision_no IS NOT NULL)),
  CHECK((state='REJECTED')=(rejection_code IS NOT NULL)),
  CHECK((state IN ('PENDING','APPLIED','REJECTED'))=(confirmed_at IS NOT NULL))
);
CREATE TABLE askdata.add_to_report_outbox(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(), tenant_id uuid NOT NULL,
  intent_id uuid NOT NULL, state text NOT NULL DEFAULT 'PENDING' CHECK(state IN ('PENDING','RUNNING','DONE','FAILED')),
  attempt integer NOT NULL DEFAULT 0 CHECK(attempt BETWEEN 0 AND 10),
  lease_token uuid, lease_expires_at timestamptz, next_attempt_at timestamptz NOT NULL DEFAULT now(),
  created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE(intent_id), FOREIGN KEY(intent_id,tenant_id) REFERENCES askdata.add_to_report_intents(id,tenant_id) ON DELETE CASCADE
);
CREATE INDEX add_to_report_outbox_claim_idx ON askdata.add_to_report_outbox(state,next_attempt_at,lease_expires_at) WHERE state IN ('PENDING','RUNNING');
ALTER TABLE askdata.add_to_report_intents ENABLE ROW LEVEL SECURITY;
ALTER TABLE askdata.add_to_report_intents FORCE ROW LEVEL SECURITY;
ALTER TABLE askdata.add_to_report_outbox ENABLE ROW LEVEL SECURITY;
ALTER TABLE askdata.add_to_report_outbox FORCE ROW LEVEL SECURITY;
CREATE POLICY add_to_report_intents_actor ON askdata.add_to_report_intents
  USING(tenant_id=platform.current_tenant_id() AND (platform.is_system_access() OR actor_user_id=platform.current_user_id()))
  WITH CHECK(tenant_id=platform.current_tenant_id() AND (platform.is_system_access() OR actor_user_id=platform.current_user_id()));
CREATE POLICY add_to_report_outbox_worker ON askdata.add_to_report_outbox
  USING(tenant_id=platform.current_tenant_id() AND platform.is_system_access())
  WITH CHECK(tenant_id=platform.current_tenant_id() AND platform.is_system_access());
