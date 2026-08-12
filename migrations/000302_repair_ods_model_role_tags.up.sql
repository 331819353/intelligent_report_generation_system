BEGIN;

-- 000169 was applied before local/demo tenants were seeded in the normal
-- bootstrap order, so its data INSERT legitimately saw zero tenants. Repair
-- every active tenant now; the seed command also owns this invariant for
-- tenants created after this migration.
--
-- 000194 later prohibited every PLATFORM tag, unintentionally making the four
-- non-business, controlled system taxonomy codes unusable outside their owner
-- domain. Keep the isolation fence for all user/business tags and admit only
-- immutable-looking system.* CONTROLLED taxonomy across a tenant's domains.
ALTER TABLE platform.semantic_tags
  DROP CONSTRAINT semantic_tags_no_cross_domain_sharing;
ALTER TABLE platform.semantic_tags
  ADD CONSTRAINT semantic_tags_no_cross_domain_sharing CHECK(
    sharing_scope<>'PLATFORM'
    OR (governance='CONTROLLED' AND code::text LIKE 'system.%')
  );

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
   AND domain.deleted_at IS NULL
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
  tenant_id,domain_id,sharing_scope,code,name,description,
  category,governance,status,created_by,updated_by
)
SELECT tenant_actor.tenant_id,tenant_actor.domain_id,'PLATFORM',
  taxonomy.code,taxonomy.name,taxonomy.description,
  'TABLE_FUNCTION','CONTROLLED','ACTIVE',
  tenant_actor.actor_id,tenant_actor.actor_id
FROM tenant_actor
CROSS JOIN taxonomy
ON CONFLICT(tenant_id,code) DO UPDATE SET
  name=EXCLUDED.name,
  description=EXCLUDED.description,
  category='TABLE_FUNCTION',
  governance='CONTROLLED',
  sharing_scope='PLATFORM',
  status='ACTIVE',
  updated_by=EXCLUDED.updated_by,
  updated_at=now();

COMMIT;
