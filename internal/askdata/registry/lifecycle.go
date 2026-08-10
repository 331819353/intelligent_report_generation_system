package registry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/platform/database"
	platformidempotency "intelligent-report-generation-system/internal/platform/idempotency"
)

var (
	ErrReleasePreflightFailed = errors.New("semantic release preflight failed")
	ErrReleaseGateFailed      = errors.New("semantic release evaluation gate failed")
	ErrReleaseApprovalFailed  = errors.New("semantic release approval failed")
	ErrReleaseStateConflict   = errors.New("semantic release state version conflict")
)

type ReleasePreflightIssue struct {
	Code            string            `json:"code"`
	ObjectType      ReleaseObjectType `json:"objectType,omitempty"`
	ObjectVersionID string            `json:"objectVersionId,omitempty"`
}

type ReleasePreflightResult struct {
	ReleaseID   string                  `json:"releaseId"`
	ContentHash askdata.ContentHash     `json:"contentHash"`
	ObjectCount int                     `json:"objectCount"`
	Passed      bool                    `json:"passed"`
	Issues      []ReleasePreflightIssue `json:"issues"`
}

type ReleaseProjectionStartResult struct {
	Preflight ReleasePreflightResult `json:"preflight"`
	Status    string                 `json:"status"`
	Started   bool                   `json:"started"`
}

type EvaluationBatchPlanInput struct {
	EvaluationSetID   string `json:"evaluationSetId"`
	EvaluationBatchID string `json:"evaluationBatchId"`
	RunKind           string `json:"runKind"`
}

type EvaluationBatchPlanResult struct {
	ReleaseID         string  `json:"releaseId"`
	EvaluationSetID   string  `json:"evaluationSetId"`
	EvaluationBatchID string  `json:"evaluationBatchId"`
	RunKind           string  `json:"runKind"`
	ShardIDs          []int16 `json:"shardIds"`
	CanIssue95Percent bool    `json:"canIssue95Percent"`
}

type ErrorBudgetAttachmentInput struct {
	EvaluationSetID   string          `json:"evaluationSetId"`
	EvaluationBatchID string          `json:"evaluationBatchId"`
	Report            json.RawMessage `json:"report"`
}

type ReleaseGateInput struct {
	EvaluationSetID   string `json:"evaluationSetId"`
	EvaluationBatchID string `json:"evaluationBatchId"`
}

type ReleaseGateResult struct {
	Passed      bool            `json:"passed"`
	ReceiptHash string          `json:"receiptHash"`
	Failures    []string        `json:"failures"`
	Facts       json.RawMessage `json:"facts"`
}

type ReleaseReviewReportInput struct {
	EvaluationSetID   string          `json:"evaluationSetId"`
	EvaluationBatchID string          `json:"evaluationBatchId"`
	GateReceiptHash   string          `json:"gateReceiptHash"`
	Recommendation    string          `json:"recommendation"`
	Report            json.RawMessage `json:"report"`
}

type ReleaseApprovalInput struct {
	EvaluationSetID   string `json:"evaluationSetId"`
	EvaluationBatchID string `json:"evaluationBatchId"`
	GateReceiptHash   string `json:"gateReceiptHash"`
	ReviewRole        string `json:"reviewRole"`
	Decision          string `json:"decision"`
	CommentHash       string `json:"commentHash"`
}

type ReleaseApprovalResult struct {
	ReleaseID    string `json:"releaseId"`
	ReviewRole   string `json:"reviewRole"`
	Decision     string `json:"decision"`
	ApprovalHash string `json:"approvalHash"`
}

type ReleaseActivationInput struct {
	EvaluationSetID      string `json:"evaluationSetId"`
	EvaluationBatchID    string `json:"evaluationBatchId"`
	ExpectedStateVersion int64  `json:"expectedStateVersion"`
}

type ReleaseActivationResult struct {
	Activated           bool     `json:"activated"`
	ActiveReleaseID     string   `json:"activeReleaseId,omitempty"`
	SupersededReleaseID string   `json:"supersededReleaseId,omitempty"`
	ReleaseStateVersion int64    `json:"releaseStateVersion"`
	GateReceiptHash     string   `json:"gateReceiptHash,omitempty"`
	Failures            []string `json:"failures"`
}

