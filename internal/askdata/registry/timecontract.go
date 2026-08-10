package registry

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"intelligent-report-generation-system/internal/askdata"
)

const (
	PlatformDefaultIncompletePeriodPolicy IncompletePeriodPolicy = "MTD"

	TimeContractMissing          = "TIME_CONTRACT_MISSING"
	TimeInvalidTimezone          = "TIME_INVALID_TIMEZONE"
	TimeCalendarRequired         = "TIME_CALENDAR_REQUIRED"
	TimeCalendarNotActive        = "TIME_CALENDAR_NOT_ACTIVE"
	TimeUnsupportedGrain         = "TIME_UNSUPPORTED_GRAIN"
	TimeContractVersionImmutable = "TIME_CONTRACT_VERSION_IMMUTABLE"
)

type IncompletePeriodPolicy string

const (
	IncompletePeriodMTD          IncompletePeriodPolicy = "MTD"
	IncompletePeriodFull         IncompletePeriodPolicy = "FULL_PERIOD"
	IncompletePeriodLastComplete IncompletePeriodPolicy = "LAST_COMPLETE"
)

type WeekStart string

const (
	WeekStartMonday WeekStart = "MONDAY"
	WeekStartSunday WeekStart = "SUNDAY"
)

type WeekNumbering string

const (
	WeekNumberingISO WeekNumbering = "ISO"
	WeekNumberingUS  WeekNumbering = "US"
)

type FiscalMonthRule string

const (
	FiscalMonthCalendar     FiscalMonthRule = "CALENDAR"
	FiscalMonthFourFourFive FiscalMonthRule = "FOUR_FOUR_FIVE"
	FiscalMonthCustomTable  FiscalMonthRule = "CUSTOM_TABLE"
)

type ComparisonAlignment string

const (
	ComparisonSameDayCount      ComparisonAlignment = "SAME_DAY_COUNT"
	ComparisonSameCalendarRange ComparisonAlignment = "SAME_CALENDAR_RANGE"
)

type MonthEndOverflowRule string

const (
	MonthEndClampToLastDay MonthEndOverflowRule = "CLAMP_TO_LAST_DAY"
	MonthEndSkip           MonthEndOverflowRule = "SKIP"
)

type TimeGrain string

const (
	TimeGrainDay           TimeGrain = "DAY"
	TimeGrainWeek          TimeGrain = "WEEK"
	TimeGrainMonth         TimeGrain = "MONTH"
	TimeGrainQuarter       TimeGrain = "QUARTER"
	TimeGrainYear          TimeGrain = "YEAR"
	TimeGrainFiscalMonth   TimeGrain = "FISCAL_MONTH"
	TimeGrainFiscalQuarter TimeGrain = "FISCAL_QUARTER"
	TimeGrainFiscalYear    TimeGrain = "FISCAL_YEAR"
)

type PolicySource string

const (
	PolicySourceMetric          PolicySource = "METRIC"
	PolicySourceTimeContract    PolicySource = "TIME_CONTRACT"
	PolicySourceDomain          PolicySource = "DOMAIN"
	PolicySourcePlatformDefault PolicySource = "PLATFORM_DEFAULT"
)

type TimeContract struct {
	ID          string    `json:"id"`
	TenantID    string    `json:"tenantId"`
	DomainID    string    `json:"domainId"`
	Code        string    `json:"code"`
	Name        string    `json:"name"`
	OwnerUserID string    `json:"ownerUserId"`
	CreatedAt   time.Time `json:"createdAt,omitempty"`
	UpdatedAt   time.Time `json:"updatedAt,omitempty"`
}

type TimeContractVersion struct {
	ID                       string                 `json:"id"`
	TenantID                 string                 `json:"tenantId"`
	DomainID                 string                 `json:"domainId"`
	TimeContractID           string                 `json:"timeContractId"`
	VersionNo                int                    `json:"versionNo"`
	Status                   VersionStatus          `json:"status"`
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
	ContentHash              askdata.ContentHash    `json:"contentHash"`
	CreatedAt                time.Time              `json:"createdAt,omitempty"`
	UpdatedAt                time.Time              `json:"updatedAt,omitempty"`
}

// Domain is the bounded policy projection required to resolve an incomplete
// period. It deliberately carries no runtime or authorization state.
type Domain struct {
	DefaultIncompletePeriodPolicy IncompletePeriodPolicy `json:"defaultIncompletePeriodPolicy,omitempty"`
}

type CalendarDatasetVersion struct {
	TenantID                  string
	DomainID                  string
	DatasetVersionID          string
	DatasetStatus             string
	VersionStatus             string
	CurrentPublishedVersionID string
}

type CalendarDatasetVersionResolver interface {
	ResolveCalendarDatasetVersion(ctx context.Context, tenantID, domainID, versionID string) (CalendarDatasetVersion, error)
}

