-- 领域标签已被彻底移除，回滚不恢复被清理的历史数据。仅放宽旧客户端可能
-- 依赖的数据库枚举；应用层仍不会创建或消费 BUSINESS_DOMAIN 标签。
ALTER TABLE platform.semantic_tags
  DROP CONSTRAINT semantic_tags_category_check,
  ADD CONSTRAINT semantic_tags_category_check CHECK(category IN (
    'BUSINESS_DOMAIN','BUSINESS_ENTITY','TABLE_FUNCTION','USAGE_SCOPE',
    'DATA_GRAIN','JOIN_ROLE','SENSITIVITY','FREEFORM'
  ));

ALTER TABLE platform.dataset_tag_suggestion_items
  DROP CONSTRAINT dataset_tag_suggestion_items_category_check,
  ADD CONSTRAINT dataset_tag_suggestion_items_category_check CHECK(category IN (
    'BUSINESS_DOMAIN','BUSINESS_ENTITY','TABLE_FUNCTION',
    'USAGE_SCOPE','DATA_GRAIN','JOIN_ROLE'
  ));
