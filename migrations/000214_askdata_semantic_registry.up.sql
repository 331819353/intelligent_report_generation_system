-- Authoritative semantic registry. Version rows are append-only after
-- certification and may reference only active published DWS/ADS assets.
CREATE TABLE askdata.domains(
  id uuid NOT NULL,
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id) ON DELETE RESTRICT,
  code citext NOT NULL,
  name text NOT NULL CHECK(length(btrim(name)) BETWEEN 1 AND 200),
  description text NOT NULL DEFAULT '' CHECK(
    length(description)<=2000 AND description !~ '[[:cntrl:]]'
  ),
  default_timezone text NOT NULL DEFAULT 'Asia/Shanghai' CHECK(
    length(default_timezone) BETWEEN 1 AND 64
    AND default_timezone=btrim(default_timezone)
  ),
  status text NOT NULL DEFAULT 'ACTIVE' CHECK(status IN ('ACTIVE','INACTIVE')),
  owner_id uuid NOT NULL,
  version bigint NOT NULL DEFAULT 1 CHECK(version>0),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY(id),
  CONSTRAINT askdata_domains_identity_tenant_key UNIQUE(id,tenant_id),
  CONSTRAINT askdata_domains_code_key UNIQUE(tenant_id,code),
  CONSTRAINT askdata_domains_platform_domain_fk
    FOREIGN KEY(id,tenant_id)
    REFERENCES platform.business_domains(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT askdata_domains_owner_fk
    FOREIGN KEY(owner_id,tenant_id)
    REFERENCES platform.users(id,tenant_id) ON DELETE RESTRICT
);

CREATE TABLE askdata.entities(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id) ON DELETE RESTRICT,
  domain_id uuid NOT NULL,
  entity_id uuid NOT NULL,
  version_no integer NOT NULL CHECK(version_no>0),
  code citext NOT NULL,
  name text NOT NULL CHECK(length(btrim(name)) BETWEEN 1 AND 200),
  description text NOT NULL DEFAULT '' CHECK(length(description)<=4000),
  key_contract jsonb NOT NULL CHECK(
    jsonb_typeof(key_contract)='object'
    AND pg_column_size(key_contract)<=65536
    AND askdata.json_is_safe(key_contract)
  ),
  status text NOT NULL DEFAULT 'DRAFT' CHECK(status IN ('DRAFT','CERTIFIED','DEPRECATED')),
  content_hash text NOT NULL CHECK(content_hash ~ '^[0-9a-f]{64}$'),
  owner_id uuid NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT askdata_entities_identity_key UNIQUE(tenant_id,entity_id,version_no),
  CONSTRAINT askdata_entities_identity_tenant_key UNIQUE(id,tenant_id),
  CONSTRAINT askdata_entities_identity_domain_tenant_key UNIQUE(id,domain_id,tenant_id),
  CONSTRAINT askdata_entities_domain_fk
    FOREIGN KEY(domain_id,tenant_id)
    REFERENCES askdata.domains(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT askdata_entities_owner_fk
    FOREIGN KEY(owner_id,tenant_id)
    REFERENCES platform.users(id,tenant_id) ON DELETE RESTRICT
);

