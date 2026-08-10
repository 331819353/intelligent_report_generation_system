package validator

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"intelligent-report-generation-system/internal/askdata/compiler"
	"intelligent-report-generation-system/internal/materialization"
)

const (
	CodeTimeCoverageNone      = "TIME_COVERAGE_NONE"
	CodeTimeCoverageTruncated = "TIME_COVERAGE_TRUNCATED"
)

var (
	ErrInvalidCoverage     = errors.New("time coverage verdict is invalid")
	ErrCoverageUnavailable = errors.New("time coverage metadata is unavailable")
)

type CoverageRelation string

const (
	CoverageFull      CoverageRelation = "FULL"
	CoverageTruncated CoverageRelation = "TRUNCATED"
	CoverageNone      CoverageRelation = "NONE"
)

type CoverageResultStatus string

const (
	CoverageResultFull    CoverageResultStatus = "FULL"
	CoverageResultPartial CoverageResultStatus = "PARTIAL"
	CoverageResultNoData  CoverageResultStatus = "NO_DATA"
)

// CoverageDecisionEvidence is safe to persist with the plan decision. It
// contains only resolved business-time boundaries and never warehouse SQL or
// physical relation names.
type CoverageDecisionEvidence struct {
	RequestedStart        string `json:"requestedStart"`
	RequestedEndExclusive string `json:"requestedEndExclusive"`
	ActualStart           string `json:"actualStart,omitempty"`
	ActualEndExclusive    string `json:"actualEndExclusive,omitempty"`
	DataAvailableThrough  string `json:"dataAvailableThrough"`
	AvailableEndExclusive string `json:"availableEndExclusive"`
	SuggestedStart        string `json:"suggestedStart,omitempty"`
	SuggestedEndExclusive string `json:"suggestedEndExclusive,omitempty"`
	TimeRangeTruncated    bool   `json:"timeRangeTruncated"`
	UserPrompt            string `json:"userPrompt,omitempty"`
}

// CoverageVerdict is the pre-compilation time-coverage gate. The unexported
// seal prevents a caller from constructing a value that can authorize a timed
// plan without going through EvaluateCoverage.
type CoverageVerdict struct {
	Relation           CoverageRelation          `json:"relation"`
	Code               string                    `json:"code,omitempty"`
	ResultStatus       CoverageResultStatus      `json:"resultStatus"`
	QueryAllowed       bool                      `json:"queryAllowed"`
	ResolvedTimeSpec   compiler.ResolvedTimeSpec `json:"resolvedTimeSpec"`
	MaterializationIDs []string                  `json:"materializationIds"`
	Evidence           CoverageDecisionEvidence  `json:"evidence"`
	sealed             bool
}

func (verdict CoverageVerdict) Validate() error {
	if !verdict.sealed || compiler.ValidateResolvedTimeSpec(verdict.ResolvedTimeSpec) != nil ||
		len(verdict.MaterializationIDs) == 0 || verdict.Evidence.RequestedStart == "" ||
		verdict.Evidence.RequestedEndExclusive == "" || verdict.Evidence.DataAvailableThrough == "" ||
		verdict.Evidence.AvailableEndExclusive == "" {
		return ErrInvalidCoverage
	}
	for index, id := range verdict.MaterializationIDs {
		if strings.TrimSpace(id) == "" || (index > 0 && verdict.MaterializationIDs[index-1] >= id) {
			return ErrInvalidCoverage
		}
	}
	switch verdict.Relation {
	case CoverageFull:
		if verdict.Code != "" || verdict.ResultStatus != CoverageResultFull || !verdict.QueryAllowed ||
			verdict.Evidence.TimeRangeTruncated || verdict.Evidence.ActualStart == "" ||
			verdict.Evidence.ActualEndExclusive == "" {
			return ErrInvalidCoverage
		}
	case CoverageTruncated:
		if verdict.Code != CodeTimeCoverageTruncated || verdict.ResultStatus != CoverageResultPartial ||
			!verdict.QueryAllowed || !verdict.ResolvedTimeSpec.TruncatedByDataAvailability ||
			!verdict.Evidence.TimeRangeTruncated || verdict.Evidence.ActualStart == "" ||
			verdict.Evidence.ActualEndExclusive == "" || verdict.Evidence.UserPrompt == "" {
			return ErrInvalidCoverage
		}
	case CoverageNone:
		if verdict.Code != CodeTimeCoverageNone || verdict.ResultStatus != CoverageResultNoData ||
			verdict.QueryAllowed || verdict.Evidence.TimeRangeTruncated ||
			verdict.Evidence.SuggestedStart == "" || verdict.Evidence.SuggestedEndExclusive == "" ||
			verdict.Evidence.UserPrompt == "" {
			return ErrInvalidCoverage
		}
	default:
		return ErrInvalidCoverage
	}
	return nil
}

// EvaluateCoverage evaluates one control-plane materialization snapshot.
// Invalid or incomplete metadata yields an invalid zero verdict, which all
// validation entry points reject closed.
func EvaluateCoverage(
	spec compiler.ResolvedTimeSpec,
	meta materialization.MaterializationMeta,
) CoverageVerdict {
	return EvaluateCoverageForMaterializations(spec, []materialization.MaterializationMeta{meta})
}

