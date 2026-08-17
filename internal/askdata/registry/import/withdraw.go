package registryimport

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/jackc/pgx/v5"
)

type WithdrawRepository interface {
	WithdrawImportSelective(
		context.Context,
		string, string, string, string, string,
		SelectiveDraftWithdrawer,
	) ([]WithdrawalRejection, error)
}

type WithdrawInput struct {
	TenantID string
	DomainID string
	ActorID  string
	ImportID string
	Reason   string
}

type WithdrawResult struct {
	ImportID string                `json:"importId"`
	State    State                 `json:"state"`
	Rejected []WithdrawalRejection `json:"rejected"`
}

type WithdrawService struct {
	store      WithdrawRepository
	withdrawer SelectiveDraftWithdrawer
}

func NewWithdrawService(store WithdrawRepository, withdrawer SelectiveDraftWithdrawer) *WithdrawService {
	return &WithdrawService{store: store, withdrawer: withdrawer}
}

func (service *WithdrawService) Withdraw(ctx context.Context, input WithdrawInput) (WithdrawResult, error) {
	if service == nil || service.store == nil || service.withdrawer == nil ||
		!canonicalUUID(input.TenantID) || !canonicalUUID(input.DomainID) ||
		!canonicalUUID(input.ActorID) || !canonicalUUID(input.ImportID) {
		return WithdrawResult{}, ErrInvalidImport
	}
	rejected, err := service.store.WithdrawImportSelective(
		ctx, input.TenantID, input.DomainID, input.ActorID, input.ImportID,
		input.Reason, service.withdrawer,
	)
	if err != nil {
		return WithdrawResult{}, err
	}
	return WithdrawResult{ImportID: input.ImportID, State: StateWithdrawn, Rejected: rejected}, nil
}

type PostgresDraftWithdrawer struct{}

func NewPostgresDraftWithdrawer() *PostgresDraftWithdrawer { return &PostgresDraftWithdrawer{} }

func (withdrawer *PostgresDraftWithdrawer) WithdrawDraftsSelective(
	ctx context.Context,
	tx pgx.Tx,
	batch SemanticImport,
	rows []ImportRow,
) ([]WithdrawalRejection, error) {
	if withdrawer == nil || tx == nil {
		return nil, ErrInvalidImport
	}
	rejections := make([]WithdrawalRejection, 0)
	grouped := make(map[string][]ImportRow, len(rows))
	versionIDs := make([]string, 0, len(rows))
	for _, row := range rows {
		if _, exists := grouped[row.CreatedVersionID]; !exists {
			versionIDs = append(versionIDs, row.CreatedVersionID)
		}
		grouped[row.CreatedVersionID] = append(grouped[row.CreatedVersionID], row)
	}
	// 逆行号顺序撤回：BUNDLE 批内后提交的行可能依赖先提交的行（指标引用
	// 模型、成员引用维度），先删依赖者才能让被依赖者通过引用检查。
	sort.Slice(versionIDs, func(i, j int) bool {
		return grouped[versionIDs[i]][0].RowNo > grouped[versionIDs[j]][0].RowNo
	})
	for _, versionID := range versionIDs {
		versionRows := grouped[versionID]
		row := versionRows[0]
		rowType, err := rowAssetType(batch, row)
		if err != nil {
			return nil, err
		}
		table, err := versionTableForAsset(rowType)
		if err != nil {
			return nil, err
		}
		query := fmt.Sprintf(`SELECT status FROM askdata.%s WHERE id=$1 AND domain_id=$2 FOR UPDATE`, table)
		var status string
		if err := tx.QueryRow(ctx, query, versionID, batch.DomainID).Scan(&status); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				rejections = appendWithdrawalRejections(rejections, versionRows, "VERSION_NOT_FOUND", nil)
				continue
			}
			return nil, err
		}
		if status != "DRAFT" {
			rejections = appendWithdrawalRejections(rejections, versionRows, "VERSION_NOT_DRAFT", nil)
			continue
		}
		references, err := draftAndReleaseReferences(ctx, tx, batch, versionID)
		if err != nil {
			return nil, err
		}
		if len(references) > 0 {
			rejections = appendWithdrawalRejections(rejections, versionRows, "VERSION_REFERENCED", references)
			continue
		}
		query = fmt.Sprintf(`DELETE FROM askdata.%s WHERE id=$1 AND domain_id=$2 AND status='DRAFT'`, table)
		tag, err := tx.Exec(ctx, query, versionID, batch.DomainID)
		if err != nil {
			return nil, err
		}
		if tag.RowsAffected() != 1 {
			return nil, ErrImportConflict
		}
		if err := deleteOrphanIdentity(ctx, tx, rowType, row.CreatedObjectID); err != nil {
			return nil, err
		}
	}
	sort.Slice(rejections, func(i, j int) bool { return rejections[i].RowNo < rejections[j].RowNo })
	return rejections, nil
}

