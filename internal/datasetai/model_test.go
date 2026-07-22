package datasetai

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func testCatalog() []CatalogTable {
	return []CatalogTable{
		{ID: "table-customers", Columns: []CatalogColumn{
			{Name: "customer_id", CanonicalType: "STRING"},
			{Name: "region", CanonicalType: "STRING"},
		}},
		{ID: "table-orders", Columns: []CatalogColumn{
			{Name: "customer_id", CanonicalType: "STRING"},
			{Name: "amount", CanonicalType: "DECIMAL"},
		}},
	}
}

func testProposal() Proposal {
	return Proposal{
		SchemaVersion: SchemaVersion,
		Mode:          "CREATE",
		Summary:       "按地区汇总客户订单金额",
		Assumptions:   []string{},
		Warnings:      []string{},
		Plan: GraphPlan{
			Dataset: PlanDataset{Name: "客户订单汇总", Description: "按地区统计订单金额"},
			Nodes: []PlanNode{
				{ID: "node_1", TableID: "table-customers", Alias: "customers", SelectedColumns: []string{"customer_id", "region"}},
				{ID: "node_2", TableID: "table-orders", Alias: "orders", SelectedColumns: []string{"customer_id", "amount"}},
			},
			Joins: []PlanJoin{{
				ID: "join_1", Name: "客户订单关联", Left: PlanInput{Kind: "NODE", ID: "node_1"}, Right: PlanInput{Kind: "NODE", ID: "node_2"}, JoinType: "LEFT",
				Conditions: []PlanJoinCondition{{LeftNodeID: "node_1", LeftColumn: "customer_id", RightNodeID: "node_2", RightColumn: "customer_id"}},
			}},
			Groups: []PlanGroup{{
				ID: "group_1", Name: "地区订单汇总", Input: PlanInput{Kind: "JOIN", ID: "join_1"},
				Dimensions: []PlanDimension{{NodeID: "node_1", Column: "region", Grouping: ""}},
				Metrics:    []PlanMetric{{NodeID: "node_2", Column: "amount", Aggregation: "SUM"}},
			}},
			End: PlanEnd{Name: "最终输出", Input: PlanInput{Kind: "GROUP", ID: "group_1"}, Outputs: []PlanOutput{
				{NodeID: "node_1", Column: "region", Name: "地区", Code: "region"},
				{NodeID: "node_2", Column: "amount", Name: "订单金额", Code: "order_amount"},
			}},
		},
	}
}

func TestValidateProposalAcceptsConnectedTree(t *testing.T) {
	if err := validateProposal(testProposal(), testCatalog()); err != nil {
		t.Fatalf("validateProposal() error = %v", err)
	}
}

func TestProposalSchemaIncludesEveryFineGrainedTransformComponent(t *testing.T) {
	raw, err := json.Marshal(proposalOutputSchema(testCatalog()))
	if err != nil {
		t.Fatal(err)
	}
	contract := string(raw)
	for _, componentType := range []string{
		"TEXT_UPPER", "TEXT_TRIM", "TEXT_REPLACE", "TEXT_LOWER", "TEXT_SUBSTRING", "TEXT_CONCAT",
		"NUMBER_ABSOLUTE", "NUMBER_ROUNDING", "NUMBER_ARITHMETIC", "DATE_FORMAT", "NULL", "CAST", "CONDITION",
	} {
		if !strings.Contains(contract, `"`+componentType+`"`) {
			t.Fatalf("proposal schema does not include transform component %s", componentType)
		}
	}
	if !strings.Contains(contract, `"TRANSFORM"`) || !strings.Contains(contract, `"conditionValues"`) {
		t.Fatal("proposal schema does not expose transform inputs or condition arrays")
	}
}

