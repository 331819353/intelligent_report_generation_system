package suites

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/dataset"
)

const (
	AdditivitySuiteCaseCount = 120
	AdditivitySuiteThreshold = 1.0
)

var ErrInvalidAdditivitySuite = errors.New("additivity evaluation suite is invalid")

type AdditivitySuiteCategory string

const (
	AdditivityRatioGroupTotal        AdditivitySuiteCategory = "RATIO_GROUP_TOTAL"
	AdditivityDistinctGroupTotal     AdditivitySuiteCategory = "DISTINCT_GROUP_TOTAL"
	AdditivitySemiPeriod             AdditivitySuiteCategory = "SEMI_PERIOD"
	AdditivitySemiTimeAndNonTime     AdditivitySuiteCategory = "SEMI_TIME_AND_NON_TIME"
	AdditivityMixedUnitCurrencyBlock AdditivitySuiteCategory = "MIXED_UNIT_CURRENCY_BLOCK"
)

var minimumAdditivityCases = map[AdditivitySuiteCategory]int{
	AdditivityRatioGroupTotal: 30, AdditivityDistinctGroupTotal: 25, AdditivitySemiPeriod: 30,
	AdditivitySemiTimeAndNonTime: 20, AdditivityMixedUnitCurrencyBlock: 15,
}

type AdditivitySuiteCase struct {
	CaseID           askdata.ID
	Category         AdditivitySuiteCategory
	Synthetic        bool
	ContractHash     askdata.ContentHash
	ExpectedFunction string
}

type AdditivityASTResult struct {
	Expression          *dataset.Expression
	PreAggregationCount int
	CompileErrorCode    string
}

type AdditivitySuiteCompiler interface {
	CompileAdditivityCase(context.Context, AdditivitySuiteCase) (AdditivityASTResult, error)
}

type AdditivitySuiteFailure struct {
	CaseID askdata.ID `json:"caseId"`
	Code   string     `json:"code"`
}

type AdditivitySuiteReport struct {
	CaseCount   int                      `json:"caseCount"`
	PassedCount int                      `json:"passedCount"`
	Accuracy    float64                  `json:"accuracy"`
	Threshold   float64                  `json:"threshold"`
	GatePassed  bool                     `json:"gatePassed"`
	Failures    []AdditivitySuiteFailure `json:"failures"`
}

func ValidateAdditivitySuiteInventory(cases []AdditivitySuiteCase) error {
	if len(cases) < AdditivitySuiteCaseCount {
		return fmt.Errorf("%w: at least %d cases are required", ErrInvalidAdditivitySuite, AdditivitySuiteCaseCount)
	}
	counts := map[AdditivitySuiteCategory]int{}
	seen := map[askdata.ID]struct{}{}
	for _, evaluationCase := range cases {
		if evaluationCase.CaseID.Validate() != nil || evaluationCase.ContractHash.Validate() != nil {
			return ErrInvalidAdditivitySuite
		}
		if _, ok := minimumAdditivityCases[evaluationCase.Category]; !ok {
			return ErrInvalidAdditivitySuite
		}
		if _, duplicate := seen[evaluationCase.CaseID]; duplicate {
			return ErrInvalidAdditivitySuite
		}
		seen[evaluationCase.CaseID] = struct{}{}
		counts[evaluationCase.Category]++
	}
	for category, minimum := range minimumAdditivityCases {
		if counts[category] < minimum {
			return fmt.Errorf("%w: %s requires at least %d cases", ErrInvalidAdditivitySuite, category, minimum)
		}
	}
	return nil
}

func EvaluateAdditivitySuite(
	ctx context.Context,
	cases []AdditivitySuiteCase,
	compiler AdditivitySuiteCompiler,
) (AdditivitySuiteReport, error) {
	if ctx == nil || compiler == nil {
		return AdditivitySuiteReport{}, ErrInvalidAdditivitySuite
	}
	if err := ValidateAdditivitySuiteInventory(cases); err != nil {
		return AdditivitySuiteReport{}, err
	}
	normalized := append([]AdditivitySuiteCase(nil), cases...)
	sort.Slice(normalized, func(i, j int) bool { return normalized[i].CaseID < normalized[j].CaseID })
	report := AdditivitySuiteReport{
		CaseCount: len(normalized), Threshold: AdditivitySuiteThreshold, Failures: []AdditivitySuiteFailure{},
	}
	for _, evaluationCase := range normalized {
		result, err := compiler.CompileAdditivityCase(ctx, evaluationCase)
		if err != nil {
			report.Failures = append(report.Failures, AdditivitySuiteFailure{CaseID: evaluationCase.CaseID, Code: "COMPILATION_FAILED"})
			continue
		}
		if code := validateAdditivityAST(evaluationCase, result); code != "" {
			report.Failures = append(report.Failures, AdditivitySuiteFailure{CaseID: evaluationCase.CaseID, Code: code})
			continue
		}
		report.PassedCount++
	}
	report.Accuracy = float64(report.PassedCount) / float64(report.CaseCount)
	report.GatePassed = report.Accuracy == AdditivitySuiteThreshold
	return report, nil
}

