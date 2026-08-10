package registryimport

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"
)

const validationDatasetID = "11111111-1111-4111-8111-111111111111"

func TestFourLayerValidatorCoversEveryGovernedIssueCode(t *testing.T) {
	tests := []struct {
		name      string
		assetType AssetType
		mutate    func([]map[string]string)
		rows      int
		code      string
	}{
		{"required", AssetMetric, func(rows []map[string]string) { rows[0]["name"] = "" }, 1, ImportRequiredMissing},
		{"type", AssetModel, func(rows []map[string]string) { rows[0]["datasetVersionId"] = "bad" }, 1, ImportTypeInvalid},
		{"enum", AssetMetric, func(rows []map[string]string) { rows[0]["additivity"] = "MAYBE" }, 1, ImportEnumInvalid},
		{"ref missing", AssetMetric, func(rows []map[string]string) { rows[0]["modelCode"] = "missing" }, 1, ImportRefNotFound},
		{"ref inactive", AssetMetric, func(rows []map[string]string) { rows[0]["modelCode"] = "draft_model" }, 1, ImportRefNotActive},
		{"owner", AssetMetric, func(rows []map[string]string) { rows[0]["ownerEmail"] = "missing@example.com" }, 1, ImportOwnerUnknown},
		{"formula invalid", AssetMetric, func(rows []map[string]string) { rows[0]["formula"] = `{"sql":"select 1"}` }, 1, ImportFormulaInvalid},
		{"formula cycle", AssetMetric, makeFormulaCycle, 2, ImportFormulaCycle},
		{"compatibility", AssetMetricDimension, func(rows []map[string]string) { rows[0]["metricCode"] = "cross_metric" }, 1, ImportCompatAsymmetric},
		{"hierarchy", AssetHierarchy, makeBrokenHierarchy, 2, ImportHierarchyBroken},
		{"fanout", AssetRelationship, func(rows []map[string]string) {
			rows[0]["cardinality"] = "MANY_TO_MANY"
			rows[0]["fanoutPolicy"] = "SAFE"
		}, 1, ImportFanoutCombinationInvalid},
		{"additivity", AssetMetric, func(rows []map[string]string) { rows[0]["additivity"] = "SEMI_ADDITIVE" }, 1, ImportAdditivityInconsistent},
		{"name", AssetMetric, func(rows []map[string]string) { rows[0]["name"] = "Revenue Metric" }, 1, ImportNameConflict},
		{"term priority", AssetTerm, makeTermPriorityConflict, 2, ImportTermPriorityConflict},
		{"negative context", AssetTerm, func(rows []map[string]string) { rows[0]["negativeContexts"] = rows[0]["term"] }, 1, ImportNegativeContextContradiction},
		{"sensitivity", AssetDimension, func(rows []map[string]string) {
			rows[0]["sensitivity"] = "RESTRICTED"
			rows[0]["memberIndexPolicy"] = "FULL"
		}, 1, ImportSensitivityPolicyInvalid},
		{"impact warning", AssetMetric, func(rows []map[string]string) {
			rows[0]["code"] = "existing_metric"
			rows[0]["name"] = "Existing Metric"
		}, 1, ImportImpactRequiresReview},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			values := make([]map[string]string, test.rows)
			for index := range values {
				values[index] = validImportValues(t, test.assetType)
			}
			test.mutate(values)
			results := validateFixture(t, test.assetType, values)
			for _, result := range results {
				if _, err := normalizeValidatedRow(result); err != nil {
					t.Fatalf("row %d cannot be persisted: %v (%#v)", result.RowNo, err, result.Errors)
				}
			}
			if !resultsContainCode(results, test.code) {
				t.Fatalf("issues = %#v, want %s", collectIssues(results), test.code)
			}
			if test.code == ImportImpactRequiresReview && results[0].State != RowValid {
				t.Fatalf("impact warning state = %s, want VALID", results[0].State)
			}
		})
	}
}

func TestFourLayerValidatorShortCircuitsAfterL1(t *testing.T) {
	values := validImportValues(t, AssetMetric)
	values["modelCode"] = "missing"
	values["additivity"] = "invalid"
	result := validateFixture(t, AssetMetric, []map[string]string{values})[0]
	if !issueCodes(result.Errors)[ImportEnumInvalid] {
		t.Fatalf("issues = %#v", result.Errors)
	}
	for _, forbidden := range []string{ImportRefNotFound, ImportAdditivityInconsistent, ImportNameConflict} {
		if issueCodes(result.Errors)[forbidden] {
			t.Fatalf("L1 failure leaked %s: %#v", forbidden, result.Errors)
		}
	}
}

