-- Report asset governance: append-only lifecycle timeline, guarded soft
-- archive/restore transitions and list/search indexes.
CREATE TABLE platform.report_asset_events(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id) ON DELETE RESTRICT,
  report_id uuid NOT NULL,
  event_type text NOT NULL CHECK(event_type IN (
    'CREATED','OWNER_CHANGED','PUBLISHED','ROLLED_BACK',
    'PERMISSION_GRANTED','PERMISSION_REVOKED',
    'ARCHIVED','RESTORED','SHARE_CREATED','SHARE_REVOKED'
  )),
  actor_user_id uuid,
  subject_type platform.permission_subject_type,
  subject_id uuid,
  action text,
  reason text,
  previous_status text CHECK(previous_status IS NULL OR previous_status IN ('ACTIVE','ARCHIVED')),
  new_status text CHECK(new_status IS NULL OR new_status IN ('ACTIVE','ARCHIVED')),
  payload_json jsonb NOT NULL DEFAULT '{}'::jsonb CHECK(jsonb_typeof(payload_json)='object'),
  created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
  CONSTRAINT report_asset_event_report_fk FOREIGN KEY(report_id,tenant_id)
    REFERENCES platform.reports(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT report_asset_event_actor_fk FOREIGN KEY(actor_user_id,tenant_id)
    REFERENCES platform.users(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT report_asset_event_shape_check CHECK(
    (event_type IN ('ARCHIVED','RESTORED'))=(reason IS NOT NULL)
    AND (reason IS NULL OR (
      length(btrim(reason)) BETWEEN 1 AND 1000
      AND reason=btrim(reason) AND reason !~ '[[:cntrl:]]'
    ))
    AND ((subject_type IS NULL AND subject_id IS NULL AND action IS NULL)
      OR (subject_type IS NOT NULL AND subject_id IS NOT NULL AND action IS NOT NULL))
    AND ((event_type IN ('ARCHIVED','RESTORED'))
      =(previous_status IS NOT NULL AND new_status IS NOT NULL))
  )
);

CREATE INDEX report_asset_events_timeline_idx
  ON platform.report_asset_events(tenant_id,report_id,created_at DESC,id DESC);
CREATE INDEX report_asset_domain_status_updated_idx
  ON platform.reports(tenant_id,domain_id,status,updated_at DESC,id DESC);
CREATE INDEX report_asset_domain_owner_updated_idx
  ON platform.reports(tenant_id,domain_id,owner_user_id,updated_at DESC,id DESC);
CREATE INDEX report_asset_name_search_idx
  ON platform.reports(tenant_id,domain_id,lower(name) text_pattern_ops);

CREATE OR REPLACE FUNCTION platform.reject_report_asset_event_mutation()
RETURNS trigger LANGUAGE plpgsql SET search_path=pg_catalog,platform AS $$
BEGIN
  RAISE EXCEPTION 'report asset events are append-only' USING ERRCODE='55000';
END
$$;
CREATE TRIGGER report_asset_events_immutable
BEFORE UPDATE OR DELETE ON platform.report_asset_events
FOR EACH ROW EXECUTE FUNCTION platform.reject_report_asset_event_mutation();

CREATE OR REPLACE FUNCTION platform.guard_report_asset_status_transition()
RETURNS trigger LANGUAGE plpgsql SET search_path=pg_catalog,platform AS $$
DECLARE transition_reason text;
BEGIN
  IF NEW.status IS NOT DISTINCT FROM OLD.status THEN
    RETURN NEW;
  END IF;
  transition_reason := current_setting('app.report_asset_reason',true);
  IF transition_reason IS NULL OR transition_reason<>btrim(transition_reason)
     OR length(transition_reason) NOT BETWEEN 1 AND 1000
     OR transition_reason ~ '[[:cntrl:]]' THEN
    RAISE EXCEPTION 'report asset status transition requires a valid reason'
      USING ERRCODE='23514';
  END IF;
  IF NOT (OLD.status='ACTIVE' AND NEW.status='ARCHIVED'
       OR OLD.status='ARCHIVED' AND NEW.status='ACTIVE') THEN
    RAISE EXCEPTION 'invalid report asset status transition' USING ERRCODE='23514';
  END IF;
  RETURN NEW;
END
$$;
CREATE TRIGGER report_asset_status_guard
BEFORE UPDATE OF status ON platform.reports
FOR EACH ROW EXECUTE FUNCTION platform.guard_report_asset_status_transition();

CREATE OR REPLACE FUNCTION platform.guard_report_permission_action()
RETURNS trigger LANGUAGE plpgsql SET search_path=pg_catalog,platform AS $$
BEGIN
  IF NEW.object_type='REPORT' AND NEW.action NOT IN (
    'VIEW','EDIT','PUBLISH','EXPORT','SHARE','AI_EDIT'
  ) THEN
    RAISE EXCEPTION 'invalid report object permission action' USING ERRCODE='23514';
  END IF;
  RETURN NEW;
END
$$;
CREATE TRIGGER report_object_permission_action_guard
BEFORE INSERT OR UPDATE OF object_type,action ON platform.object_permissions
FOR EACH ROW EXECUTE FUNCTION platform.guard_report_permission_action();

CREATE OR REPLACE FUNCTION platform.append_report_asset_projection_event()
RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER
SET search_path=pg_catalog,platform AS $$
DECLARE version_row platform.report_versions%ROWTYPE;
BEGIN
  IF TG_OP='INSERT' THEN
    INSERT INTO platform.report_asset_events(
      tenant_id,report_id,event_type,actor_user_id,payload_json,created_at
    ) VALUES(NEW.tenant_id,NEW.id,'CREATED',NEW.created_by,
      jsonb_build_object('code',NEW.code,'reportType',NEW.report_type),NEW.created_at);
    RETURN NEW;
  END IF;
  IF NEW.owner_user_id IS DISTINCT FROM OLD.owner_user_id THEN
    INSERT INTO platform.report_asset_events(
      tenant_id,report_id,event_type,actor_user_id,subject_type,subject_id,action,payload_json
    ) VALUES(NEW.tenant_id,NEW.id,'OWNER_CHANGED',platform.current_user_id(),
      'USER',NEW.owner_user_id,'OWNER',jsonb_build_object('previousOwnerUserId',OLD.owner_user_id));
  END IF;
  IF NEW.current_published_version_id IS DISTINCT FROM OLD.current_published_version_id
     AND NEW.current_published_version_id IS NOT NULL THEN
    SELECT * INTO version_row FROM platform.report_versions
      WHERE id=NEW.current_published_version_id AND report_id=NEW.id AND tenant_id=NEW.tenant_id;
    INSERT INTO platform.report_asset_events(
      tenant_id,report_id,event_type,actor_user_id,payload_json,created_at
    ) VALUES(NEW.tenant_id,NEW.id,
      CASE WHEN version_row.rollback_of_version_no IS NULL THEN 'PUBLISHED' ELSE 'ROLLED_BACK' END,
      version_row.published_by,
      jsonb_build_object('versionNo',version_row.version_no,
        'rollbackOfVersionNo',version_row.rollback_of_version_no),version_row.published_at);
  END IF;
  RETURN NEW;
END
$$;
CREATE TRIGGER report_asset_projection_events
AFTER INSERT OR UPDATE OF owner_user_id,current_published_version_id ON platform.reports
FOR EACH ROW EXECUTE FUNCTION platform.append_report_asset_projection_event();

CREATE OR REPLACE FUNCTION platform.append_report_share_asset_event()
RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER
SET search_path=pg_catalog,platform AS $$
BEGIN
  IF TG_OP='INSERT' THEN
    INSERT INTO platform.report_asset_events(
      tenant_id,report_id,event_type,actor_user_id,subject_type,subject_id,action,payload_json,created_at
    ) VALUES(NEW.tenant_id,NEW.report_id,'SHARE_CREATED',NEW.created_by,
      CASE WHEN NEW.share_type='INTERNAL_GROUP' THEN 'ROLE'::platform.permission_subject_type
           ELSE 'USER'::platform.permission_subject_type END,
      NEW.principal_id,'SHARE',jsonb_build_object('shareId',NEW.id,'shareType',NEW.share_type),NEW.created_at);
  ELSIF OLD.revoked_at IS NULL AND NEW.revoked_at IS NOT NULL THEN
    INSERT INTO platform.report_asset_events(
      tenant_id,report_id,event_type,actor_user_id,payload_json,created_at
    ) VALUES(NEW.tenant_id,NEW.report_id,'SHARE_REVOKED',platform.current_user_id(),
      jsonb_build_object('shareId',NEW.id),NEW.revoked_at);
  END IF;
  RETURN NEW;
END
$$;
CREATE TRIGGER report_share_asset_events
AFTER INSERT OR UPDATE OF revoked_at ON platform.report_shares
FOR EACH ROW EXECUTE FUNCTION platform.append_report_share_asset_event();

-- Existing reports predate the event table; seed a deterministic creation
-- event so every timeline has a stable beginning.
INSERT INTO platform.report_asset_events(
  tenant_id,report_id,event_type,actor_user_id,payload_json,created_at
)
SELECT tenant_id,id,'CREATED',created_by,
  jsonb_build_object('code',code,'reportType',report_type),created_at
FROM platform.reports;

ALTER TABLE platform.report_asset_events ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform.report_asset_events FORCE ROW LEVEL SECURITY;
CREATE POLICY report_asset_events_read ON platform.report_asset_events FOR SELECT
  USING(tenant_id=platform.current_tenant_id()
    AND platform.report_v2_can_access(report_id,ARRAY['VIEW','EDIT','PUBLISH']::text[]));
CREATE POLICY report_asset_events_insert ON platform.report_asset_events FOR INSERT
  WITH CHECK(tenant_id=platform.current_tenant_id()
    AND (platform.is_system_access() OR actor_user_id=platform.current_user_id())
    AND platform.report_v2_can_access(report_id,ARRAY['EDIT','PUBLISH']::text[]));

REVOKE ALL ON FUNCTION platform.reject_report_asset_event_mutation() FROM PUBLIC;
REVOKE ALL ON FUNCTION platform.guard_report_asset_status_transition() FROM PUBLIC;
REVOKE ALL ON FUNCTION platform.guard_report_permission_action() FROM PUBLIC;
REVOKE ALL ON FUNCTION platform.append_report_asset_projection_event() FROM PUBLIC;
REVOKE ALL ON FUNCTION platform.append_report_share_asset_event() FROM PUBLIC;