func TestValidateProposalAcceptsConditionTransformWithMixedInValues(t *testing.T) {
	proposal := Proposal{
		SchemaVersion: SchemaVersion, Mode: "CREATE", Summary: "按候选区域生成映射字段", Assumptions: []string{}, Warnings: []string{},
		Plan: GraphPlan{
			Dataset: PlanDataset{Name: "客户区域映射", Description: "条件映射"},
			Nodes:   []PlanNode{{ID: "node_1", TableID: "table-customers", Alias: "customers", SelectedColumns: []string{"customer_id", "region"}}},
			Joins:   []PlanJoin{}, Groups: []PlanGroup{},
			Transforms: []PlanTransform{{
				ID: "transform_1", Name: "区域条件映射", Family: "CONDITION", ComponentType: "CONDITION", Input: PlanInput{Kind: "NODE", ID: "node_1"},
				Rules: []PlanTransformRule{{
					ID: "rule_1", Operation: "CASE", InputKeys: []string{"node_1.region"},
					Output:            PlanTransformOutput{ID: "region_group", Name: "区域分组", Code: "region_group", CanonicalType: "STRING"},
					ConditionOperator: "IN", ThenValue: "目标", ElseValue: "其他",
					ConditionValues: []PlanConditionValue{{ID: "value_1", Mode: "LITERAL", Value: "华东"}, {ID: "value_2", Mode: "FIELD", Value: "node_1.customer_id"}},
				}},
			}},
			End: PlanEnd{Name: "最终输出", Input: PlanInput{Kind: "TRANSFORM", ID: "transform_1"}, Outputs: []PlanOutput{{NodeID: "node_1", Column: "region", Key: "transform_1.region_group", Name: "区域分组", Code: "region_group"}}},
		},
	}
	if err := validateProposal(proposal, testCatalog()); err != nil {
		t.Fatalf("validateProposal() transform error = %v", err)
	}
}

func TestValidateProposalRejectsUnavailableAssetAndField(t *testing.T) {
	proposal := testProposal()
	proposal.Plan.Nodes[0].TableID = "unknown"
	if err := validateProposal(proposal, testCatalog()); !errors.Is(err, ErrInvalidOutput) {
		t.Fatalf("validateProposal() error = %v, want ErrInvalidOutput", err)
	}

	proposal = testProposal()
	proposal.Plan.Nodes[1].SelectedColumns[1] = "secret_column"
	if err := validateProposal(proposal, testCatalog()); !errors.Is(err, ErrInvalidOutput) {
		t.Fatalf("validateProposal() error = %v, want ErrInvalidOutput", err)
	}
}

func TestValidateProposalRejectsCycleOrSharedComponent(t *testing.T) {
	proposal := testProposal()
	proposal.Plan.Groups[0].Input = PlanInput{Kind: "GROUP", ID: "group_1"}
	if err := validateProposal(proposal, testCatalog()); !errors.Is(err, ErrInvalidOutput) {
		t.Fatalf("validateProposal() error = %v, want ErrInvalidOutput", err)
	}

	proposal = testProposal()
	proposal.Plan.End.Input = PlanInput{Kind: "JOIN", ID: "join_1"}
	if err := validateProposal(proposal, testCatalog()); !errors.Is(err, ErrInvalidOutput) {
		t.Fatalf("validateProposal() error = %v, want ErrInvalidOutput", err)
	}
}

func TestValidateProposalRejectsUnsupportedJoinAndNonNumericSum(t *testing.T) {
	proposal := testProposal()
	proposal.Plan.Joins[0].JoinType = "FULL"
	if err := validateProposal(proposal, testCatalog()); !errors.Is(err, ErrInvalidOutput) {
		t.Fatalf("validateProposal() error = %v, want ErrInvalidOutput", err)
	}

	proposal = testProposal()
	proposal.Plan.Groups[0].Metrics[0] = PlanMetric{NodeID: "node_1", Column: "region", Aggregation: "SUM"}
	proposal.Plan.End.Outputs[1] = PlanOutput{NodeID: "node_1", Column: "region", Name: "地区合计", Code: "region_total"}
	if err := validateProposal(proposal, testCatalog()); !errors.Is(err, ErrInvalidOutput) {
		t.Fatalf("validateProposal() error = %v, want ErrInvalidOutput", err)
	}
}

func TestValidateProposalExplainsThatGroupedOutputsKeepPhysicalKeys(t *testing.T) {
	proposal := testProposal()
	proposal.Plan.End.Outputs[1].Key = "group_1.amount_total"
	err := validateProposal(proposal, testCatalog())
	var invalid *InvalidOutputError
	if !errors.As(err, &invalid) || invalid.ReasonCode != InvalidOutputReasonOutput || !strings.Contains(invalid.Detail, "physical nodeId.column key") {
		t.Fatalf("grouped output key error = %#v (%v)", invalid, err)
	}
}

func TestValidateProposalIdentifiesUnavailableGroupFieldForRepair(t *testing.T) {
	proposal := testProposal()
	proposal.Plan.Groups[0].Dimensions[0].Column = "missing_region"
	err := validateProposal(proposal, testCatalog())
	var invalid *InvalidOutputError
	if !errors.As(err, &invalid) || invalid.ReasonCode != InvalidOutputReasonFieldReference {
		t.Fatalf("group dimension error = %#v (%v)", invalid, err)
	}
	for _, expected := range []string{"group_1", "node_1.missing_region", "JOIN:join_1"} {
		if !strings.Contains(invalid.Detail, expected) {
			t.Fatalf("group dimension detail %q does not contain %q", invalid.Detail, expected)
		}
	}
}

