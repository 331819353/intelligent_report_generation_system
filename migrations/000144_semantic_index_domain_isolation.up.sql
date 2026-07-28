BEGIN;

-- Vector/search indexes are not an alternate authorization path. Every
-- semantic document inherits the visibility of the governed source asset.
DROP POLICY semantic_documents_tenant_isolation
  ON platform.semantic_documents;
CREATE POLICY semantic_documents_read_scope
  ON platform.semantic_documents FOR SELECT
  USING(
    tenant_id=platform.current_tenant_id()
    AND CASE subject_type
      WHEN 'TAG' THEN platform.semantic_tag_can_read(tag_id)
      WHEN 'DATASET_VERSION' THEN platform.dataset_can_read(dataset_id)
      WHEN 'DATASET_FIELD' THEN platform.dataset_can_read(dataset_id)
      WHEN 'DIMENSION' THEN platform.dimension_can_read(dimension_id)
      WHEN 'DIMENSION_MEMBER' THEN platform.dimension_can_read(dimension_id)
      WHEN 'METRIC_VERSION' THEN platform.metric_can_read(metric_id)
      ELSE false
    END
  );
CREATE POLICY semantic_documents_write_scope
  ON platform.semantic_documents FOR ALL
  USING(
    tenant_id=platform.current_tenant_id()
    AND CASE subject_type
      WHEN 'TAG' THEN platform.semantic_tag_can_write(tag_id)
      WHEN 'DATASET_VERSION' THEN platform.dataset_can_write(dataset_id)
      WHEN 'DATASET_FIELD' THEN platform.dataset_can_write(dataset_id)
      WHEN 'DIMENSION' THEN platform.dimension_can_write(dimension_id)
      WHEN 'DIMENSION_MEMBER' THEN platform.dimension_can_write(dimension_id)
      WHEN 'METRIC_VERSION' THEN platform.metric_can_write(metric_id)
      ELSE false
    END
  )
  WITH CHECK(
    tenant_id=platform.current_tenant_id()
    AND CASE subject_type
      WHEN 'TAG' THEN platform.semantic_tag_can_write(tag_id)
      WHEN 'DATASET_VERSION' THEN platform.dataset_can_write(dataset_id)
      WHEN 'DATASET_FIELD' THEN platform.dataset_can_write(dataset_id)
      WHEN 'DIMENSION' THEN platform.dimension_can_write(dimension_id)
      WHEN 'DIMENSION_MEMBER' THEN platform.dimension_can_write(dimension_id)
      WHEN 'METRIC_VERSION' THEN platform.metric_can_write(metric_id)
      ELSE false
    END
  );

DROP POLICY metric_semantic_documents_tenant_isolation
  ON platform.metric_semantic_documents;
CREATE POLICY metric_semantic_documents_read_scope
  ON platform.metric_semantic_documents FOR SELECT
  USING(
    tenant_id=platform.current_tenant_id()
    AND CASE subject_type
      WHEN 'METRIC_VERSION' THEN platform.metric_can_read(metric_id)
      ELSE platform.dataset_can_read(dataset_id)
    END
  );
CREATE POLICY metric_semantic_documents_write_scope
  ON platform.metric_semantic_documents FOR ALL
  USING(
    tenant_id=platform.current_tenant_id()
    AND CASE subject_type
      WHEN 'METRIC_VERSION' THEN platform.metric_can_write(metric_id)
      ELSE platform.dataset_can_write(dataset_id)
    END
  )
  WITH CHECK(
    tenant_id=platform.current_tenant_id()
    AND CASE subject_type
      WHEN 'METRIC_VERSION' THEN platform.metric_can_write(metric_id)
      ELSE platform.dataset_can_write(dataset_id)
    END
  );

DROP POLICY dimension_member_semantic_documents_tenant_isolation
  ON platform.dimension_member_semantic_documents;
CREATE POLICY dimension_member_semantic_documents_read_scope
  ON platform.dimension_member_semantic_documents FOR SELECT
  USING(
    tenant_id=platform.current_tenant_id()
    AND platform.dimension_can_read(dimension_id)
  );
CREATE POLICY dimension_member_semantic_documents_write_scope
  ON platform.dimension_member_semantic_documents FOR ALL
  USING(
    tenant_id=platform.current_tenant_id()
    AND platform.dimension_can_write(dimension_id)
  )
  WITH CHECK(
    tenant_id=platform.current_tenant_id()
    AND platform.dimension_can_write(dimension_id)
  );

COMMENT ON TABLE platform.semantic_documents IS
  'Governed semantic index; RLS inherits TAG/DATASET/DIMENSION/METRIC sharing scope';

COMMIT;
