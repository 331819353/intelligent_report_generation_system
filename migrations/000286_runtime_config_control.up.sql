CREATE TABLE platform.runtime_config_versions(
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),tenant_id uuid NOT NULL,scope_type text NOT NULL CHECK(scope_type IN('TENANT','DOMAIN','WORKER')),
 scope_id text NOT NULL CHECK(length(btrim(scope_id)) BETWEEN 1 AND 128),version_no integer NOT NULL CHECK(version_no>0),
 base_version_id uuid,config_json jsonb NOT NULL CHECK(jsonb_typeof(config_json)='object' AND pg_column_size(config_json)<=65536),
 config_hash text NOT NULL CHECK(config_hash ~ '^[0-9a-f]{64}$'),state text NOT NULL DEFAULT 'DRAFT' CHECK(state IN('DRAFT','IN_REVIEW','APPROVED','ROLLING_OUT','ACTIVE','FAILED','SUPERSEDED','ROLLED_BACK')),
 compatibility text NOT NULL CHECK(compatibility IN('HOT_RELOAD','NEXT_RESTART')),impact_summary text NOT NULL CHECK(length(impact_summary)<=2000),
 created_by uuid NOT NULL,approved_by uuid,record_version bigint NOT NULL DEFAULT 1 CHECK(record_version>0),
 created_at timestamptz NOT NULL DEFAULT now(),updated_at timestamptz NOT NULL DEFAULT now(),submitted_at timestamptz,approved_at timestamptz,activated_at timestamptz,
 UNIQUE(id,tenant_id),UNIQUE(tenant_id,scope_type,scope_id,version_no),FOREIGN KEY(base_version_id,tenant_id) REFERENCES platform.runtime_config_versions(id,tenant_id) ON DELETE RESTRICT,
 FOREIGN KEY(created_by,tenant_id) REFERENCES platform.users(id,tenant_id) ON DELETE RESTRICT,FOREIGN KEY(approved_by,tenant_id) REFERENCES platform.users(id,tenant_id) ON DELETE RESTRICT,
 CHECK((state IN('APPROVED','ROLLING_OUT','ACTIVE','SUPERSEDED','ROLLED_BACK'))=(approved_by IS NOT NULL AND approved_at IS NOT NULL) OR state='FAILED'),
 CHECK(approved_by IS NULL OR approved_by<>created_by)
);
CREATE TABLE platform.runtime_config_effective(
 tenant_id uuid NOT NULL,scope_type text NOT NULL,scope_id text NOT NULL,version_id uuid NOT NULL,updated_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(tenant_id,scope_type,scope_id),FOREIGN KEY(version_id,tenant_id) REFERENCES platform.runtime_config_versions(id,tenant_id) ON DELETE RESTRICT
);
CREATE TABLE platform.runtime_config_rollout_nodes(
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),tenant_id uuid NOT NULL,version_id uuid NOT NULL,ordinal integer NOT NULL CHECK(ordinal>0),
 consumer_type text NOT NULL CHECK(consumer_type IN('API','WORKER','ASKDATA_WORKER','REPORT_WORKER')),
 state text NOT NULL DEFAULT 'PENDING' CHECK(state IN('PENDING','APPLIED','WAITING_RESTART','FAILED','CANCELED')),
 expected_hash text NOT NULL CHECK(expected_hash ~ '^[0-9a-f]{64}$'),applied_hash text NOT NULL DEFAULT '' CHECK(applied_hash='' OR applied_hash ~ '^[0-9a-f]{64}$'),
 failure_code text NOT NULL DEFAULT '' CHECK(failure_code ~ '^[A-Z0-9_]{0,127}$'),attempt integer NOT NULL DEFAULT 0 CHECK(attempt BETWEEN 0 AND 20),
 created_at timestamptz NOT NULL DEFAULT now(),updated_at timestamptz NOT NULL DEFAULT now(),applied_at timestamptz,
 UNIQUE(id,tenant_id),UNIQUE(tenant_id,version_id,consumer_type),FOREIGN KEY(version_id,tenant_id) REFERENCES platform.runtime_config_versions(id,tenant_id) ON DELETE RESTRICT,
 CHECK((state='APPLIED')=(applied_hash=expected_hash AND applied_at IS NOT NULL)),CHECK(state='FAILED' OR failure_code='')
);
CREATE TABLE platform.runtime_config_events(
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),tenant_id uuid NOT NULL,version_id uuid NOT NULL,event_type text NOT NULL CHECK(event_type ~ '^[A-Z][A-Z0-9_]{0,63}$'),
 actor_user_id uuid,details_json jsonb NOT NULL DEFAULT '{}' CHECK(jsonb_typeof(details_json)='object' AND pg_column_size(details_json)<=32768),created_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(id,tenant_id),FOREIGN KEY(version_id,tenant_id) REFERENCES platform.runtime_config_versions(id,tenant_id) ON DELETE RESTRICT,
 FOREIGN KEY(actor_user_id,tenant_id) REFERENCES platform.users(id,tenant_id) ON DELETE RESTRICT
);
CREATE INDEX runtime_config_versions_scope_idx ON platform.runtime_config_versions(tenant_id,scope_type,scope_id,version_no DESC);
CREATE INDEX runtime_config_rollout_claim_idx ON platform.runtime_config_rollout_nodes(tenant_id,state,ordinal,created_at,id) WHERE state='PENDING';
DO $$DECLARE n text;BEGIN FOREACH n IN ARRAY ARRAY['runtime_config_versions','runtime_config_effective','runtime_config_rollout_nodes','runtime_config_events'] LOOP EXECUTE format('ALTER TABLE platform.%I ENABLE ROW LEVEL SECURITY',n);EXECUTE format('ALTER TABLE platform.%I FORCE ROW LEVEL SECURITY',n);EXECUTE format('CREATE POLICY %I ON platform.%I USING(tenant_id=platform.current_tenant_id() AND (platform.is_system_access() OR platform.user_is_platform_administrator())) WITH CHECK(tenant_id=platform.current_tenant_id() AND (platform.is_system_access() OR platform.user_is_platform_administrator()))',n||'_admin',n);END LOOP;END$$;
CREATE OR REPLACE FUNCTION platform.reject_runtime_config_event_mutation()RETURNS trigger LANGUAGE plpgsql AS $$BEGIN RAISE EXCEPTION 'runtime config events are append-only' USING ERRCODE='55000';END$$;
CREATE TRIGGER runtime_config_events_immutable BEFORE UPDATE OR DELETE ON platform.runtime_config_events FOR EACH ROW EXECUTE FUNCTION platform.reject_runtime_config_event_mutation();
REVOKE ALL ON FUNCTION platform.reject_runtime_config_event_mutation() FROM PUBLIC;
CREATE OR REPLACE FUNCTION platform.runtime_config_rollout_tenants() RETURNS TABLE(tenant_id uuid) LANGUAGE sql SECURITY DEFINER SET search_path=pg_catalog,platform AS $$
 SELECT DISTINCT node.tenant_id FROM platform.runtime_config_rollout_nodes AS node JOIN platform.runtime_config_versions AS version ON version.tenant_id=node.tenant_id AND version.id=node.version_id WHERE node.state='PENDING' AND version.state='ROLLING_OUT' ORDER BY node.tenant_id
$$;
REVOKE ALL ON FUNCTION platform.runtime_config_rollout_tenants() FROM PUBLIC;
COMMENT ON TABLE platform.runtime_config_versions IS 'Validated non-secret online runtime configuration; deployment secrets and infrastructure remain outside this table';
