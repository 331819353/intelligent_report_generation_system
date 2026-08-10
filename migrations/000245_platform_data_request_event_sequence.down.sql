CREATE OR REPLACE FUNCTION platform.guard_data_request_event()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,platform
AS $$
DECLARE current_state text;
DECLARE latest_state text;
DECLARE requester_id uuid;
BEGIN
  IF TG_OP<>'INSERT' THEN
    RAISE EXCEPTION 'data request events are append only' USING ERRCODE='23514';
  END IF;
  SELECT request.state,request.requester_user_id
  INTO current_state,requester_id
  FROM platform.data_requests AS request
  WHERE request.id=NEW.data_request_id AND request.tenant_id=NEW.tenant_id
    AND request.domain_id=NEW.domain_id;
  SELECT event.to_state INTO latest_state
  FROM platform.data_request_events AS event
  WHERE event.data_request_id=NEW.data_request_id AND event.tenant_id=NEW.tenant_id
  ORDER BY event.created_at DESC,event.id DESC LIMIT 1;
  IF NEW.to_state<>current_state OR NEW.actor_user_id IS DISTINCT FROM platform.current_user_id()
    OR (latest_state IS NULL AND (NEW.from_state IS NOT NULL OR NEW.to_state<>'DRAFT'
      OR NEW.actor_user_id<>requester_id))
    OR (latest_state IS NOT NULL AND (NEW.from_state IS NULL OR NEW.from_state<>latest_state
      OR NEW.to_state=NEW.from_state)) THEN
    RAISE EXCEPTION 'data request event chain is invalid' USING ERRCODE='23514';
  END IF;
  RETURN NEW;
END
$$;

REVOKE ALL ON FUNCTION platform.guard_data_request_event() FROM PUBLIC;

ALTER TABLE platform.data_request_events
  DROP CONSTRAINT platform_data_request_events_request_sequence_key,
  DROP CONSTRAINT platform_data_request_events_sequence_check,
  DROP COLUMN sequence_no;
