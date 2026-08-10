-- The Report V2 corrections intentionally have no unsafe logical rollback.
-- Re-applying the preceding definitions keeps data readable while allowing the
-- migration ledger to move backwards in development environments.
CREATE OR REPLACE FUNCTION platform.guard_report_v2_version_mutation()
RETURNS trigger LANGUAGE plpgsql SET search_path=pg_catalog,platform AS $$
BEGIN
  IF TG_OP='DELETE' THEN
    RAISE EXCEPTION 'report v2 immutable artifact cannot be deleted' USING ERRCODE='55000';
  END IF;
  IF ROW(OLD.id,OLD.tenant_id,OLD.report_id,OLD.version_no,OLD.source_revision_no,
         OLD.definition_json,OLD.definition_bytes,OLD.definition_hash,OLD.schema_version,
         OLD.object_uri,OLD.published_by,OLD.published_at,OLD.rollback_of_version_no,
         OLD.rollback_reason,OLD.stale_insights_acknowledged)
     IS DISTINCT FROM
     ROW(NEW.id,NEW.tenant_id,NEW.report_id,NEW.version_no,NEW.source_revision_no,
         NEW.definition_json,NEW.definition_bytes,NEW.definition_hash,NEW.schema_version,
         NEW.object_uri,NEW.published_by,NEW.published_at,NEW.rollback_of_version_no,
         NEW.rollback_reason,NEW.stale_insights_acknowledged) THEN
    RAISE EXCEPTION 'report v2 immutable artifact content cannot be changed' USING ERRCODE='55000';
  END IF;
  IF OLD.artifact_state='READY' OR
     (OLD.artifact_state='PENDING' AND NEW.artifact_state NOT IN ('PENDING','READY','RETRY')) OR
     (OLD.artifact_state='RETRY' AND NEW.artifact_state NOT IN ('RETRY','READY')) THEN
    RAISE EXCEPTION 'invalid report artifact state transition' USING ERRCODE='23514';
  END IF;
  RETURN NEW;
END
$$;

ALTER TABLE platform.ai_tenant_policies
  DROP CONSTRAINT IF EXISTS ai_tenant_policies_purposes_check;
ALTER TABLE platform.ai_tenant_policies
  ADD CONSTRAINT ai_tenant_policies_purposes_check CHECK(
    cardinality(allowed_purposes) BETWEEN 1 AND 9
    AND array_position(allowed_purposes,NULL) IS NULL
    AND allowed_purposes <@ ARRAY[
      'METADATA_COMPLETION','DATASET_DAG_GENERATION','DATASET_TAG_SUGGESTION',
      'DATASET_SEMANTIC_NAMING','DATA_SOURCE_CONFIGURATION','SEMANTIC_QUESTION',
      'REPORT_GENERATION','BLOCK_EDIT','CONCLUSION_GENERATION'
    ]::text[]
  );

DROP POLICY IF EXISTS report_shares_actor_policy ON platform.report_shares;
CREATE POLICY report_shares_actor_policy ON platform.report_shares
  USING(tenant_id=platform.current_tenant_id() AND (
    platform.is_system_access() OR created_by=platform.current_user_id() OR
    (share_type='INTERNAL_USER' AND principal_id=platform.current_user_id()) OR
    (share_type='INTERNAL_GROUP' AND EXISTS(
      SELECT 1
      FROM platform.user_roles AS assignment
      JOIN platform.roles AS role
        ON role.id=assignment.role_id AND role.tenant_id=assignment.tenant_id
      WHERE assignment.tenant_id=report_shares.tenant_id
        AND assignment.user_id=platform.current_user_id()
        AND assignment.role_id=report_shares.principal_id
        AND role.status='ACTIVE' AND role.deleted_at IS NULL
    ))
  ))
  WITH CHECK(tenant_id=platform.current_tenant_id() AND (
    platform.is_system_access() OR created_by=platform.current_user_id()
  ));
