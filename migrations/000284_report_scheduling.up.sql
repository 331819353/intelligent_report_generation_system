CREATE TABLE platform.report_schedules(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(), tenant_id uuid NOT NULL, domain_id uuid NOT NULL,
  report_id uuid NOT NULL, report_version_id uuid NOT NULL, name text NOT NULL CHECK(length(btrim(name)) BETWEEN 1 AND 256),
  schedule_kind text NOT NULL CHECK(schedule_kind IN ('DAILY','WEEKLY','MONTHLY')),
  local_time time NOT NULL, weekdays smallint[] NOT NULL DEFAULT '{}' CHECK(
    cardinality(weekdays)<=7 AND array_position(weekdays,NULL) IS NULL AND weekdays<@ARRAY[0,1,2,3,4,5,6]::smallint[]),
  day_of_month smallint CHECK(day_of_month BETWEEN 1 AND 31), timezone text NOT NULL CHECK(length(btrim(timezone)) BETWEEN 1 AND 128),
  business_calendar text NOT NULL CHECK(business_calendar IN ('CALENDAR_DAYS','WEEKDAYS')),
  state text NOT NULL DEFAULT 'ACTIVE' CHECK(state IN ('ACTIVE','PAUSED','DISABLED')),
  next_run_at timestamptz NOT NULL, consecutive_failures integer NOT NULL DEFAULT 0 CHECK(consecutive_failures BETWEEN 0 AND 1000),
  max_consecutive_failures integer NOT NULL DEFAULT 3 CHECK(max_consecutive_failures BETWEEN 1 AND 20),
  miss_after_seconds integer NOT NULL DEFAULT 86400 CHECK(miss_after_seconds BETWEEN 60 AND 604800),
  lease_token uuid, lease_expires_at timestamptz, last_failure_code text NOT NULL DEFAULT '',
  owner_user_id uuid NOT NULL, created_by uuid NOT NULL, record_version bigint NOT NULL DEFAULT 1 CHECK(record_version>0),
  created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE(id,tenant_id), UNIQUE(id,domain_id,tenant_id),
  FOREIGN KEY(domain_id,tenant_id) REFERENCES platform.business_domains(id,tenant_id) ON DELETE RESTRICT,
  FOREIGN KEY(report_id,tenant_id) REFERENCES platform.reports(id,tenant_id) ON DELETE RESTRICT,
  FOREIGN KEY(report_version_id,report_id,tenant_id) REFERENCES platform.report_versions(id,report_id,tenant_id) ON DELETE RESTRICT,
  FOREIGN KEY(owner_user_id,tenant_id) REFERENCES platform.users(id,tenant_id) ON DELETE RESTRICT,
  FOREIGN KEY(created_by,tenant_id) REFERENCES platform.users(id,tenant_id) ON DELETE RESTRICT,
  CHECK((schedule_kind='DAILY' AND cardinality(weekdays)=0 AND day_of_month IS NULL)
    OR (schedule_kind='WEEKLY' AND cardinality(weekdays)>0 AND day_of_month IS NULL)
    OR (schedule_kind='MONTHLY' AND cardinality(weekdays)=0 AND day_of_month IS NOT NULL)),
  CHECK((lease_token IS NULL)=(lease_expires_at IS NULL))
);

CREATE TABLE platform.report_subscriptions(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(), tenant_id uuid NOT NULL, domain_id uuid NOT NULL,
  schedule_id uuid NOT NULL, recipient_user_id uuid NOT NULL, channel text NOT NULL DEFAULT 'IN_APP' CHECK(channel='IN_APP'),
  state text NOT NULL DEFAULT 'ACTIVE' CHECK(state IN ('ACTIVE','PAUSED','REVOKED')),
  created_by uuid NOT NULL, record_version bigint NOT NULL DEFAULT 1 CHECK(record_version>0),
  created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE(id,tenant_id), UNIQUE(tenant_id,schedule_id,recipient_user_id),
  FOREIGN KEY(schedule_id,domain_id,tenant_id) REFERENCES platform.report_schedules(id,domain_id,tenant_id) ON DELETE RESTRICT,
  FOREIGN KEY(recipient_user_id,tenant_id) REFERENCES platform.users(id,tenant_id) ON DELETE RESTRICT,
  FOREIGN KEY(created_by,tenant_id) REFERENCES platform.users(id,tenant_id) ON DELETE RESTRICT
);