CREATE TABLE askdata.semantic_models(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id) ON DELETE RESTRICT,
  domain_id uuid NOT NULL,
  model_id uuid NOT NULL,
  version_no integer NOT NULL CHECK(version_no>0),
  code citext NOT NULL,
  name text NOT NULL CHECK(length(btrim(name)) BETWEEN 1 AND 200),
  description text NOT NULL DEFAULT '' CHECK(length(description)<=4000),
  entity_version_id uuid,
  dataset_id uuid NOT NULL,
  dataset_version_id uuid NOT NULL,
  materialization_id uuid NOT NULL,
  dataset_schema_hash text NOT NULL CHECK(dataset_schema_hash ~ '^[0-9a-f]{64}$'),
  layer text NOT NULL CHECK(layer IN ('DWS','ADS')),
  grain_contract jsonb NOT NULL CHECK(
    jsonb_typeof(grain_contract)='object'
    AND pg_column_size(grain_contract)<=65536
    AND askdata.json_is_safe(grain_contract)
  ),
  primary_time_field_id text NOT NULL DEFAULT '' CHECK(
    length(primary_time_field_id)<=128
    AND primary_time_field_id !~ '[[:cntrl:]]'
  ),
  status text NOT NULL DEFAULT 'DRAFT' CHECK(status IN ('DRAFT','CERTIFIED','DEPRECATED')),
  content_hash text NOT NULL CHECK(content_hash ~ '^[0-9a-f]{64}$'),
  owner_id uuid NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT askdata_semantic_models_identity_key UNIQUE(tenant_id,model_id,version_no),
  CONSTRAINT askdata_semantic_models_code_version_key UNIQUE(tenant_id,domain_id,code,version_no),
  CONSTRAINT askdata_semantic_models_identity_tenant_key UNIQUE(id,tenant_id),
  CONSTRAINT askdata_semantic_models_identity_domain_tenant_key UNIQUE(id,domain_id,tenant_id),
  CONSTRAINT askdata_semantic_models_domain_fk
    FOREIGN KEY(domain_id,tenant_id)
    REFERENCES askdata.domains(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT askdata_semantic_models_entity_fk
    FOREIGN KEY(entity_version_id,domain_id,tenant_id)
    REFERENCES askdata.entities(id,domain_id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT askdata_semantic_models_dataset_version_fk
    FOREIGN KEY(dataset_version_id,dataset_id,tenant_id)
    REFERENCES platform.dataset_versions(id,dataset_id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT askdata_semantic_models_materialization_fk
    FOREIGN KEY(materialization_id,tenant_id)
    REFERENCES platform.dataset_materializations(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT askdata_semantic_models_owner_fk
    FOREIGN KEY(owner_id,tenant_id)
    REFERENCES platform.users(id,tenant_id) ON DELETE RESTRICT
);

CREATE TABLE askdata.measures(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id) ON DELETE RESTRICT,
  domain_id uuid NOT NULL,
  measure_id uuid NOT NULL,
  version_no integer NOT NULL CHECK(version_no>0),
  semantic_model_version_id uuid NOT NULL,
  code citext NOT NULL,
  name text NOT NULL CHECK(length(btrim(name)) BETWEEN 1 AND 200),
  description text NOT NULL DEFAULT '' CHECK(length(description)<=4000),
  formula_ast jsonb NOT NULL CHECK(
    jsonb_typeof(formula_ast)='object'
    AND pg_column_size(formula_ast)<=65536
    AND askdata.json_is_safe(formula_ast)
  ),
  aggregation text NOT NULL CHECK(aggregation IN ('SUM','AVG','MIN','MAX','COUNT','COUNT_DISTINCT')),
  additivity text NOT NULL CHECK(additivity IN ('ADDITIVE','SEMI_ADDITIVE','NON_ADDITIVE')),
  data_type text NOT NULL CHECK(data_type IN ('INTEGER','DECIMAL')),
  unit text NOT NULL DEFAULT '' CHECK(length(unit)<=64),
  status text NOT NULL DEFAULT 'DRAFT' CHECK(status IN ('DRAFT','CERTIFIED','DEPRECATED')),
  content_hash text NOT NULL CHECK(content_hash ~ '^[0-9a-f]{64}$'),
  owner_id uuid NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT askdata_measures_identity_key UNIQUE(tenant_id,measure_id,version_no),
  CONSTRAINT askdata_measures_model_code_key UNIQUE(tenant_id,semantic_model_version_id,code,version_no),
  CONSTRAINT askdata_measures_identity_tenant_key UNIQUE(id,tenant_id),
  CONSTRAINT askdata_measures_identity_domain_tenant_key UNIQUE(id,domain_id,tenant_id),
  CONSTRAINT askdata_measures_domain_fk
    FOREIGN KEY(domain_id,tenant_id)
    REFERENCES askdata.domains(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT askdata_measures_model_fk
    FOREIGN KEY(semantic_model_version_id,domain_id,tenant_id)
    REFERENCES askdata.semantic_models(id,domain_id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT askdata_measures_owner_fk
    FOREIGN KEY(owner_id,tenant_id)
    REFERENCES platform.users(id,tenant_id) ON DELETE RESTRICT
);

CREATE TABLE askdata.metrics(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id) ON DELETE RESTRICT,
  domain_id uuid NOT NULL,
  code citext NOT NULL,
  name text NOT NULL CHECK(length(btrim(name)) BETWEEN 1 AND 200),
  description text NOT NULL DEFAULT '' CHECK(length(description)<=4000),
  status text NOT NULL DEFAULT 'DRAFT' CHECK(status IN ('DRAFT','ACTIVE','DEPRECATED')),
  owner_id uuid NOT NULL,
  version bigint NOT NULL DEFAULT 1 CHECK(version>0),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT askdata_metrics_identity_tenant_key UNIQUE(id,tenant_id),
  CONSTRAINT askdata_metrics_identity_domain_tenant_key UNIQUE(id,domain_id,tenant_id),
  CONSTRAINT askdata_metrics_code_key UNIQUE(tenant_id,domain_id,code),
  CONSTRAINT askdata_metrics_domain_fk
    FOREIGN KEY(domain_id,tenant_id)
    REFERENCES askdata.domains(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT askdata_metrics_owner_fk
    FOREIGN KEY(owner_id,tenant_id)
    REFERENCES platform.users(id,tenant_id) ON DELETE RESTRICT
);

CREATE TABLE askdata.metric_versions(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id) ON DELETE RESTRICT,
  domain_id uuid NOT NULL,
  metric_id uuid NOT NULL,
  version_no integer NOT NULL CHECK(version_no>0),
  semantic_model_version_id uuid NOT NULL,
  formula_ast jsonb NOT NULL CHECK(
    jsonb_typeof(formula_ast)='object'
    AND pg_column_size(formula_ast)<=131072
    AND askdata.json_is_safe(formula_ast)
  ),
  default_filters_ast jsonb NOT NULL DEFAULT '{"type":"TRUE"}'::jsonb CHECK(
    jsonb_typeof(default_filters_ast)='object'
    AND pg_column_size(default_filters_ast)<=65536
    AND askdata.json_is_safe(default_filters_ast)
  ),
  unit text NOT NULL DEFAULT '' CHECK(length(unit)<=64),
  time_grain text NOT NULL DEFAULT 'NONE' CHECK(time_grain IN ('NONE','DAY','WEEK','MONTH','QUARTER','YEAR')),
  additivity text NOT NULL CHECK(additivity IN ('ADDITIVE','SEMI_ADDITIVE','NON_ADDITIVE')),
  null_policy text NOT NULL DEFAULT 'PRESERVE' CHECK(null_policy IN ('PRESERVE','ZERO','REJECT')),
  status text NOT NULL DEFAULT 'DRAFT' CHECK(status IN ('DRAFT','CERTIFIED','DEPRECATED')),
  content_hash text NOT NULL CHECK(content_hash ~ '^[0-9a-f]{64}$'),
  owner_id uuid NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT askdata_metric_versions_identity_key UNIQUE(tenant_id,metric_id,version_no),
  CONSTRAINT askdata_metric_versions_identity_tenant_key UNIQUE(id,tenant_id),
  CONSTRAINT askdata_metric_versions_identity_domain_tenant_key UNIQUE(id,domain_id,tenant_id),
  CONSTRAINT askdata_metric_versions_domain_fk
    FOREIGN KEY(domain_id,tenant_id)
    REFERENCES askdata.domains(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT askdata_metric_versions_metric_fk
    FOREIGN KEY(metric_id,domain_id,tenant_id)
    REFERENCES askdata.metrics(id,domain_id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT askdata_metric_versions_model_fk
    FOREIGN KEY(semantic_model_version_id,domain_id,tenant_id)
    REFERENCES askdata.semantic_models(id,domain_id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT askdata_metric_versions_owner_fk
    FOREIGN KEY(owner_id,tenant_id)
    REFERENCES platform.users(id,tenant_id) ON DELETE RESTRICT
);

CREATE TABLE askdata.metric_version_measures(
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id) ON DELETE RESTRICT,
  domain_id uuid NOT NULL,
  metric_version_id uuid NOT NULL,
  measure_version_id uuid NOT NULL,
  ordinal integer NOT NULL CHECK(ordinal BETWEEN 1 AND 64),
  PRIMARY KEY(tenant_id,metric_version_id,measure_version_id),
  UNIQUE(tenant_id,metric_version_id,ordinal),
  CONSTRAINT askdata_metric_version_measures_domain_fk
    FOREIGN KEY(domain_id,tenant_id)
    REFERENCES askdata.domains(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT askdata_metric_version_measures_metric_fk
    FOREIGN KEY(metric_version_id,domain_id,tenant_id)
    REFERENCES askdata.metric_versions(id,domain_id,tenant_id) ON DELETE CASCADE,
  CONSTRAINT askdata_metric_version_measures_measure_fk
    FOREIGN KEY(measure_version_id,domain_id,tenant_id)
    REFERENCES askdata.measures(id,domain_id,tenant_id) ON DELETE RESTRICT
);

CREATE TABLE askdata.dimensions(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id) ON DELETE RESTRICT,
  domain_id uuid NOT NULL,
  dimension_id uuid NOT NULL,
  version_no integer NOT NULL CHECK(version_no>0),
  semantic_model_version_id uuid NOT NULL,
  logical_field_id text NOT NULL CHECK(
    length(logical_field_id) BETWEEN 1 AND 128
    AND logical_field_id=btrim(logical_field_id)
    AND logical_field_id !~ '[[:cntrl:]]'
  ),
  code citext NOT NULL,
  name text NOT NULL CHECK(length(btrim(name)) BETWEEN 1 AND 200),
  description text NOT NULL DEFAULT '' CHECK(length(description)<=4000),
  dimension_kind text NOT NULL CHECK(dimension_kind IN ('CATEGORICAL','TIME','ENTITY')),
  sensitivity text NOT NULL DEFAULT 'INTERNAL' CHECK(sensitivity IN ('PUBLIC','INTERNAL','CONFIDENTIAL','RESTRICTED')),
  member_index_policy text NOT NULL DEFAULT 'EXACT_ONLY' CHECK(
    member_index_policy IN ('FULL','EXACT_ONLY','ON_DEMAND','NONE')
  ),
  high_cardinality boolean NOT NULL DEFAULT false,
  status text NOT NULL DEFAULT 'DRAFT' CHECK(status IN ('DRAFT','CERTIFIED','DEPRECATED')),
  content_hash text NOT NULL CHECK(content_hash ~ '^[0-9a-f]{64}$'),
  owner_id uuid NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT askdata_dimensions_identity_key UNIQUE(tenant_id,dimension_id,version_no),
  CONSTRAINT askdata_dimensions_member_policy_cardinality_check CHECK(
    NOT high_cardinality OR member_index_policy IN ('ON_DEMAND','NONE')
  ),
  CONSTRAINT askdata_dimensions_member_policy_sensitivity_check CHECK(
    sensitivity NOT IN ('CONFIDENTIAL','RESTRICTED')
    OR member_index_policy IN ('EXACT_ONLY','NONE')
  ),
  CONSTRAINT askdata_dimensions_model_field_key UNIQUE(tenant_id,semantic_model_version_id,logical_field_id,version_no),
  CONSTRAINT askdata_dimensions_identity_tenant_key UNIQUE(id,tenant_id),
  CONSTRAINT askdata_dimensions_identity_domain_tenant_key UNIQUE(id,domain_id,tenant_id),
  CONSTRAINT askdata_dimensions_domain_fk
    FOREIGN KEY(domain_id,tenant_id)
    REFERENCES askdata.domains(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT askdata_dimensions_model_fk
    FOREIGN KEY(semantic_model_version_id,domain_id,tenant_id)
    REFERENCES askdata.semantic_models(id,domain_id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT askdata_dimensions_owner_fk
    FOREIGN KEY(owner_id,tenant_id)
    REFERENCES platform.users(id,tenant_id) ON DELETE RESTRICT
);

CREATE TABLE askdata.hierarchies(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id) ON DELETE RESTRICT,
  domain_id uuid NOT NULL,
  hierarchy_id uuid NOT NULL,
  version_no integer NOT NULL CHECK(version_no>0),
  code citext NOT NULL,
  name text NOT NULL CHECK(length(btrim(name)) BETWEEN 1 AND 200),
  description text NOT NULL DEFAULT '' CHECK(length(description)<=4000),
  status text NOT NULL DEFAULT 'DRAFT' CHECK(status IN ('DRAFT','CERTIFIED','DEPRECATED')),
  content_hash text NOT NULL CHECK(content_hash ~ '^[0-9a-f]{64}$'),
  owner_id uuid NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT askdata_hierarchies_identity_key UNIQUE(tenant_id,hierarchy_id,version_no),
  CONSTRAINT askdata_hierarchies_identity_tenant_key UNIQUE(id,tenant_id),
  CONSTRAINT askdata_hierarchies_identity_domain_tenant_key UNIQUE(id,domain_id,tenant_id),
  CONSTRAINT askdata_hierarchies_domain_fk
    FOREIGN KEY(domain_id,tenant_id)
    REFERENCES askdata.domains(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT askdata_hierarchies_owner_fk
    FOREIGN KEY(owner_id,tenant_id)
    REFERENCES platform.users(id,tenant_id) ON DELETE RESTRICT
);

CREATE TABLE askdata.hierarchy_levels(
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id) ON DELETE RESTRICT,
  domain_id uuid NOT NULL,
  hierarchy_version_id uuid NOT NULL,
  dimension_version_id uuid NOT NULL,
  ordinal integer NOT NULL CHECK(ordinal BETWEEN 1 AND 32),
  PRIMARY KEY(tenant_id,hierarchy_version_id,dimension_version_id),
  UNIQUE(tenant_id,hierarchy_version_id,ordinal),
  CONSTRAINT askdata_hierarchy_levels_domain_fk
    FOREIGN KEY(domain_id,tenant_id)
    REFERENCES askdata.domains(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT askdata_hierarchy_levels_hierarchy_fk
    FOREIGN KEY(hierarchy_version_id,domain_id,tenant_id)
    REFERENCES askdata.hierarchies(id,domain_id,tenant_id) ON DELETE CASCADE,
  CONSTRAINT askdata_hierarchy_levels_dimension_fk
    FOREIGN KEY(dimension_version_id,domain_id,tenant_id)
    REFERENCES askdata.dimensions(id,domain_id,tenant_id) ON DELETE RESTRICT
);

CREATE TABLE askdata.relationships(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id) ON DELETE RESTRICT,
  domain_id uuid NOT NULL,
  relationship_id uuid NOT NULL,
  version_no integer NOT NULL CHECK(version_no>0),
  left_model_version_id uuid NOT NULL,
  right_model_version_id uuid NOT NULL,
  relationship_type text NOT NULL CHECK(relationship_type IN ('MODEL_JOIN','ENTITY_LINK','DIMENSION_COMPATIBILITY')),
  join_type text NOT NULL CHECK(join_type IN ('INNER','LEFT','RIGHT','FULL','NONE')),
  cardinality text NOT NULL CHECK(cardinality IN ('ONE_TO_ONE','MANY_TO_ONE','ONE_TO_MANY','MANY_TO_MANY')),
  join_ast jsonb NOT NULL CHECK(
    jsonb_typeof(join_ast)='object'
    AND pg_column_size(join_ast)<=65536
    AND askdata.json_is_safe(join_ast)
  ),
  fanout_policy text NOT NULL DEFAULT 'BLOCK' CHECK(fanout_policy IN ('BLOCK','CERTIFIED_PREAGG','SAFE')),
  status text NOT NULL DEFAULT 'DRAFT' CHECK(status IN ('DRAFT','CERTIFIED','DEPRECATED')),
  content_hash text NOT NULL CHECK(content_hash ~ '^[0-9a-f]{64}$'),
  owner_id uuid NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT askdata_relationships_distinct_models_check CHECK(left_model_version_id<>right_model_version_id),
  CONSTRAINT askdata_relationships_identity_key UNIQUE(tenant_id,relationship_id,version_no),
  CONSTRAINT askdata_relationships_identity_tenant_key UNIQUE(id,tenant_id),
  CONSTRAINT askdata_relationships_identity_domain_tenant_key UNIQUE(id,domain_id,tenant_id),
  CONSTRAINT askdata_relationships_domain_fk
    FOREIGN KEY(domain_id,tenant_id)
    REFERENCES askdata.domains(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT askdata_relationships_left_model_fk
    FOREIGN KEY(left_model_version_id,domain_id,tenant_id)
    REFERENCES askdata.semantic_models(id,domain_id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT askdata_relationships_right_model_fk
    FOREIGN KEY(right_model_version_id,domain_id,tenant_id)
    REFERENCES askdata.semantic_models(id,domain_id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT askdata_relationships_owner_fk
    FOREIGN KEY(owner_id,tenant_id)
    REFERENCES platform.users(id,tenant_id) ON DELETE RESTRICT
);

CREATE TABLE askdata.quality_rules(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id) ON DELETE RESTRICT,
  domain_id uuid NOT NULL,
  quality_rule_id uuid NOT NULL,
  version_no integer NOT NULL CHECK(version_no>0),
  target_type text NOT NULL CHECK(target_type IN ('SEMANTIC_MODEL','METRIC','DIMENSION')),
  target_version_id uuid NOT NULL,
  code citext NOT NULL,
  name text NOT NULL CHECK(length(btrim(name)) BETWEEN 1 AND 200),
  rule_ast jsonb NOT NULL CHECK(
    jsonb_typeof(rule_ast)='object'
    AND pg_column_size(rule_ast)<=65536
    AND askdata.json_is_safe(rule_ast)
  ),
  severity text NOT NULL CHECK(severity IN ('INFO','WARNING','BLOCKING')),
  status text NOT NULL DEFAULT 'DRAFT' CHECK(status IN ('DRAFT','CERTIFIED','DEPRECATED')),
  content_hash text NOT NULL CHECK(content_hash ~ '^[0-9a-f]{64}$'),
  owner_id uuid NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT askdata_quality_rules_identity_key UNIQUE(tenant_id,quality_rule_id,version_no),
  CONSTRAINT askdata_quality_rules_identity_tenant_key UNIQUE(id,tenant_id),
  CONSTRAINT askdata_quality_rules_identity_domain_tenant_key UNIQUE(id,domain_id,tenant_id),
  CONSTRAINT askdata_quality_rules_domain_fk
    FOREIGN KEY(domain_id,tenant_id)
    REFERENCES askdata.domains(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT askdata_quality_rules_owner_fk
    FOREIGN KEY(owner_id,tenant_id)
    REFERENCES platform.users(id,tenant_id) ON DELETE RESTRICT
);

CREATE TABLE askdata.business_terms(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id) ON DELETE RESTRICT,
  domain_id uuid NOT NULL,
  term_id uuid NOT NULL,
  version_no integer NOT NULL CHECK(version_no>0),
  code citext NOT NULL,
  name text NOT NULL CHECK(length(btrim(name)) BETWEEN 1 AND 200),
  definition text NOT NULL CHECK(length(btrim(definition)) BETWEEN 1 AND 4000),
  aliases text[] NOT NULL DEFAULT '{}'::text[] CHECK(cardinality(aliases)<=64),
  status text NOT NULL DEFAULT 'DRAFT' CHECK(status IN ('DRAFT','CERTIFIED','DEPRECATED')),
  content_hash text NOT NULL CHECK(content_hash ~ '^[0-9a-f]{64}$'),
  owner_id uuid NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT askdata_business_terms_identity_key UNIQUE(tenant_id,term_id,version_no),
  CONSTRAINT askdata_business_terms_identity_tenant_key UNIQUE(id,tenant_id),
  CONSTRAINT askdata_business_terms_identity_domain_tenant_key UNIQUE(id,domain_id,tenant_id),
  CONSTRAINT askdata_business_terms_domain_fk
    FOREIGN KEY(domain_id,tenant_id)
    REFERENCES askdata.domains(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT askdata_business_terms_owner_fk
    FOREIGN KEY(owner_id,tenant_id)
    REFERENCES platform.users(id,tenant_id) ON DELETE RESTRICT
);

CREATE OR REPLACE FUNCTION askdata.protect_certified_version()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
  IF OLD.status='CERTIFIED' THEN
    RAISE EXCEPTION 'certified askdata versions are immutable'
      USING ERRCODE='55000';
  END IF;
  RETURN CASE WHEN TG_OP='DELETE' THEN OLD ELSE NEW END;
END
$$;

CREATE OR REPLACE FUNCTION askdata.validate_semantic_model_source()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE source_valid boolean;
BEGIN
  SELECT EXISTS(
    SELECT 1
    FROM platform.datasets AS dataset
    JOIN platform.dataset_versions AS version
      ON version.id=NEW.dataset_version_id
     AND version.dataset_id=dataset.id
     AND version.tenant_id=dataset.tenant_id
    JOIN platform.dataset_materializations AS materialization
      ON materialization.id=NEW.materialization_id
     AND materialization.dataset_id=dataset.id
     AND materialization.dataset_version_id=version.id
     AND materialization.tenant_id=dataset.tenant_id
    WHERE dataset.id=NEW.dataset_id
      AND dataset.tenant_id=NEW.tenant_id
      AND dataset.domain_id=NEW.domain_id
      AND dataset.deleted_at IS NULL
      AND dataset.status='PUBLISHED'
      AND dataset.current_published_version_id=version.id
      AND version.status='PUBLISHED'
      AND version.layer IN ('DWS','ADS')
      AND version.layer=NEW.layer
      AND version.schema_hash=NEW.dataset_schema_hash
      AND materialization.status='ACTIVE'
      AND materialization.layer=version.layer
      AND materialization.schema_hash=version.schema_hash
  ) INTO source_valid;
  IF NOT source_valid THEN
    RAISE EXCEPTION 'semantic model source must be an active published DWS/ADS materialization'
      USING ERRCODE='23514';
  END IF;
  RETURN NEW;
END
$$;

CREATE OR REPLACE FUNCTION askdata.validate_certified_dependency()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE dependency_valid boolean := false;
BEGIN
  IF NEW.status<>'CERTIFIED' THEN
    RETURN NEW;
  END IF;
  CASE TG_TABLE_NAME
    WHEN 'semantic_models' THEN
      dependency_valid := NEW.entity_version_id IS NULL OR EXISTS(
        SELECT 1 FROM askdata.entities
        WHERE id=NEW.entity_version_id AND domain_id=NEW.domain_id
          AND tenant_id=NEW.tenant_id AND status='CERTIFIED'
      );
    WHEN 'measures' THEN
      SELECT EXISTS(
        SELECT 1 FROM askdata.semantic_models
        WHERE id=NEW.semantic_model_version_id AND domain_id=NEW.domain_id
          AND tenant_id=NEW.tenant_id AND status='CERTIFIED'
      ) INTO dependency_valid;
    WHEN 'metric_versions' THEN
      SELECT EXISTS(
        SELECT 1 FROM askdata.semantic_models
        WHERE id=NEW.semantic_model_version_id AND domain_id=NEW.domain_id
          AND tenant_id=NEW.tenant_id AND status='CERTIFIED'
      ) AND EXISTS(
        SELECT 1 FROM askdata.metric_version_measures
        WHERE metric_version_id=NEW.id AND domain_id=NEW.domain_id
          AND tenant_id=NEW.tenant_id
      ) AND NOT EXISTS(
        SELECT 1
        FROM askdata.metric_version_measures AS link
        JOIN askdata.measures AS measure
          ON measure.id=link.measure_version_id
         AND measure.domain_id=link.domain_id
         AND measure.tenant_id=link.tenant_id
        WHERE link.metric_version_id=NEW.id
          AND link.domain_id=NEW.domain_id
          AND link.tenant_id=NEW.tenant_id
          AND measure.status<>'CERTIFIED'
      ) INTO dependency_valid;
    WHEN 'dimensions' THEN
      SELECT EXISTS(
        SELECT 1 FROM askdata.semantic_models
        WHERE id=NEW.semantic_model_version_id AND domain_id=NEW.domain_id
          AND tenant_id=NEW.tenant_id AND status='CERTIFIED'
      ) INTO dependency_valid;
    WHEN 'hierarchies' THEN
      SELECT EXISTS(
        SELECT 1 FROM askdata.hierarchy_levels
        WHERE hierarchy_version_id=NEW.id AND domain_id=NEW.domain_id
          AND tenant_id=NEW.tenant_id
      ) AND NOT EXISTS(
        SELECT 1
        FROM askdata.hierarchy_levels AS level
        JOIN askdata.dimensions AS dimension
          ON dimension.id=level.dimension_version_id
         AND dimension.domain_id=level.domain_id
         AND dimension.tenant_id=level.tenant_id
        WHERE level.hierarchy_version_id=NEW.id
          AND level.domain_id=NEW.domain_id
          AND level.tenant_id=NEW.tenant_id
          AND dimension.status<>'CERTIFIED'
      ) INTO dependency_valid;
    WHEN 'relationships' THEN
      SELECT count(*)=2 AND bool_and(status='CERTIFIED')
      FROM askdata.semantic_models
      WHERE id IN (NEW.left_model_version_id,NEW.right_model_version_id)
        AND domain_id=NEW.domain_id AND tenant_id=NEW.tenant_id
      INTO dependency_valid;
    ELSE
      dependency_valid := true;
  END CASE;
  IF NOT COALESCE(dependency_valid,false) THEN
    RAISE EXCEPTION 'certified semantic object requires certified dependencies'
      USING ERRCODE='23514';
  END IF;
  RETURN NEW;
END
$$;

CREATE OR REPLACE FUNCTION askdata.validate_quality_rule_target()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE target_valid boolean := false;
BEGIN
  CASE NEW.target_type
    WHEN 'SEMANTIC_MODEL' THEN
      SELECT EXISTS(
        SELECT 1 FROM askdata.semantic_models
        WHERE id=NEW.target_version_id AND domain_id=NEW.domain_id
          AND tenant_id=NEW.tenant_id
          AND status=CASE WHEN NEW.status='CERTIFIED' THEN 'CERTIFIED' ELSE status END
          AND status<>'DEPRECATED'
      ) INTO target_valid;
    WHEN 'METRIC' THEN
      SELECT EXISTS(
        SELECT 1 FROM askdata.metric_versions
        WHERE id=NEW.target_version_id AND domain_id=NEW.domain_id
          AND tenant_id=NEW.tenant_id
          AND status=CASE WHEN NEW.status='CERTIFIED' THEN 'CERTIFIED' ELSE status END
          AND status<>'DEPRECATED'
      ) INTO target_valid;
    WHEN 'DIMENSION' THEN
      SELECT EXISTS(
        SELECT 1 FROM askdata.dimensions
        WHERE id=NEW.target_version_id AND domain_id=NEW.domain_id
          AND tenant_id=NEW.tenant_id
          AND status=CASE WHEN NEW.status='CERTIFIED' THEN 'CERTIFIED' ELSE status END
          AND status<>'DEPRECATED'
      ) INTO target_valid;
  END CASE;
  IF NOT COALESCE(target_valid,false) THEN
    RAISE EXCEPTION 'quality rule target is missing, deprecated, cross-domain, or uncertified'
      USING ERRCODE='23514';
  END IF;
  RETURN NEW;
END
$$;

CREATE OR REPLACE FUNCTION askdata.protect_certified_child_link()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE parent_certified boolean := false;
BEGIN
  IF TG_TABLE_NAME='metric_version_measures' THEN
    IF TG_OP<>'INSERT' THEN
      SELECT status='CERTIFIED' INTO parent_certified
      FROM askdata.metric_versions
      WHERE id=OLD.metric_version_id AND domain_id=OLD.domain_id
        AND tenant_id=OLD.tenant_id;
    END IF;
    IF NOT COALESCE(parent_certified,false) AND TG_OP<>'DELETE' THEN
      SELECT status='CERTIFIED' INTO parent_certified
      FROM askdata.metric_versions
      WHERE id=NEW.metric_version_id AND domain_id=NEW.domain_id
        AND tenant_id=NEW.tenant_id;
    END IF;
  ELSIF TG_TABLE_NAME='hierarchy_levels' THEN
    IF TG_OP<>'INSERT' THEN
      SELECT status='CERTIFIED' INTO parent_certified
      FROM askdata.hierarchies
      WHERE id=OLD.hierarchy_version_id AND domain_id=OLD.domain_id
        AND tenant_id=OLD.tenant_id;
    END IF;
    IF NOT COALESCE(parent_certified,false) AND TG_OP<>'DELETE' THEN
      SELECT status='CERTIFIED' INTO parent_certified
      FROM askdata.hierarchies
      WHERE id=NEW.hierarchy_version_id AND domain_id=NEW.domain_id
        AND tenant_id=NEW.tenant_id;
    END IF;
  END IF;
  IF COALESCE(parent_certified,false) THEN
    RAISE EXCEPTION 'certified semantic child links are immutable'
      USING ERRCODE='55000';
  END IF;
  RETURN CASE WHEN TG_OP='DELETE' THEN OLD ELSE NEW END;
END
$$;

REVOKE ALL ON FUNCTION
  askdata.protect_certified_version(),
  askdata.validate_semantic_model_source(),
  askdata.validate_certified_dependency(),
  askdata.validate_quality_rule_target(),
  askdata.protect_certified_child_link()
FROM PUBLIC;

CREATE TRIGGER askdata_semantic_models_validate_source
BEFORE INSERT OR UPDATE ON askdata.semantic_models
FOR EACH ROW EXECUTE FUNCTION askdata.validate_semantic_model_source();

CREATE TRIGGER askdata_semantic_models_validate_dependencies
BEFORE INSERT OR UPDATE ON askdata.semantic_models
FOR EACH ROW EXECUTE FUNCTION askdata.validate_certified_dependency();
CREATE TRIGGER askdata_measures_validate_dependencies
BEFORE INSERT OR UPDATE ON askdata.measures
FOR EACH ROW EXECUTE FUNCTION askdata.validate_certified_dependency();
CREATE TRIGGER askdata_metric_versions_validate_dependencies
BEFORE INSERT OR UPDATE ON askdata.metric_versions
FOR EACH ROW EXECUTE FUNCTION askdata.validate_certified_dependency();
CREATE TRIGGER askdata_dimensions_validate_dependencies
BEFORE INSERT OR UPDATE ON askdata.dimensions
FOR EACH ROW EXECUTE FUNCTION askdata.validate_certified_dependency();
CREATE TRIGGER askdata_hierarchies_validate_dependencies
BEFORE INSERT OR UPDATE ON askdata.hierarchies
FOR EACH ROW EXECUTE FUNCTION askdata.validate_certified_dependency();
CREATE TRIGGER askdata_relationships_validate_dependencies
BEFORE INSERT OR UPDATE ON askdata.relationships
FOR EACH ROW EXECUTE FUNCTION askdata.validate_certified_dependency();
CREATE TRIGGER askdata_quality_rules_validate_target
BEFORE INSERT OR UPDATE ON askdata.quality_rules
FOR EACH ROW EXECUTE FUNCTION askdata.validate_quality_rule_target();
CREATE TRIGGER askdata_metric_version_measures_protect_certified
BEFORE INSERT OR UPDATE OR DELETE ON askdata.metric_version_measures
FOR EACH ROW EXECUTE FUNCTION askdata.protect_certified_child_link();
CREATE TRIGGER askdata_hierarchy_levels_protect_certified
BEFORE INSERT OR UPDATE OR DELETE ON askdata.hierarchy_levels
FOR EACH ROW EXECUTE FUNCTION askdata.protect_certified_child_link();

CREATE TRIGGER askdata_domains_set_updated_at BEFORE UPDATE ON askdata.domains
FOR EACH ROW EXECUTE FUNCTION askdata.set_updated_at();
CREATE TRIGGER askdata_entities_set_updated_at BEFORE UPDATE ON askdata.entities
FOR EACH ROW EXECUTE FUNCTION askdata.set_updated_at();
CREATE TRIGGER askdata_semantic_models_set_updated_at BEFORE UPDATE ON askdata.semantic_models
FOR EACH ROW EXECUTE FUNCTION askdata.set_updated_at();
CREATE TRIGGER askdata_measures_set_updated_at BEFORE UPDATE ON askdata.measures
FOR EACH ROW EXECUTE FUNCTION askdata.set_updated_at();
CREATE TRIGGER askdata_metrics_set_updated_at BEFORE UPDATE ON askdata.metrics
FOR EACH ROW EXECUTE FUNCTION askdata.set_updated_at();
CREATE TRIGGER askdata_metric_versions_set_updated_at BEFORE UPDATE ON askdata.metric_versions
FOR EACH ROW EXECUTE FUNCTION askdata.set_updated_at();
CREATE TRIGGER askdata_dimensions_set_updated_at BEFORE UPDATE ON askdata.dimensions
FOR EACH ROW EXECUTE FUNCTION askdata.set_updated_at();
CREATE TRIGGER askdata_hierarchies_set_updated_at BEFORE UPDATE ON askdata.hierarchies
FOR EACH ROW EXECUTE FUNCTION askdata.set_updated_at();
CREATE TRIGGER askdata_relationships_set_updated_at BEFORE UPDATE ON askdata.relationships
FOR EACH ROW EXECUTE FUNCTION askdata.set_updated_at();
CREATE TRIGGER askdata_quality_rules_set_updated_at BEFORE UPDATE ON askdata.quality_rules
FOR EACH ROW EXECUTE FUNCTION askdata.set_updated_at();
CREATE TRIGGER askdata_business_terms_set_updated_at BEFORE UPDATE ON askdata.business_terms
FOR EACH ROW EXECUTE FUNCTION askdata.set_updated_at();

CREATE TRIGGER askdata_entities_protect_certified BEFORE UPDATE OR DELETE ON askdata.entities
FOR EACH ROW EXECUTE FUNCTION askdata.protect_certified_version();
CREATE TRIGGER askdata_semantic_models_protect_certified BEFORE UPDATE OR DELETE ON askdata.semantic_models
FOR EACH ROW EXECUTE FUNCTION askdata.protect_certified_version();
CREATE TRIGGER askdata_measures_protect_certified BEFORE UPDATE OR DELETE ON askdata.measures
FOR EACH ROW EXECUTE FUNCTION askdata.protect_certified_version();
CREATE TRIGGER askdata_metric_versions_protect_certified BEFORE UPDATE OR DELETE ON askdata.metric_versions
FOR EACH ROW EXECUTE FUNCTION askdata.protect_certified_version();
CREATE TRIGGER askdata_dimensions_protect_certified BEFORE UPDATE OR DELETE ON askdata.dimensions
FOR EACH ROW EXECUTE FUNCTION askdata.protect_certified_version();
CREATE TRIGGER askdata_hierarchies_protect_certified BEFORE UPDATE OR DELETE ON askdata.hierarchies
FOR EACH ROW EXECUTE FUNCTION askdata.protect_certified_version();
CREATE TRIGGER askdata_relationships_protect_certified BEFORE UPDATE OR DELETE ON askdata.relationships
FOR EACH ROW EXECUTE FUNCTION askdata.protect_certified_version();
CREATE TRIGGER askdata_quality_rules_protect_certified BEFORE UPDATE OR DELETE ON askdata.quality_rules
FOR EACH ROW EXECUTE FUNCTION askdata.protect_certified_version();
CREATE TRIGGER askdata_business_terms_protect_certified BEFORE UPDATE OR DELETE ON askdata.business_terms
FOR EACH ROW EXECUTE FUNCTION askdata.protect_certified_version();

CREATE INDEX askdata_entities_lookup_idx ON askdata.entities(tenant_id,domain_id,code,status,version_no DESC);
CREATE INDEX askdata_semantic_models_lookup_idx ON askdata.semantic_models(tenant_id,domain_id,status,code,version_no DESC);
CREATE INDEX askdata_measures_model_idx ON askdata.measures(tenant_id,semantic_model_version_id,status,code);
CREATE INDEX askdata_metric_versions_lookup_idx ON askdata.metric_versions(tenant_id,domain_id,status,metric_id,version_no DESC);
CREATE INDEX askdata_dimensions_model_idx ON askdata.dimensions(tenant_id,semantic_model_version_id,status,code);
CREATE INDEX askdata_relationships_models_idx ON askdata.relationships(tenant_id,domain_id,left_model_version_id,right_model_version_id,status);
CREATE INDEX askdata_quality_rules_target_idx ON askdata.quality_rules(tenant_id,domain_id,target_type,target_version_id,status);
CREATE INDEX askdata_business_terms_lookup_idx ON askdata.business_terms(tenant_id,domain_id,status,code);
CREATE INDEX askdata_business_terms_aliases_idx ON askdata.business_terms USING gin(aliases);

DO $rls$
DECLARE relation_name text;
BEGIN
  FOREACH relation_name IN ARRAY ARRAY[
    'entities','semantic_models','measures','metrics','metric_versions',
    'metric_version_measures','dimensions','hierarchies','hierarchy_levels',
    'relationships','quality_rules','business_terms'
  ] LOOP
    EXECUTE format('ALTER TABLE askdata.%I ENABLE ROW LEVEL SECURITY',relation_name);
    EXECUTE format('ALTER TABLE askdata.%I FORCE ROW LEVEL SECURITY',relation_name);
    EXECUTE format(
      'CREATE POLICY %I ON askdata.%I USING(askdata.tenant_matches(tenant_id) AND askdata.domain_can_access(domain_id)) WITH CHECK(askdata.tenant_matches(tenant_id) AND askdata.domain_can_access(domain_id))',
      'askdata_'||relation_name||'_domain_isolation',relation_name
    );
  END LOOP;
END
$rls$;

ALTER TABLE askdata.domains ENABLE ROW LEVEL SECURITY;
ALTER TABLE askdata.domains FORCE ROW LEVEL SECURITY;
CREATE POLICY askdata_domains_domain_isolation ON askdata.domains
  USING(askdata.tenant_matches(tenant_id) AND askdata.domain_can_access(id))
  WITH CHECK(askdata.tenant_matches(tenant_id) AND askdata.domain_can_access(id));

COMMENT ON TABLE askdata.semantic_models IS
  'Versioned semantic models pinned to active published DWS/ADS dataset materializations';
COMMENT ON TABLE askdata.metric_versions IS
  'Immutable certified metric formula, default filters, unit, grain and additivity contract';
COMMENT ON TABLE askdata.dimensions IS
  'Versioned governed dimensions including sensitivity and member indexing policy';
COMMENT ON TABLE askdata.relationships IS
  'Certified model/entity relationships and bounded join AST; no raw SQL is accepted';
