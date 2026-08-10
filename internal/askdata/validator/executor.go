package validator

import (
	"context"
	"database/sql/driver"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/compiler"
	"intelligent-report-generation-system/internal/askdata/registry"
	"intelligent-report-generation-system/internal/dataset"
	"intelligent-report-generation-system/internal/platform/database"
	"intelligent-report-generation-system/internal/queryruntime"
)

const ExecutionVersion = "semantic-query-execution-v1"

var (
	ErrInvalidExecutor       = errors.New("semantic query executor is invalid")
	ErrInvalidExecution      = errors.New("semantic query execution request is invalid")
	ErrExecutionRejected     = errors.New("semantic query execution was rejected")
	ErrExecutionUnavailable  = errors.New("semantic query execution is unavailable")
	ErrExecutionTimeout      = errors.New("semantic query execution timed out")
	ErrExecutionCanceled     = errors.New("semantic query execution was canceled")
	ErrExecutionAuditFailure = errors.New("semantic query execution audit failed")
)

var (
	errMaterializationStale = errors.New("semantic materialization is stale")
	errWarehouseRejected    = errors.New("warehouse execution was rejected")
	errResultInvalid        = errors.New("warehouse result is invalid")
)

type ExecutorOptions struct {
	MaxDurationMS  int `json:"maxDurationMs"`
	MaxResultBytes int `json:"maxResultBytes"`
}

func DefaultExecutorOptions() ExecutorOptions {
	return ExecutorOptions{MaxDurationMS: 25000, MaxResultBytes: 16 << 20}
}

func (options ExecutorOptions) Validate() error {
	if options.MaxDurationMS < 100 || options.MaxDurationMS > 25000 ||
		options.MaxResultBytes < 1024 || options.MaxResultBytes > 64<<20 {
		return ErrInvalidExecutor
	}
	return nil
}

type ExecutionRequest struct {
	RunID      string
	Query      compiler.QueryArtifact
	Validation ValidationArtifact
}

type ExecutionColumn struct {
	Name        string `json:"name"`
	DataTypeOID uint32 `json:"dataTypeOid"`
}

type PlanExecution struct {
	Role             compiler.QueryRole  `json:"role"`
	QueryPlanHash    askdata.ContentHash `json:"queryPlanHash"`
	CompiledPlanHash askdata.ContentHash `json:"compiledPlanHash"`
	MaxRows          int                 `json:"maxRows"`
	Columns          []ExecutionColumn   `json:"columns"`
	RowCount         int                 `json:"rowCount"`
	ResultHash       askdata.ContentHash `json:"resultHash"`
}

// ExecutionArtifact is safe for ordinary audit/artifact storage. Result rows
// are deliberately held only by ExecutionResult's in-process private field.
type ExecutionArtifact struct {
	Version               string              `json:"version"`
	RunID                 string              `json:"runId"`
	Scope                 askdata.PolicyScope `json:"scope"`
	DomainID              askdata.ID          `json:"domainId"`
	QueryArtifactPlanHash askdata.ContentHash `json:"queryArtifactPlanHash"`
	ValidationHash        askdata.ContentHash `json:"validationHash"`
	Plans                 []PlanExecution     `json:"plans"`
	TotalRows             int                 `json:"totalRows"`
	ResultHash            askdata.ContentHash `json:"resultHash"`
}

type executionRows struct {
	role compiler.QueryRole
	rows [][]any
}

type ExecutionResult struct {
	Artifact ExecutionArtifact `json:"artifact"`
	rows     []executionRows
}

// Rows returns an isolated copy for QUERY-006 and the final response builder.
// The values are JSON-safe scalar values; DECIMAL and temporal values retain
// exact text representations instead of being coerced through float64.
func (result ExecutionResult) Rows(role compiler.QueryRole) ([][]any, bool) {
	for _, plan := range result.rows {
		if plan.role == role {
			return cloneResultRows(plan.rows), true
		}
	}
	return nil, false
}

