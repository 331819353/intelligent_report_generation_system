package inbound

import (
	"testing"
	"time"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/compiler"
	"intelligent-report-generation-system/internal/askdata/ir"
	"intelligent-report-generation-system/internal/report"
)

func TestSemanticBindingTimeRequirement(t *testing.T) {
	base := report.SemanticQueryRef{
		DatasetVersionID: idPointer("00000000-0000-4000-8000-000000000001"),
		EvidenceRefs: []askdata.EvidenceRef{{
			EvidenceID: "evidence:query", Kind: askdata.EvidenceKindQueryResult,
			SourceID: "00000000-0000-4000-8000-000000000002", ContentHash: askdata.HashBytes([]byte("result")),
		}},
	}
	if !semanticBindingShapeAllowed(base) {
		t.Fatal("timeless governed aggregation must be eligible for authorization")
	}
	base.SemanticIR.TimeRange = &ir.TimeRange{}
	if semanticBindingShapeAllowed(base) {
		t.Fatal("explicit time range without a resolved contract must fail closed")
	}
	now := time.Now().UTC()
	base.ResolvedTimeSpec = &compiler.ResolvedTimeSpec{ResolvedStart: now, ResolvedEndExclusive: now.Add(time.Hour)}
	if !semanticBindingShapeAllowed(base) {
		t.Fatal("explicit time range with a resolved contract must proceed to full authorization")
	}
}

func idPointer(value askdata.ID) *askdata.ID { return &value }