// EvaluateCoverageForMaterializations applies the earliest watermark across
// every source, making a multi-model plan no fresher than its stalest input.
func EvaluateCoverageForMaterializations(
	spec compiler.ResolvedTimeSpec,
	metas []materialization.MaterializationMeta,
) CoverageVerdict {
	if compiler.ValidateResolvedTimeSpec(spec) != nil || len(metas) == 0 {
		return CoverageVerdict{}
	}
	ids := make([]string, 0, len(metas))
	seen := make(map[string]bool, len(metas))
	var watermark time.Time
	for _, meta := range metas {
		id := strings.TrimSpace(meta.MaterializationID)
		if id == "" || seen[id] || meta.DataAvailableThrough == nil || meta.DataAvailableThrough.IsZero() {
			return CoverageVerdict{}
		}
		seen[id] = true
		ids = append(ids, id)
		if watermark.IsZero() || meta.DataAvailableThrough.Before(watermark) {
			watermark = *meta.DataAvailableThrough
		}
	}
	sort.Strings(ids)
	loc, err := time.LoadLocation(spec.Timezone)
	if err != nil {
		return CoverageVerdict{}
	}
	localWatermark := watermark.In(loc)
	availableEnd := time.Date(
		localWatermark.Year(), localWatermark.Month(), localWatermark.Day(),
		0, 0, 0, 0, loc,
	).AddDate(0, 0, 1)
	format := func(value time.Time) string { return value.In(loc).Format(time.RFC3339) }
	evidence := CoverageDecisionEvidence{
		RequestedStart: format(spec.ResolvedStart), RequestedEndExclusive: format(spec.ResolvedEndExclusive),
		DataAvailableThrough: format(localWatermark), AvailableEndExclusive: format(availableEnd),
	}
	verdict := CoverageVerdict{
		ResolvedTimeSpec: spec, MaterializationIDs: ids, Evidence: evidence, sealed: true,
	}

	if !spec.ResolvedStart.Before(availableEnd) {
		verdict.Relation, verdict.Code = CoverageNone, CodeTimeCoverageNone
		verdict.ResultStatus = CoverageResultNoData
		verdict.ResolvedTimeSpec.DataAvailableThrough = localWatermark
		suggestedStart := availableEnd.AddDate(0, 0, -1)
		verdict.Evidence.SuggestedStart = format(suggestedStart)
		verdict.Evidence.SuggestedEndExclusive = format(availableEnd)
		verdict.Evidence.UserPrompt = fmt.Sprintf(
			"数据截至 %s；所选区间暂无可用数据，建议改查 [%s, %s)。",
			localWatermark.Format("2006-01-02"), suggestedStart.Format("2006-01-02"), availableEnd.Format("2006-01-02"),
		)
		if verdict.Validate() != nil {
			return CoverageVerdict{}
		}
		return verdict
	}

	adjusted, err := compiler.ApplyDataAvailability(spec, localWatermark)
	if err != nil {
		return CoverageVerdict{}
	}
	verdict.ResolvedTimeSpec = adjusted
	verdict.QueryAllowed = true
	verdict.Evidence.ActualStart = format(adjusted.ResolvedStart)
	verdict.Evidence.ActualEndExclusive = format(adjusted.ResolvedEndExclusive)
	if spec.ResolvedEndExclusive.After(availableEnd) || spec.TruncatedByDataAvailability {
		verdict.Relation, verdict.Code = CoverageTruncated, CodeTimeCoverageTruncated
		verdict.ResultStatus = CoverageResultPartial
		verdict.Evidence.TimeRangeTruncated = true
		verdict.Evidence.UserPrompt = fmt.Sprintf(
			"数据截至 %s，已按实际可用区间 [%s, %s) 查询。",
			localWatermark.Format("2006-01-02"), adjusted.ResolvedStart.Format("2006-01-02"), adjusted.ResolvedEndExclusive.Format("2006-01-02"),
		)
	} else {
		verdict.Relation = CoverageFull
		verdict.ResultStatus = CoverageResultFull
	}
	if verdict.Validate() != nil {
		return CoverageVerdict{}
	}
	return verdict
}

// CoverageControl loads freshness only through SNAP-001's control-plane
// reader. Its interface cannot issue a warehouse query.
type CoverageControl struct {
	reader materialization.SnapshotControlReader
}

func NewCoverageControl(reader materialization.SnapshotControlReader) (*CoverageControl, error) {
	if reader == nil {
		return nil, ErrCoverageUnavailable
	}
	return &CoverageControl{reader: reader}, nil
}

func (control *CoverageControl) Evaluate(
	ctx context.Context,
	tenantID string,
	materializationIDs []string,
	spec compiler.ResolvedTimeSpec,
) (CoverageVerdict, error) {
	if control == nil || control.reader == nil || strings.TrimSpace(tenantID) == "" ||
		len(materializationIDs) == 0 || compiler.ValidateResolvedTimeSpec(spec) != nil {
		return CoverageVerdict{}, ErrCoverageUnavailable
	}
	ids := append([]string(nil), materializationIDs...)
	sort.Strings(ids)
	metas := make([]materialization.MaterializationMeta, 0, len(ids))
	for index, id := range ids {
		if strings.TrimSpace(id) == "" || (index > 0 && ids[index-1] == id) {
			return CoverageVerdict{}, ErrCoverageUnavailable
		}
		meta, err := control.reader.GetLatestSnapshot(ctx, tenantID, id)
		if err != nil {
			return CoverageVerdict{}, fmt.Errorf("%w: %v", ErrCoverageUnavailable, err)
		}
		if meta.MaterializationID != id {
			return CoverageVerdict{}, ErrCoverageUnavailable
		}
		metas = append(metas, meta)
	}
	verdict := EvaluateCoverageForMaterializations(spec, metas)
	if verdict.Validate() != nil {
		return CoverageVerdict{}, ErrCoverageUnavailable
	}
	return verdict, nil
}
