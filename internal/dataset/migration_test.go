package dataset

import (
	"os"
	"strings"
	"testing"
)

func TestDatasetPublicationOriginMigrationKeepsAuthorizationOutOfAuditLogs(t *testing.T) {
	raw, err := os.ReadFile("../../migrations/000072_dataset_publication_origin.up.sql")
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	migration := string(raw)
	required := []string{
		"ADD COLUMN publication_origin text",
		"SET publication_origin='HUMAN_APPROVAL'",
		"audit.detail->>'publicationSource'",
		"audit.detail->>'originTableId'",
		"audit.detail->>'publishedVersionId'",
		"audit.detail->>'versionNo'",
		"audit.detail->>'dslHash'",
		"audit.detail->>'planHash'",
		"dataset_versions_status_publication_origin_check",
		"dataset_publication_origin_facts_match",
		"request.status='PENDING'",
		"request.reserved_published_version_id=candidate.id",
		"dataset.origin_table_id IS NOT NULL",
		"pending.status='PENDING'",
		"NEW.source_draft_version_id,NEW.source_draft_record_version,NEW.publication_origin",
		"OLD.source_draft_version_id,OLD.source_draft_record_version,OLD.publication_origin",
	}
	for _, fragment := range required {
		if !strings.Contains(migration, fragment) {
			t.Errorf("publication-origin migration is missing %q", fragment)
		}
	}

	const helperStart = "CREATE OR REPLACE FUNCTION platform.dataset_publication_origin_facts_match("
	start := strings.Index(migration, helperStart)
	if start < 0 {
		t.Fatal("publication-origin fact helper is missing")
	}
	helperTail := migration[start:]
	end := strings.Index(helperTail, "\n$$;")
	if end < 0 {
		t.Fatal("publication-origin fact helper has no terminator")
	}
	helper := helperTail[:end]
	if strings.Contains(helper, "audit_logs") {
		t.Fatal("runtime publication-origin authorization still depends on audit_logs")
	}
}

func TestSystemMappedDraftUpgradeMigrationUsesCurrentRelationalFacts(t *testing.T) {
	raw, err := os.ReadFile("../../migrations/000076_system_mapped_draft_upgrade_publication.up.sql")
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	migration := string(raw)
	required := []string{
		"WHEN 'SYSTEM_MAPPED_DEFAULT'",
		"dataset.version=candidate.source_draft_record_version",
		"draft.record_version=candidate.source_draft_record_version",
		"draft.schema_hash=candidate.schema_hash",
		"draft.plan_hash=candidate.plan_hash",
		")=candidate.source_draft_record_version",
		"revision.operation_type='CREATE'",
		"revision.operation_type='ROLLBACK'",
		"platform.dataset_publication_requests AS request",
		"history.status IN ('PUBLISHED','STALE','DEPRECATED')",
	}
	for _, fragment := range required {
		if !strings.Contains(migration, fragment) {
			t.Errorf("system mapped draft upgrade migration is missing %q", fragment)
		}
	}
	if strings.Contains(migration, "audit_logs") {
		t.Fatal("system mapped default publication authorization depends on audit logs")
	}
}

func TestDeletedODSMetadataCleanupMigrationOnlyTouchesControlPlaneAssets(t *testing.T) {
	raw, err := os.ReadFile("../../migrations/000077_deleted_ods_metadata_asset_cleanup.up.sql")
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	migration := strings.ToUpper(string(raw))
	required := []string{
		"UPDATE PLATFORM.METADATA_COLUMNS",
		"UPDATE PLATFORM.METADATA_TABLES",
		"DATASET.ORIGIN_TABLE_ID",
		"DATASET.LAYER='ODS'",
		"DATASET.DELETED_AT IS NOT NULL",
		"ASSET_STATUS='INACTIVE'",
		"MANAGEMENT_STATUS='DISABLED'",
	}
	for _, fragment := range required {
		if !strings.Contains(migration, fragment) {
			t.Errorf("deleted ODS metadata cleanup migration is missing %q", fragment)
		}
	}
	for _, forbidden := range []string{"DROP TABLE", "TRUNCATE", "DELETE FROM"} {
		if strings.Contains(migration, forbidden) {
			t.Fatalf("deleted ODS metadata cleanup migration contains %q", forbidden)
		}
	}
}

