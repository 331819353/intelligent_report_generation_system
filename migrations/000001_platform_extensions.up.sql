-- 初始化平台依赖的加密、大小写不敏感文本扩展与独立模式。
CREATE EXTENSION IF NOT EXISTS pgcrypto;
CREATE EXTENSION IF NOT EXISTS citext;

CREATE SCHEMA IF NOT EXISTS platform;

COMMENT ON SCHEMA platform IS 'Intelligent report system control-plane objects';
