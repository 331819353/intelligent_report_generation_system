CREATE TABLE platform.report_structure_templates(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(), tenant_id uuid NOT NULL,
  code text NOT NULL, name text NOT NULL, owner_user_id uuid NOT NULL,
  UNIQUE(tenant_id,code), UNIQUE(id,tenant_id),
  FOREIGN KEY(tenant_id) REFERENCES platform.tenants(id) ON DELETE RESTRICT,
  FOREIGN KEY(owner_user_id,tenant_id) REFERENCES platform.users(id,tenant_id) ON DELETE RESTRICT
);
CREATE TABLE platform.report_layout_templates(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(), tenant_id uuid NOT NULL,
  code text NOT NULL, name text NOT NULL, owner_user_id uuid NOT NULL,
  UNIQUE(tenant_id,code), UNIQUE(id,tenant_id),
  FOREIGN KEY(tenant_id) REFERENCES platform.tenants(id) ON DELETE RESTRICT,
  FOREIGN KEY(owner_user_id,tenant_id) REFERENCES platform.users(id,tenant_id) ON DELETE RESTRICT
);
CREATE TABLE platform.report_themes(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(), tenant_id uuid NOT NULL,
  code text NOT NULL, name text NOT NULL, owner_user_id uuid NOT NULL,
  UNIQUE(tenant_id,code), UNIQUE(id,tenant_id),
  FOREIGN KEY(tenant_id) REFERENCES platform.tenants(id) ON DELETE RESTRICT,
  FOREIGN KEY(owner_user_id,tenant_id) REFERENCES platform.users(id,tenant_id) ON DELETE RESTRICT
);
CREATE TABLE platform.report_narrative_templates(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(), tenant_id uuid NOT NULL,
  code text NOT NULL, name text NOT NULL, owner_user_id uuid NOT NULL,
  UNIQUE(tenant_id,code), UNIQUE(id,tenant_id),
  FOREIGN KEY(tenant_id) REFERENCES platform.tenants(id) ON DELETE RESTRICT,
  FOREIGN KEY(owner_user_id,tenant_id) REFERENCES platform.users(id,tenant_id) ON DELETE RESTRICT
);

CREATE TABLE platform.report_structure_template_versions(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(), tenant_id uuid NOT NULL,
  template_id uuid NOT NULL,
  version text NOT NULL CHECK(version ~ '^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$'),
  status text NOT NULL CHECK(status IN ('DRAFT','PUBLISHED','DEPRECATED','RETAINED')),
  definition_json jsonb NOT NULL CHECK(jsonb_typeof(definition_json)='object'),
  content_hash text NOT NULL CHECK(content_hash ~ '^[0-9a-f]{64}$'),
  UNIQUE(template_id,version), UNIQUE(id,tenant_id),
  FOREIGN KEY(template_id,tenant_id) REFERENCES platform.report_structure_templates(id,tenant_id) ON DELETE RESTRICT
);
CREATE TABLE platform.report_layout_template_versions(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(), tenant_id uuid NOT NULL,
  template_id uuid NOT NULL,
  version text NOT NULL CHECK(version ~ '^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$'),
  status text NOT NULL CHECK(status IN ('DRAFT','PUBLISHED','DEPRECATED','RETAINED')),
  definition_json jsonb NOT NULL CHECK(jsonb_typeof(definition_json)='object'),
  content_hash text NOT NULL CHECK(content_hash ~ '^[0-9a-f]{64}$'),
  UNIQUE(template_id,version), UNIQUE(id,tenant_id),
  FOREIGN KEY(template_id,tenant_id) REFERENCES platform.report_layout_templates(id,tenant_id) ON DELETE RESTRICT
);
CREATE TABLE platform.report_theme_versions(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(), tenant_id uuid NOT NULL,
  theme_id uuid NOT NULL,
  version text NOT NULL CHECK(version ~ '^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$'),
  status text NOT NULL CHECK(status IN ('DRAFT','PUBLISHED','DEPRECATED','RETAINED')),
  definition_json jsonb NOT NULL CHECK(jsonb_typeof(definition_json)='object'),
  content_hash text NOT NULL CHECK(content_hash ~ '^[0-9a-f]{64}$'),
  UNIQUE(theme_id,version), UNIQUE(id,tenant_id),
  FOREIGN KEY(theme_id,tenant_id) REFERENCES platform.report_themes(id,tenant_id) ON DELETE RESTRICT
);
CREATE TABLE platform.report_narrative_template_versions(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(), tenant_id uuid NOT NULL,
  template_id uuid NOT NULL,
  version text NOT NULL CHECK(version ~ '^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$'),
  status text NOT NULL CHECK(status IN ('DRAFT','PUBLISHED','DEPRECATED','RETAINED')),
  definition_json jsonb NOT NULL CHECK(jsonb_typeof(definition_json)='object'),
  content_hash text NOT NULL CHECK(content_hash ~ '^[0-9a-f]{64}$'),
  UNIQUE(template_id,version), UNIQUE(id,tenant_id),
  FOREIGN KEY(template_id,tenant_id) REFERENCES platform.report_narrative_templates(id,tenant_id) ON DELETE RESTRICT
);

