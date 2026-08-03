package semanticqa

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"intelligent-report-generation-system/internal/platform/database"
)

type QuestionExecutionRegistryProof struct {
	ReleaseID                 string   `json:"releaseId"`
	SemanticVersion           string   `json:"semanticVersion"`
	SemanticContentHash       string   `json:"semanticContentHash"`
	ProjectionResourceVersion string   `json:"projectionResourceVersion"`
	RegistryObjectIDs         []string `json:"registryObjectIds"`
	MetricVersionIDs          []string `json:"metricVersionIds"`
	DatasetVersionIDs         []string `json:"datasetVersionIds"`
	MaterializationIDs        []string `json:"materializationIds"`
	QualityRuleIDs            []string `json:"qualityRuleIds"`
	FreshnessObservedAt       string   `json:"freshnessObservedAt"`
	QualityDecision           string   `json:"qualityDecision"`
	ProofHash                 string   `json:"proofHash"`
}

type questionExecutionRegistryStore interface {
	ValidateQuestionExecutionRegistry(
		context.Context, string, string, string, string, []QueryPlan,
	) (QuestionExecutionRegistryProof, error)
}

type questionToolCallRecord struct {
	QuestionRunID       string
	ActorID             string
	ReleaseID           string
	SemanticVersion     string
	SemanticContentHash string
	ToolCallID          string
	ToolName            string
	State               QuestionState
	Status              string
	RequestHash         string
	PolicyScopeHash     string
	ResultHash          string
	EvidenceIDs         []string
	Budget              QuestionBudgets
	DurationMS          int64
	ErrorCode           string
}

type questionToolAuditStore interface {
	RecordQuestionToolCall(context.Context, string, questionToolCallRecord) error
}