CREATE TABLE platform.report_deliveries(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(), tenant_id uuid NOT NULL, domain_id uuid NOT NULL,
  schedule_id uuid NOT NULL, subscription_id uuid NOT NULL, report_id uuid NOT NULL, report_version_id uuid NOT NULL,
  recipient_user_id uuid NOT NULL, scheduled_for timestamptz NOT NULL, channel text NOT NULL CHECK(channel='IN_APP'),
  state text NOT NULL DEFAULT 'PENDING' CHECK(state IN ('PENDING','RUNNING','READY','FAILED','MISSED','SKIPPED')),
  attempt integer NOT NULL DEFAULT 0 CHECK(attempt BETWEEN 0 AND 20), next_attempt_at timestamptz NOT NULL DEFAULT now(),
  lease_token uuid, lease_expires_at timestamptz, access_checked_at timestamptz,
  report_link text NOT NULL DEFAULT '' CHECK(length(report_link)<=2048), failure_code text NOT NULL DEFAULT '' CHECK(failure_code ~ '^[A-Z0-9_]{0,127}$'),
  read_at timestamptz, created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE(id,tenant_id), UNIQUE(tenant_id,subscription_id,scheduled_for),
  FOREIGN KEY(schedule_id,domain_id,tenant_id) REFERENCES platform.report_schedules(id,domain_id,tenant_id) ON DELETE RESTRICT,
  FOREIGN KEY(subscription_id,tenant_id) REFERENCES platform.report_subscriptions(id,tenant_id) ON DELETE RESTRICT,
  FOREIGN KEY(report_id,tenant_id) REFERENCES platform.reports(id,tenant_id) ON DELETE RESTRICT,
  FOREIGN KEY(report_version_id,report_id,tenant_id) REFERENCES platform.report_versions(id,report_id,tenant_id) ON DELETE RESTRICT,
  FOREIGN KEY(recipient_user_id,tenant_id) REFERENCES platform.users(id,tenant_id) ON DELETE RESTRICT,
  CHECK((lease_token IS NULL)=(lease_expires_at IS NULL)),
  CHECK((state='RUNNING')=(lease_token IS NOT NULL)),
  CHECK((state='READY' AND report_link<>'' AND failure_code='' AND access_checked_at IS NOT NULL)
    OR (state IN ('FAILED','MISSED','SKIPPED') AND report_link='' AND failure_code<>'' AND access_checked_at IS NOT NULL)
    OR (state IN ('PENDING','RUNNING') AND report_link='' AND failure_code=''))
);

CREATE TABLE platform.report_delivery_events(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(), tenant_id uuid NOT NULL, domain_id uuid NOT NULL,
  schedule_id uuid NOT NULL, delivery_id uuid, event_type text NOT NULL CHECK(event_type ~ '^[A-Z][A-Z0-9_]{0,63}$'),
  actor_user_id uuid, failure_code text NOT NULL DEFAULT '' CHECK(failure_code ~ '^[A-Z0-9_]{0,127}$'),
  details_json jsonb NOT NULL DEFAULT '{}' CHECK(jsonb_typeof(details_json)='object' AND pg_column_size(details_json)<=32768),
  created_at timestamptz NOT NULL DEFAULT now(), UNIQUE(id,tenant_id),
  FOREIGN KEY(schedule_id,domain_id,tenant_id) REFERENCES platform.report_schedules(id,domain_id,tenant_id) ON DELETE RESTRICT,
  FOREIGN KEY(delivery_id,tenant_id) REFERENCES platform.report_deliveries(id,tenant_id) ON DELETE RESTRICT,
  FOREIGN KEY(actor_user_id,tenant_id) REFERENCES platform.users(id,tenant_id) ON DELETE RESTRICT
);

CREATE INDEX report_schedules_due_idx ON platform.report_schedules(tenant_id,next_run_at,id) WHERE state='ACTIVE';
CREATE INDEX report_subscriptions_recipient_idx ON platform.report_subscriptions(tenant_id,domain_id,recipient_user_id,state,updated_at DESC,id);
CREATE INDEX report_deliveries_recipient_idx ON platform.report_deliveries(tenant_id,domain_id,recipient_user_id,created_at DESC,id);
CREATE INDEX report_deliveries_claim_idx ON platform.report_deliveries(tenant_id,next_attempt_at,created_at,id)
  WHERE state IN ('PENDING','RUNNING','FAILED');
CREATE INDEX report_delivery_events_subject_idx ON platform.report_delivery_events(tenant_id,schedule_id,created_at,id);

CREATE OR REPLACE FUNCTION platform.reject_report_delivery_event_mutation() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN RAISE EXCEPTION 'report delivery events are append-only' USING ERRCODE='55000'; END $$;
CREATE TRIGGER report_delivery_events_immutable BEFORE UPDATE OR DELETE ON platform.report_delivery_events
FOR EACH ROW EXECUTE FUNCTION platform.reject_report_delivery_event_mutation();
REVOKE ALL ON FUNCTION platform.reject_report_delivery_event_mutation() FROM PUBLIC;

