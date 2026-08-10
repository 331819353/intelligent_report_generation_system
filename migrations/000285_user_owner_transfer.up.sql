ALTER TABLE platform.datasets ADD COLUMN owner_user_id uuid;
UPDATE platform.datasets SET owner_user_id=created_by WHERE owner_user_id IS NULL;
ALTER TABLE platform.datasets ALTER COLUMN owner_user_id SET NOT NULL;
ALTER TABLE platform.datasets ADD CONSTRAINT platform_datasets_owner_fk FOREIGN KEY(owner_user_id,tenant_id)
  REFERENCES platform.users(id,tenant_id) ON DELETE RESTRICT;

CREATE OR REPLACE FUNCTION platform.default_dataset_owner() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN NEW.owner_user_id=COALESCE(NEW.owner_user_id,NEW.created_by,platform.current_user_id());RETURN NEW;END $$;
CREATE TRIGGER datasets_default_owner BEFORE INSERT ON platform.datasets FOR EACH ROW EXECUTE FUNCTION platform.default_dataset_owner();
REVOKE ALL ON FUNCTION platform.default_dataset_owner() FROM PUBLIC;

DROP POLICY IF EXISTS datasets_read_scope ON platform.datasets;
DROP POLICY IF EXISTS datasets_insert_scope ON platform.datasets;
DROP POLICY IF EXISTS datasets_update_scope ON platform.datasets;
DROP POLICY IF EXISTS datasets_delete_scope ON platform.datasets;
CREATE POLICY datasets_read_scope ON platform.datasets FOR SELECT USING(
  tenant_id=platform.current_tenant_id() AND platform.asset_can_read(domain_id,owner_user_id,sharing_scope));
CREATE POLICY datasets_insert_scope ON platform.datasets FOR INSERT WITH CHECK(
  tenant_id=platform.current_tenant_id() AND platform.asset_can_write(domain_id,owner_user_id));
CREATE POLICY datasets_update_scope ON platform.datasets FOR UPDATE USING(
  tenant_id=platform.current_tenant_id() AND platform.asset_can_write(domain_id,owner_user_id)) WITH CHECK(
  tenant_id=platform.current_tenant_id() AND platform.asset_can_write(domain_id,owner_user_id));
CREATE POLICY datasets_delete_scope ON platform.datasets FOR DELETE USING(
  tenant_id=platform.current_tenant_id() AND platform.asset_can_write(domain_id,owner_user_id));
CREATE OR REPLACE FUNCTION platform.dataset_can_read(asset_id uuid) RETURNS boolean LANGUAGE sql STABLE SECURITY DEFINER
SET search_path=pg_catalog,platform AS $$SELECT EXISTS(SELECT 1 FROM platform.datasets asset WHERE asset.id=asset_id
  AND platform.asset_can_read(asset.domain_id,asset.owner_user_id,asset.sharing_scope))$$;
CREATE OR REPLACE FUNCTION platform.dataset_can_write(asset_id uuid) RETURNS boolean LANGUAGE sql STABLE SECURITY DEFINER
SET search_path=pg_catalog,platform AS $$SELECT EXISTS(SELECT 1 FROM platform.datasets asset WHERE asset.id=asset_id
  AND platform.asset_can_write(asset.domain_id,asset.owner_user_id))$$;

