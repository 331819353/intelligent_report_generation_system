-- 人工建模每次重新提交都保留独立的“本次请求时间”。durable outbox 的稳定
-- job id 继续承载检查点和幂等性，任务中心则按 requested_at 展示本次真实运行。
ALTER TABLE platform.dwd_modeling_jobs
  ADD COLUMN requested_at timestamptz;
UPDATE platform.dwd_modeling_jobs SET requested_at=created_at;
ALTER TABLE platform.dwd_modeling_jobs
  ALTER COLUMN requested_at SET DEFAULT now(),
  ALTER COLUMN requested_at SET NOT NULL;

ALTER TABLE platform.dws_modeling_jobs
  ADD COLUMN requested_at timestamptz;
UPDATE platform.dws_modeling_jobs SET requested_at=created_at;
ALTER TABLE platform.dws_modeling_jobs
  ALTER COLUMN requested_at SET DEFAULT now(),
  ALTER COLUMN requested_at SET NOT NULL;

-- 草稿版本可在同一个 version id 上继续编辑；schema hash 是标签任务不可变
-- 身份的一部分，不能复用或回退已终结任务。
ALTER TABLE platform.dataset_tag_suggestion_jobs
  DROP CONSTRAINT dataset_tag_suggestion_jobs_idempotency_key;
ALTER TABLE platform.dataset_tag_suggestion_jobs
  ADD CONSTRAINT dataset_tag_suggestion_jobs_idempotency_key
  UNIQUE(tenant_id,dataset_version_id,prompt_version,schema_hash);