type ReleaseLifecycleSnapshot struct {
	ReleaseID            string             `json:"releaseId"`
	Status               string             `json:"status"`
	ContentHash          string             `json:"contentHash"`
	ReleaseVersion       int64              `json:"releaseVersion"`
	ReleaseStateVersion  int64              `json:"releaseStateVersion"`
	ActiveReleaseID      string             `json:"activeReleaseId,omitempty"`
	ReadyProjectionCount int                `json:"readyProjectionCount"`
	LatestGate           *ReleaseGateResult `json:"latestGate,omitempty"`
	ReviewReportCount    int                `json:"reviewReportCount"`
	ApprovalCount        int                `json:"approvalCount"`
}

type ReleaseLifecycleBackend interface {
	ValidateAndStartProjection(context.Context, AdminScope, string) (ReleaseProjectionStartResult, error)
	PlanEvaluationBatch(context.Context, AdminScope, string, EvaluationBatchPlanInput) (EvaluationBatchPlanResult, error)
	RecordErrorBudget(context.Context, AdminScope, string, ErrorBudgetAttachmentInput) (string, error)
	RecomputeReleaseGate(context.Context, AdminScope, string, ReleaseGateInput) (ReleaseGateResult, error)
	RecordReleaseReviewReport(context.Context, AdminScope, string, ReleaseReviewReportInput) (string, error)
	SubmitReleaseApproval(context.Context, AdminScope, string, ReleaseApprovalInput) (ReleaseApprovalResult, error)
	ActivateRelease(context.Context, AdminScope, string, ReleaseActivationInput) (ReleaseActivationResult, error)
	GetReleaseLifecycle(context.Context, AdminScope, string) (ReleaseLifecycleSnapshot, error)
}

// IdempotencyRepository lets the authenticated HTTP adapter apply the shared
// durable governed-write middleware to release activation without exposing the
// PostgreSQL pool or coupling the registry package to HTTP.
func (store *PostgresStore) IdempotencyRepository() platformidempotency.Repository {
	if store == nil || store.pool == nil {
		return nil
	}
	return platformidempotency.NewPostgresRepository(store.pool)
}

func (store *PostgresStore) ValidateAndStartProjection(
	ctx context.Context,
	scope AdminScope,
	releaseID string,
) (ReleaseProjectionStartResult, error) {
	if err := store.validateLifecycle(ctx, scope, releaseID); err != nil {
		return ReleaseProjectionStartResult{}, err
	}
	var result ReleaseProjectionStartResult
	err := database.WithTenantTx(ctx, store.pool, scope.TenantID, func(tx pgx.Tx) error {
		if err := requireSemanticPermissionTx(ctx, tx, scope, AdminActionRelease, releaseID); err != nil {
			return err
		}
		var releaseHash string
		var releaseStatus string
		if err := tx.QueryRow(ctx, `SELECT content_hash,status FROM askdata.releases
			WHERE id=$1 AND domain_id=$2`, releaseID, scope.DomainID).Scan(&releaseHash, &releaseStatus); err != nil {
			return err
		}
		if releaseStatus != "DRAFT" {
			return ErrRegistryVersionConflict
		}
		objects, err := loadReleaseObjectsTx(ctx, tx, releaseID)
		if err != nil {
			return err
		}
		preflight := EvaluateReleasePreflight(releaseID, askdata.ContentHash(releaseHash), objects)
		result.Preflight = preflight
		if !preflight.Passed {
			return ErrReleasePreflightFailed
		}
		summary, err := json.Marshal(map[string]any{
			"schemaVersion":     "askdata-release-preflight-v1",
			"databaseRechecked": true,
			"contentHash":       preflight.ContentHash,
			"objectCount":       preflight.ObjectCount,
			"checks":            []string{"OBJECT_CONTRACT", "PERMISSION", "SENSITIVITY", "VECTOR_POLICY", "RELATIONSHIP", "DATA_QUALITY"},
		})
		if err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT askdata.start_release_projection($1,$2,$3::jsonb)`,
			releaseID, scope.ActorID, summary).Scan(&result.Started); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT status FROM askdata.releases WHERE id=$1`, releaseID).Scan(&result.Status); err != nil {
			return err
		}
		if !result.Started {
			return ErrReleasePreflightFailed
		}
		return nil
	})
	return result, normalizeLifecycleError(err)
}

