-- Give the append-only request audit an explicit monotonic order. Timestamps
-- are evidence, not a safe sequence key when several transitions share one
-- application clock tick.
ALTER TABLE platform.data_request_events
  ADD COLUMN sequence_no bigint;

WITH ranked AS (
  SELECT event.id,
    row_number() OVER(
      PARTITION BY event.data_request_id
      ORDER BY CASE event.to_state
        WHEN 'DRAFT' THEN 1
        WHEN 'SUBMITTED' THEN 2
        WHEN 'APPROVED' THEN 3
        WHEN 'REJECTED' THEN 3
        WHEN 'IN_PROGRESS' THEN 4
        WHEN 'DELIVERED' THEN 5
        WHEN 'CLOSED' THEN 6
        ELSE 99
      END,event.created_at,event.id
    ) AS sequence_no
  FROM platform.data_request_events AS event
)
UPDATE platform.data_request_events AS event
SET sequence_no=ranked.sequence_no
FROM ranked
WHERE ranked.id=event.id;

ALTER TABLE platform.data_request_events
  ALTER COLUMN sequence_no SET NOT NULL,
  ADD CONSTRAINT platform_data_request_events_sequence_check CHECK(sequence_no>0),
  ADD CONSTRAINT platform_data_request_events_request_sequence_key
    UNIQUE(data_request_id,sequence_no);

CREATE OR REPLACE FUNCTION platform.guard_data_request_event()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,platform
AS $$
DECLARE current_state text;
DECLARE current_version bigint;
DECLARE latest_state text;
DECLARE latest_sequence bigint;
DECLARE requester_id uuid;
BEGIN
  IF TG_OP<>'INSERT' THEN
    RAISE EXCEPTION 'data request events are append only' USING ERRCODE='23514';
  END IF;
  SELECT request.state,request.record_version,request.requester_user_id
  INTO current_state,current_version,requester_id
  FROM platform.data_requests AS request
  WHERE request.id=NEW.data_request_id AND request.tenant_id=NEW.tenant_id
    AND request.domain_id=NEW.domain_id;
  SELECT event.to_state,event.sequence_no INTO latest_state,latest_sequence
  FROM platform.data_request_events AS event
  WHERE event.data_request_id=NEW.data_request_id AND event.tenant_id=NEW.tenant_id
  ORDER BY event.sequence_no DESC LIMIT 1;
  IF NEW.to_state<>current_state OR NEW.sequence_no<>current_version
    OR NEW.actor_user_id IS DISTINCT FROM platform.current_user_id()
    OR (latest_sequence IS NULL AND (NEW.sequence_no<>1 OR NEW.from_state IS NOT NULL
      OR NEW.to_state<>'DRAFT' OR NEW.actor_user_id<>requester_id))
    OR (latest_sequence IS NOT NULL AND (NEW.sequence_no<>latest_sequence+1
      OR NEW.from_state IS NULL OR NEW.from_state<>latest_state
      OR NEW.to_state=NEW.from_state)) THEN
    RAISE EXCEPTION 'data request event chain is invalid' USING ERRCODE='23514';
  END IF;
  RETURN NEW;
END
$$;

REVOKE ALL ON FUNCTION platform.guard_data_request_event() FROM PUBLIC;

COMMENT ON COLUMN platform.data_request_events.sequence_no IS
  'Monotonic request record version used as the authoritative audit event order';