CREATE TABLE platform.report_templates(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(), tenant_id uuid NOT NULL,
  code text NOT NULL, name text NOT NULL, category text NOT NULL,
  owner_user_id uuid NOT NULL, auto_follow boolean NOT NULL DEFAULT false,
  UNIQUE(tenant_id,code), UNIQUE(id,tenant_id),
  FOREIGN KEY(tenant_id) REFERENCES platform.tenants(id) ON DELETE RESTRICT,
  FOREIGN KEY(owner_user_id,tenant_id) REFERENCES platform.users(id,tenant_id) ON DELETE RESTRICT
);
CREATE TABLE platform.report_template_versions(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(), tenant_id uuid NOT NULL,
  report_template_id uuid NOT NULL,
  version text NOT NULL CHECK(version ~ '^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$'),
  status text NOT NULL CHECK(status IN ('DRAFT','PUBLISHED','DEPRECATED','RETAINED')),
  structure_template_version_id uuid NOT NULL,
  layout_template_version_id uuid NOT NULL,
  theme_version_id uuid NOT NULL,
  narrative_template_version_id uuid NOT NULL,
  definition_json jsonb NOT NULL CHECK(jsonb_typeof(definition_json)='object'),
  content_hash text NOT NULL CHECK(content_hash ~ '^[0-9a-f]{64}$'),
  UNIQUE(report_template_id,version), UNIQUE(id,tenant_id),
  FOREIGN KEY(report_template_id,tenant_id) REFERENCES platform.report_templates(id,tenant_id) ON DELETE RESTRICT,
  FOREIGN KEY(structure_template_version_id,tenant_id) REFERENCES platform.report_structure_template_versions(id,tenant_id) ON DELETE RESTRICT,
  FOREIGN KEY(layout_template_version_id,tenant_id) REFERENCES platform.report_layout_template_versions(id,tenant_id) ON DELETE RESTRICT,
  FOREIGN KEY(theme_version_id,tenant_id) REFERENCES platform.report_theme_versions(id,tenant_id) ON DELETE RESTRICT,
  FOREIGN KEY(narrative_template_version_id,tenant_id) REFERENCES platform.report_narrative_template_versions(id,tenant_id) ON DELETE RESTRICT
);

CREATE TABLE platform.component_templates(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid REFERENCES platform.tenants(id) ON DELETE RESTRICT,
  type text NOT NULL CHECK(type ~ '^[a-z][a-z0-9-]{0,63}$')
);
CREATE UNIQUE INDEX component_template_platform_type_key
  ON platform.component_templates(type) WHERE tenant_id IS NULL;