func EvaluateReleasePreflight(
	releaseID string,
	releaseHash askdata.ContentHash,
	objects []ReleaseObject,
) ReleasePreflightResult {
	result := ReleasePreflightResult{
		ReleaseID: releaseID, ContentHash: releaseHash, ObjectCount: len(objects),
		Issues: []ReleasePreflightIssue{},
	}
	manifest, err := BuildReleaseManifest(objects)
	if err != nil {
		result.Issues = append(result.Issues, ReleasePreflightIssue{Code: "RELEASE_OBJECT_CONTRACT_INVALID"})
	} else if manifest.ContentHash != releaseHash {
		result.Issues = append(result.Issues, ReleasePreflightIssue{Code: "RELEASE_MANIFEST_HASH_MISMATCH"})
	}
	for _, object := range objects {
		switch object.Type {
		case ReleaseObjectDimension, ReleaseObjectMember:
			var policy struct {
				Sensitivity       Sensitivity       `json:"sensitivity"`
				MemberIndexPolicy MemberIndexPolicy `json:"memberIndexPolicy"`
			}
			if json.Unmarshal(object.Contract, &policy) != nil {
				result.Issues = append(result.Issues, lifecycleIssue("RELEASE_SENSITIVITY_CONTRACT_INVALID", object))
				continue
			}
			if (object.Sensitivity == SensitivityRestricted || policy.Sensitivity == SensitivityRestricted) &&
				(policy.MemberIndexPolicy == MemberIndexFull || policy.MemberIndexPolicy == MemberIndexOnDemand) {
				result.Issues = append(result.Issues, lifecycleIssue("RELEASE_RESTRICTED_VECTOR_POLICY", object))
			}
		case ReleaseObjectRelationship:
			var relationship struct {
				Cardinality  Cardinality  `json:"cardinality"`
				FanoutPolicy FanoutPolicy `json:"fanoutPolicy"`
			}
			if json.Unmarshal(object.Contract, &relationship) != nil ||
				((relationship.Cardinality == CardinalityOneToMany || relationship.Cardinality == CardinalityManyToMany) &&
					relationship.FanoutPolicy != FanoutPreAggregateRequired && relationship.FanoutPolicy != FanoutBridgeRequired) {
				result.Issues = append(result.Issues, lifecycleIssue("RELEASE_RELATIONSHIP_FANOUT_UNSAFE", object))
			}
		case ReleaseObjectQualityRule:
			var quality struct {
				Severity string `json:"severity"`
				RuleAST  any    `json:"ruleAst"`
			}
			if json.Unmarshal(object.Contract, &quality) != nil || strings.TrimSpace(quality.Severity) == "" || quality.RuleAST == nil {
				result.Issues = append(result.Issues, lifecycleIssue("RELEASE_QUALITY_RULE_INVALID", object))
			}
		}
	}
	sort.Slice(result.Issues, func(i, j int) bool {
		if result.Issues[i].Code != result.Issues[j].Code {
			return result.Issues[i].Code < result.Issues[j].Code
		}
		return result.Issues[i].ObjectVersionID < result.Issues[j].ObjectVersionID
	})
	result.Passed = len(result.Issues) == 0
	return result
}

func lifecycleIssue(code string, object ReleaseObject) ReleasePreflightIssue {
	return ReleasePreflightIssue{Code: code, ObjectType: object.Type, ObjectVersionID: object.ObjectVersionID}
}

