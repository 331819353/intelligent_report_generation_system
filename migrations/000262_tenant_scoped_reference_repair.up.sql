-- Repair historical cross-module references so every AskData foreign key
-- carries the tenant boundary enforced by the referenced object.
ALTER TABLE platform.report_versions
  ADD CONSTRAINT report_v2_version_identity_tenant_key UNIQUE(id,tenant_id);

ALTER TABLE askdata.conversations
  DROP CONSTRAINT conversations_report_version_id_fkey,
  ADD CONSTRAINT askdata_conversations_report_version_tenant_fk
    FOREIGN KEY(report_version_id,tenant_id)
    REFERENCES platform.report_versions(id,tenant_id) ON DELETE RESTRICT;

ALTER TABLE askdata.feedback_tickets
  DROP CONSTRAINT feedback_tickets_linked_release_id_fkey,
  DROP CONSTRAINT feedback_tickets_linked_evaluation_case_id_fkey,
  ADD CONSTRAINT askdata_feedback_tickets_release_tenant_fk
    FOREIGN KEY(linked_release_id,tenant_id)
    REFERENCES askdata.releases(id,tenant_id) ON DELETE RESTRICT,
  ADD CONSTRAINT askdata_feedback_tickets_evaluation_case_tenant_fk
    FOREIGN KEY(linked_evaluation_case_id,tenant_id)
    REFERENCES askdata.evaluation_cases(id,tenant_id) ON DELETE RESTRICT;

ALTER TABLE askdata.question_seed_contexts
  DROP CONSTRAINT question_seed_contexts_report_version_id_fkey,
  DROP CONSTRAINT question_seed_contexts_pinned_release_id_fkey,
  ADD CONSTRAINT askdata_question_seed_contexts_report_version_tenant_fk
    FOREIGN KEY(report_version_id,tenant_id)
    REFERENCES platform.report_versions(id,tenant_id) ON DELETE RESTRICT,
  ADD CONSTRAINT askdata_question_seed_contexts_release_tenant_fk
    FOREIGN KEY(pinned_release_id,tenant_id)
    REFERENCES askdata.releases(id,tenant_id) ON DELETE RESTRICT;
