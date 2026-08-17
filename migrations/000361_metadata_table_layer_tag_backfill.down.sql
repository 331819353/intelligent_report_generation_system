-- 移除回填的层级标签。人工后续改写的层级标签也一并移除；旧版本应用不识别该分面。
BEGIN;

UPDATE platform.metadata_tables
SET tags=ARRAY(SELECT tag FROM unnest(COALESCE(tags,'{}'::text[])) AS tag WHERE tag NOT LIKE '层级:%'),
    business_version=business_version+1
WHERE EXISTS(
  SELECT 1 FROM unnest(COALESCE(tags,'{}'::text[])) AS tag WHERE tag LIKE '层级:%'
);

COMMIT;
