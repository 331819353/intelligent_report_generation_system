DROP POLICY IF EXISTS business_domains_tenant_isolation ON platform.business_domains;
DROP TRIGGER IF EXISTS business_domains_set_updated_at ON platform.business_domains;
DROP TABLE IF EXISTS platform.business_domains;