func (store *PostgresStore) PlanEvaluationBatch(
	ctx context.Context, scope AdminScope, releaseID string, input EvaluationBatchPlanInput,
) (EvaluationBatchPlanResult, error) {
	if err := store.validateLifecycleInput(ctx, scope, releaseID, input.EvaluationSetID, input.EvaluationBatchID); err != nil {
		return EvaluationBatchPlanResult{}, err
	}
	var result EvaluationBatchPlanResult
	err := store.withReleasePermission(ctx, scope, releaseID, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT askdata.plan_evaluation_batch($1,$2,$3,$4)`,
			input.EvaluationSetID, input.EvaluationBatchID, input.RunKind, scope.ActorID).Scan(&result.ShardIDs); err != nil {
			return err
		}
		result.ReleaseID, result.EvaluationSetID = releaseID, input.EvaluationSetID
		result.EvaluationBatchID, result.RunKind = input.EvaluationBatchID, input.RunKind
		result.CanIssue95Percent = len(result.ShardIDs) == 4
		return nil
	})
	return result, normalizeLifecycleError(err)
}

func (store *PostgresStore) RecordErrorBudget(
	ctx context.Context, scope AdminScope, releaseID string, input ErrorBudgetAttachmentInput,
) (string, error) {
	if err := store.validateLifecycleInput(ctx, scope, releaseID, input.EvaluationSetID, input.EvaluationBatchID); err != nil {
		return "", err
	}
	if len(input.Report) < 2 || len(input.Report) > 131072 || !json.Valid(input.Report) {
		return "", ErrRegistryInvalidRequest
	}
	var reportHash string
	err := store.withReleasePermission(ctx, scope, releaseID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT askdata.record_release_error_budget($1,$2,$3,$4::jsonb,$5)`,
			releaseID, input.EvaluationSetID, input.EvaluationBatchID, input.Report, scope.ActorID).Scan(&reportHash)
	})
	return reportHash, normalizeLifecycleError(err)
}

func (store *PostgresStore) RecomputeReleaseGate(
	ctx context.Context, scope AdminScope, releaseID string, input ReleaseGateInput,
) (ReleaseGateResult, error) {
	if err := store.validateLifecycleInput(ctx, scope, releaseID, input.EvaluationSetID, input.EvaluationBatchID); err != nil {
		return ReleaseGateResult{}, err
	}
	var result ReleaseGateResult
	err := store.withReleasePermission(ctx, scope, releaseID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT * FROM askdata.recompute_release_evaluation_gate($1,$2,$3,$4)`,
			releaseID, input.EvaluationSetID, input.EvaluationBatchID, scope.ActorID).
			Scan(&result.Passed, &result.ReceiptHash, &result.Failures, &result.Facts)
	})
	return result, normalizeLifecycleError(err)
}

func (store *PostgresStore) RecordReleaseReviewReport(
	ctx context.Context, scope AdminScope, releaseID string, input ReleaseReviewReportInput,
) (string, error) {
	if err := store.validateLifecycleInput(ctx, scope, releaseID, input.EvaluationSetID, input.EvaluationBatchID); err != nil {
		return "", err
	}
	if !validLifecycleHash(input.GateReceiptHash) || len(input.Report) < 2 || len(input.Report) > 131072 || !json.Valid(input.Report) {
		return "", ErrRegistryInvalidRequest
	}
	var reportHash string
	err := store.withReleasePermission(ctx, scope, releaseID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT askdata.record_release_review_report($1,$2,$3,$4,$5,$6::jsonb,$7)`,
			releaseID, input.EvaluationSetID, input.EvaluationBatchID, input.GateReceiptHash,
			input.Recommendation, input.Report, scope.ActorID).Scan(&reportHash)
	})
	return reportHash, normalizeLifecycleError(err)
}

