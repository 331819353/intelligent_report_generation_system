DROP FUNCTION IF EXISTS platform.update_data_source_connection_test_stage(uuid,uuid,text);

ALTER TABLE platform.data_source_connection_test_jobs
  DROP COLUMN IF EXISTS stage;