func TestODSDWDModelingMigrationSchedulesLLMWorkAndGuardsDeletion(t *testing.T) {
	raw, err := os.ReadFile("../../migrations/000078_ods_dwd_modeling.up.sql")
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	migration := string(raw)
	required := []string{
		"CREATE TABLE platform.dwd_modeling_jobs",
		"CREATE TABLE platform.dwd_modeling_outputs",
		"prompt_version text NOT NULL DEFAULT 'dwd-modeling-v1'",
		"ai_request_id uuid",
		"NEW.layer='ODS'",
		"NEW.status='PUBLISHED'",
		"interval '5 minutes'",
		"UNIQUE(tenant_id,trigger_dataset_version_id)",
		"last_generated_schema_hash",
		"dataset_versions_enqueue_dwd_modeling",
		"downstream_version.layer='DWD'",
		"datasets_ods_dwd_reference_guard",
	}
	for _, fragment := range required {
		if !strings.Contains(migration, fragment) {
			t.Errorf("ODS-to-DWD migration is missing %q", fragment)
		}
	}
	if strings.Contains(strings.ToUpper(migration), "DROP TABLE PLATFORM.METADATA") {
		t.Fatal("ODS-to-DWD migration must not alter physical source tables")
	}
}

func TestDWDModelingOtherRoleMigrationMatchesLLMClassificationContract(t *testing.T) {
	raw, err := os.ReadFile("../../migrations/000079_dwd_modeling_other_role.up.sql")
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	migration := string(raw)
	for _, fragment := range []string{
		"DROP CONSTRAINT dwd_modeling_jobs_trigger_role_check",
		"'FACT','DIMENSION','MASTER','OTHER'",
	} {
		if !strings.Contains(migration, fragment) {
			t.Errorf("DWD OTHER role migration is missing %q", fragment)
		}
	}
}

func TestODSDWDHistoricalReferenceGuardIncludesDeprecatedVersions(t *testing.T) {
	raw, err := os.ReadFile("../../migrations/000080_ods_dwd_historical_reference_guard.up.sql")
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	migration := string(raw)
	for _, fragment := range []string{
		"downstream_version.layer='DWD'",
		"downstream_dataset.deleted_at IS NULL",
		"datasets_ods_dwd_reference_guard",
	} {
		if !strings.Contains(migration, fragment) {
			t.Errorf("historical DWD reference guard is missing %q", fragment)
		}
	}
	if strings.Contains(migration, "downstream_version.status<>'DEPRECATED'") {
		t.Fatal("historical DWD references are incorrectly ignored after version deprecation")
	}
}

func TestPostApprovalProcessingMigrationCancelsOnlyLegacyPreApprovalWork(t *testing.T) {
	raw, err := os.ReadFile("../../migrations/000083_post_approval_dataset_processing.up.sql")
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	migration := string(raw)
	for _, fragment := range []string{
		"UPDATE platform.metric_candidate_preparation_jobs",
		"WHERE status IN ('PENDING','RUNNING')",
		"status='CANCELLED'",
		"error_code='MOVED_AFTER_APPROVAL'",
		"request.status='PENDING'",
		"metric_candidate_generation_status='PENDING'",
		"v83 起不再创建",
	} {
		if !strings.Contains(migration, fragment) {
			t.Errorf("post-approval processing migration is missing %q", fragment)
		}
	}
	for _, forbidden := range []string{
		"UPDATE platform.dataset_build_runs",
		"UPDATE platform.metric_extraction_jobs",
		"DELETE FROM",
		"DROP TABLE",
	} {
		if strings.Contains(migration, forbidden) {
			t.Fatalf("post-approval migration unexpectedly contains %q", forbidden)
		}
	}
}

