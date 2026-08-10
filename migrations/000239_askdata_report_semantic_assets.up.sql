CREATE TABLE askdata.report_semantic_assets(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(), tenant_id uuid NOT NULL, domain_id uuid NOT NULL,
  report_id uuid NOT NULL, report_version_id uuid NOT NULL,
  page_id text, section_id text, block_id text, component_id text NOT NULL,
  semantic_release_id uuid NOT NULL,
  semantic_ir_json jsonb NOT NULL CHECK(jsonb_typeof(semantic_ir_json)='object' AND askdata.json_is_safe(semantic_ir_json)),
  semantic_ir_hash text NOT NULL CHECK(semantic_ir_hash ~ '^[0-9a-f]{64}$'),
  metric_version_ids uuid[] NOT NULL CHECK(cardinality(metric_version_ids) BETWEEN 1 AND 16),
  dimension_version_ids uuid[] NOT NULL DEFAULT '{}', member_version_ids uuid[] NOT NULL DEFAULT '{}',
  chart_type text NOT NULL, chart_version text NOT NULL,
  narrative_role text, component_content_hash text NOT NULL CHECK(component_content_hash ~ '^[0-9a-f]{64}$'),
  state text NOT NULL DEFAULT 'PENDING' CHECK(state IN ('PENDING','CERTIFIED','REVOKED','INVALIDATED')),
  created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE(report_version_id,component_id), UNIQUE(id,tenant_id),
  FOREIGN KEY(domain_id,tenant_id) REFERENCES askdata.domains(id,tenant_id) ON DELETE RESTRICT,
  FOREIGN KEY(report_version_id,report_id,tenant_id) REFERENCES platform.report_versions(id,report_id,tenant_id) ON DELETE CASCADE,
  FOREIGN KEY(semantic_release_id,domain_id,tenant_id) REFERENCES askdata.releases(id,domain_id,tenant_id) ON DELETE RESTRICT
);
CREATE TABLE askdata.report_asset_certifications(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(), tenant_id uuid NOT NULL,
  report_semantic_asset_id uuid NOT NULL, approver_user_id uuid NOT NULL,
  approver_role text NOT NULL CHECK(approver_role IN ('REPORT_OWNER','SEMANTIC_OWNER')),
  approved_at timestamptz NOT NULL DEFAULT now(),
  component_content_hash text NOT NULL CHECK(component_content_hash ~ '^[0-9a-f]{64}$'),
  note text NOT NULL DEFAULT '' CHECK(length(note)<=2000),
  UNIQUE(report_semantic_asset_id,approver_user_id),
  FOREIGN KEY(report_semantic_asset_id,tenant_id) REFERENCES askdata.report_semantic_assets(id,tenant_id) ON DELETE CASCADE,
  FOREIGN KEY(approver_user_id,tenant_id) REFERENCES platform.users(id,tenant_id) ON DELETE RESTRICT
);

CREATE OR REPLACE FUNCTION askdata.refresh_report_asset_certification()
RETURNS trigger LANGUAGE plpgsql SET search_path=pg_catalog,askdata,platform AS $$
BEGIN
  UPDATE askdata.report_semantic_assets AS asset SET
    state=CASE WHEN EXISTS(
      SELECT 1 FROM askdata.report_asset_certifications AS certification
      WHERE certification.report_semantic_asset_id=asset.id
        AND certification.component_content_hash=asset.component_content_hash
    ) THEN 'CERTIFIED' ELSE 'INVALIDATED' END,
    updated_at=now()
  WHERE asset.id=COALESCE(NEW.report_semantic_asset_id,OLD.report_semantic_asset_id)
    AND asset.state<>'REVOKED';
  RETURN COALESCE(NEW,OLD);
END
$$;
CREATE TRIGGER report_asset_certification_refresh
AFTER INSERT OR UPDATE OR DELETE ON askdata.report_asset_certifications
FOR EACH ROW EXECUTE FUNCTION askdata.refresh_report_asset_certification();
CREATE OR REPLACE FUNCTION askdata.invalidate_changed_report_asset()
RETURNS trigger LANGUAGE plpgsql SET search_path=pg_catalog,askdata AS $$
BEGIN
  IF OLD.component_content_hash IS DISTINCT FROM NEW.component_content_hash THEN
    NEW.state='INVALIDATED';
  END IF;
  RETURN NEW;
END
$$;
CREATE TRIGGER report_asset_hash_guard BEFORE UPDATE ON askdata.report_semantic_assets
FOR EACH ROW EXECUTE FUNCTION askdata.invalidate_changed_report_asset();

CREATE INDEX report_semantic_assets_search_idx ON askdata.report_semantic_assets(tenant_id,domain_id,state,semantic_release_id);
ALTER TABLE askdata.report_semantic_assets ENABLE ROW LEVEL SECURITY;
ALTER TABLE askdata.report_semantic_assets FORCE ROW LEVEL SECURITY;
ALTER TABLE askdata.report_asset_certifications ENABLE ROW LEVEL SECURITY;
ALTER TABLE askdata.report_asset_certifications FORCE ROW LEVEL SECURITY;
CREATE POLICY report_semantic_assets_scope ON askdata.report_semantic_assets
  USING(tenant_id=platform.current_tenant_id() AND (platform.is_system_access() OR domain_id=platform.current_domain_id()))
  WITH CHECK(tenant_id=platform.current_tenant_id() AND (platform.is_system_access() OR domain_id=platform.current_domain_id()));
CREATE POLICY report_asset_certifications_scope ON askdata.report_asset_certifications
  USING(tenant_id=platform.current_tenant_id()) WITH CHECK(tenant_id=platform.current_tenant_id());
REVOKE ALL ON FUNCTION askdata.refresh_report_asset_certification(),askdata.invalidate_changed_report_asset() FROM PUBLIC;
