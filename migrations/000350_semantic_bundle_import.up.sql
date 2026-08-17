-- SEM-4S-001: 统一语义资产 JSON Bundle 导入 + 业务知识（Business Knowledge）分区。
--
-- 四分区语义资产架构（docs/09）把导入合同收敛为一个 semantic-bundle/v1 JSON
-- 文件：一个 Bundle 覆盖 MODEL / METRIC / DIMENSION / KNOWLEDGE 四个分区，
-- 上传后按确定性顺序展开为既有 semantic_import_rows 行，复用租约、校验、
-- DRAFT 提交与撤回机制。批级 asset_type 因此新增 'BUNDLE'。
--
-- 业务知识不是新的对象族：它落在 askdata.business_term_versions 上（与
-- 000340 的原则一致——口径散文属于它描述的对象所在的治理行）。本迁移给
-- 版本行补三个治理列：
--   * knowledge_kind  —— 该词条属于哪种知识（ALIAS 是历史别名词条的缺省）；
--   * authority       —— AUTHORITATIVE 定义可被直接引用为事实并在冲突时胜出，
--                        SUPPLEMENTARY 只作为 LLM 上下文；
--   * relation        —— 词条与目标对象的关系（DEFINES/CONSTRAINS/...）。
-- 纯概念型知识（不指向任何注册对象）使用 CONCEPT 目标类型，目标 UUID 由
-- code 确定性生成（与 OPERATOR 词条同一机制）。
--
-- “同一目标至多一个 AUTHORITATIVE DEFINES” 在导入四层校验（L4）里强制，
-- 不用部分唯一索引：版本化表的历史行会让索引约束产生假冲突。
BEGIN;

ALTER TABLE askdata.semantic_imports
  DROP CONSTRAINT semantic_imports_asset_type_check;
ALTER TABLE askdata.semantic_imports
  ADD CONSTRAINT semantic_imports_asset_type_check CHECK(asset_type IN (
    'MODEL','MEASURE','METRIC','METRIC_DIMENSION','DIMENSION','MEMBER',
    'HIERARCHY','RELATIONSHIP','TERM','CERTIFIED_EXAMPLE','KPI_BUNDLE',
    'EVAL_CASE','KNOWLEDGE','BUNDLE'
  ));

ALTER TABLE askdata.business_terms
  DROP CONSTRAINT business_terms_term_type_check;
ALTER TABLE askdata.business_terms
  ADD CONSTRAINT business_terms_term_type_check CHECK(term_type IN (
    'METRIC','DIMENSION','MEMBER','TIME','OPERATOR','CONCEPT'
  ));

ALTER TABLE askdata.business_term_versions
  DROP CONSTRAINT business_term_versions_target_object_type_check;
ALTER TABLE askdata.business_term_versions
  ADD CONSTRAINT business_term_versions_target_object_type_check
  CHECK(target_object_type IN (
    'METRIC','DIMENSION','MEMBER','TIME_CONTRACT','OPERATOR','LEGACY','CONCEPT'
  ));

ALTER TABLE askdata.business_term_versions
  ADD COLUMN knowledge_kind text NOT NULL DEFAULT 'ALIAS' CHECK(knowledge_kind IN (
    'ALIAS','TERM','DEFINITION','CONVENTION','POLICY','FAQ','DOMAIN_NOTE'
  )),
  ADD COLUMN authority text NOT NULL DEFAULT 'SUPPLEMENTARY' CHECK(authority IN (
    'AUTHORITATIVE','SUPPLEMENTARY'
  )),
  ADD COLUMN relation text NOT NULL DEFAULT 'EXPLAINS' CHECK(relation IN (
    'DEFINES','CONSTRAINS','EXPLAINS','EXEMPLIFIES','DEPRECATES'
  ));

COMMENT ON COLUMN askdata.business_term_versions.knowledge_kind IS
  'Business Knowledge 分区的词条种类；ALIAS 是问数别名词条（含全部历史行）的缺省，不进入知识 code 命名空间';
COMMENT ON COLUMN askdata.business_term_versions.authority IS
  'AUTHORITATIVE 定义是可引用的事实源并在冲突时胜出；SUPPLEMENTARY 只作为检索与 LLM 上下文证据';
COMMENT ON COLUMN askdata.business_term_versions.relation IS
  '词条与 target 对象的治理关系；同一目标至多一个 AUTHORITATIVE DEFINES，由导入 L4 校验强制';

-- 知识分区按 code 检索最新版本；ALIAS 词条不占用该命名空间。
CREATE INDEX askdata_business_term_versions_knowledge_code_idx
  ON askdata.business_term_versions(tenant_id,domain_id,code,version_no DESC)
  WHERE knowledge_kind<>'ALIAS';

COMMIT;