func TestResumableWarehouseModelingMigrationPersistsOnlyValidatedStages(t *testing.T) {
	raw, err := os.ReadFile(
		"../../migrations/000085_resumable_warehouse_modeling.up.sql",
	)
	if err != nil {
		t.Fatalf("read resumable warehouse modeling migration: %v", err)
	}
	migration := string(raw)
	for _, fragment := range []string{
		"ADD COLUMN checkpoint_version",
		"ADD COLUMN claimed_checkpoint_version",
		"CREATE TABLE platform.dwd_modeling_checkpoints",
		"'CLASSIFICATION','FACT_DESIGN'",
		"snapshot_hash",
		"prompt_version",
		"payload_hash",
		"materialization_json_is_safe(payload_json)",
		"REFERENCES platform.ai_requests(id,tenant_id)",
		"ENABLE ROW LEVEL SECURITY",
		"FORCE ROW LEVEL SECURITY",
	} {
		if !strings.Contains(migration, fragment) {
			t.Errorf("resumable modeling migration is missing %q", fragment)
		}
	}
	for _, forbidden := range []string{
		"prompt_text", "response_text", "sample_rows", "physical_table",
	} {
		if strings.Contains(strings.ToLower(migration), forbidden) {
			t.Fatalf(
				"resumable modeling migration stores forbidden content %q",
				forbidden,
			)
		}
	}
}

func TestDimensionDesignCheckpointMigrationExtendsResumableStages(t *testing.T) {
	raw, err := os.ReadFile(
		"../../migrations/000108_dimension_design_checkpoints.up.sql",
	)
	if err != nil {
		t.Fatalf("read DIM design checkpoint migration: %v", err)
	}
	migration := string(raw)
	for _, fragment := range []string{
		"DROP CONSTRAINT dwd_modeling_checkpoints_checkpoint_kind_check",
		"'CLASSIFICATION','DIM_DESIGN','FACT_DESIGN'",
		"逐 DIM 说明与标准化设计",
	} {
		if !strings.Contains(migration, fragment) {
			t.Errorf("DIM design checkpoint migration is missing %q", fragment)
		}
	}
	for _, forbidden := range []string{
		"prompt_text", "response_text", "sample_rows", "physical_table",
	} {
		if strings.Contains(strings.ToLower(migration), forbidden) {
			t.Fatalf(
				"DIM design checkpoint migration stores forbidden content %q",
				forbidden,
			)
		}
	}
}

func TestMultiOutputODSModelingMigrationUpgradesPromptContracts(t *testing.T) {
	raw, err := os.ReadFile(
		"../../migrations/000111_multi_output_ods_modeling.up.sql",
	)
	if err != nil {
		t.Fatalf("read multi-output ODS modeling migration: %v", err)
	}
	migration := string(raw)
	for _, fragment := range []string{
		"warehouse-classification-v2",
		"warehouse-dimension-design-v2",
		"status='PENDING'",
		"pg_get_functiondef",
		"trigger_manual_dwd_modeling(uuid)",
		"一张 ODS 可按实际粒度同时产出 DWD 与抽取 DIM",
	} {
		if !strings.Contains(migration, fragment) {
			t.Errorf("multi-output ODS migration is missing %q", fragment)
		}
	}
	for _, forbidden := range []string{
		"DELETE FROM", "TRUNCATE", "DROP TABLE",
	} {
		if strings.Contains(migration, forbidden) {
			t.Fatalf("multi-output ODS migration unexpectedly contains %q", forbidden)
		}
	}
}

func TestManualDatasetLLMTriggerMigrationRemovesPublicationAutomation(t *testing.T) {
	raw, err := os.ReadFile("../../migrations/000096_manual_dataset_llm_triggers.up.sql")
	if err != nil {
		t.Fatalf("read manual LLM trigger migration: %v", err)
	}
	migration := string(raw)
	for _, fragment := range []string{
		"DROP TRIGGER IF EXISTS dataset_versions_enqueue_tag_suggestion",
		"DROP TRIGGER IF EXISTS dataset_versions_enqueue_dwd_modeling",
		"DROP TRIGGER IF EXISTS dataset_versions_enqueue_dws_modeling",
		"人工提交",
	} {
		if !strings.Contains(migration, fragment) {
			t.Errorf("manual LLM trigger migration is missing %q", fragment)
		}
	}
	for _, forbidden := range []string{
		"DROP TABLE platform.dataset_tag_suggestion_jobs",
		"DROP TABLE platform.dwd_modeling_jobs",
		"DROP TABLE platform.dws_modeling_jobs",
	} {
		if strings.Contains(migration, forbidden) {
			t.Fatalf("manual LLM trigger migration unexpectedly contains %q", forbidden)
		}
	}
}