CREATE OR REPLACE FUNCTION platform.report_schedule_work_tenants() RETURNS TABLE(tenant_id uuid)
LANGUAGE sql SECURITY DEFINER SET search_path=pg_catalog,platform AS $$
 SELECT tenant.id FROM platform.tenants AS tenant
 WHERE tenant.status='ACTIVE' AND tenant.deleted_at IS NULL AND (
   EXISTS(SELECT 1 FROM platform.report_schedules AS schedule WHERE schedule.tenant_id=tenant.id AND schedule.state='ACTIVE' AND schedule.next_run_at<=now())
   OR EXISTS(SELECT 1 FROM platform.report_deliveries AS delivery WHERE delivery.tenant_id=tenant.id AND delivery.state IN ('PENDING','RUNNING','FAILED') AND delivery.next_attempt_at<=now())
 ) ORDER BY tenant.id
$$;
REVOKE ALL ON FUNCTION platform.report_schedule_work_tenants() FROM PUBLIC;

DO $$ DECLARE table_name text; BEGIN
  FOREACH table_name IN ARRAY ARRAY['report_schedules','report_subscriptions','report_deliveries','report_delivery_events'] LOOP
    EXECUTE format('ALTER TABLE platform.%I ENABLE ROW LEVEL SECURITY',table_name);
    EXECUTE format('ALTER TABLE platform.%I FORCE ROW LEVEL SECURITY',table_name);
  END LOOP;
END $$;
CREATE POLICY report_schedules_access ON platform.report_schedules USING(
  tenant_id=platform.current_tenant_id() AND (platform.is_system_access() OR
  (domain_id=platform.current_domain_id() AND platform.report_v2_can_access(report_id,ARRAY['VIEW','EDIT']::text[]))))
  WITH CHECK(tenant_id=platform.current_tenant_id() AND (platform.is_system_access() OR
  (domain_id=platform.current_domain_id() AND platform.report_v2_can_access(report_id,ARRAY['EDIT']::text[]))));
CREATE POLICY report_subscriptions_access ON platform.report_subscriptions USING(
  tenant_id=platform.current_tenant_id() AND (platform.is_system_access() OR (domain_id=platform.current_domain_id() AND
  (recipient_user_id=platform.current_user_id() OR EXISTS(
    SELECT 1 FROM platform.report_schedules schedule WHERE schedule.tenant_id=report_subscriptions.tenant_id
      AND schedule.id=report_subscriptions.schedule_id AND platform.report_v2_can_access(schedule.report_id,ARRAY['EDIT']::text[]))))))
  WITH CHECK(tenant_id=platform.current_tenant_id() AND (platform.is_system_access() OR (domain_id=platform.current_domain_id() AND
  (recipient_user_id=platform.current_user_id() OR EXISTS(
    SELECT 1 FROM platform.report_schedules schedule WHERE schedule.tenant_id=report_subscriptions.tenant_id
      AND schedule.id=report_subscriptions.schedule_id AND platform.report_v2_can_access(schedule.report_id,ARRAY['EDIT']::text[]))))));
CREATE POLICY report_deliveries_access ON platform.report_deliveries USING(
  tenant_id=platform.current_tenant_id() AND (platform.is_system_access() OR (domain_id=platform.current_domain_id() AND
  (recipient_user_id=platform.current_user_id() OR platform.report_v2_can_access(report_id,ARRAY['EDIT']::text[])))))
  WITH CHECK(tenant_id=platform.current_tenant_id() AND platform.is_system_access());
CREATE POLICY report_delivery_events_access ON platform.report_delivery_events USING(
  tenant_id=platform.current_tenant_id() AND (platform.is_system_access() OR (domain_id=platform.current_domain_id() AND
  (EXISTS(SELECT 1 FROM platform.report_schedules schedule
    WHERE schedule.tenant_id=report_delivery_events.tenant_id AND schedule.id=report_delivery_events.schedule_id
      AND platform.report_v2_can_access(schedule.report_id,ARRAY['EDIT']::text[])) OR EXISTS(
    SELECT 1 FROM platform.report_deliveries delivery WHERE delivery.tenant_id=report_delivery_events.tenant_id
      AND delivery.id=report_delivery_events.delivery_id AND delivery.recipient_user_id=platform.current_user_id())))))
  WITH CHECK(tenant_id=platform.current_tenant_id() AND (platform.is_system_access() OR domain_id=platform.current_domain_id()));

COMMENT ON TABLE platform.report_deliveries IS 'Permission-rechecked in-app links only; no data attachment and no external channel';
