-- Candidate retrieval needs more than a fact/dimension bit. Repair the system
-- taxonomy for tenants created after the original seed migration, re-run exact
-- dataset-version suggestions under a new prompt identity, and enrich physical
-- table tags from deterministic metadata evidence.
BEGIN;

WITH taxonomy(category,code,name,description) AS (
  VALUES
    ('TABLE_FUNCTION','system.function.ods_fact','作用:ODS事实表','当前精确 ODS 版本的主要行粒度是原子事实、事件或周期快照'),
    ('TABLE_FUNCTION','system.function.ods_dimension','作用:ODS维度表','当前精确 ODS 版本可形成一个或多个稳定实体维度'),
    ('TABLE_FUNCTION','system.function.ods_fact_dimension','作用:ODS事实兼维度表','当前精确 ODS 版本既保留事实粒度，也可抽取稳定实体维度'),
    ('TABLE_FUNCTION','system.function.ods_other','作用:ODS其他表','当前精确 ODS 版本未识别为事实表或维度表'),
    ('TABLE_FUNCTION','system.function.fact_detail','作用:事实明细','保持业务事件或交易明细粒度'),
    ('TABLE_FUNCTION','system.function.entity_dimension','作用:实体维度','提供稳定实体说明属性'),
    ('TABLE_FUNCTION','system.function.subject_summary','作用:主题汇总','面向分析主题形成聚合结果'),
    ('TABLE_FUNCTION','system.function.application_delivery','作用:应用交付','面向报表或应用场景交付'),
    ('BUSINESS_ENTITY','system.entity.order','主题:订单','订单业务实体'),
    ('BUSINESS_ENTITY','system.entity.order_item','主题:订单商品','订单商品行项目业务实体'),
    ('BUSINESS_ENTITY','system.entity.after_sales','主题:售后服务','售后工单、退货与退款业务实体'),
    ('BUSINESS_ENTITY','system.entity.payment','主题:支付','支付与结算业务实体'),
    ('BUSINESS_ENTITY','system.entity.fulfillment','主题:履约','发货、配送与签收业务实体'),
    ('BUSINESS_ENTITY','system.entity.customer','主题:客户','客户、用户或会员业务实体'),
    ('BUSINESS_ENTITY','system.entity.product','主题:商品','商品、产品或 SKU 业务实体'),
    ('BUSINESS_ENTITY','system.entity.store','主题:门店','门店或商户业务实体'),
    ('BUSINESS_ENTITY','system.entity.inventory','主题:库存','库存记录业务实体'),
    ('BUSINESS_ENTITY','system.entity.warehouse','主题:仓库','仓库业务实体'),
    ('BUSINESS_ENTITY','system.entity.supplier','主题:供应商','供应商业务实体'),
    ('BUSINESS_ENTITY','system.entity.employee','主题:员工','员工或人员业务实体'),
    ('BUSINESS_ENTITY','system.entity.organization','主题:组织','组织或部门业务实体'),
    ('BUSINESS_ENTITY','system.entity.channel','主题:渠道','销售、获客或投放渠道业务实体'),
    ('BUSINESS_ENTITY','system.entity.campaign','主题:营销活动','营销活动业务实体'),
    ('USAGE_SCOPE','system.usage.operations','范围:运营分析','用于运营过程与效率分析'),
    ('USAGE_SCOPE','system.usage.business','范围:经营分析','用于经营结果与趋势分析'),
    ('USAGE_SCOPE','system.usage.finance','范围:财务分析','用于金额、收入、成本与结算分析'),
    ('USAGE_SCOPE','system.usage.risk','范围:风险分析','用于异常、风险与合规分析'),
    ('USAGE_SCOPE','system.usage.product','范围:商品分析','用于商品、SKU 与品类分析'),
    ('USAGE_SCOPE','system.usage.fulfillment','范围:履约分析','用于发货、配送与履约分析'),
    ('USAGE_SCOPE','system.usage.customer','范围:客户分析','用于客户、用户与会员分析'),
    ('USAGE_SCOPE','system.usage.supply_chain','范围:供应链分析','用于采购、库存与供应保障分析'),
    ('USAGE_SCOPE','system.usage.marketing','范围:营销分析','用于渠道、活动与获客分析'),
    ('USAGE_SCOPE','system.usage.human_resources','范围:人力资源分析','用于员工、组织与人才分析'),
    ('DATA_GRAIN','system.grain.order','粒度:订单','每行代表一个订单'),
    ('DATA_GRAIN','system.grain.order_item','粒度:订单商品','每行代表一个订单商品行项目'),
    ('DATA_GRAIN','system.grain.after_sales_ticket','粒度:售后工单','每行代表一个售后工单'),
    ('DATA_GRAIN','system.grain.payment','粒度:支付','每行代表一笔支付或结算记录'),
    ('DATA_GRAIN','system.grain.customer','粒度:客户','每行代表一个客户或用户'),
    ('DATA_GRAIN','system.grain.product','粒度:商品','每行代表一个商品或 SKU'),
    ('DATA_GRAIN','system.grain.store','粒度:门店','每行代表一个门店或商户'),
    ('DATA_GRAIN','system.grain.inventory_record','粒度:库存记录','每行代表一个库存位置与商品组合记录'),
    ('DATA_GRAIN','system.grain.warehouse','粒度:仓库','每行代表一个仓库'),
    ('DATA_GRAIN','system.grain.supplier','粒度:供应商','每行代表一个供应商'),
    ('DATA_GRAIN','system.grain.employee','粒度:员工','每行代表一个员工或人员'),
    ('DATA_GRAIN','system.grain.organization','粒度:组织','每行代表一个组织或部门'),
    ('DATA_GRAIN','system.grain.channel','粒度:渠道','每行代表一个业务渠道'),
    ('DATA_GRAIN','system.grain.event','粒度:事件','每行代表一次业务事件'),
    ('DATA_GRAIN','system.grain.day','粒度:自然日','每行代表一个自然日粒度结果'),
    ('DATA_GRAIN','system.grain.month','粒度:自然月','每行代表一个自然月粒度结果'),
    ('JOIN_ROLE','system.join.fact','关联:事实中心','作为明细或聚合分析的事实中心'),
    ('JOIN_ROLE','system.join.dimension','关联:维度扩充','作为事实模型的维度说明来源'),
    ('JOIN_ROLE','system.join.master','关联:主数据','作为稳定核心实体主数据来源')
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
  taxonomy.code,taxonomy.name,taxonomy.description,taxonomy.category,
  'CONTROLLED','ACTIVE',tenant_actor.actor_id,tenant_actor.actor_id
FROM tenant_actor
CROSS JOIN taxonomy
ON CONFLICT(tenant_id,code) DO UPDATE SET
  name=EXCLUDED.name,
  description=EXCLUDED.description,
  category=EXCLUDED.category,
  governance='CONTROLLED',
  sharing_scope='PLATFORM',
  status='ACTIVE',
  updated_by=EXCLUDED.updated_by,
  updated_at=now();

-- Physical tags remain a compact controlled search document. Rules use table
-- identity for subject/grain and declared constraints for relationship roles;
-- no join target is inferred from naming alone.
WITH evidence AS (
  SELECT t.id,
    -- Subject and grain must come from the table's identity, not from a broad
    -- description that may list every downstream use or related dimension.
    lower(concat_ws(' ',t.table_name,t.business_name,t.source_comment)) AS identity_text,
    (lower(t.table_name) LIKE 'fact\_%' ESCAPE '\'
      OR array_position(t.tags,'作用:事实表') IS NOT NULL
      OR t.business_name LIKE '%事实表%') AS is_fact,
    (lower(t.table_name) LIKE 'dim\_%' ESCAPE '\'
      OR array_position(t.tags,'作用:维度表') IS NOT NULL
      OR t.business_name LIKE '%维度表%') AS is_dimension,
    EXISTS(
      SELECT 1 FROM platform.metadata_columns AS c
      WHERE c.table_id=t.id AND c.asset_status='ACTIVE'
        AND c.canonical_type IN ('INTEGER','DECIMAL','FLOAT')
    ) AS has_numeric,
    EXISTS(
      SELECT 1 FROM platform.metadata_columns AS c
      WHERE c.table_id=t.id AND c.asset_status='ACTIVE'
        AND c.canonical_type IN ('DATE','TIME','DATETIME','TIMESTAMP')
    ) AS has_time,
    EXISTS(
      SELECT 1 FROM jsonb_array_elements(
        CASE WHEN jsonb_typeof(t.constraints_json)='array'
          THEN t.constraints_json ELSE '[]'::jsonb END
      ) AS constraint_item
      WHERE upper(COALESCE(constraint_item->>'type',''))='FOREIGN KEY'
        AND COALESCE(constraint_item->>'referencedTable','')<>''
    ) AS has_declared_foreign_key
  FROM platform.metadata_tables AS t
  WHERE t.asset_status='ACTIVE'
), inferred AS (
  SELECT evidence.id,array_remove(ARRAY[
    CASE WHEN evidence.is_fact THEN '作用:事实表' END,
    CASE WHEN evidence.is_fact THEN '功能:交易明细' END,
    CASE WHEN evidence.is_fact AND evidence.has_numeric THEN '作用:指标来源' END,
    CASE WHEN evidence.is_dimension THEN '作用:维度表' END,
    CASE WHEN evidence.is_dimension THEN '作用:主数据' END,
    CASE WHEN evidence.is_dimension THEN '功能:实体主数据' END,
    CASE WHEN evidence.identity_text ~ '(order[_ ]?item|订单(商品|明细)|行项目)' THEN '主题:订单商品' END,
    CASE WHEN evidence.identity_text ~ '(after[_ -]?sales|售后|退货|退款|service[_ ]?(order|ticket))' THEN '主题:售后服务' END,
    CASE WHEN evidence.identity_text ~ '(order|订单|销售)' AND evidence.identity_text !~ '(order[_ ]?item|订单(商品|明细)|行项目)' THEN '主题:订单' END,
    CASE WHEN evidence.identity_text ~ '(payment|pay_|支付|结算)' THEN '主题:支付' END,
    CASE WHEN evidence.identity_text ~ '(delivery|fulfill|shipment|履约|配送|发货|签收)' THEN '主题:履约' END,
    CASE WHEN evidence.identity_text ~ '(customer|member|客户|用户|会员)' THEN '主题:客户' END,
    CASE WHEN evidence.identity_text ~ '(product|sku|商品|产品)'
      AND evidence.identity_text !~ '(order[_ ]?item|订单(商品|明细)|行项目)' THEN '主题:商品' END,
    CASE WHEN evidence.identity_text ~ '(store|merchant|shop|门店|商户|店铺)' THEN '主题:门店' END,
    CASE WHEN evidence.identity_text ~ '(inventory|stock|库存)' THEN '主题:库存' END,
    CASE WHEN evidence.identity_text ~ '(supplier|vendor|供应商)' THEN '主题:供应商' END,
    CASE WHEN evidence.identity_text ~ '(employee|staff|员工|人员)' THEN '主题:员工' END,
    CASE WHEN evidence.identity_text ~ '(order|订单|销售)'
      AND evidence.identity_text !~ '(after[_ -]?sales|售后)' THEN '过程:销售' END,
    CASE WHEN evidence.identity_text ~ '(payment|pay_|支付|结算)' THEN '过程:支付' END,
    CASE WHEN evidence.identity_text ~ '(delivery|fulfill|shipment|履约|配送|发货|签收)' THEN '过程:履约' END,
    CASE WHEN evidence.identity_text ~ '(after[_ -]?sales|售后|退货|退款)' THEN '过程:售后' END,
    CASE WHEN evidence.is_dimension AND evidence.identity_text ~ '(customer|member|客户|用户|会员)' THEN '过程:客户经营' END,
    CASE WHEN evidence.is_dimension AND evidence.identity_text ~ '(product|sku|商品|产品)' THEN '过程:商品管理' END,
    CASE WHEN evidence.is_dimension AND evidence.identity_text ~ '(store|merchant|shop|门店|商户|店铺)' THEN '过程:门店经营' END,
    CASE WHEN evidence.identity_text ~ '(inventory|stock|warehouse|库存|仓库)' THEN '过程:库存管理' END,
    CASE WHEN evidence.identity_text ~ '(purchase|procure|采购)' THEN '过程:采购' END,
    CASE WHEN evidence.identity_text ~ '(campaign|marketing|营销|投放)' THEN '过程:营销' END,
    CASE WHEN evidence.identity_text ~ '(order[_ ]?item|订单(商品|明细)|行项目)' THEN '粒度:订单商品' END,
    CASE WHEN evidence.identity_text ~ '(after[_ -]?sales|售后|service[_ ]?(order|ticket))' THEN '粒度:售后工单' END,
    CASE WHEN evidence.identity_text ~ '(order|订单)' AND evidence.identity_text !~ '(order[_ ]?item|订单(商品|明细)|行项目)' THEN '粒度:订单' END,
    CASE WHEN evidence.identity_text ~ '(customer|member|客户|用户|会员)' AND evidence.is_dimension THEN '粒度:客户' END,
    CASE WHEN evidence.identity_text ~ '(product|sku|商品|产品)' AND evidence.is_dimension THEN '粒度:产品' END,
    CASE WHEN evidence.identity_text ~ '(store|merchant|shop|门店|商户|店铺)' AND evidence.is_dimension THEN '粒度:门店' END,
    CASE WHEN evidence.identity_text ~ '(supplier|vendor|供应商)' AND evidence.is_dimension THEN '粒度:供应商' END,
    CASE WHEN evidence.is_fact AND evidence.has_time AND evidence.identity_text !~ '(snapshot|快照)' THEN '时间:事件时间' END,
    CASE WHEN evidence.has_time AND evidence.identity_text ~ '(snapshot|快照)' THEN '时间:快照日期' END,
    CASE WHEN evidence.is_dimension AND evidence.has_time AND evidence.identity_text ~ '(effective|valid|生效|有效)' THEN '时间:生效时间' END,
    CASE WHEN evidence.has_declared_foreign_key THEN '关联:外键' END
  ]::text[],NULL) AS tags
  FROM evidence
)
UPDATE platform.metadata_tables AS target
SET tags=(
  SELECT COALESCE(array_agg(deduplicated.tag ORDER BY deduplicated.first_position),'{}'::text[])
  FROM (
    SELECT candidate.tag,min(candidate.position) AS first_position
    FROM (
      SELECT existing.tag,existing.ordinality::bigint AS position
      FROM unnest(target.tags) WITH ORDINALITY AS existing(tag,ordinality)
      UNION ALL
      SELECT proposed.tag,1000+proposed.ordinality::bigint AS position
      FROM unnest(inferred.tags) WITH ORDINALITY AS proposed(tag,ordinality)
    ) AS candidate
    WHERE btrim(candidate.tag)<>''
    GROUP BY candidate.tag
  ) AS deduplicated
),business_version=business_version+1
FROM inferred
WHERE target.id=inferred.id
  AND EXISTS(
    SELECT 1 FROM unnest(inferred.tags) AS proposed(tag)
    WHERE array_position(target.tags,proposed.tag) IS NULL
  );

UPDATE platform.dataset_tag_suggestion_jobs
SET status='SKIPPED',error_code='PROMPT_SUPERSEDED',
    error_message='受控标签词表与粒度选择规则已增强',
    lease_owner='',lease_token=NULL,lease_expires_at=NULL,
    completed_at=now(),updated_at=now()
WHERE prompt_version='dataset-tag-suggestion-v7'
  AND status IN ('PENDING','RUNNING');

DO $migration$
DECLARE definition text;
BEGIN
  SELECT pg_get_functiondef(
    'platform.enqueue_dataset_tag_suggestion()'::regprocedure
  ) INTO definition;
  IF position('dataset-tag-suggestion-v7' IN definition)=0 THEN
    RAISE EXCEPTION 'dataset tag suggestion enqueue prompt is not v7';
  END IF;
  EXECUTE replace(
    definition,'dataset-tag-suggestion-v7','dataset-tag-suggestion-v8'
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
  version.layer,'dataset-tag-suggestion-v8',
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
