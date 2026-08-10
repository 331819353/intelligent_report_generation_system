package registryimport

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"testing"

	"github.com/google/uuid"
	"github.com/xuri/excelize/v2"
)

func TestValidationReportContainsRepairColumnsAndRoundTripsThroughTemplateParser(t *testing.T) {
	values := validImportValues(t, AssetMetric)
	raw, _ := json.Marshal(values)
	batch := SemanticImport{
		ID: uuid.NewString(), TenantID: uuid.NewString(), DomainID: uuid.NewString(),
		AssetType: AssetMetric, State: StateValidated, TotalRows: 1,
	}
	store := reportFixtureStore{
		batch: batch,
		rows: []ImportRow{{
			RowNo: 1, RawJSON: raw, ValidationState: RowInvalid,
			Errors: []ValidationIssue{{
				Column: "formula", Code: ImportFormulaCycle,
				Message: "指标公式依赖形成环", Expected: "有向无环的公式", Actual: "A -> B -> A",
			}},
		}},
	}
	artifact, err := NewReportService(store).Generate(
		context.Background(), batch.TenantID, batch.DomainID, batch.ID,
	)
	if err != nil {
		t.Fatal(err)
	}
	workbook, err := excelize.OpenReader(bytes.NewReader(artifact.Bytes))
	if err != nil {
		t.Fatal(err)
	}
	defer workbook.Close()
	rows, err := workbook.GetRows("Import")
	if err != nil {
		t.Fatal(err)
	}
	definition, _ := TemplateDefinitionFor(AssetMetric)
	wantHeaders := make([]string, 0, len(definition.Columns)+4)
	for _, column := range definition.Columns {
		wantHeaders = append(wantHeaders, column.Name)
	}
	wantHeaders = append(wantHeaders, reportColumns...)
	if len(rows) != 3 || !slices.Equal(rows[0], wantHeaders) {
		t.Fatalf("report rows/header = %d %#v", len(rows), rows[0])
	}
	if rows[2][len(definition.Columns)] != ImportFormulaCycle ||
		rows[2][len(definition.Columns)+2] != "有向无环的公式" {
		t.Fatalf("repair columns = %#v", rows[2][len(definition.Columns):])
	}

	digest := sha256.Sum256(artifact.Bytes)
	claim := validWorkerClaim()
	claim.AssetType = AssetMetric
	claim.FileName = "validation-report.xlsx"
	claim.FileHash = hex.EncodeToString(digest[:])
	source := NewFileRowSource(memoryObjectStorage{body: artifact.Bytes})
	captured := []json.RawMessage{}
	if err := source.ForEachRow(context.Background(), claim, 0, func(rowNo int, row json.RawMessage) error {
		if rowNo != 1 {
			t.Fatalf("rowNo = %d", rowNo)
		}
		captured = append(captured, row)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(captured) != 1 || !json.Valid(captured[0]) {
		t.Fatalf("round-trip rows = %#v", captured)
	}
	var parsed map[string]string
	if err := json.Unmarshal(captured[0], &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed["code"] != values["code"] || parsed["errorCode"] != ImportFormulaCycle {
		t.Fatalf("parsed report row = %#v", parsed)
	}
}

func TestValidationReportFailsClosedBeforeCompletionOrOnRowCountDrift(t *testing.T) {
	batch := SemanticImport{
		ID: uuid.NewString(), TenantID: uuid.NewString(), DomainID: uuid.NewString(),
		AssetType: AssetMetric, State: StateValidating,
	}
	service := NewReportService(reportFixtureStore{batch: batch})
	if _, err := service.Generate(context.Background(), batch.TenantID, batch.DomainID, batch.ID); !errors.Is(err, ErrImportReportNotReady) {
		t.Fatalf("not-ready error = %v", err)
	}
	batch.State, batch.TotalRows = StateValidated, 2
	service = NewReportService(reportFixtureStore{batch: batch, rows: []ImportRow{{RowNo: 1}}})
	if _, err := service.Generate(context.Background(), batch.TenantID, batch.DomainID, batch.ID); !errors.Is(err, ErrImportConflict) {
		t.Fatalf("count drift error = %v", err)
	}
}

type reportFixtureStore struct {
	batch SemanticImport
	rows  []ImportRow
	err   error
}

func (store reportFixtureStore) Get(context.Context, string, string, string) (SemanticImport, error) {
	return store.batch, store.err
}

func (store reportFixtureStore) ListRows(context.Context, string, string, string) ([]ImportRow, error) {
	return append([]ImportRow(nil), store.rows...), store.err
}