func (store *PostgresStore) SubmitReleaseApproval(
	ctx context.Context, scope AdminScope, releaseID string, input ReleaseApprovalInput,
) (ReleaseApprovalResult, error) {
	if err := store.validateLifecycleInput(ctx, scope, releaseID, input.EvaluationSetID, input.EvaluationBatchID); err != nil {
		return ReleaseApprovalResult{}, err
	}
	if !validLifecycleHash(input.GateReceiptHash) || !validLifecycleHash(input.CommentHash) {
		return ReleaseApprovalResult{}, ErrRegistryInvalidRequest
	}
	result := ReleaseApprovalResult{ReleaseID: releaseID, ReviewRole: input.ReviewRole, Decision: input.Decision}
	err := store.withReleasePermission(ctx, scope, releaseID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT askdata.submit_release_approval($1,$2,$3,$4,$5,$6,$7,$8)`,
			releaseID, input.EvaluationSetID, input.EvaluationBatchID, input.GateReceiptHash,
			input.ReviewRole, input.Decision, input.CommentHash, scope.ActorID).Scan(&result.ApprovalHash)
	})
	return result, normalizeLifecycleError(err)
}

func (store *PostgresStore) ActivateRelease(
	ctx context.Context, scope AdminScope, releaseID string, input ReleaseActivationInput,
) (ReleaseActivationResult, error) {
	if err := store.validateLifecycleInput(ctx, scope, releaseID, input.EvaluationSetID, input.EvaluationBatchID); err != nil {
		return ReleaseActivationResult{}, err
	}
	if input.ExpectedStateVersion < 1 {
		return ReleaseActivationResult{}, ErrRegistryInvalidRequest
	}
	var result ReleaseActivationResult
	var activeID, supersededID, gateReceiptHash *string
	err := store.withReleasePermission(ctx, scope, releaseID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT * FROM askdata.activate_release($1,$2,$3,$4,$5)`,
			releaseID, input.EvaluationSetID, input.EvaluationBatchID, scope.ActorID,
			input.ExpectedStateVersion).Scan(&result.Activated, &activeID, &supersededID,
			&result.ReleaseStateVersion, &gateReceiptHash, &result.Failures)
	})
	if activeID != nil {
		result.ActiveReleaseID = *activeID
	}
	if supersededID != nil {
		result.SupersededReleaseID = *supersededID
	}
	if gateReceiptHash != nil {
		result.GateReceiptHash = *gateReceiptHash
	}
	if err == nil && !result.Activated {
		if containsLifecycleFailure(result.Failures, "RELEASE_STATE_VERSION_CONFLICT") {
			return result, ErrReleaseStateConflict
		}
		if containsLifecycleFailure(result.Failures, "RELEASE_APPROVALS_REQUIRED") {
			return result, ErrReleaseApprovalFailed
		}
		return result, ErrReleaseGateFailed
	}
	return result, normalizeLifecycleError(err)
}

func (store *PostgresStore) GetReleaseLifecycle(
	ctx context.Context, scope AdminScope, releaseID string,
) (ReleaseLifecycleSnapshot, error) {
	if err := store.validateLifecycle(ctx, scope, releaseID); err != nil {
		return ReleaseLifecycleSnapshot{}, err
	}
	var result ReleaseLifecycleSnapshot
	err := database.WithTenantTx(ctx, store.pool, scope.TenantID, func(tx pgx.Tx) error {
		if err := requireSemanticPermissionTx(ctx, tx, scope, AdminActionView, releaseID); err != nil {
			return err
		}
		var activeID *string
		if err := tx.QueryRow(ctx, `SELECT release.id::text,release.status,release.content_hash,
			release.version,state.version,state.active_release_id::text,
			(SELECT count(*) FROM askdata.release_projections AS projection
			 WHERE projection.release_id=release.id AND projection.status='READY'
			   AND projection.expected_content_hash=release.content_hash
			   AND projection.applied_content_hash=release.content_hash),
			(SELECT count(*) FROM askdata.release_review_reports AS report WHERE report.release_id=release.id),
			(SELECT count(*) FROM askdata.release_approvals AS approval WHERE approval.release_id=release.id)
		FROM askdata.releases AS release
		JOIN askdata.release_state AS state ON state.tenant_id=release.tenant_id AND state.domain_id=release.domain_id
		WHERE release.id=$1 AND release.domain_id=$2`, releaseID, scope.DomainID).Scan(
			&result.ReleaseID, &result.Status, &result.ContentHash, &result.ReleaseVersion,
			&result.ReleaseStateVersion, &activeID, &result.ReadyProjectionCount,
			&result.ReviewReportCount, &result.ApprovalCount); err != nil {
			return err
		}
		if activeID != nil {
			result.ActiveReleaseID = *activeID
		}
		var latest ReleaseGateResult
		err := tx.QueryRow(ctx, `SELECT passed,receipt_hash,failure_codes,facts_json
			FROM askdata.release_evaluation_gate_receipts
			WHERE release_id=$1 ORDER BY recomputed_at DESC,id DESC LIMIT 1`, releaseID).
			Scan(&latest.Passed, &latest.ReceiptHash, &latest.Failures, &latest.Facts)
		if err == nil {
			result.LatestGate = &latest
			return nil
		}
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return err
	})
	return result, normalizeLifecycleError(err)
}

