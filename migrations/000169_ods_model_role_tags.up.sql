-- ODS 分类结果使用互斥 TABLE_FUNCTION 标签表达，避免“事实+维度”只能靠
-- 两个独立建议推断。PLATFORM 在当前资产范围枚举中表示租户内跨领域可见；
-- 标签绑定仍固定到精确 ODS 版本。
WITH taxonomy(code,name,description) AS (
  VALUES
    ('system.function.ods_fact','作用:ODS事实表',
      '当前精确 ODS 版本的主要行粒度是原子事实、事件或周期快照'),
    ('system.function.ods_dimension','作用:ODS维度表',
      '当前精确 ODS 版本可形成一个或多个稳定实体维度'),
    ('system.function.ods_fact_dimension','作用:ODS事实兼维度表',
      '当前精确 ODS 版本既保留事实粒度，也可抽取稳定实体维度'),
    ('system.function.ods_other','作用:ODS其他表',
      '当前精确 ODS 版本未识别为事实表或维度表')
), tenant_actor AS (
  SELECT tenant.id AS tenant_id,actor.id AS actor_id,domain.id AS domain_id
  FROM platform.tenants AS tenant
  JOIN platform.business_domains AS domain
    ON domain.tenant_id=tenant.id
   AND domain.is_default
   AND domain.status='ACTIVE'
  JOIN LATERAL (
    SELECT user_account.id
    FROM platform.users AS user_account
    WHERE user_account.tenant_id=tenant.id
      AND user_account.status='ACTIVE'
      AND user_account.deleted_at IS NULL
    ORDER BY user_account.created_at,user_account.id
    LIMIT 1
  ) AS actor ON true
  WHERE tenant.status='ACTIVE' AND tenant.deleted_at IS NULL
)
INSERT INTO platform.semantic_tags(
  tenant_id,domain_id,sharing_scope,
  code,name,description,category,governance,status,
  created_by,updated_by
)
SELECT
  tenant_actor.tenant_id,tenant_actor.domain_id,'PLATFORM',
  taxonomy.code,taxonomy.name,taxonomy.description,
  'TABLE_FUNCTION','CONTROLLED','ACTIVE',
  tenant_actor.actor_id,tenant_actor.actor_id
FROM tenant_actor
CROSS JOIN taxonomy
ON CONFLICT(tenant_id,code) DO NOTHING;

COMMENT ON FUNCTION platform.trigger_manual_dim_modeling(uuid,uuid[]) IS
  '逐 ODS 扫描并行识别 DIM；分类完成后为精确 ODS 版本建议事实、维度、事实兼维度或其他标签';
