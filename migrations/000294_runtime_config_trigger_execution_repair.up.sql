-- The guard invokes a non-public transition predicate and therefore must run
-- under the migration owner rather than the restricted runtime role.
ALTER FUNCTION platform.guard_runtime_config_version_mutation() SECURITY DEFINER;