func TestAutomaticNonODSDatasetTagsMigrationRestoresOnlyTagAutomation(t *testing.T) {
	raw, err := os.ReadFile("../../migrations/000097_automatic_non_ods_dataset_tags.up.sql")
	if err != nil {
		t.Fatalf("read automatic non-ODS tag migration: %v", err)
	}
	migration := string(raw)
	for _, fragment := range []string{
		"NEW.layer IN ('DIM','DWD','DWS','ADS')",
		"CREATE TRIGGER dataset_versions_enqueue_tag_suggestion",
		"FOR EACH ROW EXECUTE FUNCTION platform.enqueue_dataset_tag_suggestion()",
		"version.layer IN ('DIM','DWD','DWS','ADS')",
		"ON CONFLICT(tenant_id,dataset_version_id,prompt_version) DO NOTHING",
		"非 ODS 数据集发布时自动登记",
	} {
		if !strings.Contains(migration, fragment) {
			t.Errorf("automatic non-ODS tag migration is missing %q", fragment)
		}
	}
	for _, forbidden := range []string{
		"CREATE TRIGGER dataset_versions_enqueue_dwd_modeling",
		"CREATE TRIGGER dataset_versions_enqueue_dws_modeling",
		"NEW.layer='ODS'",
	} {
		if strings.Contains(migration, forbidden) {
			t.Fatalf("automatic non-ODS tag migration unexpectedly contains %q", forbidden)
		}
	}
}

func TestModelingRunVisibilityAndDraftTagsMigrationRepairsCurrentAssets(t *testing.T) {
	raw, err := os.ReadFile(
		"../../migrations/000102_modeling_run_visibility_and_draft_tags.up.sql",
	)
	if err != nil {
		t.Fatalf("read modeling visibility/tag migration: %v", err)
	}
	migration := string(raw)
	for _, fragment := range []string{
		"ADD COLUMN requested_at timestamptz",
		"'CONTROLLED','ACTIVE'",
		"'作用:事实明细'",
		"'作用:实体维度'",
		"NEW.status IN ('DRAFT','PUBLISHED')",
		"AFTER INSERT OR UPDATE OF status,schema_hash",
		"dataset.current_draft_version_id=version.id",
		"version.status IN ('DRAFT','PUBLISHED')",
		"UNIQUE(tenant_id,dataset_version_id,prompt_version,schema_hash)",
		"'dataset-tag-suggestion-v4'",
	} {
		if !strings.Contains(migration, fragment) {
			t.Errorf("modeling visibility/tag migration is missing %q", fragment)
		}
	}
}

func TestManualModelingUsesTenantBoundSecurityDefinerEntrypoints(t *testing.T) {
	raw, err := os.ReadFile(
		"../../migrations/000106_secure_manual_modeling_entrypoints.up.sql",
	)
	if err != nil {
		t.Fatalf("read secure manual modeling migration: %v", err)
	}
	migration := string(raw)
	for _, fragment := range []string{
		"SECURITY DEFINER",
		"platform.trigger_manual_dwd_modeling(actor_id uuid)",
		"platform.trigger_manual_dws_modeling(actor_id uuid)",
		"version.tenant_id=platform.current_tenant_id()",
		"requested_at=now()",
		"REVOKE ALL ON FUNCTION",
	} {
		if !strings.Contains(migration, fragment) {
			t.Errorf("secure manual modeling migration is missing %q", fragment)
		}
	}
}

