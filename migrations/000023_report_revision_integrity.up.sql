-- 不可变修订一旦写入便不能修正，因此先在数据库层固化最基本的序号和正文一致性。
ALTER TABLE platform.report_revisions
  ADD CONSTRAINT report_revisions_revision_step_check
    CHECK(revision_no=base_revision_no+1),
  ADD CONSTRAINT report_revisions_change_index_range_check
    CHECK(change_index<=change_count),
  ADD CONSTRAINT report_revisions_client_operation_check
    CHECK((operation_type='REPORT_CREATE')=(client_operation_id IS NULL)),
  ADD CONSTRAINT report_revisions_patch_count_json_check
    CHECK(patch_count=jsonb_array_length(patch_json));

-- 修订审计采用显式保留策略；报告硬删除不得与不可变触发器形成隐式级联冲突。
ALTER TABLE platform.report_revisions
  DROP CONSTRAINT report_revisions_report_id_tenant_id_fkey,
  ADD CONSTRAINT report_revisions_report_id_tenant_id_fkey
    FOREIGN KEY(report_id,tenant_id) REFERENCES platform.reports(id,tenant_id) ON DELETE RESTRICT;