CREATE UNIQUE INDEX component_template_tenant_type_key
  ON platform.component_templates(tenant_id,type) WHERE tenant_id IS NOT NULL;

CREATE TABLE platform.component_template_versions(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  component_template_id uuid NOT NULL REFERENCES platform.component_templates(id) ON DELETE RESTRICT,
  version text NOT NULL CHECK(version ~ '^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$'),
  status text NOT NULL CHECK(status IN ('ACTIVE','DEPRECATED','RETAINED')),
  manifest_json jsonb NOT NULL CHECK(jsonb_typeof(manifest_json)='object'),
  content_hash text NOT NULL CHECK(content_hash ~ '^[0-9a-f]{64}$'),
  migrator_id text,
  UNIQUE(component_template_id,version)
);

CREATE OR REPLACE FUNCTION platform.enforce_component_template_state()
RETURNS trigger LANGUAGE plpgsql SET search_path=pg_catalog,platform AS $$
BEGIN
  IF OLD.manifest_json->>'seed'='embedded-registry' THEN
    IF NEW.manifest_json ? 'seed'
      OR OLD.id<>NEW.id
      OR OLD.component_template_id<>NEW.component_template_id
      OR OLD.status<>NEW.status
      OR OLD.version<>NEW.version
      OR OLD.migrator_id IS DISTINCT FROM NEW.migrator_id THEN
      RAISE EXCEPTION 'invalid embedded component manifest hydration' USING ERRCODE='23514';
    END IF;
    RETURN NEW;
  END IF;
  IF OLD.status='ACTIVE' AND NEW.status NOT IN ('ACTIVE','DEPRECATED') THEN
    RAISE EXCEPTION 'invalid component template state transition' USING ERRCODE='23514';
  ELSIF OLD.status='DEPRECATED' AND NEW.status NOT IN ('DEPRECATED','RETAINED') THEN
    RAISE EXCEPTION 'invalid component template state transition' USING ERRCODE='23514';
  ELSIF OLD.status='RETAINED' AND NEW.status<>'RETAINED' THEN
    RAISE EXCEPTION 'retained component template is immutable' USING ERRCODE='55000';
  END IF;
  IF OLD.manifest_json IS DISTINCT FROM NEW.manifest_json
    OR OLD.content_hash IS DISTINCT FROM NEW.content_hash
    OR OLD.id<>NEW.id
    OR OLD.component_template_id<>NEW.component_template_id
    OR OLD.version IS DISTINCT FROM NEW.version
    OR OLD.migrator_id IS DISTINCT FROM NEW.migrator_id THEN
    RAISE EXCEPTION 'component template version content is immutable' USING ERRCODE='55000';
  END IF;
  RETURN NEW;
END
$$;
CREATE TRIGGER component_template_version_guard
BEFORE UPDATE ON platform.component_template_versions
FOR EACH ROW EXECUTE FUNCTION platform.enforce_component_template_state();

-- The 13 frozen MVP component identities are present immediately. Manifests
-- are loaded and hash-verified by the Go registry/seed path.
INSERT INTO platform.component_templates(tenant_id,type)
SELECT NULL, type FROM unnest(ARRAY[
  'metric-card','line-trend','bar-horizontal','bar-comparison','area-stacked',
  'pie-donut','scatter','funnel','data-table','insight-text','filter-control',
  'rich-text','image'
]) AS seed(type);
INSERT INTO platform.component_template_versions(
  component_template_id,version,status,manifest_json,content_hash
)
SELECT template.id,'1.0.0','ACTIVE',jsonb_build_object('type',template.type,'seed','embedded-registry'),
  encode(public.digest(jsonb_build_object('type',template.type,'seed','embedded-registry')::text,'sha256'),'hex')
FROM platform.component_templates AS template WHERE template.tenant_id IS NULL;