func TestDWSSemanticAssetDiscoveryPipelineIsAutomaticAndReviewBounded(t *testing.T) {
	metricRaw, err := os.ReadFile(
		"../../migrations/000098_dws_metric_discovery_guarantee.up.sql",
	)
	if err != nil {
		t.Fatalf("read DWS metric discovery migration: %v", err)
	}
	metricMigration := string(metricRaw)
	for _, fragment := range []string{
		"NEW.layer='DWS'",
		"NEW.status='PUBLISHED'",
		"INSERT INTO platform.metric_extraction_jobs",
		"'metric-candidate-v4'",
		"CREATE TRIGGER dataset_versions_enqueue_dws_metric_discovery",
		"ON CONFLICT(tenant_id,dataset_version_id,extractor_version) DO NOTHING",
	} {
		if !strings.Contains(metricMigration, fragment) {
			t.Errorf("DWS metric discovery migration is missing %q", fragment)
		}
	}
	if strings.Contains(metricMigration, "INSERT INTO platform.metrics") {
		t.Fatal("DWS discovery must create review candidates, not publish metrics")
	}

	dimensionRaw, err := os.ReadFile(
		"../../migrations/000068_dws_dimension_survey.up.sql",
	)
	if err != nil {
		t.Fatalf("read DWS dimension survey migration: %v", err)
	}
	dimensionMigration := string(dimensionRaw)
	for _, fragment := range []string{
		"CREATE TRIGGER dataset_versions_enqueue_dimension_survey",
		"INSERT INTO platform.dimension_survey_runs",
		"INSERT INTO platform.dimension_survey_candidates",
		"CREATE TRIGGER dataset_materializations_complete_dimension_survey",
	} {
		if !strings.Contains(dimensionMigration, fragment) {
			t.Errorf("DWS dimension discovery migration is missing %q", fragment)
		}
	}

	surveyStoreRaw, err := os.ReadFile(
		"../semanticmanagement/dimension_survey_postgres.go",
	)
	if err != nil {
		t.Fatalf("read dimension survey store: %v", err)
	}
	surveyStore := string(surveyStoreRaw)
	for _, fragment := range []string{
		"INSERT INTO platform.semantic_dimensions",
		"'PUBLISHED'",
		"INSERT INTO platform.dimension_member_refresh_jobs",
		"'QUEUED'",
	} {
		if !strings.Contains(surveyStore, fragment) {
			t.Errorf("dimension member mapping workflow is missing %q", fragment)
		}
	}
}

func TestIncrementalWarehouseModelingRecoveryOnlyRequeuesCurrentBrokenJobs(t *testing.T) {
	raw, err := os.ReadFile(
		"../../migrations/000099_incremental_warehouse_modeling_recovery.up.sql",
	)
	if err != nil {
		t.Fatalf("read incremental warehouse modeling recovery migration: %v", err)
	}
	migration := string(raw)
	for _, fragment := range []string{
		"dataset.current_published_version_id=job.trigger_dataset_version_id",
		"job.status IN ('FAILED','SKIPPED')",
		"'WAREHOUSE_MODELING_INVALID_OUTPUT'",
		"'SUBJECT_CHANGED'",
		"status='PENDING'",
		"attempt=0",
		"claimed_checkpoint_version=checkpoint_version",
	} {
		if !strings.Contains(migration, fragment) {
			t.Errorf("incremental recovery migration is missing %q", fragment)
		}
	}
	for _, forbidden := range []string{
		"DELETE FROM", "DROP TABLE", "TRUNCATE",
	} {
		if strings.Contains(migration, forbidden) {
			t.Fatalf("incremental recovery migration unexpectedly contains %q", forbidden)
		}
	}
}

func TestWarehouseContractCompletionRecoveryIsIncrementalAndBusinessSafe(t *testing.T) {
	raw, err := os.ReadFile(
		"../../migrations/000100_warehouse_contract_completion_recovery.up.sql",
	)
	if err != nil {
		t.Fatalf("read warehouse contract completion recovery migration: %v", err)
	}
	migration := string(raw)
	for _, fragment := range []string{
		"job.error_code='SOME_LAYER_DESIGNS_SKIPPED'",
		"modeled.code ~ '^dim_auto_[0-9a-f]{16,}$'",
		"dataset.current_published_version_id=job.trigger_dataset_version_id",
		"SELECT DISTINCT ON (job.tenant_id, job.domain_key)",
		"claimed_checkpoint_version=checkpoint_version",
		"status='PENDING'",
	} {
		if !strings.Contains(migration, fragment) {
			t.Errorf("warehouse contract recovery migration is missing %q", fragment)
		}
	}
	for _, forbidden := range []string{
		"DELETE FROM", "DROP TABLE", "TRUNCATE",
	} {
		if strings.Contains(migration, forbidden) {
			t.Fatalf("warehouse contract recovery unexpectedly contains %q", forbidden)
		}
	}
}

