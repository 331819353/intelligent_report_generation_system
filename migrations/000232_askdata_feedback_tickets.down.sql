DROP TABLE IF EXISTS askdata.active_learning_candidates;
DROP TABLE IF EXISTS askdata.feedback_ticket_events;
DROP TABLE IF EXISTS askdata.feedback_tickets;
DROP FUNCTION IF EXISTS askdata.feedback_ticket_can_access(uuid,uuid,uuid,uuid);
DROP FUNCTION IF EXISTS askdata.guard_feedback_ticket_event();
DROP FUNCTION IF EXISTS askdata.guard_feedback_ticket();
DROP FUNCTION IF EXISTS askdata.feedback_ticket_transition_allowed(text,text);
