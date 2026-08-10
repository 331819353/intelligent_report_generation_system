package registry

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"

	"intelligent-report-generation-system/internal/askdata"
)

const validationCalendarVersion = "3e463c60-7abe-45be-b378-f802a7d7e2d4"

type calendarResolverStub struct {
	calendar CalendarDatasetVersion
	err      error
}

func (stub calendarResolverStub) ResolveCalendarDatasetVersion(context.Context, string, string, string) (CalendarDatasetVersion, error) {
	return stub.calendar, stub.err
}

func TestTimeContractSchemaRoundTripAndUnknownFieldRejection(t *testing.T) {
	version := validTimeContractVersion(t)
	raw, err := json.Marshal(version)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	var decoded TimeContractVersion
	if err := askdata.DecodeStrictJSON(raw, &decoded); err != nil {
		t.Fatalf("DecodeStrictJSON() error = %v", err)
	}
	if !reflect.DeepEqual(version, decoded) {
		t.Fatalf("round trip mismatch:\n%#v\n%#v", version, decoded)
	}
	schemaRaw, err := os.ReadFile("../../../api/schemas/time-contract-v1.schema.json")
	if err != nil {
		t.Fatalf("read time contract schema: %v", err)
	}
	var schema struct {
		AdditionalProperties bool                       `json:"additionalProperties"`
		Properties           map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(schemaRaw, &schema); err != nil {
		t.Fatalf("decode time contract schema: %v", err)
	}
	if schema.AdditionalProperties || len(schema.Properties) == 0 {
		t.Fatalf("time contract schema is not closed: %#v", schema)
	}
	unknown := strings.Replace(string(raw), `"timezone":`, `"unexpected":true,"timezone":`, 1)
	if err := askdata.DecodeStrictJSON([]byte(unknown), &decoded); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("unknown field error = %v", err)
	}
}

func TestTimeContractValidationAndCalendarActivity(t *testing.T) {
	version := validTimeContractVersion(t)
	if err := ValidateTimeContractVersion(context.Background(), version, nil); err != nil {
		t.Fatalf("ValidateTimeContractVersion() error = %v", err)
	}

	invalidTimezone := version
	invalidTimezone.Timezone = "Mars/Olympus"
	invalidTimezone.ContentHash = mustTimeContractHash(t, invalidTimezone)
	assertTimeIssue(t, invalidTimezone.Validate(), TimeInvalidTimezone, "timezone")

	fiscal := version
	fiscal.SupportedGrains = append(fiscal.SupportedGrains, TimeGrainFiscalMonth)
	fiscal.ContentHash = mustTimeContractHash(t, fiscal)
	assertTimeIssue(t, fiscal.Validate(), TimeCalendarRequired, "calendarDatasetVersionId")

	fiscal.CalendarDatasetVersionID = validationCalendarVersion
	fiscal.ContentHash = mustTimeContractHash(t, fiscal)
	inactive := calendarResolverStub{calendar: CalendarDatasetVersion{
		TenantID: fiscal.TenantID, DomainID: fiscal.DomainID,
		DatasetVersionID: fiscal.CalendarDatasetVersionID,
		DatasetStatus:    "PUBLISHED", VersionStatus: "DEPRECATED",
		CurrentPublishedVersionID: fiscal.CalendarDatasetVersionID,
	}}
	assertTimeIssue(t, ValidateTimeContractVersion(context.Background(), fiscal, inactive), TimeCalendarNotActive, "calendarDatasetVersionId")
	active := inactive
	active.calendar.VersionStatus = "PUBLISHED"
	if err := ValidateTimeContractVersion(context.Background(), fiscal, active); err != nil {
		t.Fatalf("active fiscal calendar rejected: %v", err)
	}
	missing := calendarResolverStub{err: errors.New("not found")}
	assertTimeIssue(t, ValidateTimeContractVersion(context.Background(), fiscal, missing), TimeCalendarNotActive, "calendarDatasetVersionId")
}

func TestResolveIncompletePeriodPolicyUsesFourLayerPrecedence(t *testing.T) {
	version := validTimeContractVersion(t)
	version.IncompletePeriodPolicy = ""
	metric := MetricVersion{}
	domain := Domain{}
	policy, source := ResolveIncompletePeriodPolicy(metric, domain, version)
	if policy != IncompletePeriodMTD || source != PolicySourcePlatformDefault {
		t.Fatalf("platform resolution = %s/%s", policy, source)
	}
	domain.DefaultIncompletePeriodPolicy = IncompletePeriodFull
	policy, source = ResolveIncompletePeriodPolicy(metric, domain, version)
	if policy != IncompletePeriodFull || source != PolicySourceDomain {
		t.Fatalf("domain resolution = %s/%s", policy, source)
	}
	version.IncompletePeriodPolicy = IncompletePeriodLastComplete
	policy, source = ResolveIncompletePeriodPolicy(metric, domain, version)
	if policy != IncompletePeriodLastComplete || source != PolicySourceTimeContract {
		t.Fatalf("contract resolution = %s/%s", policy, source)
	}
	metric.IncompletePeriodPolicyOverride = IncompletePeriodMTD
	policy, source = ResolveIncompletePeriodPolicy(metric, domain, version)
	if policy != IncompletePeriodMTD || source != PolicySourceMetric {
		t.Fatalf("metric resolution = %s/%s", policy, source)
	}
}

