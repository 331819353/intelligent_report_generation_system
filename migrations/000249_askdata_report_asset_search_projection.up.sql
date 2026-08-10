ALTER TABLE askdata.search_documents
  DROP CONSTRAINT search_documents_object_type_check,
  ADD CONSTRAINT search_documents_object_type_check CHECK(object_type IN (
    'ENTITY','SEMANTIC_MODEL','MEASURE','METRIC','DIMENSION','MEMBER',
    'BUSINESS_TERM','RELATIONSHIP','CERTIFIED_EXAMPLE','REPORT_ASSET'
  )),
  DROP CONSTRAINT search_documents_view_type_check,
  ADD CONSTRAINT search_documents_view_type_check CHECK(view_type IN (
    'NAME_ALIAS','DEFINITION_QUESTION','DIMENSION_VALUE','EXAMPLE_INTENT','REPORT_PRIOR'
  ));

CREATE OR REPLACE FUNCTION askdata.validate_search_document_subject()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,askdata,platform
AS $$
DECLARE subject_valid boolean := false;
BEGIN
  CASE NEW.object_type
    WHEN 'ENTITY' THEN
      SELECT EXISTS(SELECT 1 FROM askdata.entities WHERE tenant_id=NEW.tenant_id AND domain_id=NEW.domain_id AND id=NEW.object_version_id AND status='CERTIFIED') INTO subject_valid;
    WHEN 'SEMANTIC_MODEL' THEN
      SELECT EXISTS(SELECT 1 FROM askdata.semantic_models WHERE tenant_id=NEW.tenant_id AND domain_id=NEW.domain_id AND id=NEW.object_version_id AND status='CERTIFIED') INTO subject_valid;
    WHEN 'MEASURE' THEN
      SELECT EXISTS(SELECT 1 FROM askdata.measures WHERE tenant_id=NEW.tenant_id AND domain_id=NEW.domain_id AND id=NEW.object_version_id AND status='CERTIFIED') INTO subject_valid;
    WHEN 'METRIC' THEN
      SELECT EXISTS(SELECT 1 FROM askdata.metric_versions WHERE tenant_id=NEW.tenant_id AND domain_id=NEW.domain_id AND id=NEW.object_version_id AND status='CERTIFIED') INTO subject_valid;
    WHEN 'DIMENSION' THEN
      SELECT EXISTS(SELECT 1 FROM askdata.dimensions WHERE tenant_id=NEW.tenant_id AND domain_id=NEW.domain_id AND id=NEW.object_version_id AND status='CERTIFIED' AND sensitivity<>'RESTRICTED' AND sensitivity=NEW.sensitivity) INTO subject_valid;
    WHEN 'MEMBER' THEN
      SELECT EXISTS(
        SELECT 1 FROM askdata.dimension_members AS member
        JOIN askdata.dimensions AS dimension ON dimension.id=member.dimension_version_id AND dimension.tenant_id=member.tenant_id
        WHERE member.tenant_id=NEW.tenant_id AND member.domain_id=NEW.domain_id
          AND member.id=NEW.object_version_id AND member.status='CERTIFIED'
          AND member.sensitivity IN ('PUBLIC','INTERNAL')
          AND dimension.sensitivity IN ('PUBLIC','INTERNAL')
          AND dimension.member_index_policy='FULL' AND NOT dimension.high_cardinality
          AND NEW.sensitivity=CASE WHEN member.sensitivity='INTERNAL' OR dimension.sensitivity='INTERNAL' THEN 'INTERNAL' ELSE 'PUBLIC' END
      ) INTO subject_valid;
    WHEN 'BUSINESS_TERM' THEN
      SELECT EXISTS(SELECT 1 FROM askdata.business_terms WHERE tenant_id=NEW.tenant_id AND domain_id=NEW.domain_id AND id=NEW.object_version_id AND status='CERTIFIED') INTO subject_valid;
    WHEN 'RELATIONSHIP' THEN
      SELECT EXISTS(SELECT 1 FROM askdata.relationships WHERE tenant_id=NEW.tenant_id AND domain_id=NEW.domain_id AND id=NEW.object_version_id AND status='CERTIFIED') INTO subject_valid;
    WHEN 'CERTIFIED_EXAMPLE' THEN
      subject_valid := false;
    WHEN 'REPORT_ASSET' THEN
      SELECT EXISTS(
        SELECT 1 FROM askdata.report_semantic_assets AS asset
        JOIN platform.report_versions AS version
          ON version.id=asset.report_version_id AND version.report_id=asset.report_id
         AND version.tenant_id=asset.tenant_id
        WHERE asset.tenant_id=NEW.tenant_id AND asset.domain_id=NEW.domain_id
          AND asset.id=NEW.object_version_id AND asset.state='CERTIFIED'
          AND version.artifact_state='READY'
          AND NEW.view_type='REPORT_PRIOR'
      ) INTO subject_valid;
  END CASE;
  IF NOT COALESCE(subject_valid,false) THEN
    RAISE EXCEPTION 'search document subject is not a certified indexable object'
      USING ERRCODE='23514';
  END IF;
  RETURN NEW;
END
$$;

REVOKE ALL ON FUNCTION askdata.validate_search_document_subject() FROM PUBLIC;
