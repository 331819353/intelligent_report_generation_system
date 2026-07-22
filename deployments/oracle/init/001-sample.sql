-- 本地经营分析主题库（Oracle 半区）：4 张可关联的销售业务表。
ALTER SESSION SET CONTAINER=FREEPDB1;

CREATE TABLE report_reader.regions (
  region_id NUMBER(19) PRIMARY KEY,
  region_code VARCHAR2(20) NOT NULL UNIQUE,
  region_name VARCHAR2(100) NOT NULL,
  manager_name VARCHAR2(100) NOT NULL
);

CREATE TABLE report_reader.stores (
  store_id NUMBER(19) PRIMARY KEY,
  store_code VARCHAR2(20) NOT NULL UNIQUE,
  store_name VARCHAR2(100) NOT NULL,
  region_id NUMBER(19) NOT NULL,
  opened_date DATE NOT NULL,
  store_status VARCHAR2(20) NOT NULL,
  CONSTRAINT fk_stores_region FOREIGN KEY (region_id) REFERENCES report_reader.regions(region_id)
);

CREATE TABLE report_reader.sales_order_items (
  order_item_id NUMBER(19) PRIMARY KEY,
  order_id NUMBER(19) NOT NULL,
  product_id NUMBER(19) NOT NULL,
  quantity NUMBER(10) NOT NULL,
  unit_price NUMBER(18,2) NOT NULL,
  discount_amount NUMBER(18,2) NOT NULL,
  sales_amount NUMBER(18,2) NOT NULL
);
CREATE INDEX report_reader.idx_sales_order_items_order ON report_reader.sales_order_items(order_id);
CREATE INDEX report_reader.idx_sales_order_items_product ON report_reader.sales_order_items(product_id);

CREATE TABLE report_reader.refunds (
  refund_id NUMBER(19) PRIMARY KEY,
  order_id NUMBER(19) NOT NULL,
  order_item_id NUMBER(19),
  refund_date DATE NOT NULL,
  refund_amount NUMBER(18,2) NOT NULL,
  refund_status VARCHAR2(20) NOT NULL,
  CONSTRAINT fk_refunds_item FOREIGN KEY (order_item_id) REFERENCES report_reader.sales_order_items(order_item_id)
);
CREATE INDEX report_reader.idx_refunds_order ON report_reader.refunds(order_id);
CREATE INDEX report_reader.idx_refunds_date ON report_reader.refunds(refund_date);

COMMENT ON TABLE report_reader.regions IS '销售区域维度表';
COMMENT ON COLUMN report_reader.regions.region_id IS '区域唯一标识';
COMMENT ON COLUMN report_reader.regions.region_code IS '区域编码';
COMMENT ON COLUMN report_reader.regions.region_name IS '区域名称';
COMMENT ON COLUMN report_reader.regions.manager_name IS '区域负责人';

COMMENT ON TABLE report_reader.stores IS '销售门店维度表';
COMMENT ON COLUMN report_reader.stores.store_id IS '门店唯一标识';
COMMENT ON COLUMN report_reader.stores.store_code IS '门店编码';
COMMENT ON COLUMN report_reader.stores.store_name IS '门店名称';
COMMENT ON COLUMN report_reader.stores.region_id IS '所属区域标识';
COMMENT ON COLUMN report_reader.stores.opened_date IS '开店日期';
COMMENT ON COLUMN report_reader.stores.store_status IS '门店状态';

COMMENT ON TABLE report_reader.sales_order_items IS '销售订单明细事实表';
COMMENT ON COLUMN report_reader.sales_order_items.order_item_id IS '订单明细唯一标识';
COMMENT ON COLUMN report_reader.sales_order_items.order_id IS '销售订单标识';
COMMENT ON COLUMN report_reader.sales_order_items.product_id IS '商品标识';
COMMENT ON COLUMN report_reader.sales_order_items.quantity IS '销售数量';
COMMENT ON COLUMN report_reader.sales_order_items.unit_price IS '成交单价';
COMMENT ON COLUMN report_reader.sales_order_items.discount_amount IS '优惠金额';
COMMENT ON COLUMN report_reader.sales_order_items.sales_amount IS '明细销售额';

