package metadataai

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
)

type enrichmentCommitSinkStub struct {
	called   int
	tenantID string
	actorID  string
	tableID  string
	err      error
}

var _ EnrichmentCommitSink = (*enrichmentCommitSinkStub)(nil)

func (s *enrichmentCommitSinkStub) EnsureMappedDatasetTx(_ context.Context, _ pgx.Tx, tenantID, actorID, tableID string) error {
	s.called++
	s.tenantID = tenantID
	s.actorID = actorID
	s.tableID = tableID
	return s.err
}

func TestEnsureMappedDatasetTxForwardsCommitContextAndFailure(t *testing.T) {
	wantErr := errors.New("mapped dataset failed")
	sink := &enrichmentCommitSinkStub{err: wantErr}
	store := NewPostgresStore(nil)
	store.SetEnrichmentCommitSink(sink)

	err := store.ensureMappedDatasetTx(context.Background(), nil, "tenant-1", "actor-1", "table-1")
	if !errors.Is(err, wantErr) {
		t.Fatalf("error=%v, want %v", err, wantErr)
	}
	if sink.called != 1 || sink.tenantID != "tenant-1" || sink.actorID != "actor-1" || sink.tableID != "table-1" {
		t.Fatalf("sink call=%#v", sink)
	}
}

func TestEnsureMappedDatasetTxAllowsMissingSink(t *testing.T) {
	store := NewPostgresStore(nil)
	if err := store.ensureMappedDatasetTx(context.Background(), nil, "tenant-1", "actor-1", "table-1"); err != nil {
		t.Fatalf("error=%v", err)
	}
}

func TestMetadataSourceFormatDistinguishesCSVFromWorkbookAndDatabase(t *testing.T) {
	tests := []struct {
		name       string
		sourceType string
		filename   string
		want       string
	}{
		{name: "CSV 小写扩展名", sourceType: "EXCEL", filename: "经营分析.csv", want: SourceFormatCSV},
		{name: "CSV 大写扩展名", sourceType: "excel", filename: "SALES.CSV", want: SourceFormatCSV},
		{name: "Excel 工作簿", sourceType: "EXCEL", filename: "销售明细.xlsx", want: SourceFormatExcel},
		{name: "MySQL", sourceType: "MYSQL", filename: "", want: SourceFormatDatabase},
		{name: "Oracle", sourceType: "ORACLE", filename: "", want: SourceFormatDatabase},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := metadataSourceFormat(test.sourceType, test.filename); got != test.want {
				t.Fatalf("metadataSourceFormat(%q, %q)=%q, want %q", test.sourceType, test.filename, got, test.want)
			}
		})
	}
}
