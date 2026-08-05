package registry

import (
	"encoding/json"
	"errors"
	"testing"

	"intelligent-report-generation-system/internal/askdata"
)

const (
	validationTenant = "0b3ee268-009a-47ca-8797-615eab7d70d5"
	validationDomain = "1b20e618-acaf-434c-b002-bf6356ef64e8"
	validationOwner  = "7868a9d8-8cd4-40b1-90f0-558bed962a1a"
	validationRow    = "2c8dc06d-a2af-43e0-8d15-54d5f7771943"
	validationObject = "d761091b-b0fa-4383-97ce-aad2c2b6b811"
)

func TestMeasureValidationRejectsUnsafeAdditivityWithStablePathAndCode(t *testing.T) {
	measure := validMeasure()
	measure.Aggregation = AggregationAverage
	measure.Additivity = Additive
	err := measure.Validate()
	var validation ValidationErrors
	if !errors.As(err, &validation) {
		t.Fatalf("Validate() error = %v, want ValidationErrors", err)
	}
	assertIssue(t, validation, validationCodeInvalidAdditivity, "additivity")
}

func TestMetricVersionValidationRejectsDuplicateAndMissingDependencies(t *testing.T) {
	metric := MetricVersion{
		VersionIdentity: validVersionIdentity(), MetricID: validationObject,
		SemanticModelVersionID: validationRow,
		FormulaAST:             json.RawMessage(`{"type":"MEASURE_REF","measureId":"m"}`),
		DefaultFiltersAST:      json.RawMessage(`{"type":"TRUE"}`),
		TimeGrain:              "MONTH", Additivity: Additive, NullPolicy: "ZERO",
		MeasureVersionIDs: []string{validationObject, validationObject},
	}
	err := metric.Validate()
	var validation ValidationErrors
	if !errors.As(err, &validation) {
		t.Fatalf("Validate() error = %v, want ValidationErrors", err)
	}
	assertIssue(t, validation, validationCodeDuplicate, "measureVersionIds[1]")
}

func TestDimensionValidationEnforcesSensitivityAndCardinalityPolicy(t *testing.T) {
	dimension := Dimension{
		VersionIdentity: validVersionIdentity(), SemanticModelVersionID: validationRow,
		LogicalFieldID: "field_customer", Code: "customer", Name: "客户",
		Kind: DimensionCategorical, Sensitivity: SensitivityRestricted,
		MemberIndexPolicy: MemberIndexFull, HighCardinality: true,
	}
	err := dimension.Validate()
	var validation ValidationErrors
	if !errors.As(err, &validation) {
		t.Fatalf("Validate() error = %v, want ValidationErrors", err)
	}
	count := 0
	for _, issue := range validation.Issues {
		if issue.Code == validationCodeInvalidMemberPolicy && issue.Path == "memberIndexPolicy" {
			count++
		}
	}
	if count != 2 {
		t.Fatalf("member policy issue count = %d, want 2: %#v", count, validation.Issues)
	}
}

func TestRelationshipValidationRejectsManyToManySafeFanoutAndRawSQL(t *testing.T) {
	relationship := Relationship{
		VersionIdentity:     validVersionIdentity(),
		LeftModelVersionID:  validationRow,
		RightModelVersionID: "1ecf1685-edab-46f4-a7a7-0eec17f2ab31",
		Type:                RelationshipModelJoin, JoinType: JoinLeft,
		Cardinality: CardinalityManyToMany, FanoutPolicy: FanoutSafe,
		JoinAST: json.RawMessage(`{"type":"EQUALS","sql":"a.id=b.id"}`),
	}
	err := relationship.Validate()
	var validation ValidationErrors
	if !errors.As(err, &validation) {
		t.Fatalf("Validate() error = %v, want ValidationErrors", err)
	}
	assertIssue(t, validation, validationCodeUnsafeFanout, "fanoutPolicy")
	assertIssue(t, validation, validationCodeInvalidAST, "joinAst")
}

func TestValidSemanticRegistryObjectsPass(t *testing.T) {
	measure := validMeasure()
	if err := measure.Validate(); err != nil {
		t.Fatalf("Measure.Validate() error = %v", err)
	}
	dimension := Dimension{
		VersionIdentity: validVersionIdentity(), SemanticModelVersionID: validationRow,
		LogicalFieldID: "field_month", Code: "stat_month", Name: "统计月",
		Kind: DimensionTime, Sensitivity: SensitivityInternal,
		MemberIndexPolicy: MemberIndexExactOnly,
	}
	if err := dimension.Validate(); err != nil {
		t.Fatalf("Dimension.Validate() error = %v", err)
	}
}

func validVersionIdentity() VersionIdentity {
	return VersionIdentity{
		ID: validationRow, TenantID: validationTenant, DomainID: validationDomain,
		ObjectID: validationObject, VersionNo: 1, Status: VersionStatusDraft,
		ContentHash: askdata.HashBytes([]byte("semantic object")), OwnerID: validationOwner,
	}
}

func validMeasure() Measure {
	return Measure{
		VersionIdentity: validVersionIdentity(), SemanticModelVersionID: validationRow,
		Code: "order_count", Name: "订单数",
		FormulaAST:  json.RawMessage(`{"type":"FIELD_REF","fieldId":"field_order_count"}`),
		Aggregation: AggregationSum, Additivity: Additive, DataType: NumericInteger,
	}
}

func assertIssue(t *testing.T, validation ValidationErrors, code, path string) {
	t.Helper()
	for _, issue := range validation.Issues {
		if issue.Code == code && issue.Path == path {
			return
		}
	}
	t.Fatalf("missing issue %s at %s: %#v", code, path, validation.Issues)
}
