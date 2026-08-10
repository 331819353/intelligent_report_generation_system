-- Durable extraction/projection runtime for certified report semantic assets.
ALTER TABLE askdata.report_semantic_assets
  ADD COLUMN semantic_release_content_hash text CHECK(semantic_release_content_hash IS NULL OR semantic_release_content_hash ~ '^[0-9a-f]{64}$'),
  ADD COLUMN query_plan_hash text CHECK(query_plan_hash IS NULL OR query_plan_hash ~ '^[0-9a-f]{64}$'),
  ADD COLUMN sensitivity text NOT NULL DEFAULT 'INTERNAL' CHECK(sensitivity IN('PUBLIC','INTERNAL','CONFIDENTIAL')),
  ADD COLUMN report_title text NOT NULL DEFAULT '' CHECK(length(report_title)<=512 AND report_title !~ '[[:cntrl:]]'),
  ADD COLUMN report_description text NOT NULL DEFAULT '' CHECK(length(report_description)<=4000 AND report_description !~ '[[:cntrl:]]'),
  ADD COLUMN section_purpose text NOT NULL DEFAULT '' CHECK(length(section_purpose)<=2000 AND section_purpose !~ '[[:cntrl:]]'),
  ADD COLUMN block_title text NOT NULL DEFAULT '' CHECK(length(block_title)<=512 AND block_title !~ '[[:cntrl:]]'),
  ADD COLUMN contains_uncertified_free_text boolean NOT NULL DEFAULT false,
  ADD COLUMN projection_state text NOT NULL DEFAULT 'PENDING' CHECK(projection_state IN('PENDING','READY','RETRY','REMOVED'));

UPDATE askdata.report_semantic_assets AS asset SET semantic_release_content_hash=release.content_hash
FROM askdata.releases AS release WHERE release.id=asset.semantic_release_id
  AND release.tenant_id=asset.tenant_id AND release.domain_id=asset.domain_id;

CREATE TABLE askdata.report_asset_extraction_outbox(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(), tenant_id uuid NOT NULL,
  report_id uuid NOT NULL, report_version_id uuid NOT NULL,
  state text NOT NULL DEFAULT 'PENDING' CHECK(state IN('PENDING','RUNNING','DONE','FAILED')),
  attempt integer NOT NULL DEFAULT 0 CHECK(attempt BETWEEN 0 AND 10),
  next_attempt_at timestamptz NOT NULL DEFAULT now(), lease_token uuid, lease_expires_at timestamptz,
  error_code text NOT NULL DEFAULT '' CHECK(length(error_code)<=128),
  created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE(tenant_id,report_version_id),
  FOREIGN KEY(report_version_id,report_id,tenant_id) REFERENCES platform.report_versions(id,report_id,tenant_id) ON DELETE CASCADE,
  CHECK((state='RUNNING')=(lease_token IS NOT NULL AND lease_expires_at IS NOT NULL))
);

CREATE TABLE askdata.report_asset_projection_outbox(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(), tenant_id uuid NOT NULL,
  report_semantic_asset_id uuid NOT NULL,
  operation text NOT NULL CHECK(operation IN('UPSERT','REMOVE')),
  component_content_hash text NOT NULL CHECK(component_content_hash ~ '^[0-9a-f]{64}$'),
  state text NOT NULL DEFAULT 'PENDING' CHECK(state IN('PENDING','RUNNING','DONE','FAILED')),
  attempt integer NOT NULL DEFAULT 0 CHECK(attempt BETWEEN 0 AND 10),
  next_attempt_at timestamptz NOT NULL DEFAULT now(), lease_token uuid, lease_expires_at timestamptz,
  error_code text NOT NULL DEFAULT '' CHECK(length(error_code)<=128),
  created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE(tenant_id,report_semantic_asset_id,component_content_hash,operation),
  FOREIGN KEY(report_semantic_asset_id,tenant_id) REFERENCES askdata.report_semantic_assets(id,tenant_id) ON DELETE CASCADE,
  CHECK((state='RUNNING')=(lease_token IS NOT NULL AND lease_expires_at IS NOT NULL))
);
CREATE INDEX report_asset_extraction_claim_idx ON askdata.report_asset_extraction_outbox(tenant_id,state,next_attempt_at);
CREATE INDEX report_asset_projection_claim_idx ON askdata.report_asset_projection_outbox(tenant_id,state,next_attempt_at);

CREATE OR REPLACE FUNCTION askdata.enqueue_report_version_asset_extraction()
RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,askdata AS $$
BEGIN
  IF NEW.artifact_state='READY' AND OLD.artifact_state<>'READY' THEN
    INSERT INTO askdata.report_asset_extraction_outbox(tenant_id,report_id,report_version_id)
    VALUES(NEW.tenant_id,NEW.report_id,NEW.id)
    ON CONFLICT(tenant_id,report_version_id) DO UPDATE SET state='PENDING',attempt=0,
      next_attempt_at=now(),lease_token=NULL,lease_expires_at=NULL,error_code='',updated_at=now();
  END IF;
  RETURN NEW;
END
$$;
CREATE TRIGGER report_version_asset_extraction_enqueue AFTER UPDATE OF artifact_state ON platform.report_versions
FOR EACH ROW EXECUTE FUNCTION askdata.enqueue_report_version_asset_extraction();