func (store *PostgresStore) ValidateQuestionExecutionRegistry(
	ctx context.Context,
	tenantID, releaseID, semanticVersion, contentHash string,
	plans []QueryPlan,
) (proof QuestionExecutionRegistryProof, err error) {
	if store == nil || store.pool == nil || uuid.Validate(tenantID) != nil ||
		uuid.Validate(releaseID) != nil || semanticVersion == "" ||
		!validHash(contentHash) || len(plans) < 1 || len(plans) > 2 {
		return proof, ErrInvalidRequest
	}
	proof = QuestionExecutionRegistryProof{
		ReleaseID: releaseID, SemanticVersion: semanticVersion,
		SemanticContentHash: contentHash, QualityDecision: "PASS",
	}
	latestFreshness := time.Time{}
	err = database.WithTenantTx(ctx, store.pool, tenantID, func(tx pgx.Tx) error {
		var expectedObjects, registeredObjects int
		if err := tx.QueryRow(ctx, `SELECT release.object_count,
				projection.resource_version,
				(SELECT count(*)::int
				 FROM platform.semantic_execution_registry AS registry
				 WHERE registry.release_id=release.id)
			FROM platform.semantic_release_state AS state
			JOIN platform.semantic_releases AS release
			  ON release.tenant_id=state.tenant_id
			 AND release.id=state.active_release_id
			JOIN platform.semantic_release_projections AS projection
			  ON projection.tenant_id=release.tenant_id
			 AND projection.release_id=release.id
			 AND projection.target='EXECUTION_SEMANTIC_LAYER'
			WHERE state.tenant_id=platform.current_tenant_id()
			  AND release.id=$1::uuid AND release.status='ACTIVE'
			  AND release.semantic_version=$2 AND release.content_hash=$3
			  AND projection.status='READY'
			  AND projection.expected_content_hash=$3
			  AND projection.applied_content_hash=$3
			  AND projection.resource_version<>''`,
			releaseID, semanticVersion, contentHash,
		).Scan(&expectedObjects, &proof.ProjectionResourceVersion, &registeredObjects); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrUnprovenPath
			}
			return err
		}
		if expectedObjects < 1 || registeredObjects != expectedObjects {
			return ErrUnprovenPath
		}

		for _, plan := range plans {
			if uuid.Validate(plan.SelectedMetricID) != nil ||
				uuid.Validate(plan.SelectedMetricVersionID) != nil ||
				uuid.Validate(plan.SelectedDatasetVersionID) != nil ||
				uuid.Validate(plan.SelectedMaterializationID) != nil {
				return ErrUnprovenPath
			}
			var metricObjectID, metricObjectVersion, metricObjectHash string
			var metricContractJSON []byte
			var metricDatasetVersionID string
			if err := tx.QueryRow(ctx, `SELECT registry.object_id,
					registry.object_version,registry.content_hash,
					registry.contract_json,version.dataset_version_id::text
				FROM platform.semantic_execution_registry AS registry
				JOIN platform.metrics AS metric
				  ON metric.tenant_id=registry.tenant_id
				 AND metric.id::text=registry.native_object_id
				JOIN platform.metric_versions AS version
				  ON version.tenant_id=metric.tenant_id
				 AND version.metric_id=metric.id
				 AND version.id::text=registry.native_version_id
				WHERE registry.release_id=$1::uuid
				  AND registry.object_type='METRIC'
				  AND registry.native_object_id=$2
				  AND registry.native_version_id=$3
				  AND metric.current_published_version_id=version.id
				  AND metric.status='PUBLISHED' AND metric.deleted_at IS NULL
				  AND version.status='PUBLISHED'`,
				releaseID, plan.SelectedMetricID, plan.SelectedMetricVersionID,
			).Scan(
				&metricObjectID, &metricObjectVersion, &metricObjectHash,
				&metricContractJSON, &metricDatasetVersionID,
			); err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					return ErrUnprovenPath
				}
				return err
			}
			if !validHash(metricObjectHash) ||
				metricDatasetVersionID != plan.SelectedDatasetVersionID {
				return ErrUnprovenPath
			}
			var metricContract map[string]any
			if err := json.Unmarshal(metricContractJSON, &metricContract); err != nil ||
				semanticString(metricContract["nativeDatasetVersionId"]) != plan.SelectedDatasetVersionID {
				return ErrUnprovenPath
			}
			qualityIDs := semanticStringSlice(metricContract["qualityRuleIds"])
			if len(qualityIDs) == 0 {
				return ErrUnprovenPath
			}
			var qualityCount int
			if err := tx.QueryRow(ctx, `SELECT count(DISTINCT object_id)::int
				FROM platform.semantic_execution_registry
				WHERE release_id=$1::uuid AND object_type='QUALITY_RULE'
				  AND object_id=ANY($2::text[])`, releaseID, qualityIDs,
			).Scan(&qualityCount); err != nil || qualityCount != len(qualityIDs) {
				if err != nil {
					return err
				}
				return ErrUnprovenPath
			}

			var datasetObjectID, datasetObjectVersion, datasetObjectHash string
			var activatedAt time.Time
			var blockingFailures int
			if err := tx.QueryRow(ctx, `SELECT registry.object_id,
					registry.object_version,registry.content_hash,
					materialization.activated_at,
					(SELECT count(*)::int
					 FROM platform.data_quality_results AS quality
					 WHERE quality.materialization_id=materialization.id
					   AND quality.status='FAILED'
					   AND quality.severity='ERROR')
				FROM platform.semantic_execution_registry AS registry
				JOIN platform.datasets AS dataset
				  ON dataset.tenant_id=registry.tenant_id
				 AND dataset.id::text=registry.native_object_id
				JOIN platform.dataset_versions AS version
				  ON version.tenant_id=dataset.tenant_id
				 AND version.dataset_id=dataset.id
				 AND version.id::text=registry.native_version_id
				JOIN platform.dataset_materializations AS materialization
				  ON materialization.tenant_id=version.tenant_id
				 AND materialization.id=$3::uuid
				 AND materialization.dataset_id=dataset.id
				 AND materialization.dataset_version_id=version.id
				WHERE registry.release_id=$1::uuid
				  AND registry.object_type='DATASET'
				  AND registry.native_version_id=$2
				  AND dataset.current_published_version_id=version.id
				  AND dataset.status='PUBLISHED' AND dataset.deleted_at IS NULL
				  AND version.status='PUBLISHED'
				  AND materialization.status='ACTIVE'
				  AND materialization.schema_hash=version.schema_hash
				  AND registry.contract_json->'freshness'->>'requireActiveMaterialization'='true'`,
				releaseID, plan.SelectedDatasetVersionID,
				plan.SelectedMaterializationID,
			).Scan(
				&datasetObjectID, &datasetObjectVersion, &datasetObjectHash,
				&activatedAt, &blockingFailures,
			); err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					return ErrUnprovenPath
				}
				return err
			}
			if !validHash(datasetObjectHash) || blockingFailures > 0 {
				return ErrUnprovenPath
			}
			if activatedAt.After(latestFreshness) {
				latestFreshness = activatedAt
			}
			proof.RegistryObjectIDs = append(
				proof.RegistryObjectIDs,
				"METRIC:"+metricObjectID+":"+metricObjectVersion,
				"DATASET:"+datasetObjectID+":"+datasetObjectVersion,
			)
			proof.MetricVersionIDs = append(proof.MetricVersionIDs, plan.SelectedMetricVersionID)
			proof.DatasetVersionIDs = append(proof.DatasetVersionIDs, plan.SelectedDatasetVersionID)
			proof.MaterializationIDs = append(proof.MaterializationIDs, plan.SelectedMaterializationID)
			proof.QualityRuleIDs = append(proof.QualityRuleIDs, qualityIDs...)
		}
		return nil
	})
	if err != nil {
		return QuestionExecutionRegistryProof{}, err
	}
	proof.RegistryObjectIDs = uniqueStrings(proof.RegistryObjectIDs, 64)
	proof.MetricVersionIDs = uniqueStrings(proof.MetricVersionIDs, 16)
	proof.DatasetVersionIDs = uniqueStrings(proof.DatasetVersionIDs, 16)
	proof.MaterializationIDs = uniqueStrings(proof.MaterializationIDs, 16)
	proof.QualityRuleIDs = uniqueStrings(proof.QualityRuleIDs, 64)
	if !latestFreshness.IsZero() {
		proof.FreshnessObservedAt = latestFreshness.UTC().Format(time.RFC3339Nano)
	}
	hashInput := proof
	hashInput.ProofHash = ""
	proof.ProofHash, err = hashJSON(hashInput)
	if err != nil || !validHash(proof.ProofHash) {
		return QuestionExecutionRegistryProof{}, ErrUnprovenPath
	}
	return proof, nil
}

