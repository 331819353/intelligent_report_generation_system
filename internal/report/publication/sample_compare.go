package publication

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"sort"
	"strconv"

	"intelligent-report-generation-system/internal/askdata"
	askcompiler "intelligent-report-generation-system/internal/askdata/compiler"
	reportmodel "intelligent-report-generation-system/internal/report"
	reportruntime "intelligent-report-generation-system/internal/report/runtime"
	"intelligent-report-generation-system/internal/report/store"
)

type SemanticUpgradeRuntime interface {
	CompileAndExecuteSemanticIR(context.Context, reportruntime.SemanticExecutionRequest) (reportruntime.QueryResult, error)
	ExecuteCompiledSemanticIR(
		context.Context, reportruntime.SemanticExecutionRequest, askcompiler.QueryArtifact,
	) (reportruntime.QueryResult, error)
}

type RuntimeSampleComparator struct {
	Runtime SemanticUpgradeRuntime
	Limit   int
}

func (comparator RuntimeSampleComparator) CompareUpgrade(
	ctx context.Context,
	identity store.Identity,
	comparison UpgradeComparison,
) (SampleImpact, error) {
	if comparator.Runtime == nil || identity.Validate() != nil || comparison.ReportID.Validate() != nil ||
		comparison.BaseVersionID.Validate() != nil {
		return SampleImpact{}, ErrUpgradeInvalid
	}
	beforeRef, beforeOK := semanticReference(comparison.Before)
	afterRef, afterOK := semanticReference(comparison.After)
	if !beforeOK || !afterOK {
		return SampleImpact{Direction: "UNKNOWN"}, nil
	}
	if comparison.Compilation == nil {
		if beforeRef.QueryPlanHash == afterRef.QueryPlanHash &&
			beforeRef.SemanticReleaseID == afterRef.SemanticReleaseID &&
			beforeRef.SemanticContentHash == afterRef.SemanticContentHash {
			evidence := askdata.HashBytes([]byte("report-upgrade-unchanged-plan-v1\x00" + string(beforeRef.QueryPlanHash)))
			return SampleImpact{Direction: "UNCHANGED", RelativeChange: "0.000000", EvidenceHash: string(evidence)}, nil
		}
		return SampleImpact{}, ErrSemanticCompilationInvalid
	}
	if comparison.Compilation.validate(identity) != nil ||
		comparison.Compilation.ComponentID != comparison.After.ID ||
		comparison.Compilation.Artifact.PlanHash != afterRef.QueryPlanHash {
		return SampleImpact{}, ErrSemanticCompilationInvalid
	}
	limit := comparator.Limit
	if limit < 1 || limit > 500 {
		limit = 100
	}
	executionContext := reportruntime.WithViewerIdentity(ctx, identity)
	before, err := comparator.Runtime.CompileAndExecuteSemanticIR(executionContext, reportruntime.SemanticExecutionRequest{
		ReportID: comparison.ReportID, ReportVersionID: comparison.BaseVersionID,
		SourceRunID:           sourceID(beforeRef.SourceQuestionRunID),
		CompilationArtifactID: sourceID(beforeRef.CompilationArtifactID),
		ReleaseID:             beforeRef.SemanticReleaseID, ContentHash: beforeRef.SemanticContentHash,
		IR: beforeRef.SemanticIR, ResolvedTimeSpec: beforeRef.ResolvedTimeSpec,
		FixedPlanHash: beforeRef.QueryPlanHash, Limit: limit,
	})
	if err != nil {
		return SampleImpact{}, fmt.Errorf("compare report upgrade before sample: %w", err)
	}
	after, err := comparator.Runtime.ExecuteCompiledSemanticIR(executionContext, reportruntime.SemanticExecutionRequest{
		ReportID: comparison.ReportID, ReportVersionID: comparison.BaseVersionID,
		ReleaseID: afterRef.SemanticReleaseID, ContentHash: afterRef.SemanticContentHash,
		IR: afterRef.SemanticIR, ResolvedTimeSpec: afterRef.ResolvedTimeSpec,
		FixedPlanHash: afterRef.QueryPlanHash, Limit: limit,
	}, comparison.Compilation.Artifact)
	if err != nil {
		return SampleImpact{}, fmt.Errorf("compare report upgrade after sample: %w", err)
	}
	return compareSampleResults(beforeRef, afterRef, before, after)
}