func (store *PostgresStore) withReleasePermission(
	ctx context.Context, scope AdminScope, releaseID string, execute func(pgx.Tx) error,
) error {
	return database.WithTenantTx(ctx, store.pool, scope.TenantID, func(tx pgx.Tx) error {
		if err := requireSemanticPermissionTx(ctx, tx, scope, AdminActionRelease, releaseID); err != nil {
			return err
		}
		return execute(tx)
	})
}

func (store *PostgresStore) validateLifecycle(ctx context.Context, scope AdminScope, releaseID string) error {
	if store == nil || store.pool == nil {
		return errors.New("semantic registry PostgreSQL store is not configured")
	}
	if err := scope.Validate(ctx); err != nil {
		return err
	}
	if !canonicalAdminUUID(releaseID) {
		return ErrRegistryInvalidRequest
	}
	return nil
}

func (store *PostgresStore) validateLifecycleInput(
	ctx context.Context, scope AdminScope, releaseID string, identifiers ...string,
) error {
	if err := store.validateLifecycle(ctx, scope, releaseID); err != nil {
		return err
	}
	for _, identifier := range identifiers {
		if !canonicalAdminUUID(identifier) {
			return ErrRegistryInvalidRequest
		}
	}
	return nil
}

func loadReleaseObjectsTx(ctx context.Context, tx pgx.Tx, releaseID string) ([]ReleaseObject, error) {
	rows, err := tx.Query(ctx, `SELECT object_type,object_id::text,object_version_id::text,
		content_hash,sensitivity,contract_json FROM askdata.release_objects
		WHERE release_id=$1 ORDER BY object_type,object_id,object_version_id`, releaseID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	objects := []ReleaseObject{}
	for rows.Next() {
		var object ReleaseObject
		if err := rows.Scan(&object.Type, &object.ObjectID, &object.ObjectVersionID,
			&object.ContentHash, &object.Sensitivity, &object.Contract); err != nil {
			return nil, err
		}
		objects = append(objects, object)
	}
	return objects, rows.Err()
}

func normalizeLifecycleError(err error) error {
	if err == nil || errors.Is(err, ErrRegistryInvalidRequest) || errors.Is(err, ErrRegistryPermissionDenied) ||
		errors.Is(err, ErrRegistryNotFound) || errors.Is(err, ErrRegistryVersionConflict) ||
		errors.Is(err, ErrReleasePreflightFailed) || errors.Is(err, ErrReleaseGateFailed) ||
		errors.Is(err, ErrReleaseApprovalFailed) || errors.Is(err, ErrReleaseStateConflict) {
		return err
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrRegistryNotFound
	}
	var postgresError *pgconn.PgError
	if errors.As(err, &postgresError) {
		switch {
		case postgresError.Code == "42501":
			return ErrRegistryPermissionDenied
		case strings.Contains(postgresError.Message, "RELEASE_APPROVAL"):
			return fmt.Errorf("%w: %s", ErrReleaseApprovalFailed, postgresError.Message)
		case strings.Contains(postgresError.Message, "EVAL_GATE") || strings.Contains(postgresError.Message, "RELEASE_REVIEW"):
			return fmt.Errorf("%w: %s", ErrReleaseGateFailed, postgresError.Message)
		case postgresError.Code == "40001":
			return ErrReleaseStateConflict
		case postgresError.Code == "22023" || postgresError.Code == "23514" || postgresError.Code == "55000":
			return fmt.Errorf("%w: %s", ErrRegistryInvalidRequest, postgresError.Message)
		}
	}
	return err
}

func containsLifecycleFailure(failures []string, target string) bool {
	for _, failure := range failures {
		if failure == target {
			return true
		}
	}
	return false
}

func validLifecycleHash(value string) bool {
	return len(value) == 64 && askdata.ContentHash(value).Validate() == nil
}

var _ ReleaseLifecycleBackend = (*PostgresStore)(nil)
