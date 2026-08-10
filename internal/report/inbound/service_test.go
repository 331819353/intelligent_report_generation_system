package inbound

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/reportasset"
	"intelligent-report-generation-system/internal/report"
	"intelligent-report-generation-system/internal/report/operation"
	"intelligent-report-generation-system/internal/report/store"
)

type inboundAuthorizerStub struct {
	reportErr, semanticErr error
	reportCalls, dataCalls int
}

func (stub *inboundAuthorizerStub) AuthorizeReportEdit(context.Context, store.Identity, askdata.ID) error {
	stub.reportCalls++
	return stub.reportErr
}

func (stub *inboundAuthorizerStub) AuthorizeSemanticBinding(context.Context, store.Identity, report.SemanticQueryRef) error {
	stub.dataCalls++
	return stub.semanticErr
}

type inboundStoreStub struct {
	result store.InboundResult
	err    error
	calls  int
}

func (stub *inboundStoreStub) ApplyInbound(context.Context, store.Identity, store.InboundInput) (store.InboundResult, error) {
	stub.calls++
	return stub.result, stub.err
}

func TestServiceReauthorizesReportAndDataBeforeAtomicApply(t *testing.T) {
	claim := validInboundClaim(t)
	tests := []struct {
		name        string
		reportErr   error
		semanticErr error
		storeErr    error
		wantCode    string
		wantStore   int
	}{
		{name: "report denied", reportErr: ErrUnauthorized, wantCode: "REPORT_EDIT_FORBIDDEN"},
		{name: "data denied", semanticErr: ErrUnauthorized, wantCode: "REPORT_DATA_ACCESS_FORBIDDEN"},
		{name: "revision conflict", storeErr: store.ErrRevisionConflict, wantCode: "REPORT_REVISION_CONFLICT", wantStore: 1},
		{name: "success", wantStore: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			authorizer := &inboundAuthorizerStub{reportErr: test.reportErr, semanticErr: test.semanticErr}
			atomic := &inboundStoreStub{result: store.InboundResult{RevisionNo: 2}, err: test.storeErr}
			service, err := NewService(authorizer, atomic)
			if err != nil {
				t.Fatal(err)
			}
			revision, err := service.ApplyIntent(context.Background(), claim)
			if test.wantCode == "" {
				if err != nil || revision != 2 || authorizer.reportCalls != 1 || authorizer.dataCalls != 1 || atomic.calls != 1 {
					t.Fatalf("ApplyIntent() revision=%d err=%v authorizer=%#v store=%#v", revision, err, authorizer, atomic)
				}
				return
			}
			var failure *reportasset.DeliveryFailure
			if !errors.As(err, &failure) || failure.Code != test.wantCode || atomic.calls != test.wantStore {
				t.Fatalf("ApplyIntent() failure=%#v err=%v store calls=%d", failure, err, atomic.calls)
			}
		})
	}
}

func validInboundClaim(t *testing.T) reportasset.IntentDeliveryClaim {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "api", "examples", "report-definition", "ask-data-report.json"))
	if err != nil {
		t.Fatal(err)
	}
	definition, err := report.Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	reportID := askdata.ID("00000000-0000-4000-8000-000000000201")
	bundle := operation.Bundle{
		SchemaVersion: operation.SchemaVersion, ReportID: reportID, BaseRevision: 1, Source: operation.SourceSystem,
		Operations: []operation.Operation{{
			Op: operation.ComponentCreate, TargetID: definition.Components[0].ID,
			Payload: &operation.ComponentCreatePayload{Component: definition.Components[0]},
		}},
	}
	if err := bundle.Validate(); err != nil {
		t.Fatal(err)
	}
	return reportasset.IntentDeliveryClaim{
		IntentID: "00000000-0000-4000-8000-000000000202",
		TenantID: "00000000-0000-4000-8000-000000000203",
		ActorID:  "00000000-0000-4000-8000-000000000204",
		DomainID: "00000000-0000-4000-8000-000000000205",
		ReportID: reportID, IdempotencyKeyHash: askdata.HashBytes([]byte("inbound-key")), Bundle: bundle,
	}
}