func (artifact ExecutionArtifact) Validate() error {
	parsed, err := uuid.Parse(artifact.RunID)
	if artifact.Version != ExecutionVersion || err != nil || parsed.String() != artifact.RunID ||
		artifact.Scope.Validate() != nil || artifact.DomainID.Validate() != nil ||
		!containsID(artifact.Scope.DomainIDs, artifact.DomainID) ||
		artifact.QueryArtifactPlanHash.Validate() != nil || artifact.ValidationHash.Validate() != nil ||
		artifact.ResultHash.Validate() != nil || len(artifact.Plans) < 1 || len(artifact.Plans) > 2 ||
		artifact.TotalRows < 0 || artifact.TotalRows > 20000 {
		return ErrInvalidExecution
	}
	total := 0
	for index, plan := range artifact.Plans {
		if plan.QueryPlanHash.Validate() != nil || plan.CompiledPlanHash.Validate() != nil ||
			plan.ResultHash.Validate() != nil || plan.MaxRows < 1 || plan.MaxRows > 10000 ||
			plan.RowCount < 0 || plan.RowCount > plan.MaxRows || len(plan.Columns) < 1 || len(plan.Columns) > 1024 ||
			(index == 0 && plan.Role != compiler.QueryRoleCurrent) ||
			(index == 1 && plan.Role != compiler.QueryRoleBaseline) {
			return ErrInvalidExecution
		}
		seen := make(map[string]bool, len(plan.Columns))
		for _, column := range plan.Columns {
			if column.Name == "" || len(column.Name) > 128 || column.DataTypeOID == 0 ||
				!utf8.ValidString(column.Name) || strings.ContainsAny(column.Name, "\x00\r\n") || seen[column.Name] {
				return ErrInvalidExecution
			}
			seen[column.Name] = true
		}
		total += plan.RowCount
	}
	if total != artifact.TotalRows {
		return ErrInvalidExecution
	}
	expected, err := executionResultHash(artifact)
	if err != nil || expected != artifact.ResultHash {
		return ErrInvalidExecution
	}
	return nil
}

type activeExecution struct {
	actorID  string
	domainID string
	cancel   context.CancelFunc
}

type executionRunner interface {
	Run(context.Context, compiler.QueryArtifact, ValidationArtifact, ExecutorOptions) ([]executedPlan, error)
}

type Executor struct {
	runner  executionRunner
	auditor queryruntime.SemanticQuestionAuditStore
	options ExecutorOptions
	mu      sync.Mutex
	active  map[string]activeExecution
}

func NewExecutor(
	warehousePool *pgxpool.Pool,
	revalidator queryruntime.SemanticMaterializationRevalidator,
	auditor queryruntime.SemanticQuestionAuditStore,
	configured ...ExecutorOptions,
) (*Executor, error) {
	if warehousePool == nil || revalidator == nil || auditor == nil || len(configured) > 1 {
		return nil, ErrInvalidExecutor
	}
	options := DefaultExecutorOptions()
	if len(configured) == 1 {
		options = configured[0]
	}
	return newExecutorWithRunner(
		&postgresExecutionRunner{pool: warehousePool, revalidator: revalidator}, auditor, options,
	)
}

func newExecutorWithRunner(
	runner executionRunner,
	auditor queryruntime.SemanticQuestionAuditStore,
	options ExecutorOptions,
) (*Executor, error) {
	if runner == nil || auditor == nil || options.Validate() != nil {
		return nil, ErrInvalidExecutor
	}
	return &Executor{
		runner: runner, auditor: auditor, options: options,
		active: make(map[string]activeExecution),
	}, nil
}

