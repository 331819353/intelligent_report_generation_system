package insight

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"

	"intelligent-report-generation-system/internal/askdata/shared"
)

type NumericValue struct {
	Key      string
	Group    string
	Value    float64
	Previous *float64
	Target   *float64
	Missing  bool
	CellRef  shared.CellRef
}

type MethodInput struct {
	Values    []NumericValue
	TopN      int
	Threshold float64
}

type ComputedFact struct {
	Kind     string             `json:"kind"`
	Key      string             `json:"key,omitempty"`
	Values   map[string]float64 `json:"values"`
	Strings  map[string]string  `json:"strings"`
	CellRefs []shared.CellRef   `json:"cellRefs"`
}

type MethodResult struct {
	Facts    []ComputedFact `json:"facts"`
	Warnings []string       `json:"warnings"`
}

type Method interface {
	ID() AnalysisMethod
	Version() string
	InputContract() MethodContract
	Analyze(MethodInput) (MethodResult, error)
}

type MethodContract struct {
	RequiredRoles []string `json:"requiredRoles"`
	MinimumRows   int      `json:"minimumRows"`
}

type Registry struct{ methods map[AnalysisMethod]Method }

func NewRegistry() *Registry {
	methods := []Method{
		methodFunc{id: AnalysisCurrentValue, contract: contract(1, "VALUE"), run: currentValue},
		methodFunc{id: AnalysisPeriodComparison, contract: contract(1, "VALUE", "PREVIOUS"), run: periodComparison},
		methodFunc{id: AnalysisTrend, contract: contract(1, "ORDER", "VALUE"), run: trend},
		methodFunc{id: AnalysisAnomalyPoint, contract: contract(1, "ORDER", "VALUE"), run: anomalyPoint},
		methodFunc{id: AnalysisTopN, contract: contract(1, "DIMENSION", "VALUE"), run: topN},
		methodFunc{id: AnalysisContribution, contract: contract(1, "DIMENSION", "VALUE"), run: contribution},
		methodFunc{id: AnalysisMaxChange, contract: contract(1, "DIMENSION", "VALUE", "PREVIOUS"), run: maxChange},
		methodFunc{id: AnalysisTargetAchievement, contract: contract(1, "DIMENSION", "VALUE", "TARGET"), run: targetAchievement},
		methodFunc{id: AnalysisGroupDifference, contract: contract(2, "GROUP", "VALUE"), run: groupDifference},
		methodFunc{id: AnalysisShareOfTotal, contract: contract(1, "DIMENSION", "VALUE"), run: shareOfTotal},
		methodFunc{id: AnalysisDataCompleteness, contract: contract(1, "DIMENSION", "PRESENCE"), run: dataCompleteness},
	}
	result := &Registry{methods: make(map[AnalysisMethod]Method, len(methods))}
	for _, method := range methods {
		result.methods[method.ID()] = method
	}
	return result
}

func (registry *Registry) Get(id AnalysisMethod) (Method, bool) {
	if registry == nil {
		return nil, false
	}
	method, exists := registry.methods[id]
	return method, exists
}

func (registry *Registry) List() []Method {
	result := make([]Method, 0, len(registry.methods))
	for _, method := range registry.methods {
		result = append(result, method)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID() < result[j].ID() })
	return result
}

type methodFunc struct {
	id       AnalysisMethod
	contract MethodContract
	run      func(MethodInput) (MethodResult, error)
}

func contract(minimumRows int, roles ...string) MethodContract {
	return MethodContract{RequiredRoles: append([]string(nil), roles...), MinimumRows: minimumRows}
}

func (method methodFunc) ID() AnalysisMethod            { return method.id }
func (method methodFunc) Version() string               { return "1.0.0" }
func (method methodFunc) InputContract() MethodContract { return method.contract }
func (method methodFunc) Analyze(input MethodInput) (MethodResult, error) {
	if err := validateMethodInput(input); err != nil {
		return MethodResult{}, err
	}
	return method.run(input)
}

