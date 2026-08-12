BEGIN;

DROP TABLE IF EXISTS platform.support_tickets;
DROP FUNCTION IF EXISTS platform.guard_support_ticket_mutation();

COMMIT;
