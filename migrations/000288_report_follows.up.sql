CREATE TABLE platform.report_follows(
  tenant_id uuid NOT NULL,domain_id uuid NOT NULL,report_id uuid NOT NULL,actor_user_id uuid NOT NULL,
  followed_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY(tenant_id,domain_id,actor_user_id,report_id),
  FOREIGN KEY(domain_id,tenant_id) REFERENCES platform.business_domains(id,tenant_id) ON DELETE RESTRICT,
  FOREIGN KEY(report_id,tenant_id) REFERENCES platform.reports(id,tenant_id) ON DELETE CASCADE,
  FOREIGN KEY(actor_user_id,tenant_id) REFERENCES platform.users(id,tenant_id) ON DELETE CASCADE
);
CREATE INDEX report_follows_actor_idx ON platform.report_follows(tenant_id,domain_id,actor_user_id,followed_at DESC,report_id);
ALTER TABLE platform.report_follows ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform.report_follows FORCE ROW LEVEL SECURITY;
CREATE POLICY report_follows_read ON platform.report_follows FOR SELECT USING(
  tenant_id=platform.current_tenant_id() AND domain_id=platform.current_domain_id() AND actor_user_id=platform.current_user_id());
CREATE POLICY report_follows_insert ON platform.report_follows FOR INSERT WITH CHECK(
  tenant_id=platform.current_tenant_id() AND domain_id=platform.current_domain_id() AND actor_user_id=platform.current_user_id()
  AND platform.report_v2_can_access(report_id,ARRAY['VIEW']::text[]));
CREATE POLICY report_follows_delete ON platform.report_follows FOR DELETE USING(
  tenant_id=platform.current_tenant_id() AND domain_id=platform.current_domain_id() AND actor_user_id=platform.current_user_id());
COMMENT ON TABLE platform.report_follows IS 'Actor/domain-scoped personalization only; follows never grant report access';