CREATE TABLE platform.user_lifecycle_batches(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),tenant_id uuid NOT NULL,target_user_id uuid NOT NULL,requested_by uuid NOT NULL,
  status text NOT NULL CHECK(status IN ('PLANNED','EXECUTING','COMPLETED','TRANSFER_FAILED')),
  plan_hash text NOT NULL CHECK(plan_hash ~ '^[0-9a-f]{64}$'),failure_code text NOT NULL DEFAULT '' CHECK(failure_code ~ '^[A-Z0-9_]{0,127}$'),
  record_version bigint NOT NULL DEFAULT 1 CHECK(record_version>0),created_at timestamptz NOT NULL DEFAULT now(),updated_at timestamptz NOT NULL DEFAULT now(),completed_at timestamptz,
  UNIQUE(id,tenant_id),FOREIGN KEY(target_user_id,tenant_id) REFERENCES platform.users(id,tenant_id) ON DELETE RESTRICT,
  FOREIGN KEY(requested_by,tenant_id) REFERENCES platform.users(id,tenant_id) ON DELETE RESTRICT,
  CHECK((status='COMPLETED')=(completed_at IS NOT NULL)),CHECK(status='TRANSFER_FAILED' OR failure_code='')
);
CREATE TABLE platform.user_lifecycle_batch_items(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),tenant_id uuid NOT NULL,batch_id uuid NOT NULL,domain_id uuid,
  category text NOT NULL CHECK(category ~ '^[A-Z][A-Z0-9_]{0,63}$'),object_id uuid NOT NULL,disposition text NOT NULL CHECK(disposition IN ('TRANSFER','AUTO_CLOSE','READ_ONLY','BLOCK')),
  receiver_user_id uuid,source_version text NOT NULL CHECK(length(source_version) BETWEEN 1 AND 128),executed_at timestamptz,
  UNIQUE(tenant_id,batch_id,category,object_id),FOREIGN KEY(batch_id,tenant_id) REFERENCES platform.user_lifecycle_batches(id,tenant_id) ON DELETE RESTRICT,
  FOREIGN KEY(domain_id,tenant_id) REFERENCES platform.business_domains(id,tenant_id) ON DELETE RESTRICT,
  FOREIGN KEY(receiver_user_id,tenant_id) REFERENCES platform.users(id,tenant_id) ON DELETE RESTRICT,
  CHECK((disposition='TRANSFER')=(receiver_user_id IS NOT NULL))
);
CREATE TABLE platform.user_lifecycle_events(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),tenant_id uuid NOT NULL,batch_id uuid NOT NULL,event_type text NOT NULL CHECK(event_type ~ '^[A-Z][A-Z0-9_]{0,63}$'),
  actor_user_id uuid NOT NULL,details_json jsonb NOT NULL DEFAULT '{}' CHECK(jsonb_typeof(details_json)='object' AND pg_column_size(details_json)<=32768),created_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE(id,tenant_id),FOREIGN KEY(batch_id,tenant_id) REFERENCES platform.user_lifecycle_batches(id,tenant_id) ON DELETE RESTRICT,
  FOREIGN KEY(actor_user_id,tenant_id) REFERENCES platform.users(id,tenant_id) ON DELETE RESTRICT
);
CREATE INDEX user_lifecycle_batches_target_idx ON platform.user_lifecycle_batches(tenant_id,target_user_id,created_at DESC,id);
CREATE INDEX user_lifecycle_items_batch_idx ON platform.user_lifecycle_batch_items(tenant_id,batch_id,domain_id,category,object_id);
ALTER TABLE platform.user_lifecycle_batches ENABLE ROW LEVEL SECURITY;ALTER TABLE platform.user_lifecycle_batches FORCE ROW LEVEL SECURITY;
ALTER TABLE platform.user_lifecycle_batch_items ENABLE ROW LEVEL SECURITY;ALTER TABLE platform.user_lifecycle_batch_items FORCE ROW LEVEL SECURITY;
ALTER TABLE platform.user_lifecycle_events ENABLE ROW LEVEL SECURITY;ALTER TABLE platform.user_lifecycle_events FORCE ROW LEVEL SECURITY;
CREATE POLICY user_lifecycle_batches_admin ON platform.user_lifecycle_batches USING(
  tenant_id=platform.current_tenant_id() AND (platform.is_system_access() OR platform.user_is_platform_administrator()))
  WITH CHECK(tenant_id=platform.current_tenant_id() AND (platform.is_system_access() OR platform.user_is_platform_administrator()));
CREATE POLICY user_lifecycle_items_admin ON platform.user_lifecycle_batch_items USING(
  tenant_id=platform.current_tenant_id() AND (platform.is_system_access() OR platform.user_is_platform_administrator()))
  WITH CHECK(tenant_id=platform.current_tenant_id() AND (platform.is_system_access() OR platform.user_is_platform_administrator()));
CREATE POLICY user_lifecycle_events_admin ON platform.user_lifecycle_events USING(
  tenant_id=platform.current_tenant_id() AND (platform.is_system_access() OR platform.user_is_platform_administrator()))
  WITH CHECK(tenant_id=platform.current_tenant_id() AND (platform.is_system_access() OR platform.user_is_platform_administrator()));

