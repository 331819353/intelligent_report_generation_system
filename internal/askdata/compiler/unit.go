package compiler

import (
	"sort"
	"strings"
)

// CheckUnitCompatibility rejects mixed measures before any Dataset DSL or SQL
// is produced. Empty currency is a real value for non-currency units.
func CheckUnitCompatibility(metrics []MetricContract) error {
	units := map[string]struct{}{}
	currencies := map[string]struct{}{}
	for _, metric := range metrics {
		unit := strings.TrimSpace(metric.Unit)
		if unit == "" {
			return &AggregationPlanError{Code: IncompatibleUnitCode, MetricVersionID: metric.MetricVersionID}
		}
		currency := strings.TrimSpace(metric.Currency)
		if strings.EqualFold(unit, "CURRENCY") && currency == "" {
			return &AggregationPlanError{Code: IncompatibleUnitCode, MetricVersionID: metric.MetricVersionID}
		}
		units[unit] = struct{}{}
		currencies[currency] = struct{}{}
	}
	if len(units) <= 1 && len(currencies) <= 1 {
		return nil
	}
	return &AggregationPlanError{
		Code:  IncompatibleUnitCode,
		Units: sortedAggregationLabels(units), Currencies: sortedAggregationLabels(currencies),
	}
}

func sortedAggregationLabels(values map[string]struct{}) []string {
	result := make([]string, 0, len(values))
	for value := range values {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}
