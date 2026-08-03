DROP TRIGGER IF EXISTS semantic_release_events_immutable
  ON platform.semantic_release_events;
DROP TRIGGER IF EXISTS semantic_release_state_set_updated_at
  ON platform.semantic_release_state;
DROP TRIGGER IF EXISTS semantic_release_projections_set_updated_at
  ON platform.semantic_release_projections;
DROP TRIGGER IF EXISTS semantic_releases_set_updated_at
  ON platform.semantic_releases;
DROP TRIGGER IF EXISTS tenants_initialize_semantic_release_state
  ON platform.tenants;
DROP FUNCTION IF EXISTS platform.reject_semantic_release_event_mutation();
DROP FUNCTION IF EXISTS platform.initialize_semantic_release_state();
DROP TABLE IF EXISTS platform.semantic_release_events;
DROP TABLE IF EXISTS platform.semantic_release_state;
DROP TABLE IF EXISTS platform.semantic_release_projections;
DROP TABLE IF EXISTS platform.semantic_release_objects;
DROP TABLE IF EXISTS platform.semantic_releases;
