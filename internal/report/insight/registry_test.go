package insight

import (
	"math"
	"regexp"
	"testing"
)

func TestRegistryFreezesElevenVersionedMethodsAndInputContracts(t *testing.T) {
	methods := NewRegistry().List()
	if len(methods) != 11 {
		t.Fatalf("method count = %d; want 11", len(methods))
	}
	semver := regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)
	seen := map[AnalysisMethod]struct{}{}
	for _, method := range methods {
		if _, duplicate := seen[method.ID()]; duplicate {
			t.Fatalf("duplicate method %s", method.ID())
		}
		seen[method.ID()] = struct{}{}
		contract := method.InputContract()
		if !semver.MatchString(method.Version()) || contract.MinimumRows < 1 || len(contract.RequiredRoles) == 0 {
			t.Fatalf("method %s metadata = version %q contract %#v", method.ID(), method.Version(), contract)
		}
	}
}

func TestAllAnalysisMethodsCoverNormalAndBoundaryInputs(t *testing.T) {
	previousA, previousB, previousC := 8.0, 22.0, 30.0
	targetA, targetB, targetC := 20.0, 20.0, 0.0
	base := []NumericValue{
		{Key: "a", Group: "g1", Value: 10, Previous: &previousA, Target: &targetA},
		{Key: "b", Group: "g1", Value: 20, Previous: &previousB, Target: &targetB},
		{Key: "c", Group: "g2", Value: 30, Previous: &previousC, Target: &targetC},
	}
	equal := []NumericValue{{Key: "a", Group: "g1", Value: 5}, {Key: "b", Group: "g2", Value: 5}, {Key: "c", Group: "g2", Value: 5}}
	allMissing := []NumericValue{{Key: "a", Group: "g1", Value: 0, Missing: true}, {Key: "b", Group: "g2", Value: 0, Missing: true}}

	tests := []struct {
		id       AnalysisMethod
		normal   MethodInput
		boundary MethodInput
		check    func(*testing.T, MethodResult, MethodResult)
	}{
		{AnalysisCurrentValue, MethodInput{Values: base}, MethodInput{Values: allMissing}, expectBoundaryError},
		{AnalysisPeriodComparison, MethodInput{Values: base}, MethodInput{Values: []NumericValue{{Key: "a", Value: 1}}}, expectBoundaryError},
		{AnalysisTrend, MethodInput{Values: base}, MethodInput{Values: equal}, func(t *testing.T, _, boundary MethodResult) {
			if boundary.Facts[0].Values["slope"] != 0 || boundary.Facts[0].Strings["direction"] != "FLAT" {
				t.Fatalf("flat trend = %#v", boundary)
			}
		}},
		{AnalysisAnomalyPoint, MethodInput{Values: base, Threshold: 1}, MethodInput{Values: []NumericValue{{Key: "a", Value: 1}}}, func(t *testing.T, _, boundary MethodResult) {
			if len(boundary.Facts) != 1 || len(boundary.Warnings) != 1 {
				t.Fatalf("single point anomaly = %#v", boundary)
			}
		}},
		{AnalysisTopN, MethodInput{Values: base, TopN: 2}, MethodInput{Values: equal, TopN: 2}, expectFacts},
		{AnalysisContribution, MethodInput{Values: base}, MethodInput{Values: []NumericValue{{Key: "a", Value: 0}}}, expectFacts},
		{AnalysisMaxChange, MethodInput{Values: base}, MethodInput{Values: equal}, expectBoundaryError},
		{AnalysisTargetAchievement, MethodInput{Values: base}, MethodInput{Values: []NumericValue{{Key: "a", Value: 2, Target: &targetC}}}, expectFacts},
		{AnalysisGroupDifference, MethodInput{Values: base}, MethodInput{Values: equal}, func(t *testing.T, _, boundary MethodResult) {
			if boundary.Facts[len(boundary.Facts)-1].Values["spread"] != 0 {
				t.Fatalf("equal group spread = %#v", boundary)
			}
		}},
		{AnalysisShareOfTotal, MethodInput{Values: base}, MethodInput{Values: []NumericValue{{Key: "a", Value: 0}, {Key: "b", Value: 0}}}, expectFacts},
		{AnalysisDataCompleteness, MethodInput{Values: base}, MethodInput{Values: allMissing}, func(t *testing.T, _, boundary MethodResult) {
			if math.Abs(boundary.Facts[0].Values["missingRatio"]-1) > 1e-12 {
				t.Fatalf("all-missing completeness = %#v", boundary)
			}
		}},
	}

	registry := NewRegistry()
	for _, test := range tests {
		t.Run(string(test.id), func(t *testing.T) {
			method, exists := registry.Get(test.id)
			if !exists {
				t.Fatal("method missing")
			}
			normal, err := method.Analyze(test.normal)
			if err != nil || len(normal.Facts) == 0 {
				t.Fatalf("normal result = %#v, %v", normal, err)
			}
			boundary, boundaryErr := method.Analyze(test.boundary)
			if test.check == nil {
				return
			}
			if test.id == AnalysisCurrentValue || test.id == AnalysisPeriodComparison || test.id == AnalysisMaxChange {
				if boundaryErr == nil {
					t.Fatalf("boundary unexpectedly passed: %#v", boundary)
				}
				return
			}
			if boundaryErr != nil {
				t.Fatalf("boundary error = %v", boundaryErr)
			}
			test.check(t, normal, boundary)
		})
	}
}

func expectBoundaryError(*testing.T, MethodResult, MethodResult) {}

func expectFacts(t *testing.T, _ MethodResult, boundary MethodResult) {
	if len(boundary.Facts) == 0 {
		t.Fatal("boundary result has no facts")
	}
}
