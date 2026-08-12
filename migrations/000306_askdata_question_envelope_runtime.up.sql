-- Short-lived encrypted question context for the asynchronous AskData worker.
-- Plaintext never reaches PostgreSQL: the API seals it with AES-GCM and the
-- worker opens it only while the pinned run is executable.

BEGIN;

CREATE TABLE askdata.question_envelopes(
  run_id uuid NOT NULL,
  tenant_id uuid NOT NULL,
  domain_id uuid NOT NULL,
  actor_id uuid NOT NULL,
  conversation_id uuid NOT NULL,
  release_id uuid NOT NULL,
  release_content_hash text NOT NULL CHECK(release_content_hash ~ '^[0-9a-f]{64}$'),
  policy_scope_hash text NOT NULL CHECK(policy_scope_hash ~ '^[0-9a-f]{64}$'),
  question_hash text NOT NULL CHECK(question_hash ~ '^[0-9a-f]{64}$'),
  envelope_json jsonb NOT NULL CHECK(
    jsonb_typeof(envelope_json)='object'
    AND pg_column_size(envelope_json)<=32768
    AND envelope_json->>'version'='askdata-question-envelope-v1'
    AND envelope_json->>'mode'='ENCRYPTED_SHORT_TERM'
    AND envelope_json->>'runId'=run_id::text
    AND envelope_json->>'tenantId'=tenant_id::text
    AND envelope_json->>'domainId'=domain_id::text
    AND envelope_json->>'actorId'=actor_id::text
    AND envelope_json->>'conversationId'=conversation_id::text
    AND envelope_json->'release'->>'releaseId'=release_id::text
    AND envelope_json->'release'->>'contentHash'=release_content_hash
    AND envelope_json->>'policyScopeHash'=policy_scope_hash
    AND envelope_json->>'questionHash'=question_hash
    AND length(COALESCE(envelope_json->>'ciphertext','')) BETWEEN 32 AND 32768
    AND NOT envelope_json ? 'question'
    AND NOT envelope_json ? 'rawQuestion'
    AND NOT envelope_json ? 'questionText'
  ),
  expires_at timestamptz NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY(run_id,tenant_id),
  CONSTRAINT askdata_question_envelopes_run_identity_fk FOREIGN KEY(
    run_id,actor_id,release_id,release_content_hash,policy_scope_hash,domain_id,tenant_id
  ) REFERENCES askdata.question_runs(
    id,actor_id,release_id,release_content_hash,policy_scope_hash,domain_id,tenant_id
  ) ON DELETE CASCADE,
  CONSTRAINT askdata_question_envelopes_expiry_check CHECK(expires_at>created_at)
);

CREATE INDEX askdata_question_envelopes_expiry_idx
  ON askdata.question_envelopes(tenant_id,expires_at,run_id);

CREATE OR REPLACE FUNCTION askdata.validate_question_envelope_runtime()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,askdata
AS $$
BEGIN
  NEW.created_at=(NEW.envelope_json->>'createdAt')::timestamptz;
  NEW.expires_at=(NEW.envelope_json->>'expiresAt')::timestamptz;
  IF NEW.created_at IS NULL OR NEW.expires_at IS NULL
    OR NEW.expires_at<=NEW.created_at OR NEW.expires_at>NEW.created_at+interval '7 days' THEN
    RAISE EXCEPTION 'question envelope expiry is invalid' USING ERRCODE='22023';
  END IF;
  RETURN NEW;
EXCEPTION WHEN invalid_datetime_format OR datetime_field_overflow THEN
  RAISE EXCEPTION 'question envelope timestamps are invalid' USING ERRCODE='22023';
END
$$;

REVOKE ALL ON FUNCTION askdata.validate_question_envelope_runtime() FROM PUBLIC;

CREATE TRIGGER askdata_question_envelopes_validate
BEFORE INSERT ON askdata.question_envelopes
FOR EACH ROW EXECUTE FUNCTION askdata.validate_question_envelope_runtime();

ALTER TABLE askdata.question_envelopes ENABLE ROW LEVEL SECURITY;
ALTER TABLE askdata.question_envelopes FORCE ROW LEVEL SECURITY;
CREATE POLICY askdata_question_envelopes_actor_domain_isolation
  ON askdata.question_envelopes
  USING(askdata.question_runtime_can_access(tenant_id,domain_id,actor_id))
  WITH CHECK(askdata.question_runtime_can_access(tenant_id,domain_id,actor_id));

REVOKE ALL ON TABLE askdata.question_envelopes FROM PUBLIC;

COMMENT ON TABLE askdata.question_envelopes IS
  'Short-lived AES-GCM question envelopes bound to one actor, domain, policy and semantic release; plaintext is prohibited';

COMMIT;