func (executor *Executor) Execute(ctx context.Context, request ExecutionRequest) (ExecutionResult, error) {
	if executor == nil || executor.runner == nil || executor.auditor == nil || executor.options.Validate() != nil {
		return ExecutionResult{}, ErrInvalidExecutor
	}
	if err := ctx.Err(); err != nil {
		return ExecutionResult{}, err
	}
	timeoutMS, maxRows, maxExplainCost, err := executor.validateRequest(request)
	if err != nil {
		return ExecutionResult{}, err
	}
	access, ok := database.AccessContextFromContext(ctx)
	if !ok || access.UserID != string(request.Query.Scope.ActorID) || access.DomainID != string(request.Query.DomainID) {
		return ExecutionResult{}, ErrInvalidExecution
	}
	executionContext, cancel := context.WithTimeout(ctx, time.Duration(timeoutMS)*time.Millisecond)
	executor.mu.Lock()
	if _, exists := executor.active[request.RunID]; exists {
		executor.mu.Unlock()
		cancel()
		return ExecutionResult{}, ErrInvalidExecution
	}
	executor.active[request.RunID] = activeExecution{
		actorID: access.UserID, domainID: access.DomainID, cancel: cancel,
	}
	executor.mu.Unlock()
	defer func() {
		cancel()
		executor.mu.Lock()
		delete(executor.active, request.RunID)
		executor.mu.Unlock()
	}()

	auditRun := queryruntime.SemanticQuestionRun{
		RunID: request.RunID, RunType: queryruntime.RunTypeSemanticQuestion,
		TenantID: string(request.Query.Scope.TenantID), DomainID: string(request.Query.DomainID),
		ActorID: string(request.Query.Scope.ActorID), QueryPlanHash: string(request.Query.PlanHash),
		ValidationHash: string(request.Validation.ValidationHash), PlanCount: len(request.Query.Plans),
		MaxRows: maxRows, TimeoutMS: timeoutMS, MaxExplainCost: maxExplainCost,
	}
	if auditRun.Validate() != nil || executor.auditor.StartSemanticQuestion(executionContext, auditRun) != nil {
		return ExecutionResult{}, ErrExecutionAuditFailure
	}
	started := time.Now()
	runOptions := executor.options
	runOptions.MaxDurationMS = timeoutMS
	outputs, executionErr := executor.runner.Run(executionContext, request.Query, request.Validation, runOptions)
	durationMS := time.Since(started).Milliseconds()
	if executionErr != nil {
		status, code, publicErr := executionFailure(executionContext, executionErr)
		completion := queryruntime.SemanticQuestionCompletion{
			RunID: request.RunID, TenantID: string(request.Query.Scope.TenantID), Status: status,
			DurationMS: durationMS, ErrorCode: code,
		}
		if executor.finishAudit(ctx, completion) != nil {
			return ExecutionResult{}, ErrExecutionAuditFailure
		}
		return ExecutionResult{}, publicErr
	}
	result, err := buildExecutionResult(request, outputs)
	if err != nil {
		completion := queryruntime.SemanticQuestionCompletion{
			RunID: request.RunID, TenantID: string(request.Query.Scope.TenantID),
			Status: queryruntime.SemanticQuestionFailed, DurationMS: durationMS,
			ErrorCode: "INVALID_QUERY_RESULT",
		}
		if executor.finishAudit(ctx, completion) != nil {
			return ExecutionResult{}, ErrExecutionAuditFailure
		}
		return ExecutionResult{}, ErrExecutionRejected
	}
	completion := queryruntime.SemanticQuestionCompletion{
		RunID: request.RunID, TenantID: string(request.Query.Scope.TenantID),
		Status: queryruntime.SemanticQuestionSucceeded, ResultHash: string(result.Artifact.ResultHash),
		RowCount: result.Artifact.TotalRows, DurationMS: durationMS,
	}
	if executor.finishAudit(ctx, completion) != nil {
		return ExecutionResult{}, ErrExecutionAuditFailure
	}
	return result, nil
}