COMMENT ON TABLE report_reader.refunds IS '销售退款事实表';
COMMENT ON COLUMN report_reader.refunds.refund_id IS '退款记录唯一标识';
COMMENT ON COLUMN report_reader.refunds.order_id IS '销售订单标识';
COMMENT ON COLUMN report_reader.refunds.order_item_id IS '退款订单明细标识';
COMMENT ON COLUMN report_reader.refunds.refund_date IS '退款日期';
COMMENT ON COLUMN report_reader.refunds.refund_amount IS '退款金额';
COMMENT ON COLUMN report_reader.refunds.refund_status IS '退款状态';

INSERT INTO report_reader.regions(region_id,region_code,region_name,manager_name) VALUES (1,'EAST','华东','张伟');
INSERT INTO report_reader.regions(region_id,region_code,region_name,manager_name) VALUES (2,'NORTH','华北','李娜');
INSERT INTO report_reader.regions(region_id,region_code,region_name,manager_name) VALUES (3,'SOUTH','华南','王强');
INSERT INTO report_reader.regions(region_id,region_code,region_name,manager_name) VALUES (4,'WEST','西部','赵敏');

INSERT INTO report_reader.stores(store_id,store_code,store_name,region_id,opened_date,store_status) VALUES (101,'SH-001','上海旗舰店',1,DATE '2021-03-18','ACTIVE');
INSERT INTO report_reader.stores(store_id,store_code,store_name,region_id,opened_date,store_status) VALUES (102,'HZ-001','杭州中心店',1,DATE '2022-06-01','ACTIVE');
INSERT INTO report_reader.stores(store_id,store_code,store_name,region_id,opened_date,store_status) VALUES (201,'BJ-001','北京旗舰店',2,DATE '2020-09-10','ACTIVE');
INSERT INTO report_reader.stores(store_id,store_code,store_name,region_id,opened_date,store_status) VALUES (301,'GZ-001','广州中心店',3,DATE '2021-11-08','ACTIVE');
INSERT INTO report_reader.stores(store_id,store_code,store_name,region_id,opened_date,store_status) VALUES (401,'CD-001','成都中心店',4,DATE '2023-01-15','ACTIVE');

INSERT INTO report_reader.sales_order_items(order_item_id,order_id,product_id,quantity,unit_price,discount_amount,sales_amount) VALUES (6001,5001,1001,1,4999.00,0.00,4999.00);
INSERT INTO report_reader.sales_order_items(order_item_id,order_id,product_id,quantity,unit_price,discount_amount,sales_amount) VALUES (6002,5001,1002,1,3299.00,300.00,2999.00);
INSERT INTO report_reader.sales_order_items(order_item_id,order_id,product_id,quantity,unit_price,discount_amount,sales_amount) VALUES (6003,5002,1002,1,3299.00,0.00,3299.00);
INSERT INTO report_reader.sales_order_items(order_item_id,order_id,product_id,quantity,unit_price,discount_amount,sales_amount) VALUES (6004,5003,1004,1,5999.00,0.00,5999.00);
INSERT INTO report_reader.sales_order_items(order_item_id,order_id,product_id,quantity,unit_price,discount_amount,sales_amount) VALUES (6005,5004,1003,2,2899.00,0.00,5798.00);
INSERT INTO report_reader.sales_order_items(order_item_id,order_id,product_id,quantity,unit_price,discount_amount,sales_amount) VALUES (6006,5005,1001,1,4999.00,0.00,4999.00);
INSERT INTO report_reader.sales_order_items(order_item_id,order_id,product_id,quantity,unit_price,discount_amount,sales_amount) VALUES (6007,5006,1002,1,3299.00,0.00,3299.00);
INSERT INTO report_reader.sales_order_items(order_item_id,order_id,product_id,quantity,unit_price,discount_amount,sales_amount) VALUES (6008,5007,1004,1,5999.00,0.00,5999.00);
INSERT INTO report_reader.sales_order_items(order_item_id,order_id,product_id,quantity,unit_price,discount_amount,sales_amount) VALUES (6009,5007,1003,1,2899.00,0.00,2899.00);
INSERT INTO report_reader.sales_order_items(order_item_id,order_id,product_id,quantity,unit_price,discount_amount,sales_amount) VALUES (6010,5008,1003,1,2899.00,0.00,2899.00);

INSERT INTO report_reader.refunds(refund_id,order_id,order_item_id,refund_date,refund_amount,refund_status) VALUES (8001,5006,6007,DATE '2026-03-25',3299.00,'COMPLETED');
INSERT INTO report_reader.refunds(refund_id,order_id,order_item_id,refund_date,refund_amount,refund_status) VALUES (8002,5001,6002,DATE '2026-02-02',500.00,'COMPLETED');
COMMIT;
