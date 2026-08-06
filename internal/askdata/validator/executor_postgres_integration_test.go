package validator

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"intelligent-report-generation-system/internal/askdata/compiler"
	"intelligent-report-generation-system/internal/platform/database"
	"intelligent-report-generation-system/internal/queryruntime"
)

type acceptingMaterializationRevalidator struct {
	mu    sync.Mutex
	calls [][]queryruntime.SemanticMaterialization
}

func (revalidator *acceptingMaterializationRevalidator) RevalidateSemanticMaterializations(
	_ context.Context,
	_ string,
	materializations []queryruntime.SemanticMaterialization,
) error {
	revalidator.mu.Lock()
	defer revalidator.mu.Unlock()
	revalidator.calls = append(revalidator.calls,
		append([]queryruntime.SemanticMaterialization(nil), materializations...))
	return nil
}

func TestPostgresExecutorUsesReaderRoleReadOnlySnapshotAndSupportsCancellation(t *testing.T) {
	adminURL := os.Getenv("ASKDATA_INTEGRATION_WAREHOUSE_ADMIN_DATABASE_URL")
	readerURL := os.Getenv("ASKDATA_INTEGRATION_WAREHOUSE_DATABASE_URL")
	if adminURL == "" || readerURL == "" {
		t.Skip("set AskData warehouse integration database URLs")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	admin, err := database.Open(ctx, adminURL)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	reader, err := database.Open(ctx, readerURL)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	readerConfig, err := pgx.ParseConfig(readerURL)
	if err != nil {
		t.Fatal(err)
	}
	suffix := strings.ReplaceAll(uuid.NewString(), "-", "")[:16]
	relation := "askdata_exec_" + suffix
	base := pgx.Identifier{"warehouse_dws", relation}.Sanitize()
	view := pgx.Identifier{"warehouse_published", relation}.Sanitize()
	readerRole := pgx.Identifier{readerConfig.User}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE TABLE "+base+` (net_sales numeric(38,10) NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = admin.Exec(context.Background(), "DROP VIEW IF EXISTS "+view)
		_, _ = admin.Exec(context.Background(), "DROP TABLE IF EXISTS "+base)
	}()
	if _, err := admin.Exec(ctx, "INSERT INTO "+base+`(net_sales) VALUES(10.25),(20.50)`); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, "CREATE VIEW "+view+" AS SELECT net_sales FROM "+base); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, "GRANT SELECT ON "+view+" TO "+readerRole); err != nil {
		t.Fatal(err)
	}
	if _, err := reader.Exec(ctx, "INSERT INTO "+view+`(net_sales) VALUES(999)`); err == nil {
		t.Fatal("warehouse reader unexpectedly had write privilege on the published view")
	}

	artifact, accessContext := liveQueryArtifactForSource(t, relation)
	validator, err := NewValidator(NewPostgresExplainer(reader), DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	validation, err := validator.Validate(accessContext, artifact)
	if err != nil {
		t.Fatal(err)
	}
	revalidator := &acceptingMaterializationRevalidator{}
	audit := &recordingSemanticAudit{}
	executor, err := NewExecutor(reader, revalidator, audit)
	if err != nil {
		t.Fatal(err)
	}
	request := ExecutionRequest{RunID: uuid.NewString(), Query: artifact, Validation: validation}
	result, err := executor.Execute(accessContext, request)
	if err != nil {
		t.Fatal(err)
	}
	rows, ok := result.Rows(compiler.QueryRoleCurrent)
	if !ok || len(rows) != 1 || len(rows[0]) != 1 || rows[0][0] != "30.7500000000" {
		t.Fatalf("unexpected exact warehouse result: %#v", rows)
	}
	revalidator.mu.Lock()
	revalidationCalls := len(revalidator.calls)
	revalidator.mu.Unlock()
	if revalidationCalls != 1 {
		t.Fatalf("materialization revalidation calls = %d", revalidationCalls)
	}
	starts, completions := audit.snapshot()
	auditJSON, err := json.Marshal(struct {
		Starts      []queryruntime.SemanticQuestionRun
		Completions []queryruntime.SemanticQuestionCompletion
	}{starts, completions})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(auditJSON), "10.25") || strings.Contains(string(auditJSON), "20.50") ||
		strings.Contains(string(auditJSON), "30.75") {
		t.Fatalf("ordinary audit leaked result values: %s", auditJSON)
	}

	lock, err := admin.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Rollback(context.Background()) //nolint:errcheck
	if _, err := lock.Exec(ctx, "LOCK TABLE "+base+" IN ACCESS EXCLUSIVE MODE"); err != nil {
		t.Fatal(err)
	}
	cancelAudit := &recordingSemanticAudit{started: make(chan struct{})}
	cancelExecutor, err := NewExecutor(reader, revalidator, cancelAudit)
	if err != nil {
		t.Fatal(err)
	}
	cancelRequest := ExecutionRequest{RunID: uuid.NewString(), Query: artifact, Validation: validation}
	done := make(chan error, 1)
	go func() {
		_, executeErr := cancelExecutor.Execute(accessContext, cancelRequest)
		done <- executeErr
	}()
	select {
	case <-cancelAudit.started:
	case <-time.After(2 * time.Second):
		t.Fatal("warehouse execution did not enter audited running state")
	}
	time.Sleep(100 * time.Millisecond)
	if cancelled, err := cancelExecutor.Cancel(accessContext, cancelRequest.RunID); err != nil || !cancelled {
		t.Fatalf("warehouse cancellation = %v, %v", cancelled, err)
	}
	select {
	case err := <-done:
		if !errors.Is(err, ErrExecutionCanceled) {
			t.Fatalf("canceled warehouse execution error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("PostgreSQL query did not honor context cancellation")
	}
	_, cancelCompletions := cancelAudit.snapshot()
	if len(cancelCompletions) != 1 || cancelCompletions[0].Status != queryruntime.SemanticQuestionCanceled {
		t.Fatalf("unexpected warehouse cancellation audit: %#v", cancelCompletions)
	}
}
