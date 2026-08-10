package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/evaluation"
	"intelligent-report-generation-system/internal/askdata/testfixture"
)

func TestRunBuiltInSyntheticFixture(t *testing.T) {
	var stdout, stderr bytes.Buffer
	exitCode := run(context.Background(), []string{"-pretty=false"}, &stdout, &stderr)
	if exitCode != 0 || stderr.Len() != 0 {
		t.Fatalf("run exit=%d stderr=%q", exitCode, stderr.String())
	}
	var report evaluation.FixtureRegressionReport
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatalf("decode report: %v", err)
	}
	if report.SchemaVersion != evaluation.FixtureRegressionVersion ||
		report.TotalCases != 6 || report.PassedCases != 6 || report.FailedCases != 0 ||
		report.Validate() != nil {
		t.Fatalf("report = %#v", report)
	}
	if strings.Contains(stdout.String(), "今年华东区按月的销售额") ||
		strings.Contains(stdout.String(), "1200.00") {
		t.Fatalf("CLI report leaked question or result rows: %s", stdout.String())
	}
}

func TestRunReturnsRegressionExitCodeWithMachineReadableReport(t *testing.T) {
	fixture := testfixture.Standard()
	for index := range fixture.Questions {
		if fixture.Questions[index].QuestionID == "question-member-ambiguous" {
			fixture.Questions[index].ExpectedReasonCode = "DIFFERENT_GOLD_REASON"
		}
	}
	path := writeFixture(t, fixture)
	var stdout, stderr bytes.Buffer
	exitCode := run(context.Background(), []string{"-fixture", path, "-pretty=false"}, &stdout, &stderr)
	if exitCode != 1 || stderr.Len() != 0 {
		t.Fatalf("run exit=%d stderr=%q", exitCode, stderr.String())
	}
	var report evaluation.FixtureRegressionReport
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatalf("decode report: %v", err)
	}
	if report.FailedCases != 1 || report.PassedCases != 5 {
		t.Fatalf("report counts = %#v", report)
	}
}

func TestRunRejectsUnknownOrNonSyntheticFixture(t *testing.T) {
	path := filepath.Join(t.TempDir(), "invalid.json")
	if err := os.WriteFile(path, []byte(`{"fixtureVersion":"askdata-synthetic-v1","synthetic":false,"unknown":true}`), 0o600); err != nil {
		t.Fatalf("write invalid fixture: %v", err)
	}
	var stdout, stderr bytes.Buffer
	if exitCode := run(context.Background(), []string{"-fixture", path}, &stdout, &stderr); exitCode != 2 {
		t.Fatalf("run exit=%d stdout=%q stderr=%q", exitCode, stdout.String(), stderr.String())
	}
	if stdout.Len() != 0 || !strings.Contains(stderr.String(), "load fixture") {
		t.Fatalf("stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestRunRetrievalRecallSupportsANNExactAndNeverEmitsQuestionText(t *testing.T) {
	caseSet := evaluation.RetrievalCaseSet{
		SchemaVersion: evaluation.RetrievalEvaluationVersion,
		Cases: []evaluation.RetrievalEvaluationCase{{
			SchemaVersion: evaluation.RetrievalEvaluationVersion,
			CaseID:        "case-secret", DomainID: "sales", Complexity: evaluation.ComplexitySimple,
			Sensitive: true,
			Gold: evaluation.RetrievalGold{
				MetricVersionIDs:    []askdata.ID{"metric-sales@v1"},
				DimensionVersionIDs: []askdata.ID{"dimension-region@v1"},
				MemberVersionIDs:    []askdata.ID{"member-east@v1"},
			},
			ANN: []evaluation.RetrievalCandidate{},
			Exact: []evaluation.RetrievalCandidate{
				{ObjectType: evaluation.RetrievalMetric, VersionID: "metric-sales@v1", Rank: 1},
				{ObjectType: evaluation.RetrievalDimension, VersionID: "dimension-region@v1", Rank: 1},
				{ObjectType: evaluation.RetrievalMember, VersionID: "member-east@v1", Rank: 1},
			},
		}},
	}
	raw, err := json.Marshal(caseSet)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "retrieval.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	var annOut, annErr bytes.Buffer
	if exit := run(context.Background(), []string{
		"-retrieval-cases", path, "-retrieval-mode", "ANN", "-pretty=false",
	}, &annOut, &annErr); exit != 1 || annErr.Len() != 0 {
		t.Fatalf("ANN exit=%d stdout=%s stderr=%s", exit, annOut.String(), annErr.String())
	}
	var exactOut, exactErr bytes.Buffer
	if exit := run(context.Background(), []string{
		"-retrieval-cases", path, "-retrieval-mode", "EXACT", "-pretty=false",
	}, &exactOut, &exactErr); exit != 0 || exactErr.Len() != 0 {
		t.Fatalf("EXACT exit=%d stdout=%s stderr=%s", exit, exactOut.String(), exactErr.String())
	}
	if strings.Contains(exactOut.String(), "question") || strings.Contains(annOut.String(), "customer secret") {
		t.Fatalf("retrieval output leaked question text: %s / %s", annOut.String(), exactOut.String())
	}
}

func writeFixture(t *testing.T, fixture testfixture.Set) string {
	t.Helper()
	raw, err := json.Marshal(fixture)
	if err != nil {
		t.Fatalf("marshal fixture: %v", err)
	}
	path := filepath.Join(t.TempDir(), "fixture.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return path
}
