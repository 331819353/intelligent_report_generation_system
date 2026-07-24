CREATE SCHEMA IF NOT EXISTS warehouse_staging;
CREATE SCHEMA IF NOT EXISTS warehouse_ods;
CREATE SCHEMA IF NOT EXISTS warehouse_dwd;
CREATE SCHEMA IF NOT EXISTS warehouse_dws;
CREATE SCHEMA IF NOT EXISTS warehouse_published;

REVOKE ALL ON SCHEMA
  warehouse_staging,warehouse_ods,warehouse_dwd,warehouse_dws,warehouse_published
FROM PUBLIC;