func TestWarehouseCaseAndCodeRecoveryTargetsOnlyCurrentAffectedDomains(t *testing.T) {
	raw, err := os.ReadFile(
		"../../migrations/000101_warehouse_case_and_code_recovery.up.sql",
	)
	if err != nil {
		t.Fatalf("read warehouse case/code recovery migration: %v", err)
	}
	migration := string(raw)
	for _, fragment := range []string{
		"job.error_code='SOME_LAYER_DESIGNS_SKIPPED'",
		"modeled.code ~ '^dwd_(agg|fact|fct|ods|mapped)_'",
		"dataset.current_published_version_id=job.trigger_dataset_version_id",
		"SELECT DISTINCT ON (job.tenant_id, job.domain_key)",
		"claimed_checkpoint_version=checkpoint_version",
		"status='PENDING'",
	} {
		if !strings.Contains(migration, fragment) {
			t.Errorf("warehouse case/code recovery migration is missing %q", fragment)
		}
	}
	for _, forbidden := range []string{
		"DELETE FROM", "DROP TABLE", "TRUNCATE",
	} {
		if strings.Contains(migration, forbidden) {
			t.Fatalf("warehouse case/code recovery unexpectedly contains %q", forbidden)
		}
	}
}

func TestDeletedModeledOutputInvalidatesOnlyTheRequiredLLMCheckpoints(t *testing.T) {
	raw, err := os.ReadFile(
		"../../migrations/000112_deleted_model_output_checkpoint_invalidation.up.sql",
	)
	if err != nil {
		t.Fatalf("read deleted model output invalidation migration: %v", err)
	}
	migration := string(raw)
	for _, fragment := range []string{
		"CREATE OR REPLACE FUNCTION platform.invalidate_deleted_modeled_dataset",
		"selected_layer='DIM'",
		"checkpoint.checkpoint_kind='FACT_DESIGN'",
		"checkpoint.subject_dataset_version_id=",
		"status='SKIPPED'",
		"error_code='MODEL_OUTPUT_DELETED'",
		"DELETE FROM platform.dwd_modeling_checkpoints",
		"'INVALIDATE_MODELING_CHECKPOINTS'",
		"SET row_security=off",
	} {
		if !strings.Contains(migration, fragment) {
			t.Errorf("deleted output invalidation migration is missing %q", fragment)
		}
	}

	storeRaw, err := os.ReadFile("postgres_store.go")
	if err != nil {
		t.Fatalf("read dataset postgres store: %v", err)
	}
	store := string(storeRaw)
	invalidateAt := strings.Index(
		store,
		"platform.invalidate_deleted_modeled_dataset",
	)
	deleteMappingAt := strings.Index(
		store,
		"DELETE FROM platform.dwd_modeling_outputs",
	)
	if invalidateAt < 0 {
		t.Fatal("dataset deletion does not invalidate modeled output checkpoints")
	}
	if deleteMappingAt < 0 || invalidateAt > deleteMappingAt {
		t.Fatal("checkpoint invalidation must run before output ownership is deleted")
	}
}

func TestDraftUpstreamTagsAndDIMPublicationResumeAreEventDriven(t *testing.T) {
	raw, err := os.ReadFile(
		"../../migrations/000113_draft_upstream_tags_and_dim_resume.up.sql",
	)
	if err != nil {
		t.Fatalf("read draft upstream tag and DIM resume migration: %v", err)
	}
	migration := string(raw)
	for _, fragment := range []string{
		"'dataset-tag-suggestion-v5'",
		"error_code='PROMPT_SUPERSEDED'",
		"CREATE OR REPLACE FUNCTION platform.resume_fact_modeling_after_dim_publication",
		"stage.error_code='DIM_PUBLICATION_REQUIRED'",
		"stage.stage='FACT_MODELING'",
		"dimension.current_published_version_id IS NULL",
		"CREATE TRIGGER datasets_resume_fact_modeling_after_dim_publication",
	} {
		if !strings.Contains(migration, fragment) {
			t.Errorf("draft upstream/DIM resume migration is missing %q", fragment)
		}
	}

	tagStoreRaw, err := os.ReadFile(
		"../datasettagsuggestion/postgres_store.go",
	)
	if err != nil {
		t.Fatalf("read dataset tag suggestion store: %v", err)
	}
	tagStore := string(tagStoreRaw)
	for _, fragment := range []string{
		"$2='DRAFT'",
		"version.status='DRAFT'",
		"dataset.current_draft_version_id=version.id",
	} {
		if !strings.Contains(tagStore, fragment) {
			t.Errorf("draft upstream tag loading is missing %q", fragment)
		}
	}

	modelingRaw, err := os.ReadFile("dwd_modeling.go")
	if err != nil {
		t.Fatalf("read DWD modeling worker: %v", err)
	}
	modeling := string(modelingRaw)
	for _, fragment := range []string{
		"finishWaitingForPublishedDIMs",
		"SET status='PARTIAL'",
		"error_code='DIM_PUBLICATION_REQUIRED'",
	} {
		if !strings.Contains(modeling, fragment) {
			t.Errorf("DIM publication wait completion is missing %q", fragment)
		}
	}
}

