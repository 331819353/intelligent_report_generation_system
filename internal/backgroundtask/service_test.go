package backgroundtask

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

type serviceStore struct {
	task      Task
	page      Page
	cancelErr error
	cancelled bool
	retryErr  error
	retried   bool
}

func (store *serviceStore) List(context.Context, string, string, int) (Page, error) {
	return store.page, nil
}

func (store *serviceStore) Find(context.Context, string, string, string) (Task, error) {
	if store.task.ID == "" {
		return Task{}, ErrNotFound
	}
	if store.cancelled {
		store.task.SourceStatus = "SKIPPED"
		store.task.ErrorCode = "USER_CANCELLED"
		decorate(&store.task)
	}
	if store.retried {
		store.task.SourceStatus = "PENDING"
		store.task.ErrorCode = ""
		store.task.ErrorMessage = ""
		store.task.Attempt = 0
		decorate(&store.task)
	}
	return store.task, nil
}

func (store *serviceStore) Cancel(context.Context, string, string, string, string) error {
	if store.cancelErr != nil {
		return store.cancelErr
	}
	store.cancelled = true
	return nil
}

func (store *serviceStore) Retry(context.Context, string, string, string, string) error {
	if store.retryErr != nil {
		return store.retryErr
	}
	store.retried = true
	return nil
}

