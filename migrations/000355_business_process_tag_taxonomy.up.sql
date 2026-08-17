-- Business process is an independent retrieval axis, not a business entity or
-- usage scope. Add it to the governed taxonomy and regenerate current exact
-- dataset-version suggestions under a new prompt identity.
BEGIN;

ALTER TABLE platform.semantic_tags
  DROP CONSTRAINT semantic_tags_category_check,
  ADD CONSTRAINT semantic_tags_category_check CHECK(category IN (
    'BUSINESS_ENTITY','BUSINESS_PROCESS','TABLE_FUNCTION','USAGE_SCOPE',
    'DATA_GRAIN','JOIN_ROLE','SENSITIVITY','FREEFORM'
  ));

ALTER TABLE platform.dataset_tag_suggestion_items
  DROP CONSTRAINT dataset_tag_suggestion_items_category_check,
  ADD CONSTRAINT dataset_tag_suggestion_items_category_check CHECK(category IN (
    'BUSINESS_ENTITY','BUSINESS_PROCESS','TABLE_FUNCTION',
    'USAGE_SCOPE','DATA_GRAIN','JOIN_ROLE'
  ));

WITH taxonomy(code,name,description) AS (
  VALUES
    ('system.process.sales','过程:销售','下单、成交与销售业务过程'),
    ('system.process.payment','过程:支付','支付与结算业务过程'),
    ('system.process.fulfillment','过程:履约','发货、配送与签收业务过程'),
    ('system.process.after_sales','过程:售后','售后、退货与退款业务过程'),
    ('system.process.customer_operations','过程:客户经营','获客、会员与客户运营过程'),
    ('system.process.product_management','过程:商品管理','商品、SKU 与品类管理过程'),
    ('system.process.store_operations','过程:门店经营','门店与商户经营过程'),
    ('system.process.inventory_management','过程:库存管理','入库、出库与库存管理过程'),
    ('system.process.procurement','过程:采购','采购与供应商协同过程'),
    ('system.process.marketing','过程:营销','营销活动、渠道与投放过程')
), tenant_actor AS (
  SELECT tenant.id AS tenant_id,actor.id AS actor_id,domain.id AS domain_id
  FROM platform.tenants AS tenant
  JOIN platform.business_domains AS domain
    ON domain.tenant_id=tenant.id
   AND domain.is_default
   AND domain.status='ACTIVE'
   AND domain.deleted_at IS NULL
  JOIN LATERAL (
    SELECT account.id
    FROM platform.users AS account
    WHERE account.tenant_id=tenant.id
      AND account.status='ACTIVE'
      AND account.deleted_at IS NULL
    ORDER BY account.created_at,account.id
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
  'BUSINESS_PROCESS','CONTROLLED','ACTIVE',
  tenant_actor.actor_id,tenant_actor.actor_id
FROM tenant_actor
CROSS JOIN taxonomy
ON CONFLICT(tenant_id,code) DO UPDATE SET
  name=EXCLUDED.name,
  description=EXCLUDED.description,
  category='BUSINESS_PROCESS',
  governance='CONTROLLED',
  sharing_scope='PLATFORM',
  status='ACTIVE',
  updated_by=EXCLUDED.updated_by,
  updated_at=now();

UPDATE platform.dataset_tag_suggestion_jobs
SET status='SKIPPED',error_code='PROMPT_SUPERSEDED',
    error_message='标签建议已增加独立业务过程维度',
    lease_owner='',lease_token=NULL,lease_expires_at=NULL,
    completed_at=now(),updated_at=now()
WHERE prompt_version='dataset-tag-suggestion-v8'
  AND status IN ('PENDING','RUNNING');

DO $migration$
DECLARE definition text;
BEGIN
  SELECT pg_get_functiondef(
    'platform.enqueue_dataset_tag_suggestion()'::regprocedure
  ) INTO definition;
  IF position('dataset-tag-suggestion-v8' IN definition)=0 THEN
    RAISE EXCEPTION 'dataset tag suggestion enqueue prompt is not v8';
  END IF;
  EXECUTE replace(
    definition,'dataset-tag-suggestion-v8','dataset-tag-suggestion-v9'
  );
END
$migration$;

INSERT INTO platform.dataset_tag_suggestion_jobs(
  tenant_id,dataset_id,dataset_version_id,schema_hash,
  source_version_snapshot,source_version_snapshot_hash,layer,
  prompt_version,requested_by
)
SELECT version.tenant_id,version.dataset_id,version.id,version.schema_hash,
  source_facts.snapshot,
  encode(public.digest(source_facts.snapshot::text,'sha256'),'hex'),
  version.layer,'dataset-tag-suggestion-v9',
  COALESCE(version.published_by,version.created_by)
FROM platform.dataset_versions AS version
JOIN platform.datasets AS dataset
  ON dataset.id=version.dataset_id
 AND dataset.tenant_id=version.tenant_id
 AND (
   dataset.current_draft_version_id=version.id
   OR dataset.current_published_version_id=version.id
 )
CROSS JOIN LATERAL (
  SELECT COALESCE(jsonb_agg(
    jsonb_build_object(
      'dataSourceId',source_fact.data_source_id,
      'dataSourceVersionId',source_fact.data_source_version_id
    ) ORDER BY source_fact.data_source_id
  ),'[]'::jsonb) AS snapshot
  FROM (
    SELECT DISTINCT source.id::text AS data_source_id,
      COALESCE(source.current_published_version_id::text,'')
        AS data_source_version_id
    FROM platform.dataset_dependencies AS dependency
    JOIN platform.metadata_tables AS source_table
      ON dependency.source_type='TABLE'
     AND source_table.id::text=dependency.source_id
     AND source_table.tenant_id=dependency.tenant_id
    JOIN platform.data_sources AS source
      ON source.id=source_table.data_source_id
     AND source.tenant_id=source_table.tenant_id
    WHERE dependency.tenant_id=version.tenant_id
      AND dependency.dataset_version_id=version.id
  ) AS source_fact
) AS source_facts
WHERE version.layer IN ('DIM','DWD','DWS','ADS')
  AND version.status IN ('DRAFT','PUBLISHED')
  AND dataset.deleted_at IS NULL
ON CONFLICT(
  tenant_id,dataset_version_id,prompt_version,schema_hash
) DO NOTHING;

COMMIT;