func TestFourLayerValidatorResolvesSameBatchMetricReferences(t *testing.T) {
	rows := []map[string]string{
		validImportValues(t, AssetMetric), validImportValues(t, AssetMetric),
	}
	rows[0]["code"], rows[0]["name"] = "metric_a", "Metric A"
	rows[1]["code"], rows[1]["name"] = "metric_b", "Metric B"
	rows[0]["formula"] = `{"type":"METRIC_REF","metricCode":"metric_b"}`
	rows[1]["formula"] = `{"type":"MEASURE_REF","measureCode":"revenue"}`
	results := validateFixture(t, AssetMetric, rows)
	for _, result := range results {
		if result.State != RowValid || len(result.Errors) != 0 {
			t.Fatalf("row %d = %s %#v", result.RowNo, result.State, result.Errors)
		}
	}
}

func TestFourLayerValidatorAcceptsValidRowsForAllTwelveAssetTypes(t *testing.T) {
	for _, assetType := range []AssetType{
		AssetModel, AssetMeasure, AssetMetric, AssetMetricDimension, AssetDimension, AssetMember,
		AssetHierarchy, AssetRelationship, AssetTerm, AssetCertifiedExample, AssetKPIBundle, AssetEvalCase,
	} {
		t.Run(string(assetType), func(t *testing.T) {
			result := validateFixture(t, assetType, []map[string]string{validImportValues(t, assetType)})[0]
			if result.State != RowValid || len(result.Errors) != 0 {
				t.Fatalf("result = %s %#v", result.State, result.Errors)
			}
		})
	}
}

func TestFourLayerValidatorUsesProfileHighCardinalityEvidence(t *testing.T) {
	values := validImportValues(t, AssetDimension)
	values["memberIndexPolicy"] = "EXACT_ONLY"
	snapshot := validValidationSnapshot()
	snapshot.HighCardinalityFields[validationDatasetID] = map[string]bool{"region_id": true}
	result := validateFixtureWithSnapshot(t, AssetDimension, []map[string]string{values}, snapshot)[0]
	if !issueCodes(result.Errors)[ImportSensitivityPolicyInvalid] {
		t.Fatalf("issues = %#v", result.Errors)
	}
}

func TestFourLayerValidatorResolvesQualifiedMemberTermTargets(t *testing.T) {
	values := validImportValues(t, AssetTerm)
	setValues(values, "term", "华东", "termType", "MEMBER", "targetCode", "region::east")
	requested := []string{}
	catalog := fixtureValidationCatalog{
		snapshot: validValidationSnapshot(),
		memberTargets: map[string]ValidationReference{
			"region::east": {
				Kind: "MEMBER", Code: "region::east", Name: "East", ID: uuid.NewString(),
				Active: true, Certified: true,
			},
		},
		requestedMemberTargets: &requested,
	}
	result := validateFixtureWithCatalog(t, AssetTerm, []map[string]string{values}, catalog)[0]
	if result.State != RowValid || len(result.Errors) != 0 {
		t.Fatalf("qualified member term = %s %#v", result.State, result.Errors)
	}
	if len(requested) != 1 || requested[0] != "region::east" {
		t.Fatalf("member resolver requested %#v", requested)
	}

	catalog.memberTargets = nil
	catalog.snapshot = validValidationSnapshot()
	result = validateFixtureWithCatalog(t, AssetTerm, []map[string]string{values}, catalog)[0]
	if result.State != RowInvalid || !issueCodes(result.Errors)[ImportRefNotFound] {
		t.Fatalf("missing member target = %s %#v", result.State, result.Errors)
	}
}

func TestParseMemberTargetCodeRequiresDimensionQualifiedSafeValue(t *testing.T) {
	tests := []struct {
		input, dimension, member string
		valid                    bool
	}{
		{"region::east", "region", "east", true},
		{" region :: 华东 ", "region", "华东", true},
		{"east", "", "", false},
		{"bad code::east", "", "", false},
		{"region::", "", "", false},
		{"region::east\nwest", "", "", false},
	}
	for _, test := range tests {
		dimension, member, err := parseMemberTargetCode(test.input)
		if test.valid && (err != nil || dimension != test.dimension || member != test.member) {
			t.Fatalf("parse %q = %q/%q/%v", test.input, dimension, member, err)
		}
		if !test.valid && err == nil {
			t.Fatalf("parse %q unexpectedly succeeded", test.input)
		}
	}
}

func makeFormulaCycle(rows []map[string]string) {
	rows[0]["code"], rows[0]["name"] = "metric_a", "Metric A"
	rows[1]["code"], rows[1]["name"] = "metric_b", "Metric B"
	rows[0]["formula"] = `{"type":"METRIC_REF","metricCode":"metric_b"}`
	rows[1]["formula"] = `{"type":"METRIC_REF","metricCode":"metric_a"}`
}

