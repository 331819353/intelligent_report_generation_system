package validator

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"math/big"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/compiler"
	"intelligent-report-generation-system/internal/platform/database"
	"intelligent-report-generation-system/internal/queryruntime"
)

type fakeExecutionRunner struct {
	run func(context.Context, compiler.QueryArtifact, ValidationArtifact, ExecutorOptions) ([]executedPlan, error)
}

func (runner *fakeExecutionRunner) Run(
	ctx context.Context,
	artifact compiler.QueryArtifact,
	validation ValidationArtifact,
	options ExecutorOptions,
) ([]executedPlan, error) {
	return runner.run(ctx, artifact, validation, options)
}

type recordingSemanticAudit struct {
	mu          sync.Mutex
	starts      []queryruntime.SemanticQuestionRun
	completions []queryruntime.SemanticQuestionCompletion
	started     chan struct{}
	startOnce   sync.Once
	startErr    error
	finishErr   error
}

func (audit *recordingSemanticAudit) StartSemanticQuestion(
	_ context.Context,
	run queryruntime.SemanticQuestionRun,
) error {
	audit.mu.Lock()
	audit.starts = append(audit.starts, run)
	audit.mu.Unlock()
	if audit.started != nil {
		audit.startOnce.Do(func() { close(audit.started) })
	}
	return audit.startErr
}

func (audit *recordingSemanticAudit) FinishSemanticQuestion(
	_ context.Context,
	completion queryruntime.SemanticQuestionCompletion,
) error {
	audit.mu.Lock()
	audit.completions = append(audit.completions, completion)
	audit.mu.Unlock()
	return audit.finishErr
}

func (audit *recordingSemanticAudit) snapshot() (
	[]queryruntime.SemanticQuestionRun,
	[]queryruntime.SemanticQuestionCompletion,
) {
	audit.mu.Lock()
	defer audit.mu.Unlock()
	return append([]queryruntime.SemanticQuestionRun(nil), audit.starts...),
		append([]queryruntime.SemanticQuestionCompletion(nil), audit.completions...)
}

func TestExecutorReturnsRowsInProcessAndAuditsOnlyHashes(t *testing.T) {
	request, ctx := validatedExecutionRequest(t)
	secret := "sensitive-result-value"
	runner := &fakeExecutionRunner{run: func(
		context.Context, compiler.QueryArtifact, ValidationArtifact, ExecutorOptions,
	) ([]executedPlan, error) {
		return []executedPlan{testExecutedPlan(t, compiler.QueryRoleCurrent,
			[]ExecutionColumn{{Name: "net_sales", DataTypeOID: pgtype.TextOID}}, [][]any{{secret}})}, nil
	}}
	audit := &recordingSemanticAudit{}
	executor, err := newExecutorWithRunner(runner, audit, DefaultExecutorOptions())
	if err != nil {
		t.Fatal(err)
	}
	result, err := executor.Execute(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if err := result.Artifact.Validate(); err != nil {
		t.Fatal(err)
	}
	rows, ok := result.Rows(compiler.QueryRoleCurrent)
	if !ok || !reflect.DeepEqual(rows, [][]any{{secret}}) {
		t.Fatalf("unexpected live rows: %#v", rows)
	}
	rows[0][0] = "changed"
	rowsAgain, _ := result.Rows(compiler.QueryRoleCurrent)
	if rowsAgain[0][0] != secret {
		t.Fatal("Rows returned a mutable internal slice")
	}
	artifactJSON, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	starts, completions := audit.snapshot()
	auditJSON, err := json.Marshal(struct {
		Starts      []queryruntime.SemanticQuestionRun
		Completions []queryruntime.SemanticQuestionCompletion
	}{starts, completions})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(artifactJSON), secret) || strings.Contains(string(auditJSON), secret) ||
		strings.Contains(string(auditJSON), `"sql"`) || strings.Contains(string(auditJSON), `"args"`) {
		t.Fatalf("execution artifact or audit leaked live data: artifact=%s audit=%s", artifactJSON, auditJSON)
	}
	if len(starts) != 1 || starts[0].RunType != queryruntime.RunTypeSemanticQuestion ||
		starts[0].QueryPlanHash != string(request.Query.PlanHash) ||
		starts[0].ValidationHash != string(request.Validation.ValidationHash) ||
		len(completions) != 1 || completions[0].Status != queryruntime.SemanticQuestionSucceeded ||
		completions[0].ResultHash != string(result.Artifact.ResultHash) {
		t.Fatalf("unexpected semantic audit: starts=%#v completions=%#v", starts, completions)
	}

	second := request
	second.RunID = uuid.NewString()
	secondResult, err := executor.Execute(ctx, second)
	if err != nil {
		t.Fatal(err)
	}
	if secondResult.Artifact.ResultHash != result.Artifact.ResultHash {
		t.Fatal("result hash changed only because run ID changed")
	}
}

