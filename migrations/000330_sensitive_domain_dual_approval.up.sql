-- UI-FLOW-P01-B02: security co-signature, two-person approval and timed
-- escalation for access to sensitive business domains.
--
-- Domain access was a single-reviewer decision: one domain administrator could
-- grant themselves a colleague's access to any domain, however sensitive its
-- data. That is the same exposure the detail-delivery chain already guards with
-- an independent security co-signer (000233/000271), so this reuses that
-- vocabulary and its separation-of-duties rule rather than inventing a second
-- notion of "sensitive".
--
-- The two approval seats are DOMAIN_OWNER (the business decision: should this
-- person work in this domain) and SECURITY (the independent check: is granting
-- it acceptable). They must be two different accounts, and one rejection in
-- either seat is decisive.
BEGIN;

ALTER TABLE platform.business_domains
  ADD COLUMN access_sensitivity text NOT NULL DEFAULT 'INTERNAL'
    CHECK(access_sensitivity IN ('PUBLIC','INTERNAL','CONFIDENTIAL','RESTRICTED'));

COMMENT ON COLUMN platform.business_domains.access_sensitivity IS
  'Access sensitivity of the domain; CONFIDENTIAL and RESTRICTED require a two-seat approval with an independent security co-signature';

-- Approval receipts reference the application by (id,tenant_id) so a receipt can
-- never be attached across tenants.
ALTER TABLE platform.domain_access_applications
  ADD CONSTRAINT domain_access_applications_identity_tenant_key UNIQUE(id,tenant_id);

-- The requirement is pinned when the application is submitted. Reading it from
-- the domain at decision time would let a sensitivity downgrade retroactively
-- weaken an in-flight request.
ALTER TABLE platform.domain_access_applications
  ADD COLUMN requires_dual_approval boolean NOT NULL DEFAULT false,
  ADD COLUMN sla_due_at timestamptz,
  ADD COLUMN escalation_level smallint NOT NULL DEFAULT 0
    CHECK(escalation_level BETWEEN 0 AND 3);

CREATE TABLE platform.domain_access_approvals(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id),
  application_id uuid NOT NULL,
  reviewer_id uuid NOT NULL,
  review_role text NOT NULL CHECK(review_role IN ('DOMAIN_OWNER','SECURITY')),
  decision text NOT NULL CHECK(decision IN ('APPROVED','REJECTED')),
  comment text NOT NULL DEFAULT '' CHECK(octet_length(comment)<=1000),
  created_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT domain_access_approvals_application_fk
    FOREIGN KEY(application_id,tenant_id)
    REFERENCES platform.domain_access_applications(id,tenant_id) ON DELETE CASCADE,
  CONSTRAINT domain_access_approvals_reviewer_fk
    FOREIGN KEY(reviewer_id,tenant_id)
    REFERENCES platform.users(id,tenant_id) ON DELETE RESTRICT,
  -- Separation of duties: one account occupies at most one seat, and each seat
  -- is occupied at most once.
  CONSTRAINT domain_access_approvals_one_seat_per_reviewer_key
    UNIQUE(application_id,reviewer_id),
  CONSTRAINT domain_access_approvals_one_reviewer_per_seat_key
    UNIQUE(application_id,review_role)
);
CREATE INDEX domain_access_approvals_application_idx
  ON platform.domain_access_approvals(tenant_id,application_id,created_at);

CREATE TABLE platform.domain_access_escalations(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id),
  application_id uuid NOT NULL,
  level smallint NOT NULL CHECK(level BETWEEN 1 AND 3),
  escalated_by uuid NOT NULL,
  note text NOT NULL DEFAULT '' CHECK(octet_length(note)<=1000),
  created_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT domain_access_escalations_application_fk
    FOREIGN KEY(application_id,tenant_id)
    REFERENCES platform.domain_access_applications(id,tenant_id) ON DELETE CASCADE,
  CONSTRAINT domain_access_escalations_actor_fk
    FOREIGN KEY(escalated_by,tenant_id)
    REFERENCES platform.users(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT domain_access_escalations_level_key UNIQUE(application_id,level)
);
CREATE INDEX domain_access_escalations_application_idx
  ON platform.domain_access_escalations(tenant_id,application_id,level);

-- Both ledgers are append-only: an approval that can be edited afterwards is
-- not evidence of who agreed to what.
CREATE OR REPLACE FUNCTION platform.reject_domain_access_ledger_mutation()
RETURNS trigger
LANGUAGE plpgsql
SET search_path=pg_catalog,platform
AS $$
BEGIN
  RAISE EXCEPTION 'domain access approval ledger is append-only' USING ERRCODE='55000';
END
$$;

CREATE TRIGGER domain_access_approvals_immutable
BEFORE UPDATE OR DELETE ON platform.domain_access_approvals
FOR EACH ROW EXECUTE FUNCTION platform.reject_domain_access_ledger_mutation();

CREATE TRIGGER domain_access_escalations_immutable
BEFORE UPDATE OR DELETE ON platform.domain_access_escalations
FOR EACH ROW EXECUTE FUNCTION platform.reject_domain_access_ledger_mutation();

ALTER TABLE platform.domain_access_approvals ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform.domain_access_approvals FORCE ROW LEVEL SECURITY;
CREATE POLICY domain_access_approvals_tenant_isolation
  ON platform.domain_access_approvals
  USING(tenant_id=platform.current_tenant_id())
  WITH CHECK(tenant_id=platform.current_tenant_id());

ALTER TABLE platform.domain_access_escalations ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform.domain_access_escalations FORCE ROW LEVEL SECURITY;
CREATE POLICY domain_access_escalations_tenant_isolation
  ON platform.domain_access_escalations
  USING(tenant_id=platform.current_tenant_id())
  WITH CHECK(tenant_id=platform.current_tenant_id());

REVOKE ALL ON FUNCTION platform.reject_domain_access_ledger_mutation() FROM PUBLIC;

COMMENT ON TABLE platform.domain_access_approvals IS
  'Append-only two-seat domain access approval receipts; one account can never occupy both the DOMAIN_OWNER and SECURITY seats';
COMMENT ON TABLE platform.domain_access_escalations IS
  'Append-only three-level escalation ledger for domain access requests past their review SLA';

COMMIT;
