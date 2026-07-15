-- 在本地 Oracle PDB 中创建连接器元数据采集和查询示例表。
ALTER SESSION SET CONTAINER=FREEPDB1;

CREATE TABLE report_reader.orders (
  order_id NUMBER(19) PRIMARY KEY,
  customer_id NUMBER(19) NOT NULL,
  amount NUMBER(18,2) NOT NULL,
  created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP NOT NULL
);
INSERT INTO report_reader.orders(order_id,customer_id,amount) VALUES (101,1,1200.50);
INSERT INTO report_reader.orders(order_id,customer_id,amount) VALUES (102,2,800.00);
INSERT INTO report_reader.orders(order_id,customer_id,amount) VALUES (103,1,300.25);
COMMIT;
