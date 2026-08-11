package reportai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/platform/database"
	"intelligent-report-generation-system/internal/report/operation"
	"intelligent-report-generation-system/internal/report/store"
)

type RunKind string

const (
	RunPlan          RunKind = "PLAN"
	RunGenerateDraft RunKind = "GENERATE_DRAFT"
	RunScopedEdit    RunKind = "SCOPED_EDIT"
	RunInsight       RunKind = "INSIGHT"
	RunPublishReview RunKind = "PUBLISH_REVIEW"
)

type RunState string

const (
	RunRunning   RunState = "RUNNING"
	RunSucceeded RunState = "SUCCEEDED"
	RunFailed    RunState = "FAILED"
	RunRejected  RunState = "REJECTED"
)

// RequestSummary is deliberately closed: prompts, sample rows and raw values
// cannot be represented and therefore cannot accidentally enter the audit DB.
type RequestSummary struct {
	Intent          string   `json:"intent,omitempty"`
	SelectionIDs    []string `json:"selectionIds,omitempty"`
	AvailableFields []string `json:"availableFields,omitempty"`
}

type StartRunInput struct {
	ReportID      askdata.ID
	Kind          RunKind
	PromptVersion string
	ModelPolicy   string
	Summary       RequestSummary
	BaseRevision  *int64
	Scope         json.RawMessage
}

type Run struct {
	ID             askdata.ID     `json:"id"`
	ReportID       askdata.ID     `json:"reportId"`
	Kind           RunKind        `json:"kind"`
	State          RunState       `json:"state"`
	RequestSummary RequestSummary `json:"requestSummary"`
	CreatedAt      time.Time      `json:"createdAt"`
	FinishedAt     *time.Time     `json:"finishedAt,omitempty"`
}

type OperationRecord struct {
	ID                askdata.ID `json:"id"`
	ValidationState   string     `json:"validationState"`
	RejectionCode     string     `json:"rejectionCode,omitempty"`
	AppliedRevisionNo *int64     `json:"appliedRevisionNo,omitempty"`
}

type PostgresStore struct{ pool *pgxpool.Pool }

func NewPostgresStore(pool *pgxpool.Pool) *PostgresStore { return &PostgresStore{pool: pool} }

func (s *PostgresStore) StartRun(ctx context.Context, identity store.Identity, input StartRunInput) (Run, error) {
	if s == nil || s.pool == nil || identity.Validate() != nil || input.ReportID.Validate() != nil {
		return Run{}, errors.New("invalid report AI run")
	}
	if !validRunKind(input.Kind) || strings.TrimSpace(input.PromptVersion) == "" || strings.TrimSpace(input.ModelPolicy) == "" {
		return Run{}, errors.New("invalid report AI run metadata")
	}
	summary, err := normalizeSummary(input.Summary)
	if err != nil {
		return Run{}, err
	}
	summaryJSON, err := json.Marshal(summary)
	if err != nil {
		return Run{}, err
	}
	if len(input.Scope) != 0 && !json.Valid(input.Scope) {
		return Run{}, errors.New("AI scope must be valid JSON")
	}
	ctx = database.WithAccessContext(ctx, string(identity.ActorID), string(identity.DomainID))
	result := Run{ID: askdata.ID(uuid.NewString()), ReportID: input.ReportID, Kind: input.Kind, State: RunRunning, RequestSummary: summary}
	requiredAction := "EDIT"
	if input.Kind == RunPublishReview {
		requiredAction = "PUBLISH"
	}
	err = database.WithTenantTx(ctx, s.pool, string(identity.TenantID), func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `INSERT INTO platform.report_ai_runs(
			id,tenant_id,report_id,kind,actor_user_id,prompt_version,model_policy,
			request_summary_json,base_revision_no,scope_json,state
		) SELECT $1,$2,$3,$4,$5,$6,$7,$8,$9,NULLIF($10,'')::jsonb,'RUNNING'
		WHERE platform.report_v2_can_access($3,ARRAY[$11]::text[])
		RETURNING created_at`, result.ID, identity.TenantID, input.ReportID, input.Kind,
			identity.ActorID, input.PromptVersion, input.ModelPolicy, summaryJSON,
			input.BaseRevision, string(input.Scope), requiredAction).Scan(&result.CreatedAt)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Run{}, store.ErrNotFound
	}
	return result, err
}

