-- The lifecycle trigger calls a deliberately non-public principal lookup.
-- Execute the trigger as its owner instead of exposing that lookup to runtime
-- roles, while retaining the fixed search_path from 000266.
ALTER FUNCTION platform.guard_report_share_lifecycle() SECURITY DEFINER;
REVOKE ALL ON FUNCTION platform.guard_report_share_lifecycle() FROM PUBLIC;
REVOKE ALL ON FUNCTION platform.report_share_principal_valid(uuid,text,uuid) FROM PUBLIC;