func TestManualModelingClickCreatesANewRunWhileRetryKeepsTheOldRun(t *testing.T) {
	raw, err := os.ReadFile(
		"../../migrations/000114_manual_modeling_run_identity.up.sql",
	)
	if err != nil {
		t.Fatalf("read manual modeling run identity migration: %v", err)
	}
	migration := string(raw)
	for _, fragment := range []string{
		"DROP CONSTRAINT dwd_modeling_jobs_version_key",
		"CREATE UNIQUE INDEX dwd_modeling_jobs_active_version_uidx",
		"WHERE status IN ('PENDING','RUNNING')",
		"CREATE OR REPLACE FUNCTION platform.trigger_manual_dwd_modeling",
		"WHERE NOT EXISTS(",
		"ON CONFLICT DO NOTHING",
		"INSERT INTO platform.dwd_modeling_stage_jobs",
		"每次人工点击创建新批次，任务重试只恢复原批次",
	} {
		if !strings.Contains(migration, fragment) {
			t.Errorf("manual modeling run identity migration is missing %q", fragment)
		}
	}
	manualFunctionAt := strings.Index(
		migration,
		"CREATE OR REPLACE FUNCTION platform.trigger_manual_dwd_modeling",
	)
	automaticFunctionAt := strings.Index(
		migration,
		"CREATE OR REPLACE FUNCTION platform.enqueue_ods_dwd_modeling",
	)
	if manualFunctionAt < 0 || automaticFunctionAt <= manualFunctionAt {
		t.Fatal("manual and automatic modeling function boundaries are invalid")
	}
	manualFunction := migration[manualFunctionAt:automaticFunctionAt]
	if strings.Contains(manualFunction, "DO UPDATE") {
		t.Fatal("manual modeling click must not reset an existing workflow")
	}
}

func TestManualDimensionAndFactModelingHaveIndependentGates(t *testing.T) {
	raw, err := os.ReadFile(
		"../../migrations/000119_split_manual_dim_and_dwd_modeling.up.sql",
	)
	if err != nil {
		t.Fatalf("read split manual modeling migration: %v", err)
	}
	migration := string(raw)
	for _, fragment := range []string{
		"ADD COLUMN manual_enabled boolean NOT NULL DEFAULT true",
		"CREATE OR REPLACE FUNCTION platform.trigger_manual_dim_modeling",
		"('FACT_MODELING',3,'warehouse-fact-design-v3',false)",
		"CREATE FUNCTION platform.trigger_manual_dwd_modeling",
		"AND NOT fact.manual_enabled",
		"SET manual_enabled=true",
		"'DIM_PUBLICATION_REQUIRED'",
		"'DIM_MODELING_REQUIRED'",
		"(SELECT count(*) FROM normalized_scopes)",
	} {
		if !strings.Contains(migration, fragment) {
			t.Errorf("split manual modeling migration is missing %q", fragment)
		}
	}

	workerRaw, err := os.ReadFile("dwd_modeling.go")
	if err != nil {
		t.Fatalf("read DWD modeling worker: %v", err)
	}
	worker := string(workerRaw)
	for _, fragment := range []string{
		"AND queued.manual_enabled",
		"AND manual_enabled",
		"dwdStageOrder(claim.Stage)",
	} {
		if !strings.Contains(worker, fragment) {
			t.Errorf("manual stage gate is missing %q", fragment)
		}
	}
}

