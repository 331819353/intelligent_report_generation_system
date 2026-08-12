package registry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
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
	ErrReleaseRolloutInvalid  = errors.New("semantic release rollout is invalid")
	ErrReleaseRollbackInvalid = errors.New("semantic release rollback is invalid")
	ErrReleaseRetireBlocked   = errors.New("semantic release retirement is blocked")
	ErrReleaseRetentionOpen   = errors.New("semantic release retention window is still open")
)

// ReleasePreflightError preserves the machine-readable blockers so the UI can
// route an owner to the exact object and safe remediation instead of showing a
// generic 422 error.
type ReleasePreflightError struct {
	Result ReleasePreflightResult
}

func (err *ReleasePreflightError) Error() string { return ErrReleasePreflightFailed.Error() }
func (err *ReleasePreflightError) Unwrap() error { return ErrReleasePreflightFailed }

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

type ReleaseProjectionRetryResult struct {
	ReleaseID    string `json:"releaseId"`
	Status       string `json:"status"`
	RetriedCount int    `json:"retriedCount"`
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
	Passed            bool            `json:"passed"`
	ReceiptHash       string          `json:"receiptHash"`
	Failures          []string        `json:"failures"`
	Facts             json.RawMessage `json:"facts"`
	EvaluationSetID   string          `json:"evaluationSetId,omitempty"`
	EvaluationBatchID string          `json:"evaluationBatchId,omitempty"`
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

type ReleaseApprovalRecoveryInput struct {
	GateReceiptHash string `json:"gateReceiptHash"`
	ReviewRole      string `json:"reviewRole,omitempty"`
	ReasonHash      string `json:"reasonHash"`
}

type ReleaseApprovalRecoveryResult struct {
	ReleaseID       string `json:"releaseId"`
	WithdrawalID    string `json:"withdrawalId,omitempty"`
	ResetCount      int    `json:"resetCount,omitempty"`
	EscalationLevel int    `json:"escalationLevel,omitempty"`
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

type ReleaseRolloutSnapshot struct {
	ID                 string    `json:"id,omitempty"`
	CandidateReleaseID string    `json:"candidateReleaseId"`
	ControlReleaseID   string    `json:"controlReleaseId,omitempty"`
	Stage              string    `json:"stage"`
	State              string    `json:"state"`
	CanaryPercent      int       `json:"canaryPercent"`
	Version            int64     `json:"version"`
	StartedAt          time.Time `json:"startedAt"`
	StageStartedAt     time.Time `json:"stageStartedAt"`
	UpdatedAt          time.Time `json:"updatedAt"`
}

type ReleaseRolloutMutationInput struct {
	ExpectedVersion int64  `json:"expectedVersion,omitempty"`
	ReasonHash      string `json:"reasonHash"`
}

type ReleaseOperationalImpact struct {
	ReleaseID            string                  `json:"releaseId"`
	Status               string                  `json:"status"`
	RetentionUntil       string                  `json:"retentionUntil,omitempty"`
	CanRetire            bool                    `json:"canRetire"`
	BlockedCode          string                  `json:"blockedCode,omitempty"`
	ActiveReferenceCount int                     `json:"activeReferenceCount"`
	References           []ReleaseReference      `json:"references"`
	Rollout              *ReleaseRolloutSnapshot `json:"rollout,omitempty"`
	Observability        json.RawMessage         `json:"observability,omitempty"`
}

type ReleaseRollbackInput struct {
	ExpectedStateVersion int64  `json:"expectedStateVersion"`
	ReasonHash           string `json:"reasonHash"`
}

type ReleaseRollbackResult struct {
	RolledBack        bool   `json:"rolledBack"`
	ActiveReleaseID   string `json:"activeReleaseId"`
	ReplacedReleaseID string `json:"replacedReleaseId"`
	StateVersion      int64  `json:"releaseStateVersion"`
}

type ReleaseLifecycleSnapshot struct {
	ReleaseID            string                      `json:"releaseId"`
	Status               string                      `json:"status"`
	ContentHash          string                      `json:"contentHash"`
	ReleaseVersion       int64                       `json:"releaseVersion"`
	ReleaseStateVersion  int64                       `json:"releaseStateVersion"`
	ActiveReleaseID      string                      `json:"activeReleaseId,omitempty"`
	ReadyProjectionCount int                         `json:"readyProjectionCount"`
	LatestGate           *ReleaseGateResult          `json:"latestGate,omitempty"`
	ReviewReportCount    int                         `json:"reviewReportCount"`
	ApprovalCount        int                         `json:"approvalCount"`
	ApprovedRoles        []string                    `json:"approvedRoles"`
	ActorHasApproved     bool                        `json:"actorHasApproved"`
	RejectionCount       int                         `json:"rejectionCount"`
	RejectedRoles        []string                    `json:"rejectedRoles"`
	ActorApprovalRole    string                      `json:"actorApprovalRole,omitempty"`
	ApprovalDueAt        string                      `json:"approvalDueAt,omitempty"`
	ApprovalSLAStatus    string                      `json:"approvalSlaStatus"`
	EscalationLevel      int                         `json:"escalationLevel"`
	Projections          []ReleaseProjectionSnapshot `json:"projections"`
}

type ReleaseProjectionSnapshot struct {
	Target              string `json:"target"`
	Status              string `json:"status"`
	ExpectedContentHash string `json:"expectedContentHash"`
	AppliedContentHash  string `json:"appliedContentHash,omitempty"`
	Attempt             int    `json:"attempt"`
	MaxAttempts         int    `json:"maxAttempts"`
	ErrorCode           string `json:"errorCode,omitempty"`
	HashMatched         bool   `json:"hashMatched"`
}

type ReleaseLifecycleBackend interface {
	ValidateAndStartProjection(context.Context, AdminScope, string) (ReleaseProjectionStartResult, error)
	RetryFailedProjections(context.Context, AdminScope, string) (ReleaseProjectionRetryResult, error)
	PlanEvaluationBatch(context.Context, AdminScope, string, EvaluationBatchPlanInput) (EvaluationBatchPlanResult, error)
	RecordErrorBudget(context.Context, AdminScope, string, ErrorBudgetAttachmentInput) (string, error)
	RecomputeReleaseGate(context.Context, AdminScope, string, ReleaseGateInput) (ReleaseGateResult, error)
	RecordReleaseReviewReport(context.Context, AdminScope, string, ReleaseReviewReportInput) (string, error)
	SubmitReleaseApproval(context.Context, AdminScope, string, ReleaseApprovalInput) (ReleaseApprovalResult, error)
	WithdrawReleaseApproval(context.Context, AdminScope, string, ReleaseApprovalRecoveryInput) (ReleaseApprovalRecoveryResult, error)
	ResetRejectedReleaseApprovals(context.Context, AdminScope, string, ReleaseApprovalRecoveryInput) (ReleaseApprovalRecoveryResult, error)
	EscalateReleaseApproval(context.Context, AdminScope, string, ReleaseApprovalRecoveryInput) (ReleaseApprovalRecoveryResult, error)
	ActivateRelease(context.Context, AdminScope, string, ReleaseActivationInput) (ReleaseActivationResult, error)
	GetReleaseOperationalImpact(context.Context, AdminScope, string) (ReleaseOperationalImpact, error)
	StartReleaseRollout(context.Context, AdminScope, string, ReleaseRolloutMutationInput) (ReleaseRolloutSnapshot, error)
	AdvanceReleaseRollout(context.Context, AdminScope, string, ReleaseRolloutMutationInput) (ReleaseRolloutSnapshot, error)
	PauseReleaseRollout(context.Context, AdminScope, string, ReleaseRolloutMutationInput) (ReleaseRolloutSnapshot, error)
	ResumeReleaseRollout(context.Context, AdminScope, string, ReleaseRolloutMutationInput) (ReleaseRolloutSnapshot, error)
	StopReleaseRollout(context.Context, AdminScope, string, ReleaseRolloutMutationInput) (ReleaseRolloutSnapshot, error)
	RollbackRelease(context.Context, AdminScope, string, ReleaseRollbackInput) (ReleaseRollbackResult, error)
	RetireRelease(context.Context, AdminScope, string) error
	GetReleaseLifecycle(context.Context, AdminScope, string) (ReleaseLifecycleSnapshot, error)
}

func (store *PostgresStore) RetryFailedProjections(
	ctx context.Context, scope AdminScope, releaseID string,
) (ReleaseProjectionRetryResult, error) {
	if err := store.validateLifecycle(ctx, scope, releaseID); err != nil {
		return ReleaseProjectionRetryResult{}, err
	}
	result := ReleaseProjectionRetryResult{ReleaseID: releaseID}
	err := store.withReleasePermission(ctx, scope, releaseID, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT askdata.retry_failed_release_projections($1,$2)`,
			releaseID, scope.ActorID).Scan(&result.RetriedCount); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `SELECT status FROM askdata.releases WHERE id=$1`, releaseID).Scan(&result.Status)
	})
	return result, normalizeLifecycleError(err)
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
			return &ReleasePreflightError{Result: preflight}
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
		return tx.QueryRow(ctx, `SELECT askdata.submit_release_approval_v2($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
			releaseID, input.EvaluationSetID, input.EvaluationBatchID, input.GateReceiptHash,
			input.ReviewRole, input.Decision, input.CommentHash, scope.ActorID, uuid.NewString()).Scan(&result.ApprovalHash)
	})
	return result, normalizeLifecycleError(err)
}

func (store *PostgresStore) WithdrawReleaseApproval(ctx context.Context, scope AdminScope, releaseID string, input ReleaseApprovalRecoveryInput) (ReleaseApprovalRecoveryResult, error) {
	if err := store.validateLifecycle(ctx, scope, releaseID); err != nil || !validLifecycleHash(input.GateReceiptHash) || !validLifecycleHash(input.ReasonHash) {
		if err != nil {
			return ReleaseApprovalRecoveryResult{}, err
		}
		return ReleaseApprovalRecoveryResult{}, ErrRegistryInvalidRequest
	}
	result := ReleaseApprovalRecoveryResult{ReleaseID: releaseID}
	err := store.withReleasePermission(ctx, scope, releaseID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT askdata.withdraw_release_approval($1,$2,$3,$4,$5)`, releaseID, input.GateReceiptHash, input.ReviewRole, input.ReasonHash, scope.ActorID).Scan(&result.WithdrawalID)
	})
	return result, normalizeLifecycleError(err)
}

func (store *PostgresStore) ResetRejectedReleaseApprovals(ctx context.Context, scope AdminScope, releaseID string, input ReleaseApprovalRecoveryInput) (ReleaseApprovalRecoveryResult, error) {
	if err := store.validateLifecycle(ctx, scope, releaseID); err != nil || !validLifecycleHash(input.GateReceiptHash) || !validLifecycleHash(input.ReasonHash) {
		if err != nil {
			return ReleaseApprovalRecoveryResult{}, err
		}
		return ReleaseApprovalRecoveryResult{}, ErrRegistryInvalidRequest
	}
	result := ReleaseApprovalRecoveryResult{ReleaseID: releaseID}
	err := store.withReleasePermission(ctx, scope, releaseID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT askdata.reset_rejected_release_approvals($1,$2,$3,$4)`, releaseID, input.GateReceiptHash, input.ReasonHash, scope.ActorID).Scan(&result.ResetCount)
	})
	return result, normalizeLifecycleError(err)
}

