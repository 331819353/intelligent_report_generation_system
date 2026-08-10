package registryimport

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/xuri/excelize/v2"
)

func TestTemplateDefinitionsMatchAllTwelveLockedColumnContracts(t *testing.T) {
	expected := map[AssetType][]string{
		AssetModel: {
			"code", "name", "datasetVersionId", "entityCode", "grainDescription",
			"grainKeyFields", "primaryTimeDimensionCode", "timeContractCode", "ownerEmail",
		},
		AssetMeasure: {
			"modelCode", "code", "name", "logicalFieldId", "defaultAggregation",
			"additivity", "unit", "currency", "nullPolicy",
		},
		AssetMetric: {
			"code", "name", "description", "modelCode", "formula", "defaultFilter",
			"unit", "currency", "additivity", "semiAdditiveTimeAggregation",
			"aggregationRestriction", "nonAdditiveDimensionCodes", "timeGrain", "dedupKey",
			"displayPrecision", "zeroDenominatorPolicy", "incompletePeriodPolicyOverride",
			"positiveExamples", "negativeExamples", "ownerEmail",
		},
		AssetMetricDimension: {"metricCode", "dimensionCode", "compatible", "role"},
		AssetDimension: {
			"modelCode", "code", "name", "description", "kind", "logicalFieldId",
			"sensitivity", "memberIndexPolicy", "groupable", "filterable", "sortable",
			"hierarchyCode", "ownerEmail",
		},
		AssetMember: {
			"dimensionCode", "canonicalValue", "displayLabel", "aliases", "hierarchyPath",
			"validFrom", "validTo", "sensitivity",
		},
		AssetHierarchy: {"code", "name", "levelOrder", "dimensionCode", "parentDimensionCode"},
		AssetRelationship: {
			"leftModelCode", "rightModelCode", "joinAst", "joinType", "cardinality",
			"fanoutPolicy", "bridgeModelCode", "validFrom", "validTo",
		},
		AssetTerm: {
			"term", "termType", "targetCode", "matchMode", "priority", "negativeContexts",
			"validFrom", "validTo", "source",
		},
		AssetCertifiedExample: {
			"question", "expectedMetricCodes", "expectedDimensionCodes", "expectedMemberValues",
			"expectedTimeExpression", "applicableRoles", "notes",
		},
		AssetKPIBundle: {
			"code", "name", "metricCodes", "defaultDimensionCodes", "defaultTimeExpression",
			"defaultChartTypes", "roleMapping", "applicableQuestionTypes",
		},
		AssetEvalCase: {
			"question", "actorRole", "expectedOutcome", "expectedMetricCodes",
			"expectedDimensionCodes", "expectedMemberValues", "expectedTimeExpression",
			"expectedResultHint", "setType", "shardId",
		},
	}
	if len(expected) != 12 {
		t.Fatal("test contract does not cover 12 asset types")
	}
	for assetType, want := range expected {
		definition, err := TemplateDefinitionFor(assetType)
		if err != nil {
			t.Fatalf("TemplateDefinitionFor(%s): %v", assetType, err)
		}
		got := make([]string, len(definition.Columns))
		for index, column := range definition.Columns {
			got[index] = column.Name
			if column.Description == "" {
				t.Errorf("%s column %s has no explanation", assetType, column.Name)
			}
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s columns = %v, want %v", assetType, got, want)
		}
	}
}

func TestAllTwelveAssetTemplatesGenerateInCSVAndXLSX(t *testing.T) {
	service := NewTemplateService(staticTemplateCatalog{})
	for _, assetType := range []AssetType{
		AssetModel, AssetMeasure, AssetMetric, AssetMetricDimension, AssetDimension,
		AssetMember, AssetHierarchy, AssetRelationship, AssetTerm,
		AssetCertifiedExample, AssetKPIBundle, AssetEvalCase,
	} {
		for _, format := range []TemplateFormat{TemplateFormatCSV, TemplateFormatXLSX} {
			artifact, err := service.Generate(
				context.Background(), uuid.NewString(), uuid.NewString(), assetType, format,
			)
			if err != nil || len(artifact.Bytes) == 0 {
				t.Errorf("Generate(%s,%s) = %d bytes, %v", assetType, format, len(artifact.Bytes), err)
			}
		}
	}
}

