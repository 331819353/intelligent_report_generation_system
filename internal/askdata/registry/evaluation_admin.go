package registry

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/platform/database"
)

var (
	ErrEvaluationSetInvalid        = errors.New("evaluation set request is invalid")
	ErrEvaluationSetNotFound       = errors.New("evaluation set was not found")
	ErrEvaluationSetCasesMissing   = errors.New("certified sealed evaluation cases are missing")
	ErrEvaluationSetHintInvalid    = errors.New("evaluation case expected result contract is invalid")
	ErrEvaluationSetReviewConflict = errors.New("evaluation set review is not eligible")
)

type EvaluationSetCreateInput struct {
	Code        string `json:"code"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

type EvaluationSetCreateResult struct {
	EvaluationSetID string `json:"evaluationSetId"`
	ReleaseID       string `json:"releaseId"`
	CaseCount       int    `json:"caseCount"`
	Status          string `json:"status"`
}

type EvaluationCaseReviewItem struct {
	ID                     string `json:"id"`
	CaseKey                string `json:"caseKey"`
	ApprovedQuestion       string `json:"approvedQuestion"`
	Priority               string `json:"priority"`
	ExpectedDisposition    string `json:"expectedDisposition"`
	SecurityExpectation    string `json:"securityExpectation"`
	Complexity             string `json:"complexity"`
	Ambiguity              string `json:"ambiguity"`
	ShardID                int16  `json:"shardId"`
	ContentHash            string `json:"contentHash"`
	IndependentReviewCount int    `json:"independentReviewCount"`
	ActorReviewed          bool   `json:"actorReviewed"`
	ActorEligible          bool   `json:"actorEligible"`
}

type EvaluationCaseReviewPage struct {
	EvaluationSetID   string                     `json:"evaluationSetId"`
	Status            string                     `json:"status"`
	Items             []EvaluationCaseReviewItem `json:"items"`
	Total             int                        `json:"total"`
	ActorReviewed     int                        `json:"actorReviewed"`
	ActorEligible     bool                       `json:"actorEligible"`
	FullyReviewed     int                        `json:"fullyReviewed"`
	NextOffset        int                        `json:"nextOffset,omitempty"`
	RequiredReviewers int                        `json:"requiredReviewers"`
}

type EvaluationSetReviewInput struct {
	Decision string   `json:"decision"`
	Comment  string   `json:"comment"`
	CaseIDs  []string `json:"caseIds"`
}

type EvaluationSetReviewResult struct {
	EvaluationSetID string `json:"evaluationSetId"`
	Decision        string `json:"decision"`
	ReviewedCount   int    `json:"reviewedCount"`
	TotalCount      int    `json:"totalCount"`
}

type EvaluationSetSealResult struct {
	EvaluationSetID string `json:"evaluationSetId"`
	Sealed          bool   `json:"sealed"`
	Status          string `json:"status"`
	CaseCount       int    `json:"caseCount"`
	ReviewCount     int    `json:"reviewCount"`
	ContentHash     string `json:"contentHash,omitempty"`
}

type EvaluationSetAdminBackend interface {
	CreateEvaluationSetFromCertified(context.Context, AdminScope, string, EvaluationSetCreateInput) (EvaluationSetCreateResult, error)
	ListEvaluationCasesForReview(context.Context, AdminScope, string, int, int) (EvaluationCaseReviewPage, error)
	ReviewEvaluationSet(context.Context, AdminScope, string, EvaluationSetReviewInput) (EvaluationSetReviewResult, error)
	SealEvaluationSet(context.Context, AdminScope, string) (EvaluationSetSealResult, error)
}

type evaluationExpectedContract struct {
	ExpectedIRHash      string `json:"expectedIrHash"`
	ExpectedPathHash    string `json:"expectedPathHash"`
	ExpectedResultHash  string `json:"expectedResultHash"`
	Priority            string `json:"priority"`
	SecurityExpectation string `json:"securityExpectation"`
	Complexity          string `json:"complexity"`
	Ambiguity           string `json:"ambiguity"`
}

type certifiedEvaluationCase struct {
	VersionID, AssetID, QuestionHash, Question, ExpectedOutcome, Hint string
	ShardID                                                           int16
	Contract                                                          evaluationExpectedContract
}

func (store *PostgresStore) CreateEvaluationSetFromCertified(
	ctx context.Context, scope AdminScope, releaseID string, input EvaluationSetCreateInput,
) (EvaluationSetCreateResult, error) {
	if store == nil || store.pool == nil || scope.Validate(ctx) != nil || !canonicalAdminUUID(releaseID) {
		return EvaluationSetCreateResult{}, ErrEvaluationSetInvalid
	}
	input.Code = strings.TrimSpace(input.Code)
	input.Name = strings.TrimSpace(input.Name)
	input.Description = strings.TrimSpace(input.Description)
	if input.Code != "" && !stableAdminCode(input.Code) || len(input.Name) > 200 || len(input.Description) > 4000 {
		return EvaluationSetCreateResult{}, ErrEvaluationSetInvalid
	}
	result := EvaluationSetCreateResult{ReleaseID: releaseID, Status: "DRAFT"}
	err := database.WithTenantTx(ctx, store.pool, scope.TenantID, func(tx pgx.Tx) error {
		if err := requireSemanticPermissionTx(ctx, tx, scope, AdminActionRelease, releaseID); err != nil {
			return err
		}
		var semanticVersion, releaseHash, releaseStatus string
		if err := tx.QueryRow(ctx, `SELECT semantic_version,content_hash,status
			FROM askdata.releases WHERE id=$1 AND domain_id=$2`, releaseID, scope.DomainID).
			Scan(&semanticVersion, &releaseHash, &releaseStatus); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrEvaluationSetNotFound
			}
			return err
		}
		if releaseStatus != "READY" && releaseStatus != "SUPERSEDED" {
			return ErrEvaluationSetInvalid
		}
		cases, err := loadCertifiedEvaluationCases(ctx, tx, scope.DomainID)
		if err != nil {
			return err
		}
		if len(cases) == 0 {
			return ErrEvaluationSetCasesMissing
		}
		if input.Code == "" {
			input.Code = "release_gate_" + strings.ReplaceAll(releaseID, "-", "")[:12]
		}
		if input.Name == "" {
			input.Name = semanticVersion + " 密封评测集"
		}
		var versionNo int
		if err := tx.QueryRow(ctx, `SELECT COALESCE(max(version_no),0)+1
			FROM askdata.evaluation_sets WHERE domain_id=$1 AND code=$2`, scope.DomainID, input.Code).Scan(&versionNo); err != nil {
			return err
		}
		result.EvaluationSetID = uuid.NewString()
		if _, err := tx.Exec(ctx, `INSERT INTO askdata.evaluation_sets(
			id,tenant_id,domain_id,code,version_no,name,description,dataset_split,
			evaluation_mode,status,target_release_id,target_semantic_version,
			target_release_content_hash,created_by,updated_by
		) VALUES($1,$2,$3,$4,$5,$6,$7,'SEALED','END_TO_END_RESULT_EQUIVALENCE',
			'DRAFT',$8,$9,$10,$11,$11)`, result.EvaluationSetID, scope.TenantID,
			scope.DomainID, input.Code, versionNo, input.Name, input.Description,
			releaseID, semanticVersion, releaseHash, scope.ActorID); err != nil {
			return err
		}
		policyHash := askdata.HashBytes([]byte("governed-evaluation-question-redaction-policy-v1"))
		for _, evaluationCase := range cases {
			contract := evaluationCase.Contract
			answerable := evaluationCase.ExpectedOutcome != "REFUSE"
			if contract.SecurityExpectation != "NONE" {
				answerable = false
			}
			if _, err := tx.Exec(ctx, `INSERT INTO askdata.evaluation_cases(
				id,tenant_id,domain_id,evaluation_set_id,case_key,schema_version,
				question_hash,approved_question,question_redaction_policy_hash,priority,
				answerable,expected_disposition,security_expectation,complexity,ambiguity,
				expected_path_hash,expected_ir_hash,expected_result_hash,content_hash,
				created_by,content_updated_by,updated_by,shard_id
			) VALUES($1,$2,$3,$4,$5,'governed-eval-case-v1',$6,$7,$8,$9,$10,$11,$12,
				$13,$14,$15,$16,$17,$18,$19,$19,$19,$20)`, uuid.NewString(), scope.TenantID,
				scope.DomainID, result.EvaluationSetID, "eval:"+evaluationCase.AssetID,
				evaluationCase.QuestionHash, evaluationCase.Question, policyHash,
				contract.Priority, answerable, evaluationCase.ExpectedOutcome,
				contract.SecurityExpectation, contract.Complexity, contract.Ambiguity,
				nullableEvaluationHash(contract.ExpectedPathHash), nullableEvaluationHash(contract.ExpectedIRHash),
				nullableEvaluationHash(contract.ExpectedResultHash), strings.Repeat("0", 64), scope.ActorID,
				evaluationCase.ShardID); err != nil {
				return err
			}
		}
		result.CaseCount = len(cases)
		return nil
	})
	return result, normalizeEvaluationSetError(err)
}

func loadCertifiedEvaluationCases(ctx context.Context, tx pgx.Tx, domainID string) ([]certifiedEvaluationCase, error) {
	rows, err := tx.Query(ctx, `SELECT DISTINCT ON (version.evaluation_case_asset_id)
		version.id::text,version.evaluation_case_asset_id::text,asset.question_hash,
		version.question,version.expected_outcome,version.expected_result_hint,version.shard_id
	FROM askdata.evaluation_case_versions AS version
	JOIN askdata.evaluation_case_assets AS asset
	  ON asset.id=version.evaluation_case_asset_id AND asset.tenant_id=version.tenant_id
	WHERE version.domain_id=$1 AND version.status='CERTIFIED' AND version.set_type='SEALED'
	ORDER BY version.evaluation_case_asset_id,version.version_no DESC`, domainID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []certifiedEvaluationCase{}
	for rows.Next() {
		var item certifiedEvaluationCase
		if err := rows.Scan(&item.VersionID, &item.AssetID, &item.QuestionHash, &item.Question,
			&item.ExpectedOutcome, &item.Hint, &item.ShardID); err != nil {
			return nil, err
		}
		contract, err := parseEvaluationExpectedContract(item.ExpectedOutcome, item.Hint)
		if err != nil {
			return nil, fmt.Errorf("%w: version %s", err, item.VersionID)
		}
		item.Contract = contract
		result = append(result, item)
	}
	return result, rows.Err()
}

func parseEvaluationExpectedContract(disposition, raw string) (evaluationExpectedContract, error) {
	contract := evaluationExpectedContract{Priority: "P1", SecurityExpectation: "NONE", Complexity: "SIMPLE", Ambiguity: "NONE"}
	if strings.TrimSpace(raw) != "" {
		if err := askdata.DecodeStrictJSON([]byte(raw), &contract); err != nil {
			return evaluationExpectedContract{}, ErrEvaluationSetHintInvalid
		}
	}
	if contract.Priority == "" {
		contract.Priority = "P1"
	}
	if contract.SecurityExpectation == "" {
		contract.SecurityExpectation = "NONE"
	}
	if contract.Complexity == "" {
		contract.Complexity = "SIMPLE"
	}
	if contract.Ambiguity == "" {
		contract.Ambiguity = "NONE"
	}
	if !containsEvaluationValue(contract.Priority, "P0", "P1", "P2") ||
		!containsEvaluationValue(contract.SecurityExpectation, "NONE", "UNAUTHORIZED_BLOCK", "PROMPT_INJECTION_BLOCK", "SENSITIVE_DATA_BLOCK", "CACHE_ISOLATION_BLOCK") ||
		!containsEvaluationValue(contract.Complexity, "SIMPLE", "COMPOSITE", "CONTEXTUAL", "RELATIONAL") ||
		!containsEvaluationValue(contract.Ambiguity, "NONE", "METRIC", "DIMENSION", "MEMBER", "CROSS_DOMAIN", "MULTIPLE") {
		return evaluationExpectedContract{}, ErrEvaluationSetHintInvalid
	}
	for _, hash := range []string{contract.ExpectedIRHash, contract.ExpectedPathHash, contract.ExpectedResultHash} {
		if hash != "" && !validLifecycleHash(hash) {
			return evaluationExpectedContract{}, ErrEvaluationSetHintInvalid
		}
	}
	if disposition == "DIRECT" && (contract.ExpectedIRHash == "" || contract.ExpectedResultHash == "") ||
		contract.Complexity == "RELATIONAL" && disposition == "DIRECT" && contract.ExpectedPathHash == "" ||
		contract.SecurityExpectation != "NONE" && disposition != "REFUSE" {
		return evaluationExpectedContract{}, ErrEvaluationSetHintInvalid
	}
	return contract, nil
}

func (store *PostgresStore) ListEvaluationCasesForReview(
	ctx context.Context, scope AdminScope, evaluationSetID string, limit, offset int,
) (EvaluationCaseReviewPage, error) {
	if store == nil || store.pool == nil || scope.Validate(ctx) != nil || !canonicalAdminUUID(evaluationSetID) ||
		limit < 1 || limit > 200 || offset < 0 || offset > 100000 {
		return EvaluationCaseReviewPage{}, ErrEvaluationSetInvalid
	}
	result := EvaluationCaseReviewPage{EvaluationSetID: evaluationSetID, Items: []EvaluationCaseReviewItem{}, RequiredReviewers: 2}
	err := database.WithTenantTx(ctx, store.pool, scope.TenantID, func(tx pgx.Tx) error {
		if err := requireSemanticPermissionTx(ctx, tx, scope, AdminActionView, evaluationSetID); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT status,
			(SELECT count(*) FROM askdata.evaluation_cases WHERE evaluation_set_id=evaluation_set.id),
			(SELECT count(*) FROM askdata.evaluation_cases WHERE evaluation_set_id=evaluation_set.id AND independent_review_count=2),
			(SELECT count(*) FROM askdata.evaluation_case_reviews review
			 WHERE review.evaluation_set_id=evaluation_set.id AND review.reviewer_id=$3),
			NOT EXISTS(SELECT 1 FROM askdata.evaluation_cases evaluation_case
			 WHERE evaluation_case.evaluation_set_id=evaluation_set.id
			   AND (evaluation_case.created_by=$3 OR evaluation_case.content_updated_by=$3))
		FROM askdata.evaluation_sets evaluation_set WHERE id=$1 AND domain_id=$2`,
			evaluationSetID, scope.DomainID, scope.ActorID).Scan(&result.Status, &result.Total,
			&result.FullyReviewed, &result.ActorReviewed, &result.ActorEligible); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrEvaluationSetNotFound
			}
			return err
		}
		if result.Status != "DRAFT" {
			return nil
		}
		rows, err := tx.Query(ctx, `SELECT evaluation_case.id::text,evaluation_case.case_key,
			evaluation_case.approved_question,evaluation_case.priority,evaluation_case.expected_disposition,
			evaluation_case.security_expectation,evaluation_case.complexity,evaluation_case.ambiguity,
			evaluation_case.shard_id,evaluation_case.content_hash,evaluation_case.independent_review_count,
			EXISTS(SELECT 1 FROM askdata.evaluation_case_reviews review
			 WHERE review.evaluation_case_id=evaluation_case.id AND review.reviewer_id=$2),
			(evaluation_case.created_by<>$2 AND evaluation_case.content_updated_by<>$2)
		FROM askdata.evaluation_cases evaluation_case WHERE evaluation_case.evaluation_set_id=$1
		ORDER BY evaluation_case.case_key,evaluation_case.id LIMIT $3 OFFSET $4`,
			evaluationSetID, scope.ActorID, limit, offset)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var item EvaluationCaseReviewItem
			if err := rows.Scan(&item.ID, &item.CaseKey, &item.ApprovedQuestion, &item.Priority,
				&item.ExpectedDisposition, &item.SecurityExpectation, &item.Complexity,
				&item.Ambiguity, &item.ShardID, &item.ContentHash, &item.IndependentReviewCount,
				&item.ActorReviewed, &item.ActorEligible); err != nil {
				return err
			}
			result.Items = append(result.Items, item)
		}
		if offset+len(result.Items) < result.Total {
			result.NextOffset = offset + len(result.Items)
		}
		return rows.Err()
	})
	return result, normalizeEvaluationSetError(err)
}