func appendWithdrawalRejections(
	target []WithdrawalRejection,
	rows []ImportRow,
	reason string,
	references []string,
) []WithdrawalRejection {
	for _, row := range rows {
		target = append(target, WithdrawalRejection{
			RowNo: row.RowNo, VersionID: row.CreatedVersionID, Reason: reason,
			References: append([]string(nil), references...),
		})
	}
	return target
}

func versionTableForAsset(asset AssetType) (string, error) {
	switch asset {
	case AssetModel:
		return "semantic_models", nil
	case AssetMeasure:
		return "measures", nil
	case AssetMetric:
		return "metric_versions", nil
	case AssetMetricDimension:
		return "metric_dimension_versions", nil
	case AssetDimension:
		return "dimensions", nil
	case AssetMember:
		return "dimension_members", nil
	case AssetHierarchy:
		return "hierarchies", nil
	case AssetRelationship:
		return "relationships", nil
	case AssetTerm, AssetKnowledge:
		return "business_term_versions", nil
	case AssetCertifiedExample:
		return "certified_example_versions", nil
	case AssetKPIBundle:
		return "kpi_bundle_versions", nil
	case AssetEvalCase:
		return "evaluation_case_versions", nil
	default:
		return "", ErrImportDraftContract
	}
}

func draftAndReleaseReferences(
	ctx context.Context,
	tx pgx.Tx,
	batch SemanticImport,
	versionID string,
) ([]string, error) {
	rows, err := tx.Query(ctx, `
		SELECT 'RELEASE:'||release_id::text FROM askdata.release_objects WHERE object_version_id=$1
		UNION ALL SELECT 'MODEL:ENTITY:'||id::text FROM askdata.semantic_models WHERE entity_version_id=$1 AND status='DRAFT'
		UNION ALL SELECT 'MODEL:TIME_CONTRACT:'||id::text FROM askdata.semantic_models WHERE time_contract_version_id=$1 AND status='DRAFT'
		UNION ALL SELECT 'MEASURE:MODEL:'||id::text FROM askdata.measures WHERE semantic_model_version_id=$1 AND status='DRAFT'
		UNION ALL SELECT 'METRIC:MODEL:'||id::text FROM askdata.metric_versions WHERE semantic_model_version_id=$1 AND status='DRAFT'
		UNION ALL SELECT 'METRIC:MEASURE:'||metric_version_id::text FROM askdata.metric_version_measures WHERE measure_version_id=$1
		UNION ALL SELECT 'DIMENSION:MODEL:'||id::text FROM askdata.dimensions WHERE semantic_model_version_id=$1 AND status='DRAFT'
		UNION ALL SELECT 'MEMBER:DIMENSION:'||id::text FROM askdata.dimension_members WHERE dimension_version_id=$1 AND status='DRAFT'
		UNION ALL SELECT 'MEMBER:PARENT:'||id::text FROM askdata.dimension_members WHERE parent_member_version_id=$1 AND status='DRAFT'
		UNION ALL SELECT 'HIERARCHY:DIMENSION:'||hierarchy_version_id::text FROM askdata.hierarchy_levels WHERE dimension_version_id=$1
		UNION ALL SELECT 'RELATIONSHIP:MODEL:'||id::text FROM askdata.relationships WHERE status='DRAFT' AND ($1 IN (left_model_version_id,right_model_version_id) OR bridge_model_version_id=$1)
		UNION ALL SELECT 'METRIC_DIMENSION:METRIC:'||id::text FROM askdata.metric_dimension_versions WHERE metric_version_id=$1 AND status='DRAFT'
		UNION ALL SELECT 'METRIC_DIMENSION:DIMENSION:'||id::text FROM askdata.metric_dimension_versions WHERE dimension_version_id=$1 AND status='DRAFT'
		UNION ALL SELECT 'TERM:TARGET:'||id::text FROM askdata.business_term_versions WHERE target_version_id=$1 AND status='DRAFT'
		UNION ALL SELECT 'CERTIFIED_EXAMPLE:METRIC:'||id::text FROM askdata.certified_example_versions WHERE status='DRAFT' AND $1=ANY(expected_metric_version_ids)
		UNION ALL SELECT 'CERTIFIED_EXAMPLE:DIMENSION:'||id::text FROM askdata.certified_example_versions WHERE status='DRAFT' AND $1=ANY(expected_dimension_version_ids)
		UNION ALL SELECT 'KPI_BUNDLE:DIMENSION:'||id::text FROM askdata.kpi_bundle_versions WHERE status='DRAFT' AND $1=ANY(default_dimension_version_ids)
		UNION ALL SELECT 'KPI_BUNDLE:METRIC:'||bundle.id::text FROM askdata.kpi_bundle_versions AS bundle, LATERAL jsonb_array_elements(bundle.items) AS item WHERE bundle.status='DRAFT' AND item->>'metricVersionId'=$1::text
		UNION ALL SELECT 'EVAL_CASE:METRIC:'||id::text FROM askdata.evaluation_case_versions WHERE status='DRAFT' AND $1=ANY(expected_metric_version_ids)
		UNION ALL SELECT 'EVAL_CASE:DIMENSION:'||id::text FROM askdata.evaluation_case_versions WHERE status='DRAFT' AND $1=ANY(expected_dimension_version_ids)
		ORDER BY 1`, versionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []string{}
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return nil, err
		}
		result = append(result, value)
	}
	return result, rows.Err()
}

