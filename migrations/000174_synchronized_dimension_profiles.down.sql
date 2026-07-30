REVOKE ALL ON FUNCTION
  platform.enqueue_current_dws_dimension_profiles(uuid)
FROM report_app;

DROP FUNCTION IF EXISTS
  platform.enqueue_current_dws_dimension_profiles(uuid);