func TestTimeContractContentHashNormalizesGrainOrderAndPinsValues(t *testing.T) {
	left := validTimeContractVersion(t)
	right := left
	right.SupportedGrains = []TimeGrain{TimeGrainMonth, TimeGrainDay}
	right.ContentHash = mustTimeContractHash(t, right)
	if left.ContentHash != right.ContentHash {
		t.Fatalf("grain order changed hash: %s != %s", left.ContentHash, right.ContentHash)
	}
	right.ExpectedLagHours++
	right.ContentHash = mustTimeContractHash(t, right)
	if left.ContentHash == right.ContentHash {
		t.Fatal("value change did not change content hash")
	}
}

func TestTimeContractCertificationAndReleaseDependencyClosure(t *testing.T) {
	version := validTimeContractVersion(t)
	version.Status = VersionStatusCertified
	version.ContentHash = mustTimeContractHash(t, version)
	model := SemanticModel{
		VersionIdentity: VersionIdentity{
			ID: validationRow, TenantID: validationTenant, DomainID: validationDomain,
			ObjectID: validationObject, VersionNo: 1, Status: VersionStatusCertified,
			OwnerID: validationOwner,
		},
		Code: "sales_model", Name: "销售模型", DatasetID: validationObject,
		DatasetVersionID: validationRow, MaterializationID: validationCalendarVersion,
		DatasetSchemaHash: askdata.HashBytes([]byte("schema")), Layer: "DWS",
		GrainContract:      json.RawMessage(`{"keys":["order_id"]}`),
		PrimaryTimeFieldID: "order_date", TimeContractVersionID: version.ID,
	}
	model.ContentHash = contentHashForContract(semanticModelContract(model))
	if err := ValidateSemanticModelCertification(model, &version); err != nil {
		t.Fatalf("ValidateSemanticModelCertification() error = %v", err)
	}
	modelObject, err := SemanticModelReleaseObject(model)
	if err != nil {
		t.Fatalf("SemanticModelReleaseObject() error = %v", err)
	}
	timeObject, err := TimeContractReleaseObject(version)
	if err != nil {
		t.Fatalf("TimeContractReleaseObject() error = %v", err)
	}
	if _, err := BuildReleaseManifest([]ReleaseObject{modelObject}); err == nil || !strings.Contains(err.Error(), TimeContractMissing) {
		t.Fatalf("manifest without time contract error = %v", err)
	}
	manifest, err := BuildReleaseManifest([]ReleaseObject{modelObject, timeObject})
	if err != nil || len(manifest.Objects) != 2 {
		t.Fatalf("manifest with time contract = %#v, %v", manifest, err)
	}

	model.TimeContractVersionID = ""
	if err := ValidateSemanticModelCertification(model, nil); err == nil {
		t.Fatal("certification accepted a missing time contract")
	}
}

func TestValidateSupportedGrainFailsClosed(t *testing.T) {
	version := validTimeContractVersion(t)
	if err := ValidateSupportedGrain(version, TimeGrainMonth); err != nil {
		t.Fatalf("supported grain rejected: %v", err)
	}
	assertTimeIssue(t, ValidateSupportedGrain(version, TimeGrainFiscalYear), TimeUnsupportedGrain, "grain")
}

func validTimeContractVersion(t *testing.T) TimeContractVersion {
	t.Helper()
	version := TimeContractVersion{
		ID: validationRow, TenantID: validationTenant, DomainID: validationDomain,
		TimeContractID: validationObject, VersionNo: 1, Status: VersionStatusDraft,
		Timezone: "Asia/Shanghai", WeekStart: WeekStartMonday, WeekNumbering: WeekNumberingISO,
		FiscalYearStartMonth: 1, FiscalMonthRule: FiscalMonthCalendar,
		IncompletePeriodPolicy:   IncompletePeriodMTD,
		ComparisonAlignment:      ComparisonSameDayCount,
		MonthEndOverflowRule:     MonthEndClampToLastDay,
		SupportedGrains:          []TimeGrain{TimeGrainDay, TimeGrainMonth},
		DataAvailableThroughExpr: "MATERIALIZATION_MAX_PRIMARY_TIME", ExpectedLagHours: 26,
	}
	version.ContentHash = mustTimeContractHash(t, version)
	return version
}

func mustTimeContractHash(t *testing.T, version TimeContractVersion) askdata.ContentHash {
	t.Helper()
	hash, err := ContentHash(version)
	if err != nil {
		t.Fatalf("ContentHash() error = %v", err)
	}
	return hash
}

func assertTimeIssue(t *testing.T, err error, code, path string) {
	t.Helper()
	var validation ValidationErrors
	if !errors.As(err, &validation) {
		t.Fatalf("error = %v, want ValidationErrors", err)
	}
	assertIssue(t, validation, code, path)
}
