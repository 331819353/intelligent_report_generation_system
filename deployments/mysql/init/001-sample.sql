-- 创建仅供本地联调的示例表，并确保连接器账号只有读取权限。
CREATE TABLE IF NOT EXISTS customers (
  customer_id BIGINT PRIMARY KEY,
  customer_name VARCHAR(100) NOT NULL,
  region_code VARCHAR(20) NOT NULL
);
INSERT INTO customers(customer_id,customer_name,region_code) VALUES
  (1,'华东客户','CN-SH'),(2,'华北客户','CN-BJ'),(3,'华南客户','CN-GD')
ON DUPLICATE KEY UPDATE customer_name=VALUES(customer_name),region_code=VALUES(region_code);
GRANT SELECT ON report_source.* TO 'report_reader'@'%';