func makeBrokenHierarchy(rows []map[string]string) {
	rows[0]["code"], rows[0]["name"], rows[0]["levelOrder"] = "geo_new", "Geo New", "1"
	rows[0]["dimensionCode"], rows[0]["parentDimensionCode"] = "region", ""
	rows[1]["code"], rows[1]["name"], rows[1]["levelOrder"] = "geo_new", "Geo New", "3"
	rows[1]["dimensionCode"], rows[1]["parentDimensionCode"] = "time", "time"
}

func makeTermPriorityConflict(rows []map[string]string) {
	rows[0]["term"], rows[1]["term"] = "收入", "收入"
	rows[0]["priority"], rows[1]["priority"] = "100", "100"
	rows[0]["targetCode"], rows[1]["targetCode"] = "revenue", "existing_metric"
}

func validateFixture(t *testing.T, assetType AssetType, values []map[string]string) []ValidatedRow {
	t.Helper()
	return validateFixtureWithSnapshot(t, assetType, values, validValidationSnapshot())
}

func validateFixtureWithSnapshot(
	t *testing.T,
	assetType AssetType,
	values []map[string]string,
	snapshot ValidationSnapshot,
) []ValidatedRow {
	t.Helper()
	return validateFixtureWithCatalog(t, assetType, values, fixtureValidationCatalog{snapshot: snapshot})
}

func validateFixtureWithCatalog(
	t *testing.T,
	assetType AssetType,
	values []map[string]string,
	catalog fixtureValidationCatalog,
) []ValidatedRow {
	t.Helper()
	claim := validWorkerClaim()
	claim.AssetType = assetType
	claim.Attempt = 1
	rows := make([]RawImportRow, len(values))
	for index, value := range values {
		payload, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		rows[index] = RawImportRow{RowNo: index + 1, Raw: payload}
	}
	validator := NewFourLayerValidator(catalog)
	prepared, err := validator.Prepare(context.Background(), claim, rows)
	if err != nil {
		t.Fatal(err)
	}
	results := make([]ValidatedRow, len(rows))
	for index, row := range rows {
		results[index], err = prepared.ValidateRow(context.Background(), claim, row.RowNo, row.Raw)
		if err != nil {
			t.Fatal(err)
		}
	}
	return results
}

func validImportValues(t *testing.T, assetType AssetType) map[string]string {
	t.Helper()
	definition, err := TemplateDefinitionFor(assetType)
	if err != nil {
		t.Fatal(err)
	}
	values := map[string]string{}
	for _, column := range definition.Columns {
		values[column.Name] = ""
	}
	switch assetType {
	case AssetModel:
		setValues(values, "code", "new_model", "name", "New Model", "datasetVersionId", validationDatasetID,
			"entityCode", "order", "grainDescription", "one row per order", "grainKeyFields", "order_id", "ownerEmail", "owner@example.com")
	case AssetMeasure:
		setValues(values, "modelCode", "sales", "code", "new_measure", "name", "New Measure", "logicalFieldId", "amount",
			"defaultAggregation", "SUM", "additivity", "FULLY_ADDITIVE", "nullPolicy", "PRESERVE")
	case AssetMetric:
		setValues(values, "code", "new_metric", "name", "New Metric", "modelCode", "sales",
			"formula", `{"type":"MEASURE_REF","measureCode":"revenue"}`, "additivity", "FULLY_ADDITIVE",
			"timeGrain", "NONE", "displayPrecision", "2", "zeroDenominatorPolicy", "NULL", "ownerEmail", "owner@example.com")
	case AssetMetricDimension:
		setValues(values, "metricCode", "revenue", "dimensionCode", "region", "compatible", "TRUE", "role", "FILTER")
	case AssetDimension:
		setValues(values, "modelCode", "sales", "code", "new_dimension", "name", "New Dimension", "kind", "CATEGORICAL",
			"logicalFieldId", "region_id", "sensitivity", "INTERNAL", "memberIndexPolicy", "EXACT_ONLY",
			"groupable", "TRUE", "filterable", "TRUE", "sortable", "TRUE", "ownerEmail", "owner@example.com")
	case AssetMember:
		setValues(values, "dimensionCode", "region", "canonicalValue", "east", "displayLabel", "East", "validFrom", "2026-01-01", "sensitivity", "INTERNAL")
	case AssetHierarchy:
		setValues(values, "code", "new_hierarchy", "name", "New Hierarchy", "levelOrder", "1", "dimensionCode", "region")
	case AssetRelationship:
		setValues(values, "leftModelCode", "sales", "rightModelCode", "other", "joinAst", `{"type":"EQ","leftField":"id","rightField":"id"}`,
			"joinType", "INNER", "cardinality", "ONE_TO_ONE", "fanoutPolicy", "SAFE", "validFrom", "2026-01-01")
	case AssetTerm:
		setValues(values, "term", "收入", "termType", "METRIC", "targetCode", "revenue", "matchMode", "EXACT", "priority", "100", "validFrom", "2026-01-01", "source", "IMPORT")
	case AssetCertifiedExample:
		setValues(values, "question", "收入是多少", "expectedMetricCodes", "revenue")
	case AssetKPIBundle:
		setValues(values, "code", "new_bundle", "name", "New Bundle", "metricCodes", "revenue",
			"defaultTimeExpression", "CURRENT_MONTH")
	case AssetEvalCase:
		setValues(values, "question", "收入是多少", "actorRole", "analyst", "expectedOutcome", "DIRECT", "setType", "VALIDATION", "shardId", "1")
	}
	return values
}

