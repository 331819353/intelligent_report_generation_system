REVOKE ALL ON FUNCTION platform.cancel_background_task(text,uuid,uuid)
  FROM report_app;
DROP FUNCTION IF EXISTS platform.cancel_background_task(text,uuid,uuid);