func TestExecutorRequiresMatchingLiveValidatedPlan(t *testing.T) {
	request, ctx := validatedExecutionRequest(t)
	calls := 0
	runner := &fakeExecutionRunner{run: func(
		context.Context, compiler.QueryArtifact, ValidationArtifact, ExecutorOptions,
	) ([]executedPlan, error) {
		calls++
		return nil, nil
	}}
	audit := &recordingSemanticAudit{}
	executor, err := newExecutorWithRunner(runner, audit, DefaultExecutorOptions())
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(request.Query)
	if err != nil {
		t.Fatal(err)
	}
	var replayed compiler.QueryArtifact
	if err := askdata.DecodeStrictJSON(raw, &replayed); err != nil {
		t.Fatal(err)
	}
	request.Query = replayed
	if _, err := executor.Execute(ctx, request); !errors.Is(err, ErrInvalidExecution) {
		t.Fatalf("replayed plan error = %v", err)
	}
	if calls != 0 {
		t.Fatal("executor ran a serialized plan without live parameters")
	}
	starts, _ := audit.snapshot()
	if len(starts) != 0 {
		t.Fatal("invalid execution reached ordinary audit")
	}
}

func TestExecutorDiscardsSuccessfulRowsWhenCompletionAuditFails(t *testing.T) {
	request, ctx := validatedExecutionRequest(t)
	runner := &fakeExecutionRunner{run: func(
		context.Context, compiler.QueryArtifact, ValidationArtifact, ExecutorOptions,
	) ([]executedPlan, error) {
		return []executedPlan{testExecutedPlan(t, compiler.QueryRoleCurrent,
			[]ExecutionColumn{{Name: "net_sales", DataTypeOID: pgtype.TextOID}}, [][]any{{"must-not-return"}})}, nil
	}}
	audit := &recordingSemanticAudit{finishErr: errors.New("audit unavailable")}
	executor, err := newExecutorWithRunner(runner, audit, DefaultExecutorOptions())
	if err != nil {
		t.Fatal(err)
	}
	result, err := executor.Execute(ctx, request)
	if !errors.Is(err, ErrExecutionAuditFailure) || result.Artifact.ResultHash != "" {
		t.Fatalf("audit failure returned result=%#v error=%v", result, err)
	}
}

func TestExecutorRejectsRowsBeyondValidatedMaximum(t *testing.T) {
	request, ctx := validatedExecutionRequest(t)
	rows := make([][]any, request.Validation.Plans[0].MaxRows+1)
	for index := range rows {
		rows[index] = []any{int64(index)}
	}
	runner := &fakeExecutionRunner{run: func(
		context.Context, compiler.QueryArtifact, ValidationArtifact, ExecutorOptions,
	) ([]executedPlan, error) {
		return []executedPlan{testExecutedPlan(t, compiler.QueryRoleCurrent,
			[]ExecutionColumn{{Name: "net_sales", DataTypeOID: pgtype.Int8OID}}, rows)}, nil
	}}
	audit := &recordingSemanticAudit{}
	executor, err := newExecutorWithRunner(runner, audit, DefaultExecutorOptions())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := executor.Execute(ctx, request); !errors.Is(err, ErrExecutionRejected) {
		t.Fatalf("over-limit result error = %v", err)
	}
	_, completions := audit.snapshot()
	if len(completions) != 1 || completions[0].Status != queryruntime.SemanticQuestionFailed ||
		completions[0].ErrorCode != "INVALID_QUERY_RESULT" || completions[0].RowCount != 0 {
		t.Fatalf("unexpected failed audit: %#v", completions)
	}
}

