package goldenset

import (
	"encoding/json"
	"fmt"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/compiler"
	"intelligent-report-generation-system/internal/askdata/evaluation/suites"
	"intelligent-report-generation-system/internal/askdata/registry"
)

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
	metrics []compiler.MetricContract,
	subject askdata.ID,
	groupBy []askdata.ID,
	withTimeRange bool,
) {
	if builder.err != nil {
		return
	}
	scope, err := goldenScope()
	if err != nil {
		builder.fail(err)
		return
	}
	dimensionIDs := append([]askdata.ID(nil), groupBy...)
	var timeDimension *askdata.ID
	if withTimeRange {
		dimensionIDs = append(dimensionIDs, dimensionOrderDate)
		value := dimensionOrderDate
		timeDimension = &value
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
		query:           goldenSemanticIR(metrics, groupBy, withTimeRange),
		resolution:      goldenResolution(scope, metrics, dimensionIDs, timeDimension),
		metricVersionID: subject,
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
	groupings := [][]askdata.ID{{dimensionRegion}, {dimensionRegion, dimensionChannel}}
	index := 0
	for _, pair := range pairs {
		for _, zeroPolicy := range []registry.ZeroDenominatorPolicy{
			registry.ZeroDenominatorNull, registry.ZeroDenominatorZero,
		} {
			for _, denominatorAggregation := range []registry.Aggregation{
				registry.AggregationSum, registry.AggregationCount,
			} {
				for _, grouping := range groupings {
					index++
					id := fmt.Sprintf("additivity-golden-ratio-%03d", index)
					numerator := goldenMeasure(
						pair.name+"-numerator-"+fmt.Sprint(index), pair.numerator,
						registry.AggregationSum, registry.NumericDecimal,
					)
					denominator := goldenMeasure(
						pair.name+"-denominator-"+fmt.Sprint(index), pair.denominator,
						denominatorAggregation, registry.NumericDecimal,
					)
					metricVersionID := askdata.ID("metric-golden-" + pair.name + "-" + fmt.Sprint(index) + "-v1")
					formula := fmt.Sprintf(
						`{"type":"DIVIDE","arguments":[%s,%s]}`,
						measureReferenceAST(numerator.MeasureVersionID),
						measureReferenceAST(denominator.MeasureVersionID),
					)
					metric := compiler.MetricContract{
						MetricVersionID: metricVersionID, ModelVersionID: goldenModelVersionID,
						ContentHash:      askdata.HashBytes([]byte("golden-metric|" + metricVersionID)),
						FormulaAST:       json.RawMessage(formula),
						DefaultFilterAST: json.RawMessage(`{"type":"TRUE"}`),
						Unit:             "RATIO", TimeGrain: "DAY",
						Additivity:             registry.NonAdditive,
						AggregationRestriction: registry.PostAggregate,
						NonAdditiveDimensions:  []string{},
						ZeroDenominatorPolicy:  zeroPolicy, DisplayPrecision: 4, NullPolicy: "PRESERVE",
						Measures: []compiler.MeasureContract{numerator, denominator},
					}
					builder.add(
						id, suites.AdditivityRatioGroupTotal, "",
						[]compiler.MetricContract{metric}, metricVersionID, grouping, false,
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
	groupings := [][]askdata.ID{
		{dimensionRegion}, {dimensionChannel}, {dimensionRegion, dimensionChannel},
	}
	index := 0
	for _, subject := range subjects {
		for _, grouping := range groupings {
			for _, nullPolicy := range []string{"PRESERVE", "ZERO"} {
				for _, unit := range []string{"COUNT", "ENTITY"} {
					index++
					id := fmt.Sprintf("additivity-golden-distinct-%03d", index)
					measure := goldenMeasure(
						subject.name+"-"+fmt.Sprint(index), subject.field,
						registry.AggregationCountDistinct, registry.NumericInteger,
					)
					metricVersionID := askdata.ID("metric-golden-" + subject.name + "-" + fmt.Sprint(index) + "-v1")
					metric := compiler.MetricContract{
						MetricVersionID: metricVersionID, ModelVersionID: goldenModelVersionID,
						ContentHash:      askdata.HashBytes([]byte("golden-metric|" + metricVersionID)),
						FormulaAST:       json.RawMessage(measureReferenceAST(measure.MeasureVersionID)),
						DefaultFilterAST: json.RawMessage(`{"type":"TRUE"}`),
						Unit:             unit, TimeGrain: "DAY",
						Additivity:             registry.NonAdditive,
						AggregationRestriction: registry.PostAggregate,
						NonAdditiveDimensions:  []string{},
						ZeroDenominatorPolicy:  registry.ZeroDenominatorNull,
						DisplayPrecision:       0, NullPolicy: nullPolicy,
						Measures: []compiler.MeasureContract{measure},
					}
					builder.add(
						id, suites.AdditivityDistinctGroupTotal, "",
						[]compiler.MetricContract{metric}, metricVersionID, grouping, false,
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
					id := fmt.Sprintf("additivity-golden-semi-time-%03d", index)
					metric, metricVersionID := semiAdditiveMetric(
						subject.name, index, subject.field, inner, reduction.aggregation, nullPolicy,
					)
					builder.add(
						id, suites.AdditivitySemiPeriod, reduction.expected,
						[]compiler.MetricContract{metric}, metricVersionID, nil, true,
					)
				}
			}
		}
	}
	groupings := [][]askdata.ID{{dimensionRegion}, {dimensionRegion, dimensionChannel}}
	index = 0
	for _, subject := range subjects {
		for _, reduction := range reductions {
			for _, grouping := range groupings {
				for _, nullPolicy := range []string{"PRESERVE", "ZERO"} {
					index++
					id := fmt.Sprintf("additivity-golden-semi-mixed-%03d", index)
					metric, metricVersionID := semiAdditiveMetric(
						subject.name+"-grouped", index, subject.field,
						registry.AggregationSum, reduction.aggregation, nullPolicy,
					)
					builder.add(
						id, suites.AdditivitySemiTimeAndNonTime, reduction.expected,
						[]compiler.MetricContract{metric}, metricVersionID, grouping, true,
					)
				}
			}
		}
	}
}

func semiAdditiveMetric(
	name string,
	index int,
	field askdata.ID,
	inner registry.Aggregation,
	reduction registry.SemiAdditiveTimeAggregation,
	nullPolicy string,
) (compiler.MetricContract, askdata.ID) {
	measure := goldenMeasure(name+"-"+fmt.Sprint(index), field, inner, registry.NumericDecimal)
	metricVersionID := askdata.ID("metric-golden-" + name + "-" + fmt.Sprint(index) + "-v1")
	return compiler.MetricContract{
		MetricVersionID: metricVersionID, ModelVersionID: goldenModelVersionID,
		ContentHash:      askdata.HashBytes([]byte("golden-metric|" + metricVersionID)),
		FormulaAST:       json.RawMessage(measureReferenceAST(measure.MeasureVersionID)),
		DefaultFilterAST: json.RawMessage(`{"type":"TRUE"}`),
		Unit:             "UNIT", TimeGrain: "DAY",
		Additivity:                  registry.SemiAdditive,
		SemiAdditiveTimeAggregation: reduction,
		NonAdditiveDimensions:       []string{},
		ZeroDenominatorPolicy:       registry.ZeroDenominatorNull,
		DisplayPrecision:            2, NullPolicy: nullPolicy,
		Measures: []compiler.MeasureContract{measure},
	}, metricVersionID
}

// mixedUnitBlocks asserts the compiler refuses rather than produces a number.
// A chart that adds a currency amount to a count is not a degraded answer, it
// is a wrong one, so the only acceptable behaviour is a governed refusal.
func (builder *additivityScenarioBuilder) mixedUnitBlocks() {
	type variation struct {
		name      string
		units     []string
		currency  []string
		metricSet int
	}
	variations := []variation{
		{"unit-mismatch", []string{"CURRENCY", "COUNT"}, []string{"CNY", ""}, 2},
		{"currency-mismatch", []string{"CURRENCY", "CURRENCY"}, []string{"CNY", "USD"}, 2},
		{"ratio-versus-percent", []string{"RATIO", "PERCENT"}, []string{"", ""}, 2},
		{"missing-unit", []string{""}, []string{""}, 1},
		{"currency-without-code", []string{"CURRENCY"}, []string{""}, 1},
	}
	groupings := [][]askdata.ID{nil, {dimensionRegion}, {dimensionRegion, dimensionChannel}}
	index := 0
	for _, item := range variations {
		for _, grouping := range groupings {
			index++
			id := fmt.Sprintf("additivity-golden-mixed-unit-%03d", index)
			metrics := make([]compiler.MetricContract, 0, item.metricSet)
			for position := 0; position < item.metricSet; position++ {
				measure := goldenMeasure(
					fmt.Sprintf("%s-%d-%d", item.name, index, position), fieldNetSales,
					registry.AggregationSum, registry.NumericDecimal,
				)
				metricVersionID := askdata.ID(fmt.Sprintf("metric-golden-%s-%d-%d-v1", item.name, index, position))
				metrics = append(metrics, compiler.MetricContract{
					MetricVersionID: metricVersionID, ModelVersionID: goldenModelVersionID,
					ContentHash:      askdata.HashBytes([]byte("golden-metric|" + metricVersionID)),
					FormulaAST:       json.RawMessage(measureReferenceAST(measure.MeasureVersionID)),
					DefaultFilterAST: json.RawMessage(`{"type":"TRUE"}`),
					Unit:             item.units[position], Currency: item.currency[position], TimeGrain: "DAY",
					Additivity: registry.FullyAdditive, NonAdditiveDimensions: []string{},
					ZeroDenominatorPolicy: registry.ZeroDenominatorNull,
					DisplayPrecision:      2, NullPolicy: "PRESERVE",
					Measures: []compiler.MeasureContract{measure},
				})
			}
			builder.add(
				id, suites.AdditivityMixedUnitCurrencyBlock, "",
				metrics, metrics[0].MetricVersionID, grouping, false,
			)
		}
	}
}