func (contract TimeContract) Validate() error {
	validation := validator{}
	validateUUID(&validation, "id", contract.ID, true)
	validateUUID(&validation, "tenantId", contract.TenantID, true)
	validateUUID(&validation, "domainId", contract.DomainID, true)
	validateUUID(&validation, "ownerUserId", contract.OwnerUserID, true)
	validateCodeName(&validation, contract.Code, contract.Name)
	return validation.result()
}

func (version TimeContractVersion) Validate() error {
	validation := validator{}
	validateUUID(&validation, "id", version.ID, true)
	validateUUID(&validation, "tenantId", version.TenantID, true)
	validateUUID(&validation, "domainId", version.DomainID, true)
	validateUUID(&validation, "timeContractId", version.TimeContractID, true)
	if version.VersionNo < 1 {
		validation.add(validationCodeRequired, "versionNo", "must be positive")
	}
	if version.Status != VersionStatusDraft && version.Status != VersionStatusCertified && version.Status != VersionStatusDeprecated {
		validation.add(validationCodeInvalidEnum, "status", "unsupported version status")
	}
	if strings.TrimSpace(version.Timezone) != version.Timezone || version.Timezone == "" {
		validation.add(TimeInvalidTimezone, "timezone", "must be a non-empty IANA timezone")
	} else if _, err := time.LoadLocation(version.Timezone); err != nil {
		validation.add(TimeInvalidTimezone, "timezone", "must be a valid IANA timezone")
	}
	if version.WeekStart != WeekStartMonday && version.WeekStart != WeekStartSunday {
		validation.add(validationCodeInvalidEnum, "weekStart", "must be MONDAY or SUNDAY")
	}
	if version.WeekNumbering != WeekNumberingISO && version.WeekNumbering != WeekNumberingUS {
		validation.add(validationCodeInvalidEnum, "weekNumbering", "must be ISO or US")
	}
	if version.FiscalYearStartMonth < 1 || version.FiscalYearStartMonth > 12 {
		validation.add(validationCodeInvalidEnum, "fiscalYearStartMonth", "must be between 1 and 12")
	}
	if !oneOf(string(version.FiscalMonthRule), string(FiscalMonthCalendar), string(FiscalMonthFourFourFive), string(FiscalMonthCustomTable)) {
		validation.add(validationCodeInvalidEnum, "fiscalMonthRule", "unsupported fiscal month rule")
	}
	if version.IncompletePeriodPolicy != "" && !validIncompletePeriodPolicy(version.IncompletePeriodPolicy) {
		validation.add(validationCodeInvalidEnum, "incompletePeriodPolicy", "unsupported incomplete-period policy")
	}
	if !oneOf(string(version.ComparisonAlignment), string(ComparisonSameDayCount), string(ComparisonSameCalendarRange)) {
		validation.add(validationCodeInvalidEnum, "comparisonAlignment", "unsupported comparison alignment")
	}
	if !oneOf(string(version.MonthEndOverflowRule), string(MonthEndClampToLastDay), string(MonthEndSkip)) {
		validation.add(validationCodeInvalidEnum, "monthEndOverflowRule", "unsupported month-end overflow rule")
	}
	if len(version.SupportedGrains) == 0 {
		validation.add(TimeUnsupportedGrain, "supportedGrains", "must contain at least one grain")
	}
	seen := make(map[TimeGrain]struct{}, len(version.SupportedGrains))
	for index, grain := range version.SupportedGrains {
		path := fmt.Sprintf("supportedGrains[%d]", index)
		if !validTimeGrain(grain) {
			validation.add(TimeUnsupportedGrain, path, "unsupported time grain")
		}
		if _, exists := seen[grain]; exists {
			validation.add(validationCodeDuplicate, path, "time grain is duplicated")
		}
		seen[grain] = struct{}{}
	}
	if strings.TrimSpace(version.DataAvailableThroughExpr) == "" || strings.TrimSpace(version.DataAvailableThroughExpr) != version.DataAvailableThroughExpr || len(version.DataAvailableThroughExpr) > 512 || strings.ContainsAny(version.DataAvailableThroughExpr, "\x00\r\n") {
		validation.add(validationCodeRequired, "dataAvailableThroughExpr", "must be a safe bounded control-plane expression")
	}
	if version.ExpectedLagHours < 0 {
		validation.add(validationCodeInvalidEnum, "expectedLagHours", "must be non-negative")
	}
	if version.CalendarDatasetVersionID != "" {
		validateUUID(&validation, "calendarDatasetVersionId", version.CalendarDatasetVersionID, true)
	}
	if requiresCalendar(version) && version.CalendarDatasetVersionID == "" {
		validation.add(TimeCalendarRequired, "calendarDatasetVersionId", "fiscal grains and non-calendar fiscal rules require a calendar dataset version")
	}
	if err := version.ContentHash.Validate(); err != nil {
		validation.add(validationCodeInvalidID, "contentHash", err.Error())
	}
	return validation.result()
}

