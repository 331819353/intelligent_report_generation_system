-- Order header/line is a structural distinction. Only field identities may
-- prove it; a measure description mentioning "商品原价" must not turn an order
-- header table into an order-line table.
BEGIN;

WITH evidence AS (
  SELECT table_asset.id,
    lower(concat_ws(' ',table_asset.table_name,table_asset.business_name)) AS identity_text,
    COALESCE(bool_or(lower(concat_ws(' ',column_asset.column_name,column_asset.business_name))
      ~ '(^|[^a-z])(product|sku|item|line|quantity)([^a-z]|$)|商品|产品|行项目|数量'),false) AS has_order_line
  FROM platform.metadata_tables AS table_asset
  LEFT JOIN platform.metadata_columns AS column_asset
    ON column_asset.table_id=table_asset.id
   AND column_asset.tenant_id=table_asset.tenant_id
   AND column_asset.asset_status='ACTIVE'
  WHERE table_asset.asset_status='ACTIVE'
  GROUP BY table_asset.id,table_asset.table_name,table_asset.business_name
), corrected AS (
  SELECT evidence.id,
    CASE WHEN evidence.has_order_line THEN '内容:订单行' ELSE '内容:订单头' END AS expected_tag
  FROM evidence
  WHERE evidence.identity_text ~ '(order|订单|销售)'
)
UPDATE platform.metadata_tables AS target
SET tags=ARRAY(
  SELECT deduplicated.tag
  FROM (
    SELECT candidate.tag,min(candidate.position) AS first_position
    FROM (
      SELECT existing.tag,existing.ordinality::bigint AS position
      FROM unnest(target.tags) WITH ORDINALITY AS existing(tag,ordinality)
      WHERE existing.tag NOT IN ('内容:订单头','内容:订单行')
      UNION ALL
      SELECT corrected.expected_tag,1000::bigint
    ) AS candidate
    GROUP BY candidate.tag
  ) AS deduplicated
  ORDER BY deduplicated.first_position
),business_version=business_version+1
FROM corrected
WHERE target.id=corrected.id
  AND (
    array_position(target.tags,corrected.expected_tag) IS NULL
    OR target.tags&&ARRAY[
      CASE WHEN corrected.expected_tag='内容:订单头' THEN '内容:订单行' ELSE '内容:订单头' END
    ]::text[]
  );

COMMIT;