CREATE OR REPLACE FUNCTION platform.user_has_open_responsibility(selected_user_id uuid) RETURNS boolean
LANGUAGE sql STABLE SECURITY DEFINER SET search_path=pg_catalog,platform,askdata,decision AS $$
 SELECT EXISTS(SELECT 1 FROM platform.data_sources WHERE tenant_id=platform.current_tenant_id() AND owner_user_id=selected_user_id AND status<>'DELETED')
 OR EXISTS(SELECT 1 FROM platform.datasets WHERE tenant_id=platform.current_tenant_id() AND owner_user_id=selected_user_id AND deleted_at IS NULL)
 OR EXISTS(SELECT 1 FROM askdata.saved_questions WHERE tenant_id=platform.current_tenant_id() AND owner_user_id=selected_user_id AND status='ACTIVE')
 OR EXISTS(SELECT 1 FROM platform.reports WHERE tenant_id=platform.current_tenant_id() AND owner_user_id=selected_user_id)
 OR EXISTS(SELECT 1 FROM platform.report_schedules WHERE tenant_id=platform.current_tenant_id() AND owner_user_id=selected_user_id AND state<>'DISABLED')
 OR EXISTS(SELECT 1 FROM askdata.feedback_tickets WHERE tenant_id=platform.current_tenant_id() AND owner_user_id=selected_user_id AND status NOT IN('REJECTED','CLOSED'))
 OR EXISTS(SELECT 1 FROM platform.data_requests WHERE tenant_id=platform.current_tenant_id() AND assignee_user_id=selected_user_id AND state IN('IN_PROGRESS','DELIVERED'))
 OR EXISTS(SELECT 1 FROM decision.decisions WHERE tenant_id=platform.current_tenant_id() AND owner_user_id=selected_user_id AND status NOT IN('CLOSED','CANCELED'))
 OR EXISTS(SELECT 1 FROM decision.action_items WHERE tenant_id=platform.current_tenant_id() AND assignee_user_id=selected_user_id AND status NOT IN('DONE','CANCELED'))
 OR EXISTS(SELECT 1 FROM askdata.kpi_bundles WHERE tenant_id=platform.current_tenant_id() AND owner_user_id=selected_user_id)
 OR EXISTS(SELECT 1 FROM askdata.time_contracts WHERE tenant_id=platform.current_tenant_id() AND owner_user_id=selected_user_id)
 OR EXISTS(SELECT 1 FROM askdata.metrics WHERE tenant_id=platform.current_tenant_id() AND owner_id=selected_user_id)
 OR EXISTS(SELECT 1 FROM askdata.dimensions WHERE tenant_id=platform.current_tenant_id() AND owner_id=selected_user_id)
 OR EXISTS(SELECT 1 FROM askdata.relationships WHERE tenant_id=platform.current_tenant_id() AND owner_id=selected_user_id)
 OR EXISTS(SELECT 1 FROM askdata.business_term_versions WHERE tenant_id=platform.current_tenant_id() AND owner_id=selected_user_id)
 OR EXISTS(SELECT 1 FROM askdata.certified_example_versions WHERE tenant_id=platform.current_tenant_id() AND owner_id=selected_user_id)
 OR EXISTS(SELECT 1 FROM platform.data_requests WHERE tenant_id=platform.current_tenant_id() AND selected_user_id=ANY(approver_user_ids) AND state='SUBMITTED')
 OR EXISTS(SELECT 1 FROM decision.decision_approvals WHERE tenant_id=platform.current_tenant_id() AND approver_user_id=selected_user_id AND status='PENDING')
 OR EXISTS(SELECT 1 FROM platform.report_subscriptions WHERE tenant_id=platform.current_tenant_id() AND recipient_user_id=selected_user_id AND state='ACTIVE')
 OR EXISTS(SELECT 1 FROM platform.report_deliveries WHERE tenant_id=platform.current_tenant_id() AND recipient_user_id=selected_user_id AND state IN('PENDING','RUNNING','FAILED'))
 OR EXISTS(SELECT 1 FROM platform.user_roles assignment JOIN platform.roles role ON role.tenant_id=assignment.tenant_id AND role.id=assignment.role_id WHERE assignment.tenant_id=platform.current_tenant_id() AND assignment.user_id=selected_user_id AND role.code::text='platform_admin' AND role.status='ACTIVE' AND role.deleted_at IS NULL)
$$;
CREATE OR REPLACE FUNCTION platform.guard_user_disable_responsibility() RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER
SET search_path=pg_catalog,platform,askdata,decision AS $$
BEGIN IF OLD.status<>'DISABLED' AND NEW.status='DISABLED' AND platform.user_has_open_responsibility(OLD.id)
 THEN RAISE EXCEPTION 'USER_RESPONSIBILITY_TRANSFER_REQUIRED' USING ERRCODE='55000';END IF;RETURN NEW;END $$;
CREATE TRIGGER users_guard_responsibility BEFORE UPDATE OF status ON platform.users FOR EACH ROW EXECUTE FUNCTION platform.guard_user_disable_responsibility();
REVOKE ALL ON FUNCTION platform.user_has_open_responsibility(uuid),platform.guard_user_disable_responsibility() FROM PUBLIC;

CREATE OR REPLACE FUNCTION platform.reject_user_lifecycle_event_mutation() RETURNS trigger LANGUAGE plpgsql AS $$BEGIN RAISE EXCEPTION 'user lifecycle events are append-only' USING ERRCODE='55000';END$$;
CREATE TRIGGER user_lifecycle_events_immutable BEFORE UPDATE OR DELETE ON platform.user_lifecycle_events FOR EACH ROW EXECUTE FUNCTION platform.reject_user_lifecycle_event_mutation();
REVOKE ALL ON FUNCTION platform.reject_user_lifecycle_event_mutation() FROM PUBLIC;