func (s *PostgresStore) RecordOperation(ctx context.Context, identity store.Identity, runID askdata.ID, item operation.Operation, rejectionCode string, appliedRevision *int64) (OperationRecord, error) {
	if s == nil || s.pool == nil || identity.Validate() != nil || runID.Validate() != nil {
		return OperationRecord{}, errors.New("invalid AI operation audit")
	}
	if appliedRevision != nil {
		return OperationRecord{}, errors.New("AI operation application is recorded only by draft commit")
	}
	if strings.TrimSpace(rejectionCode) == "" {
		if err := item.Validate(); err != nil {
			return OperationRecord{}, err
		}
	}
	operationJSON, err := json.Marshal(item)
	if err != nil {
		return OperationRecord{}, err
	}
	state := "VALID"
	if strings.TrimSpace(rejectionCode) != "" {
		state = "REJECTED"
		appliedRevision = nil
	}
	result := OperationRecord{ID: askdata.ID(uuid.NewString()), ValidationState: state, RejectionCode: strings.TrimSpace(rejectionCode), AppliedRevisionNo: appliedRevision}
	ctx = database.WithAccessContext(ctx, string(identity.ActorID), string(identity.DomainID))
	err = database.WithTenantTx(ctx, s.pool, string(identity.TenantID), func(tx pgx.Tx) error {
		tag, execErr := tx.Exec(ctx, `INSERT INTO platform.report_ai_operations(
			id,tenant_id,ai_run_id,operation_json,validation_state,rejection_code,applied_revision_no
		) SELECT $1,$2,$3,$4,$5,NULLIF($6,''),$7
		WHERE EXISTS(SELECT 1 FROM platform.report_ai_runs WHERE id=$3 AND state='RUNNING')`,
			result.ID, identity.TenantID, runID, operationJSON, state, result.RejectionCode, appliedRevision)
		if execErr != nil {
			return execErr
		}
		if tag.RowsAffected() != 1 {
			return errors.New("report AI run is unavailable or already finished")
		}
		return nil
	})
	return result, err
}

func (s *PostgresStore) FinishRun(ctx context.Context, identity store.Identity, runID askdata.ID, state RunState, responseSummary map[string]any, errorCode string) error {
	if s == nil || s.pool == nil || identity.Validate() != nil || runID.Validate() != nil ||
		(state != RunSucceeded && state != RunFailed && state != RunRejected) {
		return errors.New("invalid AI run completion")
	}
	if state == RunSucceeded && strings.TrimSpace(errorCode) != "" {
		return errors.New("successful AI run cannot have an error code")
	}
	if state != RunSucceeded && strings.TrimSpace(errorCode) == "" {
		return errors.New("failed or rejected AI run requires an error code")
	}
	if responseSummary == nil {
		responseSummary = map[string]any{}
	}
	responseJSON, err := json.Marshal(responseSummary)
	if err != nil || len(responseJSON) > 64<<10 {
		return errors.New("invalid AI response summary")
	}
	ctx = database.WithAccessContext(ctx, string(identity.ActorID), string(identity.DomainID))
	return database.WithTenantTx(ctx, s.pool, string(identity.TenantID), func(tx pgx.Tx) error {
		tag, execErr := tx.Exec(ctx, `UPDATE platform.report_ai_runs SET
			state=$2,response_summary_json=$3,error_code=NULLIF($4,''),finished_at=now()
			WHERE id=$1 AND state='RUNNING'`, runID, state, responseJSON, strings.TrimSpace(errorCode))
		if execErr != nil {
			return execErr
		}
		if tag.RowsAffected() != 1 {
			return errors.New("report AI run is unavailable or already finished")
		}
		return nil
	})
}

// CompletePreview atomically records every validated proposed operation and
// closes the generation run. Confirmation later only sets appliedRevisionNo.
func (s *PostgresStore) CompletePreview(ctx context.Context, identity store.Identity, runID askdata.ID, operations []operation.Operation, responseSummary map[string]any) error {
	return s.completeOperations(ctx, identity, runID, operations, "VALID", "", RunSucceeded, responseSummary)
}

func (s *PostgresStore) RejectPreview(ctx context.Context, identity store.Identity, runID askdata.ID, operations []operation.Operation, code string) error {
	if strings.TrimSpace(code) == "" {
		return errors.New("AI rejection code is required")
	}
	return s.completeOperations(ctx, identity, runID, operations, "REJECTED", code, RunRejected,
		map[string]any{"operationCount": len(operations)})
}