func TestValidateProposalRejectsIncompatibleJoinTypes(t *testing.T) {
	proposal := testProposal()
	proposal.Plan.Joins[0].Conditions[0].RightColumn = "amount"
	if err := validateProposal(proposal, testCatalog()); !errors.Is(err, ErrInvalidOutput) {
		t.Fatalf("validateProposal() error = %v, want ErrInvalidOutput", err)
	}
}

func TestValidateProposalRejectsDateGroupingOnNonDateField(t *testing.T) {
	proposal := testProposal()
	proposal.Plan.Groups[0].Dimensions[0].Grouping = "MONTH"
	if err := validateProposal(proposal, testCatalog()); !errors.Is(err, ErrInvalidOutput) {
		t.Fatalf("validateProposal() error = %v, want ErrInvalidOutput", err)
	}
}

func TestValidateProposalRejectsNonExecutablePhysicalColumn(t *testing.T) {
	catalog := testCatalog()
	catalog[0].Columns[1].Name = "customer region"
	proposal := testProposal()
	proposal.Plan.Nodes[0].SelectedColumns[1] = "customer region"
	proposal.Plan.Groups[0].Dimensions[0].Column = "customer region"
	proposal.Plan.End.Outputs[0].Column = "customer region"
	if err := validateProposal(proposal, catalog); !errors.Is(err, ErrInvalidOutput) {
		t.Fatalf("validateProposal() error = %v, want ErrInvalidOutput", err)
	}
}

func TestNormalizePlanRequestKeepsIncompleteCurrentNodesForRepair(t *testing.T) {
	current := GraphPlan{
		Dataset: PlanDataset{Name: "半成品"},
		Nodes:   []PlanNode{{ID: "node_1", TableID: "table-customers", Alias: "customers", SelectedColumns: []string{"customer_id"}}},
		Joins:   []PlanJoin{}, Groups: []PlanGroup{},
		End: PlanEnd{Name: "最终输出", Input: PlanInput{Kind: "NODE", ID: "node_1"}, Outputs: []PlanOutput{}},
	}
	input, err := normalizePlanRequest(PlanRequest{Instruction: "补完整", Current: &current})
	if err != nil {
		t.Fatalf("normalizePlanRequest() error = %v", err)
	}
	if input.Current == nil || len(input.Current.Nodes) != 1 {
		t.Fatalf("normalizePlanRequest() current = %#v", input.Current)
	}
}

func monthlyRegionalOrderCountCatalog() []CatalogTable {
	return []CatalogTable{
		{ID: "table-orders", Columns: []CatalogColumn{
			{Name: "ORDER_ID", CanonicalType: "NUMBER", SemanticType: "IDENTIFIER", Nullable: false},
			{Name: "CUSTOMER_ID", CanonicalType: "NUMBER", SemanticType: "IDENTIFIER", Nullable: false},
			{Name: "CREATED_AT", CanonicalType: "DATETIME", SemanticType: "DATETIME", Nullable: false},
		}},
		{ID: "table-customers", Columns: []CatalogColumn{
			{Name: "customer_id", CanonicalType: "NUMBER", SemanticType: "IDENTIFIER", Nullable: false},
			{Name: "region_code", CanonicalType: "STRING", SemanticType: "DIMENSION", Nullable: false},
		}},
	}
}

func monthlyRegionalOrderCountProposal() Proposal {
	return Proposal{
		SchemaVersion: SchemaVersion,
		Mode:          "CREATE",
		Summary:       "按月和地区统计订单量",
		Assumptions:   []string{},
		Warnings:      []string{},
		Plan: GraphPlan{
			Dataset: PlanDataset{Name: "月度区域订单量", Description: "按下单月份和客户地区统计订单数"},
			Nodes: []PlanNode{
				{ID: "node_1", TableID: "table-orders", Alias: "orders", SelectedColumns: []string{"ORDER_ID", "CUSTOMER_ID", "CREATED_AT"}},
				{ID: "node_2", TableID: "table-customers", Alias: "customers", SelectedColumns: []string{"customer_id", "region_code"}},
			},
			Joins: []PlanJoin{{
				ID: "join_1", Name: "订单客户关联", Left: PlanInput{Kind: "NODE", ID: "node_1"}, Right: PlanInput{Kind: "NODE", ID: "node_2"}, JoinType: "LEFT",
				Conditions: []PlanJoinCondition{{LeftNodeID: "node_1", LeftColumn: "CUSTOMER_ID", RightNodeID: "node_2", RightColumn: "customer_id"}},
			}},
			Groups: []PlanGroup{{
				ID: "group_1", Name: "月度地区汇总", Input: PlanInput{Kind: "JOIN", ID: "join_1"},
				Dimensions: []PlanDimension{
					{NodeID: "node_1", Column: "CREATED_AT", Grouping: "MONTH"},
					{NodeID: "node_2", Column: "region_code", Grouping: ""},
				},
				Metrics: []PlanMetric{{NodeID: "node_1", Column: "ORDER_ID", Aggregation: "COUNT"}},
			}},
			End: PlanEnd{Name: "最终输出", Input: PlanInput{Kind: "GROUP", ID: "group_1"}, Outputs: []PlanOutput{
				{NodeID: "node_1", Column: "CREATED_AT", Name: "月份", Code: "order_month"},
				{NodeID: "node_2", Column: "region_code", Name: "地区", Code: "region_code"},
				{NodeID: "node_1", Column: "ORDER_ID", Name: "订单量", Code: "order_count"},
			}},
		},
	}
}

