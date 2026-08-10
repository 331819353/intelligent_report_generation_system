DROP POLICY IF EXISTS report_deliveries_access ON platform.report_deliveries;
CREATE POLICY report_deliveries_access ON platform.report_deliveries USING(
  tenant_id=platform.current_tenant_id() AND (platform.is_system_access() OR
  (domain_id=platform.current_domain_id() AND
  (recipient_user_id=platform.current_user_id() OR
   platform.report_v2_can_access(report_id,ARRAY['EDIT']::text[])))))
WITH CHECK(tenant_id=platform.current_tenant_id() AND platform.is_system_access());

DROP TRIGGER IF EXISTS report_deliveries_user_mutation_guard
  ON platform.report_deliveries;
DROP FUNCTION IF EXISTS platform.guard_report_delivery_user_mutation();
