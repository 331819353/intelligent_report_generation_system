-- 新产物必须显式携带领域；校验历史上游时允许使用 000122 的只读 ODS
-- 继承结果，避免“旧快照缺字段”阻断按新合同生成的 DWS。
CREATE OR REPLACE FUNCTION platform.enforce_dataset_domain_lineage()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,platform
AS $$
DECLARE
  target_domain text;
  dependency_count integer;
  mismatched_count integer;
BEGIN
  IF NEW.status NOT IN ('PUBLISHING','PUBLISHED') THEN
    RETURN NEW;
  END IF;
  target_domain := btrim(COALESCE(NEW.dsl_json#>>'{dataset,domain}',''));
  IF target_domain='' THEN
    RAISE EXCEPTION 'published dataset domain is required'
      USING ERRCODE='23514',CONSTRAINT='dataset_versions_domain_required';
  END IF;
  IF NEW.layer='ODS' THEN
    RETURN NEW;
  END IF;

  SELECT count(*)::integer,
    count(*) FILTER(
      WHERE upstream.id IS NULL
         OR lower(platform.dataset_version_effective_domain(upstream.id))
              <>lower(target_domain)
    )::integer
  INTO dependency_count,mismatched_count
  FROM jsonb_array_elements(COALESCE(NEW.dsl_json->'nodes','[]'::jsonb)) AS node
  LEFT JOIN platform.dataset_versions AS upstream
    ON upstream.tenant_id=NEW.tenant_id
   AND upstream.id=(node->>'datasetVersionId')::uuid
   AND upstream.status='PUBLISHED'
  WHERE node->>'type'='DATASET';

  IF dependency_count=0 OR mismatched_count>0 THEN
    RAISE EXCEPTION 'downstream dataset domain must equal every published upstream domain'
      USING ERRCODE='23514',CONSTRAINT='dataset_versions_domain_lineage_match';
  END IF;
  RETURN NEW;
END
$$;
