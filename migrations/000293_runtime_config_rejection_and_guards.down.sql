DROP TRIGGER IF EXISTS runtime_config_rollout_mutation_guard
  ON platform.runtime_config_rollout_nodes;
DROP TRIGGER IF EXISTS runtime_config_versions_mutation_guard
  ON platform.runtime_config_versions;
DROP FUNCTION IF EXISTS platform.guard_runtime_config_rollout_mutation();
DROP FUNCTION IF EXISTS platform.guard_runtime_config_version_mutation();
DROP FUNCTION IF EXISTS platform.runtime_config_transition_allowed(text,text);

ALTER TABLE platform.runtime_config_versions
  DROP CONSTRAINT IF EXISTS runtime_config_versions_rejection_shape,
  DROP CONSTRAINT IF EXISTS runtime_config_versions_rejected_by_fk,
  DROP COLUMN IF EXISTS rejection_reason,
  DROP COLUMN IF EXISTS rejected_at,
  DROP COLUMN IF EXISTS rejected_by,
  DROP CONSTRAINT runtime_config_versions_state_check,
  ADD CONSTRAINT runtime_config_versions_state_check CHECK(state IN(
    'DRAFT','IN_REVIEW','APPROVED','ROLLING_OUT','ACTIVE','FAILED',
    'SUPERSEDED','ROLLED_BACK'));
