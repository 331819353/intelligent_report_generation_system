package goldenset

import (
	"encoding/json"
	"fmt"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/compiler"
	"intelligent-report-generation-system/internal/askdata/evaluation/suites"
	"intelligent-report-generation-system/internal/askdata/registry"
)

// The metric identities are fixed per position so many cases can share one
// compiled question shape. What varies between cases is the governed contract
// the release resolves those identities to — which is exactly the variable the
// suite is about.
var (
	subjectMetricA = chainMetric{
		ObjectID: "metric-golden-subject-a", VersionID: "metric-golden-subject-a-v1", Text: "库存",
	}
	subjectMetricB = chainMetric{
		ObjectID: "metric-golden-subject-b", VersionID: "metric-golden-subject-b-v1", Text: "收入",
	}
	regionDimension = chainDimension{
		ObjectID: dimensionRegionObject, VersionID: dimensionRegion, Text: "地区",
	}
	channelDimension = chainDimension{
		ObjectID: dimensionChannelObject, VersionID: dimensionChannel, Text: "渠道",
	}
)

// The questions deliberately avoid 按 and 各: those trigger deterministic
// grouping rules, which would make the parser rather than the contract decide
// the GROUP_BY roles this suite is not trying to test.
var additivityChainSpecs = []chainSpec{
	{Key: "one-metric", Question: "库存", Metrics: []chainMetric{subjectMetricA}},
	{
		Key: "one-metric-region", Question: "地区库存",
		Metrics: []chainMetric{subjectMetricA}, GroupBy: []chainDimension{regionDimension},
	},
	{
		Key: "one-metric-channel", Question: "渠道库存",
		Metrics: []chainMetric{subjectMetricA}, GroupBy: []chainDimension{channelDimension},
	},
	{
		Key: "one-metric-region-channel", Question: "地区渠道库存",
		Metrics: []chainMetric{subjectMetricA},
		GroupBy: []chainDimension{regionDimension, channelDimension},
	},
	{
		Key: "one-metric-time", Question: "本月库存",
		Metrics: []chainMetric{subjectMetricA}, WithTime: true,
	},
	{
		Key: "one-metric-region-time", Question: "本月地区库存",
		Metrics: []chainMetric{subjectMetricA}, GroupBy: []chainDimension{regionDimension}, WithTime: true,
	},
	{
		Key: "one-metric-region-channel-time", Question: "本月地区渠道库存",
		Metrics: []chainMetric{subjectMetricA},
		GroupBy: []chainDimension{regionDimension, channelDimension}, WithTime: true,
	},
	{
		Key: "two-metrics", Question: "库存收入",
		Metrics: []chainMetric{subjectMetricA, subjectMetricB},
	},
	{
		Key: "two-metrics-region", Question: "地区库存收入",
		Metrics: []chainMetric{subjectMetricA, subjectMetricB}, GroupBy: []chainDimension{regionDimension},
	},
	{
		Key: "two-metrics-region-channel", Question: "地区渠道库存收入",
		Metrics: []chainMetric{subjectMetricA, subjectMetricB},
		GroupBy: []chainDimension{regionDimension, channelDimension},
	},
}

func buildAdditivityChains() (map[string]chain, error) {
	chains := make(map[string]chain, len(additivityChainSpecs))
	for _, spec := range additivityChainSpecs {
		built, err := buildChain(spec)
		if err != nil {
			return nil, err
		}
		chains[spec.Key] = built
	}
	return chains, nil
}

// dimensionIDsFor lists the dimensions the release must resolve for a shape.
// The time dimension is included because a resolved time range is looked up as
// a dimension, not as a property of the query.
func dimensionIDsFor(shape string) ([]askdata.ID, error) {
	for _, spec := range additivityChainSpecs {
		if spec.Key != shape {
			continue
		}
		ids := make([]askdata.ID, 0, len(spec.GroupBy)+1)
		for _, dimension := range chainDimensions(spec) {
			ids = append(ids, dimension.VersionID)
		}
		return ids, nil
	}
	return nil, fmt.Errorf("%w: unknown shape %s", ErrAdditivityGoldenSet, shape)
}

type additivityScenarioBuilder struct {
	scenarios []additivityScenario
	err       error
}

func (builder *additivityScenarioBuilder) fail(err error) {
	if builder.err == nil && err != nil {
		builder.err = err
	}
}