func (store *PostgresStore) EscalateReleaseApproval(ctx context.Context, scope AdminScope, releaseID string, input ReleaseApprovalRecoveryInput) (ReleaseApprovalRecoveryResult, error) {
	if err := store.validateLifecycle(ctx, scope, releaseID); err != nil || !validLifecycleHash(input.GateReceiptHash) || !validLifecycleHash(input.ReasonHash) {
		if err != nil {
			return ReleaseApprovalRecoveryResult{}, err
		}
		return ReleaseApprovalRecoveryResult{}, ErrRegistryInvalidRequest
	}
	result := ReleaseApprovalRecoveryResult{ReleaseID: releaseID}
	err := store.withReleasePermission(ctx, scope, releaseID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT askdata.escalate_release_approval($1,$2,$3,$4)`, releaseID, input.GateReceiptHash, input.ReasonHash, scope.ActorID).Scan(&result.EscalationLevel)
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
		var approvalCount int
		if err := tx.QueryRow(ctx, `SELECT askdata.active_release_approval_count($1,(
			SELECT receipt_hash FROM askdata.release_evaluation_gate_receipts
			WHERE release_id=$1 AND evaluation_set_id=$2 AND evaluation_batch_id=$3
			ORDER BY recomputed_at DESC,id DESC LIMIT 1
		))`, releaseID, input.EvaluationSetID, input.EvaluationBatchID).Scan(&approvalCount); err != nil {
			// The authoritative activation procedure still rechecks all gates. This
			// helper prevents a withdrawn approval from reaching it as a candidate.
			if !strings.Contains(err.Error(), "function askdata.active_release_approval_count") {
				return err
			}
		} else if approvalCount != 2 {
			return ErrReleaseApprovalFailed
		}
		var rolloutID, rolloutReasonHash string
		if err := tx.QueryRow(ctx, `SELECT id::text,reason_hash FROM askdata.release_rollouts
			WHERE tenant_id=$1 AND domain_id=$2 AND candidate_release_id=$3
			  AND stage='ACCEPTED_95' AND state='ACCEPTED'
			ORDER BY accepted_at DESC,id DESC LIMIT 1 FOR UPDATE`, scope.TenantID, scope.DomainID, releaseID).
			Scan(&rolloutID, &rolloutReasonHash); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrReleaseRolloutInvalid
			}
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT * FROM askdata.activate_release($1,$2,$3,$4,$5)`,
			releaseID, input.EvaluationSetID, input.EvaluationBatchID, scope.ActorID,
			input.ExpectedStateVersion).Scan(&result.Activated, &activeID, &supersededID,
			&result.ReleaseStateVersion, &gateReceiptHash, &result.Failures); err != nil || !result.Activated {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE askdata.release_rollouts SET state='COMPLETED',completed_at=clock_timestamp(),
			updated_at=clock_timestamp(),updated_by=$1,version=version+1 WHERE id=$2 AND state='ACCEPTED'`,
			scope.ActorID, rolloutID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO askdata.release_rollout_events(
			tenant_id,domain_id,rollout_id,candidate_release_id,event_type,from_stage,to_stage,actor_id,reason_hash,detail
		) VALUES($1,$2,$3,$4,'ACTIVATED','ACCEPTED_95','ACCEPTED_95',$5,$6,
			jsonb_build_object('releaseStateVersion',$7))`, scope.TenantID, scope.DomainID,
			rolloutID, releaseID, scope.ActorID, rolloutReasonHash, result.ReleaseStateVersion)
		return err
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

func (store *PostgresStore) GetReleaseOperationalImpact(
	ctx context.Context, scope AdminScope, releaseID string,
) (ReleaseOperationalImpact, error) {
	if err := store.validateLifecycle(ctx, scope, releaseID); err != nil {
		return ReleaseOperationalImpact{}, err
	}
	result := ReleaseOperationalImpact{ReleaseID: releaseID, References: []ReleaseReference{}}
	err := database.WithTenantTx(ctx, store.pool, scope.TenantID, func(tx pgx.Tx) error {
		if err := requireSemanticPermissionTx(ctx, tx, scope, AdminActionView, releaseID); err != nil {
			return err
		}
		var retentionUntil *time.Time
		if err := tx.QueryRow(ctx, `SELECT status,retention_until FROM askdata.releases
			WHERE id=$1 AND domain_id=$2`, releaseID, scope.DomainID).Scan(&result.Status, &retentionUntil); err != nil {
			return err
		}
		if retentionUntil != nil {
			result.RetentionUntil = retentionUntil.UTC().Format(time.RFC3339Nano)
		}
		rows, err := tx.Query(ctx, `SELECT reference.id::text,reference.tenant_id::text,
			release.domain_id::text,reference.release_id::text,reference.reference_type,
			reference.reference_id::text,reference.reference_name,reference.owner_id::text,
			reference.created_at
		FROM askdata.release_references AS reference
		JOIN askdata.releases AS release ON release.id=reference.release_id AND release.tenant_id=reference.tenant_id
		WHERE reference.tenant_id=$1 AND reference.release_id=$2 AND release.domain_id=$3
		  AND reference.released_at IS NULL
		ORDER BY reference.reference_type,reference.reference_name,reference.reference_id`,
			scope.TenantID, releaseID, scope.DomainID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var reference ReleaseReference
			if err := rows.Scan(&reference.ID, &reference.TenantID, &reference.DomainID,
				&reference.ReleaseID, &reference.Type, &reference.ReferenceID,
				&reference.ReferenceName, &reference.OwnerID, &reference.CreatedAt); err != nil {
				return err
			}
			result.References = append(result.References, reference)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		result.ActiveReferenceCount = len(result.References)
		result.CanRetire = (result.Status == "SUPERSEDED" || result.Status == "RETAINED") && result.ActiveReferenceCount == 0 &&
			(result.Status != "RETAINED" || retentionUntil == nil || !time.Now().Before(*retentionUntil))
		if result.ActiveReferenceCount > 0 {
			result.BlockedCode = ReleaseRetireBlockedCode
		} else if result.Status == "RETAINED" && retentionUntil != nil && time.Now().Before(*retentionUntil) {
			result.BlockedCode = ReleaseRetentionNotExpiredCode
		} else if result.Status != "SUPERSEDED" && result.Status != "RETAINED" {
			result.BlockedCode = "RELEASE_RETIRE_STATE_INVALID"
		}
		rollout, found, err := loadReleaseRolloutTx(ctx, tx, releaseID)
		if err != nil {
			return err
		}
		if found {
			result.Rollout = &rollout
			if err := tx.QueryRow(ctx, `SELECT askdata.release_rollout_observability($1)`, rollout.ID).
				Scan(&result.Observability); err != nil {
				return err
			}
		}
		return nil
	})
	return result, normalizeLifecycleError(err)
}

func (store *PostgresStore) StartReleaseRollout(ctx context.Context, scope AdminScope, releaseID string, input ReleaseRolloutMutationInput) (ReleaseRolloutSnapshot, error) {
	if err := store.validateLifecycle(ctx, scope, releaseID); err != nil || !validLifecycleHash(input.ReasonHash) {
		if err != nil {
			return ReleaseRolloutSnapshot{}, err
		}
		return ReleaseRolloutSnapshot{}, ErrRegistryInvalidRequest
	}
	var result ReleaseRolloutSnapshot
	err := store.withReleasePermission(ctx, scope, releaseID, func(tx pgx.Tx) error {
		var status, contentHash, controlID string
		if err := tx.QueryRow(ctx, `SELECT release.status,release.content_hash,state.active_release_id::text
			FROM askdata.releases AS release JOIN askdata.release_state AS state
			ON state.tenant_id=release.tenant_id AND state.domain_id=release.domain_id
			WHERE release.id=$1 AND release.domain_id=$2 FOR UPDATE OF release,state`, releaseID, scope.DomainID).
			Scan(&status, &contentHash, &controlID); err != nil {
			return err
		}
		if status != "READY" || controlID == "" || controlID == releaseID {
			return ErrReleaseRolloutInvalid
		}
		rolloutID := uuid.NewString()
		if err := tx.QueryRow(ctx, `INSERT INTO askdata.release_rollouts(
			id,tenant_id,domain_id,candidate_release_id,control_release_id,stage,state,
			canary_percent,salt_hash,reason_hash,started_by,updated_by
		) VALUES($1,$2,$3,$4,$5,'SHADOW','RUNNING',0,$6,$7,$8,$8)
		ON CONFLICT(tenant_id,candidate_release_id,reason_hash) DO UPDATE SET updated_at=askdata.release_rollouts.updated_at
		RETURNING id::text,candidate_release_id::text,control_release_id::text,stage,state,
			canary_percent,version,started_at,stage_started_at,updated_at`,
			rolloutID, scope.TenantID, scope.DomainID, releaseID, controlID,
			askdata.HashBytes([]byte("release-rollout-salt-v1\x00"+contentHash+"\x00"+rolloutID)), input.ReasonHash, scope.ActorID).
			Scan(&result.ID, &result.CandidateReleaseID, &result.ControlReleaseID, &result.Stage,
				&result.State, &result.CanaryPercent, &result.Version,
				&result.StartedAt, &result.StageStartedAt, &result.UpdatedAt); err != nil {
			return err
		}
		if result.ID != rolloutID {
			return nil
		}
		_, err := tx.Exec(ctx, `INSERT INTO askdata.release_rollout_events(
			tenant_id,domain_id,rollout_id,candidate_release_id,event_type,to_stage,actor_id,reason_hash,detail
		) VALUES($1,$2,$3,$4,'STARTED','SHADOW',$5,$6,jsonb_build_object('controlReleaseId',$7))`,
			scope.TenantID, scope.DomainID, result.ID, releaseID, scope.ActorID, input.ReasonHash, controlID)
		return err
	})
	return result, normalizeLifecycleError(err)
}

func (store *PostgresStore) mutateReleaseRollout(ctx context.Context, scope AdminScope, releaseID, action string, input ReleaseRolloutMutationInput) (ReleaseRolloutSnapshot, error) {
	if err := store.validateLifecycle(ctx, scope, releaseID); err != nil || !validLifecycleHash(input.ReasonHash) || input.ExpectedVersion < 1 {
		if err != nil {
			return ReleaseRolloutSnapshot{}, err
		}
		return ReleaseRolloutSnapshot{}, ErrRegistryInvalidRequest
	}
	var result ReleaseRolloutSnapshot
	err := store.withReleasePermission(ctx, scope, releaseID, func(tx pgx.Tx) error {
		var id, stage, state string
		var version int64
		if err := tx.QueryRow(ctx, `SELECT id::text,stage,state,version FROM askdata.release_rollouts
			WHERE tenant_id=$1 AND domain_id=$2 AND candidate_release_id=$3
			ORDER BY updated_at DESC,id DESC LIMIT 1 FOR UPDATE`, scope.TenantID, scope.DomainID, releaseID).
			Scan(&id, &stage, &state, &version); err != nil {
			return err
		}
		if version != input.ExpectedVersion {
			return ErrReleaseStateConflict
		}
		nextStage, nextState, percent := stage, state, 0
		now := time.Now().UTC()
		var setPaused, setStopped any
		switch action {
		case "ADVANCE":
			if state != "RUNNING" {
				return ErrReleaseRolloutInvalid
			}
			var advanceAllowed bool
			if err := tx.QueryRow(ctx, `SELECT COALESCE((askdata.release_rollout_observability($1)->>'advanceAllowed')::boolean,false)`, id).
				Scan(&advanceAllowed); err != nil {
				return err
			}
			if !advanceAllowed {
				return ErrReleaseRolloutInvalid
			}
			switch stage {
			case "SHADOW":
				nextStage, percent = "CANARY_5", 5
			case "CANARY_5":
				nextStage, percent = "CANARY_20", 20
			case "CANARY_20":
				nextStage, percent = "CANARY_50", 50
			case "CANARY_50":
				nextStage, nextState, percent = "ACCEPTED_95", "ACCEPTED", 95
			default:
				return ErrReleaseRolloutInvalid
			}
		case "PAUSE":
			if state != "RUNNING" {
				return ErrReleaseRolloutInvalid
			}
			nextState = "PAUSED"
			percent = rolloutPercent(stage)
			setPaused = now
		case "RESUME":
			if state != "PAUSED" {
				return ErrReleaseRolloutInvalid
			}
			nextState = "RUNNING"
			percent = rolloutPercent(stage)
		case "STOP":
			if state != "RUNNING" && state != "PAUSED" {
				return ErrReleaseRolloutInvalid
			}
			nextState = "STOPPED"
			percent = rolloutPercent(stage)
			setStopped = now
		default:
			return ErrReleaseRolloutInvalid
		}
		tag, err := tx.Exec(ctx, `UPDATE askdata.release_rollouts SET stage=$1,state=$2,canary_percent=$3,
			reason_hash=$4,updated_by=$5,version=version+1,
			stage_started_at=CASE WHEN stage IS DISTINCT FROM $1 THEN $6 ELSE stage_started_at END,
			paused_at=$7,stopped_at=$8,accepted_at=CASE WHEN $2='ACCEPTED' THEN $6 ELSE accepted_at END,
			updated_at=$6 WHERE id=$9 AND tenant_id=$10 AND version=$11`,
			nextStage, nextState, percent, input.ReasonHash, scope.ActorID, now, setPaused, setStopped, id, scope.TenantID, version)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return ErrReleaseStateConflict
		}
		eventType := map[string]string{"ADVANCE": "ADVANCED", "PAUSE": "PAUSED", "RESUME": "RESUMED", "STOP": "STOPPED"}[action]
		if nextState == "ACCEPTED" {
			eventType = "ACCEPTED"
		}
		if _, err := tx.Exec(ctx, `INSERT INTO askdata.release_rollout_events(
			tenant_id,domain_id,rollout_id,candidate_release_id,event_type,from_stage,to_stage,actor_id,reason_hash,detail
		) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,jsonb_build_object('state',$10,'canaryPercent',$11))`,
			scope.TenantID, scope.DomainID, id, releaseID, eventType, stage, nextStage,
			scope.ActorID, input.ReasonHash, nextState, percent); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `SELECT id::text,candidate_release_id::text,control_release_id::text,stage,state,
			canary_percent,version,started_at,stage_started_at,updated_at FROM askdata.release_rollouts WHERE id=$1`, id).
			Scan(&result.ID, &result.CandidateReleaseID, &result.ControlReleaseID, &result.Stage, &result.State,
				&result.CanaryPercent, &result.Version, &result.StartedAt, &result.StageStartedAt, &result.UpdatedAt)
	})
	return result, normalizeLifecycleError(err)
}

