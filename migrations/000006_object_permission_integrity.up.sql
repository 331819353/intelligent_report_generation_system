-- 在数据库层校验多态授权主体确实属于当前租户。
CREATE OR REPLACE FUNCTION platform.validate_object_permission_subject()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
  -- USER 与 ROLE 共用 subject_id，必须按类型分别验证租户归属。
  IF NEW.subject_type = 'USER' AND NOT EXISTS (
    SELECT 1 FROM platform.users WHERE tenant_id = NEW.tenant_id AND id = NEW.subject_id AND deleted_at IS NULL
  ) THEN
    RAISE EXCEPTION 'object permission USER subject does not belong to tenant' USING ERRCODE = '23503';
  END IF;
  IF NEW.subject_type = 'ROLE' AND NOT EXISTS (
    SELECT 1 FROM platform.roles WHERE tenant_id = NEW.tenant_id AND id = NEW.subject_id AND deleted_at IS NULL
  ) THEN
    RAISE EXCEPTION 'object permission ROLE subject does not belong to tenant' USING ERRCODE = '23503';
  END IF;
  RETURN NEW;
END
$$;

CREATE TRIGGER object_permissions_validate_subject
BEFORE INSERT OR UPDATE OF tenant_id, subject_type, subject_id ON platform.object_permissions
FOR EACH ROW EXECUTE FUNCTION platform.validate_object_permission_subject();

COMMENT ON FUNCTION platform.validate_object_permission_subject() IS 'Enforces polymorphic USER/ROLE subject membership inside the current tenant';
