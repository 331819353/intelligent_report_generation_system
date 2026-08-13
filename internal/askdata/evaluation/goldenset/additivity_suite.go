package goldenset

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/compiler"
	"intelligent-report-generation-system/internal/askdata/evaluation/suites"
	"intelligent-report-generation-system/internal/askdata/ir"
)

// AdditivitySuiteVersion identifies the inventory. It must change whenever a
// case, a contract or the synthetic model changes.
const AdditivitySuiteVersion = "askdata-additivity-golden-v1"

var ErrAdditivityGoldenSet = errors.New("additivity golden set is invalid")

type additivityScenario struct {
	public          suites.AdditivitySuiteCase
	query           ir.SemanticIR
	resolution      compiler.Resolution
	metricVersionID askdata.ID
}

// AdditivitySuite is the inventory and the suites.AdditivitySuiteCompiler for
// it. Every case compiles through compiler.InspectAdditivity, which runs the
// production compile stage rather than a restatement of its rules.
type AdditivitySuite struct {
	scenarios []additivityScenario
	byID      map[askdata.ID]additivityScenario
}

func NewAdditivitySuite() (*AdditivitySuite, error) {
	scenarios, err := buildAdditivityScenarios()
	if err != nil {
		return nil, err
	}
	sort.Slice(scenarios, func(left, right int) bool {
		return scenarios[left].public.CaseID < scenarios[right].public.CaseID
	})
	suite := &AdditivitySuite{scenarios: scenarios, byID: make(map[askdata.ID]additivityScenario, len(scenarios))}
	for _, scenario := range scenarios {
		if _, duplicate := suite.byID[scenario.public.CaseID]; duplicate {
			return nil, fmt.Errorf("%w: duplicate case %s", ErrAdditivityGoldenSet, scenario.public.CaseID)
		}
		suite.byID[scenario.public.CaseID] = scenario
	}
	if err := suites.ValidateAdditivitySuiteInventory(suite.Cases()); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrAdditivityGoldenSet, err)
	}
	return suite, nil
}

func (suite *AdditivitySuite) Cases() []suites.AdditivitySuiteCase {
	cases := make([]suites.AdditivitySuiteCase, 0, len(suite.scenarios))
	for _, scenario := range suite.scenarios {
		cases = append(cases, scenario.public)
	}
	return cases
}

func (suite *AdditivitySuite) CompileAdditivityCase(
	ctx context.Context,
	evaluationCase suites.AdditivitySuiteCase,
) (suites.AdditivityASTResult, error) {
	if err := ctx.Err(); err != nil {
		return suites.AdditivityASTResult{}, err
	}
	scenario, known := suite.byID[evaluationCase.CaseID]
	if !known {
		return suites.AdditivityASTResult{}, fmt.Errorf(
			"%w: unknown case %s", ErrAdditivityGoldenSet, evaluationCase.CaseID,
		)
	}
	if evaluationCase.ContractHash != scenario.public.ContractHash ||
		evaluationCase.Category != scenario.public.Category ||
		evaluationCase.ExpectedFunction != scenario.public.ExpectedFunction {
		return suites.AdditivityASTResult{}, fmt.Errorf(
			"%w: case %s was substituted", ErrAdditivityGoldenSet, evaluationCase.CaseID,
		)
	}
	shape, err := compiler.InspectAdditivity(scenario.query, scenario.resolution, scenario.metricVersionID)
	if err != nil {
		return suites.AdditivityASTResult{}, err
	}
	return suites.AdditivityASTResult{
		Expression:          shape.Expression,
		PreAggregationCount: shape.PreAggregationCount,
		CompileErrorCode:    shape.CompileErrorCode,
	}, nil
}

func (suite *AdditivitySuite) Run(ctx context.Context) (suites.AdditivitySuiteReport, error) {
	return suites.EvaluateAdditivitySuite(ctx, suite.Cases(), suite)
}
