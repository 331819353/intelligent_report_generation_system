BEGIN;

UPDATE platform.dataset_tag_suggestion_jobs
SET status='SKIPPED',error_code='PROMPT_SUPERSEDED',
    error_message='标签增强迁移已回滚',
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
  IF position('dataset-tag-suggestion-v8' IN definition)>0 THEN
    EXECUTE replace(
      definition,'dataset-tag-suggestion-v8','dataset-tag-suggestion-v7'
    );
  END IF;
END
$migration$;

UPDATE platform.metadata_tables
SET tags=ARRAY(
  SELECT tag FROM unnest(tags) AS tag
  WHERE tag<>ALL(ARRAY[
    '主题:订单','主题:订单商品','主题:售后服务','主题:支付','主题:履约',
    '主题:客户','主题:商品','主题:门店','主题:库存','主题:供应商','主题:员工',
    '过程:销售','过程:支付','过程:履约','过程:售后','过程:客户经营',
    '过程:商品管理','过程:门店经营','过程:库存管理','过程:采购','过程:营销',
    '功能:实体主数据','粒度:订单商品','粒度:售后工单','粒度:门店',
    '粒度:供应商','时间:事件时间','时间:快照日期','时间:生效时间'
  ]::text[])
),business_version=business_version+1
WHERE tags&&ARRAY[
  '主题:订单','主题:订单商品','主题:售后服务','主题:支付','主题:履约',
  '主题:客户','主题:商品','主题:门店','主题:库存','主题:供应商','主题:员工',
  '过程:销售','过程:支付','过程:履约','过程:售后','过程:客户经营',
  '过程:商品管理','过程:门店经营','过程:库存管理','过程:采购','过程:营销',
  '功能:实体主数据','粒度:订单商品','粒度:售后工单','粒度:门店',
  '粒度:供应商','时间:事件时间','时间:快照日期','时间:生效时间'
]::text[];

-- Keep taxonomy rows that are already referenced by governed bindings. Only
-- unbound repair rows can be safely removed during a rollback.
DELETE FROM platform.semantic_tags AS tag
WHERE tag.code::text=ANY(ARRAY[
  'system.function.fact_detail','system.function.entity_dimension',
  'system.function.subject_summary','system.function.application_delivery',
  'system.entity.order','system.entity.order_item','system.entity.after_sales',
  'system.entity.payment','system.entity.fulfillment','system.entity.customer',
  'system.entity.product','system.entity.store','system.entity.inventory',
  'system.entity.warehouse','system.entity.supplier','system.entity.employee',
  'system.entity.organization','system.entity.channel','system.entity.campaign',
  'system.usage.operations','system.usage.business','system.usage.finance',
  'system.usage.risk','system.usage.product','system.usage.fulfillment',
  'system.usage.customer','system.usage.supply_chain','system.usage.marketing',
  'system.usage.human_resources','system.grain.order','system.grain.order_item',
  'system.grain.after_sales_ticket','system.grain.payment','system.grain.customer',
  'system.grain.product','system.grain.store','system.grain.inventory_record',
  'system.grain.warehouse','system.grain.supplier','system.grain.employee',
  'system.grain.organization','system.grain.channel','system.grain.event',
  'system.grain.day','system.grain.month','system.join.fact',
  'system.join.dimension','system.join.master'
]::text[])
  AND NOT EXISTS(
    SELECT 1 FROM platform.asset_tag_bindings AS binding
    WHERE binding.tag_id=tag.id AND binding.tenant_id=tag.tenant_id
  );

COMMIT;