func (builder *additivityScenarioBuilder) add(
	id string,
	category suites.AdditivitySuiteCategory,
	expectedFunction string,
	shape string,
	metrics []compiler.MetricContract,
	subject askdata.ID,
) {
	if builder.err != nil {
		return
	}
	dimensionIDs, err := dimensionIDsFor(shape)
	if err != nil {
		builder.fail(err)
		return
	}
	contractPayload, err := json.Marshal(metrics)
	if err != nil {
		builder.fail(err)
		return
	}
	builder.scenarios = append(builder.scenarios, additivityScenario{
		public: suites.AdditivitySuiteCase{
			CaseID: askdata.ID(id), Category: category, Synthetic: true,
			ContractHash:     askdata.HashBytes(append([]byte(AdditivitySuiteVersion+"|"+id+"|"), contractPayload...)),
			ExpectedFunction: expectedFunction,
		},
		shape: shape, metrics: metrics, dimensionIDs: dimensionIDs, subject: subject,
	})
}

func buildAdditivityScenarios() ([]additivityScenario, error) {
	builder := &additivityScenarioBuilder{}
	builder.ratioGroupTotals()
	builder.distinctGroupTotals()
	builder.semiAdditivePeriods()
	builder.mixedUnitBlocks()
	if builder.err != nil {
		return nil, builder.err
	}
	return builder.scenarios, nil
}

// ratioGroupTotals asserts the single most common wrong number in analytics:
// a rate summed across groups. The compiler must divide aggregates, never
// aggregate a per-row division, and must guard the denominator.
func (builder *additivityScenarioBuilder) ratioGroupTotals() {
	pairs := []struct {
		name        string
		numerator   askdata.ID
		denominator askdata.ID
	}{
		{"gross-margin", fieldGrossProfit, fieldNetSales},
		{"sales-per-order", fieldNetSales, fieldOrderCount},
		{"profit-per-order", fieldGrossProfit, fieldOrderCount},
		{"sales-per-unit", fieldNetSales, fieldInventoryUnits},
		{"orders-per-unit", fieldOrderCount, fieldInventoryUnits},
	}
	shapes := []string{"one-metric-region", "one-metric-region-channel"}
	index := 0
	for _, pair := range pairs {
		for _, zeroPolicy := range []registry.ZeroDenominatorPolicy{
			registry.ZeroDenominatorNull, registry.ZeroDenominatorZero,
		} {
			for _, denominatorAggregation := range []registry.Aggregation{
				registry.AggregationSum, registry.AggregationCount,
			} {
				for _, shape := range shapes {
					index++
					numerator, err := goldenMeasure(
						fmt.Sprintf("%s-numerator-%d", pair.name, index), pair.numerator,
						registry.AggregationSum, registry.NumericDecimal,
					)
					if err != nil {
						builder.fail(err)
						return
					}
					denominator, err := goldenMeasure(
						fmt.Sprintf("%s-denominator-%d", pair.name, index), pair.denominator,
						denominatorAggregation, registry.NumericDecimal,
					)
					if err != nil {
						builder.fail(err)
						return
					}
					formula := fmt.Sprintf(
						`{"type":"DIVIDE","arguments":[%s,%s]}`,
						measureReferenceAST(numerator.MeasureVersionID),
						measureReferenceAST(denominator.MeasureVersionID),
					)
					// NullPolicy stays PRESERVE: a metric-level zero fill on top of
					// the zero-denominator COALESCE would nest two COALESCE nodes
					// and the ratio shape would no longer be recognisable — the
					// compiler would be right and the assertion wrong.
					metric, err := goldenMetric(goldenMetricSpec{
						Name: pair.name, Index: index, Formula: formula, Unit: "RATIO",
						Additivity: registry.NonAdditive, Restriction: registry.PostAggregate,
						ZeroPolicy: zeroPolicy, NullPolicy: "PRESERVE", Precision: 4,
						Measures: []compiler.MeasureContract{numerator, denominator},
					})
					if err != nil {
						builder.fail(err)
						return
					}
					builder.add(
						fmt.Sprintf("additivity-golden-ratio-%03d", index),
						suites.AdditivityRatioGroupTotal, "", shape,
						[]compiler.MetricContract{metric}, metric.MetricVersionID,
					)
				}
			}
		}
	}
}