func (executor *Executor) validateRequest(request ExecutionRequest) (int, int, float64, error) {
	parsed, err := uuid.Parse(request.RunID)
	if err != nil || parsed.String() != request.RunID || request.Query.Validate() != nil ||
		request.Validation.Validate() != nil ||
		!reflect.DeepEqual(request.Query.Scope, request.Validation.Scope) ||
		request.Query.DomainID != request.Validation.DomainID ||
		request.Query.PlanHash != request.Validation.QueryArtifactPlanHash ||
		len(request.Query.Plans) != len(request.Validation.Plans) {
		return 0, 0, 0, ErrInvalidExecution
	}
	timeoutMS := executor.options.MaxDurationMS
	if request.Validation.Limits.StatementTimeoutMS < timeoutMS {
		timeoutMS = request.Validation.Limits.StatementTimeoutMS
	}
	maxRows := 0
	maxExplainCost := float64(0)
	for index, plan := range request.Query.Plans {
		validated := request.Validation.Plans[index]
		compiled, live := plan.CompiledQuery()
		if !live || compiled.PlanHash != string(plan.CompiledPlanHash) ||
			validated.Role != plan.Role || validated.QueryPlanHash != plan.PlanHash ||
			validated.CompiledPlanHash != plan.CompiledPlanHash || validated.MaxRows != compiled.MaxRows ||
			validateSQL(compiled.SQL, plan.Source) != nil {
			return 0, 0, 0, ErrInvalidExecution
		}
		if plan.Document.ExecutionPolicy.TimeoutMS < timeoutMS {
			timeoutMS = plan.Document.ExecutionPolicy.TimeoutMS
		}
		if compiled.MaxRows > maxRows {
			maxRows = compiled.MaxRows
		}
		if validated.Explain.TotalCost > maxExplainCost {
			maxExplainCost = validated.Explain.TotalCost
		}
	}
	if timeoutMS < 1 || maxRows < 1 {
		return 0, 0, 0, ErrInvalidExecution
	}
	return timeoutMS, maxRows, maxExplainCost, nil
}

func (executor *Executor) finishAudit(ctx context.Context, completion queryruntime.SemanticQuestionCompletion) error {
	if completion.Validate() != nil {
		return ErrExecutionAuditFailure
	}
	finishContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	if executor.auditor.FinishSemanticQuestion(finishContext, completion) != nil {
		return ErrExecutionAuditFailure
	}
	return nil
}

// Cancel terminates only a run owned by the authenticated actor and selected
// domain in ctx. PostgreSQL receives cancellation through the canceled query
// context; the executing goroutine remains responsible for the terminal audit.
func (executor *Executor) Cancel(ctx context.Context, runID string) (bool, error) {
	if executor == nil {
		return false, ErrInvalidExecutor
	}
	parsed, err := uuid.Parse(runID)
	if err != nil || parsed.String() != runID {
		return false, ErrInvalidExecution
	}
	access, ok := database.AccessContextFromContext(ctx)
	if !ok || access.DomainID == "" {
		return false, ErrInvalidExecution
	}
	executor.mu.Lock()
	active, exists := executor.active[runID]
	if !exists || active.actorID != access.UserID || active.domainID != access.DomainID {
		executor.mu.Unlock()
		return false, nil
	}
	active.cancel()
	executor.mu.Unlock()
	return true, nil
}

func executionFailure(ctx context.Context, executionErr error) (string, string, error) {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(executionErr, context.DeadlineExceeded) {
		return queryruntime.SemanticQuestionTimeout, "QUERY_TIMEOUT", ErrExecutionTimeout
	}
	if errors.Is(ctx.Err(), context.Canceled) || errors.Is(executionErr, context.Canceled) {
		return queryruntime.SemanticQuestionCanceled, "QUERY_CANCELED", ErrExecutionCanceled
	}
	if errors.Is(executionErr, errMaterializationStale) || errors.Is(executionErr, errWarehouseRejected) ||
		errors.Is(executionErr, errResultInvalid) {
		return queryruntime.SemanticQuestionFailed, "QUERY_REJECTED", ErrExecutionRejected
	}
	return queryruntime.SemanticQuestionFailed, "QUERY_EXECUTION_FAILED", ErrExecutionUnavailable
}