DO $$
DECLARE table_name text;
BEGIN
  FOREACH table_name IN ARRAY ARRAY[
    'report_structure_templates','report_layout_templates','report_themes','report_narrative_templates',
    'report_structure_template_versions','report_layout_template_versions','report_theme_versions',
    'report_narrative_template_versions','report_templates','report_template_versions',
    'component_templates','component_template_versions'
  ] LOOP
    EXECUTE format('ALTER TABLE platform.%I ENABLE ROW LEVEL SECURITY',table_name);
    EXECUTE format('ALTER TABLE platform.%I FORCE ROW LEVEL SECURITY',table_name);
  END LOOP;
END
$$;

CREATE POLICY report_structure_templates_tenant ON platform.report_structure_templates USING(tenant_id=platform.current_tenant_id()) WITH CHECK(tenant_id=platform.current_tenant_id());
CREATE POLICY report_layout_templates_tenant ON platform.report_layout_templates USING(tenant_id=platform.current_tenant_id()) WITH CHECK(tenant_id=platform.current_tenant_id());
CREATE POLICY report_themes_tenant ON platform.report_themes USING(tenant_id=platform.current_tenant_id()) WITH CHECK(tenant_id=platform.current_tenant_id());
CREATE POLICY report_narrative_templates_tenant ON platform.report_narrative_templates USING(tenant_id=platform.current_tenant_id()) WITH CHECK(tenant_id=platform.current_tenant_id());
CREATE POLICY report_structure_template_versions_tenant ON platform.report_structure_template_versions USING(tenant_id=platform.current_tenant_id()) WITH CHECK(tenant_id=platform.current_tenant_id());
CREATE POLICY report_layout_template_versions_tenant ON platform.report_layout_template_versions USING(tenant_id=platform.current_tenant_id()) WITH CHECK(tenant_id=platform.current_tenant_id());
CREATE POLICY report_theme_versions_tenant ON platform.report_theme_versions USING(tenant_id=platform.current_tenant_id()) WITH CHECK(tenant_id=platform.current_tenant_id());
CREATE POLICY report_narrative_template_versions_tenant ON platform.report_narrative_template_versions USING(tenant_id=platform.current_tenant_id()) WITH CHECK(tenant_id=platform.current_tenant_id());
CREATE POLICY report_templates_tenant ON platform.report_templates USING(tenant_id=platform.current_tenant_id()) WITH CHECK(tenant_id=platform.current_tenant_id());
CREATE POLICY report_template_versions_tenant ON platform.report_template_versions USING(tenant_id=platform.current_tenant_id()) WITH CHECK(tenant_id=platform.current_tenant_id());
CREATE POLICY component_templates_select ON platform.component_templates FOR SELECT
  USING(tenant_id IS NULL OR tenant_id=platform.current_tenant_id());
CREATE POLICY component_templates_write ON platform.component_templates FOR ALL
  USING(tenant_id=platform.current_tenant_id() OR (tenant_id IS NULL AND platform.is_system_access()))
  WITH CHECK(tenant_id=platform.current_tenant_id() OR (tenant_id IS NULL AND platform.is_system_access()));
CREATE POLICY component_template_versions_select ON platform.component_template_versions FOR SELECT
  USING(EXISTS(SELECT 1 FROM platform.component_templates AS template WHERE template.id=component_template_id
    AND (template.tenant_id IS NULL OR template.tenant_id=platform.current_tenant_id())));
CREATE POLICY component_template_versions_write ON platform.component_template_versions FOR ALL
  USING(EXISTS(SELECT 1 FROM platform.component_templates AS template WHERE template.id=component_template_id
    AND (template.tenant_id=platform.current_tenant_id() OR (template.tenant_id IS NULL AND platform.is_system_access()))))
  WITH CHECK(EXISTS(SELECT 1 FROM platform.component_templates AS template WHERE template.id=component_template_id
    AND (template.tenant_id=platform.current_tenant_id() OR (template.tenant_id IS NULL AND platform.is_system_access()))));

REVOKE ALL ON FUNCTION platform.enforce_component_template_state() FROM PUBLIC;
