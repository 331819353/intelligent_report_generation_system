package registry

import (
	"context"
	"errors"
	"testing"
	"time"

	"intelligent-report-generation-system/internal/askdata"
)

type fakeImportedDraftStore struct {
	drafts []ImportedDraft
	err    error
}

func (store *fakeImportedDraftStore) SaveImportedDraft(_ context.Context, draft ImportedDraft) error {
	if store.err != nil {
		return store.err
	}
	store.drafts = append(store.drafts, draft)
	return nil
}

func TestImporterCreatesDeterministicDraftsFromCurrentPublishedDWSADS(t *testing.T) {
	schemaHash := string(askdata.HashBytes([]byte("schema")))
	records := []PublishedAssetRecord{
		publishedRecord("DWS", "merchant_daily", schemaHash),
		func() PublishedAssetRecord {
			record := publishedRecord("ADS", "other_domain", schemaHash)
			record.DomainID = "84388fa5-477a-4975-a334-859f37b4608d"
			return record
		}(),
	}
	store := &fakeImportedDraftStore{}
	importer := NewImporter(fakeInventoryStore{records: records}, store)
	importer.inventory.summarizer = fakeSummarizer{summary: DocumentSummary{
		Fields: []InventoryField{
			{FieldID: "field_month", Code: "stat_month", Name: "统计月", CanonicalType: "DATE", Role: "TIME", Visible: true},
			{FieldID: "field_region", Code: "region", Name: "区域", CanonicalType: "STRING", Role: "DIMENSION", Visible: true},
			{FieldID: "field_orders", Code: "order_count", Name: "订单数", CanonicalType: "INTEGER", Role: "MEASURE", Aggregation: "SUM", Additivity: "ADDITIVE", NullPolicy: "ZERO", Visible: true},
		},
		OutputGrain: InventoryGrain{Description: "每月每区域一行", KeyFields: []string{"stat_month", "region"}, TimeField: "stat_month", DefaultTimeGrain: "MONTH"},
		TimeFields:  []string{"stat_month"},
	}}
	importer.inventory.now = func() time.Time { return time.Date(2026, 8, 5, 0, 0, 0, 0, time.UTC) }
	result, err := importer.Import(context.Background(), testTenantID, testDomainID, validationOwner)
	if err != nil {
		t.Fatalf("Import() error = %v", err)
	}
	if len(result.Drafts) != 1 || len(store.drafts) != 1 {
		t.Fatalf("result/store drafts = %d/%d, want 1/1", len(result.Drafts), len(store.drafts))
	}
	draft := result.Drafts[0]
	if draft.SemanticModel.Status != VersionStatusDraft || len(draft.Measures) != 1 || len(draft.Dimensions) != 2 {
		t.Fatalf("unexpected draft: %#v", draft)
	}
	if draft.SemanticModel.PrimaryTimeFieldID != "field_month" || draft.Dimensions[0].MemberIndexPolicy != MemberIndexExactOnly {
		t.Fatalf("semantic field identity/policy was not preserved: %#v", draft)
	}
	if draft.Measures[0].Additivity != "" || draft.Measures[0].AdditivitySuggestion != FullyAdditive {
		t.Fatalf("importer confirmed a suggestion as fact: %#v", draft.Measures[0])
	}
	second, err := importer.Import(context.Background(), testTenantID, testDomainID, validationOwner)
	if err != nil {
		t.Fatalf("second Import() error = %v", err)
	}
	if second.Drafts[0].SemanticModel.ID != draft.SemanticModel.ID || second.Drafts[0].SemanticModel.ContentHash != draft.SemanticModel.ContentHash {
		t.Fatal("replayed import changed stable ID or content hash")
	}
}

func TestImporterFailsClosedForInvalidMeasureAndStoreFailure(t *testing.T) {
	schemaHash := string(askdata.HashBytes([]byte("schema")))
	store := &fakeImportedDraftStore{}
	importer := NewImporter(fakeInventoryStore{records: []PublishedAssetRecord{publishedRecord("DWS", "asset", schemaHash)}}, store)
	importer.inventory.summarizer = fakeSummarizer{summary: DocumentSummary{Fields: []InventoryField{
		{FieldID: "field_bad", Code: "bad_measure", Name: "错误度量", CanonicalType: "STRING", Role: "MEASURE", Visible: true},
	}, OutputGrain: InventoryGrain{KeyFields: []string{}}, TimeFields: []string{}}}
	if _, err := importer.Import(context.Background(), testTenantID, testDomainID, validationOwner); err == nil {
		t.Fatal("Import() accepted non-numeric measure")
	}

	store.err = errors.New("write failed")
	importer.inventory.summarizer = fakeSummarizer{summary: DocumentSummary{Fields: []InventoryField{}, OutputGrain: InventoryGrain{KeyFields: []string{}}, TimeFields: []string{}}}
	if _, err := importer.Import(context.Background(), testTenantID, testDomainID, validationOwner); err == nil || !errors.Is(err, store.err) {
		t.Fatalf("Import() error = %v, want store failure", err)
	}
}
