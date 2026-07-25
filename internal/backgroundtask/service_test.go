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
	return store.task, nil
}

func (store *serviceStore) Cancel(context.Context, string, string, string, string) error {
	if store.cancelErr != nil {
		return store.cancelErr
	}
	store.cancelled = true
	return nil
}

func TestServiceCancelReturnsNormalizedCancelledTask(t *testing.T) {
	store := &serviceStore{task: Task{
		ID: "job-1", Kind: "DWD_MODELING", Name: "订单明细",
		SourceStatus: "RUNNING", ResourceType: "DATASET", ResourceID: "dataset-1",
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}}
	decorate(&store.task)
	result, err := NewService(store).Cancel(
		context.Background(), "tenant-1", "user-1", "DWD_MODELING", "job-1",
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
		{ID: "job-1", Kind: "DWD_MODELING", SourceStatus: "SUCCEEDED"},
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

	unknown := Task{Kind: "DWD_MODELING", SourceStatus: "RUNNING"}
	decorate(&unknown)
	if unknown.ProgressPercent != nil ||
		unknown.ProgressText != "正在执行，暂时无法估算剩余时间" {
		t.Fatalf("unknown=%+v", unknown)
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