func TestExecutorCancelIsActorAndDomainBound(t *testing.T) {
	request, ctx := validatedExecutionRequest(t)
	running := make(chan struct{})
	runner := &fakeExecutionRunner{run: func(
		ctx context.Context, _ compiler.QueryArtifact, _ ValidationArtifact, _ ExecutorOptions,
	) ([]executedPlan, error) {
		close(running)
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	audit := &recordingSemanticAudit{started: make(chan struct{})}
	executor, err := newExecutorWithRunner(runner, audit, DefaultExecutorOptions())
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, executeErr := executor.Execute(ctx, request)
		done <- executeErr
	}()
	select {
	case <-running:
	case <-time.After(time.Second):
		t.Fatal("execution did not start")
	}
	wrong := database.WithAccessContext(context.Background(), "other-actor", string(request.Query.DomainID))
	if cancelled, err := executor.Cancel(wrong, request.RunID); err != nil || cancelled {
		t.Fatalf("cross-actor cancellation = %v, %v", cancelled, err)
	}
	if cancelled, err := executor.Cancel(ctx, request.RunID); err != nil || !cancelled {
		t.Fatalf("owner cancellation = %v, %v", cancelled, err)
	}
	select {
	case err := <-done:
		if !errors.Is(err, ErrExecutionCanceled) {
			t.Fatalf("canceled execution error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled execution did not stop")
	}
	_, completions := audit.snapshot()
	if len(completions) != 1 || completions[0].Status != queryruntime.SemanticQuestionCanceled ||
		completions[0].ErrorCode != "QUERY_CANCELED" {
		t.Fatalf("unexpected cancellation audit: %#v", completions)
	}
}

func TestExecutorHonorsCallerDeadlineAndAuditsTimeout(t *testing.T) {
	request, parent := validatedExecutionRequest(t)
	ctx, cancel := context.WithTimeout(parent, 20*time.Millisecond)
	defer cancel()
	runner := &fakeExecutionRunner{run: func(
		ctx context.Context, _ compiler.QueryArtifact, _ ValidationArtifact, _ ExecutorOptions,
	) ([]executedPlan, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	audit := &recordingSemanticAudit{}
	executor, err := newExecutorWithRunner(runner, audit, DefaultExecutorOptions())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := executor.Execute(ctx, request); !errors.Is(err, ErrExecutionTimeout) {
		t.Fatalf("deadline execution error = %v", err)
	}
	_, completions := audit.snapshot()
	if len(completions) != 1 || completions[0].Status != queryruntime.SemanticQuestionTimeout ||
		completions[0].ErrorCode != "QUERY_TIMEOUT" {
		t.Fatalf("unexpected timeout audit: %#v", completions)
	}
}

func TestResultNormalizationPreservesExactDecimalAndRejectsNaN(t *testing.T) {
	value, cell, err := normalizeResultValue(pgtype.Numeric{Int: big.NewInt(1234), Exp: -2, Valid: true}, pgtype.NumericOID)
	if err != nil || value != "12.34" || cell != (canonicalCell{Kind: "DECIMAL", Value: "12.34"}) {
		t.Fatalf("numeric normalization = %#v %#v %v", value, cell, err)
	}
	if _, _, err := normalizeResultValue(math.NaN(), pgtype.Float8OID); err == nil {
		t.Fatal("NaN result was accepted")
	}
}

func validatedExecutionRequest(t *testing.T) (ExecutionRequest, context.Context) {
	t.Helper()
	artifact, ctx := liveQueryArtifact(t)
	validator, err := NewValidator(&recordingExplainer{raw: safeExplainJSON()}, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	validation, err := validator.Validate(ctx, artifact)
	if err != nil {
		t.Fatal(err)
	}
	return ExecutionRequest{RunID: uuid.NewString(), Query: artifact, Validation: validation}, ctx
}

func testExecutedPlan(
	t *testing.T,
	role compiler.QueryRole,
	columns []ExecutionColumn,
	rows [][]any,
) executedPlan {
	t.Helper()
	result := executedPlan{
		role: role, columns: append([]ExecutionColumn(nil), columns...), rows: cloneResultRows(rows),
		canonicalRows: make([][]canonicalCell, len(rows)),
	}
	for rowIndex, row := range rows {
		if len(row) != len(columns) {
			t.Fatal("test result row width mismatch")
		}
		result.canonicalRows[rowIndex] = make([]canonicalCell, len(row))
		for columnIndex, value := range row {
			normalized, canonical, err := normalizeResultValue(value, columns[columnIndex].DataTypeOID)
			if err != nil {
				t.Fatal(err)
			}
			result.rows[rowIndex][columnIndex] = normalized
			result.canonicalRows[rowIndex][columnIndex] = canonical
		}
	}
	return result
}
