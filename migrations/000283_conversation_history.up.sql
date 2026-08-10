ALTER TABLE askdata.conversations
  ADD COLUMN is_pinned boolean NOT NULL DEFAULT false,
  ADD COLUMN archived_at timestamptz,
  ADD COLUMN record_version bigint NOT NULL DEFAULT 1 CHECK(record_version>0),
  ADD CONSTRAINT askdata_conversations_archive_shape_check CHECK(
    NOT is_pinned OR archived_at IS NULL
  );

CREATE INDEX askdata_conversations_history_idx ON askdata.conversations(
  tenant_id,domain_id,actor_id,is_pinned DESC,updated_at DESC,id
) WHERE archived_at IS NULL;

COMMENT ON COLUMN askdata.conversations.archived_at IS
  'Actor-controlled history visibility; durable runs and artifacts remain retained';