// distinctGroupTotals asserts that a distinct count is never re-aggregated.
// Summing per-group distinct counts double counts every entity that appears in
// more than one group, and the total silently exceeds the population.
func (builder *additivityScenarioBuilder) distinctGroupTotals() {
	subjects := []struct {
		name  string
		field askdata.ID
	}{
		{"distinct-customers", fieldCustomer},
		{"distinct-regions", fieldRegion},
		{"distinct-channels", fieldChannel},
	}
	shapes := []string{"one-metric-region", "one-metric-channel", "one-metric-region-channel"}
	index := 0
	for _, subject := range subjects {
		for _, shape := range shapes {
			for _, nullPolicy := range []string{"PRESERVE", "ZERO"} {
				for _, unit := range []string{"COUNT", "ENTITY"} {
					index++
					measure, err := goldenMeasure(
						fmt.Sprintf("%s-%d", subject.name, index), subject.field,
						registry.AggregationCountDistinct, registry.NumericInteger,
					)
					if err != nil {
						builder.fail(err)
						return
					}
					metric, err := goldenMetric(goldenMetricSpec{
						Name: subject.name, Index: index,
						Formula: measureReferenceAST(measure.MeasureVersionID), Unit: unit,
						Additivity: registry.NonAdditive, Restriction: registry.PostAggregate,
						ZeroPolicy: registry.ZeroDenominatorNull, NullPolicy: nullPolicy, Precision: 0,
						Measures: []compiler.MeasureContract{measure},
					})
					if err != nil {
						builder.fail(err)
						return
					}
					builder.add(
						fmt.Sprintf("additivity-golden-distinct-%03d", index),
						suites.AdditivityDistinctGroupTotal, "", shape,
						[]compiler.MetricContract{metric}, metric.MetricVersionID,
					)
				}
			}
		}
	}
}

// semiAdditivePeriods asserts that a stock measure is reduced over time before
// it is added up across anything else. Without the window the compiler would
// sum a balance across days, which is the classic inflated inventory number.
func (builder *additivityScenarioBuilder) semiAdditivePeriods() {
	subjects := []struct {
		name  string
		field askdata.ID
	}{
		{"inventory-on-hand", fieldInventoryUnits},
		{"account-balance", fieldAccountBalance},
	}
	reductions := []struct {
		aggregation registry.SemiAdditiveTimeAggregation
		expected    string
	}{
		{registry.SemiAdditivePeriodEnd, "PERIOD_END"},
		{registry.SemiAdditivePeriodBegin, "PERIOD_BEGIN"},
		{registry.SemiAdditivePeriodAverage, "AVG"},
	}
	innerAggregations := []registry.Aggregation{
		registry.AggregationSum, registry.AggregationMaximum, registry.AggregationMinimum,
	}
	index := 0
	for _, subject := range subjects {
		for _, reduction := range reductions {
			for _, inner := range innerAggregations {
				for _, nullPolicy := range []string{"PRESERVE", "ZERO"} {
					index++
					metric, err := builder.semiAdditiveMetric(
						subject.name, index, subject.field, inner, reduction.aggregation, nullPolicy,
					)
					if err != nil {
						builder.fail(err)
						return
					}
					builder.add(
						fmt.Sprintf("additivity-golden-semi-time-%03d", index),
						suites.AdditivitySemiPeriod, reduction.expected, "one-metric-time",
						[]compiler.MetricContract{metric}, metric.MetricVersionID,
					)
				}
			}
		}
	}
	shapes := []string{"one-metric-region-time", "one-metric-region-channel-time"}
	index = 0
	for _, subject := range subjects {
		for _, reduction := range reductions {
			for _, shape := range shapes {
				for _, nullPolicy := range []string{"PRESERVE", "ZERO"} {
					index++
					metric, err := builder.semiAdditiveMetric(
						subject.name+"-grouped", index, subject.field,
						registry.AggregationSum, reduction.aggregation, nullPolicy,
					)
					if err != nil {
						builder.fail(err)
						return
					}
					builder.add(
						fmt.Sprintf("additivity-golden-semi-mixed-%03d", index),
						suites.AdditivitySemiTimeAndNonTime, reduction.expected, shape,
						[]compiler.MetricContract{metric}, metric.MetricVersionID,
					)
				}
			}
		}
	}
}

func (builder *additivityScenarioBuilder) semiAdditiveMetric(
	name string,
	index int,
	field askdata.ID,
	inner registry.Aggregation,
	reduction registry.SemiAdditiveTimeAggregation,
	nullPolicy string,
) (compiler.MetricContract, error) {
	measure, err := goldenMeasure(fmt.Sprintf("%s-%d", name, index), field, inner, registry.NumericDecimal)
	if err != nil {
		return compiler.MetricContract{}, err
	}
	return goldenMetric(goldenMetricSpec{
		Name: name, Index: index, Formula: measureReferenceAST(measure.MeasureVersionID), Unit: "UNIT",
		Additivity: registry.SemiAdditive, SemiAdditive: reduction,
		ZeroPolicy: registry.ZeroDenominatorNull, NullPolicy: nullPolicy, Precision: 2,
		Measures: []compiler.MeasureContract{measure},
	})
}

