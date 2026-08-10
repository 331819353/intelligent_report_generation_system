package insight

import (
	"fmt"
	"sort"
	"strconv"
	"time"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/shared"
)

type EvidenceRequest struct {
	SourceType               SourceType
	SemanticReleaseID        *askdata.ID
	SemanticIRHash           *askdata.ContentHash
	DatasetVersionID         askdata.ID
	DataSnapshotVersion      string
	QueryPlanHash            askdata.ContentHash
	FilterHash               askdata.ContentHash
	AsOf                     time.Time
	ResolvedTimeRange        ResolvedTimeRange
	MetricVersionID          askdata.ID
	Unit                     string
	Method                   AnalysisMethod
	EvidenceAlgorithmVersion string
	Input                    MethodInput
}

func BuildEvidence(registry *Registry, request EvidenceRequest, generatedAt time.Time) (EvidenceBundle, error) {
	if registry == nil {
		return EvidenceBundle{}, fmt.Errorf("analysis registry is required")
	}
	method, exists := registry.Get(request.Method)
	if !exists {
		return EvidenceBundle{}, fmt.Errorf("analysis method %q is not registered", request.Method)
	}
	result, err := method.Analyze(request.Input)
	if err != nil {
		return EvidenceBundle{}, err
	}
	if len(result.Facts) == 0 {
		return EvidenceBundle{}, fmt.Errorf("analysis method %q produced no evidence facts", request.Method)
	}
	allInputRefs := allRefs(request.Input.Values)
	facts := make([]Fact, 0, len(result.Facts))
	for index, computed := range result.Facts {
		cellRefs := normalizeCellRefs(computed.CellRefs)
		if len(cellRefs) == 0 {
			cellRefs = normalizeCellRefs(allInputRefs)
		}
		if len(cellRefs) == 0 {
			return EvidenceBundle{}, fmt.Errorf("computed fact %d has no source cell", index)
		}
		current := primaryNumeric(computed.Values)
		fact := Fact{
			ID:              askdata.ID(fmt.Sprintf("%s/%03d", request.Method, index+1)),
			MetricVersionID: request.MetricVersionID,
			CurrentValue:    decimal(current), Unit: request.Unit, CellRefs: cellRefs,
		}
		if request.Method == AnalysisPeriodComparison {
			previous, previousExists := computed.Values["previous"]
			changeRate, rateExists := computed.Values["changeRate"]
			if !previousExists || !rateExists {
				return EvidenceBundle{}, fmt.Errorf("period comparison fact lacks previous/changeRate")
			}
			previousValue, rateValue := decimal(previous), decimal(changeRate)
			fact.PreviousValue, fact.ChangeRate = &previousValue, &rateValue
		}
		facts = append(facts, fact)
	}
	warnings := make([]QualityWarning, 0, len(result.Warnings))
	for _, warning := range result.Warnings {
		warnings = append(warnings, QualityWarning{Code: warning, Severity: WarningInfo, Message: warning})
	}
	bundle := EvidenceBundle{
		SchemaVersion: EvidenceSchemaVersion, SourceType: request.SourceType,
		SemanticReleaseID: request.SemanticReleaseID, SemanticIRHash: request.SemanticIRHash,
		DatasetVersionID: request.DatasetVersionID, DataSnapshotVersion: request.DataSnapshotVersion,
		QueryPlanHash: request.QueryPlanHash, FilterHash: request.FilterHash,
		AsOf: request.AsOf.Format(time.RFC3339Nano), ResolvedTimeRange: request.ResolvedTimeRange,
		AnalysisMethod: request.Method, AnalysisMethodVersion: method.Version(),
		EvidenceAlgorithmVersion: request.EvidenceAlgorithmVersion,
		Facts:                    facts, QualityWarnings: warnings, GeneratedAt: generatedAt.Format(time.RFC3339Nano),
	}
	return bundle.Normalize(), bundle.Validate()
}

func normalizeCellRefs(values []shared.CellRef) []shared.CellRef {
	seen := map[string]struct{}{}
	result := make([]shared.CellRef, 0, len(values))
	for _, value := range values {
		key := value.RowKey + "\x00" + value.ColumnKey
		if value.Validate() != nil {
			continue
		}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, value)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].RowKey != result[j].RowKey {
			return result[i].RowKey < result[j].RowKey
		}
		return result[i].ColumnKey < result[j].ColumnKey
	})
	return result
}

func primaryNumeric(values map[string]float64) float64 {
	for _, name := range []string{"value", "current", "actual", "slope", "delta", "rate", "share", "spread", "missingRatio", "totalShare"} {
		if value, exists := values[name]; exists {
			return value
		}
	}
	for _, value := range values {
		return value
	}
	return 0
}

func decimal(value float64) string {
	if value == 0 {
		return "0"
	}
	return strconv.FormatFloat(value, 'f', -1, 64)
}