func (s *PostgresStore) completeOperations(ctx context.Context, identity store.Identity, runID askdata.ID, operations []operation.Operation, validationState, rejectionCode string, runState RunState, responseSummary map[string]any) error {
	maximumOperations := operation.MaxAIOperations
	if validationState == "REJECTED" {
		// Preserve the bounded invalid response for audit. The 30-operation limit
		// is exactly what may have been violated, while the protocol-wide bound
		// still prevents an unbounded database write.
		maximumOperations = operation.MaxOperations
	}
	if s == nil || s.pool == nil || identity.Validate() != nil || runID.Validate() != nil || len(operations) > maximumOperations {
		return errors.New("invalid AI operation preview audit")
	}
	rawOperations := make([][]byte, len(operations))
	for index, item := range operations {
		if validationState == "VALID" {
			if err := item.Validate(); err != nil {
				return fmt.Errorf("operations[%d]: %w", index, err)
			}
		}
		raw, err := json.Marshal(item)
		if err != nil {
			return err
		}
		rawOperations[index] = raw
	}
	responseJSON, err := json.Marshal(responseSummary)
	if err != nil || len(responseJSON) > 64<<10 {
		return errors.New("invalid AI preview response summary")
	}
	ctx = database.WithAccessContext(ctx, string(identity.ActorID), string(identity.DomainID))
	return database.WithTenantTx(ctx, s.pool, string(identity.TenantID), func(tx pgx.Tx) error {
		for _, raw := range rawOperations {
			if _, err := tx.Exec(ctx, `INSERT INTO platform.report_ai_operations(
				id,tenant_id,ai_run_id,operation_json,validation_state,rejection_code
			) VALUES($1,$2,$3,$4,$5,NULLIF($6,''))`, uuid.NewString(), identity.TenantID,
				runID, raw, validationState, rejectionCode); err != nil {
				return err
			}
		}
		tag, err := tx.Exec(ctx, `UPDATE platform.report_ai_runs SET state=$2,
			response_summary_json=$3,error_code=NULLIF($4,''),finished_at=now()
			WHERE id=$1 AND actor_user_id=$5 AND state='RUNNING'`, runID, runState,
			responseJSON, rejectionCode, identity.ActorID)
		if err != nil || tag.RowsAffected() != 1 {
			return errors.Join(err, errors.New("report AI run is unavailable or already finished"))
		}
		return nil
	})
}

func normalizeSummary(input RequestSummary) (RequestSummary, error) {
	input.Intent = strings.TrimSpace(input.Intent)
	if !utf8.ValidString(input.Intent) || utf8.RuneCountInString(input.Intent) > 1024 ||
		strings.ContainsAny(input.Intent, "\x00\r\n") || len(input.SelectionIDs) > 300 ||
		len(input.AvailableFields) > 1000 {
		return RequestSummary{}, errors.New("AI request summary exceeds limits")
	}
	for name, values := range map[string][]string{"selectionIds": input.SelectionIDs, "availableFields": input.AvailableFields} {
		seen := map[string]struct{}{}
		for index, value := range values {
			value = strings.TrimSpace(value)
			if value == "" || !utf8.ValidString(value) || utf8.RuneCountInString(value) > 256 ||
				strings.ContainsAny(value, "\x00\r\n") {
				return RequestSummary{}, fmt.Errorf("%s[%d] is invalid", name, index)
			}
			if _, exists := seen[value]; exists {
				return RequestSummary{}, fmt.Errorf("%s[%d] is duplicated", name, index)
			}
			seen[value] = struct{}{}
			values[index] = value
		}
	}
	if input.SelectionIDs == nil {
		input.SelectionIDs = []string{}
	}
	if input.AvailableFields == nil {
		input.AvailableFields = []string{}
	}
	return input, nil
}

func validRunKind(kind RunKind) bool {
	return kind == RunPlan || kind == RunGenerateDraft || kind == RunScopedEdit || kind == RunInsight || kind == RunPublishReview
}

func (s *PostgresStore) ValidatePublicationReview(
	ctx context.Context, identity store.Identity, runID, reportID askdata.ID, sourceRevision int64,
) ([]string, error) {
	if s == nil || s.pool == nil || identity.Validate() != nil || runID.Validate() != nil ||
		reportID.Validate() != nil || sourceRevision < 0 {
		return nil, errors.New("invalid publication review receipt")
	}
	ctx = database.WithAccessContext(ctx, string(identity.ActorID), string(identity.DomainID))
	warnings := []string{}
	err := database.WithTenantTx(ctx, s.pool, string(identity.TenantID), func(tx pgx.Tx) error {
		var raw json.RawMessage
		if err := tx.QueryRow(ctx, `SELECT response_summary_json
			FROM platform.report_ai_runs
			WHERE id=$1 AND report_id=$2 AND kind='PUBLISH_REVIEW' AND state='SUCCEEDED'
			  AND base_revision_no=$3 AND platform.report_v2_can_access(report_id,ARRAY['PUBLISH']::text[])`,
			runID, reportID, sourceRevision).Scan(&raw); err != nil {
			return err
		}
		var summary struct {
			WarningCodes   []string `json:"warningCodes"`
			DefinitionHash string   `json:"definitionHash"`
		}
		if json.Unmarshal(raw, &summary) != nil || len(summary.DefinitionHash) != 64 || len(summary.WarningCodes) > 100 {
			return errors.New("publication review receipt is malformed")
		}
		warnings = summary.WarningCodes
		return nil
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, store.ErrNotFound
	}
	return warnings, err
}
