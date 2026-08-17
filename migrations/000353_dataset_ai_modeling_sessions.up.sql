-- Dataset AI modeling sessions: the persisted source of truth for a conversational
-- modeling workflow.
--
-- Until now every AI planning turn was stateless: the user's confirmed decisions
-- (dataset type, table scope, clarification answers) survived only as prose stitched
-- into the next instruction, and disappeared on reload. The session document makes
-- those decisions durable, structured facts that the server injects into the intent
-- and planner prompts as trusted context, and lets the catalog loader *enforce* a
-- confirmed CREATE scope instead of hoping the model honors instruction text.
--
-- The aggregate is a per-actor working document with append-mostly semantics and no
-- cross-row query needs beyond "my active session for this dataset", so it is stored
-- as one JSONB document with key columns, matching how dataset DSL documents are
-- persisted. `revision` provides optimistic concurrency for the reload-and-reapply
-- update loop; there is no partial update path.
BEGIN;

CREATE TABLE platform.dataset_ai_modeling_sessions(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id),
  actor_id uuid NOT NULL,
  -- NULL while the dataset has never been saved; the client keeps the session id in
  -- memory for that case and a reload legitimately starts a fresh session.
  dataset_id uuid,
  status text NOT NULL DEFAULT 'ACTIVE' CHECK(status IN ('ACTIVE','CLOSED')),
  document jsonb NOT NULL DEFAULT '{}'::jsonb,
  revision bigint NOT NULL DEFAULT 1 CHECK(revision >= 1),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT dataset_ai_modeling_sessions_actor_fk
    FOREIGN KEY(actor_id,tenant_id)
    REFERENCES platform.users(id,tenant_id) ON DELETE CASCADE,
  CONSTRAINT dataset_ai_modeling_sessions_dataset_fk
    FOREIGN KEY(dataset_id,tenant_id)
    REFERENCES platform.datasets(id,tenant_id) ON DELETE CASCADE
);

COMMENT ON TABLE platform.dataset_ai_modeling_sessions IS
  'Per-actor conversational modeling state for dataset AI: goal, confirmed type/table scope, clarification Q&A, and proposal lifecycle';
COMMENT ON COLUMN platform.dataset_ai_modeling_sessions.document IS
  'ModelingSessionState JSON: goal, modelKind(+source), confirmed scope, clarifications, proposals';
COMMENT ON COLUMN platform.dataset_ai_modeling_sessions.revision IS
  'Optimistic concurrency token; every update must present the revision it read';

-- One live session per actor and saved dataset. Creating a new one closes the old
-- one in the same transaction (service-level takeover), so the index only has to
-- hold the invariant, not resolve races by itself.
CREATE UNIQUE INDEX dataset_ai_modeling_sessions_active_dataset_key
  ON platform.dataset_ai_modeling_sessions(tenant_id,actor_id,dataset_id)
  WHERE status='ACTIVE' AND dataset_id IS NOT NULL;
CREATE INDEX dataset_ai_modeling_sessions_actor_idx
  ON platform.dataset_ai_modeling_sessions(tenant_id,actor_id,updated_at DESC);

ALTER TABLE platform.dataset_ai_modeling_sessions ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform.dataset_ai_modeling_sessions FORCE ROW LEVEL SECURITY;
CREATE POLICY dataset_ai_modeling_sessions_tenant_isolation
  ON platform.dataset_ai_modeling_sessions
  USING(tenant_id=platform.current_tenant_id())
  WITH CHECK(tenant_id=platform.current_tenant_id());

COMMIT;
