CREATE TABLE askdata.idempotency_records(
  id uuid PRIMARY KEY,
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id) ON DELETE RESTRICT,
  actor_id uuid NOT NULL,
  endpoint text NOT NULL CHECK(
    length(endpoint) BETWEEN 1 AND 512 AND endpoint=btrim(endpoint)
    AND endpoint !~ '[[:cntrl:]]'
  ),
  idempotency_key text NOT NULL CHECK(
    length(idempotency_key) BETWEEN 1 AND 256
    AND idempotency_key=btrim(idempotency_key)
    AND idempotency_key !~ '[[:cntrl:]]'
  ),
  request_hash text NOT NULL CHECK(request_hash ~ '^[0-9a-f]{64}$'),
  response_status integer CHECK(response_status BETWEEN 100 AND 599),
  response_body bytea CHECK(response_body IS NULL OR octet_length(response_body)<=2097152),
  response_hash text CHECK(response_hash IS NULL OR response_hash ~ '^[0-9a-f]{64}$'),
  state text NOT NULL CHECK(state IN ('IN_FLIGHT','COMPLETED')),
  created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
  expires_at timestamptz NOT NULL,
  CONSTRAINT askdata_idempotency_records_actor_fk
    FOREIGN KEY(actor_id,tenant_id) REFERENCES platform.users(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT askdata_idempotency_records_key UNIQUE(
    tenant_id,actor_id,endpoint,idempotency_key
  ),
  CONSTRAINT askdata_idempotency_records_shape_check CHECK(
    expires_at>created_at AND expires_at<=created_at+interval '24 hours 1 second'
    AND (
      state='IN_FLIGHT' AND response_status IS NULL
      AND response_body IS NULL AND response_hash IS NULL
      OR state='COMPLETED' AND response_status IS NOT NULL
      AND response_body IS NOT NULL AND response_hash IS NOT NULL
    )
  )
);

CREATE INDEX askdata_idempotency_records_expiry_idx
  ON askdata.idempotency_records(expires_at,id);

CREATE OR REPLACE FUNCTION askdata.enforce_idempotency_record()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,askdata
AS $$
DECLARE parsed jsonb;
BEGIN
  IF TG_OP='INSERT' THEN
    IF NEW.state<>'IN_FLIGHT' OR NEW.response_body IS NOT NULL THEN
      RAISE EXCEPTION 'idempotency record must begin in flight' USING ERRCODE='23514';
    END IF;
    RETURN NEW;
  END IF;
  IF TG_OP='DELETE' THEN
    IF OLD.state<>'IN_FLIGHT' AND OLD.expires_at>clock_timestamp() THEN
      RAISE EXCEPTION 'completed idempotency record is retained for 24 hours' USING ERRCODE='55000';
    END IF;
    RETURN OLD;
  END IF;
  IF NEW.id IS DISTINCT FROM OLD.id OR NEW.tenant_id IS DISTINCT FROM OLD.tenant_id
    OR NEW.actor_id IS DISTINCT FROM OLD.actor_id OR NEW.endpoint IS DISTINCT FROM OLD.endpoint
    OR NEW.idempotency_key IS DISTINCT FROM OLD.idempotency_key
    OR NEW.request_hash IS DISTINCT FROM OLD.request_hash
    OR NEW.created_at IS DISTINCT FROM OLD.created_at OR NEW.expires_at IS DISTINCT FROM OLD.expires_at
    OR OLD.state<>'IN_FLIGHT' OR NEW.state<>'COMPLETED' THEN
    RAISE EXCEPTION 'idempotency identity and completion are immutable' USING ERRCODE='55000';
  END IF;
  BEGIN
    parsed := convert_from(NEW.response_body,'UTF8')::jsonb;
  EXCEPTION WHEN OTHERS THEN
    RAISE EXCEPTION 'idempotency response must be valid UTF-8 JSON' USING ERRCODE='22023';
  END;
  IF parsed IS NULL OR NEW.response_hash IS DISTINCT FROM
      encode(public.digest(NEW.response_body,'sha256'),'hex') THEN
    RAISE EXCEPTION 'idempotency response hash mismatch' USING ERRCODE='23514';
  END IF;
  RETURN NEW;
END
$$;

REVOKE ALL ON FUNCTION askdata.enforce_idempotency_record() FROM PUBLIC;

CREATE TRIGGER askdata_idempotency_records_lifecycle
BEFORE INSERT OR UPDATE OR DELETE ON askdata.idempotency_records
FOR EACH ROW EXECUTE FUNCTION askdata.enforce_idempotency_record();

ALTER TABLE askdata.idempotency_records ENABLE ROW LEVEL SECURITY;
ALTER TABLE askdata.idempotency_records FORCE ROW LEVEL SECURITY;
CREATE POLICY askdata_idempotency_records_actor_isolation
  ON askdata.idempotency_records
  USING(
    askdata.tenant_matches(tenant_id)
    AND (askdata.system_access() OR actor_id=askdata.current_actor_id())
  )
  WITH CHECK(
    askdata.tenant_matches(tenant_id)
    AND (askdata.system_access() OR actor_id=askdata.current_actor_id())
  );

COMMENT ON TABLE askdata.idempotency_records IS
  'Actor-scoped 24-hour replay records for governed write endpoints; 5xx and panic paths release IN_FLIGHT rows';
