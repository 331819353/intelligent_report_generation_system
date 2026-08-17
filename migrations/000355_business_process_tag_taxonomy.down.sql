BEGIN;

UPDATE platform.dataset_tag_suggestion_jobs
SET status='SKIPPED',error_code='PROMPT_SUPERSEDED',
    error_message='业务过程标签迁移已回滚',
    lease_owner='',lease_token=NULL,lease_expires_at=NULL,
    completed_at=now(),updated_at=now()
WHERE prompt_version='dataset-tag-suggestion-v9'
  AND status IN ('PENDING','RUNNING');

DO $migration$
DECLARE definition text;
BEGIN
  SELECT pg_get_functiondef(
    'platform.enqueue_dataset_tag_suggestion()'::regprocedure
  ) INTO definition;
  IF position('dataset-tag-suggestion-v9' IN definition)>0 THEN
    EXECUTE replace(
      definition,'dataset-tag-suggestion-v9','dataset-tag-suggestion-v8'
    );
  END IF;
END
$migration$;

-- Keep process taxonomy and the expanded category constraint when governed
-- bindings already reference them. Removing those rows would destroy audit
-- history; unbound process rows can be removed safely.
DELETE FROM platform.semantic_tags AS tag
WHERE tag.code::text=ANY(ARRAY[
  'system.process.sales','system.process.payment','system.process.fulfillment',
  'system.process.after_sales','system.process.customer_operations',
  'system.process.product_management','system.process.store_operations',
  'system.process.inventory_management','system.process.procurement',
  'system.process.marketing'
]::text[])
  AND NOT EXISTS(
    SELECT 1 FROM platform.asset_tag_bindings AS binding
    WHERE binding.tag_id=tag.id AND binding.tenant_id=tag.tenant_id
  );

COMMIT;
