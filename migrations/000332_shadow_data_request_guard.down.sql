BEGIN;

DROP TRIGGER IF EXISTS platform_data_requests_00_user_run ON platform.data_requests;
DROP FUNCTION IF EXISTS platform.reject_shadow_data_request_source();

COMMIT;