// mixedUnitBlocks asserts the compiler refuses rather than produces a number.
// A chart that adds a currency amount to a count is not a degraded answer, it
// is a wrong one, so the only acceptable behaviour is a governed refusal.
func (builder *additivityScenarioBuilder) mixedUnitBlocks() {
	// Every variation is a pair of individually well-formed metrics that simply
	// cannot share an axis. A metric with no unit at all is deliberately absent:
	// the resolver refuses to load such a contract in the first place, so it
	// never reaches the aggregation planner. Filing it here would credit this
	// suite with a refusal an earlier and stronger gate actually made.
	variations := []struct {
		name       string
		units      []string
		currencies []string
	}{
		{"unit-mismatch", []string{"CURRENCY", "COUNT"}, []string{"CNY", ""}},
		{"currency-mismatch", []string{"CURRENCY", "CURRENCY"}, []string{"CNY", "USD"}},
		{"ratio-versus-percent", []string{"RATIO", "PERCENT"}, []string{"", ""}},
		{"currency-versus-unit", []string{"CURRENCY", "UNIT"}, []string{"CNY", ""}},
		{"count-versus-ratio", []string{"COUNT", "RATIO"}, []string{"", ""}},
	}
	twoMetricShapes := []string{"two-metrics", "two-metrics-region", "two-metrics-region-channel"}
	index := 0
	for _, variation := range variations {
		for _, shape := range twoMetricShapes {
			index++
			metrics := make([]compiler.MetricContract, 0, len(variation.units))
			for position := range variation.units {
				measure, err := goldenMeasure(
					fmt.Sprintf("%s-%d-%d", variation.name, index, position), fieldNetSales,
					registry.AggregationSum, registry.NumericDecimal,
				)
				if err != nil {
					builder.fail(err)
					return
				}
				metric, err := goldenMetric(goldenMetricSpec{
					Name: variation.name, Index: index, Position: position,
					Formula: measureReferenceAST(measure.MeasureVersionID),
					Unit:    variation.units[position], Currency: variation.currencies[position],
					Additivity: registry.FullyAdditive, ZeroPolicy: registry.ZeroDenominatorNull,
					NullPolicy: "PRESERVE", Precision: 2,
					Measures: []compiler.MeasureContract{measure},
				})
				if err != nil {
					builder.fail(err)
					return
				}
				metrics = append(metrics, metric)
			}
			builder.add(
				fmt.Sprintf("additivity-golden-mixed-unit-%03d", index),
				suites.AdditivityMixedUnitCurrencyBlock, "", shape,
				metrics, metrics[0].MetricVersionID,
			)
		}
	}
}

// goldenMetricSpec keeps the contract fields a case actually varies together in
// one place, so a new case cannot silently omit one the resolver requires.
type goldenMetricSpec struct {
	Name         string
	Index        int
	Position     int
	Formula      string
	Unit         string
	Currency     string
	Additivity   registry.Additivity
	SemiAdditive registry.SemiAdditiveTimeAggregation
	Restriction  registry.AggregationRestriction
	ZeroPolicy   registry.ZeroDenominatorPolicy
	NullPolicy   string
	Precision    int16
	Measures     []compiler.MeasureContract
}

func goldenMetric(spec goldenMetricSpec) (compiler.MetricContract, error) {
	formula, err := canonicalAST(spec.Formula)
	if err != nil {
		return compiler.MetricContract{}, err
	}
	defaultFilter, err := canonicalAST(`{"type":"TRUE"}`)
	if err != nil {
		return compiler.MetricContract{}, err
	}
	// The identity comes from the position in the query, not from the case: many
	// cases share one compiled question shape and therefore one metric identity.
	metricVersionID := subjectMetricA.VersionID
	if spec.Position == 1 {
		metricVersionID = subjectMetricB.VersionID
	}
	return compiler.MetricContract{
		MetricVersionID: metricVersionID, ModelVersionID: goldenModelVersionID,
		ContentHash: askdata.HashBytes([]byte(fmt.Sprintf(
			"golden-metric|%s|%d|%d", spec.Name, spec.Index, spec.Position,
		))),
		FormulaAST: formula, DefaultFilterAST: defaultFilter,
		Unit: spec.Unit, Currency: spec.Currency, TimeGrain: "DAY",
		Additivity: spec.Additivity, SemiAdditiveTimeAggregation: spec.SemiAdditive,
		AggregationRestriction: spec.Restriction, NonAdditiveDimensions: []string{},
		ZeroDenominatorPolicy: spec.ZeroPolicy, DisplayPrecision: spec.Precision,
		NullPolicy: spec.NullPolicy, Measures: spec.Measures,
	}, nil
}
