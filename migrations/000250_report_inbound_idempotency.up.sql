CREATE TABLE platform.report_inbound_idempotency(
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id) ON DELETE RESTRICT,
  intent_id uuid NOT NULL,
  actor_user_id uuid NOT NULL,
  report_id uuid NOT NULL,
  idempotency_key_hash text NOT NULL CHECK(idempotency_key_hash ~ '^[0-9a-f]{64}$'),
  bundle_hash text NOT NULL CHECK(bundle_hash ~ '^[0-9a-f]{64}$'),
  state text NOT NULL CHECK(state IN ('PROCESSING','APPLIED')),
  applied_revision_no bigint,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY(tenant_id,intent_id),
  UNIQUE(tenant_id,actor_user_id,idempotency_key_hash),
  CHECK((state='APPLIED')=(applied_revision_no IS NOT NULL)),
  FOREIGN KEY(actor_user_id,tenant_id) REFERENCES platform.users(id,tenant_id) ON DELETE RESTRICT,
  FOREIGN KEY(report_id,tenant_id) REFERENCES platform.reports(id,tenant_id) ON DELETE CASCADE
);

ALTER TABLE platform.report_inbound_idempotency ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform.report_inbound_idempotency FORCE ROW LEVEL SECURITY;
CREATE POLICY report_inbound_idempotency_scope ON platform.report_inbound_idempotency
  USING(tenant_id=platform.current_tenant_id() AND
        (platform.is_system_access() OR actor_user_id=platform.current_user_id()))
  WITH CHECK(tenant_id=platform.current_tenant_id() AND
             (platform.is_system_access() OR actor_user_id=platform.current_user_id()));

-- Runtime grants are applied centrally by scripts/migrate.sh using the
-- environment-specific application and worker role names.