-- 标签建议只能从 ACTIVE CONTROLLED taxonomy 中选择。为每个已有租户补齐
-- 一套最小、可维护的中文治理词表，避免空词表让所有 LLM 标签任务直接跳过。
WITH taxonomy(category,code,name,description) AS (
  VALUES
    ('BUSINESS_DOMAIN','system.domain.operations','领域:运营','运营过程与运营效率相关数据'),
    ('BUSINESS_DOMAIN','system.domain.transaction','领域:交易','订单、成交与支付交易相关数据'),
    ('BUSINESS_DOMAIN','system.domain.customer','领域:客户','客户、用户及会员相关数据'),
    ('BUSINESS_DOMAIN','system.domain.product','领域:商品','商品、SKU 与品类相关数据'),
    ('BUSINESS_DOMAIN','system.domain.fulfillment','领域:履约','配送、履约与服务过程相关数据'),
    ('BUSINESS_DOMAIN','system.domain.merchant','领域:商户','商户与门店经营相关数据'),
    ('BUSINESS_ENTITY','system.entity.order','主题:订单','订单业务实体'),
    ('BUSINESS_ENTITY','system.entity.order_item','主题:订单商品','订单商品行项目业务实体'),
    ('BUSINESS_ENTITY','system.entity.product','主题:商品','商品或 SKU 业务实体'),
    ('BUSINESS_ENTITY','system.entity.customer','主题:客户','客户或用户业务实体'),
    ('BUSINESS_ENTITY','system.entity.merchant','主题:商户','商户或门店业务实体'),
    ('BUSINESS_ENTITY','system.entity.courier','主题:骑手','骑手或配送人员业务实体'),
    ('BUSINESS_ENTITY','system.entity.delivery_zone','主题:配送区域','配送区域业务实体'),
    ('BUSINESS_ENTITY','system.entity.delivery_event','主题:配送事件','配送过程事件业务实体'),
    ('TABLE_FUNCTION','system.function.fact_detail','作用:事实明细','保持业务事件或交易明细粒度'),
    ('TABLE_FUNCTION','system.function.entity_dimension','作用:实体维度','提供稳定实体说明属性'),
    ('TABLE_FUNCTION','system.function.subject_summary','作用:主题汇总','面向分析主题形成聚合结果'),
    ('TABLE_FUNCTION','system.function.application_delivery','作用:应用交付','面向报表或应用场景交付'),
    ('USAGE_SCOPE','system.usage.operations','范围:运营分析','用于运营过程与效率分析'),
    ('USAGE_SCOPE','system.usage.business','范围:经营分析','用于经营结果与趋势分析'),
    ('USAGE_SCOPE','system.usage.product','范围:商品分析','用于商品、SKU 与品类分析'),
    ('USAGE_SCOPE','system.usage.fulfillment','范围:履约分析','用于配送与履约分析'),
    ('USAGE_SCOPE','system.usage.customer','范围:客户分析','用于客户与用户分析'),
    ('DATA_GRAIN','system.grain.order','粒度:订单','每行代表一个订单'),
    ('DATA_GRAIN','system.grain.order_item','粒度:订单商品','每行代表一个订单中的一个商品项'),
    ('DATA_GRAIN','system.grain.product','粒度:商品','每行代表一个商品或 SKU'),
    ('DATA_GRAIN','system.grain.customer','粒度:客户','每行代表一个客户或用户'),
    ('DATA_GRAIN','system.grain.merchant','粒度:商户','每行代表一个商户或门店'),
    ('DATA_GRAIN','system.grain.courier','粒度:骑手','每行代表一个骑手或配送人员'),
    ('DATA_GRAIN','system.grain.delivery_zone','粒度:配送区域','每行代表一个配送区域'),
    ('DATA_GRAIN','system.grain.event','粒度:事件','每行代表一次业务事件'),
    ('DATA_GRAIN','system.grain.day','粒度:自然日','每行代表一个自然日粒度结果'),
    ('JOIN_ROLE','system.join.fact','关联:事实中心','作为明细或聚合分析的事实中心'),
    ('JOIN_ROLE','system.join.dimension','关联:维度扩充','作为事实模型的维度说明来源'),
    ('JOIN_ROLE','system.join.master','关联:主数据','作为稳定核心实体主数据来源')
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

-- 非 ODS 标签在建模草稿生成后立即自动建议，发布时仍会针对新的不可变版本
-- 再生成一次。标签只写 SUGGESTED 绑定，不绕过人工治理审批。
CREATE OR REPLACE FUNCTION platform.enqueue_dataset_tag_suggestion()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,platform
AS $$
DECLARE
  source_snapshot jsonb;
  request_actor uuid;
BEGIN
  IF NEW.layer IN ('DIM','DWD','DWS','ADS')
     AND NEW.status IN ('DRAFT','PUBLISHED')
     AND (
       TG_OP='INSERT'
       OR OLD.status IS DISTINCT FROM NEW.status
       OR OLD.schema_hash IS DISTINCT FROM NEW.schema_hash
     ) THEN
    SELECT COALESCE(
      jsonb_agg(
        jsonb_build_object(
          'dataSourceId',source_fact.data_source_id,
          'dataSourceVersionId',source_fact.data_source_version_id
        )
        ORDER BY source_fact.data_source_id
      ),
      '[]'::jsonb
    )
    INTO source_snapshot
    FROM (
      SELECT DISTINCT
        source.id::text AS data_source_id,
        COALESCE(source.current_published_version_id::text,'') AS data_source_version_id
      FROM platform.dataset_dependencies AS dependency
      JOIN platform.metadata_tables AS source_table
        ON dependency.source_type='TABLE'
       AND source_table.id::text=dependency.source_id
       AND source_table.tenant_id=dependency.tenant_id
      JOIN platform.data_sources AS source
        ON source.id=source_table.data_source_id
       AND source.tenant_id=source_table.tenant_id
      WHERE dependency.tenant_id=NEW.tenant_id
        AND dependency.dataset_version_id=NEW.id
    ) AS source_fact;

    request_actor := COALESCE(NEW.published_by,NEW.created_by);
    INSERT INTO platform.dataset_tag_suggestion_jobs(
      tenant_id,dataset_id,dataset_version_id,schema_hash,
      source_version_snapshot,source_version_snapshot_hash,layer,
      prompt_version,requested_by
    ) VALUES(
      NEW.tenant_id,NEW.dataset_id,NEW.id,NEW.schema_hash,
      source_snapshot,encode(public.digest(source_snapshot::text,'sha256'),'hex'),NEW.layer,
      'dataset-tag-suggestion-v4',request_actor
    )
    ON CONFLICT(
      tenant_id,dataset_version_id,prompt_version,schema_hash
    ) DO NOTHING;
  END IF;
  RETURN NEW;
END
$$;

REVOKE ALL ON FUNCTION platform.enqueue_dataset_tag_suggestion() FROM PUBLIC;

DROP TRIGGER IF EXISTS dataset_versions_enqueue_tag_suggestion
  ON platform.dataset_versions;
CREATE TRIGGER dataset_versions_enqueue_tag_suggestion
AFTER INSERT OR UPDATE OF status,schema_hash ON platform.dataset_versions
FOR EACH ROW EXECUTE FUNCTION platform.enqueue_dataset_tag_suggestion();

-- 当前页面上的 DIM/DWD/DWS/ADS 草稿无需等待再次保存或发布，迁移后即可进入
-- 自动标签队列。
INSERT INTO platform.dataset_tag_suggestion_jobs(
  tenant_id,dataset_id,dataset_version_id,schema_hash,
  source_version_snapshot,source_version_snapshot_hash,layer,
  prompt_version,requested_by
)
SELECT
  version.tenant_id,version.dataset_id,version.id,version.schema_hash,
  source_facts.snapshot,
  encode(public.digest(source_facts.snapshot::text,'sha256'),'hex'),
  version.layer,'dataset-tag-suggestion-v4',
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
  SELECT COALESCE(
    jsonb_agg(
      jsonb_build_object(
        'dataSourceId',source_fact.data_source_id,
        'dataSourceVersionId',source_fact.data_source_version_id
      )
      ORDER BY source_fact.data_source_id
    ),
    '[]'::jsonb
  ) AS snapshot
  FROM (
    SELECT DISTINCT
      source.id::text AS data_source_id,
      COALESCE(source.current_published_version_id::text,'') AS data_source_version_id
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

COMMENT ON TABLE platform.dataset_tag_suggestion_jobs IS
  '非 ODS 当前草稿及发布版本的自动 LLM 标签建议 outbox；输出只进入 SUGGESTED 治理态';
