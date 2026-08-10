CREATE TABLE platform.work_item_receipts(
  tenant_id uuid NOT NULL,
  domain_id uuid NOT NULL,
  actor_user_id uuid NOT NULL,
  source_type text NOT NULL CHECK(source_type ~ '^[A-Z][A-Z0-9_]{0,63}$'),
  source_id uuid NOT NULL,
  source_version text NOT NULL CHECK(length(source_version) BETWEEN 1 AND 128),
  read_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY(tenant_id,domain_id,actor_user_id,source_type,source_id),
  FOREIGN KEY(domain_id,tenant_id) REFERENCES platform.business_domains(id,tenant_id) ON DELETE RESTRICT,
  FOREIGN KEY(actor_user_id,tenant_id) REFERENCES platform.users(id,tenant_id) ON DELETE RESTRICT
);

CREATE INDEX work_item_receipts_actor_idx ON platform.work_item_receipts(
  tenant_id,domain_id,actor_user_id,read_at DESC,source_type,source_id
);

ALTER TABLE platform.work_item_receipts ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform.work_item_receipts FORCE ROW LEVEL SECURITY;
CREATE POLICY work_item_receipts_actor_access ON platform.work_item_receipts
  USING(tenant_id=platform.current_tenant_id() AND domain_id=platform.current_domain_id()
    AND actor_user_id=platform.current_user_id())
  WITH CHECK(tenant_id=platform.current_tenant_id() AND domain_id=platform.current_domain_id()
    AND actor_user_id=platform.current_user_id());

COMMENT ON TABLE platform.work_item_receipts IS
  'Actor-scoped read markers only; unified inbox business truth remains in each source bounded context';
