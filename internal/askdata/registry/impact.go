package registry

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/platform/database"
)

type ImpactChange struct {
	Kind     string `json:"kind"`
	ObjectID string `json:"objectId"`
}

type ImpactObjectRef struct {
	ID      askdata.ID `json:"id"`
	Name    string     `json:"name"`
	OwnerID askdata.ID `json:"ownerId"`
}

type RegistryImpactReport struct {
	CertifiedExamples []ImpactObjectRef `json:"certifiedExamples"`
	SavedQuestions    []ImpactObjectRef `json:"savedQuestions"`
	KPIBundles        []ImpactObjectRef `json:"kpiBundles"`
	EvaluationCases   []ImpactObjectRef `json:"evaluationCases"`
}

var ErrInvalidImpactChange = errors.New("invalid semantic impact change")

func (change ImpactChange) Validate() error {
	switch change.Kind {
	case "METRIC_VERSION", "DIMENSION_VERSION", "MEMBER_VERSION", "DATASET_VERSION", "SEMANTIC_RELEASE":
		parsed, err := uuid.Parse(change.ObjectID)
		if err != nil || parsed.String() != change.ObjectID {
			return ErrInvalidImpactChange
		}
	case "COMPONENT_TEMPLATE":
		parts := strings.Split(change.ObjectID, "@")
		if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" {
			return ErrInvalidImpactChange
		}
	default:
		return ErrInvalidImpactChange
	}
	return nil
}

type RegistryImpactAnalyzer struct{ pool *pgxpool.Pool }

func NewRegistryImpactAnalyzer(pool *pgxpool.Pool) *RegistryImpactAnalyzer {
	return &RegistryImpactAnalyzer{pool: pool}
}

func (analyzer *RegistryImpactAnalyzer) AnalyzeImpact(
	ctx context.Context, tenantID askdata.ID, change ImpactChange,
) (report RegistryImpactReport, err error) {
	if analyzer == nil || analyzer.pool == nil || tenantID.Validate() != nil || change.Validate() != nil {
		return RegistryImpactReport{}, ErrInvalidImpactChange
	}
	err = database.WithTenantTx(ctx, analyzer.pool, string(tenantID), func(tx pgx.Tx) error {
		var queryErr error
		if report.CertifiedExamples, queryErr = queryImpactRefs(ctx, tx, certifiedExampleImpactSQL, tenantID, change); queryErr != nil {
			return queryErr
		}
		if report.SavedQuestions, queryErr = queryImpactRefs(ctx, tx, savedQuestionImpactSQL, tenantID, change); queryErr != nil {
			return queryErr
		}
		if report.KPIBundles, queryErr = queryImpactRefs(ctx, tx, kpiBundleImpactSQL, tenantID, change); queryErr != nil {
			return queryErr
		}
		if report.EvaluationCases, queryErr = queryImpactRefs(ctx, tx, evaluationCaseImpactSQL, tenantID, change); queryErr != nil {
			return queryErr
		}
		return nil
	})
	if err != nil {
		return RegistryImpactReport{}, fmt.Errorf("analyze registry impact: %w", err)
	}
	return report, nil
}

// Each query binds governed version columns or normalized dependency indexes.
// For a release-wide change it reuses the retention reference registry.
const certifiedExampleImpactSQL = `
SELECT DISTINCT version.id::text,'Certified example '||version.certified_example_id::text,version.owner_id::text
FROM askdata.certified_example_versions AS version
WHERE version.tenant_id=$1 AND version.status='CERTIFIED' AND (
  ($2='METRIC_VERSION' AND $4::uuid=ANY(version.expected_metric_version_ids))
  OR ($2='DIMENSION_VERSION' AND $4::uuid=ANY(version.expected_dimension_version_ids))
  OR ($2='MEMBER_VERSION' AND EXISTS(
    SELECT 1 FROM jsonb_array_elements(version.expected_member_values) AS member
    WHERE member->>'memberVersionId'=$3
  ))
  OR ($2='SEMANTIC_RELEASE' AND EXISTS(
    SELECT 1 FROM askdata.release_references AS reference
    WHERE reference.tenant_id=version.tenant_id AND reference.release_id=$4::uuid
      AND reference.reference_type='CERTIFIED_EXAMPLE' AND reference.reference_id=version.id
      AND reference.released_at IS NULL
  ))
)
ORDER BY 1`