func semanticReference(component reportmodel.Component) (*reportmodel.SemanticQueryRef, bool) {
	if component.DataBinding == nil || component.DataBinding.BindingMode != reportmodel.BindingSemanticIR ||
		component.DataBinding.SemanticQueryRef == nil {
		return nil, false
	}
	return component.DataBinding.SemanticQueryRef, true
}

func sourceID(value *askdata.ID) askdata.ID {
	if value == nil {
		return ""
	}
	return *value
}

func compareSampleResults(
	beforeRef *reportmodel.SemanticQueryRef,
	afterRef *reportmodel.SemanticQueryRef,
	before reportruntime.QueryResult,
	after reportruntime.QueryResult,
) (SampleImpact, error) {
	aliases := make(map[string]struct{}, len(beforeRef.SemanticIR.Metrics)+len(afterRef.SemanticIR.Metrics))
	for _, metric := range beforeRef.SemanticIR.Metrics {
		aliases[metric.Alias] = struct{}{}
	}
	for _, metric := range afterRef.SemanticIR.Metrics {
		aliases[metric.Alias] = struct{}{}
	}
	beforeTotal, beforeCount, err := sumMetricColumns(before, aliases)
	if err != nil {
		return SampleImpact{}, err
	}
	afterTotal, afterCount, err := sumMetricColumns(after, aliases)
	if err != nil {
		return SampleImpact{}, err
	}
	evidence, _ := json.Marshal(struct {
		BeforeHash  askdata.ContentHash `json:"beforeHash"`
		AfterHash   askdata.ContentHash `json:"afterHash"`
		BeforeTotal string              `json:"beforeTotal"`
		AfterTotal  string              `json:"afterTotal"`
		Aliases     []string            `json:"aliases"`
	}{before.Hash, after.Hash, beforeTotal.RatString(), afterTotal.RatString(), sortedKeys(aliases)})
	impact := SampleImpact{Direction: "UNKNOWN", EvidenceHash: string(askdata.HashBytes(evidence))}
	if beforeCount == 0 || afterCount == 0 {
		return impact, nil
	}
	switch afterTotal.Cmp(beforeTotal) {
	case -1:
		impact.Direction = "DECREASE"
	case 0:
		impact.Direction = "UNCHANGED"
	default:
		impact.Direction = "INCREASE"
	}
	if beforeTotal.Sign() != 0 {
		difference := new(big.Rat).Sub(afterTotal, beforeTotal)
		denominator := new(big.Rat).Abs(new(big.Rat).Set(beforeTotal))
		impact.RelativeChange = new(big.Rat).Quo(difference, denominator).FloatString(6)
	}
	return impact, nil
}

func sumMetricColumns(result reportruntime.QueryResult, aliases map[string]struct{}) (*big.Rat, int, error) {
	indexes := []int{}
	for index, column := range result.Columns {
		if _, selected := aliases[column]; selected {
			indexes = append(indexes, index)
		}
	}
	total := new(big.Rat)
	count := 0
	for _, row := range result.Rows {
		for _, index := range indexes {
			if index >= len(row) || row[index] == nil {
				continue
			}
			value, ok := numericRat(row[index])
			if !ok {
				return nil, 0, errors.New("report upgrade sample contains a non-numeric metric")
			}
			total.Add(total, value)
			count++
		}
	}
	return total, count, nil
}

func numericRat(value any) (*big.Rat, bool) {
	var raw string
	switch typed := value.(type) {
	case json.Number:
		raw = typed.String()
	case string:
		raw = typed
	case float64:
		raw = strconv.FormatFloat(typed, 'g', -1, 64)
	case float32:
		raw = strconv.FormatFloat(float64(typed), 'g', -1, 32)
	case int:
		raw = strconv.FormatInt(int64(typed), 10)
	case int8:
		raw = strconv.FormatInt(int64(typed), 10)
	case int16:
		raw = strconv.FormatInt(int64(typed), 10)
	case int32:
		raw = strconv.FormatInt(int64(typed), 10)
	case int64:
		raw = strconv.FormatInt(typed, 10)
	case uint:
		raw = strconv.FormatUint(uint64(typed), 10)
	case uint8:
		raw = strconv.FormatUint(uint64(typed), 10)
	case uint16:
		raw = strconv.FormatUint(uint64(typed), 10)
	case uint32:
		raw = strconv.FormatUint(uint64(typed), 10)
	case uint64:
		raw = strconv.FormatUint(typed, 10)
	default:
		return nil, false
	}
	result, ok := new(big.Rat).SetString(raw)
	return result, ok
}

func sortedKeys(values map[string]struct{}) []string {
	result := make([]string, 0, len(values))
	for value := range values {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}