func deleteOrphanIdentity(ctx context.Context, tx pgx.Tx, assetType AssetType, objectID string) error {
	var query string
	switch assetType {
	case AssetMetric:
		query = `DELETE FROM askdata.metrics AS identity WHERE id=$1 AND status='DRAFT'
			AND NOT EXISTS(SELECT 1 FROM askdata.metric_versions WHERE metric_id=identity.id)`
	case AssetMetricDimension:
		query = `DELETE FROM askdata.metric_dimensions AS identity WHERE id=$1
			AND NOT EXISTS(SELECT 1 FROM askdata.metric_dimension_versions WHERE metric_dimension_id=identity.id)`
	case AssetTerm, AssetKnowledge:
		query = `DELETE FROM askdata.business_terms AS identity WHERE id=$1
			AND NOT EXISTS(SELECT 1 FROM askdata.business_term_versions WHERE business_term_id=identity.id)`
	case AssetCertifiedExample:
		query = `DELETE FROM askdata.certified_examples AS identity WHERE id=$1
			AND NOT EXISTS(SELECT 1 FROM askdata.certified_example_versions WHERE certified_example_id=identity.id)`
	case AssetKPIBundle:
		query = `DELETE FROM askdata.kpi_bundles AS identity WHERE id=$1
			AND NOT EXISTS(SELECT 1 FROM askdata.kpi_bundle_versions WHERE kpi_bundle_id=identity.id)`
	case AssetEvalCase:
		query = `DELETE FROM askdata.evaluation_case_assets AS identity WHERE id=$1
			AND NOT EXISTS(SELECT 1 FROM askdata.evaluation_case_versions WHERE evaluation_case_asset_id=identity.id)`
	default:
		return nil
	}
	_, err := tx.Exec(ctx, query, objectID)
	return err
}
