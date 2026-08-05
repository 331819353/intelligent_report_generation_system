package registry

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/dataset"
)

const (
	testTenantID = "0b3ee268-009a-47ca-8797-615eab7d70d5"
	testDomainID = "1b20e618-acaf-434c-b002-bf6356ef64e8"
)

type fakeInventoryStore struct {
	records []PublishedAssetRecord
	err     error
}

func (store fakeInventoryStore) ListPublishedAssets(context.Context, string) ([]PublishedAssetRecord, error) {
	return append([]PublishedAssetRecord(nil), store.records...), store.err
}

type fakeSummarizer struct {
	summary DocumentSummary
	err     error
}

func (summarizer fakeSummarizer) Summarize([]byte, string) (DocumentSummary, error) {
	return summarizer.summary, summarizer.err
}

func TestInventoryDefaultsToRedactedDeterministicOutput(t *testing.T) {
	schemaHash := string(askdata.HashBytes([]byte("schema")))
	records := []PublishedAssetRecord{
		publishedRecord("ADS", "z_asset", schemaHash),
		publishedRecord("DWS", "a_asset", schemaHash),
	}
	service := &InventoryService{
		store: fakeInventoryStore{records: records},
		summarizer: fakeSummarizer{summary: DocumentSummary{
			Fields:      []InventoryField{{FieldID: "field_month", Code: "stat_month", Name: "统计月", CanonicalType: "DATE", Role: "TIME", Visible: true, Ordinal: 1}},
			OutputGrain: InventoryGrain{Description: "每月一行", KeyFields: []string{"stat_month"}, TimeField: "stat_month", DefaultTimeGrain: "MONTH"},
			TimeFields:  []string{"stat_month"},
		}},
		now: func() time.Time { return time.Date(2026, 8, 5, 12, 0, 0, 0, time.FixedZone("CST", 8*60*60)) },
	}
	inventory, err := service.List(context.Background(), testTenantID, InventoryOptions{})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if !inventory.Redacted || inventory.Assets[0].Physical != nil || inventory.Assets[1].Physical != nil {
		t.Fatalf("inventory leaked physical identifiers: %#v", inventory)
	}
	if inventory.Assets[0].Layer != "ADS" || inventory.Assets[1].Layer != "DWS" {
		t.Fatalf("assets are not stable-sorted: %#v", inventory.Assets)
	}
	if !strings.HasPrefix(inventory.TenantReference, "tenant-sha256:") || strings.Contains(inventory.TenantReference, testTenantID) {
		t.Fatalf("tenant reference is not redacted: %q", inventory.TenantReference)
	}
	if inventory.GeneratedAt.Location() != time.UTC {
		t.Fatalf("GeneratedAt location = %s, want UTC", inventory.GeneratedAt.Location())
	}
	if inventory.Assets[0].PhysicalReferenceHash != PhysicalReferenceDigest("warehouse_published", "asset_view") {
		t.Fatalf("unexpected physical reference hash %q", inventory.Assets[0].PhysicalReferenceHash)
	}
}

func TestInventoryPhysicalIdentifiersRequireExplicitOption(t *testing.T) {
	schemaHash := string(askdata.HashBytes([]byte("schema")))
	service := &InventoryService{
		store:      fakeInventoryStore{records: []PublishedAssetRecord{publishedRecord("DWS", "asset", schemaHash)}},
		summarizer: fakeSummarizer{summary: DocumentSummary{Fields: []InventoryField{}, OutputGrain: InventoryGrain{KeyFields: []string{}}, TimeFields: []string{}}},
		now:        time.Now,
	}
	inventory, err := service.List(context.Background(), testTenantID, InventoryOptions{IncludePhysicalIdentifiers: true})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if inventory.Redacted || inventory.Assets[0].Physical == nil || inventory.Assets[0].Physical.PublishedName != "asset_view" {
		t.Fatalf("explicit physical output is missing: %#v", inventory)
	}
}

