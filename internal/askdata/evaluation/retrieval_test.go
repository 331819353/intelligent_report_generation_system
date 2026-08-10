package evaluation

import (
	"encoding/json"
	"strings"
	"testing"

	"intelligent-report-generation-system/internal/askdata"
)

func TestEvaluateRetrievalRecallModesAndSafeFailures(t *testing.T) {
	cases := retrievalCases()
	ann, err := EvaluateRetrievalRecall(cases, RetrievalModeANN)
	if err != nil {
		t.Fatalf("ANN evaluation: %v", err)
	}
	if ann.Passed || ann.Overall.Metric.Recall != .5 || ann.Overall.Dimension.Recall != 1 ||
		ann.Overall.Member.Recall != 1 || len(ann.Failures) != 1 ||
		ann.Failures[0].CaseID != "case-sensitive" || ann.Failures[0].ObjectType != RetrievalMetric {
		t.Fatalf("ANN report = %+v", ann)
	}
	raw, err := json.Marshal(ann)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "customer secret") {
		t.Fatalf("report leaked question text: %s", raw)
	}
	exact, err := EvaluateRetrievalRecall(cases, RetrievalModeExact)
	if err != nil {
		t.Fatalf("exact evaluation: %v", err)
	}
	if !exact.Passed || exact.Overall.Metric.Recall != 1 || len(exact.Failures) != 0 || exact.SourceHash != ann.SourceHash {
		t.Fatalf("exact report = %+v", exact)
	}
}

func TestEvaluateRetrievalRecallRejectsInvalidAndIsStable(t *testing.T) {
	cases := retrievalCases()
	reversed := []RetrievalEvaluationCase{cases[1], cases[0]}
	left, err := EvaluateRetrievalRecall(cases, RetrievalModeExact)
	if err != nil {
		t.Fatal(err)
	}
	right, err := EvaluateRetrievalRecall(reversed, RetrievalModeExact)
	if err != nil {
		t.Fatal(err)
	}
	if left.SourceHash != right.SourceHash {
		t.Fatalf("source hash is order-sensitive: %s != %s", left.SourceHash, right.SourceHash)
	}

	invalid := retrievalCases()
	invalid[0].ANN = append(invalid[0].ANN, invalid[0].ANN[0])
	if _, err := EvaluateRetrievalRecall(invalid, RetrievalModeANN); err == nil {
		t.Fatal("duplicate candidate was accepted")
	}
	if _, err := EvaluateRetrievalRecall(cases, "HYBRID"); err == nil {
		t.Fatal("unknown mode was accepted")
	}
}

func retrievalCases() []RetrievalEvaluationCase {
	metricA, metricB := askdata.ID("metric-a@v1"), askdata.ID("metric-b@v1")
	dimension, member := askdata.ID("dimension-region@v1"), askdata.ID("member-east@v1")
	all := func(metrics []askdata.ID) []RetrievalCandidate {
		result := []RetrievalCandidate{
			{ObjectType: RetrievalDimension, VersionID: dimension, Rank: 1},
			{ObjectType: RetrievalMember, VersionID: member, Rank: 1},
		}
		for index, id := range metrics {
			result = append(result, RetrievalCandidate{ObjectType: RetrievalMetric, VersionID: id, Rank: index + 1})
		}
		return result
	}
	return []RetrievalEvaluationCase{
		{
			SchemaVersion: RetrievalEvaluationVersion, CaseID: "case-basic", DomainID: "sales",
			Complexity: ComplexitySimple,
			Gold:       RetrievalGold{MetricVersionIDs: []askdata.ID{metricA}, DimensionVersionIDs: []askdata.ID{dimension}, MemberVersionIDs: []askdata.ID{member}},
			ANN:        all([]askdata.ID{metricA}), Exact: all([]askdata.ID{metricA}),
		},
		{
			SchemaVersion: RetrievalEvaluationVersion, CaseID: "case-sensitive", DomainID: "sales",
			Complexity: ComplexityComposite, Sensitive: true,
			Gold: RetrievalGold{MetricVersionIDs: []askdata.ID{metricB}},
			ANN:  []RetrievalCandidate{}, Exact: []RetrievalCandidate{{ObjectType: RetrievalMetric, VersionID: metricB, Rank: 1}},
		},
	}
}
