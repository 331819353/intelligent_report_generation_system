-- Same-named tables are different assets unless provenance says otherwise.
-- Add field-backed content coverage labels and rebuild search documents with
-- source identity so retrieval and the confirmation UI can explain the difference.
BEGIN;

WITH column_evidence AS (
  SELECT table_asset.id,
    lower(concat_ws(' ',table_asset.table_name,table_asset.business_name)) AS identity_text,
    COALESCE(bool_or(lower(concat_ws(' ',column_asset.column_name,column_asset.business_name,
      column_asset.business_description,array_to_string(column_asset.tags,' ')))
      ~ '(product|sku|item|line|quantity|商品|产品|行项目|数量)'),false) AS has_order_line,
    COALESCE(bool_or(lower(concat_ws(' ',column_asset.column_name,column_asset.business_name,
      column_asset.business_description,array_to_string(column_asset.tags,' ')))
      ~ '(payment|paid|pay_|settlement|支付|付款|实付|结算)'),false) AS has_payment,
    COALESCE(bool_or(lower(concat_ws(' ',column_asset.column_name,column_asset.business_name,
      column_asset.business_description,array_to_string(column_asset.tags,' ')))
      ~ '(campaign|marketing|promotion|acquisition|营销|活动|推广|获客)'),false) AS has_marketing,
    COALESCE(bool_or(lower(concat_ws(' ',column_asset.column_name,column_asset.business_name,
      column_asset.business_description,array_to_string(column_asset.tags,' ')))
      ~ '(province|city|district|region|address|省|城市|市|区县|区域|地址)'),false) AS has_region,
    COALESCE(bool_or(lower(concat_ws(' ',column_asset.column_name,column_asset.business_name,
      column_asset.business_description,array_to_string(column_asset.tags,' ')))
      ~ '(delivery|shipment|warehouse|signed|发货|配送|履约|签收|仓库)'),false) AS has_fulfillment,
    COALESCE(bool_or(lower(concat_ws(' ',column_asset.column_name,column_asset.business_name,
      column_asset.business_description,array_to_string(column_asset.tags,' ')))
      ~ '(refund|return|after.?sales|service.?ticket|退款|退货|售后|工单)'),false) AS has_after_sales,
    COALESCE(bool_or(lower(concat_ws(' ',column_asset.column_name,column_asset.business_name,
      column_asset.business_description,array_to_string(column_asset.tags,' ')))
      ~ '(gender|birthday|birth_date|age|member|level|性别|生日|年龄|会员|等级)'),false) AS has_customer_profile,
    COALESCE(bool_or(lower(concat_ws(' ',column_asset.column_name,column_asset.business_name,
      column_asset.business_description,array_to_string(column_asset.tags,' ')))
      ~ '(category|brand|spec|color|model|品类|类目|品牌|规格|颜色|型号)'),false) AS has_product_attribute,
    COALESCE(bool_or(lower(concat_ws(' ',column_asset.column_name,column_asset.business_name,
      column_asset.business_description,array_to_string(column_asset.tags,' ')))
      ~ '(store_type|merchant_type|rating|follower|open_date|门店类型|商户类型|评分|粉丝|开店)'),false) AS has_store_attribute,
    COALESCE(bool_or(lower(concat_ws(' ',column_asset.column_name,column_asset.business_name,
      column_asset.business_description,array_to_string(column_asset.tags,' ')))
      ~ '(amount|price|revenue|cost|fee|金额|价格|收入|成本|费用)'),false) AS has_amount,
    COALESCE(bool_or(lower(concat_ws(' ',column_asset.column_name,column_asset.business_name,
      column_asset.business_description,array_to_string(column_asset.tags,' ')))
      ~ '(^|[^a-z])(status|state)([^a-z]|$)|状态'),false) AS has_status
  FROM platform.metadata_tables AS table_asset
  LEFT JOIN platform.metadata_columns AS column_asset
    ON column_asset.table_id=table_asset.id
   AND column_asset.tenant_id=table_asset.tenant_id
   AND column_asset.asset_status='ACTIVE'
  WHERE table_asset.asset_status='ACTIVE'
  GROUP BY table_asset.id,table_asset.table_name,table_asset.business_name
), inferred AS (
  SELECT column_evidence.id,array_remove(ARRAY[
    CASE WHEN column_evidence.identity_text ~ '(order|订单|销售)'
      AND NOT column_evidence.has_order_line THEN '内容:订单头' END,
    CASE WHEN column_evidence.identity_text ~ '(order|订单|销售)'
      AND column_evidence.has_order_line THEN '内容:订单行' END,
    CASE WHEN column_evidence.has_payment THEN '内容:支付信息' END,
    CASE WHEN column_evidence.has_marketing THEN '内容:营销归因' END,
    CASE WHEN column_evidence.identity_text ~ '(order|订单|销售)'
      AND column_evidence.has_region THEN '内容:收货地域' END,
    CASE WHEN column_evidence.identity_text ~ '(order|订单|销售|after.?sales|售后)'
      AND column_evidence.has_fulfillment THEN '内容:履约信息' END,
    CASE WHEN column_evidence.identity_text ~ '(after.?sales|refund|return|售后|退款|退货)'
      OR column_evidence.has_after_sales THEN '内容:售后处理' END,
    CASE WHEN column_evidence.identity_text ~ '(customer|member|客户|用户|会员)'
      AND column_evidence.has_customer_profile THEN '内容:客户画像' END,
    CASE WHEN column_evidence.identity_text ~ '(product|sku|商品|产品)'
      AND column_evidence.has_product_attribute THEN '内容:商品属性' END,
    CASE WHEN column_evidence.identity_text ~ '(store|merchant|shop|门店|商户|店铺)'
      AND column_evidence.has_store_attribute THEN '内容:门店属性' END,
    CASE WHEN column_evidence.has_amount THEN '内容:金额指标' END,
    CASE WHEN column_evidence.has_status THEN '内容:状态信息' END
  ]::text[],NULL) AS tags
  FROM column_evidence
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

-- Document v2 contains data-source identity. Rebuild every active asset, even
-- when no new content label was inferred for it.
INSERT INTO platform.asset_embedding_outbox(tenant_id,asset_type,asset_id,table_id)
SELECT tenant_id,'TABLE',id,id
FROM platform.metadata_tables
WHERE asset_status='ACTIVE'
ON CONFLICT(tenant_id,asset_type,asset_id) DO UPDATE SET
  table_id=EXCLUDED.table_id,status='PENDING',
  event_version=platform.asset_embedding_outbox.event_version+1,
  attempt=0,error_code='',next_attempt_at=now(),lease_owner='',lease_expires_at=NULL,
  completed_at=NULL,updated_at=now();

INSERT INTO platform.asset_embedding_outbox(tenant_id,asset_type,asset_id,table_id)
SELECT tenant_id,'COLUMN',id,table_id
FROM platform.metadata_columns
WHERE asset_status='ACTIVE'
ON CONFLICT(tenant_id,asset_type,asset_id) DO UPDATE SET
  table_id=EXCLUDED.table_id,status='PENDING',
  event_version=platform.asset_embedding_outbox.event_version+1,
  attempt=0,error_code='',next_attempt_at=now(),lease_owner='',lease_expires_at=NULL,
  completed_at=NULL,updated_at=now();

COMMIT;