func (store *PostgresStore) RecordQuestionToolCall(
	ctx context.Context,
	tenantID string,
	record questionToolCallRecord,
) error {
	if store == nil || store.pool == nil || uuid.Validate(tenantID) != nil ||
		uuid.Validate(record.QuestionRunID) != nil || uuid.Validate(record.ActorID) != nil ||
		uuid.Validate(record.ReleaseID) != nil || uuid.Validate(record.ToolCallID) != nil ||
		!validHash(record.SemanticContentHash) || !validHash(record.RequestHash) ||
		!validHash(record.PolicyScopeHash) || !validHash(record.ResultHash) ||
		record.DurationMS < 0 ||
		!oneOf(record.Status, "SUCCEEDED", "BLOCKED", "FAILED") {
		return ErrInvalidRequest
	}
	budgetJSON, err := json.Marshal(record.Budget)
	if err != nil {
		return err
	}
	return database.WithTenantTx(ctx, store.pool, tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO platform.semantic_tool_calls(
				tenant_id,question_run_id,actor_user_id,semantic_release_id,
				semantic_version,semantic_content_hash,tool_call_id,tool_name,
				state,status,request_hash,policy_scope_hash,result_hash,
				evidence_ids,budget_json,duration_ms,error_code
			) VALUES(
				platform.current_tenant_id(),$1::uuid,$2::uuid,$3::uuid,
				$4,$5,$6,$7,$8,$9,$10,$11,$12,$13::text[],$14::jsonb,$15,$16
			)`,
			record.QuestionRunID, record.ActorID, record.ReleaseID,
			record.SemanticVersion, record.SemanticContentHash,
			record.ToolCallID, record.ToolName, record.State, record.Status,
			record.RequestHash, record.PolicyScopeHash, record.ResultHash,
			uniqueStrings(record.EvidenceIDs, 256), budgetJSON,
			record.DurationMS, record.ErrorCode,
		)
		return err
	})
}

func (service *Service) auditQuestionToolCall(
	ctx context.Context,
	tenantID, actorID, questionRunID string,
	snapshot *QuestionSemanticSnapshot,
	toolName string,
	state QuestionState,
	request, result any,
	evidenceIDs []string,
	budgets QuestionBudgets,
	started time.Time,
	callErr error,
) error {
	store, ok := service.store.(questionToolAuditStore)
	if !ok || snapshot == nil || !defaultQuestionToolRegistry.Contains(toolName) ||
		!defaultQuestionToolRegistry.Allowed(toolName, state) {
		return ErrUnprovenPath
	}
	requestHash, err := hashJSON(request)
	if err != nil {
		return err
	}
	resultHash, err := hashJSON(result)
	if err != nil {
		return err
	}
	policyScopeHash, err := hashJSON(struct {
		TenantID        string   `json:"tenantId"`
		ActorID         string   `json:"actorId"`
		ReleaseID       string   `json:"releaseId"`
		SemanticVersion string   `json:"semanticVersion"`
		ContentHash     string   `json:"contentHash"`
		RoleCodes       []string `json:"roleCodes"`
		Purpose         string   `json:"purpose"`
	}{
		TenantID: tenantID, ActorID: actorID, ReleaseID: snapshot.ReleaseID,
		SemanticVersion: snapshot.SemanticVersion, ContentHash: snapshot.ContentHash,
		RoleCodes: snapshot.RoleCodes, Purpose: snapshot.Purpose,
	})
	if err != nil {
		return err
	}
	status, errorCode := "SUCCEEDED", ""
	if callErr != nil {
		status, errorCode = "BLOCKED", "TOOL_EXECUTION_REJECTED"
	}
	return store.RecordQuestionToolCall(ctx, tenantID, questionToolCallRecord{
		QuestionRunID: questionRunID, ActorID: actorID,
		ReleaseID: snapshot.ReleaseID, SemanticVersion: snapshot.SemanticVersion,
		SemanticContentHash: snapshot.ContentHash, ToolCallID: uuid.NewString(),
		ToolName: toolName, State: state, Status: status,
		RequestHash: requestHash, PolicyScopeHash: policyScopeHash,
		ResultHash: resultHash, EvidenceIDs: evidenceIDs, Budget: budgets,
		DurationMS: max(time.Since(started).Milliseconds(), 0), ErrorCode: errorCode,
	})
}

type questionToolBudgetTracker struct {
	budgets    QuestionBudgets
	metadata   int
	explain    int
	main       int
	validation int
}

func (tracker *questionToolBudgetTracker) reserve(toolName string) error {
	if tracker == nil {
		return ErrUnprovenPath
	}
	switch toolName {
	case "explain_query_plan":
		tracker.explain++
		if tracker.explain > tracker.budgets.MaximumExplainQueries {
			return fmt.Errorf("%w: explain tool budget exceeded", ErrUnprovenPath)
		}
	case "execute_query_plan":
		tracker.main++
		if tracker.main > tracker.budgets.MaximumMetricQueries {
			return fmt.Errorf("%w: execution tool budget exceeded", ErrUnprovenPath)
		}
	case "execute_validation_query":
		tracker.validation++
		if tracker.validation > tracker.budgets.MaximumValidationQueries {
			return fmt.Errorf("%w: validation tool budget exceeded", ErrUnprovenPath)
		}
	default:
		tracker.metadata++
		if tracker.metadata > tracker.budgets.MaximumMetadataTools {
			return fmt.Errorf("%w: metadata tool budget exceeded", ErrUnprovenPath)
		}
	}
	return nil
}

func toolEvidenceIDs(values ...string) []string {
	result := []string{}
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			result = append(result, value)
		}
	}
	return uniqueStrings(result, 256)
}