func TestServiceCancelReturnsNormalizedCancelledTask(t *testing.T) {
	store := &serviceStore{task: Task{
		ID: "job-1", Kind: "DWD_FACT_MODELING", Name: "订单明细",
		SourceStatus: "RUNNING", ResourceType: "DATASET", ResourceID: "dataset-1",
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}}
	decorate(&store.task)
	result, err := NewService(store).Cancel(
		context.Background(), "tenant-1", "user-1", "DWD_FACT_MODELING", "job-1",
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "CANCELLED" || result.CanCancel {
		t.Fatalf("result=%+v", result)
	}
}

func TestServiceRejectsTerminalOrUnsupportedCancellation(t *testing.T) {
	tests := []Task{
		{ID: "job-1", Kind: "DWD_FACT_MODELING", SourceStatus: "SUCCEEDED"},
		{ID: "job-2", Kind: "UNKNOWN_JOB", SourceStatus: "RUNNING"},
	}
	for _, task := range tests {
		task.CreatedAt, task.UpdatedAt = time.Now(), time.Now()
		decorate(&task)
		_, err := NewService(&serviceStore{task: task}).Cancel(
			context.Background(), "tenant-1", "user-1", task.Kind, task.ID,
		)
		if !errors.Is(err, ErrNotCancellable) {
			t.Fatalf("task=%+v err=%v", task, err)
		}
	}
}

func TestServiceRetryReturnsQueuedModelingTask(t *testing.T) {
	store := &serviceStore{task: Task{
		ID: "job-1", Kind: "DIM_MODELING", Name: "订单明细",
		SourceStatus: "FAILED", ResourceType: "DATASET", ResourceID: "dataset-1",
		ErrorCode: "WAREHOUSE_MODELING_INVALID_OUTPUT",
		Attempt:   3, MaxAttempts: 3,
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}}
	decorate(&store.task)
	if !store.task.CanRetry {
		t.Fatalf("failed modeling task is not retryable: %+v", store.task)
	}
	result, err := NewService(store).Retry(
		context.Background(), "tenant-1", "user-1", "DIM_MODELING", "job-1",
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "QUEUED" || result.CanRetry || result.Attempt != 0 {
		t.Fatalf("result=%+v", result)
	}
}

func TestTrustedPlanDatasetBuildCanRetryButOtherBuildFailuresCannot(t *testing.T) {
	retryable := Task{
		ID: "build-1", Kind: "DATASET_BUILD", SourceStatus: "FAILED",
		ErrorCode: "TRUSTED_PLAN_INVALID",
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	decorate(&retryable)
	if !retryable.CanRetry {
		t.Fatalf("trusted-plan build should be safely retryable: %+v", retryable)
	}

	unsafe := Task{
		ID: "build-2", Kind: "DATASET_BUILD", SourceStatus: "FAILED",
		ErrorCode: "QUALITY_GATE_FAILED",
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	decorate(&unsafe)
	if unsafe.CanRetry ||
		unsafe.RetryDisabledReason != "该构建失败无法在原运行上安全重试" {
		t.Fatalf("quality failure must not be retried in place: %+v", unsafe)
	}
}

func TestDecorateTreatsDomainCoalescingAsNormalNonRetryableSkip(t *testing.T) {
	task := Task{
		ID: "job-1", Kind: "DWD_MODELING", SourceStatus: "SKIPPED",
		ErrorCode:    "DOMAIN_PLAN_COALESCED",
		ErrorMessage: "同领域 ODS 已由一次 LLM 方案统一分析",
	}
	decorate(&task)
	if task.CanRetry || task.ErrorCode != "" || task.ErrorMessage != "" ||
		task.ProgressText != "已合并到同领域代表任务" {
		t.Fatalf("coalesced task=%+v", task)
	}
}

func TestDecorateUsesMeasuredProgressAndDoesNotInventRunningPercentage(t *testing.T) {
	processed, total := int64(4), int64(10)
	measured := Task{
		Kind: "DATA_SOURCE_METADATA", SourceStatus: "RUNNING",
		Processed: &processed, Total: &total,
	}
	decorate(&measured)
	if measured.ProgressPercent == nil || *measured.ProgressPercent != 40 ||
		measured.ProgressText != "已处理 4 / 10" {
		t.Fatalf("measured=%+v", measured)
	}

	unknown := Task{Kind: "DWD_FACT_MODELING", SourceStatus: "RUNNING"}
	decorate(&unknown)
	if unknown.ProgressPercent != nil ||
		unknown.ProgressText != "正在执行，暂时无法估算剩余时间" {
		t.Fatalf("unknown=%+v", unknown)
	}
}

func TestDecorateShowsPublishedDimensionWaitAsQueueProgress(t *testing.T) {
	task := Task{
		Kind:         "DWD_FACT_MODELING",
		SourceStatus: "PENDING",
		ErrorCode:    "WAITING_DIM_PUBLICATION",
		ErrorMessage: "等待 2 张 DIM 完成发布；发布后事实落地任务会自动继续",
		Attempt:      0,
		MaxAttempts:  3,
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}
	decorate(&task)
	if task.Status != "QUEUED" || !task.CanCancel ||
		task.ErrorCode != "" || task.ErrorMessage != "" ||
		task.ProgressText != "等待 2 张 DIM 完成发布；发布后事实落地任务会自动继续" {
		t.Fatalf("waiting task=%+v", task)
	}
}

func TestDecorateShowsDWDMaterializationWaitAsQueueProgress(t *testing.T) {
	task := Task{
		Kind:         "DWS_MODELING",
		SourceStatus: "WAITING_DEPENDENCY",
		ErrorCode:    "WAITING_ACTIVE_DWD_MATERIALIZATION",
		ErrorMessage: "等待全部 DWD 发布版本完成物化；物化转为可用后，主题建模会自动继续",
		Attempt:      0,
		MaxAttempts:  5,
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}
	decorate(&task)
	if task.Status != "QUEUED" ||
		task.ErrorCode != "" || task.ErrorMessage != "" ||
		task.ProgressText != "等待全部 DWD 发布版本完成物化；物化转为可用后，主题建模会自动继续" {
		t.Fatalf("waiting task=%+v", task)
	}
}

func TestCancellationMigrationUsesTenantFencedFunctionInsteadOfTableGrants(t *testing.T) {
	raw, err := os.ReadFile("../../migrations/000081_background_task_cancellation.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	migration := string(raw)
	for _, fragment := range []string{
		"CREATE OR REPLACE FUNCTION platform.cancel_background_task(",
		"SECURITY DEFINER",
		"selected_tenant_id uuid := platform.current_tenant_id()",
		"AND id=selected_task_id",
		"'CANCEL_BACKGROUND_TASK'",
		"REVOKE ALL ON FUNCTION platform.cancel_background_task",
		"GRANT EXECUTE ON FUNCTION platform.cancel_background_task",
		"TO report_app",
	} {
		if !strings.Contains(migration, fragment) {
			t.Errorf("migration is missing %q", fragment)
		}
	}
	if strings.Contains(migration, "GRANT UPDATE ON") {
		t.Fatal("API role received broad worker-table UPDATE privileges")
	}
}

func TestRetryAndDomainDedupMigrationUsesTenantFencedEntrypoints(t *testing.T) {
	raw, err := os.ReadFile(
		"../../migrations/000107_background_task_retry_and_domain_dedup.up.sql",
	)
	if err != nil {
		t.Fatal(err)
	}
	migration := string(raw)
	for _, fragment := range []string{
		"PARTITION BY published.domain_key",
		"WHERE domain_rank=1",
		"CREATE OR REPLACE FUNCTION platform.retry_background_task(",
		"SECURITY DEFINER",
		"job.error_code<>'DOMAIN_PLAN_COALESCED'",
		"'RETRY_BACKGROUND_TASK'",
		"REVOKE ALL ON FUNCTION platform.retry_background_task",
		"GRANT EXECUTE ON FUNCTION platform.retry_background_task",
	} {
		if !strings.Contains(migration, fragment) {
			t.Errorf("migration is missing %q", fragment)
		}
	}
	if strings.Contains(migration, "GRANT UPDATE ON") {
		t.Fatal("API role received broad worker-table UPDATE privileges")
	}
}

func TestWarehouseStageTasksUseDomainPurposeInsteadOfTriggerDatasetAsName(t *testing.T) {
	for _, fragment := range []string{
		"WHEN 'DOMAIN_CLASSIFICATION' THEN '领域结构分析'",
		"WHEN 'DIMENSION_MODELING' THEN '领域维度设计'",
		"ELSE '领域事实落地'",
		"'触发源：',dataset.name",
		"classification.stage='DOMAIN_CLASSIFICATION'",
		"' · 规划事实'",
	} {
		if !strings.Contains(taskUnionSQL, fragment) {
			t.Errorf("warehouse stage task projection is missing %q", fragment)
		}
	}
	if strings.Contains(
		taskUnionSQL,
		"stage_job.id::text,dataset.name::text,\n    concat(\n      dataset.code",
	) {
		t.Fatal("warehouse stage task still exposes its trigger ODS as the task name")
	}
}

func TestWarehouseStageMigrationUsesThreeTenantFencedTasks(t *testing.T) {
	raw, err := os.ReadFile(
		"../../migrations/000109_split_warehouse_modeling_stage_tasks.up.sql",
	)
	if err != nil {
		t.Fatal(err)
	}
	migration := string(raw)
	for _, fragment := range []string{
		"CREATE TABLE platform.dwd_modeling_stage_jobs(",
		"'DOMAIN_CLASSIFICATION',1,'warehouse-classification-v2'",
		"'DIMENSION_MODELING',2,'warehouse-dimension-design-v2'",
		"'FACT_MODELING',3,'warehouse-fact-design-v3'",
		"UNIQUE(tenant_id,workflow_job_id,stage)",
		"CREATE OR REPLACE FUNCTION platform.cancel_dwd_modeling_stage_task(",
		"CREATE OR REPLACE FUNCTION platform.retry_dwd_modeling_stage_task(",
		"SECURITY DEFINER",
		"selected_tenant_id uuid := platform.current_tenant_id()",
		"AND id<>selected_task_id",
		"OR (stage_order<selected_order AND status<>'SUCCEEDED')",
		"'RETRY_BACKGROUND_TASK'",
		"GRANT EXECUTE ON FUNCTION",
	} {
		if !strings.Contains(migration, fragment) {
			t.Errorf("migration is missing %q", fragment)
		}
	}
	if strings.Contains(migration, "GRANT UPDATE ON") {
		t.Fatal("API role received broad worker-table UPDATE privileges")
	}
}