func validateAdditivityAST(evaluationCase AdditivitySuiteCase, result AdditivityASTResult) string {
	switch evaluationCase.Category {
	case AdditivityRatioGroupTotal:
		if result.CompileErrorCode != "" || result.Expression == nil || !isPostAggregateRatio(*result.Expression) {
			return "RATIO_NOT_POST_AGGREGATE"
		}
	case AdditivityDistinctGroupTotal:
		if result.CompileErrorCode != "" || result.Expression == nil ||
			!containsAggregateFunction(*result.Expression, "COUNT_DISTINCT") || containsSumOfCountDistinct(*result.Expression) {
			return "DISTINCT_REAGGREGATED"
		}
	case AdditivitySemiPeriod, AdditivitySemiTimeAndNonTime:
		if result.CompileErrorCode != "" || result.Expression == nil || result.PreAggregationCount < 1 ||
			!containsSemiAdditiveFunction(*result.Expression, evaluationCase.ExpectedFunction) {
			return "SEMI_ADDITIVE_WINDOW_MISSING"
		}
	case AdditivityMixedUnitCurrencyBlock:
		if result.Expression != nil || result.CompileErrorCode != "INCOMPATIBLE_UNIT" {
			return "MIXED_UNIT_NOT_BLOCKED"
		}
	default:
		return "UNKNOWN_ADDITIVITY_CATEGORY"
	}
	return ""
}

func isPostAggregateRatio(expression dataset.Expression) bool {
	if expression.Type == "COALESCE" && len(expression.Arguments) > 0 {
		expression = expression.Arguments[0]
	}
	if expression.Type != "DIVIDE" || len(expression.Arguments) != 2 || expression.Arguments[0].Type != "AGGREGATE" {
		return false
	}
	denominator := expression.Arguments[1]
	return denominator.Type == "NULLIF" && len(denominator.Arguments) == 2 && denominator.Arguments[0].Type == "AGGREGATE"
}

func containsAggregateFunction(expression dataset.Expression, function string) bool {
	if expression.Type == "AGGREGATE" && strings.EqualFold(expression.Function, function) {
		return true
	}
	for _, child := range expressionChildren(expression) {
		if containsAggregateFunction(child, function) {
			return true
		}
	}
	return false
}

func containsSumOfCountDistinct(expression dataset.Expression) bool {
	if expression.Type == "AGGREGATE" && strings.EqualFold(expression.Function, "SUM") && expression.Argument != nil &&
		containsAggregateFunction(*expression.Argument, "COUNT_DISTINCT") {
		return true
	}
	for _, child := range expressionChildren(expression) {
		if containsSumOfCountDistinct(child) {
			return true
		}
	}
	return false
}

func containsSemiAdditiveFunction(expression dataset.Expression, expected string) bool {
	if expression.Type == "AGGREGATE" && strings.EqualFold(expression.Function, expected) {
		switch strings.ToUpper(expected) {
		case "PERIOD_END":
			return len(expression.OrderBy) == 1 && expression.OrderBy[0].Direction == "DESC"
		case "PERIOD_BEGIN":
			return len(expression.OrderBy) == 1 && expression.OrderBy[0].Direction == "ASC"
		case "AVG":
			return true
		}
	}
	for _, child := range expressionChildren(expression) {
		if containsSemiAdditiveFunction(child, expected) {
			return true
		}
	}
	return false
}

func expressionChildren(expression dataset.Expression) []dataset.Expression {
	children := append([]dataset.Expression(nil), expression.Arguments...)
	for _, child := range []*dataset.Expression{expression.Argument, expression.Left, expression.Right, expression.Lower, expression.Upper, expression.Else} {
		if child != nil {
			children = append(children, *child)
		}
	}
	for _, branch := range expression.Whens {
		children = append(children, branch.When, branch.Then)
	}
	children = append(children, expression.PartitionBy...)
	for _, order := range expression.OrderBy {
		children = append(children, order.Expression)
	}
	return children
}
