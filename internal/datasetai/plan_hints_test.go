package datasetai

import (
	"errors"
	"strings"
	"testing"
)

func monthlyRegionalOrderCountHints() *PlanHints {
	return &PlanHints{
		PreferredTableIDs: []string{"table-orders", "table-customers"},
		Aggregation:       "COUNT",
		MeasureFields:     []PlanFieldHint{{TableID: "table-orders", Column: "ORDER_ID"}},
		TimeField:         &PlanFieldHint{TableID: "table-orders", Column: "CREATED_AT"},
		DimensionFields:   []PlanFieldHint{{TableID: "table-customers", Column: "region_code"}},
		TimeGrain:         "MONTH",
	}
}

func TestValidateCreatePlanHintsAcceptsCompleteStructuredComputation(t *testing.T) {
	proposal := normalizeProposal(monthlyRegionalOrderCountProposal(), "CREATE")
	if err := validateCreatePlanHints(proposal.Plan, monthlyRegionalOrderCountHints()); err != nil {
		t.Fatalf("validateCreatePlanHints() error = %v", err)
	}
}

func TestValidateCreatePlanHintsRejectsSilentlyDroppedGroupWithoutMatchingWording(t *testing.T) {
	detailPlan := normalizeProposal(singleTableProposal("table-orders", "ORDER_ID"), "CREATE").Plan
	for _, instruction := range []string{
		"保留每条业务记录",
		"assemble the requested business view",
		"按我的选择生成结果",
	} {
		err := validateCreatePlanHints(detailPlan, monthlyRegionalOrderCountHints())
		var invalid *InvalidOutputError
		if !errors.As(err, &invalid) || invalid.ReasonCode != InvalidOutputReasonGroup ||
			!strings.Contains(invalid.Detail, "no GROUP") {
			t.Fatalf("structured hints for %q returned %#v (%v), want GROUP rejection", instruction, invalid, err)
		}
	}
}

func TestValidateCreatePlanHintsChecksExactMetricDimensionAndTimeBindings(t *testing.T) {
	tests := []struct {
		name       string
		mutate     func(*Proposal)
		reasonCode string
		detail     string
	}{
		{
			name: "wrong aggregation",
			mutate: func(proposal *Proposal) {
				proposal.Plan.Groups[0].Metrics[0].Aggregation = "COUNT_DISTINCT"
			},
			reasonCode: InvalidOutputReasonGroup,
			detail:     "aggregation COUNT",
		},
		{
			name: "wrong measure binding",
			mutate: func(proposal *Proposal) {
				proposal.Plan.Groups[0].Metrics[0] = PlanMetric{NodeID: "node_1", Column: "CUSTOMER_ID", Aggregation: "COUNT"}
				proposal.Plan.End.Outputs[2] = PlanOutput{NodeID: "node_1", Column: "CUSTOMER_ID", Name: "客户数", Code: "customer_count"}
			},
			reasonCode: InvalidOutputReasonGroup,
			detail:     "measure fields",
		},
		{
			name: "missing hinted dimension",
			mutate: func(proposal *Proposal) {
				proposal.Plan.Groups[0].Dimensions = proposal.Plan.Groups[0].Dimensions[:1]
				proposal.Plan.End.Outputs = append(proposal.Plan.End.Outputs[:1], proposal.Plan.End.Outputs[2])
			},
			reasonCode: InvalidOutputReasonGroup,
			detail:     "dimension field",
		},
		{
			name: "wrong time grain",
			mutate: func(proposal *Proposal) {
				proposal.Plan.Transforms[0].Rules[0].Unit = "YEAR"
			},
			reasonCode: InvalidOutputReasonTransform,
			detail:     "time grain MONTH",
		},
		{
			name: "time output not exposed",
			mutate: func(proposal *Proposal) {
				proposal.Plan.End.Outputs = proposal.Plan.End.Outputs[1:]
			},
			reasonCode: InvalidOutputReasonTransform,
			detail:     "time grain MONTH",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			proposal := monthlyRegionalOrderCountProposal()
			test.mutate(&proposal)
			proposal = normalizeProposal(proposal, "CREATE")
			err := validateCreatePlanHints(proposal.Plan, monthlyRegionalOrderCountHints())
			var invalid *InvalidOutputError
			if !errors.As(err, &invalid) || invalid.ReasonCode != test.reasonCode || !strings.Contains(invalid.Detail, test.detail) {
				t.Fatalf("validateCreatePlanHints() = %#v (%v), want %s containing %q", invalid, err, test.reasonCode, test.detail)
			}
		})
	}
}

func TestValidateCreatePlanHintsKeepsUnspecifiedHintsOptional(t *testing.T) {
	plan := normalizeProposal(singleTableProposal("table-orders", "ORDER_ID"), "CREATE").Plan
	for _, hints := range []*PlanHints{
		nil,
		{PreferredTableIDs: []string{"table-orders"}},
		{MeasureFields: []PlanFieldHint{{TableID: "table-orders", Column: "ORDER_ID"}}},
	} {
		if err := validateCreatePlanHints(plan, hints); err != nil {
			t.Fatalf("optional hints %#v invented a GROUP requirement: %v", hints, err)
		}
	}
}
