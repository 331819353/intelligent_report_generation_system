package registryimport

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/xuri/excelize/v2"
)

func TestSemanticExportUsesExactTemplateColumnsForAllTwelveAssets(t *testing.T) {
	sheets := make([]ExportSheet, 0, len(governedAssetOrder))
	for _, assetType := range governedAssetOrder {
		sheets = append(sheets, ExportSheet{
			AssetType: assetType, Rows: []map[string]string{validImportValues(t, assetType)},
		})
	}
	selection := exportTestSelection(governedAssetOrder...)
	artifact, err := NewExportService(exportFixtureCatalog{
		count: len(sheets), dataset: ExportDataset{Sheets: sheets},
	}).Generate(context.Background(), selection)
	if err != nil {
		t.Fatal(err)
	}
	workbook, err := excelize.OpenReader(bytes.NewReader(artifact.Bytes))
	if err != nil {
		t.Fatal(err)
	}
	defer workbook.Close()
	for _, assetType := range governedAssetOrder {
		definition, _ := TemplateDefinitionFor(assetType)
		row, err := workbook.GetRows(string(assetType))
		if err != nil || len(row) < 3 {
			t.Fatalf("%s sheet rows = %d/%v", assetType, len(row), err)
		}
		if len(row[0]) != len(definition.Columns) {
			t.Fatalf("%s headers = %#v", assetType, row[0])
		}
		for index, column := range definition.Columns {
			if row[0][index] != column.Name {
				t.Fatalf("%s header %d = %q, want %q", assetType, index, row[0][index], column.Name)
			}
		}
	}
	info, err := workbook.GetRows("ExportInfo")
	if err != nil || len(info) < 4 || info[0][1] != "semantic-export-v1" {
		t.Fatalf("export info = %#v/%v", info, err)
	}
}

func TestSemanticExportRoundTripsThroughImportParserWithStableContentHash(t *testing.T) {
	values := validImportValues(t, AssetMetric)
	values["positiveExamples"] = "revenue this month|monthly revenue"
	selection := exportTestSelection(AssetMetric)
	first, err := NewExportService(exportFixtureCatalog{
		count: 1, dataset: ExportDataset{Sheets: []ExportSheet{{
			AssetType: AssetMetric, Rows: []map[string]string{values},
		}}},
	}).Generate(context.Background(), selection)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(first.Bytes)
	claim := validWorkerClaim()
	claim.AssetType = AssetMetric
	claim.FileName = first.Filename
	claim.FileHash = hex.EncodeToString(digest[:])
	parsed := []map[string]string{}
	if err := NewFileRowSource(memoryObjectStorage{body: first.Bytes}).ForEachRow(
		context.Background(), claim, 0,
		func(_ int, raw json.RawMessage) error {
			var row map[string]string
			if err := json.Unmarshal(raw, &row); err != nil {
				return err
			}
			parsed = append(parsed, row)
			return nil
		},
	); err != nil {
		t.Fatal(err)
	}
	second, err := NewExportService(exportFixtureCatalog{
		count: len(parsed), dataset: ExportDataset{Sheets: []ExportSheet{{
			AssetType: AssetMetric, Rows: parsed,
		}}},
	}).Generate(context.Background(), selection)
	if err != nil {
		t.Fatal(err)
	}
	if first.ContentHash != second.ContentHash {
		t.Fatalf("round-trip content hash = %s / %s", first.ContentHash, second.ContentHash)
	}
}

func TestSemanticExportCanonicalizesAssetOrderAndReportsSensitiveOmissions(t *testing.T) {
	selection := exportTestSelection(AssetTerm, AssetMember, AssetMetric)
	catalog := exportFixtureCatalog{
		count: 2,
		dataset: ExportDataset{
			Sheets: []ExportSheet{
				{AssetType: AssetMember, Rows: nil},
				{AssetType: AssetMetric, Rows: []map[string]string{validImportValues(t, AssetMetric)}},
				{AssetType: AssetTerm, Rows: []map[string]string{validImportValues(t, AssetTerm)}},
			},
			OmittedSensitiveMembers: 3,
		},
	}
	artifact, err := NewExportService(catalog).Generate(context.Background(), selection)
	if err != nil {
		t.Fatal(err)
	}
	if artifact.OmittedSensitiveMembers != 3 || artifact.RowCount != 2 {
		t.Fatalf("export metadata = %#v", artifact)
	}
	workbook, err := excelize.OpenReader(bytes.NewReader(artifact.Bytes))
	if err != nil {
		t.Fatal(err)
	}
	defer workbook.Close()
	want := []string{"METRIC", "MEMBER", "TERM", "ExportInfo"}
	if got := workbook.GetSheetList(); len(got) != len(want) {
		t.Fatalf("sheet order = %#v", got)
	} else {
		for index := range want {
			if got[index] != want[index] {
				t.Fatalf("sheet order = %#v", got)
			}
		}
	}
}

type exportFixtureCatalog struct {
	count   int
	dataset ExportDataset
	err     error
}

func (catalog exportFixtureCatalog) CountExportRows(context.Context, ExportSelection) (int, error) {
	return catalog.count, catalog.err
}

func (catalog exportFixtureCatalog) LoadExportDataset(context.Context, ExportSelection) (ExportDataset, error) {
	return catalog.dataset, catalog.err
}

func exportTestSelection(assetTypes ...AssetType) ExportSelection {
	return ExportSelection{
		TenantID: uuid.NewString(), DomainID: uuid.NewString(), ActorID: uuid.NewString(),
		AssetTypes: assetTypes,
	}
}
