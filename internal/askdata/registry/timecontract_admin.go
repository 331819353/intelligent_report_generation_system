package registry

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// TimeContractCreateInput is the complete, immutable policy contract used by
// semantic models. Creating it through this endpoint also certifies version 1:
// callers need RELEASE permission and the operation is atomic and audited.
type TimeContractCreateInput struct {
	Code                     string                 `json:"code"`
	Name                     string                 `json:"name"`
	Timezone                 string                 `json:"timezone"`
	WeekStart                WeekStart              `json:"weekStart"`
	WeekNumbering            WeekNumbering          `json:"weekNumbering"`
	FiscalYearStartMonth     int                    `json:"fiscalYearStartMonth"`
	FiscalMonthRule          FiscalMonthRule        `json:"fiscalMonthRule"`
	IncompletePeriodPolicy   IncompletePeriodPolicy `json:"incompletePeriodPolicy,omitempty"`
	ComparisonAlignment      ComparisonAlignment    `json:"comparisonAlignment"`
	MonthEndOverflowRule     MonthEndOverflowRule   `json:"monthEndOverflowRule"`
	SupportedGrains          []TimeGrain            `json:"supportedGrains"`
	DataAvailableThroughExpr string                 `json:"dataAvailableThroughExpr"`
	ExpectedLagHours         int                    `json:"expectedLagHours"`
	CalendarDatasetVersionID string                 `json:"calendarDatasetVersionId,omitempty"`
}

type TimeContractAdminBackend interface {
	CreateCertifiedTimeContract(context.Context, AdminScope, TimeContractCreateInput, AdminCommand) (AdminWriteResult, error)
}

type transactionCalendarResolver struct{ tx pgx.Tx }

func (resolver transactionCalendarResolver) ResolveCalendarDatasetVersion(
	ctx context.Context, tenantID, domainID, versionID string,
) (CalendarDatasetVersion, error) {
	var result CalendarDatasetVersion
	err := resolver.tx.QueryRow(ctx, `SELECT dataset.tenant_id::text,dataset.domain_id::text,
		version.id::text,version.status,dataset.status,
		COALESCE(dataset.current_published_version_id::text,'')
		FROM platform.dataset_versions AS version
		JOIN platform.datasets AS dataset
		  ON dataset.id=version.dataset_id AND dataset.tenant_id=version.tenant_id
		WHERE version.id=$1 AND version.tenant_id=$2 AND dataset.domain_id=$3
		  AND dataset.deleted_at IS NULL`, versionID, tenantID, domainID).Scan(
		&result.TenantID, &result.DomainID, &result.DatasetVersionID,
		&result.VersionStatus, &result.DatasetStatus, &result.CurrentPublishedVersionID,
	)
	return result, err
}

func (store *PostgresStore) CreateCertifiedTimeContract(
	ctx context.Context,
	scope AdminScope,
	input TimeContractCreateInput,
	command AdminCommand,
) (AdminWriteResult, error) {
	return store.runAdminWrite(
		ctx, scope, AdminResourceTimeContract, AdminActionRelease, "",
		"TIME_CONTRACT_CERTIFIED", command,
		func(ctx context.Context, tx pgx.Tx) (AdminWriteResult, error) {
			contractID := stableAdminID(command.RequestID, string(AdminResourceTimeContract), "contract")
			versionID := stableAdminID(command.RequestID, string(AdminResourceTimeContract), "version-1")
			contract := TimeContract{
				ID: contractID, TenantID: scope.TenantID, DomainID: scope.DomainID,
				Code: input.Code, Name: input.Name, OwnerUserID: scope.ActorID,
			}
			if err := contract.Validate(); err != nil {
				return AdminWriteResult{}, err
			}
			version := TimeContractVersion{
				ID: versionID, TenantID: scope.TenantID, DomainID: scope.DomainID,
				TimeContractID: contractID, VersionNo: 1, Status: VersionStatusCertified,
				Timezone: input.Timezone, WeekStart: input.WeekStart,
				WeekNumbering:            input.WeekNumbering,
				FiscalYearStartMonth:     input.FiscalYearStartMonth,
				FiscalMonthRule:          input.FiscalMonthRule,
				IncompletePeriodPolicy:   input.IncompletePeriodPolicy,
				ComparisonAlignment:      input.ComparisonAlignment,
				MonthEndOverflowRule:     input.MonthEndOverflowRule,
				SupportedGrains:          append([]TimeGrain(nil), input.SupportedGrains...),
				DataAvailableThroughExpr: input.DataAvailableThroughExpr,
				ExpectedLagHours:         input.ExpectedLagHours,
				CalendarDatasetVersionID: input.CalendarDatasetVersionID,
			}
			contentHash, err := ContentHash(version)
			if err != nil {
				return AdminWriteResult{}, err
			}
			version.ContentHash = contentHash
			if err := ValidateTimeContractVersion(ctx, version, transactionCalendarResolver{tx: tx}); err != nil {
				return AdminWriteResult{}, err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO askdata.time_contracts(
				id,tenant_id,domain_id,code,name,owner_user_id
			) VALUES($1,$2,$3,$4,$5,$6)`, contract.ID, contract.TenantID,
				contract.DomainID, contract.Code, contract.Name, contract.OwnerUserID); err != nil {
				return AdminWriteResult{}, err
			}
			if err := tx.QueryRow(ctx, `INSERT INTO askdata.time_contract_versions(
				id,tenant_id,domain_id,time_contract_id,version_no,status,timezone,
				week_start,week_numbering,fiscal_year_start_month,fiscal_month_rule,
				incomplete_period_policy,comparison_alignment,month_end_overflow_rule,
				supported_grains,data_available_through_expr,expected_lag_hours,
				calendar_dataset_version_id,content_hash
			) VALUES($1,$2,$3,$4,1,'CERTIFIED',$5,$6,$7,$8,$9,NULLIF($10,'')::text,
				$11,$12,$13,$14,$15,NULLIF($16,'')::uuid,$17)
			RETURNING created_at,updated_at`, version.ID, version.TenantID, version.DomainID,
				version.TimeContractID, version.Timezone, version.WeekStart,
				version.WeekNumbering, version.FiscalYearStartMonth,
				version.FiscalMonthRule, version.IncompletePeriodPolicy,
				version.ComparisonAlignment, version.MonthEndOverflowRule,
				timeGrainStrings(version.SupportedGrains), version.DataAvailableThroughExpr,
				version.ExpectedLagHours, version.CalendarDatasetVersionID,
				version.ContentHash).Scan(&version.CreatedAt, &version.UpdatedAt); err != nil {
				return AdminWriteResult{}, err
			}
			updatedAt := version.UpdatedAt
			return AdminWriteResult{
				ResourceType: AdminResourceTimeContract, ResourceID: version.ID,
				ObjectID: contract.ID, ContentHash: version.ContentHash,
				Status: string(version.Status), UpdatedAt: &updatedAt,
			}, nil
		},
	)
}

func timeGrainStrings(grains []TimeGrain) []string {
	result := make([]string, len(grains))
	for index, grain := range grains {
		result[index] = string(grain)
	}
	return result
}