type canonicalCell struct {
	Kind  string `json:"kind"`
	Value string `json:"value,omitempty"`
}

type executedPlan struct {
	role          compiler.QueryRole
	columns       []ExecutionColumn
	rows          [][]any
	canonicalRows [][]canonicalCell
}

func buildExecutionResult(request ExecutionRequest, outputs []executedPlan) (ExecutionResult, error) {
	if len(outputs) != len(request.Query.Plans) {
		return ExecutionResult{}, errResultInvalid
	}
	artifact := ExecutionArtifact{
		Version: ExecutionVersion, RunID: request.RunID, Scope: request.Query.Scope,
		DomainID: request.Query.DomainID, QueryArtifactPlanHash: request.Query.PlanHash,
		ValidationHash: request.Validation.ValidationHash, Plans: []PlanExecution{},
	}
	result := ExecutionResult{rows: make([]executionRows, 0, len(outputs))}
	for index, output := range outputs {
		queryPlan := request.Query.Plans[index]
		validation := request.Validation.Plans[index]
		if output.role != queryPlan.Role || len(output.rows) != len(output.canonicalRows) ||
			len(output.rows) > validation.MaxRows || len(output.columns) < 1 || len(output.columns) > 1024 {
			return ExecutionResult{}, errResultInvalid
		}
		for rowIndex := range output.rows {
			if len(output.rows[rowIndex]) != len(output.columns) ||
				len(output.canonicalRows[rowIndex]) != len(output.columns) {
				return ExecutionResult{}, errResultInvalid
			}
		}
		planHash, err := planResultHash(queryPlan, output)
		if err != nil {
			return ExecutionResult{}, err
		}
		artifact.Plans = append(artifact.Plans, PlanExecution{
			Role: queryPlan.Role, QueryPlanHash: queryPlan.PlanHash,
			CompiledPlanHash: queryPlan.CompiledPlanHash, MaxRows: validation.MaxRows,
			Columns: append([]ExecutionColumn(nil), output.columns...), RowCount: len(output.rows),
			ResultHash: planHash,
		})
		artifact.TotalRows += len(output.rows)
		result.rows = append(result.rows, executionRows{role: output.role, rows: cloneResultRows(output.rows)})
	}
	var err error
	artifact.ResultHash, err = executionResultHash(artifact)
	if err != nil {
		return ExecutionResult{}, err
	}
	result.Artifact = artifact
	if err := result.Artifact.Validate(); err != nil {
		return ExecutionResult{}, err
	}
	return result, nil
}

func planResultHash(plan compiler.QueryPlan, output executedPlan) (askdata.ContentHash, error) {
	payload, err := registry.CanonicalValue(struct {
		Role             compiler.QueryRole  `json:"role"`
		QueryPlanHash    askdata.ContentHash `json:"queryPlanHash"`
		CompiledPlanHash askdata.ContentHash `json:"compiledPlanHash"`
		Columns          []ExecutionColumn   `json:"columns"`
		Rows             [][]canonicalCell   `json:"rows"`
	}{plan.Role, plan.PlanHash, plan.CompiledPlanHash, output.columns, output.canonicalRows})
	if err != nil {
		return "", err
	}
	return askdata.HashBytes(payload), nil
}

func executionResultHash(artifact ExecutionArtifact) (askdata.ContentHash, error) {
	payload, err := registry.CanonicalValue(struct {
		Version               string              `json:"version"`
		Scope                 askdata.PolicyScope `json:"scope"`
		DomainID              askdata.ID          `json:"domainId"`
		QueryArtifactPlanHash askdata.ContentHash `json:"queryArtifactPlanHash"`
		ValidationHash        askdata.ContentHash `json:"validationHash"`
		Plans                 []PlanExecution     `json:"plans"`
		TotalRows             int                 `json:"totalRows"`
	}{
		artifact.Version, artifact.Scope, artifact.DomainID, artifact.QueryArtifactPlanHash,
		artifact.ValidationHash, artifact.Plans, artifact.TotalRows,
	})
	if err != nil {
		return "", err
	}
	return askdata.HashBytes(payload), nil
}

