package validator

import (
	"context"
	"errors"
	"testing"
	"time"

	"intelligent-report-generation-system/internal/askdata/compiler"
	"intelligent-report-generation-system/internal/materialization"
)

type coverageSnapshotReader struct {
	metas map[string]materialization.MaterializationMeta
	calls []string
}

func (reader *coverageSnapshotReader) GetLatestSnapshot(
	_ context.Context,
	_ string,
	materializationID string,
) (materialization.MaterializationMeta, error) {
	reader.calls = append(reader.calls, materializationID)
	meta, ok := reader.metas[materializationID]
	if !ok {
		return materialization.MaterializationMeta{}, materialization.ErrNotFound
	}
	return meta, nil
}

func TestEvaluateCoverageRelationsAndHalfOpenEquality(t *testing.T) {
	loc := coverageLocation(t)
	base := coverageSpec(loc, 1, 11)
	tests := []struct {
		name      string
		startDay  int
		endDay    int
		watermark int
		want      CoverageRelation
		wantEnd   int
	}{
		{name: "full", startDay: 1, endDay: 5, watermark: 5, want: CoverageFull, wantEnd: 5},
		{name: "truncated", startDay: 1, endDay: 11, watermark: 5, want: CoverageTruncated, wantEnd: 6},
		{name: "none", startDay: 6, endDay: 11, watermark: 5, want: CoverageNone, wantEnd: 11},
		{name: "end equals available is full", startDay: 1, endDay: 6, watermark: 5, want: CoverageFull, wantEnd: 6},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			spec := base
			spec.ResolvedStart = time.Date(2026, time.August, test.startDay, 0, 0, 0, 0, loc)
			spec.ResolvedEndExclusive = time.Date(2026, time.August, test.endDay, 0, 0, 0, 0, loc)
			available := time.Date(2026, time.August, test.watermark, 18, 0, 0, 0, loc)
			verdict := EvaluateCoverage(spec, materialization.MaterializationMeta{
				MaterializationID: "mat-a", DataAvailableThrough: &available,
			})
			if err := verdict.Validate(); err != nil {
				t.Fatalf("verdict validation: %v", err)
			}
			if verdict.Relation != test.want || verdict.ResolvedTimeSpec.ResolvedEndExclusive.Day() != test.wantEnd {
				t.Fatalf("verdict = %#v, want relation=%s end day=%d", verdict, test.want, test.wantEnd)
			}
			if test.want == CoverageTruncated && (!verdict.Evidence.TimeRangeTruncated || verdict.Evidence.UserPrompt == "" || verdict.Code != CodeTimeCoverageTruncated) {
				t.Fatalf("truncated evidence is incomplete: %#v", verdict)
			}
			if test.want == CoverageNone && (verdict.QueryAllowed || verdict.Evidence.UserPrompt == "" || verdict.Code != CodeTimeCoverageNone) {
				t.Fatalf("NONE routing is incomplete: %#v", verdict)
			}
		})
	}
}

func TestCoverageControlUsesMinimumControlPlaneWatermark(t *testing.T) {
	loc := coverageLocation(t)
	late := time.Date(2026, time.August, 9, 12, 0, 0, 0, loc)
	early := time.Date(2026, time.August, 5, 23, 0, 0, 0, loc)
	reader := &coverageSnapshotReader{metas: map[string]materialization.MaterializationMeta{
		"mat-a": {MaterializationID: "mat-a", DataAvailableThrough: &late},
		"mat-b": {MaterializationID: "mat-b", DataAvailableThrough: &early},
	}}
	control, err := NewCoverageControl(reader)
	if err != nil {
		t.Fatal(err)
	}
	verdict, err := control.Evaluate(context.Background(), "tenant-a", []string{"mat-b", "mat-a"}, coverageSpec(loc, 1, 11))
	if err != nil {
		t.Fatal(err)
	}
	if verdict.Relation != CoverageTruncated || verdict.ResolvedTimeSpec.ResolvedEndExclusive.Day() != 6 ||
		verdict.ResolvedTimeSpec.DataAvailableThrough.Day() != 5 || len(reader.calls) != 2 ||
		reader.calls[0] != "mat-a" || reader.calls[1] != "mat-b" {
		t.Fatalf("minimum watermark was not applied deterministically: verdict=%#v calls=%v", verdict, reader.calls)
	}
}

func TestCoverageNoneShortCircuitsBeforeExplain(t *testing.T) {
	loc := coverageLocation(t)
	available := time.Date(2026, time.August, 5, 10, 0, 0, 0, loc)
	verdict := EvaluateCoverage(coverageSpec(loc, 6, 11), materialization.MaterializationMeta{
		MaterializationID: "mat-a", DataAvailableThrough: &available,
	})
	explainer := &recordingExplainer{raw: safeExplainJSON()}
	planValidator, err := NewValidator(explainer, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	_, err = planValidator.ValidateCovered(context.Background(), compiler.QueryArtifact{}, verdict)
	var rejection *Rejection
	if !errors.As(err, &rejection) || rejection.Code != CodeTimeCoverageNone {
		t.Fatalf("rejection = %v, want %s", err, CodeTimeCoverageNone)
	}
	if len(explainer.requests) != 0 {
		t.Fatalf("NONE issued %d warehouse EXPLAIN queries", len(explainer.requests))
	}
}

func TestTimedPlanCannotBypassCoverageGate(t *testing.T) {
	explainer := &recordingExplainer{raw: safeExplainJSON()}
	planValidator, err := NewValidator(explainer, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	spec := coverageSpec(coverageLocation(t), 1, 2)
	_, err = planValidator.Validate(context.Background(), compiler.QueryArtifact{ResolvedTimeSpec: &spec})
	if !errors.Is(err, ErrCoverageValidationRequired) {
		t.Fatalf("bypass error = %v, want %v", err, ErrCoverageValidationRequired)
	}
	if len(explainer.requests) != 0 {
		t.Fatalf("bypass issued %d warehouse EXPLAIN queries", len(explainer.requests))
	}
}

func TestCoverageFailsClosedForMissingWatermark(t *testing.T) {
	verdict := EvaluateCoverage(coverageSpec(coverageLocation(t), 1, 2), materialization.MaterializationMeta{
		MaterializationID: "mat-a",
	})
	if !errors.Is(verdict.Validate(), ErrInvalidCoverage) {
		t.Fatalf("missing watermark verdict = %#v", verdict)
	}
}

func coverageSpec(loc *time.Location, startDay, endDay int) compiler.ResolvedTimeSpec {
	return compiler.ResolvedTimeSpec{
		RequestedPeriod: "ABSOLUTE", Grain: "DAY", PolicyApplied: "FULL_PERIOD",
		PolicySource: "PLATFORM_DEFAULT", ResolvedStart: time.Date(2026, time.August, startDay, 0, 0, 0, 0, loc),
		ResolvedEndExclusive: time.Date(2026, time.August, endDay, 0, 0, 0, 0, loc),
		DataAvailableThrough: time.Date(2026, time.December, 31, 0, 0, 0, 0, loc), Timezone: loc.String(),
	}
}

func coverageLocation(t *testing.T) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	return loc
}