func rolloutPercent(stage string) int {
	switch stage {
	case "CANARY_5":
		return 5
	case "CANARY_20":
		return 20
	case "CANARY_50":
		return 50
	case "ACCEPTED_95":
		return 95
	default:
		return 0
	}
}

func (store *PostgresStore) AdvanceReleaseRollout(ctx context.Context, scope AdminScope, releaseID string, input ReleaseRolloutMutationInput) (ReleaseRolloutSnapshot, error) {
	return store.mutateReleaseRollout(ctx, scope, releaseID, "ADVANCE", input)
}
func (store *PostgresStore) PauseReleaseRollout(ctx context.Context, scope AdminScope, releaseID string, input ReleaseRolloutMutationInput) (ReleaseRolloutSnapshot, error) {
	return store.mutateReleaseRollout(ctx, scope, releaseID, "PAUSE", input)
}
func (store *PostgresStore) ResumeReleaseRollout(ctx context.Context, scope AdminScope, releaseID string, input ReleaseRolloutMutationInput) (ReleaseRolloutSnapshot, error) {
	return store.mutateReleaseRollout(ctx, scope, releaseID, "RESUME", input)
}
func (store *PostgresStore) StopReleaseRollout(ctx context.Context, scope AdminScope, releaseID string, input ReleaseRolloutMutationInput) (ReleaseRolloutSnapshot, error) {
	return store.mutateReleaseRollout(ctx, scope, releaseID, "STOP", input)
}

