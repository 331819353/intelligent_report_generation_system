package reportasset

import (
	"strings"
	"testing"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/ircontract"
	"intelligent-report-generation-system/internal/askdata/registry"
	"intelligent-report-generation-system/internal/report"
)

func TestValidateEnforcesEveryAdmissionCondition(t *testing.T) {
	base := validCandidate(t)
	tests := []struct {
		name string
		edit func(*Candidate)
		code RejectionCode
	}{
		{"semantic binding", func(value *Candidate) { value.BindingMode = report.BindingDatasetField }, CodeSemanticBindingRequired},
		{"certification", func(value *Candidate) { value.Certifications = nil }, CodeObjectNotCertified},
		{"published immutable version", func(value *Candidate) { value.ReportPublished = false }, CodeVersionNotPublished},
		{"one current approval", func(value *Candidate) { value.Approvals = nil }, CodeApprovalRequired},
		{"free text", func(value *Candidate) { value.ContainsUncertifiedFreeText = true }, CodeFreeTextUncertified},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := base
			candidate.Certifications = append([]ObjectCertification(nil), base.Certifications...)
			candidate.Approvals = append([]Approval(nil), base.Approvals...)
			test.edit(&candidate)
			validation := Validate(candidate)
			if validation.Eligible || !hasCode(validation, test.code) {
				t.Fatalf("validation=%+v, want rejection %s", validation, test.code)
			}
		})
	}
}

func TestBuildProjectionUsesOneApprovalAndNeverIndexesNarrativeText(t *testing.T) {
	candidate := validCandidate(t)
	candidate.ReportTitle = "经营日报"
	candidate.NarrativeRole = "period-comparison"
	projection, validation, err := BuildProjection(candidate)
	if err != nil || !validation.Eligible {
		t.Fatalf("BuildProjection() error=%v validation=%+v", err, validation)
	}
	if len(projection.Vertices) != 2 || len(projection.Edges) != 6 {
		t.Fatalf("projection sizes=%d/%d", len(projection.Vertices), len(projection.Edges))
	}
	if strings.Contains(projection.SearchDocument.Text, "销售额暴涨了 99%") {
		t.Fatal("historical narrative leaked into the search document")
	}
	if !strings.Contains(projection.SearchDocument.Text, "经营日报") {
		t.Fatal("governed report context is absent")
	}
	for _, vertex := range projection.Vertices {
		if vertex.ReleaseID != candidate.SemanticIR.SemanticReleaseID ||
			vertex.ReleaseHash != candidate.SemanticIR.SemanticContentHash {
			t.Fatalf("vertex is not pinned to the semantic release: %+v", vertex)
		}
	}
	for _, edge := range projection.Edges {
		if edge.ReleaseID != candidate.SemanticIR.SemanticReleaseID ||
			edge.ReleaseHash != candidate.SemanticIR.SemanticContentHash {
			t.Fatalf("edge is not pinned to the semantic release: %+v", edge)
		}
	}
}

func TestValidateAcceptsExactlyOneCurrentApproval(t *testing.T) {
	candidate := validCandidate(t)
	candidate.Approvals = []Approval{{
		ApproverUserID: candidate.Approvals[0].ApproverUserID,
		Role:           "SEMANTIC_OWNER",
		ContentHash:    candidate.ComponentContentHash,
	}}
	validation := Validate(candidate)
	if !validation.Eligible || len(validation.Rejections) != 0 {
		t.Fatalf("one current semantic-owner approval must be sufficient: %+v", validation)
	}
}

func TestDatasetFieldProducesNoProjection(t *testing.T) {
	candidate := validCandidate(t)
	candidate.BindingMode = report.BindingDatasetField
	projection, validation, err := BuildProjection(candidate)
	if err == nil || validation.Eligible || len(projection.Vertices) != 0 || len(projection.Edges) != 0 || projection.SearchDocument.Text != "" {
		t.Fatalf("dataset field must yield zero projection: %+v %+v %v", projection, validation, err)
	}
}

func hasCode(validation Validation, code RejectionCode) bool {
	for _, rejection := range validation.Rejections {
		if rejection.Code == code {
			return true
		}
	}
	return false
}

func validCandidate(t *testing.T) Candidate {
	t.Helper()
	id := func(value string) askdata.ID { return askdata.ID(value) }
	hash := askdata.HashBytes([]byte("report-asset"))
	ir := ircontract.SemanticIR{
		IRVersion:         ircontract.Version,
		SemanticReleaseID: id("00000000-0000-4000-8000-000000000007"), SemanticContentHash: askdata.HashBytes([]byte("release")),
		DomainID: id("00000000-0000-4000-8000-000000000003"), ModelVersionID: id("00000000-0000-4000-8000-000000000010"),
		Metrics: []ircontract.Metric{{MetricVersionID: id("00000000-0000-4000-8000-000000000011"), Alias: "sales"}},
		GroupBy: []ircontract.GroupBy{{DimensionVersionID: id("00000000-0000-4000-8000-000000000012")}},
		Filters: []ircontract.Filter{{DimensionVersionID: id("00000000-0000-4000-8000-000000000013"), Operator: ircontract.FilterIn, MemberVersionIDs: []askdata.ID{id("00000000-0000-4000-8000-000000000014")}}},
		Sort:    []ircontract.Sort{}, Limit: 10, OtherPolicy: ircontract.OtherNone, TieBreaking: ircontract.TieIncludeAll,
	}
	ir, _, irHash, err := ircontract.Canonicalize(ir)
	if err != nil {
		t.Fatal(err)
	}
	certified := func(kind ObjectKind, value string) ObjectCertification {
		return ObjectCertification{Kind: kind, VersionID: id(value), Status: registry.VersionStatusCertified}
	}
	return Candidate{
		ID: id("00000000-0000-4000-8000-000000000001"), TenantID: id("00000000-0000-4000-8000-000000000002"),
		DomainID: ir.DomainID, ReportID: id("00000000-0000-4000-8000-000000000004"),
		ReportVersionID: id("00000000-0000-4000-8000-000000000005"), ComponentID: id("00000000-0000-4000-8000-000000000006"),
		BindingMode: report.BindingSemanticIR, SemanticIR: ir, SemanticIRHash: irHash,
		QueryPlanHash: askdata.HashBytes([]byte("query-plan")), ReportPublished: true, ReportVersionImmutable: true,
		Certifications: []ObjectCertification{
			certified(ObjectMetric, "00000000-0000-4000-8000-000000000011"),
			certified(ObjectDimension, "00000000-0000-4000-8000-000000000012"),
			certified(ObjectDimension, "00000000-0000-4000-8000-000000000013"),
			certified(ObjectMember, "00000000-0000-4000-8000-000000000014"),
		},
		Approvals:            []Approval{{ApproverUserID: id("00000000-0000-4000-8000-000000000015"), Role: "REPORT_OWNER", ContentHash: hash}},
		ComponentContentHash: hash, ComponentType: "line-trend", ComponentVersion: "1.2.0",
		ReportDescription: "固定语义经营概览", SectionPurpose: "观察趋势", BlockTitle: "销售额趋势",
		Sensitivity: registry.SensitivityInternal,
	}
}
