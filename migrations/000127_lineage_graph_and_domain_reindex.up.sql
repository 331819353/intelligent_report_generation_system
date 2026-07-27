-- 领域函数依赖事务内的租户上下文。早期全库回填没有逐租户设置上下文，
-- 会把历史 DIM 指标文档误标为 general。逐租户修正文档并重建向量；同时
-- 请求新一代语义图，使新 worker 把当前服务版本的不可变上游版本闭包纳入图。
DO $$
DECLARE
  selected_tenant record;
BEGIN
  FOR selected_tenant IN
    SELECT tenant.id
    FROM platform.tenants AS tenant
    WHERE tenant.status='ACTIVE' AND tenant.deleted_at IS NULL
    ORDER BY tenant.id
  LOOP
    PERFORM set_config('app.tenant_id',selected_tenant.id::text,true);

    WITH effective AS (
      SELECT document.id,
        platform.dataset_version_effective_domain(
          document.dataset_version_id
        ) AS domain_name
      FROM platform.metric_semantic_documents AS document
      WHERE document.tenant_id=selected_tenant.id
    ), enriched AS (
      SELECT effective.id,effective.domain_name,
        concat(
          '业务领域：',effective.domain_name,E'\n',
          regexp_replace(document.document,'^业务领域：[^\n]*\n','','g')
        ) AS enriched_document
      FROM effective
      JOIN platform.metric_semantic_documents AS document
        ON document.tenant_id=selected_tenant.id
       AND document.id=effective.id
    )
    UPDATE platform.metric_semantic_documents AS document
    SET document=enriched.enriched_document,
        tags=ARRAY(
          SELECT DISTINCT value
          FROM unnest(array_append(
            ARRAY(
              SELECT existing
              FROM unnest(document.tags) AS existing
              WHERE existing !~ '^领域[：:]'
            ),
            '领域:'||enriched.domain_name
          )) AS value
          WHERE btrim(value)<>''
          ORDER BY value
        ),
        semantic_input_hash=encode(public.digest(
          convert_to(enriched.enriched_document,'UTF8'),'sha256'
        ),'hex'),
        embedding=NULL,embedding_model='',embedding_input_hash='',
        embedding_status='PENDING',embedding_attempt=0,
        embedding_error_code='',next_attempt_at=now(),
        lease_owner='',lease_expires_at=NULL,embedded_at=NULL,updated_at=now()
    FROM enriched
    WHERE document.tenant_id=selected_tenant.id
      AND document.id=enriched.id
      AND (
        document.document IS DISTINCT FROM enriched.enriched_document
        OR NOT document.tags @> ARRAY['领域:'||enriched.domain_name]
      );

    UPDATE platform.semantic_graph_projection_state AS state
    SET requested_event_version=
          GREATEST(
            state.requested_event_version,state.applied_event_version
          )+1,
        status=CASE
          WHEN state.status='RUNNING' THEN 'RUNNING' ELSE 'PENDING'
        END,
        next_attempt_at=now(),error_code='',updated_at=now()
    FROM platform.semantic_qa_settings AS settings
    WHERE state.tenant_id=selected_tenant.id
      AND settings.tenant_id=state.tenant_id
      AND settings.enabled AND settings.graph_projection_enabled;
  END LOOP;
END
$$;