func loadReleaseRolloutTx(ctx context.Context, tx pgx.Tx, releaseID string) (ReleaseRolloutSnapshot, bool, error) {
	var result ReleaseRolloutSnapshot
	err := tx.QueryRow(ctx, `SELECT id::text,candidate_release_id::text,control_release_id::text,stage,state,
		canary_percent,version,started_at,stage_started_at,updated_at FROM askdata.release_rollouts
		WHERE candidate_release_id=$1 OR control_release_id=$1 ORDER BY updated_at DESC,id DESC LIMIT 1`, releaseID).
		Scan(&result.ID, &result.CandidateReleaseID, &result.ControlReleaseID, &result.Stage, &result.State,
			&result.CanaryPercent, &result.Version, &result.StartedAt, &result.StageStartedAt, &result.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return ReleaseRolloutSnapshot{}, false, nil
	}
	return result, err == nil, err
}

func (store *PostgresStore) RollbackRelease(ctx context.Context, scope AdminScope, releaseID string, input ReleaseRollbackInput) (ReleaseRollbackResult, error) {
	if err := store.validateLifecycle(ctx, scope, releaseID); err != nil || !validLifecycleHash(input.ReasonHash) || input.ExpectedStateVersion < 1 {
		if err != nil {
			return ReleaseRollbackResult{}, err
		}
		return ReleaseRollbackResult{}, ErrRegistryInvalidRequest
	}
	result := ReleaseRollbackResult{}
	err := store.withReleasePermission(ctx, scope, releaseID, func(tx pgx.Tx) error {
		var targetStatus, currentID string
		var stateVersion int64
		if err := tx.QueryRow(ctx, `SELECT target.status,state.active_release_id::text,state.version
			FROM askdata.releases target JOIN askdata.release_state state ON state.tenant_id=target.tenant_id AND state.domain_id=target.domain_id
			WHERE target.id=$1 AND target.domain_id=$2 FOR UPDATE OF target,state`, releaseID, scope.DomainID).
			Scan(&targetStatus, &currentID, &stateVersion); err != nil {
			return err
		}
		if stateVersion != input.ExpectedStateVersion {
			return ErrReleaseStateConflict
		}
		if currentID == releaseID || (targetStatus != "SUPERSEDED" && targetStatus != "RETAINED") {
			return ErrReleaseRollbackInvalid
		}
		if _, err := tx.Exec(ctx, `SELECT set_config('askdata.rollback_release_id',$1,true)`, releaseID); err != nil {
			return err
		}
		var currentStatus string
		if err := tx.QueryRow(ctx, `UPDATE askdata.releases SET status='SUPERSEDED',updated_by=$1,version=version+1
			WHERE id=$2 AND tenant_id=$3 AND status='ACTIVE' RETURNING status`, scope.ActorID, currentID, scope.TenantID).Scan(&currentStatus); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE askdata.releases SET status='ACTIVE',activated_by=$1,activated_at=clock_timestamp(),updated_by=$1,version=version+1
			WHERE id=$2 AND tenant_id=$3`, scope.ActorID, releaseID, scope.TenantID); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `UPDATE askdata.release_state SET active_release_id=$1,updated_by=$2,version=version+1
			WHERE tenant_id=$3 AND domain_id=$4 AND version=$5 RETURNING version`, releaseID, scope.ActorID, scope.TenantID, scope.DomainID, stateVersion).Scan(&result.StateVersion); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO askdata.release_events(tenant_id,domain_id,release_id,event_type,actor_id,detail)
			VALUES($1,$2,$3,'ROLLED_BACK',$4,jsonb_build_object('replacedReleaseId',$5,'reasonHash',$6,'releaseStateVersion',$7))`,
			scope.TenantID, scope.DomainID, releaseID, scope.ActorID, currentID, input.ReasonHash, result.StateVersion)
		if err != nil {
			return err
		}
		var rolloutID, rolloutCandidate, rolloutStage, rolloutReason string
		rolloutErr := tx.QueryRow(ctx, `UPDATE askdata.release_rollouts SET state='ROLLED_BACK',rolled_back_at=clock_timestamp(),
			updated_at=clock_timestamp(),updated_by=$1,version=version+1
			WHERE tenant_id=$2 AND domain_id=$3 AND state IN('RUNNING','PAUSED','ACCEPTED')
			RETURNING id::text,candidate_release_id::text,stage,reason_hash`, scope.ActorID, scope.TenantID, scope.DomainID).
			Scan(&rolloutID, &rolloutCandidate, &rolloutStage, &rolloutReason)
		if rolloutErr != nil && !errors.Is(rolloutErr, pgx.ErrNoRows) {
			return rolloutErr
		}
		if rolloutErr == nil {
			if _, err := tx.Exec(ctx, `INSERT INTO askdata.release_rollout_events(
				tenant_id,domain_id,rollout_id,candidate_release_id,event_type,from_stage,to_stage,actor_id,reason_hash,detail
			) VALUES($1,$2,$3,$4,'ROLLED_BACK',$5,$5,$6,$7,jsonb_build_object('activeReleaseId',$8))`,
				scope.TenantID, scope.DomainID, rolloutID, rolloutCandidate, rolloutStage,
				scope.ActorID, rolloutReason, releaseID); err != nil {
				return err
			}
		}
		result.RolledBack = true
		result.ActiveReleaseID = releaseID
		result.ReplacedReleaseID = currentID
		return nil
	})
	return result, normalizeLifecycleError(err)
}

