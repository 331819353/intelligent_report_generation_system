-- A database login is unique inside one business domain. The password is not
-- part of the identity so rotating credentials cannot create a duplicate.
ALTER TABLE platform.data_sources
  ADD COLUMN connection_identity text;

WITH connection_keys AS (
  SELECT source.id,source.tenant_id,source.domain_id,source.created_at,
    encode(digest(concat_ws(chr(31),
      version.source_type::text,
      lower(btrim(COALESCE(version.config->>'host',''))),
      COALESCE(NULLIF(btrim(version.config->>'port'),''),'0'),
      lower(btrim(COALESCE(version.config->>'database',''))),
      lower(btrim(COALESCE(version.config->>'username','')))
    ),'sha256'),'hex') AS canonical_identity
  FROM platform.data_sources AS source
  JOIN platform.data_source_versions AS version
    ON version.id=source.current_draft_version_id
  WHERE version.source_type IN ('MYSQL','ORACLE')
), ranked AS (
  SELECT connection_keys.*,
    row_number() OVER (
      PARTITION BY tenant_id,domain_id,canonical_identity
      ORDER BY created_at,id
    ) AS duplicate_number
  FROM connection_keys
)
UPDATE platform.data_sources AS source
SET connection_identity=CASE
  WHEN ranked.duplicate_number=1 THEN ranked.canonical_identity
  ELSE ranked.canonical_identity||':legacy:'||source.id::text
END
FROM ranked
WHERE ranked.id=source.id;

CREATE UNIQUE INDEX data_sources_domain_connection_identity_active_key
  ON platform.data_sources(tenant_id,domain_id,connection_identity)
  WHERE deleted_at IS NULL AND connection_identity IS NOT NULL;

COMMENT ON COLUMN platform.data_sources.connection_identity IS
  'SHA-256 of normalized type, host, port, database/service and username for domain-scoped duplicate prevention';