type postgresExecutionRunner struct {
	pool        *pgxpool.Pool
	revalidator queryruntime.SemanticMaterializationRevalidator
}

func (runner *postgresExecutionRunner) Run(
	ctx context.Context,
	artifact compiler.QueryArtifact,
	validation ValidationArtifact,
	options ExecutorOptions,
) ([]executedPlan, error) {
	materializations, err := semanticMaterializations(artifact.Plans)
	if err != nil {
		return nil, errMaterializationStale
	}
	if err := runner.revalidator.RevalidateSemanticMaterializations(
		ctx, string(artifact.Scope.TenantID), materializations,
	); err != nil {
		if errors.Is(err, dataset.ErrVersionUnavailable) || errors.Is(err, dataset.ErrPreviewUnsupported) {
			return nil, errMaterializationStale
		}
		return nil, executionContextError(ctx)
	}
	tx, err := runner.pool.BeginTx(ctx, pgx.TxOptions{
		IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly,
	})
	if err != nil {
		return nil, executionContextError(ctx)
	}
	defer tx.Rollback(context.WithoutCancel(ctx)) //nolint:errcheck
	timezone := artifact.Timezone
	if timezone == "" {
		timezone = "UTC"
	}
	if _, err := time.LoadLocation(timezone); err != nil {
		return nil, errWarehouseRejected
	}
	statementTimeoutMS := validation.Limits.StatementTimeoutMS
	if options.MaxDurationMS < statementTimeoutMS {
		statementTimeoutMS = options.MaxDurationMS
	}
	if _, err := tx.Exec(ctx, `SELECT
		set_config('app.tenant_id',$1,true),
		set_config('app.access_mode','USER',true),
		set_config('app.user_id',$2,true),
		set_config('app.domain_id',$3,true),
		set_config('statement_timeout',$4,true),
		set_config('lock_timeout',$5,true),
		set_config('TimeZone',$6,true)`, artifact.Scope.TenantID, artifact.Scope.ActorID,
		artifact.DomainID, strconv.Itoa(statementTimeoutMS)+"ms",
		strconv.Itoa(validation.Limits.LockTimeoutMS)+"ms", timezone); err != nil {
		return nil, executionContextError(ctx)
	}
	var readOnly, safeRole bool
	if err := tx.QueryRow(ctx, `SELECT
		current_setting('transaction_read_only')='on',
		current_user=session_user AND NOT role.rolsuper AND NOT role.rolcreatedb
		AND NOT role.rolcreaterole AND NOT role.rolreplication
		AND NOT role.rolbypassrls AND NOT role.rolinherit
		FROM pg_roles AS role WHERE role.rolname=current_user`).Scan(&readOnly, &safeRole); err != nil {
		return nil, executionContextError(ctx)
	}
	if !readOnly || !safeRole {
		return nil, errWarehouseRejected
	}
	for _, source := range materializations {
		var relationKind string
		var canSelect, canInsert, canUpdate, canDelete, canTruncate bool
		err := tx.QueryRow(ctx, `SELECT class.relkind::text,
			has_table_privilege(current_user,class.oid,'SELECT'),
			has_table_privilege(current_user,class.oid,'INSERT'),
			has_table_privilege(current_user,class.oid,'UPDATE'),
			has_table_privilege(current_user,class.oid,'DELETE'),
			has_table_privilege(current_user,class.oid,'TRUNCATE')
			FROM pg_class AS class JOIN pg_namespace AS namespace ON namespace.oid=class.relnamespace
			WHERE namespace.nspname=$1 AND class.relname=$2`, source.PublishedSchema, source.PublishedName).
			Scan(&relationKind, &canSelect, &canInsert, &canUpdate, &canDelete, &canTruncate)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, errWarehouseRejected
		}
		if err != nil {
			return nil, executionContextError(ctx)
		}
		if relationKind != "v" || !canSelect || canInsert || canUpdate || canDelete || canTruncate {
			return nil, errWarehouseRejected
		}
	}
	usedBytes := 0
	result := make([]executedPlan, 0, len(artifact.Plans))
	for _, plan := range artifact.Plans {
		compiled, live := plan.CompiledQuery()
		if !live {
			return nil, errWarehouseRejected
		}
		rows, err := tx.Query(ctx, compiled.SQL, compiled.Args...)
		if err != nil {
			return nil, executionContextError(ctx)
		}
		descriptions := rows.FieldDescriptions()
		if len(descriptions) < 1 || len(descriptions) > 1024 {
			rows.Close()
			return nil, errResultInvalid
		}
		output := executedPlan{
			role: plan.Role, columns: make([]ExecutionColumn, len(descriptions)),
			rows: make([][]any, 0, compiled.MaxRows), canonicalRows: make([][]canonicalCell, 0, compiled.MaxRows),
		}
		for index, field := range descriptions {
			output.columns[index] = ExecutionColumn{Name: field.Name, DataTypeOID: field.DataTypeOID}
			usedBytes += len(field.Name) + 16
		}
		for rows.Next() {
			if len(output.rows) >= compiled.MaxRows || usedBytes > options.MaxResultBytes {
				rows.Close()
				return nil, errResultInvalid
			}
			values, err := rows.Values()
			if err != nil || len(values) != len(descriptions) {
				rows.Close()
				return nil, errResultInvalid
			}
			normalized := make([]any, len(values))
			canonical := make([]canonicalCell, len(values))
			for index, value := range values {
				normalized[index], canonical[index], err = normalizeResultValue(value, descriptions[index].DataTypeOID)
				if err != nil {
					rows.Close()
					return nil, errResultInvalid
				}
				usedBytes += len(canonical[index].Kind) + len(canonical[index].Value) + 16
				if usedBytes > options.MaxResultBytes {
					rows.Close()
					return nil, errResultInvalid
				}
			}
			output.rows = append(output.rows, normalized)
			output.canonicalRows = append(output.canonicalRows, canonical)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, executionContextError(ctx)
		}
		rows.Close()
		result = append(result, output)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, executionContextError(ctx)
	}
	return result, nil
}

