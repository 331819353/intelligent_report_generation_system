DROP TABLE IF EXISTS askdata.saved_question_shares;
DROP TABLE IF EXISTS askdata.saved_question_dependencies;
DROP TRIGGER IF EXISTS askdata_saved_questions_90_release_reference ON askdata.saved_questions;
DROP TRIGGER IF EXISTS askdata_saved_questions_10_lifecycle ON askdata.saved_questions;
DROP TABLE IF EXISTS askdata.saved_questions;
DROP FUNCTION IF EXISTS askdata.sync_saved_question_release_reference();
DROP FUNCTION IF EXISTS askdata.enforce_saved_question();
DROP FUNCTION IF EXISTS askdata.saved_question_can_read(uuid,uuid,uuid,uuid,text);
