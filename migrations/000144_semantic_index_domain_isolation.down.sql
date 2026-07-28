BEGIN;

DROP POLICY IF EXISTS dimension_member_semantic_documents_write_scope
  ON platform.dimension_member_semantic_documents;
DROP POLICY IF EXISTS dimension_member_semantic_documents_read_scope
  ON platform.dimension_member_semantic_documents;
CREATE POLICY dimension_member_semantic_documents_tenant_isolation
  ON platform.dimension_member_semantic_documents
  USING(tenant_id=platform.current_tenant_id())
  WITH CHECK(tenant_id=platform.current_tenant_id());

DROP POLICY IF EXISTS metric_semantic_documents_write_scope
  ON platform.metric_semantic_documents;
DROP POLICY IF EXISTS metric_semantic_documents_read_scope
  ON platform.metric_semantic_documents;
CREATE POLICY metric_semantic_documents_tenant_isolation
  ON platform.metric_semantic_documents
  USING(tenant_id=platform.current_tenant_id())
  WITH CHECK(tenant_id=platform.current_tenant_id());

DROP POLICY IF EXISTS semantic_documents_write_scope
  ON platform.semantic_documents;
DROP POLICY IF EXISTS semantic_documents_read_scope
  ON platform.semantic_documents;
CREATE POLICY semantic_documents_tenant_isolation
  ON platform.semantic_documents
  USING(tenant_id=platform.current_tenant_id())
  WITH CHECK(tenant_id=platform.current_tenant_id());

COMMIT;
