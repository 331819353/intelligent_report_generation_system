UPDATE platform.dwd_modeling_jobs
SET trigger_role=''
WHERE trigger_role='OTHER';

ALTER TABLE platform.dwd_modeling_jobs
  DROP CONSTRAINT dwd_modeling_jobs_trigger_role_check;

ALTER TABLE platform.dwd_modeling_jobs
  ADD CONSTRAINT dwd_modeling_jobs_trigger_role_check
  CHECK(trigger_role IN ('','FACT','DIMENSION','MASTER'));
