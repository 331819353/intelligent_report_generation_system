-- Report V2 persistence. Migration 000195 intentionally removed the legacy
-- report runtime; this is a new closed-definition implementation.
CREATE TABLE platform.reports(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id) ON DELETE RESTRICT,
  domain_id uuid,
  code text NOT NULL CHECK(code ~ '^[a-z][a-z0-9_-]{0,127}$'),
  name text NOT NULL CHECK(length(btrim(name)) BETWEEN 1 AND 512),
  report_type text NOT NULL CHECK(report_type IN ('REPORT','DASHBOARD')),
  owner_user_id uuid NOT NULL,
  current_published_version_id uuid,
  status text NOT NULL DEFAULT 'ACTIVE' CHECK(status IN ('ACTIVE','ARCHIVED')),
  created_by uuid NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT report_v2_identity_tenant_key UNIQUE(id,tenant_id),
  CONSTRAINT report_v2_code_key UNIQUE(tenant_id,code),
  CONSTRAINT report_v2_owner_fk FOREIGN KEY(owner_user_id,tenant_id)
    REFERENCES platform.users(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT report_v2_creator_fk FOREIGN KEY(created_by,tenant_id)
    REFERENCES platform.users(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT report_v2_domain_fk FOREIGN KEY(domain_id,tenant_id)
    REFERENCES platform.business_domains(id,tenant_id) ON DELETE RESTRICT
);

CREATE TABLE platform.report_drafts(
  report_id uuid PRIMARY KEY,
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id) ON DELETE RESTRICT,
  definition_json jsonb NOT NULL CHECK(jsonb_typeof(definition_json)='object'),
  definition_hash text NOT NULL CHECK(definition_hash ~ '^[0-9a-f]{64}$'),
  schema_version text NOT NULL CHECK(schema_version='1.0'),
  revision_no bigint NOT NULL DEFAULT 0 CHECK(revision_no>=0),
  updated_by uuid NOT NULL,
  updated_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT report_v2_draft_report_fk FOREIGN KEY(report_id,tenant_id)
    REFERENCES platform.reports(id,tenant_id) ON DELETE CASCADE,
  CONSTRAINT report_v2_draft_actor_fk FOREIGN KEY(updated_by,tenant_id)
    REFERENCES platform.users(id,tenant_id) ON DELETE RESTRICT
);

CREATE TABLE platform.report_revisions(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id) ON DELETE RESTRICT,
  report_id uuid NOT NULL,
  revision_no bigint NOT NULL CHECK(revision_no>0),
  base_revision_no bigint NOT NULL CHECK(base_revision_no>=0),
  source text NOT NULL CHECK(source IN ('USER','AI','IMPORT','SYSTEM','UNDO','REDO')),
  operation_json jsonb NOT NULL CHECK(jsonb_typeof(operation_json) IN ('object','array')),
  before_hash text NOT NULL CHECK(before_hash ~ '^[0-9a-f]{64}$'),
  after_hash text NOT NULL CHECK(after_hash ~ '^[0-9a-f]{64}$'),
  before_snapshot jsonb CHECK(before_snapshot IS NULL OR jsonb_typeof(before_snapshot)='object'),
  inverse_of_revision_no bigint,
  actor_user_id uuid NOT NULL,
  ai_run_id uuid,
  created_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT report_v2_revision_report_fk FOREIGN KEY(report_id,tenant_id)
    REFERENCES platform.reports(id,tenant_id) ON DELETE CASCADE,
  CONSTRAINT report_v2_revision_actor_fk FOREIGN KEY(actor_user_id,tenant_id)
    REFERENCES platform.users(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT report_v2_revision_sequence_key UNIQUE(report_id,revision_no),
  CONSTRAINT report_v2_revision_step_check CHECK(revision_no=base_revision_no+1),
  CONSTRAINT report_v2_revision_inverse_check CHECK(
    (source IN ('UNDO','REDO'))=(inverse_of_revision_no IS NOT NULL)
    AND (inverse_of_revision_no IS NULL OR inverse_of_revision_no<revision_no)
  )
);

