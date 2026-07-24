-- 物理数仓已迁移到独立 PostgreSQL 实例。控制库只保留平台元数据，
-- 禁止继续承载 ODS/DWD/DWS 表或发布视图。
DROP SCHEMA IF EXISTS warehouse_published CASCADE;
DROP SCHEMA IF EXISTS warehouse_staging CASCADE;
DROP SCHEMA IF EXISTS warehouse_ods CASCADE;
DROP SCHEMA IF EXISTS warehouse_dwd CASCADE;
DROP SCHEMA IF EXISTS warehouse_dws CASCADE;

COMMENT ON SCHEMA platform IS
  '智能报告系统控制面：仅保存系统与资产元数据，不保存 ODS/DWD/DWS 物理数据';
