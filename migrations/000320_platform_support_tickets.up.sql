BEGIN;

CREATE TABLE platform.support_tickets(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL,
  domain_id uuid NOT NULL,
  reporter_user_id uuid NOT NULL,
  client_request_id uuid NOT NULL,
  category text NOT NULL CHECK(category IN('QUESTION','DATA','REPORT','ACCESS','SYSTEM','OTHER')),
  priority text NOT NULL CHECK(priority IN('NORMAL','HIGH','URGENT')),
  subject text NOT NULL CHECK(length(btrim(subject)) BETWEEN 4 AND 120),
  description text NOT NULL CHECK(length(btrim(description)) BETWEEN 10 AND 4000),
  page_url text NOT NULL DEFAULT '' CHECK(length(page_url)<=1000),
  error_code text NOT NULL DEFAULT '' CHECK(error_code ~ '^[A-Z0-9_]{0,127}$'),
  status text NOT NULL DEFAULT 'OPEN' CHECK(status IN('OPEN','IN_PROGRESS','RESOLVED','CLOSED')),
  resolution_note text NOT NULL DEFAULT '' CHECK(length(resolution_note)<=2000),
  assignee_user_id uuid,
  record_version bigint NOT NULL DEFAULT 1 CHECK(record_version>0),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  resolved_at timestamptz,
  UNIQUE(id,tenant_id),
  UNIQUE(tenant_id,reporter_user_id,client_request_id),
  FOREIGN KEY(domain_id,tenant_id) REFERENCES platform.business_domains(id,tenant_id) ON DELETE RESTRICT,
  FOREIGN KEY(reporter_user_id,tenant_id) REFERENCES platform.users(id,tenant_id) ON DELETE RESTRICT,
  FOREIGN KEY(assignee_user_id,tenant_id) REFERENCES platform.users(id,tenant_id) ON DELETE RESTRICT,
  CHECK((status='RESOLVED' OR status='CLOSED')=(resolved_at IS NOT NULL))
);

CREATE INDEX support_tickets_queue_idx
  ON platform.support_tickets(tenant_id,domain_id,status,priority,updated_at DESC);
CREATE INDEX support_tickets_reporter_idx
  ON platform.support_tickets(tenant_id,reporter_user_id,created_at DESC);

CREATE OR REPLACE FUNCTION platform.guard_support_ticket_mutation()
RETURNS trigger
LANGUAGE plpgsql
SET search_path=pg_catalog,platform
AS $$
BEGIN
  IF TG_OP='DELETE' THEN
    RAISE EXCEPTION 'support tickets are retained audit records' USING ERRCODE='55000';
  END IF;
  IF TG_OP='INSERT' THEN
    NEW.subject:=btrim(NEW.subject);
    NEW.description:=btrim(NEW.description);
    RETURN NEW;
  END IF;
  IF ROW(NEW.id,NEW.tenant_id,NEW.domain_id,NEW.reporter_user_id,
         NEW.client_request_id,NEW.category,NEW.priority,NEW.subject,
         NEW.description,NEW.page_url,NEW.error_code,NEW.created_at)
     IS DISTINCT FROM
     ROW(OLD.id,OLD.tenant_id,OLD.domain_id,OLD.reporter_user_id,
         OLD.client_request_id,OLD.category,OLD.priority,OLD.subject,
         OLD.description,OLD.page_url,OLD.error_code,OLD.created_at) THEN
    RAISE EXCEPTION 'support ticket source facts are immutable' USING ERRCODE='55000';
  END IF;
  IF NEW.record_version<>OLD.record_version+1 THEN
    RAISE EXCEPTION 'support ticket record version conflict' USING ERRCODE='40001';
  END IF;
  IF NOT (CASE OLD.status
    WHEN 'OPEN' THEN NEW.status IN('IN_PROGRESS','RESOLVED','CLOSED')
    WHEN 'IN_PROGRESS' THEN NEW.status IN('RESOLVED','CLOSED')
    WHEN 'RESOLVED' THEN NEW.status IN('IN_PROGRESS','CLOSED')
    ELSE false END) THEN
    RAISE EXCEPTION 'invalid support ticket transition' USING ERRCODE='23514';
  END IF;
  RETURN NEW;
END
$$;

CREATE TRIGGER support_tickets_guard
BEFORE INSERT OR UPDATE OR DELETE ON platform.support_tickets
FOR EACH ROW EXECUTE FUNCTION platform.guard_support_ticket_mutation();

ALTER TABLE platform.support_tickets ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform.support_tickets FORCE ROW LEVEL SECURITY;
CREATE POLICY support_tickets_read ON platform.support_tickets FOR SELECT
  USING(
    tenant_id=platform.current_tenant_id()
    AND domain_id=platform.current_domain_id()
    AND (
      platform.is_system_access()
      OR reporter_user_id=platform.current_user_id()
      OR platform.user_is_platform_administrator()
      OR platform.user_is_domain_administrator(domain_id)
    )
  );
CREATE POLICY support_tickets_create ON platform.support_tickets FOR INSERT
  WITH CHECK(
    tenant_id=platform.current_tenant_id()
    AND domain_id=platform.current_domain_id()
    AND (platform.is_system_access() OR reporter_user_id=platform.current_user_id())
  );
CREATE POLICY support_tickets_manage ON platform.support_tickets FOR UPDATE
  USING(
    tenant_id=platform.current_tenant_id()
    AND domain_id=platform.current_domain_id()
    AND (
      platform.is_system_access()
      OR platform.user_is_platform_administrator()
      OR platform.user_is_domain_administrator(domain_id)
    )
  )
  WITH CHECK(
    tenant_id=platform.current_tenant_id()
    AND domain_id=platform.current_domain_id()
    AND (
      platform.is_system_access()
      OR platform.user_is_platform_administrator()
      OR platform.user_is_domain_administrator(domain_id)
    )
  );

REVOKE ALL ON FUNCTION platform.guard_support_ticket_mutation() FROM PUBLIC;
COMMENT ON TABLE platform.support_tickets IS
  'User-visible platform support lifecycle with reporter ownership, administrator queue access and immutable source facts';

COMMIT;
