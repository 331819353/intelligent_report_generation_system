ALTER TABLE platform.runtime_config_versions
  DROP CONSTRAINT runtime_config_versions_state_check,
  ADD CONSTRAINT runtime_config_versions_state_check CHECK(state IN(
    'DRAFT','IN_REVIEW','APPROVED','REJECTED','ROLLING_OUT','ACTIVE',
    'FAILED','SUPERSEDED','ROLLED_BACK')),
  ADD COLUMN rejected_by uuid,
  ADD COLUMN rejected_at timestamptz,
  ADD COLUMN rejection_reason text NOT NULL DEFAULT '',
  ADD CONSTRAINT runtime_config_versions_rejected_by_fk
    FOREIGN KEY(rejected_by,tenant_id) REFERENCES platform.users(id,tenant_id) ON DELETE RESTRICT,
  ADD CONSTRAINT runtime_config_versions_rejection_shape CHECK(
    (state='REJECTED')=(rejected_by IS NOT NULL AND rejected_at IS NOT NULL
      AND length(btrim(rejection_reason)) BETWEEN 1 AND 1000)
    AND (rejection_reason='' OR (rejection_reason=btrim(rejection_reason)
      AND rejection_reason !~ '[[:cntrl:]]'))
    AND (rejected_by IS NULL OR rejected_by<>created_by));

CREATE OR REPLACE FUNCTION platform.runtime_config_transition_allowed(previous_state text,next_state text)
RETURNS boolean LANGUAGE sql IMMUTABLE STRICT AS $$
 SELECT previous_state<>next_state AND CASE previous_state
  WHEN 'DRAFT' THEN next_state='IN_REVIEW'
  WHEN 'IN_REVIEW' THEN next_state IN('APPROVED','REJECTED')
  WHEN 'APPROVED' THEN next_state='ROLLING_OUT'
  WHEN 'ROLLING_OUT' THEN next_state IN('ACTIVE','FAILED')
  WHEN 'ACTIVE' THEN next_state IN('SUPERSEDED','ROLLED_BACK')
  WHEN 'SUPERSEDED' THEN next_state='ACTIVE'
  ELSE false END
$$;

CREATE OR REPLACE FUNCTION platform.guard_runtime_config_version_mutation()
RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,platform AS $$
BEGIN
 IF TG_OP='DELETE' THEN
  RAISE EXCEPTION 'runtime configuration history cannot be deleted' USING ERRCODE='55000';
 END IF;
 IF TG_OP='INSERT' THEN RETURN NEW; END IF;
 IF ROW(NEW.id,NEW.tenant_id,NEW.scope_type,NEW.scope_id,NEW.version_no,
        NEW.base_version_id,NEW.config_json,NEW.config_hash,NEW.compatibility,
        NEW.impact_summary,NEW.created_by,NEW.created_at)
    IS DISTINCT FROM
    ROW(OLD.id,OLD.tenant_id,OLD.scope_type,OLD.scope_id,OLD.version_no,
        OLD.base_version_id,OLD.config_json,OLD.config_hash,OLD.compatibility,
        OLD.impact_summary,OLD.created_by,OLD.created_at) THEN
  RAISE EXCEPTION 'runtime configuration facts are immutable' USING ERRCODE='55000';
 END IF;
 IF NEW.record_version<>OLD.record_version+1 OR NEW.updated_at<OLD.updated_at THEN
  RAISE EXCEPTION 'runtime configuration record version is stale' USING ERRCODE='40001';
 END IF;
 IF NOT platform.runtime_config_transition_allowed(OLD.state,NEW.state) THEN
  RAISE EXCEPTION 'invalid runtime configuration state transition' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END
$$;
CREATE TRIGGER runtime_config_versions_mutation_guard
BEFORE UPDATE OR DELETE ON platform.runtime_config_versions
FOR EACH ROW EXECUTE FUNCTION platform.guard_runtime_config_version_mutation();

CREATE OR REPLACE FUNCTION platform.guard_runtime_config_rollout_mutation()
RETURNS trigger LANGUAGE plpgsql SET search_path=pg_catalog,platform AS $$
BEGIN
 IF TG_OP='DELETE' THEN
  RAISE EXCEPTION 'runtime configuration rollout history cannot be deleted' USING ERRCODE='55000';
 END IF;
 IF ROW(NEW.id,NEW.tenant_id,NEW.version_id,NEW.ordinal,NEW.consumer_type,
        NEW.expected_hash,NEW.created_at)
    IS DISTINCT FROM
    ROW(OLD.id,OLD.tenant_id,OLD.version_id,OLD.ordinal,OLD.consumer_type,
        OLD.expected_hash,OLD.created_at) THEN
  RAISE EXCEPTION 'runtime rollout identity is immutable' USING ERRCODE='55000';
 END IF;
 IF NOT ((OLD.state='PENDING' AND NEW.state IN('APPLIED','WAITING_RESTART','FAILED','CANCELED'))
      OR (OLD.state='WAITING_RESTART' AND NEW.state='APPLIED')) THEN
  RAISE EXCEPTION 'invalid runtime rollout state transition' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END
$$;
CREATE TRIGGER runtime_config_rollout_mutation_guard
BEFORE UPDATE OR DELETE ON platform.runtime_config_rollout_nodes
FOR EACH ROW EXECUTE FUNCTION platform.guard_runtime_config_rollout_mutation();

REVOKE ALL ON FUNCTION platform.runtime_config_transition_allowed(text,text),
  platform.guard_runtime_config_version_mutation(),
  platform.guard_runtime_config_rollout_mutation() FROM PUBLIC;