const savedQuestionImpactSQL = `
SELECT DISTINCT question.id::text,question.name,question.owner_user_id::text
FROM askdata.saved_questions AS question
JOIN askdata.saved_question_dependencies AS dependency
  ON dependency.saved_question_id=question.id AND dependency.tenant_id=question.tenant_id
WHERE question.tenant_id=$1 AND question.status<>'ARCHIVED'
  AND dependency.dependency_type=$2 AND dependency.dependency_id=$3
  AND ($4::uuid IS NULL OR $4::uuid IS NOT NULL)
ORDER BY 1`

const kpiBundleImpactSQL = `
SELECT DISTINCT version.id::text,bundle.name,version.owner_id::text
FROM askdata.kpi_bundle_versions AS version
JOIN askdata.kpi_bundles AS bundle
  ON bundle.id=version.kpi_bundle_id AND bundle.tenant_id=version.tenant_id
WHERE version.tenant_id=$1 AND version.status='CERTIFIED' AND (
  ($2='METRIC_VERSION' AND EXISTS(
    SELECT 1 FROM jsonb_array_elements(version.items) AS item WHERE item->>'metricVersionId'=$3
  ))
  OR ($2='DIMENSION_VERSION' AND (
    $4::uuid=ANY(version.default_dimension_version_ids) OR EXISTS(
      SELECT 1 FROM jsonb_array_elements(version.items) AS item,
        LATERAL jsonb_array_elements_text(COALESCE(item->'groupByDimensionVersionIds','[]'::jsonb)) AS dimension(id)
      WHERE dimension.id=$3
    )
  ))
  OR ($2='SEMANTIC_RELEASE' AND EXISTS(
    SELECT 1 FROM askdata.release_references AS reference
    WHERE reference.tenant_id=version.tenant_id AND reference.release_id=$4::uuid
      AND reference.reference_type='KPI_BUNDLE' AND reference.reference_id=version.id
      AND reference.released_at IS NULL
  ))
)
ORDER BY 1`

const evaluationCaseImpactSQL = `
SELECT DISTINCT version.id::text,'Evaluation case '||version.evaluation_case_asset_id::text,version.owner_id::text
FROM askdata.evaluation_case_versions AS version
WHERE version.tenant_id=$1 AND version.status='CERTIFIED' AND (
  ($2='METRIC_VERSION' AND $4::uuid=ANY(version.expected_metric_version_ids))
  OR ($2='DIMENSION_VERSION' AND $4::uuid=ANY(version.expected_dimension_version_ids))
  OR ($2='MEMBER_VERSION' AND EXISTS(
    SELECT 1 FROM jsonb_array_elements(version.expected_member_values) AS member
    WHERE member->>'memberVersionId'=$3
  ))
  OR ($2='SEMANTIC_RELEASE' AND EXISTS(
    SELECT 1 FROM askdata.release_references AS reference
    WHERE reference.tenant_id=version.tenant_id AND reference.release_id=$4::uuid
      AND reference.reference_type='EVALUATION_CASE' AND reference.reference_id=version.id
      AND reference.released_at IS NULL
  ))
)
ORDER BY 1`

func queryImpactRefs(ctx context.Context, tx pgx.Tx, query string, tenantID askdata.ID, change ImpactChange) ([]ImpactObjectRef, error) {
	var objectUUID any
	if parsed, err := uuid.Parse(change.ObjectID); err == nil {
		objectUUID = parsed
	}
	rows, err := tx.Query(ctx, query, tenantID, change.Kind, change.ObjectID, objectUUID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []ImpactObjectRef{}
	for rows.Next() {
		var item ImpactObjectRef
		if err := rows.Scan(&item.ID, &item.Name, &item.OwnerID); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}
