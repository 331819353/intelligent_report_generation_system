-- 语义解析规则是无需向量化的精确语言规则。平台规则为所有租户提供默认值，
-- 租户规则按 rule_type + pattern 覆盖平台规则，变更后下一次请求立即生效。
CREATE TABLE platform.semantic_parsing_rules(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid REFERENCES platform.tenants(id) ON DELETE CASCADE,
  rule_type text NOT NULL CHECK(rule_type IN (
    'METRIC_NAME_SUFFIX','ADMIN_REGION_SUFFIX',
    'QUERY_RESIDUAL_TERM','BROAD_METRIC_PHRASE'
  )),
  pattern text NOT NULL CHECK(
    length(pattern) BETWEEN 1 AND 256
    AND pattern=btrim(pattern)
    AND pattern !~ '[[:cntrl:]]'
  ),
  match_mode text NOT NULL CHECK(match_mode IN ('EXACT','SUFFIX','CONTAINS')),
  action text NOT NULL CHECK(action IN (
    'STRIP_SUFFIX','MAP_ADMIN_REGION',
    'ALLOW_DETERMINISTIC','REQUIRE_METRIC_CONFIRMATION'
  )),
  output_name text NOT NULL DEFAULT '' CHECK(
    length(output_name)<=128 AND output_name !~ '[[:cntrl:]]'
  ),
  output_code text NOT NULL DEFAULT '' CHECK(
    length(output_code)<=128
    AND (output_code='' OR output_code ~ '^[A-Za-z][A-Za-z0-9_-]{0,127}$')
  ),
  minimum_length integer NOT NULL DEFAULT 0 CHECK(minimum_length BETWEEN 0 AND 256),
  maximum_length integer NOT NULL DEFAULT 0 CHECK(maximum_length BETWEEN 0 AND 256),
  priority integer NOT NULL DEFAULT 100 CHECK(priority BETWEEN 0 AND 1000),
  status text NOT NULL DEFAULT 'ACTIVE' CHECK(status IN ('ACTIVE','DEPRECATED')),
  version bigint NOT NULL DEFAULT 1 CHECK(version>0),
  created_by uuid REFERENCES platform.users(id) ON DELETE SET NULL,
  updated_by uuid REFERENCES platform.users(id) ON DELETE SET NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT semantic_parsing_rules_shape_check CHECK(
    (rule_type='METRIC_NAME_SUFFIX' AND match_mode='SUFFIX'
      AND action='STRIP_SUFFIX' AND output_name='' AND output_code=''
      AND minimum_length BETWEEN 1 AND 32 AND maximum_length=0)
    OR
    (rule_type='ADMIN_REGION_SUFFIX' AND match_mode='SUFFIX'
      AND action='MAP_ADMIN_REGION' AND output_name<>'' AND output_code<>''
      AND minimum_length BETWEEN 1 AND 32
      AND maximum_length BETWEEN minimum_length AND 256)
    OR
    (rule_type='QUERY_RESIDUAL_TERM' AND match_mode='EXACT'
      AND action='ALLOW_DETERMINISTIC' AND output_name='' AND output_code=''
      AND minimum_length=0 AND maximum_length=0)
    OR
    (rule_type='BROAD_METRIC_PHRASE' AND match_mode='CONTAINS'
      AND action='REQUIRE_METRIC_CONFIRMATION' AND output_name=''
      AND output_code='' AND minimum_length=0 AND maximum_length=0)
  )
);

CREATE UNIQUE INDEX semantic_parsing_rules_platform_identity_key
  ON platform.semantic_parsing_rules(rule_type,lower(pattern))
  WHERE tenant_id IS NULL;
CREATE UNIQUE INDEX semantic_parsing_rules_tenant_identity_key
  ON platform.semantic_parsing_rules(tenant_id,rule_type,lower(pattern))
  WHERE tenant_id IS NOT NULL;
CREATE INDEX semantic_parsing_rules_runtime_idx
  ON platform.semantic_parsing_rules(
    tenant_id,status,rule_type,priority DESC,pattern
  );

ALTER TABLE platform.semantic_parsing_rules ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform.semantic_parsing_rules FORCE ROW LEVEL SECURITY;
CREATE POLICY semantic_parsing_rules_read_scope
  ON platform.semantic_parsing_rules FOR SELECT
  USING(tenant_id IS NULL OR tenant_id=platform.current_tenant_id());
CREATE POLICY semantic_parsing_rules_write_scope
  ON platform.semantic_parsing_rules FOR ALL
  USING(tenant_id=platform.current_tenant_id())
  WITH CHECK(tenant_id=platform.current_tenant_id());

CREATE TRIGGER semantic_parsing_rules_set_updated_at
BEFORE UPDATE ON platform.semantic_parsing_rules
FOR EACH ROW EXECUTE FUNCTION platform.set_updated_at();

INSERT INTO platform.semantic_parsing_rules(
  rule_type,pattern,match_mode,action,minimum_length,priority
)
SELECT 'METRIC_NAME_SUFFIX',pattern,'SUFFIX','STRIP_SUFFIX',2,100
FROM unnest(ARRAY[
  '订单数量合计','商品数量合计','金额合计','实体数量','记录数量',
  '总数量','总金额','数量合计','记录数','总量','总数','数量','金额',
  '分钟数','时长','收入','比例','占比','率','数'
]) AS pattern;

INSERT INTO platform.semantic_parsing_rules(
  rule_type,pattern,match_mode,action,output_name,output_code,
  minimum_length,maximum_length,priority
) VALUES
  ('ADMIN_REGION_SUFFIX','特别行政区','SUFFIX','MAP_ADMIN_REGION','城市','city',2,12,160),
  ('ADMIN_REGION_SUFFIX','自治区','SUFFIX','MAP_ADMIN_REGION','省份','province',2,12,150),
  ('ADMIN_REGION_SUFFIX','省','SUFFIX','MAP_ADMIN_REGION','省份','province',2,12,120),
  ('ADMIN_REGION_SUFFIX','市','SUFFIX','MAP_ADMIN_REGION','城市','city',2,12,120),
  ('ADMIN_REGION_SUFFIX','区','SUFFIX','MAP_ADMIN_REGION','行政区','district',2,12,110),
  ('ADMIN_REGION_SUFFIX','县','SUFFIX','MAP_ADMIN_REGION','行政区','district',2,12,110);

INSERT INTO platform.semantic_parsing_rules(
  rule_type,pattern,match_mode,action,priority
)
SELECT 'QUERY_RESIDUAL_TERM',pattern,'EXACT','ALLOW_DETERMINISTIC',100
FROM unnest(ARRAY[
  '总量','总数','数量','金额','总额','合计','平均','均值','比例','占比',
  '分别','是什么','怎么样','什么','多少','几笔','几条','帮我','请问',
  '查询','统计','查看','告诉我','一下','经营情况','经营','情况','怎么'
]) AS pattern;

INSERT INTO platform.semantic_parsing_rules(
  rule_type,pattern,match_mode,action,priority
)
SELECT 'BROAD_METRIC_PHRASE',pattern,'CONTAINS','REQUIRE_METRIC_CONFIRMATION',100
FROM unnest(ARRAY[
  '经营情况','业务情况','整体情况','总体情况','经营怎么样','业务怎么样',
  '表现怎么样','数据怎么样','经营如何','业务如何','整体如何'
]) AS pattern;

COMMENT ON TABLE platform.semantic_parsing_rules IS
  '可热更新的精确语义解析规则；租户规则覆盖同类型同表达的平台默认规则，不参与向量化';