func validateMethodInput(input MethodInput) error {
	if len(input.Values) == 0 {
		return errors.New("analysis input has no values")
	}
	if len(input.Values) > 10000 {
		return errors.New("analysis input exceeds 10000 values")
	}
	for index, value := range input.Values {
		if strings.TrimSpace(value.Key) == "" || math.IsNaN(value.Value) || math.IsInf(value.Value, 0) {
			return fmt.Errorf("values[%d] is invalid", index)
		}
		if value.Previous != nil && (math.IsNaN(*value.Previous) || math.IsInf(*value.Previous, 0)) {
			return fmt.Errorf("values[%d].previous is invalid", index)
		}
		if value.Target != nil && (math.IsNaN(*value.Target) || math.IsInf(*value.Target, 0)) {
			return fmt.Errorf("values[%d].target is invalid", index)
		}
	}
	return nil
}

func currentValue(input MethodInput) (MethodResult, error) {
	value, exists := firstPresent(input.Values)
	if !exists {
		return MethodResult{}, errors.New("CURRENT_VALUE has no present values")
	}
	return MethodResult{Facts: []ComputedFact{{Kind: "CURRENT_VALUE", Key: value.Key, Values: map[string]float64{"value": value.Value}, Strings: map[string]string{}, CellRefs: refs(value)}}}, nil
}

func periodComparison(input MethodInput) (MethodResult, error) {
	value, exists := firstPresent(input.Values)
	if !exists {
		return MethodResult{}, errors.New("PERIOD_COMPARISON has no present values")
	}
	if value.Previous == nil {
		return MethodResult{}, errors.New("PERIOD_COMPARISON requires previous")
	}
	delta := value.Value - *value.Previous
	rate := 0.0
	if *value.Previous != 0 {
		rate = delta / math.Abs(*value.Previous)
	}
	return MethodResult{Facts: []ComputedFact{{Kind: "PERIOD_COMPARISON", Key: value.Key, Values: map[string]float64{"current": value.Value, "previous": *value.Previous, "delta": delta, "changeRate": rate}, Strings: map[string]string{}, CellRefs: refs(value)}}}, nil
}

func trend(input MethodInput) (MethodResult, error) {
	values := present(input.Values)
	if len(values) == 0 {
		return MethodResult{}, errors.New("TREND has no present values")
	}
	var sumX, sumY, sumXY, sumXX float64
	for index, value := range values {
		x := float64(index)
		sumX += x
		sumY += value.Value
		sumXY += x * value.Value
		sumXX += x * x
	}
	n := float64(len(values))
	denominator := n*sumXX - sumX*sumX
	slope := 0.0
	if denominator != 0 {
		slope = (n*sumXY - sumX*sumY) / denominator
	}
	direction := "FLAT"
	if slope > 0 {
		direction = "UP"
	} else if slope < 0 {
		direction = "DOWN"
	}
	return MethodResult{Facts: []ComputedFact{{Kind: "TREND", Values: map[string]float64{"slope": slope, "points": n}, Strings: map[string]string{"direction": direction}, CellRefs: allRefs(values)}}}, nil
}

func anomalyPoint(input MethodInput) (MethodResult, error) {
	values := present(input.Values)
	if len(values) == 0 {
		return MethodResult{}, errors.New("ANOMALY_POINT has no present values")
	}
	if len(values) < 2 {
		return MethodResult{
			Facts:    []ComputedFact{{Kind: "ANOMALY_SUMMARY", Values: map[string]float64{"points": 0}, Strings: map[string]string{"method": "ZSCORE"}, CellRefs: allRefs(values)}},
			Warnings: []string{"INSUFFICIENT_POINTS"},
		}, nil
	}
	mean := 0.0
	for _, value := range values {
		mean += value.Value
	}
	mean /= float64(len(values))
	variance := 0.0
	for _, value := range values {
		variance += math.Pow(value.Value-mean, 2)
	}
	deviation := math.Sqrt(variance / float64(len(values)))
	threshold := input.Threshold
	if threshold <= 0 {
		threshold = 2
	}
	facts := []ComputedFact{{
		Kind: "ANOMALY_SUMMARY", Values: map[string]float64{"points": 0, "threshold": threshold},
		Strings: map[string]string{"method": "ZSCORE"}, CellRefs: allRefs(values),
	}}
	if deviation != 0 {
		for _, value := range values {
			z := math.Abs(value.Value-mean) / deviation
			if z >= threshold {
				facts = append(facts, ComputedFact{Kind: "ANOMALY_POINT", Key: value.Key, Values: map[string]float64{"value": value.Value, "zScore": z, "threshold": threshold}, Strings: map[string]string{"method": "ZSCORE"}, CellRefs: refs(value)})
				facts[0].Values["points"]++
			}
		}
	}
	return MethodResult{Facts: facts}, nil
}

