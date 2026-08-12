package registry

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
)

// CreateReleaseFromCertified keeps canonical contract construction inside the
// registry. The UI selects a semantic version name; PostgreSQL resolves the
// exact certified object versions and hashes under the same transaction.
func (store *PostgresStore) CreateReleaseFromCertified(
	ctx context.Context, scope AdminScope, input ReleaseComposeInput, command AdminCommand,
) (AdminWriteResult, error) {
	return store.runAdminWrite(ctx, scope, AdminResourceRelease, AdminActionRelease, "",
		"RELEASE_DRAFT_COMPOSED", command, func(ctx context.Context, tx pgx.Tx) (AdminWriteResult, error) {
			objects, err := loadCertifiedCoreReleaseObjectsTx(ctx, tx, scope.DomainID)
			if err != nil {
				return AdminWriteResult{}, err
			}
			return createAdminReleaseDraftTx(ctx, tx, scope, input.SemanticVersion, objects, command.RequestID)
		})
}

func loadCertifiedCoreReleaseObjectsTx(ctx context.Context, tx pgx.Tx, domainID string) ([]ReleaseObject, error) {
	objects := []ReleaseObject{}

	modelRows, err := tx.Query(ctx, semanticModelAdminSelect+` WHERE domain_id=$1 AND status='CERTIFIED' ORDER BY id`, domainID)
	if err != nil {
		return nil, err
	}
	for modelRows.Next() {
		var value SemanticModel
		if err := scanSemanticModel(modelRows, &value); err != nil {
			modelRows.Close()
			return nil, err
		}
		object, err := SemanticModelReleaseObject(value)
		if err != nil {
			modelRows.Close()
			return nil, err
		}
		objects = append(objects, object)
	}
	if err := modelRows.Err(); err != nil {
		modelRows.Close()
		return nil, err
	}
	modelRows.Close()

	measureRows, err := tx.Query(ctx, measureAdminSelect+` WHERE domain_id=$1 AND status='CERTIFIED' ORDER BY id`, domainID)
	if err != nil {
		return nil, err
	}
	for measureRows.Next() {
		var value Measure
		if err := scanMeasure(measureRows, &value); err != nil {
			measureRows.Close()
			return nil, err
		}
		object, err := MeasureReleaseObject(value)
		if err != nil {
			measureRows.Close()
			return nil, err
		}
		objects = append(objects, object)
	}
	if err := measureRows.Err(); err != nil {
		measureRows.Close()
		return nil, err
	}
	measureRows.Close()

	metricRows, err := tx.Query(ctx, metricVersionAdminSelect+` WHERE version.domain_id=$1 AND version.status='CERTIFIED'
		GROUP BY version.id ORDER BY version.id`, domainID)
	if err != nil {
		return nil, err
	}
	for metricRows.Next() {
		var value MetricVersion
		if err := scanMetricVersion(metricRows, &value); err != nil {
			metricRows.Close()
			return nil, err
		}
		object, err := MetricVersionReleaseObject(value)
		if err != nil {
			metricRows.Close()
			return nil, err
		}
		objects = append(objects, object)
	}
	if err := metricRows.Err(); err != nil {
		metricRows.Close()
		return nil, err
	}
	metricRows.Close()

	dimensionRows, err := tx.Query(ctx, dimensionAdminSelect+` WHERE domain_id=$1 AND status='CERTIFIED' ORDER BY id`, domainID)
	if err != nil {
		return nil, err
	}
	for dimensionRows.Next() {
		var value Dimension
		if err := scanDimension(dimensionRows, &value); err != nil {
			dimensionRows.Close()
			return nil, err
		}
		object, err := DimensionReleaseObject(value)
		if err != nil {
			dimensionRows.Close()
			return nil, err
		}
		objects = append(objects, object)
	}
	if err := dimensionRows.Err(); err != nil {
		dimensionRows.Close()
		return nil, err
	}
	dimensionRows.Close()

	compatibilityRows, err := tx.Query(ctx, metricDimensionAdminSelect+` WHERE domain_id=$1 AND status='CERTIFIED' ORDER BY id`, domainID)
	if err != nil {
		return nil, err
	}
	for compatibilityRows.Next() {
		var value MetricDimension
		if err := scanMetricDimension(compatibilityRows, &value); err != nil {
			compatibilityRows.Close()
			return nil, err
		}
		object, err := MetricDimensionReleaseObject(value)
		if err != nil {
			compatibilityRows.Close()
			return nil, err
		}
		objects = append(objects, object)
	}
	if err := compatibilityRows.Err(); err != nil {
		compatibilityRows.Close()
		return nil, err
	}
	compatibilityRows.Close()

	termRows, err := tx.Query(ctx, businessTermAdminSelect+` WHERE domain_id=$1 AND status='CERTIFIED' ORDER BY id`, domainID)
	if err != nil {
		return nil, err
	}
	for termRows.Next() {
		var value BusinessTerm
		if err := scanBusinessTerm(termRows, &value); err != nil {
			termRows.Close()
			return nil, err
		}
		object, err := BusinessTermReleaseObject(value)
		if err != nil {
			termRows.Close()
			return nil, err
		}
		objects = append(objects, object)
	}
	if err := termRows.Err(); err != nil {
		termRows.Close()
		return nil, err
	}
	termRows.Close()

	bundleRows, err := tx.Query(ctx, kpiBundleAdminSelect+` WHERE version.domain_id=$1 AND version.status='CERTIFIED' ORDER BY version.id`, domainID)
	if err != nil {
		return nil, err
	}
	for bundleRows.Next() {
		var value KPIBundle
		if err := scanKPIBundle(bundleRows, &value); err != nil {
			bundleRows.Close()
			return nil, err
		}
		object, err := KPIBundleReleaseObject(value)
		if err != nil {
			bundleRows.Close()
			return nil, err
		}
		objects = append(objects, object)
	}
	if err := bundleRows.Err(); err != nil {
		bundleRows.Close()
		return nil, err
	}
	bundleRows.Close()

	// Row access policies travel with the release so which rows a reader may see
	// is pinned by the same manifest as what the numbers mean. A release that
	// dropped them would silently widen access.
	policyRows, err := tx.Query(ctx, rowAccessPolicyAdminSelect+` WHERE domain_id=$1 AND status='CERTIFIED' ORDER BY id`, domainID)
	if err != nil {
		return nil, err
	}
	for policyRows.Next() {
		var value RowAccessPolicy
		if err := scanRowAccessPolicy(policyRows, &value); err != nil {
			policyRows.Close()
			return nil, err
		}
		object, err := RowAccessPolicyReleaseObject(value)
		if err != nil {
			policyRows.Close()
			return nil, err
		}
		objects = append(objects, object)
	}
	if err := policyRows.Err(); err != nil {
		policyRows.Close()
		return nil, err
	}
	policyRows.Close()

	// Certified quality rules travel with the release so a run can resolve what
	// the release promised to check, not what happens to be certified now.
	qualityRows, err := tx.Query(ctx, qualityRuleAdminSelect+` WHERE domain_id=$1 AND status='CERTIFIED' ORDER BY id`, domainID)
	if err != nil {
		return nil, err
	}
	for qualityRows.Next() {
		var value QualityRule
		if err := scanQualityRule(qualityRows, &value); err != nil {
			qualityRows.Close()
			return nil, err
		}
		object, err := QualityRuleReleaseObject(value)
		if err != nil {
			qualityRows.Close()
			return nil, err
		}
		objects = append(objects, object)
	}
	if err := qualityRows.Err(); err != nil {
		qualityRows.Close()
		return nil, err
	}
	qualityRows.Close()

	timeRows, err := tx.Query(ctx, `SELECT id::text,tenant_id::text,domain_id::text,
		time_contract_id::text,version_no,status,timezone,week_start,week_numbering,
		fiscal_year_start_month,fiscal_month_rule,COALESCE(incomplete_period_policy,''),
		comparison_alignment,month_end_overflow_rule,supported_grains,
		data_available_through_expr,expected_lag_hours,
		COALESCE(calendar_dataset_version_id::text,''),content_hash,created_at,updated_at
	FROM askdata.time_contract_versions
	WHERE domain_id=$1 AND status='CERTIFIED' ORDER BY id`, domainID)
	if err != nil {
		return nil, err
	}
	for timeRows.Next() {
		var value TimeContractVersion
		if err := timeRows.Scan(
			&value.ID, &value.TenantID, &value.DomainID, &value.TimeContractID,
			&value.VersionNo, &value.Status, &value.Timezone, &value.WeekStart,
			&value.WeekNumbering, &value.FiscalYearStartMonth, &value.FiscalMonthRule,
			&value.IncompletePeriodPolicy, &value.ComparisonAlignment,
			&value.MonthEndOverflowRule, &value.SupportedGrains,
			&value.DataAvailableThroughExpr, &value.ExpectedLagHours,
			&value.CalendarDatasetVersionID, &value.ContentHash, &value.CreatedAt,
			&value.UpdatedAt,
		); err != nil {
			timeRows.Close()
			return nil, err
		}
		object, err := TimeContractReleaseObject(value)
		if err != nil {
			timeRows.Close()
			return nil, err
		}
		objects = append(objects, object)
	}
	if err := timeRows.Err(); err != nil {
		timeRows.Close()
		return nil, err
	}
	timeRows.Close()

	if len(objects) == 0 {
		return nil, fmt.Errorf("%w: no certified core semantic objects are available", ErrRegistryInvalidRequest)
	}
	return objects, nil
}