func (store *PostgresStore) RetireRelease(ctx context.Context, scope AdminScope, releaseID string) error {
	if err := store.validateLifecycle(ctx, scope, releaseID); err != nil {
		return err
	}
	err := store.Retire(database.WithAccessContext(ctx, scope.ActorID, scope.DomainID), scope.TenantID, scope.DomainID, releaseID)
	var retention *ReleaseRetentionError
	if errors.As(err, &retention) {
		switch retention.Code {
		case ReleaseRetireBlockedCode:
			return fmt.Errorf("%w: %s", ErrReleaseRetireBlocked, retention.Code)
		case ReleaseRetentionNotExpiredCode:
			return fmt.Errorf("%w: %s", ErrReleaseRetentionOpen, retention.Code)
		}
	}
	if errors.Is(err, ErrReleaseRetireState) {
		return ErrReleaseRollbackInvalid
	}
	return err
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
		var actorApprovalRole *string
		var approvalDueAt *time.Time
		if err := tx.QueryRow(ctx, `SELECT release.id::text,release.status,release.content_hash,
			release.version,state.version,state.active_release_id::text,
			(SELECT count(*) FROM askdata.release_projections AS projection
			 WHERE projection.release_id=release.id AND projection.status='READY'
			   AND projection.expected_content_hash=release.content_hash
			   AND projection.applied_content_hash=release.content_hash),
			(SELECT count(*) FROM askdata.release_review_reports AS report WHERE report.release_id=release.id),
			(SELECT count(*) FROM askdata.release_approvals AS approval
			 LEFT JOIN askdata.release_approval_withdrawals AS withdrawal ON withdrawal.tenant_id=approval.tenant_id AND withdrawal.approval_id=approval.id
			 WHERE approval.release_id=release.id AND approval.decision='APPROVED' AND withdrawal.id IS NULL),
			COALESCE((SELECT array_agg(approval.review_role ORDER BY approval.review_slot)
				FROM askdata.release_approvals AS approval
				LEFT JOIN askdata.release_approval_withdrawals AS withdrawal ON withdrawal.tenant_id=approval.tenant_id AND withdrawal.approval_id=approval.id
				WHERE approval.release_id=release.id AND approval.decision='APPROVED' AND withdrawal.id IS NULL),ARRAY[]::text[]),
			EXISTS(SELECT 1 FROM askdata.release_approvals AS approval
				LEFT JOIN askdata.release_approval_withdrawals AS withdrawal ON withdrawal.tenant_id=approval.tenant_id AND withdrawal.approval_id=approval.id
				WHERE approval.release_id=release.id AND approval.reviewer_id=$3 AND withdrawal.id IS NULL),
			(SELECT count(*) FROM askdata.release_approvals AS approval
			 LEFT JOIN askdata.release_approval_withdrawals AS withdrawal ON withdrawal.tenant_id=approval.tenant_id AND withdrawal.approval_id=approval.id
			 WHERE approval.release_id=release.id AND approval.decision='REJECTED' AND withdrawal.id IS NULL),
			COALESCE((SELECT array_agg(approval.review_role ORDER BY approval.review_slot)
			 FROM askdata.release_approvals AS approval
			 LEFT JOIN askdata.release_approval_withdrawals AS withdrawal ON withdrawal.tenant_id=approval.tenant_id AND withdrawal.approval_id=approval.id
			 WHERE approval.release_id=release.id AND approval.decision='REJECTED' AND withdrawal.id IS NULL),ARRAY[]::text[]),
			(SELECT approval.review_role FROM askdata.release_approvals AS approval
			 LEFT JOIN askdata.release_approval_withdrawals AS withdrawal ON withdrawal.tenant_id=approval.tenant_id AND withdrawal.approval_id=approval.id
			 WHERE approval.release_id=release.id AND approval.reviewer_id=$3 AND withdrawal.id IS NULL
			 ORDER BY approval.approved_at DESC LIMIT 1),
			(SELECT gate.recomputed_at + interval '24 hours' FROM askdata.release_evaluation_gate_receipts AS gate
			 WHERE gate.release_id=release.id AND gate.passed ORDER BY gate.recomputed_at DESC,gate.id DESC LIMIT 1),
			COALESCE((SELECT max(escalation.escalation_level) FROM askdata.release_approval_escalations AS escalation
			 WHERE escalation.release_id=release.id),0)
		FROM askdata.releases AS release
		JOIN askdata.release_state AS state ON state.tenant_id=release.tenant_id AND state.domain_id=release.domain_id
		WHERE release.id=$1 AND release.domain_id=$2`, releaseID, scope.DomainID, scope.ActorID).Scan(
			&result.ReleaseID, &result.Status, &result.ContentHash, &result.ReleaseVersion,
			&result.ReleaseStateVersion, &activeID, &result.ReadyProjectionCount,
			&result.ReviewReportCount, &result.ApprovalCount, &result.ApprovedRoles,
			&result.ActorHasApproved, &result.RejectionCount, &result.RejectedRoles,
			&actorApprovalRole, &approvalDueAt, &result.EscalationLevel); err != nil {
			return err
		}
		if activeID != nil {
			result.ActiveReleaseID = *activeID
		}
		if actorApprovalRole != nil {
			result.ActorApprovalRole = *actorApprovalRole
		}
		result.ApprovalSLAStatus = "NOT_STARTED"
		if approvalDueAt != nil {
			result.ApprovalDueAt = approvalDueAt.UTC().Format(time.RFC3339Nano)
			if time.Now().After(*approvalDueAt) && result.ApprovalCount < 2 {
				result.ApprovalSLAStatus = "OVERDUE"
			} else if result.ApprovalCount < 2 {
				result.ApprovalSLAStatus = "RUNNING"
			} else {
				result.ApprovalSLAStatus = "COMPLETED"
			}
		}
		rows, err := tx.Query(ctx, `SELECT target,status,expected_content_hash,
			applied_content_hash,attempt,max_attempts,error_code
			FROM askdata.release_projections WHERE release_id=$1 ORDER BY target`, releaseID)
		if err != nil {
			return err
		}
		defer rows.Close()
		result.Projections = []ReleaseProjectionSnapshot{}
		for rows.Next() {
			var projection ReleaseProjectionSnapshot
			if err := rows.Scan(&projection.Target, &projection.Status,
				&projection.ExpectedContentHash, &projection.AppliedContentHash,
				&projection.Attempt, &projection.MaxAttempts, &projection.ErrorCode); err != nil {
				return err
			}
			projection.HashMatched = projection.Status == "READY" &&
				projection.ExpectedContentHash == projection.AppliedContentHash
			result.Projections = append(result.Projections, projection)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		var latest ReleaseGateResult
		err = tx.QueryRow(ctx, `SELECT passed,receipt_hash,failure_codes,facts_json,
			evaluation_set_id::text,evaluation_batch_id::text
			FROM askdata.release_evaluation_gate_receipts
			WHERE release_id=$1 ORDER BY recomputed_at DESC,id DESC LIMIT 1`, releaseID).
			Scan(&latest.Passed, &latest.ReceiptHash, &latest.Failures, &latest.Facts,
				&latest.EvaluationSetID, &latest.EvaluationBatchID)
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
		errors.Is(err, ErrReleaseApprovalFailed) || errors.Is(err, ErrReleaseStateConflict) ||
		errors.Is(err, ErrReleaseRolloutInvalid) || errors.Is(err, ErrReleaseRollbackInvalid) ||
		errors.Is(err, ErrReleaseRetireBlocked) || errors.Is(err, ErrReleaseRetentionOpen) {
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
