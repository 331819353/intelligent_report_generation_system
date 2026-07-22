-- 本地经营分析主题库（MySQL 半区）：4 张可关联的销售业务表。
-- 所有建表与样例写入均保持幂等，便于重新初始化或手工重放。
SET NAMES utf8mb4 COLLATE utf8mb4_0900_ai_ci;

CREATE TABLE IF NOT EXISTS customers (
  customer_id BIGINT PRIMARY KEY COMMENT '客户唯一标识',
  customer_name VARCHAR(100) NOT NULL COMMENT '客户名称',
  customer_segment VARCHAR(30) NOT NULL COMMENT '客户分层',
  registered_at DATETIME NOT NULL COMMENT '注册时间'
) COMMENT='客户维度表' DEFAULT CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci;

CREATE TABLE IF NOT EXISTS products (
  product_id BIGINT PRIMARY KEY COMMENT '商品唯一标识',
  product_code VARCHAR(30) NOT NULL UNIQUE COMMENT '商品编码',
  product_name VARCHAR(120) NOT NULL COMMENT '商品名称',
  category_name VARCHAR(60) NOT NULL COMMENT '商品品类',
  standard_price DECIMAL(18,2) NOT NULL COMMENT '标准售价'
) COMMENT='商品维度表' DEFAULT CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci;

CREATE TABLE IF NOT EXISTS sales_orders (
  order_id BIGINT PRIMARY KEY COMMENT '销售订单唯一标识',
  order_no VARCHAR(40) NOT NULL UNIQUE COMMENT '销售订单号',
  order_date DATE NOT NULL COMMENT '下单日期',
  customer_id BIGINT NOT NULL COMMENT '客户标识',
  store_id BIGINT NOT NULL COMMENT '销售门店标识',
  order_status VARCHAR(20) NOT NULL COMMENT '订单状态',
  total_amount DECIMAL(18,2) NOT NULL COMMENT '订单销售总额',
  CONSTRAINT fk_orders_customer FOREIGN KEY (customer_id) REFERENCES customers(customer_id),
  INDEX idx_sales_orders_date (order_date),
  INDEX idx_sales_orders_store (store_id)
) COMMENT='销售订单事实表' DEFAULT CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci;

CREATE TABLE IF NOT EXISTS payments (
  payment_id BIGINT PRIMARY KEY COMMENT '支付记录唯一标识',
  order_id BIGINT NOT NULL COMMENT '销售订单标识',
  paid_at DATETIME NOT NULL COMMENT '支付时间',
  payment_method VARCHAR(30) NOT NULL COMMENT '支付方式',
  paid_amount DECIMAL(18,2) NOT NULL COMMENT '实付金额',
  payment_status VARCHAR(20) NOT NULL COMMENT '支付状态',
  CONSTRAINT fk_payments_order FOREIGN KEY (order_id) REFERENCES sales_orders(order_id),
  INDEX idx_payments_paid_at (paid_at)
) COMMENT='订单支付事实表' DEFAULT CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci;

INSERT INTO customers(customer_id,customer_name,customer_segment,registered_at) VALUES
  (1,'华东企业客户','ENTERPRISE','2023-01-05 09:00:00'),
  (2,'华北零售客户','RETAIL','2023-02-10 10:30:00'),
  (3,'华南企业客户','ENTERPRISE','2023-03-15 14:20:00'),
  (4,'西部零售客户','RETAIL','2023-04-20 16:00:00'),
  (5,'杭州渠道客户','CHANNEL','2023-05-08 11:15:00')
ON DUPLICATE KEY UPDATE customer_name=VALUES(customer_name),customer_segment=VALUES(customer_segment),registered_at=VALUES(registered_at);

INSERT INTO products(product_id,product_code,product_name,category_name,standard_price) VALUES
  (1001,'P-TV-001','智慧电视 65 英寸','电视',4999.00),
  (1002,'P-AC-001','变频空调 1.5 匹','空调',3299.00),
  (1003,'P-WM-001','滚筒洗衣机 10 公斤','洗衣机',2899.00),
  (1004,'P-RF-001','多门冰箱 500 升','冰箱',5999.00)
ON DUPLICATE KEY UPDATE product_name=VALUES(product_name),category_name=VALUES(category_name),standard_price=VALUES(standard_price);

INSERT INTO sales_orders(order_id,order_no,order_date,customer_id,store_id,order_status,total_amount) VALUES
  (5001,'SO-202601-001','2026-01-08',1,101,'PAID',7998.00),
  (5002,'SO-202601-002','2026-01-16',2,201,'PAID',3299.00),
  (5003,'SO-202602-001','2026-02-03',3,301,'PAID',5999.00),
  (5004,'SO-202602-002','2026-02-18',5,102,'PAID',5798.00),
  (5005,'SO-202603-001','2026-03-07',4,401,'PAID',4999.00),
  (5006,'SO-202603-002','2026-03-22',1,101,'REFUNDED',3299.00),
  (5007,'SO-202604-001','2026-04-09',2,201,'PAID',8898.00),
  (5008,'SO-202604-002','2026-04-21',3,301,'PAID',2899.00)
ON DUPLICATE KEY UPDATE order_date=VALUES(order_date),customer_id=VALUES(customer_id),store_id=VALUES(store_id),order_status=VALUES(order_status),total_amount=VALUES(total_amount);

INSERT INTO payments(payment_id,order_id,paid_at,payment_method,paid_amount,payment_status) VALUES
  (7001,5001,'2026-01-08 10:30:00','BANK_TRANSFER',7998.00,'SUCCESS'),
  (7002,5002,'2026-01-16 13:20:00','ALIPAY',3299.00,'SUCCESS'),
  (7003,5003,'2026-02-03 16:45:00','WECHAT',5999.00,'SUCCESS'),
  (7004,5004,'2026-02-18 09:10:00','BANK_TRANSFER',5798.00,'SUCCESS'),
  (7005,5005,'2026-03-07 18:05:00','WECHAT',4999.00,'SUCCESS'),
  (7006,5006,'2026-03-22 11:40:00','ALIPAY',3299.00,'SUCCESS'),
  (7007,5007,'2026-04-09 15:25:00','BANK_TRANSFER',8898.00,'SUCCESS'),
  (7008,5008,'2026-04-21 12:15:00','WECHAT',2899.00,'SUCCESS')
ON DUPLICATE KEY UPDATE paid_at=VALUES(paid_at),payment_method=VALUES(payment_method),paid_amount=VALUES(paid_amount),payment_status=VALUES(payment_status);

GRANT SELECT ON report_source.* TO 'report_reader'@'%';