func (store *PostgresStore) ReviewEvaluationSet(
	ctx context.Context, scope AdminScope, evaluationSetID string, input EvaluationSetReviewInput,
) (EvaluationSetReviewResult, error) {
	input.Decision = strings.ToUpper(strings.TrimSpace(input.Decision))
	input.Comment = strings.TrimSpace(input.Comment)
	if store == nil || store.pool == nil || scope.Validate(ctx) != nil || !canonicalAdminUUID(evaluationSetID) ||
		!containsEvaluationValue(input.Decision, "APPROVED", "REJECTED") || len(input.Comment) < 8 || len(input.Comment) > 2000 ||
		len(input.CaseIDs) < 1 || len(input.CaseIDs) > 200 {
		return EvaluationSetReviewResult{}, ErrEvaluationSetInvalid
	}
	seen := map[string]bool{}
	for _, caseID := range input.CaseIDs {
		if !canonicalAdminUUID(caseID) || seen[caseID] {
			return EvaluationSetReviewResult{}, ErrEvaluationSetInvalid
		}
		seen[caseID] = true
	}
	result := EvaluationSetReviewResult{EvaluationSetID: evaluationSetID, Decision: input.Decision}
	err := database.WithTenantTx(ctx, store.pool, scope.TenantID, func(tx pgx.Tx) error {
		if err := requireSemanticPermissionTx(ctx, tx, scope, AdminActionRelease, evaluationSetID); err != nil {
			return err
		}
		var status string
		if err := tx.QueryRow(ctx, `SELECT status FROM askdata.evaluation_sets WHERE id=$1 AND domain_id=$2`, evaluationSetID, scope.DomainID).Scan(&status); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrEvaluationSetNotFound
			}
			return err
		}
		if status != "DRAFT" {
			return ErrEvaluationSetInvalid
		}
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM askdata.evaluation_cases
			WHERE evaluation_set_id=$1 AND id=ANY($2::uuid[])`, evaluationSetID, input.CaseIDs).Scan(&result.TotalCount); err != nil {
			return err
		}
		if result.TotalCount != len(input.CaseIDs) {
			return ErrEvaluationSetInvalid
		}
		updated, err := tx.Exec(ctx, `UPDATE askdata.evaluation_case_reviews review SET
			decision=$3,review_comment=$4,reviewed_case_content_hash=evaluation_case.content_hash,
			record_version=review.record_version+1
		FROM askdata.evaluation_cases evaluation_case
		WHERE review.evaluation_case_id=evaluation_case.id AND review.evaluation_set_id=$1
		  AND review.reviewer_id=$2 AND evaluation_case.id=ANY($5::uuid[])`,
			evaluationSetID, scope.ActorID, input.Decision, input.Comment, input.CaseIDs)
		if err != nil {
			return err
		}
		inserted, err := tx.Exec(ctx, `INSERT INTO askdata.evaluation_case_reviews(
			tenant_id,domain_id,evaluation_set_id,evaluation_case_id,review_slot,reviewer_id,
			decision,reviewed_case_content_hash,review_comment,review_hash
		) SELECT evaluation_case.tenant_id,evaluation_case.domain_id,evaluation_case.evaluation_set_id,
			evaluation_case.id,CASE WHEN EXISTS(SELECT 1 FROM askdata.evaluation_case_reviews existing
			  WHERE existing.evaluation_case_id=evaluation_case.id AND existing.review_slot=1) THEN 2 ELSE 1 END,
			$2,$3,evaluation_case.content_hash,$4,$5
		FROM askdata.evaluation_cases evaluation_case
		WHERE evaluation_case.evaluation_set_id=$1
		  AND evaluation_case.id=ANY($6::uuid[])
		  AND evaluation_case.created_by<>$2 AND evaluation_case.content_updated_by<>$2
		  AND NOT EXISTS(SELECT 1 FROM askdata.evaluation_case_reviews existing
			WHERE existing.evaluation_case_id=evaluation_case.id AND existing.reviewer_id=$2)
		  AND (SELECT count(*) FROM askdata.evaluation_case_reviews existing
			WHERE existing.evaluation_case_id=evaluation_case.id)<2`, evaluationSetID, scope.ActorID,
			input.Decision, input.Comment, strings.Repeat("0", 64), input.CaseIDs)
		if err != nil {
			return err
		}
		result.ReviewedCount = int(updated.RowsAffected() + inserted.RowsAffected())
		if result.ReviewedCount == 0 {
			return ErrEvaluationSetReviewConflict
		}
		return nil
	})
	return result, normalizeEvaluationSetError(err)
}

func (store *PostgresStore) SealEvaluationSet(
	ctx context.Context, scope AdminScope, evaluationSetID string,
) (EvaluationSetSealResult, error) {
	if store == nil || store.pool == nil || scope.Validate(ctx) != nil || !canonicalAdminUUID(evaluationSetID) {
		return EvaluationSetSealResult{}, ErrEvaluationSetInvalid
	}
	result := EvaluationSetSealResult{EvaluationSetID: evaluationSetID}
	err := database.WithTenantTx(ctx, store.pool, scope.TenantID, func(tx pgx.Tx) error {
		if err := requireSemanticPermissionTx(ctx, tx, scope, AdminActionRelease, evaluationSetID); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT askdata.seal_evaluation_set($1,$2)`, evaluationSetID, scope.ActorID).Scan(&result.Sealed); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT status,sealed_case_count,sealed_review_count,
			COALESCE(sealed_content_hash,'') FROM askdata.evaluation_sets WHERE id=$1`, evaluationSetID).
			Scan(&result.Status, &result.CaseCount, &result.ReviewCount, &result.ContentHash); err != nil {
			return err
		}
		return nil
	})
	return result, normalizeEvaluationSetError(err)
}

func stableAdminCode(value string) bool {
	if len(value) < 1 || len(value) > 128 {
		return false
	}
	for index, character := range value {
		if index == 0 && !((character >= 'A' && character <= 'Z') || (character >= 'a' && character <= 'z')) {
			return false
		}
		if !((character >= 'A' && character <= 'Z') || (character >= 'a' && character <= 'z') ||
			(character >= '0' && character <= '9') || character == '_') {
			return false
		}
	}
	return true
}

func containsEvaluationValue(value string, allowed ...string) bool {
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
}

func nullableEvaluationHash(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func normalizeEvaluationSetError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, ErrRegistryPermissionDenied) || errors.Is(err, ErrEvaluationSetInvalid) ||
		errors.Is(err, ErrEvaluationSetNotFound) || errors.Is(err, ErrEvaluationSetCasesMissing) ||
		errors.Is(err, ErrEvaluationSetHintInvalid) || errors.Is(err, ErrEvaluationSetReviewConflict) {
		return err
	}
	return err
}

var _ EvaluationSetAdminBackend = (*PostgresStore)(nil)