func semanticMaterializations(plans []compiler.QueryPlan) ([]queryruntime.SemanticMaterialization, error) {
	result := make([]queryruntime.SemanticMaterialization, 0, len(plans))
	byNode := map[string]queryruntime.SemanticMaterialization{}
	for _, plan := range plans {
		item := queryruntime.SemanticMaterialization{
			NodeID: plan.Source.NodeID, MaterializationID: string(plan.Source.MaterializationID),
			DatasetVersionID: string(plan.Source.DatasetVersionID), PublishedSchema: plan.Source.PublishedSchema,
			PublishedName: plan.Source.PublishedName,
		}
		if previous, exists := byNode[item.NodeID]; exists {
			if previous != item {
				return nil, errMaterializationStale
			}
			continue
		}
		byNode[item.NodeID] = item
		result = append(result, item)
	}
	return result, nil
}

func executionContextError(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return ErrExecutionUnavailable
}

func normalizeResultValue(value any, dataTypeOID uint32) (any, canonicalCell, error) {
	if value == nil {
		return nil, canonicalCell{Kind: "NULL"}, nil
	}
	if numeric, ok := value.(pgtype.Numeric); ok {
		driverValue, err := numeric.Value()
		if err != nil {
			return nil, canonicalCell{}, err
		}
		text, ok := driverValue.(string)
		if !ok || text == "" || text == "NaN" || strings.Contains(text, "Infinity") {
			return nil, canonicalCell{}, errResultInvalid
		}
		return text, canonicalCell{Kind: "DECIMAL", Value: text}, nil
	}
	if timestamp, ok := value.(time.Time); ok {
		kind, text := "TIMESTAMPTZ", timestamp.UTC().Format(time.RFC3339Nano)
		switch dataTypeOID {
		case pgtype.DateOID:
			kind, text = "DATE", timestamp.Format("2006-01-02")
		case pgtype.TimestampOID:
			kind, text = "TIMESTAMP", timestamp.Format("2006-01-02T15:04:05.999999999")
		}
		return text, canonicalCell{Kind: kind, Value: text}, nil
	}
	switch typed := value.(type) {
	case bool:
		text := strconv.FormatBool(typed)
		return typed, canonicalCell{Kind: "BOOLEAN", Value: text}, nil
	case string:
		if !utf8.ValidString(typed) {
			return nil, canonicalCell{}, errResultInvalid
		}
		return typed, canonicalCell{Kind: "STRING", Value: typed}, nil
	case []byte:
		text := base64.StdEncoding.EncodeToString(typed)
		return text, canonicalCell{Kind: "BYTES", Value: text}, nil
	case json.Number:
		if integer, err := typed.Int64(); err == nil {
			text := strconv.FormatInt(integer, 10)
			return integer, canonicalCell{Kind: "INTEGER", Value: text}, nil
		}
		floating, err := typed.Float64()
		if err != nil || math.IsNaN(floating) || math.IsInf(floating, 0) {
			return nil, canonicalCell{}, errResultInvalid
		}
		text := strconv.FormatFloat(floating, 'g', -1, 64)
		return floating, canonicalCell{Kind: "FLOAT", Value: text}, nil
	case int:
		return normalizedSignedInteger(int64(typed))
	case int8:
		return normalizedSignedInteger(int64(typed))
	case int16:
		return normalizedSignedInteger(int64(typed))
	case int32:
		return normalizedSignedInteger(int64(typed))
	case int64:
		return normalizedSignedInteger(typed)
	case uint:
		return normalizedUnsignedInteger(uint64(typed))
	case uint8:
		return normalizedUnsignedInteger(uint64(typed))
	case uint16:
		return normalizedUnsignedInteger(uint64(typed))
	case uint32:
		return normalizedUnsignedInteger(uint64(typed))
	case uint64:
		return normalizedUnsignedInteger(typed)
	case float32:
		return normalizedFloat(float64(typed), 32)
	case float64:
		return normalizedFloat(typed, 64)
	}
	if valuer, ok := value.(driver.Valuer); ok {
		driverValue, err := valuer.Value()
		if err != nil || reflect.TypeOf(driverValue) == reflect.TypeOf(value) {
			return nil, canonicalCell{}, errResultInvalid
		}
		return normalizeResultValue(driverValue, dataTypeOID)
	}
	return nil, canonicalCell{}, errResultInvalid
}

func normalizedSignedInteger(value int64) (any, canonicalCell, error) {
	text := strconv.FormatInt(value, 10)
	return value, canonicalCell{Kind: "INTEGER", Value: text}, nil
}

func normalizedUnsignedInteger(value uint64) (any, canonicalCell, error) {
	text := strconv.FormatUint(value, 10)
	if value <= math.MaxInt64 {
		return int64(value), canonicalCell{Kind: "INTEGER", Value: text}, nil
	}
	return text, canonicalCell{Kind: "UNSIGNED_INTEGER", Value: text}, nil
}

func normalizedFloat(value float64, bits int) (any, canonicalCell, error) {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return nil, canonicalCell{}, errResultInvalid
	}
	text := strconv.FormatFloat(value, 'g', -1, bits)
	return value, canonicalCell{Kind: "FLOAT", Value: text}, nil
}

func cloneResultRows(rows [][]any) [][]any {
	result := make([][]any, len(rows))
	for index := range rows {
		result[index] = append([]any(nil), rows[index]...)
	}
	return result
}
