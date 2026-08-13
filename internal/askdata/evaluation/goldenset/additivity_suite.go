package goldenset

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/compiler"
	"intelligent-report-generation-system/internal/askdata/evaluation/suites"
	"intelligent-report-generation-system/internal/platform/database"
)

// AdditivitySuiteVersion identifies the inventory. It must change whenever a
// case, a contract or the synthetic model changes.
const AdditivitySuiteVersion = "askdata-additivity-golden-v2"

var ErrAdditivityGoldenSet = errors.New("additivity golden set is invalid")

// additivityScenario pairs the public case with the private inputs that produce
// it: which question shape it compiles through, and the governed contracts the
// release resolves to. The contracts vary per case; the shape does not, which is
// why one chain serves many cases.
type additivityScenario struct {
	public       suites.AdditivitySuiteCase
	shape        string
	metrics      []compiler.MetricContract
	dimensionIDs []askdata.ID
	subject      askdata.ID
}

// AdditivitySuite is the inventory and the suites.AdditivitySuiteCompiler for
// it. Every case travels the production chain — understanding, binding, Semantic
// IR, contract resolution and Adapt — so a regression anywhere along it fails
// here rather than only in production.
type AdditivitySuite struct {
	scenarios []additivityScenario
	byID      map[askdata.ID]additivityScenario
	chains    map[string]chain
}

func NewAdditivitySuite() (*AdditivitySuite, error) {
	chains, err := buildAdditivityChains()
	if err != nil {
		return nil, err
	}
	scenarios, err := buildAdditivityScenarios()
	if err != nil {
		return nil, err
	}
	sort.Slice(scenarios, func(left, right int) bool {
		return scenarios[left].public.CaseID < scenarios[right].public.CaseID
	})
	suite := &AdditivitySuite{
		scenarios: scenarios, chains: chains,
		byID: make(map[askdata.ID]additivityScenario, len(scenarios)),
	}
	for _, scenario := range scenarios {
		if _, duplicate := suite.byID[scenario.public.CaseID]; duplicate {
			return nil, fmt.Errorf("%w: duplicate case %s", ErrAdditivityGoldenSet, scenario.public.CaseID)
		}
		if _, known := chains[scenario.shape]; !known {
			return nil, fmt.Errorf(
				"%w: case %s names unknown shape %s", ErrAdditivityGoldenSet, scenario.public.CaseID, scenario.shape,
			)
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

// CompileAdditivityCase resolves the release-pinned contracts and compiles the
// query. A governed aggregation refusal — mixed units, a re-aggregated distinct,
// a semi-additive metric with no time window — is returned as a code rather than
// an error, because refusing is precisely the behaviour under test.
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
	activeChain := suite.chains[scenario.shape]
	store, err := newSyntheticContractStore(scenario.metrics, scenario.dimensionIDs)
	if err != nil {
		return suites.AdditivityASTResult{}, err
	}
	resolver, err := compiler.NewResolver(store)
	if err != nil {
		return suites.AdditivityASTResult{}, err
	}
	// The reading actor and domain are the same identity the scope was built
	// with; Resolve refuses a mismatch, which is the check that keeps a
	// resolution bound to the reader it was authorized for.
	resolveContext := database.WithAccessContext(
		ctx, string(activeChain.scope.ActorID), string(goldenDomainID),
	)
	resolution, err := resolver.Resolve(resolveContext, activeChain.resolveRequest)
	if err != nil {
		return suites.AdditivityASTResult{}, fmt.Errorf(
			"%w: case %s resolve: %v", ErrAdditivityGoldenSet, evaluationCase.CaseID, err,
		)
	}
	artifact, err := compiler.Adapt(compiler.AdaptRequest{
		ResolveRequest: activeChain.resolveRequest, Resolution: resolution,
	})
	if err != nil {
		var planError *compiler.AggregationPlanError
		if errors.As(err, &planError) {
			return suites.AdditivityASTResult{CompileErrorCode: planError.Code}, nil
		}
		return suites.AdditivityASTResult{}, fmt.Errorf(
			"%w: case %s adapt: %v", ErrAdditivityGoldenSet, evaluationCase.CaseID, err,
		)
	}
	return additivityShape(artifact, scenario.subject)
}

// additivityShape projects the compiled plan down to what the suite asserts on.
// It reads the alias the IR builder assigned rather than recomputing it: a
// fixture that derived the alias itself would keep passing after the builder
// changed the rule.
func additivityShape(
	artifact compiler.QueryArtifact,
	metricVersionID askdata.ID,
) (suites.AdditivityASTResult, error) {
	if len(artifact.Plans) == 0 {
		return suites.AdditivityASTResult{}, fmt.Errorf("%w: no compiled plan", ErrAdditivityGoldenSet)
	}
	alias := ""
	for _, aggregation := range artifact.MetricAggregations {
		if aggregation.MetricVersionID == metricVersionID {
			alias = aggregation.ResultColumnName
		}
	}
	if alias == "" {
		return suites.AdditivityASTResult{}, fmt.Errorf(
			"%w: metric %s is not selected by this query", ErrAdditivityGoldenSet, metricVersionID,
		)
	}
	document := artifact.Plans[0].Document
	result := suites.AdditivityASTResult{}
	for _, preAggregation := range document.PreAggregations {
		result.PreAggregationCount += len(preAggregation.Metrics)
	}
	for _, field := range document.Fields {
		if field.Code != alias || field.Role != "MEASURE" {
			continue
		}
		expression := field.Expression
		result.Expression = &expression
		return result, nil
	}
	return suites.AdditivityASTResult{}, fmt.Errorf(
		"%w: metric %s produced no output measure", ErrAdditivityGoldenSet, metricVersionID,
	)
}

func (suite *AdditivitySuite) Run(ctx context.Context) (suites.AdditivitySuiteReport, error) {
	return suites.EvaluateAdditivitySuite(ctx, suite.Cases(), suite)
}