func topN(input MethodInput) (MethodResult, error) {
	values := present(input.Values)
	if len(values) == 0 {
		return MethodResult{}, errors.New("TOP_N has no present values")
	}
	sort.SliceStable(values, func(i, j int) bool { return values[i].Value > values[j].Value })
	limit := input.TopN
	if limit <= 0 {
		limit = 10
	}
	if limit > len(values) {
		limit = len(values)
	}
	total, selected := 0.0, 0.0
	for _, value := range values {
		total += value.Value
	}
	facts := make([]ComputedFact, 0, limit)
	for _, value := range values[:limit] {
		selected += value.Value
		facts = append(facts, ComputedFact{Kind: "TOP_N_ITEM", Key: value.Key, Values: map[string]float64{"value": value.Value}, Strings: map[string]string{}, CellRefs: refs(value)})
	}
	share := 0.0
	if total != 0 {
		share = selected / total
	}
	facts = append(facts, ComputedFact{Kind: "TOP_N_SUMMARY", Values: map[string]float64{"totalShare": share}, Strings: map[string]string{}, CellRefs: allRefs(values[:limit])})
	return MethodResult{Facts: facts}, nil
}

func contribution(input MethodInput) (MethodResult, error) {
	values := present(input.Values)
	if len(values) == 0 {
		return MethodResult{}, errors.New("CONTRIBUTION has no present values")
	}
	total := 0.0
	for _, value := range values {
		total += math.Abs(value.Value)
	}
	facts := []ComputedFact{}
	for _, value := range values {
		rate := 0.0
		if total != 0 {
			rate = value.Value / total
		}
		facts = append(facts, ComputedFact{Kind: "CONTRIBUTION", Key: value.Key, Values: map[string]float64{"value": value.Value, "contribution": rate}, Strings: map[string]string{}, CellRefs: refs(value)})
	}
	return MethodResult{Facts: facts}, nil
}

func maxChange(input MethodInput) (MethodResult, error) {
	var increase, decrease *ComputedFact
	for _, value := range present(input.Values) {
		if value.Previous == nil {
			continue
		}
		delta := value.Value - *value.Previous
		fact := ComputedFact{Kind: "MAX_CHANGE", Key: value.Key, Values: map[string]float64{"current": value.Value, "previous": *value.Previous, "delta": delta}, Strings: map[string]string{}, CellRefs: refs(value)}
		if increase == nil || delta > increase.Values["delta"] {
			copy := fact
			increase = &copy
		}
		if decrease == nil || delta < decrease.Values["delta"] {
			copy := fact
			decrease = &copy
		}
	}
	if increase == nil {
		return MethodResult{}, errors.New("MAX_CHANGE requires previous values")
	}
	increase.Kind = "MAX_INCREASE"
	decrease.Kind = "MAX_DECREASE"
	return MethodResult{Facts: []ComputedFact{*increase, *decrease}}, nil
}

func targetAchievement(input MethodInput) (MethodResult, error) {
	facts := []ComputedFact{}
	for _, value := range present(input.Values) {
		if value.Target == nil {
			continue
		}
		rate := 0.0
		if *value.Target != 0 {
			rate = value.Value / *value.Target
		}
		facts = append(facts, ComputedFact{Kind: "TARGET_ACHIEVEMENT", Key: value.Key, Values: map[string]float64{"actual": value.Value, "target": *value.Target, "rate": rate}, Strings: map[string]string{}, CellRefs: refs(value)})
	}
	if len(facts) == 0 {
		return MethodResult{}, errors.New("TARGET_ACHIEVEMENT requires target values")
	}
	return MethodResult{Facts: facts}, nil
}

