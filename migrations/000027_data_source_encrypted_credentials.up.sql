-- 禁止把页面提交的数据库密码混入可回显配置，并明确内部加密引用的持久化语义。
ALTER TABLE platform.data_sources
  ADD CONSTRAINT data_source_config_no_plaintext_credentials
  CHECK (NOT (config ?| ARRAY['password','passwd','secretRef','secret_ref','jdbcUrl','jdbc_url']));

COMMENT ON COLUMN platform.data_sources.config IS 'Non-sensitive connection options only; passwords and connection strings are forbidden';
COMMENT ON COLUMN platform.data_sources.secret_ref IS 'External or AES-GCM encrypted credential reference; plaintext credentials are forbidden';
