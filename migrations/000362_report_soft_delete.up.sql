-- Recoverable report deletion. Definition, immutable versions and audit history
-- remain in storage, while every report/draft/version read path fails closed.
ALTER TABLE platform.reports
  ADD COLUMN deleted_at timestamptz,
  ADD COLUMN deleted_by uuid,
  ADD CONSTRAINT report_deleted_by_fk FOREIGN KEY(deleted_by,tenant_id)
    REFERENCES platform.users(id,tenant_id) ON DELETE RESTRICT,
  ADD CONSTRAINT report_deleted_shape_check CHECK(
    (deleted_at IS NULL AND deleted_by IS NULL)
    OR (deleted_at IS NOT NULL AND deleted_by IS NOT NULL)
  );

CREATE INDEX report_asset_active_domain_updated_idx
  ON platform.reports(tenant_id,domain_id,updated_at DESC,id DESC)
  WHERE deleted_at IS NULL;

ALTER TABLE platform.report_asset_events
  DROP CONSTRAINT report_asset_events_event_type_check,
  DROP CONSTRAINT report_asset_event_shape_check;

ALTER TABLE platform.report_asset_events
  ADD CONSTRAINT report_asset_events_event_type_check CHECK(event_type IN (
    'CREATED','OWNER_CHANGED','PUBLISHED','ROLLED_BACK',
    'PERMISSION_GRANTED','PERMISSION_REVOKED',
    'ARCHIVED','RESTORED','DELETED','SHARE_CREATED','SHARE_REVOKED','PUBLISH_REVIEWED'
  )),
  ADD CONSTRAINT report_asset_event_shape_check CHECK(
    (event_type IN ('ARCHIVED','RESTORED','DELETED'))=(reason IS NOT NULL)
    AND (reason IS NULL OR (
      length(btrim(reason)) BETWEEN 1 AND 1000
      AND reason=btrim(reason) AND reason !~ '[[:cntrl:]]'
    ))
    AND ((subject_type IS NULL AND subject_id IS NULL AND action IS NULL)
      OR (subject_type IS NOT NULL AND subject_id IS NOT NULL AND action IS NOT NULL))
    AND ((event_type IN ('ARCHIVED','RESTORED'))
      =(previous_status IS NOT NULL AND new_status IS NOT NULL))
  );

CREATE OR REPLACE FUNCTION platform.report_v2_can_access(target_report_id uuid,required_actions text[])
RETURNS boolean
LANGUAGE sql STABLE SECURITY DEFINER
SET search_path=pg_catalog,platform
AS $$
  SELECT EXISTS(
    SELECT 1 FROM platform.reports target
    WHERE target.id=target_report_id
      AND target.tenant_id=platform.current_tenant_id()
      AND target.deleted_at IS NULL
      AND platform.report_v2_row_can_access(
        target.id,target.domain_id,target.owner_user_id,required_actions
      )
  )
$$;

DROP POLICY report_v2_read_policy ON platform.reports;
CREATE POLICY report_v2_read_policy ON platform.reports FOR SELECT
  USING(tenant_id=platform.current_tenant_id()
    AND (
      deleted_at IS NULL
      OR (
        deleted_by=platform.current_user_id()
        AND current_setting('app.report_asset_delete_id',true)=id::text
      )
    )
    AND platform.report_v2_row_can_access(
      id,domain_id,owner_user_id,ARRAY['VIEW','EDIT','PUBLISH']::text[]
    ));
