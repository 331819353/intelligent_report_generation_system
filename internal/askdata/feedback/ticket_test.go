package feedback

import (
	"testing"
	"time"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/orchestrator"
)

func TestTicketLifecycleAndIllegalTransitions(t *testing.T) {
	id := askdata.ID("11111111-1111-4111-8111-111111111111")
	ticket := Ticket{Status: StatusNew, Severity: SeverityP1, SuggestedStage: StageBinding, RecordVersion: 1}
	steps := []TransitionInput{
		{ExpectedVersion: 1, TargetStatus: StatusTriaged, OwnerUserID: id, AttributedStage: StageBinding},
		{ExpectedVersion: 2, TargetStatus: StatusAccepted},
		{ExpectedVersion: 3, TargetStatus: StatusFixProposed, FixCandidateType: "METRIC", FixCandidateID: id},
		{ExpectedVersion: 4, TargetStatus: StatusFixApproved},
		{ExpectedVersion: 5, TargetStatus: StatusInRelease, LinkedReleaseID: id},
		{ExpectedVersion: 6, TargetStatus: StatusVerified, LinkedEvaluationCaseID: id},
		{ExpectedVersion: 7, TargetStatus: StatusClosed},
	}
	for _, step := range steps {
		if err := step.Validate(ticket); err != nil {
			t.Fatalf("%s -> %s: %v", ticket.Status, step.TargetStatus, err)
		}
		ticket.Status, ticket.RecordVersion = step.TargetStatus, ticket.RecordVersion+1
		if step.OwnerUserID != "" {
			ticket.OwnerUserID = step.OwnerUserID
		}
		if step.FixCandidateID != "" {
			ticket.FixCandidateID = step.FixCandidateID
			ticket.FixCandidateType = step.FixCandidateType
		}
		if step.LinkedReleaseID != "" {
			ticket.LinkedReleaseID = step.LinkedReleaseID
		}
		if step.LinkedEvaluationCaseID != "" {
			ticket.LinkedEvaluationCaseID = step.LinkedEvaluationCaseID
		}
	}
	for _, invalid := range [][2]Status{{StatusNew, StatusAccepted}, {StatusNew, StatusRejected}, {StatusTriaged, StatusClosed}, {StatusAccepted, StatusVerified}, {StatusFixProposed, StatusInRelease}, {StatusClosed, StatusNew}} {
		if CanTransition(invalid[0], invalid[1]) {
			t.Fatalf("illegal transition accepted: %v", invalid)
		}
	}
}

func TestRejectedTicketRequiresExplanationAndResponse(t *testing.T) {
	ticket := Ticket{Status: StatusTriaged, RecordVersion: 2}
	input := TransitionInput{ExpectedVersion: 2, TargetStatus: StatusRejected, ResolutionNote: "duplicate"}
	if input.Validate(ticket) == nil {
		t.Fatal("rejection without user response accepted")
	}
	input.UserResponse = "This was a duplicate."
	if err := input.Validate(ticket); err != nil {
		t.Fatal(err)
	}
}

func TestSLAAndClosureRate(t *testing.T) {
	friday := time.Date(2026, 8, 7, 10, 0, 0, 0, time.UTC)
	p0, _ := SLADueAt(friday, SeverityP0, nil)
	if p0.Sub(friday) != 4*time.Hour {
		t.Fatalf("p0=%v", p0)
	}
	p1, _ := SLADueAt(friday, SeverityP1, nil)
	if p1.Weekday() != time.Monday {
		t.Fatalf("p1=%v", p1)
	}
	p2, _ := SLADueAt(friday, SeverityP2, nil)
	if p2.Weekday() != time.Wednesday {
		t.Fatalf("p2=%v", p2)
	}
	rate := ClosureRate([]Status{StatusClosed, StatusClosed, StatusRejected, StatusAccepted})
	if rate != 2.0/3.0 {
		t.Fatalf("rate=%v", rate)
	}
}

func TestAttributionRetainsTypedArtifactEvidence(t *testing.T) {
	snapshot := orchestrator.ReplaySnapshot{Artifacts: []orchestrator.Artifact{{Type: orchestrator.ArtifactCandidateSet}, {Type: orchestrator.ArtifactSemanticIR}, {Type: orchestrator.ArtifactResultVerification}}}
	cases := map[IssueType]Stage{IssueMember: StageBinding, IssueTime: StageCompile, IssueResult: StageData, IssueNarrative: StageNarrative, IssuePermission: StageUnderstanding}
	for issue, want := range cases {
		if got := SuggestStage(issue, snapshot); got != want {
			t.Fatalf("%s=%s want %s", issue, got, want)
		}
	}
}
