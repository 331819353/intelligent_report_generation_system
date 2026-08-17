BEGIN;

UPDATE platform.metadata_tables
SET tags=ARRAY(
  SELECT tag FROM unnest(tags) AS tag
  WHERE tag<>ALL(ARRAY[
    '内容:订单头','内容:订单行','内容:支付信息','内容:营销归因',
    '内容:收货地域','内容:履约信息','内容:售后处理','内容:客户画像',
    '内容:商品属性','内容:门店属性','内容:金额指标','内容:状态信息'
  ]::text[])
),business_version=business_version+1
WHERE tags&&ARRAY[
  '内容:订单头','内容:订单行','内容:支付信息','内容:营销归因',
  '内容:收货地域','内容:履约信息','内容:售后处理','内容:客户画像',
  '内容:商品属性','内容:门店属性','内容:金额指标','内容:状态信息'
]::text[];

COMMIT;