func createAdminReleaseDraftTx(
	ctx context.Context, tx pgx.Tx, scope AdminScope, semanticVersion string,
	objects []ReleaseObject, requestID string,
) (AdminWriteResult, error) {
	manifest, err := BuildReleaseManifest(objects)
	if err != nil {
		return AdminWriteResult{}, fmt.Errorf("%w: %v", ErrRegistryInvalidRequest, err)
	}
	trimmed := strings.TrimSpace(semanticVersion)
	if trimmed != semanticVersion {
		return AdminWriteResult{}, fmt.Errorf("%w: semanticVersion must be trimmed", ErrRegistryInvalidRequest)
	}
	releaseID := stableAdminID(requestID, string(AdminResourceRelease), "record")
	tag, err := tx.Exec(ctx, `INSERT INTO askdata.releases(
		id,tenant_id,domain_id,semantic_version,content_hash,status,
		object_count,created_by,updated_by
	) VALUES($1,$2,$3,$4,$5,'DRAFT',$6,$7,$7)
	ON CONFLICT(tenant_id,domain_id,semantic_version) DO NOTHING`,
		releaseID, scope.TenantID, scope.DomainID, semanticVersion,
		manifest.ContentHash, len(manifest.Objects), scope.ActorID)
	if err != nil {
		return AdminWriteResult{}, err
	}
	if tag.RowsAffected() != 1 {
		var existingID, existingHash, status string
		if err := tx.QueryRow(ctx, `SELECT id::text,content_hash,status
			FROM askdata.releases WHERE domain_id=$1 AND semantic_version=$2`,
			scope.DomainID, semanticVersion).Scan(&existingID, &existingHash, &status); err != nil {
			return AdminWriteResult{}, err
		}
		if existingHash != string(manifest.ContentHash) || status != "DRAFT" {
			return AdminWriteResult{}, ErrRegistryConflict
		}
		releaseID = existingID
	} else {
		for _, object := range manifest.Objects {
			if _, err := tx.Exec(ctx, `INSERT INTO askdata.release_objects(
				tenant_id,domain_id,release_id,object_type,object_id,
				object_version_id,content_hash,sensitivity,contract_json
			) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
				scope.TenantID, scope.DomainID, releaseID, object.Type,
				object.ObjectID, object.ObjectVersionID, object.ContentHash,
				object.Sensitivity, object.Contract); err != nil {
				return AdminWriteResult{}, err
			}
		}
	}
	return AdminWriteResult{
		ResourceType: AdminResourceRelease, ResourceID: releaseID,
		ContentHash: manifest.ContentHash, Status: "DRAFT",
		SemanticVersion: semanticVersion,
	}, nil
}

var _ ReleaseComposer = (*PostgresStore)(nil)
