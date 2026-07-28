-- DWD/DWS/ADS 保存前语义命名会改写逻辑表名和字段别名，并选择受控标签。它仍然
-- 只写草稿和 SUGGESTED 标签，不选择物理对象，也不绕过发布审批。
ALTER TABLE platform.ai_requests
  DROP CONSTRAINT ai_requests_purpose_check;

ALTER TABLE platform.ai_requests
  ADD CONSTRAINT ai_requests_purpose_check CHECK(purpose IN (
    'METADATA_COMPLETION',
    'REPORT_GENERATION',
    'BLOCK_EDIT',
    'CONCLUSION_GENERATION',
    'DATASET_DAG_GENERATION',
    'METRIC_AUTHORING',
    'DATASET_TAG_SUGGESTION',
    'SEMANTIC_QUERY_PLANNING',
    'DATASET_SEMANTIC_NAMING'
  ));

-- 现有最小受控词表没有人力资源/员工语义，LLM 只能误选客户或运营标签。
-- 为每个活动租户补齐与员工主题直接对应的领域、实体、用途和粒度词条。
WITH taxonomy(category,code,name,description) AS (
  VALUES
    ('BUSINESS_DOMAIN','system.domain.human_resources','领域:人力资源','员工、组织、岗位与人才发展相关数据'),
    ('BUSINESS_ENTITY','system.entity.employee','主题:员工','员工或组织人员业务实体'),
    ('USAGE_SCOPE','system.usage.human_resources','范围:人力资源分析','用于员工结构、组织与人才分析'),
    ('DATA_GRAIN','system.grain.employee','粒度:员工','每行代表一个员工或人员实体')
), tenant_actor AS (
  SELECT tenant.id AS tenant_id,actor.id AS actor_id
  FROM platform.tenants AS tenant
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
  tenant_id,code,name,description,category,governance,status,
  created_by,updated_by
)
SELECT
  tenant_actor.tenant_id,taxonomy.code,taxonomy.name,taxonomy.description,
  taxonomy.category,'CONTROLLED','ACTIVE',
  tenant_actor.actor_id,tenant_actor.actor_id
FROM tenant_actor
CROSS JOIN taxonomy
ON CONFLICT(tenant_id,code) DO NOTHING;
