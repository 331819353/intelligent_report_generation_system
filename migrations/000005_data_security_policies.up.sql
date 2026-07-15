-- 定义可编译的行级过滤策略和列级脱敏、聚合限制策略。
CREATE TYPE platform.row_policy_combine_mode AS ENUM ('AND', 'OR', 'DENY_OVERRIDE');
CREATE TYPE platform.row_policy_effect AS ENUM ('ALLOW', 'DENY');
CREATE TYPE platform.column_policy_type AS ENUM ('ALLOW', 'DENY', 'MASK', 'HASH', 'NULLIFY', 'AGGREGATE_ONLY');

ALTER TABLE platform.users ADD COLUMN attributes jsonb NOT NULL DEFAULT '{}'::jsonb;

CREATE TABLE platform.data_row_policies (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id),
  object_type text NOT NULL,
  object_id uuid NOT NULL,
  name text NOT NULL,
  expression_dsl jsonb NOT NULL,
  effect platform.row_policy_effect NOT NULL DEFAULT 'ALLOW',
  priority integer NOT NULL DEFAULT 100,
  combine_mode platform.row_policy_combine_mode NOT NULL DEFAULT 'AND',
  applicable_role_ids uuid[] NOT NULL DEFAULT '{}',
  applicable_user_ids uuid[] NOT NULL DEFAULT '{}',
  enabled boolean NOT NULL DEFAULT true,
  version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT row_policy_object_type_not_blank CHECK (btrim(object_type) <> ''),
  CONSTRAINT row_policy_name_not_blank CHECK (btrim(name) <> ''),
  CONSTRAINT row_policy_expression_object CHECK (jsonb_typeof(expression_dsl) = 'object'),
  UNIQUE (id, tenant_id)
);

CREATE TABLE platform.data_column_policies (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id),
  object_type text NOT NULL,
  object_id uuid NOT NULL,
  field_code text NOT NULL,
  policy_type platform.column_policy_type NOT NULL,
  mask_rule jsonb NOT NULL DEFAULT '{}'::jsonb,
  allowed_aggregations text[] NOT NULL DEFAULT '{}',
  minimum_group_size integer CHECK (minimum_group_size IS NULL OR minimum_group_size > 0),
  deny_detail_export boolean NOT NULL DEFAULT false,
  applicable_role_ids uuid[] NOT NULL DEFAULT '{}',
  applicable_user_ids uuid[] NOT NULL DEFAULT '{}',
  enabled boolean NOT NULL DEFAULT true,
  version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT column_policy_object_type_not_blank CHECK (btrim(object_type) <> ''),
  CONSTRAINT column_policy_field_code_not_blank CHECK (btrim(field_code) <> ''),
  UNIQUE (tenant_id, object_type, object_id, field_code, id)
);

CREATE INDEX row_policies_lookup_idx ON platform.data_row_policies (tenant_id, object_type, object_id, enabled, priority);
CREATE INDEX column_policies_lookup_idx ON platform.data_column_policies (tenant_id, object_type, object_id, field_code, enabled);

CREATE TRIGGER data_row_policies_set_updated_at BEFORE UPDATE ON platform.data_row_policies FOR EACH ROW EXECUTE FUNCTION platform.set_updated_at();
CREATE TRIGGER data_column_policies_set_updated_at BEFORE UPDATE ON platform.data_column_policies FOR EACH ROW EXECUTE FUNCTION platform.set_updated_at();

ALTER TABLE platform.data_row_policies ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform.data_row_policies FORCE ROW LEVEL SECURITY;
ALTER TABLE platform.data_column_policies ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform.data_column_policies FORCE ROW LEVEL SECURITY;
CREATE POLICY data_row_policies_tenant_isolation ON platform.data_row_policies USING (tenant_id = platform.current_tenant_id()) WITH CHECK (tenant_id = platform.current_tenant_id());
CREATE POLICY data_column_policies_tenant_isolation ON platform.data_column_policies USING (tenant_id = platform.current_tenant_id()) WITH CHECK (tenant_id = platform.current_tenant_id());

COMMENT ON TABLE platform.data_row_policies IS 'Validated JSON DSL injected by the server-side query compiler';
COMMENT ON TABLE platform.data_column_policies IS 'Column visibility, masking, hashing, nullification and aggregate-only rules';