func TestValidateProposalAcceptsExactCaseCountFieldAcrossMixedCaseTables(t *testing.T) {
	if err := validateProposal(monthlyRegionalOrderCountProposal(), monthlyRegionalOrderCountCatalog()); err != nil {
		t.Fatalf("validateProposal() error = %v", err)
	}
}

func TestValidateProposalClassifiesSyntheticAndCaseMismatchedCountFields(t *testing.T) {
	tests := []struct {
		name       string
		column     string
		reasonCode string
	}{
		{name: "count expression", column: "COUNT(*)", reasonCode: InvalidOutputReasonAggregationField},
		{name: "wrong physical case", column: "order_id", reasonCode: InvalidOutputReasonFieldCaseMismatch},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			proposal := monthlyRegionalOrderCountProposal()
			proposal.Plan.Nodes[0].SelectedColumns[0] = test.column
			proposal.Plan.Groups[0].Metrics[0].Column = test.column
			proposal.Plan.End.Outputs[2].Column = test.column
			err := validateProposal(proposal, monthlyRegionalOrderCountCatalog())
			var invalid *InvalidOutputError
			if !errors.As(err, &invalid) || invalid.ReasonCode != test.reasonCode || invalid.Stage != InvalidOutputStagePlanValidation {
				t.Fatalf("invalid output = %#v (%v), want reason %s", invalid, err, test.reasonCode)
			}
		})
	}
}

func TestNormalizePlanRequestValidatesStructuredHints(t *testing.T) {
	request, err := normalizePlanRequest(PlanRequest{Instruction: "统计订单量", Hints: &PlanHints{
		PreferredTableIDs: []string{" table-orders ", "table-orders"},
		Aggregation:       "count",
		MeasureFields:     []PlanFieldHint{{TableID: " table-orders ", Column: " ORDER_ID "}},
		TimeField:         &PlanFieldHint{TableID: "table-orders", Column: "CREATED_AT"},
		DimensionFields:   []PlanFieldHint{{TableID: "table-customers", Column: "region_code"}},
		TimeGrain:         "month",
	}})
	if err != nil {
		t.Fatalf("normalizePlanRequest() error = %v", err)
	}
	if request.Hints == nil || request.Hints.Aggregation != "COUNT" || request.Hints.TimeGrain != "MONTH" || len(request.Hints.PreferredTableIDs) != 1 || request.Hints.MeasureFields[0].Column != "ORDER_ID" {
		t.Fatalf("normalized hints = %#v", request.Hints)
	}

	for _, hints := range []*PlanHints{
		{Aggregation: "MEDIAN"},
		{TimeGrain: "HOUR"},
		{MeasureFields: []PlanFieldHint{{TableID: "table-orders", Column: "*"}}},
		{TimeField: &PlanFieldHint{}},
	} {
		if _, err := normalizePlanRequest(PlanRequest{Instruction: "统计订单量", Hints: hints}); !errors.Is(err, ErrInvalidRequest) {
			t.Fatalf("normalizePlanRequest(%#v) error = %v, want ErrInvalidRequest", hints, err)
		}
	}
	fieldsAcrossTooManyTables := make([]PlanFieldHint, 0, maxHintTables+1)
	for index := 0; index <= maxHintTables; index++ {
		fieldsAcrossTooManyTables = append(fieldsAcrossTooManyTables, PlanFieldHint{TableID: fmt.Sprintf("table-%d", index), Column: "ORDER_ID"})
	}
	if _, err := normalizePlanRequest(PlanRequest{Instruction: "统计订单量", Hints: &PlanHints{MeasureFields: fieldsAcrossTooManyTables}}); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("normalizePlanRequest() too many referenced tables error = %v, want ErrInvalidRequest", err)
	}
}
