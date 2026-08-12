BEGIN;

DROP TABLE IF EXISTS askdata.question_envelopes;
DROP FUNCTION IF EXISTS askdata.validate_question_envelope_runtime();

COMMIT;
