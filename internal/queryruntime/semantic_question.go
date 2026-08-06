package queryruntime

import (
	"context"
	"encoding/hex"
	"errors"
	"math"
	"regexp"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"intelligent-report-generation-system/internal/dataset"
	"intelligent-report-generation-system/internal/platform/database"
)

const RunTypeSemanticQuestion = "SEMANTIC_QUESTION"

const (
	SemanticQuestionSucceeded = "SUCCEEDED"
	SemanticQuestionFailed    = "FAILED"
	SemanticQuestionTimeout   = "TIMEOUT"
	SemanticQuestionCanceled  = "CANCELED"
)

var semanticQuestionErrorCodePattern = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,127}$`)

// SemanticQuestionRun is the deliberately summary-only audit boundary for a
// formal AskData execution. It has no fields for SQL, parameters or rows.
type SemanticQuestionRun struct {
	RunID          string  `json:"runId"`
	RunType        string  `json:"runType"`
	TenantID       string  `json:"tenantId"`
	DomainID       string  `json:"domainId"`
	ActorID        string  `json:"actorId"`
	QueryPlanHash  string  `json:"queryPlanHash"`
	ValidationHash string  `json:"validationHash"`
	PlanCount      int     `json:"planCount"`
	MaxRows        int     `json:"maxRows"`
	TimeoutMS      int     `json:"timeoutMs"`
	MaxExplainCost float64 `json:"maxExplainCost"`
}

func (run SemanticQuestionRun) Validate() error {
	if parsed, err := uuid.Parse(run.RunID); err != nil || parsed.String() != run.RunID ||
		run.RunType != RunTypeSemanticQuestion || run.TenantID == "" || run.DomainID == "" || run.ActorID == "" ||
		!semanticHash(run.QueryPlanHash) || !semanticHash(run.ValidationHash) ||
		run.PlanCount < 1 || run.PlanCount > 2 || run.MaxRows < 1 || run.MaxRows > 10000 ||
		run.TimeoutMS < 100 || run.TimeoutMS > 25000 || math.IsNaN(run.MaxExplainCost) ||
		math.IsInf(run.MaxExplainCost, 0) || run.MaxExplainCost < 0 {
		return dataset.ErrPreviewInvalid
	}
	return nil
}

// SemanticQuestionCompletion contains operational metrics and stable hashes
// only. Result data remains in the caller process and is never passed here.
type SemanticQuestionCompletion struct {
	RunID      string `json:"runId"`
	TenantID   string `json:"tenantId"`
	Status     string `json:"status"`
	ResultHash string `json:"resultHash,omitempty"`
	RowCount   int    `json:"rowCount"`
	DurationMS int64  `json:"durationMs"`
	ErrorCode  string `json:"errorCode,omitempty"`
}

func (completion SemanticQuestionCompletion) Validate() error {
	parsed, err := uuid.Parse(completion.RunID)
	if err != nil || parsed.String() != completion.RunID || completion.TenantID == "" ||
		completion.RowCount < 0 || completion.RowCount > 20000 ||
		completion.DurationMS < 0 || completion.DurationMS > 600000 {
		return dataset.ErrPreviewInvalid
	}
	if completion.Status == SemanticQuestionSucceeded {
		if !semanticHash(completion.ResultHash) || completion.ErrorCode != "" {
			return dataset.ErrPreviewInvalid
		}
		return nil
	}
	if completion.Status != SemanticQuestionFailed && completion.Status != SemanticQuestionTimeout &&
		completion.Status != SemanticQuestionCanceled || completion.ResultHash != "" ||
		!semanticQuestionErrorCodePattern.MatchString(completion.ErrorCode) || completion.RowCount != 0 {
		return dataset.ErrPreviewInvalid
	}
	return nil
}

type SemanticQuestionAuditStore interface {
	StartSemanticQuestion(context.Context, SemanticQuestionRun) error
	FinishSemanticQuestion(context.Context, SemanticQuestionCompletion) error
}

// SemanticMaterialization is the minimum immutable control-plane identity
// required to revalidate a compiler-pinned warehouse view immediately before
// execution. It intentionally carries no column values or query parameters.
type SemanticMaterialization struct {
	NodeID            string
	MaterializationID string
	DatasetVersionID  string
	PublishedSchema   string
	PublishedName     string
}

type SemanticMaterializationRevalidator interface {
	RevalidateSemanticMaterializations(context.Context, string, []SemanticMaterialization) error
}

type PostgresSemanticMaterializationRevalidator struct{ pool *pgxpool.Pool }

func NewPostgresSemanticMaterializationRevalidator(pool *pgxpool.Pool) *PostgresSemanticMaterializationRevalidator {
	return &PostgresSemanticMaterializationRevalidator{pool: pool}
}

// RevalidateSemanticMaterializations uses a read-only USER/RLS snapshot. It
// never follows a current semantic release; only the exact materialization IDs
// already pinned by the compiler are accepted.
func (validator *PostgresSemanticMaterializationRevalidator) RevalidateSemanticMaterializations(
	ctx context.Context,
	tenantID string,
	expected []SemanticMaterialization,
) error {
	if validator == nil || validator.pool == nil || tenantID == "" || len(expected) < 1 || len(expected) > 2 {
		return dataset.ErrVersionUnavailable
	}
	access, ok := database.AccessContextFromContext(ctx)
	if !ok || access.DomainID == "" {
		return dataset.ErrVersionUnavailable
	}
	tx, err := validator.pool.BeginTx(ctx, pgx.TxOptions{
		IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly,
	})
	if err != nil {
		return err
	}
	defer tx.Rollback(context.WithoutCancel(ctx)) //nolint:errcheck
	if _, err := tx.Exec(ctx, `SELECT
		set_config('app.tenant_id',$1,true),
		set_config('app.access_mode','USER',true),
		set_config('app.user_id',$2,true),
		set_config('app.domain_id',$3,true)`, tenantID, access.UserID, access.DomainID); err != nil {
		return err
	}
	seen := make(map[string]bool, len(expected))
	for _, item := range expected {
		if item.NodeID == "" || item.MaterializationID == "" || item.DatasetVersionID == "" ||
			item.PublishedSchema != "warehouse_published" || item.PublishedName == "" || seen[item.NodeID] {
			return dataset.ErrVersionUnavailable
		}
		seen[item.NodeID] = true
		var actual SemanticMaterialization
		actual.NodeID = item.NodeID
		var layer string
		err := tx.QueryRow(ctx, `SELECT materialization.id::text,
				materialization.dataset_version_id::text,
				materialization.published_schema,
				materialization.published_name,
				materialization.layer
			FROM platform.dataset_materializations AS materialization
			JOIN platform.dataset_versions AS version
			  ON version.id=materialization.dataset_version_id
			 AND version.dataset_id=materialization.dataset_id
			 AND version.tenant_id=materialization.tenant_id
			JOIN platform.datasets AS owner
			  ON owner.id=version.dataset_id AND owner.tenant_id=version.tenant_id
			WHERE materialization.id=$1
			  AND materialization.status='ACTIVE'
			  AND materialization.layer IN ('DWS','ADS')
			  AND version.status='PUBLISHED'
			  AND owner.status='PUBLISHED'
			  AND owner.current_published_version_id=version.id
			  AND owner.deleted_at IS NULL`, item.MaterializationID).
			Scan(&actual.MaterializationID, &actual.DatasetVersionID,
				&actual.PublishedSchema, &actual.PublishedName, &layer)
		if errors.Is(err, pgx.ErrNoRows) {
			return dataset.ErrVersionUnavailable
		}
		if err != nil {
			return err
		}
		if actual != item || (layer != "DWS" && layer != "ADS") {
			return dataset.ErrVersionUnavailable
		}
	}
	return tx.Commit(ctx)
}

func semanticHash(value string) bool {
	if len(value) != 64 {
		return false
	}
	if value != strings.ToLower(value) {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}
