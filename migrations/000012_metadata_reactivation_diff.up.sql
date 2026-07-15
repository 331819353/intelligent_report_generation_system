-- 区分首次新增与删除后重新出现的元数据资产。
ALTER TYPE platform.metadata_change_type ADD VALUE IF NOT EXISTS 'REACTIVATED';
