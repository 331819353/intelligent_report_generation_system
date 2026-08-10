package suites

import (
	"context"
	"fmt"
	"testing"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/dataset"
)

type additivityCompilerFixture struct {
	mutateCase askdata.ID
}

func (compiler additivityCompilerFixture) CompileAdditivityCase(_ context.Context, evaluationCase AdditivitySuiteCase) (AdditivityASTResult, error) {
	if evaluationCase.Category == AdditivityMixedUnitCurrencyBlock {
		return AdditivityASTResult{CompileErrorCode: "INCOMPATIBLE_UNIT"}, nil
	}
	var expression dataset.Expression
	switch evaluationCase.Category {
	case AdditivityRatioGroupTotal:
		numerator := dataset.Expression{Type: "AGGREGATE", Function: "SUM"}
		denominator := dataset.Expression{Type: "AGGREGATE", Function: "SUM"}
		expression = dataset.Expression{Type: "DIVIDE", Arguments: []dataset.Expression{
			numerator, {Type: "NULLIF", Arguments: []dataset.Expression{denominator, {Type: "LITERAL", Value: 0}}},
		}}
	case AdditivityDistinctGroupTotal:
		expression = dataset.Expression{Type: "AGGREGATE", Function: "COUNT_DISTINCT"}
	case AdditivitySemiPeriod, AdditivitySemiTimeAndNonTime:
		direction := "DESC"
		if evaluationCase.ExpectedFunction == "PERIOD_BEGIN" {
			direction = "ASC"
		}
		expression = dataset.Expression{Type: "AGGREGATE", Function: evaluationCase.ExpectedFunction}
		if evaluationCase.ExpectedFunction != "AVG" {
			expression.OrderBy = []dataset.WindowOrder{{Expression: dataset.Expression{Type: "FIELD_REF"}, Direction: direction}}
		}
	}
	result := AdditivityASTResult{Expression: &expression, PreAggregationCount: 1}
	if evaluationCase.CaseID == compiler.mutateCase {
		result.Expression = &dataset.Expression{Type: "AGGREGATE", Function: "SUM"}
	}
	return result, nil
}

func TestAdditivitySuiteInventoryAndPerfectGate(t *testing.T) {
	cases := syntheticAdditivityCases()
	if err := ValidateAdditivitySuiteInventory(cases); err != nil {
		t.Fatal(err)
	}
	report, err := EvaluateAdditivitySuite(context.Background(), cases, additivityCompilerFixture{})
	if err != nil {
		t.Fatal(err)
	}
	if !report.GatePassed || report.Accuracy != 1 || len(report.Failures) != 0 {
		t.Fatalf("report = %#v", report)
	}
}

func TestAdditivitySuiteDetectsCompilerMutation(t *testing.T) {
	cases := syntheticAdditivityCases()
	report, err := EvaluateAdditivitySuite(context.Background(), cases, additivityCompilerFixture{mutateCase: cases[0].CaseID})
	if err != nil {
		t.Fatal(err)
	}
	if report.GatePassed || len(report.Failures) != 1 || report.Failures[0].Code != "RATIO_NOT_POST_AGGREGATE" {
		t.Fatalf("mutation was not detected: %#v", report)
	}
}

func syntheticAdditivityCases() []AdditivitySuiteCase {
	distribution := []struct {
		category AdditivitySuiteCategory
		count    int
		function string
	}{
		{AdditivityRatioGroupTotal, 30, ""}, {AdditivityDistinctGroupTotal, 25, ""},
		{AdditivitySemiPeriod, 30, "PERIOD_END"}, {AdditivitySemiTimeAndNonTime, 20, "PERIOD_BEGIN"},
		{AdditivityMixedUnitCurrencyBlock, 15, ""},
	}
	result := make([]AdditivitySuiteCase, 0, AdditivitySuiteCaseCount)
	for _, group := range distribution {
		for index := 0; index < group.count; index++ {
			id := askdata.ID(fmt.Sprintf("synthetic-additivity-%s-%03d", group.category, index))
			result = append(result, AdditivitySuiteCase{
				CaseID: id, Category: group.category, Synthetic: true,
				ContractHash: askdata.HashBytes([]byte(id)), ExpectedFunction: group.function,
			})
		}
	}
	return result
}
