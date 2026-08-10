DROP FUNCTION IF EXISTS askdata.activate_release(uuid,uuid,uuid,uuid,bigint);
DROP FUNCTION IF EXISTS askdata.submit_release_approval(uuid,uuid,uuid,text,text,text,text,uuid);
DROP FUNCTION IF EXISTS askdata.record_release_review_report(uuid,uuid,uuid,text,text,jsonb,uuid);

DROP TABLE IF EXISTS askdata.release_approvals;
DROP TABLE IF EXISTS askdata.release_review_reports;