CREATE OR REPLACE FUNCTION askdata.guard_report_asset_certification_actor()
RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,askdata,platform AS $$
DECLARE selected_asset askdata.report_semantic_assets%ROWTYPE; selected_actor uuid;
BEGIN
  IF TG_OP<>'INSERT' THEN
    RAISE EXCEPTION 'report asset certifications are immutable' USING ERRCODE='55000';
  END IF;
  selected_actor:=askdata.current_actor_id();
  SELECT * INTO selected_asset FROM askdata.report_semantic_assets
    WHERE id=NEW.report_semantic_asset_id AND tenant_id=NEW.tenant_id FOR SHARE;
  IF selected_asset.id IS NULL OR selected_actor IS NULL OR NEW.approver_user_id<>selected_actor
    OR NEW.component_content_hash<>selected_asset.component_content_hash
    OR selected_asset.state IN('REVOKED','INVALIDATED') THEN
    RAISE EXCEPTION 'report asset certification identity is invalid' USING ERRCODE='42501';
  END IF;
  IF NEW.approver_role='REPORT_OWNER' AND NOT EXISTS(
    SELECT 1 FROM platform.reports WHERE id=selected_asset.report_id
      AND tenant_id=selected_asset.tenant_id AND owner_user_id=selected_actor
  ) THEN RAISE EXCEPTION 'approver is not the report owner' USING ERRCODE='42501';
  END IF;
  IF NEW.approver_role='SEMANTIC_OWNER' AND NOT (
    EXISTS(SELECT 1 FROM askdata.metric_versions WHERE tenant_id=selected_asset.tenant_id
      AND id=ANY(selected_asset.metric_version_ids) AND owner_id=selected_actor)
    OR EXISTS(SELECT 1 FROM askdata.dimensions WHERE tenant_id=selected_asset.tenant_id
      AND id=ANY(selected_asset.dimension_version_ids) AND owner_id=selected_actor)
    OR EXISTS(SELECT 1 FROM askdata.dimension_members WHERE tenant_id=selected_asset.tenant_id
      AND id=ANY(selected_asset.member_version_ids) AND created_by=selected_actor)
  ) THEN RAISE EXCEPTION 'approver is not a referenced semantic owner' USING ERRCODE='42501';
  END IF;
  RETURN NEW;
END
$$;
CREATE TRIGGER report_asset_certification_actor_guard BEFORE INSERT OR UPDATE OR DELETE
ON askdata.report_asset_certifications FOR EACH ROW EXECUTE FUNCTION askdata.guard_report_asset_certification_actor();

CREATE OR REPLACE FUNCTION askdata.enqueue_report_asset_projection()
RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,askdata AS $$
DECLARE selected_operation text;
BEGIN
  IF TG_OP='DELETE' THEN selected_operation:='REMOVE';
  ELSIF NEW.state='CERTIFIED' THEN selected_operation:='UPSERT';
  ELSE selected_operation:='REMOVE'; END IF;
  IF TG_OP='INSERT' AND NEW.state<>'CERTIFIED' THEN RETURN NEW; END IF;
  IF TG_OP='UPDATE' AND NEW.state=OLD.state AND NEW.component_content_hash=OLD.component_content_hash THEN RETURN NEW; END IF;
  INSERT INTO askdata.report_asset_projection_outbox(
    tenant_id,report_semantic_asset_id,operation,component_content_hash
  ) VALUES(COALESCE(NEW.tenant_id,OLD.tenant_id),COALESCE(NEW.id,OLD.id),selected_operation,
    COALESCE(NEW.component_content_hash,OLD.component_content_hash))
  ON CONFLICT(tenant_id,report_semantic_asset_id,component_content_hash,operation)
  DO UPDATE SET state='PENDING',attempt=0,next_attempt_at=now(),lease_token=NULL,
    lease_expires_at=NULL,error_code='',updated_at=now();
  RETURN COALESCE(NEW,OLD);
END
$$;
CREATE TRIGGER report_asset_projection_enqueue AFTER INSERT OR UPDATE OF state,component_content_hash
ON askdata.report_semantic_assets FOR EACH ROW EXECUTE FUNCTION askdata.enqueue_report_asset_projection();

ALTER TABLE askdata.report_asset_extraction_outbox ENABLE ROW LEVEL SECURITY;
ALTER TABLE askdata.report_asset_extraction_outbox FORCE ROW LEVEL SECURITY;
ALTER TABLE askdata.report_asset_projection_outbox ENABLE ROW LEVEL SECURITY;
ALTER TABLE askdata.report_asset_projection_outbox FORCE ROW LEVEL SECURITY;
CREATE POLICY report_asset_extraction_system_policy ON askdata.report_asset_extraction_outbox
  USING(tenant_id=platform.current_tenant_id() AND platform.is_system_access())
  WITH CHECK(tenant_id=platform.current_tenant_id() AND platform.is_system_access());
CREATE POLICY report_asset_projection_system_policy ON askdata.report_asset_projection_outbox
  USING(tenant_id=platform.current_tenant_id() AND platform.is_system_access())
  WITH CHECK(tenant_id=platform.current_tenant_id() AND platform.is_system_access());

REVOKE ALL ON FUNCTION askdata.enqueue_report_version_asset_extraction(),
  askdata.guard_report_asset_certification_actor(),askdata.enqueue_report_asset_projection() FROM PUBLIC;