func TestInventoryRejectsUnsupportedLayerAndCrossTenantRecords(t *testing.T) {
	schemaHash := string(askdata.HashBytes([]byte("schema")))
	tests := []struct {
		name   string
		record PublishedAssetRecord
		want   string
	}{
		{name: "layer", record: publishedRecord("DWD", "asset", schemaHash), want: ErrUnsupportedInventoryLayer.Error()},
		{name: "tenant", record: func() PublishedAssetRecord {
			record := publishedRecord("DWS", "asset", schemaHash)
			record.TenantID = "5c797b6d-5990-4b1f-9157-c85783d1b940"
			return record
		}(), want: "tenant boundary"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := &InventoryService{
				store:      fakeInventoryStore{records: []PublishedAssetRecord{test.record}},
				summarizer: fakeSummarizer{summary: DocumentSummary{}}, now: time.Now,
			}
			_, err := service.List(context.Background(), testTenantID, InventoryOptions{})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("List() error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestInventoryPropagatesStoreAndSummarizerFailures(t *testing.T) {
	service := &InventoryService{store: fakeInventoryStore{err: errors.New("read failed")}, summarizer: fakeSummarizer{}, now: time.Now}
	if _, err := service.List(context.Background(), testTenantID, InventoryOptions{}); err == nil || !strings.Contains(err.Error(), "read failed") {
		t.Fatalf("List() error = %v, want store failure", err)
	}

	schemaHash := string(askdata.HashBytes([]byte("schema")))
	service = &InventoryService{
		store:      fakeInventoryStore{records: []PublishedAssetRecord{publishedRecord("DWS", "asset", schemaHash)}},
		summarizer: fakeSummarizer{err: errors.New("invalid DSL")}, now: time.Now,
	}
	if _, err := service.List(context.Background(), testTenantID, InventoryOptions{}); err == nil || !strings.Contains(err.Error(), "invalid DSL") {
		t.Fatalf("List() error = %v, want summarizer failure", err)
	}
}

func TestDatasetDocumentSummarizerExtractsFieldsGrainAndTime(t *testing.T) {
	document, err := dataset.BuildMappedDatasetDocument(
		dataset.MappedDatasetTable{
			ID: "8f3d9cad-6752-4847-a922-b66b036617ea", DataSourceID: "8f4e8b80-025c-41e1-ac7b-1c6d64750467",
			TableName: "AGG_MERCHANT_DAILY_OPS", BusinessName: "商家每日运营指标汇总",
			BusinessDescription: "按日期和商家汇总订单、配送及金额指标",
		},
		[]dataset.MappedDatasetColumn{
			{ColumnName: "METRIC_DATE", BusinessName: "指标日期", CanonicalType: "DATE", SemanticType: "DATE", PrimaryKey: true},
			{ColumnName: "MERCHANT_ID", BusinessName: "商家", CanonicalType: "STRING", SemanticType: "IDENTIFIER", PrimaryKey: true},
			{ColumnName: "ORDER_COUNT", BusinessName: "订单数", CanonicalType: "INTEGER", SemanticType: "QUANTITY"},
		},
	)
	if err != nil {
		t.Fatalf("BuildMappedDatasetDocument() error = %v", err)
	}
	raw, err := json.Marshal(document)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	prepared, err := dataset.Prepare(raw)
	if err != nil {
		t.Fatalf("dataset.Prepare() error = %v", err)
	}
	summary, err := (DatasetDocumentSummarizer{}).Summarize(prepared.DSLJSON, prepared.DSLHash)
	if err != nil {
		t.Fatalf("Summarize() error = %v", err)
	}
	if len(summary.Fields) != 3 || summary.OutputGrain.Description == "" || len(summary.OutputGrain.KeyFields) != 2 {
		t.Fatalf("summary = %#v", summary)
	}
	if len(summary.TimeFields) != 1 || summary.TimeFields[0] != "METRIC_DATE" {
		t.Fatalf("timeFields = %v, want METRIC_DATE", summary.TimeFields)
	}
	if _, err := (DatasetDocumentSummarizer{}).Summarize(prepared.DSLJSON, string(askdata.HashBytes([]byte("wrong")))); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("Summarize() error = %v, want schema hash mismatch", err)
	}
}

func publishedRecord(layer, code, schemaHash string) PublishedAssetRecord {
	return PublishedAssetRecord{
		TenantID: testTenantID, DomainID: testDomainID,
		DatasetID: "d761091b-b0fa-4383-97ce-aad2c2b6b811", DatasetCode: code, DatasetName: "合成资产",
		DatasetVersionID: "2c8dc06d-a2af-43e0-8d15-54d5f7771943", VersionNo: 1,
		Layer: layer, SchemaHash: schemaHash, DSLJSON: []byte(`{"synthetic":true}`),
		MaterializationID: "7cf8bc2c-227a-4f7c-8036-f2990c8c2d3a",
		PublishedSchema:   "warehouse_published", PublishedName: "asset_view",
		MaterializationHash: schemaHash, SnapshotHash: string(askdata.HashBytes([]byte("snapshot"))),
		RowCount: 10, ActivatedAt: time.Date(2026, 8, 5, 0, 0, 0, 0, time.UTC),
	}
}