func TestXLSXTemplateContainsReferencesInstructionsAndControlledEnums(t *testing.T) {
	tenantID, domainID := uuid.NewString(), uuid.NewString()
	modelID, dimensionID := uuid.NewString(), uuid.NewString()
	service := NewTemplateService(staticTemplateCatalog{references: []TemplateReference{
		{AssetType: "DIMENSION", Code: "region", Name: "区域", ID: dimensionID},
		{AssetType: "MODEL", Code: "sales", Name: "销售模型", ID: modelID},
	}})
	artifact, err := service.Generate(
		context.Background(), tenantID, domainID, AssetMetric, TemplateFormatXLSX,
	)
	if err != nil {
		t.Fatal(err)
	}
	if artifact.Filename != "askdata-metric-template.xlsx" ||
		artifact.ContentType != "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet" {
		t.Fatalf("artifact metadata = %#v", artifact)
	}
	workbook, err := excelize.OpenReader(bytes.NewReader(artifact.Bytes))
	if err != nil {
		t.Fatal(err)
	}
	defer workbook.Close()
	if got := workbook.GetSheetList(); !reflect.DeepEqual(got, []string{"Import", "References", "Instructions"}) {
		t.Fatalf("sheets = %v", got)
	}
	rows, err := workbook.GetRows("Import")
	if err != nil || len(rows) < 2 {
		t.Fatalf("Import rows = %v, %v", rows, err)
	}
	definition, _ := TemplateDefinitionFor(AssetMetric)
	wantHeader := columnNames(definition)
	if !reflect.DeepEqual(rows[0], wantHeader) {
		t.Fatalf("header = %v, want %v", rows[0], wantHeader)
	}
	if !strings.HasPrefix(rows[1][0], TemplateInstructionPrefix) {
		t.Fatalf("instruction marker = %q", rows[1][0])
	}
	validations, err := workbook.GetDataValidations("Import")
	if err != nil {
		t.Fatal(err)
	}
	byRange := map[string]string{}
	for _, validation := range validations {
		byRange[validation.Sqref] = validation.Formula1
	}
	assertValidationValues(t, byRange["I3:I10002"], templateEnumValues["additivity"])
	assertValidationValues(t, byRange["J3:J10002"], templateEnumValues["semiAdditiveTimeAggregation"])
	assertValidationValues(t, byRange["P3:P10002"], templateEnumValues["zeroDenominatorPolicy"])
	referenceRows, err := workbook.GetRows("References")
	if err != nil || len(referenceRows) != 3 {
		t.Fatalf("reference rows = %v, %v", referenceRows, err)
	}
	wantReferences := [][]string{
		{"assetType", "code", "name", "id"},
		{"DIMENSION", "region", "区域", dimensionID},
		{"MODEL", "sales", "销售模型", modelID},
	}
	if !reflect.DeepEqual(referenceRows, wantReferences) {
		t.Fatalf("references = %v, want %v", referenceRows, wantReferences)
	}
}

func TestEmptyDomainGeneratesCSVTemplate(t *testing.T) {
	service := NewTemplateService(staticTemplateCatalog{})
	artifact, err := service.Generate(
		context.Background(), uuid.NewString(), uuid.NewString(), AssetMember, TemplateFormatCSV,
	)
	if err != nil {
		t.Fatal(err)
	}
	if artifact.Filename != "askdata-member-template.csv" || len(artifact.Bytes) == 0 {
		t.Fatalf("artifact = %#v", artifact)
	}
	lines := strings.Split(strings.TrimSpace(string(artifact.Bytes)), "\n")
	if len(lines) != 2 || !strings.Contains(lines[1], TemplateInstructionPrefix) {
		t.Fatalf("CSV template = %q", artifact.Bytes)
	}
}

func TestGeneratedXLSXRoundTripsThroughImportRowSource(t *testing.T) {
	service := NewTemplateService(staticTemplateCatalog{})
	artifact, err := service.Generate(
		context.Background(), uuid.NewString(), uuid.NewString(), AssetDimension, TemplateFormatXLSX,
	)
	if err != nil {
		t.Fatal(err)
	}
	workbook, err := excelize.OpenReader(bytes.NewReader(artifact.Bytes))
	if err != nil {
		t.Fatal(err)
	}
	definition, _ := TemplateDefinitionFor(AssetDimension)
	values := make([]any, len(definition.Columns))
	for index, column := range definition.Columns {
		values[index] = "value_" + column.Name
	}
	if err := workbook.SetSheetRow("Import", "A3", &values); err != nil {
		t.Fatal(err)
	}
	buffer, err := workbook.WriteToBuffer()
	_ = workbook.Close()
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(buffer.Bytes())
	claim := validWorkerClaim()
	claim.FileName = artifact.Filename
	claim.FileHash = hex.EncodeToString(digest[:])
	source := NewFileRowSource(memoryObjectStorage{body: buffer.Bytes()})
	rows := []json.RawMessage{}
	if err := source.ForEachRow(
		context.Background(), claim, 0,
		func(rowNo int, raw json.RawMessage) error {
			if rowNo != 1 {
				return fmt.Errorf("row number = %d", rowNo)
			}
			rows = append(rows, raw)
			return nil
		},
	); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("parsed rows = %d", len(rows))
	}
	var parsed map[string]string
	if err := json.Unmarshal(rows[0], &parsed); err != nil {
		t.Fatal(err)
	}
	if len(parsed) != len(definition.Columns) || parsed["code"] != "value_code" {
		t.Fatalf("parsed row = %#v", parsed)
	}
}

func columnNames(definition TemplateDefinition) []string {
	result := make([]string, len(definition.Columns))
	for index, column := range definition.Columns {
		result[index] = column.Name
	}
	return result
}

func assertValidationValues(t *testing.T, formula string, values []string) {
	t.Helper()
	formula = strings.Trim(formula, `"`)
	if !reflect.DeepEqual(strings.Split(formula, ","), values) {
		t.Fatalf("validation formula = %q, want %v", formula, values)
	}
}

type staticTemplateCatalog struct {
	references []TemplateReference
	err        error
}

func (catalog staticTemplateCatalog) ListTemplateReferences(
	context.Context,
	string,
	string,
) ([]TemplateReference, error) {
	return append([]TemplateReference(nil), catalog.references...), catalog.err
}
