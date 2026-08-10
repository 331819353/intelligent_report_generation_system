-- Recipients may acknowledge their own in-app delivery, but every delivery
-- lifecycle field remains worker-owned.
CREATE OR REPLACE FUNCTION platform.guard_report_delivery_user_mutation()
RETURNS trigger LANGUAGE plpgsql SET search_path=pg_catalog,platform AS $$
BEGIN
  IF platform.is_system_access() THEN
    RETURN NEW;
  END IF;
  IF OLD.tenant_id<>platform.current_tenant_id()
     OR OLD.domain_id<>platform.current_domain_id()
     OR OLD.recipient_user_id<>platform.current_user_id()
     OR OLD.state<>'READY'
     OR NEW.read_at IS NULL
     OR NEW.read_at<OLD.created_at
     OR ROW(NEW.id,NEW.tenant_id,NEW.domain_id,NEW.schedule_id,
            NEW.subscription_id,NEW.report_id,NEW.report_version_id,
            NEW.recipient_user_id,NEW.scheduled_for,NEW.channel,NEW.state,
            NEW.attempt,NEW.next_attempt_at,NEW.lease_token,
            NEW.lease_expires_at,NEW.access_checked_at,NEW.report_link,
            NEW.failure_code,NEW.created_at)
        IS DISTINCT FROM
        ROW(OLD.id,OLD.tenant_id,OLD.domain_id,OLD.schedule_id,
            OLD.subscription_id,OLD.report_id,OLD.report_version_id,
            OLD.recipient_user_id,OLD.scheduled_for,OLD.channel,OLD.state,
            OLD.attempt,OLD.next_attempt_at,OLD.lease_token,
            OLD.lease_expires_at,OLD.access_checked_at,OLD.report_link,
            OLD.failure_code,OLD.created_at)
     OR (OLD.read_at IS NOT NULL AND NEW.read_at<>OLD.read_at) THEN
    RAISE EXCEPTION 'recipient may only acknowledge a ready report delivery'
      USING ERRCODE='42501';
  END IF;
  RETURN NEW;
END
$$;

CREATE TRIGGER report_deliveries_user_mutation_guard
BEFORE UPDATE ON platform.report_deliveries
FOR EACH ROW EXECUTE FUNCTION platform.guard_report_delivery_user_mutation();
REVOKE ALL ON FUNCTION platform.guard_report_delivery_user_mutation() FROM PUBLIC;

DROP POLICY report_deliveries_access ON platform.report_deliveries;
CREATE POLICY report_deliveries_access ON platform.report_deliveries USING(
  tenant_id=platform.current_tenant_id() AND (platform.is_system_access() OR
  (domain_id=platform.current_domain_id() AND
  (recipient_user_id=platform.current_user_id() OR
   platform.report_v2_can_access(report_id,ARRAY['EDIT']::text[])))))
WITH CHECK(
  tenant_id=platform.current_tenant_id() AND (platform.is_system_access() OR
  (domain_id=platform.current_domain_id() AND
   recipient_user_id=platform.current_user_id())));