func setValues(values map[string]string, pairs ...string) {
	for index := 0; index < len(pairs); index += 2 {
		values[pairs[index]] = pairs[index+1]
	}
}

func validValidationSnapshot() ValidationSnapshot {
	snapshot := normalizeValidationSnapshot(ValidationSnapshot{})
	for _, reference := range []ValidationReference{
		{Kind: "ENTITY", Code: "order", Name: "Order", ID: uuid.NewString(), Active: true, Certified: true},
		{Kind: "MODEL", Code: "sales", Name: "Sales", ID: uuid.NewString(), Active: true, Certified: true, DatasetVersionID: validationDatasetID, TimeGrains: []string{"DAY", "MONTH"}},
		{Kind: "MODEL", Code: "other", Name: "Other", ID: uuid.NewString(), Active: true, Certified: true, DatasetVersionID: validationDatasetID},
		{Kind: "MODEL", Code: "draft_model", Name: "Draft Model", ID: uuid.NewString(), Active: false},
		{Kind: "MEASURE", Code: "revenue", Name: "Revenue Measure", ID: uuid.NewString(), Active: true, Certified: true, ModelCode: "sales"},
		{Kind: "METRIC", Code: "revenue", Name: "Revenue Metric", ID: uuid.NewString(), Active: true, Certified: true, ModelCode: "sales"},
		{Kind: "METRIC", Code: "existing_metric", Name: "Existing Metric", ID: uuid.NewString(), Active: true, Certified: true, ModelCode: "sales"},
		{Kind: "METRIC", Code: "cross_metric", Name: "Cross Metric", ID: uuid.NewString(), Active: true, Certified: true, ModelCode: "other"},
		{Kind: "DIMENSION", Code: "region", Name: "Region", ID: uuid.NewString(), Active: true, Certified: true, ModelCode: "sales"},
		{Kind: "DIMENSION", Code: "time", Name: "Time", ID: uuid.NewString(), Active: true, Certified: true, ModelCode: "sales"},
		{Kind: "HIERARCHY", Code: "geo", Name: "Geo", ID: uuid.NewString(), Active: true, Certified: true},
		{Kind: "TIME_CONTRACT", Code: "calendar", Name: "Calendar", ID: uuid.NewString(), Active: true, Certified: true, TimeGrains: []string{"DAY", "MONTH"}},
	} {
		putReference(&snapshot, reference)
	}
	snapshot.Owners["owner@example.com"] = uuid.NewString()
	snapshot.Roles["analyst"] = uuid.NewString()
	snapshot.Datasets[validationDatasetID] = true
	snapshot.Fields[validationDatasetID] = map[string]bool{"amount": true, "region_id": true}
	return snapshot
}

type fixtureValidationCatalog struct {
	snapshot               ValidationSnapshot
	memberTargets          map[string]ValidationReference
	requestedMemberTargets *[]string
}

func (catalog fixtureValidationCatalog) LoadValidationSnapshot(context.Context, string, string) (ValidationSnapshot, error) {
	return catalog.snapshot, nil
}

func (catalog fixtureValidationCatalog) ResolveImportMemberTargets(
	_ context.Context,
	_, _ string,
	targets []string,
) (map[string]ValidationReference, error) {
	if catalog.requestedMemberTargets != nil {
		*catalog.requestedMemberTargets = append(*catalog.requestedMemberTargets, targets...)
	}
	result := make(map[string]ValidationReference, len(catalog.memberTargets))
	for code, reference := range catalog.memberTargets {
		result[code] = reference
	}
	return result, nil
}

func resultsContainCode(results []ValidatedRow, code string) bool {
	for _, result := range results {
		if issueCodes(result.Errors)[code] {
			return true
		}
	}
	return false
}

func issueCodes(issues []ValidationIssue) map[string]bool {
	result := map[string]bool{}
	for _, issue := range issues {
		result[issue.Code] = true
	}
	return result
}

func collectIssues(results []ValidatedRow) string {
	parts := []string{}
	for _, result := range results {
		for _, issue := range result.Errors {
			parts = append(parts, fmt.Sprintf("row=%d %s/%s", result.RowNo, issue.Column, issue.Code))
		}
	}
	return strings.Join(parts, ", ")
}