func groupDifference(input MethodInput) (MethodResult, error) {
	groups := map[string][]NumericValue{}
	for _, value := range present(input.Values) {
		if strings.TrimSpace(value.Group) == "" {
			return MethodResult{}, errors.New("GROUP_DIFFERENCE requires non-empty groups")
		}
		groups[value.Group] = append(groups[value.Group], value)
	}
	if len(groups) < 2 {
		return MethodResult{}, errors.New("GROUP_DIFFERENCE requires at least two groups")
	}
	minValue, maxValue := math.Inf(1), math.Inf(-1)
	facts := []ComputedFact{}
	for group, values := range groups {
		average := 0.0
		for _, value := range values {
			average += value.Value
		}
		average /= float64(len(values))
		minValue, maxValue = math.Min(minValue, average), math.Max(maxValue, average)
		facts = append(facts, ComputedFact{Kind: "GROUP_VALUE", Key: group, Values: map[string]float64{"value": average}, Strings: map[string]string{}, CellRefs: allRefs(values)})
	}
	sort.Slice(facts, func(i, j int) bool { return facts[i].Key < facts[j].Key })
	facts = append(facts, ComputedFact{Kind: "GROUP_SPREAD", Values: map[string]float64{"spread": maxValue - minValue}, Strings: map[string]string{}, CellRefs: allRefs(present(input.Values))})
	return MethodResult{Facts: facts}, nil
}

func shareOfTotal(input MethodInput) (MethodResult, error) {
	values := present(input.Values)
	if len(values) == 0 {
		return MethodResult{}, errors.New("SHARE_OF_TOTAL has no present values")
	}
	total := 0.0
	for _, value := range values {
		total += value.Value
	}
	facts := []ComputedFact{}
	for _, value := range values {
		share := 0.0
		if total != 0 {
			share = value.Value / total
		}
		facts = append(facts, ComputedFact{Kind: "SHARE_OF_TOTAL", Key: value.Key, Values: map[string]float64{"value": value.Value, "share": share}, Strings: map[string]string{}, CellRefs: refs(value)})
	}
	return MethodResult{Facts: facts}, nil
}

func dataCompleteness(input MethodInput) (MethodResult, error) {
	missing := 0
	groups := map[string]struct{}{}
	refs := []shared.CellRef{}
	for _, value := range input.Values {
		if value.Missing {
			missing++
			groups[value.Group] = struct{}{}
			refs = appendValidRef(refs, value.CellRef)
		}
	}
	return MethodResult{Facts: []ComputedFact{{Kind: "DATA_COMPLETENESS", Values: map[string]float64{"missingRatio": float64(missing) / float64(len(input.Values)), "affectedDimensions": float64(len(groups))}, Strings: map[string]string{}, CellRefs: refs}}}, nil
}

func firstPresent(values []NumericValue) (NumericValue, bool) {
	for _, value := range values {
		if !value.Missing {
			return value, true
		}
	}
	return NumericValue{}, false
}

func present(values []NumericValue) []NumericValue {
	result := make([]NumericValue, 0, len(values))
	for _, value := range values {
		if !value.Missing {
			result = append(result, value)
		}
	}
	return result
}

func refs(value NumericValue) []shared.CellRef {
	return appendValidRef(nil, value.CellRef)
}

func allRefs(values []NumericValue) []shared.CellRef {
	result := []shared.CellRef{}
	for _, value := range values {
		result = appendValidRef(result, value.CellRef)
	}
	return result
}

func appendValidRef(values []shared.CellRef, value shared.CellRef) []shared.CellRef {
	if value.RowKey != "" && value.ColumnKey != "" {
		return append(values, value)
	}
	return values
}
