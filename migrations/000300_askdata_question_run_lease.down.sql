BEGIN;

DROP FUNCTION IF EXISTS askdata.release_question_run(uuid,uuid);
DROP FUNCTION IF EXISTS askdata.heartbeat_question_run(uuid,uuid,integer);
DROP FUNCTION IF EXISTS askdata.claim_question_run(uuid,text,integer);
DROP TABLE IF EXISTS askdata.question_run_leases;

COMMIT;
