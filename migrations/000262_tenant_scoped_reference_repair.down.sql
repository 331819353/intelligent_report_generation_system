ALTER TABLE askdata.question_seed_contexts
  DROP CONSTRAINT askdata_question_seed_contexts_release_tenant_fk,
  DROP CONSTRAINT askdata_question_seed_contexts_report_version_tenant_fk,
  ADD CONSTRAINT question_seed_contexts_report_version_id_fkey
    FOREIGN KEY(report_version_id)
    REFERENCES platform.report_versions(id) ON DELETE RESTRICT,
  ADD CONSTRAINT question_seed_contexts_pinned_release_id_fkey
    FOREIGN KEY(pinned_release_id)
    REFERENCES askdata.releases(id) ON DELETE RESTRICT;

ALTER TABLE askdata.feedback_tickets
  DROP CONSTRAINT askdata_feedback_tickets_evaluation_case_tenant_fk,
  DROP CONSTRAINT askdata_feedback_tickets_release_tenant_fk,
  ADD CONSTRAINT feedback_tickets_linked_release_id_fkey
    FOREIGN KEY(linked_release_id)
    REFERENCES askdata.releases(id) ON DELETE RESTRICT,
  ADD CONSTRAINT feedback_tickets_linked_evaluation_case_id_fkey
    FOREIGN KEY(linked_evaluation_case_id)
    REFERENCES askdata.evaluation_cases(id) ON DELETE RESTRICT;

ALTER TABLE askdata.conversations
  DROP CONSTRAINT askdata_conversations_report_version_tenant_fk,
  ADD CONSTRAINT conversations_report_version_id_fkey
    FOREIGN KEY(report_version_id)
    REFERENCES platform.report_versions(id) ON DELETE RESTRICT;

ALTER TABLE platform.report_versions
  DROP CONSTRAINT report_v2_version_identity_tenant_key;