CREATE TABLE platform.report_versions(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id) ON DELETE RESTRICT,
  report_id uuid NOT NULL,
  version_no integer NOT NULL CHECK(version_no>0),
  source_revision_no bigint NOT NULL CHECK(source_revision_no>=0),
  definition_json jsonb NOT NULL CHECK(jsonb_typeof(definition_json)='object'),
  definition_bytes bigint NOT NULL CHECK(definition_bytes BETWEEN 2 AND 5242880),
  definition_hash text NOT NULL CHECK(definition_hash ~ '^[0-9a-f]{64}$'),
  schema_version text NOT NULL CHECK(schema_version='1.0'),
  object_uri text NOT NULL CHECK(length(btrim(object_uri)) BETWEEN 1 AND 2048),
  published_by uuid NOT NULL,
  published_at timestamptz NOT NULL DEFAULT now(),
  rollback_of_version_no integer,
  rollback_reason text,
  stale_insights_acknowledged boolean NOT NULL DEFAULT false,
  artifact_state text NOT NULL DEFAULT 'PENDING' CHECK(artifact_state IN ('PENDING','READY','RETRY')),
  CONSTRAINT report_v2_version_report_fk FOREIGN KEY(report_id,tenant_id)
    REFERENCES platform.reports(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT report_v2_version_actor_fk FOREIGN KEY(published_by,tenant_id)
    REFERENCES platform.users(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT report_v2_version_number_key UNIQUE(report_id,version_no),
  CONSTRAINT report_v2_version_identity_key UNIQUE(id,report_id,tenant_id),
  CONSTRAINT report_v2_rollback_shape_check CHECK(
    (rollback_of_version_no IS NULL AND rollback_reason IS NULL)
    OR (rollback_of_version_no>0 AND length(btrim(rollback_reason)) BETWEEN 1 AND 1000)
  )
);

ALTER TABLE platform.reports
  ADD CONSTRAINT report_v2_current_version_fk
  FOREIGN KEY(current_published_version_id,id,tenant_id)
  REFERENCES platform.report_versions(id,report_id,tenant_id) ON DELETE RESTRICT;

CREATE TABLE platform.report_publication_idempotency(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id) ON DELETE RESTRICT,
  report_id uuid NOT NULL,
  actor_user_id uuid NOT NULL,
  operation text NOT NULL CHECK(operation IN ('PUBLISH','ROLLBACK')),
  idempotency_key text NOT NULL CHECK(length(idempotency_key) BETWEEN 8 AND 128),
  request_hash text NOT NULL CHECK(request_hash ~ '^[0-9a-f]{64}$'),
  report_version_id uuid NOT NULL,
  response_json jsonb NOT NULL CHECK(jsonb_typeof(response_json)='object'),
  created_at timestamptz NOT NULL DEFAULT now(),
  expires_at timestamptz NOT NULL DEFAULT now()+interval '24 hours',
  CONSTRAINT report_v2_publish_idempotency_report_fk FOREIGN KEY(report_id,tenant_id)
    REFERENCES platform.reports(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT report_v2_publish_idempotency_version_fk
    FOREIGN KEY(report_version_id,report_id,tenant_id)
    REFERENCES platform.report_versions(id,report_id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT report_v2_publish_idempotency_actor_fk FOREIGN KEY(actor_user_id,tenant_id)
    REFERENCES platform.users(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT report_v2_publish_idempotency_key UNIQUE(tenant_id,report_id,actor_user_id,operation,idempotency_key),
  CONSTRAINT report_v2_publish_idempotency_expiry CHECK(expires_at>created_at AND expires_at<=created_at+interval '24 hours')
);

CREATE INDEX report_v2_owner_idx ON platform.reports(tenant_id,owner_user_id,status,updated_at DESC);
CREATE INDEX report_v2_revision_idx ON platform.report_revisions(tenant_id,report_id,revision_no DESC);
CREATE INDEX report_v2_version_idx ON platform.report_versions(tenant_id,report_id,version_no DESC);
CREATE INDEX report_v2_artifact_retry_idx ON platform.report_versions(artifact_state,published_at) WHERE artifact_state<>'READY';

CREATE TRIGGER report_v2_updated_at BEFORE UPDATE ON platform.reports
FOR EACH ROW EXECUTE FUNCTION platform.set_updated_at();

CREATE OR REPLACE FUNCTION platform.reject_report_v2_immutable_mutation()
RETURNS trigger LANGUAGE plpgsql SET search_path=pg_catalog,platform AS $$
BEGIN
  RAISE EXCEPTION 'report v2 immutable artifact cannot be changed'
    USING ERRCODE='55000';
END
$$;

CREATE TRIGGER report_v2_revisions_immutable
BEFORE UPDATE OR DELETE ON platform.report_revisions
FOR EACH ROW EXECUTE FUNCTION platform.reject_report_v2_immutable_mutation();
CREATE OR REPLACE FUNCTION platform.guard_report_v2_version_mutation()
RETURNS trigger LANGUAGE plpgsql SET search_path=pg_catalog,platform AS $$
BEGIN
  IF TG_OP='DELETE' THEN
    RAISE EXCEPTION 'report v2 immutable artifact cannot be deleted' USING ERRCODE='55000';
  END IF;
  IF ROW(OLD.id,OLD.tenant_id,OLD.report_id,OLD.version_no,OLD.source_revision_no,
         OLD.definition_json,OLD.definition_bytes,OLD.definition_hash,OLD.schema_version,
         OLD.object_uri,OLD.published_by,OLD.published_at,OLD.rollback_of_version_no,
         OLD.rollback_reason,OLD.stale_insights_acknowledged)
     IS DISTINCT FROM
     ROW(NEW.id,NEW.tenant_id,NEW.report_id,NEW.version_no,NEW.source_revision_no,
         NEW.definition_json,NEW.definition_bytes,NEW.definition_hash,NEW.schema_version,
         NEW.object_uri,NEW.published_by,NEW.published_at,NEW.rollback_of_version_no,
         NEW.rollback_reason,NEW.stale_insights_acknowledged) THEN
    RAISE EXCEPTION 'report v2 immutable artifact content cannot be changed' USING ERRCODE='55000';
  END IF;
  IF OLD.artifact_state='READY' OR
     (OLD.artifact_state='PENDING' AND NEW.artifact_state NOT IN ('PENDING','READY','RETRY')) OR
     (OLD.artifact_state='RETRY' AND NEW.artifact_state NOT IN ('RETRY','READY')) THEN
    RAISE EXCEPTION 'invalid report artifact state transition' USING ERRCODE='23514';
  END IF;
  RETURN NEW;
END
$$;
CREATE TRIGGER report_v2_versions_definition_immutable
BEFORE UPDATE OR DELETE ON platform.report_versions
FOR EACH ROW EXECUTE FUNCTION platform.guard_report_v2_version_mutation();

-- Object grants never replace tenant/domain membership. This helper is
-- SECURITY DEFINER so policies can inspect grants without exposing their
-- contents. The row helper is also safe for INSERT ... RETURNING, where a
-- second query cannot yet observe the row being inserted.
CREATE OR REPLACE FUNCTION platform.report_v2_row_can_access(
  target_report_id uuid,
  target_domain_id uuid,
  target_owner_user_id uuid,
  required_actions text[]
)
RETURNS boolean
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path=pg_catalog,platform
AS $$
  SELECT platform.is_system_access()
    OR (
      platform.current_user_id() IS NOT NULL
      AND (
        target_domain_id IS NULL
        OR (
          target_domain_id=platform.current_domain_id()
          AND platform.user_has_active_domain_membership(target_domain_id)
        )
      )
      AND (
        target_owner_user_id=platform.current_user_id()
        OR platform.user_is_asset_administrator()
        OR (
          target_domain_id IS NOT NULL
          AND platform.user_is_domain_administrator(target_domain_id)
        )
        OR EXISTS(
          SELECT 1
          FROM platform.object_permissions AS permission
          WHERE permission.tenant_id=platform.current_tenant_id()
            AND permission.object_type='REPORT'
            AND permission.object_id=target_report_id
            AND permission.action=ANY(COALESCE(required_actions,ARRAY[]::text[]))
            AND (
              (
                permission.subject_type='USER'
                AND permission.subject_id=platform.current_user_id()
              )
              OR (
                permission.subject_type='ROLE'
                AND EXISTS(
                  SELECT 1
                  FROM platform.user_roles AS assignment
                  JOIN platform.roles AS role
                    ON role.id=assignment.role_id
                   AND role.tenant_id=assignment.tenant_id
                  WHERE assignment.tenant_id=platform.current_tenant_id()
                    AND assignment.user_id=platform.current_user_id()
                    AND assignment.role_id=permission.subject_id
                    AND role.status='ACTIVE'
                    AND role.deleted_at IS NULL
                )
              )
            )
        )
      )
    )
$$;

CREATE OR REPLACE FUNCTION platform.report_v2_can_access(
  target_report_id uuid,
  required_actions text[]
)
RETURNS boolean
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path=pg_catalog,platform
AS $$
  SELECT EXISTS(
    SELECT 1
    FROM platform.reports AS target
    WHERE target.id=target_report_id
      AND target.tenant_id=platform.current_tenant_id()
      AND platform.report_v2_row_can_access(
        target.id,target.domain_id,target.owner_user_id,required_actions
      )
  )
$$;

ALTER TABLE platform.reports ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform.reports FORCE ROW LEVEL SECURITY;
ALTER TABLE platform.report_drafts ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform.report_drafts FORCE ROW LEVEL SECURITY;
ALTER TABLE platform.report_revisions ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform.report_revisions FORCE ROW LEVEL SECURITY;
ALTER TABLE platform.report_versions ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform.report_versions FORCE ROW LEVEL SECURITY;
ALTER TABLE platform.report_publication_idempotency ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform.report_publication_idempotency FORCE ROW LEVEL SECURITY;

CREATE POLICY report_v2_read_policy ON platform.reports FOR SELECT
  USING(tenant_id=platform.current_tenant_id() AND platform.report_v2_row_can_access(
    id,domain_id,owner_user_id,ARRAY['VIEW','EDIT','PUBLISH']::text[]
  ));
CREATE POLICY report_v2_create_policy ON platform.reports FOR INSERT
  WITH CHECK(tenant_id=platform.current_tenant_id() AND (
    platform.is_system_access() OR (
      owner_user_id=platform.current_user_id()
      AND created_by=platform.current_user_id()
      AND (
        domain_id IS NULL OR (
          domain_id=platform.current_domain_id()
          AND platform.user_has_active_domain_membership(domain_id)
        )
      )
    )
  ));
CREATE POLICY report_v2_update_policy ON platform.reports FOR UPDATE
  USING(tenant_id=platform.current_tenant_id() AND platform.report_v2_row_can_access(
    id,domain_id,owner_user_id,ARRAY['EDIT','PUBLISH']::text[]
  ))
  WITH CHECK(tenant_id=platform.current_tenant_id() AND platform.report_v2_row_can_access(
    id,domain_id,owner_user_id,ARRAY['EDIT','PUBLISH']::text[]
  ));
CREATE POLICY report_v2_delete_policy ON platform.reports FOR DELETE
  USING(tenant_id=platform.current_tenant_id() AND platform.report_v2_row_can_access(
    id,domain_id,owner_user_id,ARRAY['EDIT']::text[]
  ));

CREATE POLICY report_v2_draft_read_policy ON platform.report_drafts FOR SELECT
  USING(tenant_id=platform.current_tenant_id() AND platform.report_v2_can_access(
    report_id,ARRAY['VIEW','EDIT','PUBLISH']::text[]
  ));
CREATE POLICY report_v2_draft_write_policy ON platform.report_drafts FOR ALL
  USING(tenant_id=platform.current_tenant_id() AND platform.report_v2_can_access(
    report_id,ARRAY['EDIT']::text[]
  ))
  WITH CHECK(tenant_id=platform.current_tenant_id() AND platform.report_v2_can_access(
    report_id,ARRAY['EDIT']::text[]
  ));

CREATE POLICY report_v2_revision_read_policy ON platform.report_revisions FOR SELECT
  USING(tenant_id=platform.current_tenant_id() AND platform.report_v2_can_access(
    report_id,ARRAY['VIEW','EDIT','PUBLISH']::text[]
  ));
CREATE POLICY report_v2_revision_write_policy ON platform.report_revisions FOR ALL
  USING(tenant_id=platform.current_tenant_id() AND platform.report_v2_can_access(
    report_id,ARRAY['EDIT']::text[]
  ))
  WITH CHECK(tenant_id=platform.current_tenant_id() AND platform.report_v2_can_access(
    report_id,ARRAY['EDIT']::text[]
  ));

CREATE POLICY report_v2_version_read_policy ON platform.report_versions FOR SELECT
  USING(tenant_id=platform.current_tenant_id() AND platform.report_v2_can_access(
    report_id,ARRAY['VIEW','EDIT','PUBLISH']::text[]
  ));
CREATE POLICY report_v2_version_write_policy ON platform.report_versions FOR ALL
  USING(tenant_id=platform.current_tenant_id() AND platform.report_v2_can_access(
    report_id,ARRAY['PUBLISH','EDIT']::text[]
  ))
  WITH CHECK(tenant_id=platform.current_tenant_id() AND platform.report_v2_can_access(
    report_id,ARRAY['PUBLISH','EDIT']::text[]
  ));
CREATE POLICY report_v2_publish_idempotency_tenant_policy ON platform.report_publication_idempotency
  USING(tenant_id=platform.current_tenant_id() AND (
    platform.is_system_access() OR actor_user_id=platform.current_user_id()
  ) AND platform.report_v2_can_access(report_id,ARRAY['PUBLISH']::text[]))
  WITH CHECK(tenant_id=platform.current_tenant_id() AND (
    platform.is_system_access() OR actor_user_id=platform.current_user_id()
  ) AND platform.report_v2_can_access(report_id,ARRAY['PUBLISH']::text[]));

REVOKE ALL ON FUNCTION platform.reject_report_v2_immutable_mutation() FROM PUBLIC;
REVOKE ALL ON FUNCTION platform.guard_report_v2_version_mutation() FROM PUBLIC;
REVOKE ALL ON FUNCTION platform.report_v2_row_can_access(uuid,uuid,uuid,text[]) FROM PUBLIC;
REVOKE ALL ON FUNCTION platform.report_v2_can_access(uuid,text[]) FROM PUBLIC;
