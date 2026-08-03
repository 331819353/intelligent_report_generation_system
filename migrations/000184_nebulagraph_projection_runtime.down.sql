DROP TABLE IF EXISTS platform.semantic_graph_plan_cache;
DROP FUNCTION IF EXISTS platform.fail_semantic_nebula_projection(
  uuid,uuid,text,uuid,text,jsonb
);
DROP FUNCTION IF EXISTS platform.complete_semantic_nebula_projection(
  uuid,uuid,text,uuid,text,text,integer,jsonb
);
DROP FUNCTION IF EXISTS platform.heartbeat_semantic_nebula_projection(
  uuid,uuid,text,uuid,integer
);
DROP FUNCTION IF EXISTS platform.claim_semantic_nebula_projection(uuid,text,integer);
DROP FUNCTION IF EXISTS platform.list_semantic_nebula_projection_tenants();
DROP INDEX IF EXISTS platform.semantic_release_nebula_claim_idx;
ALTER TABLE platform.semantic_release_projections
  DROP CONSTRAINT IF EXISTS semantic_release_projections_lease_shape_check,
  DROP COLUMN IF EXISTS next_attempt_at,
  DROP COLUMN IF EXISTS max_attempts,
  DROP COLUMN IF EXISTS attempt,
  DROP COLUMN IF EXISTS lease_expires_at,
  DROP COLUMN IF EXISTS lease_token,
  DROP COLUMN IF EXISTS lease_owner;