func ValidateTimeContractVersion(ctx context.Context, version TimeContractVersion, resolver CalendarDatasetVersionResolver) error {
	if err := version.Validate(); err != nil {
		return err
	}
	if version.CalendarDatasetVersionID == "" {
		return nil
	}
	if resolver == nil {
		return ValidationErrors{Issues: []ValidationIssue{{Code: TimeCalendarNotActive, Path: "calendarDatasetVersionId", Message: "calendar dataset resolver is unavailable"}}}
	}
	calendar, err := resolver.ResolveCalendarDatasetVersion(ctx, version.TenantID, version.DomainID, version.CalendarDatasetVersionID)
	if err != nil && !errors.Is(err, context.Canceled) {
		return ValidationErrors{Issues: []ValidationIssue{{Code: TimeCalendarNotActive, Path: "calendarDatasetVersionId", Message: "calendar dataset version is missing or unavailable"}}}
	}
	if err != nil {
		return err
	}
	if calendar.TenantID != version.TenantID || calendar.DomainID != version.DomainID ||
		calendar.DatasetVersionID != version.CalendarDatasetVersionID || calendar.DatasetStatus != "PUBLISHED" ||
		calendar.VersionStatus != "PUBLISHED" || calendar.CurrentPublishedVersionID != version.CalendarDatasetVersionID {
		return ValidationErrors{Issues: []ValidationIssue{{Code: TimeCalendarNotActive, Path: "calendarDatasetVersionId", Message: "calendar dataset version must be the current PUBLISHED version in the same tenant and domain"}}}
	}
	return nil
}

func ContentHash(version TimeContractVersion) (askdata.ContentHash, error) {
	contract := timeContractContent(version)
	hash, _, err := CanonicalContentHash(contract)
	return hash, err
}

func TimeContractReleaseObject(version TimeContractVersion) (ReleaseObject, error) {
	if version.Status != VersionStatusCertified {
		return ReleaseObject{}, errors.New("time contract version must be CERTIFIED before release")
	}
	return NewReleaseObject(ReleaseObjectTimeContract, version.TimeContractID, version.ID,
		SensitivityInternal, timeContractContent(version), version.ContentHash)
}

func ResolveIncompletePeriodPolicy(metric MetricVersion, domain Domain, contract TimeContractVersion) (IncompletePeriodPolicy, PolicySource) {
	if metric.IncompletePeriodPolicyOverride != "" {
		return metric.IncompletePeriodPolicyOverride, PolicySourceMetric
	}
	if contract.IncompletePeriodPolicy != "" {
		return contract.IncompletePeriodPolicy, PolicySourceTimeContract
	}
	if domain.DefaultIncompletePeriodPolicy != "" {
		return domain.DefaultIncompletePeriodPolicy, PolicySourceDomain
	}
	return PlatformDefaultIncompletePeriodPolicy, PolicySourcePlatformDefault
}

func ValidateSupportedGrain(version TimeContractVersion, grain TimeGrain) error {
	for _, supported := range version.SupportedGrains {
		if supported == grain {
			return nil
		}
	}
	return ValidationErrors{Issues: []ValidationIssue{{Code: TimeUnsupportedGrain, Path: "grain", Message: fmt.Sprintf("%s is not supported by time contract version %s", grain, version.ID)}}}
}

func timeContractContent(version TimeContractVersion) any {
	grains := append([]TimeGrain(nil), version.SupportedGrains...)
	sort.Slice(grains, func(left, right int) bool { return grains[left] < grains[right] })
	return struct {
		Type                     string                 `json:"type"`
		TimeContractID           string                 `json:"timeContractId"`
		VersionNo                int                    `json:"versionNo"`
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
	}{
		"TIME_CONTRACT", version.TimeContractID, version.VersionNo, version.Timezone,
		version.WeekStart, version.WeekNumbering, version.FiscalYearStartMonth,
		version.FiscalMonthRule, version.IncompletePeriodPolicy, version.ComparisonAlignment,
		version.MonthEndOverflowRule, grains, version.DataAvailableThroughExpr,
		version.ExpectedLagHours, version.CalendarDatasetVersionID,
	}
}

func validIncompletePeriodPolicy(policy IncompletePeriodPolicy) bool {
	return policy == IncompletePeriodMTD || policy == IncompletePeriodFull || policy == IncompletePeriodLastComplete
}

func validTimeGrain(grain TimeGrain) bool {
	switch grain {
	case TimeGrainDay, TimeGrainWeek, TimeGrainMonth, TimeGrainQuarter, TimeGrainYear,
		TimeGrainFiscalMonth, TimeGrainFiscalQuarter, TimeGrainFiscalYear:
		return true
	default:
		return false
	}
}

func requiresCalendar(version TimeContractVersion) bool {
	if version.FiscalMonthRule != FiscalMonthCalendar {
		return true
	}
	for _, grain := range version.SupportedGrains {
		if grain == TimeGrainFiscalMonth || grain == TimeGrainFiscalQuarter || grain == TimeGrainFiscalYear {
			return true
		}
	}
	return false
}
