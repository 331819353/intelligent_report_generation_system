SET NAMES utf8mb4 COLLATE utf8mb4_0900_ai_ci;

CREATE TABLE sales_order_lines (
  order_id varchar(32) NOT NULL COMMENT '订单编号',
  order_date date NOT NULL COMMENT '订单日期',
  region varchar(32) NOT NULL COMMENT '销售区域',
  channel varchar(32) NOT NULL COMMENT '销售渠道',
  product_category varchar(64) NOT NULL COMMENT '产品类别',
  quantity int NOT NULL COMMENT '销售数量',
  sales_amount decimal(18,2) NOT NULL COMMENT '销售金额',
  cost_amount decimal(18,2) NOT NULL COMMENT '成本金额',
  legacy_note varchar(128) NULL COMMENT '待迁移历史备注',
  PRIMARY KEY (order_id),
  KEY idx_sales_order_date_region (order_date, region)
) ENGINE=InnoDB COMMENT='销售订单明细事实表';

INSERT INTO sales_order_lines (
  order_id, order_date, region, channel, product_category,
  quantity, sales_amount, cost_amount, legacy_note
) VALUES
  ('MY202608001', '2026-08-01', '华东', '直营网点', '冰箱', 2, 12998.00, 10120.00, '迁移批次 A'),
  ('MY202608002', '2026-08-01', '华南', '电商', '洗衣机', 1, 4599.00, 3380.00, NULL),
  ('MY202608003', '2026-08-02', '华北', '经销商', '空调', 3, 17997.00, 14100.00, '迁移批次 A'),
  ('MY202608004', '2026-08-03', '西南', '电商', '热水器', 2, 6398.00, 4700.00, NULL),
  ('MY202608005', '2026-08-04', '华东', '经销商', '冰箱', 1, 8299.00, 6460.00, '迁移批次 B');

CREATE VIEW sales_daily_summary AS
SELECT
  order_date,
  region,
  SUM(quantity) AS total_quantity,
  SUM(sales_amount) AS total_sales_amount,
  SUM(cost_amount) AS total_cost_amount
FROM sales_order_lines
GROUP BY order_date, region;