func TestPublicationDependenciesAndStaleApprovalMigration(t *testing.T) {
	raw, err := os.ReadFile(
		"../../migrations/000115_dataset_publication_dependency_and_stale_approval.up.sql",
	)
	if err != nil {
		t.Fatalf("read publication dependency migration: %v", err)
	}
	migration := string(raw)
	for _, fragment := range []string{
		"'PENDING','APPROVED','REJECTED','CANCELLED'",
		"CREATE OR REPLACE FUNCTION\n  platform.cancel_stale_dataset_publication_requests",
		"AFTER UPDATE OF record_version,schema_hash,plan_hash",
		"request.expected_draft_record_version<>NEW.record_version",
		"status='CANCELLED'",
		"'AUTO_CANCEL'",
		"ADD COLUMN error_message",
		"'WAITING_ACTIVE_DWD_MATERIALIZATION'",
		"CREATE OR REPLACE FUNCTION platform.normalize_dws_modeling_error_message",
	} {
		if !strings.Contains(migration, fragment) {
			t.Errorf("publication dependency migration is missing %q", fragment)
		}
	}

	storeRaw, err := os.ReadFile("publication_approval_postgres.go")
	if err != nil {
		t.Fatalf("read publication approval store: %v", err)
	}
	store := string(storeRaw)
	for _, fragment := range []string{
		"validateDWDPublicationDependenciesTx",
		"dependency.source_type='DATASET_VERSION'",
		"dimension_version.layer='DIM'",
		"DIM_PUBLICATION_REQUIRED",
	} {
		if !strings.Contains(store, fragment) {
			t.Errorf("DWD publication dependency gate is missing %q", fragment)
		}
	}
}

func TestGroupedDWSAndManualAssetDiscoveryMigration(t *testing.T) {
	raw, err := os.ReadFile(
		"../../migrations/000116_grouped_dws_and_manual_asset_discovery.up.sql",
	)
	if err != nil {
		t.Fatalf("read grouped DWS migration: %v", err)
	}
	migration := string(raw)
	for _, fragment := range []string{
		"ADD COLUMN group_key",
		"ADD COLUMN source_scope",
		"UNIQUE(tenant_id,group_key,scope_hash)",
		"'MULTI_FACT_COMPARISON'",
		"array_agg(",
		"public.digest(",
		"jsonb_agg(jsonb_build_object(",
		"WHERE asset.layer='DWD'",
		"WHERE dimension.layer='DIM'",
		"'dws-group-planning-v2'",
		"DROP TRIGGER IF EXISTS dataset_versions_enqueue_dws_metric_discovery",
		"DROP TRIGGER IF EXISTS dataset_versions_enqueue_dimension_survey",
		"旧版逐表主题建模任务已由主题分组规划取代",
	} {
		if !strings.Contains(migration, fragment) {
			t.Errorf("grouped DWS migration is missing %q", fragment)
		}
	}
}

func TestDWDMaterializationWaitRecoveryMigration(t *testing.T) {
	raw, err := os.ReadFile(
		"../../migrations/000117_dwd_materialization_wait_recovery.up.sql",
	)
	if err != nil {
		t.Fatalf("read DWD materialization recovery migration: %v", err)
	}
	migration := string(raw)
	for _, fragment := range []string{
		"OLD.error_code='TRUSTED_PLAN_INVALID'",
		"NEW.status='QUEUED'",
		"pg_get_userbyid(relation.relowner)=current_user",
		"UPDATE platform.build_node_runs AS node",
		"input.input_layer='DIM'",
		"WHEN 'DATASET_BUILD' THEN",
		"NOT EXISTS(",
		"FROM platform.dataset_materializations AS materialization",
		"'RETRY_BACKGROUND_TASK'",
		"GRANT EXECUTE ON FUNCTION platform.retry_background_task",
	} {
		if !strings.Contains(migration, fragment) {
			t.Errorf("DWD materialization recovery migration is missing %q", fragment)
		}
	}
	if strings.Contains(migration, "GRANT UPDATE ON") {
		t.Fatal("recovery must not grant broad build-run UPDATE privileges")
	}
}

func TestDimensionOnlyFactStageCompletionMigration(t *testing.T) {
	raw, err := os.ReadFile(
		"../../migrations/000118_dimension_only_fact_stage_completion.up.sql",
	)
	if err != nil {
		t.Fatalf("read dimension-only fact-stage migration: %v", err)
	}
	migration := string(raw)
	for _, fragment := range []string{
		"fact_stage.stage='FACT_MODELING'",
		"fact_stage.error_code='DIM_PUBLICATION_REQUIRED'",
		"classification_stage.result_json#>>'{classificationSummary,factTableCount}'='0'",
		"SET status='SUCCEEDED'",
		"result_json=dimension_stage.result_json",
		"workflow.status='PARTIAL'",
	} {
		if !strings.Contains(migration, fragment) {
			t.Errorf("dimension-only recovery migration is missing %q", fragment)
		}
	}
}
