package datasetai

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func scopeTestPlan() GraphPlan {
	return GraphPlan{
		Dataset: PlanDataset{Name: "客户订单汇总", Description: "关联前后均有汇总"},
		Nodes: []PlanNode{
			{ID: "node_1", TableID: "table-customers", Alias: "customers", SelectedColumns: []string{"customer_id", "region"}},
			{ID: "node_2", TableID: "table-orders", Alias: "orders", SelectedColumns: []string{"customer_id", "amount"}},
		},
		Joins: []PlanJoin{{
			ID: "join_1", Name: "客户订单关联", Left: PlanInput{Kind: "NODE", ID: "node_1"}, Right: PlanInput{Kind: "GROUP", ID: "group_before"}, JoinType: "LEFT",
			Conditions: []PlanJoinCondition{{LeftNodeID: "node_1", LeftColumn: "customer_id", RightNodeID: "node_2", RightColumn: "customer_id"}},
		}},
		Groups: []PlanGroup{
			{
				ID: "group_before", Name: "关联前订单汇总", Input: PlanInput{Kind: "NODE", ID: "node_2"},
				Dimensions: []PlanDimension{{NodeID: "node_2", Column: "customer_id"}},
				Metrics:    []PlanMetric{{NodeID: "node_2", Column: "amount", Aggregation: "SUM"}},
			},
			{
				ID: "group_after", Name: "关联后地区汇总", Input: PlanInput{Kind: "JOIN", ID: "join_1"},
				Dimensions: []PlanDimension{{NodeID: "node_1", Column: "region"}},
				Metrics:    []PlanMetric{{NodeID: "node_2", Column: "amount", Aggregation: "SUM"}},
			},
		},
		End: PlanEnd{
			Name: "最终输出", Input: PlanInput{Kind: "GROUP", ID: "group_after"},
			Outputs: []PlanOutput{
				{NodeID: "node_1", Column: "region", Name: "地区", Code: "region"},
				{NodeID: "node_2", Column: "amount", Name: "金额", Code: "amount"},
			},
		},
	}
}

func fieldChangeTestCatalog() []CatalogTable {
	return []CatalogTable{
		{ID: "table-customers", Columns: []CatalogColumn{
			{Name: "customer_id", CanonicalType: "STRING"},
			{Name: "region", CanonicalType: "STRING"},
			{Name: "segment", CanonicalType: "STRING"},
			{Name: "join_key", CanonicalType: "STRING"},
		}},
		{ID: "table-orders", Columns: []CatalogColumn{
			{Name: "customer_id", CanonicalType: "STRING"},
			{Name: "amount", CanonicalType: "DECIMAL"},
			{Name: "order_date", CanonicalType: "DATE"},
			{Name: "join_key", CanonicalType: "STRING"},
		}},
	}
}

func scopeOperation(action, kind, id string, fields []string, changes []InputChange) ChangeOperation {
	if fields == nil {
		fields = []string{}
	}
	if changes == nil {
		changes = []InputChange{}
	}
	return ChangeOperation{
		Action: action, ComponentKind: kind, ComponentID: id, ComponentName: "模型提供的名称",
		Fields: fields, InputChanges: changes, Description: "执行用户明确要求的修改",
	}
}

func scopePostRemovalSet() ChangeSet {
	return ChangeSet{Operations: []ChangeOperation{
		scopeOperation("REMOVE", "GROUP", "group_after", nil, nil),
		scopeOperation("UPDATE", "END", endComponentID, []string{"input"}, []InputChange{{
			Field: "input", From: PlanInput{Kind: "GROUP", ID: "group_after"}, To: PlanInput{Kind: "JOIN", ID: "join_1"},
		}}),
	}}
}

func scopePlanAfterPostRemoval() GraphPlan {
	plan := scopeTestPlan()
	plan.Groups = plan.Groups[:1]
	plan.End.Input = PlanInput{Kind: "JOIN", ID: "join_1"}
	return plan
}

func TestBuildPromptEditContextContainsOnlyDerivedTopology(t *testing.T) {
	plan := scopeTestPlan()
	context := buildPromptEditContext(&plan)
	if context == nil || len(context.GroupRoles) != 2 {
		t.Fatalf("context = %#v", context)
	}
	if !reflect.DeepEqual(context.GroupRoles[0].Roles, []string{"POSITION_1", "BEFORE_JOIN"}) ||
		!reflect.DeepEqual(context.GroupRoles[0].Consumers, []string{"JOIN:join_1.right"}) {
		t.Fatalf("before role = %#v", context.GroupRoles[0])
	}
	if !reflect.DeepEqual(context.GroupRoles[1].Roles, []string{"POSITION_2", "AFTER_JOIN", "OUTPUT_GROUP"}) ||
		!reflect.DeepEqual(context.GroupRoles[1].Consumers, []string{"END:end_1.input"}) {
		t.Fatalf("after role = %#v", context.GroupRoles[1])
	}
	if buildPromptEditContext(nil) != nil {
		t.Fatal("nil current must not produce edit context")
	}
}

func TestNormalizeAndValidateReadyIntentCanonicalizesTrustedStructure(t *testing.T) {
	intent := ChangeIntent{Status: " ready ", Question: "", Candidates: nil, ChangeSet: scopePostRemovalSet()}
	normalized, err := normalizeAndValidateChangeIntent(scopeTestPlan(), intent)
	if err != nil {
		t.Fatalf("normalize intent: %v", err)
	}
	if normalized.Status != "READY" || normalized.Candidates == nil || len(normalized.ChangeSet.Operations) != 2 {
		t.Fatalf("normalized = %#v", normalized)
	}
	var removed, rewired ChangeOperation
	for _, operation := range normalized.ChangeSet.Operations {
		if operation.ComponentID == "group_after" {
			removed = operation
		}
		if operation.ComponentID == endComponentID {
			rewired = operation
		}
	}
	if removed.ComponentName != "关联后地区汇总" || rewired.ComponentName != "最终输出" {
		t.Fatalf("trusted names = %#v / %#v", removed, rewired)
	}
}

func TestNormalizeAndValidateReadyIntentDropsRedundantRemoveFieldsAndInputs(t *testing.T) {
	changeSet := scopePostRemovalSet()
	changeSet.Operations[0].Fields = []string{"dimensions", "metrics"}
	changeSet.Operations[0].InputChanges = []InputChange{{
		Field: "input", From: PlanInput{Kind: "JOIN", ID: "join_1"}, To: PlanInput{Kind: "NODE", ID: "node_1"},
	}}
	normalized, err := normalizeAndValidateChangeIntent(scopeTestPlan(), ChangeIntent{Status: "READY", Candidates: []ComponentRef{}, ChangeSet: changeSet})
	if err != nil {
		t.Fatalf("normalize redundant remove metadata: %v", err)
	}
	for _, operation := range normalized.ChangeSet.Operations {
		if operation.ComponentKind == "GROUP" && operation.ComponentID == "group_after" && (len(operation.Fields) != 0 || len(operation.InputChanges) != 0) {
			t.Fatalf("redundant remove metadata was not discarded: %#v", operation)
		}
	}
}

func TestNormalizeAndValidateClarificationRequiresRealUniqueCandidates(t *testing.T) {
	intent := ChangeIntent{
		Status: "CLARIFY", Question: "请选择要删除的分组。",
		Candidates: []ComponentRef{{ComponentKind: "group", ComponentID: "group_after"}},
		ChangeSet:  ChangeSet{Operations: []ChangeOperation{}},
	}
	normalized, err := normalizeAndValidateChangeIntent(scopeTestPlan(), intent)
	if err != nil || normalized.Candidates[0].ComponentKind != "GROUP" {
		t.Fatalf("clarify normalization = %#v, %v", normalized, err)
	}

	intent.Candidates[0].ComponentID = "missing_group"
	if _, err := normalizeAndValidateChangeIntent(scopeTestPlan(), intent); !errors.Is(err, ErrInvalidOutput) {
		t.Fatalf("missing candidate error = %v", err)
	}
	intent.Candidates = []ComponentRef{{ComponentKind: "GROUP", ComponentID: "group_after"}, {ComponentKind: "GROUP", ComponentID: "group_after"}}
	if _, err := normalizeAndValidateChangeIntent(scopeTestPlan(), intent); !errors.Is(err, ErrInvalidOutput) {
		t.Fatalf("duplicate candidate error = %v", err)
	}
}

func TestReadyIntentRejectsMissingOrIncorrectConsumerRewire(t *testing.T) {
	missing := ChangeIntent{Status: "READY", Candidates: []ComponentRef{}, ChangeSet: ChangeSet{Operations: []ChangeOperation{
		scopeOperation("REMOVE", "GROUP", "group_after", nil, nil),
	}}}
	if _, err := normalizeAndValidateChangeIntent(scopeTestPlan(), missing); !errors.Is(err, ErrInvalidOutput) || !strings.Contains(err.Error(), "UPDATE END:end_1") {
		t.Fatalf("missing rewire error = %v", err)
	}

	wrongFrom := scopePostRemovalSet()
	wrongFrom.Operations[1].InputChanges[0].From = PlanInput{Kind: "GROUP", ID: "group_before"}
	if _, err := normalizeAndValidateChangeIntent(scopeTestPlan(), ChangeIntent{Status: "READY", Candidates: []ComponentRef{}, ChangeSet: wrongFrom}); !errors.Is(err, ErrInvalidOutput) {
		t.Fatalf("wrong from error = %v", err)
	}

	missingTo := scopePostRemovalSet()
	missingTo.Operations[1].InputChanges[0].To = PlanInput{Kind: "JOIN", ID: "missing_join"}
	if _, err := normalizeAndValidateChangeIntent(scopeTestPlan(), ChangeIntent{Status: "READY", Candidates: []ComponentRef{}, ChangeSet: missingTo}); !errors.Is(err, ErrInvalidOutput) {
		t.Fatalf("unavailable to error = %v", err)
	}
}

func TestReadyIntentRejectsInvalidOperationContracts(t *testing.T) {
	tests := []ChangeOperation{
		scopeOperation("REMOVE", "GROUP", "missing", nil, nil),
		scopeOperation("REMOVE", "END", endComponentID, nil, nil),
		scopeOperation("UPDATE", "GROUP", "group_after", []string{"joinType"}, nil),
		scopeOperation("UPDATE", "GROUP", "group_after", nil, nil),
		scopeOperation("ADD", "GROUP", "group_after", nil, nil),
		scopeOperation("ADD", "GROUP", "node_1", nil, nil),
	}
	for _, operation := range tests {
		intent := ChangeIntent{Status: "READY", Candidates: []ComponentRef{}, ChangeSet: ChangeSet{Operations: []ChangeOperation{operation}}}
		if _, err := normalizeAndValidateChangeIntent(scopeTestPlan(), intent); !errors.Is(err, ErrInvalidOutput) {
			t.Errorf("operation %#v error = %v", operation, err)
		}
	}
}

func TestValidateAndCanonicalizePlanChangesAcceptsExactLockedDiff(t *testing.T) {
	expected := scopePostRemovalSet()
	canonical, err := validateAndCanonicalizePlanChanges(scopeTestPlan(), scopePlanAfterPostRemoval(), expected)
	if err != nil {
		t.Fatalf("exact diff: %v", err)
	}
	if !equalChangeSetScope(canonical, expected) || len(canonical.Operations) != 2 {
		t.Fatalf("canonical = %#v", canonical)
	}
	for _, operation := range canonical.Operations {
		if operation.Description != "执行用户明确要求的修改" || operation.ComponentName == "模型提供的名称" {
			t.Fatalf("canonical review metadata = %#v", operation)
		}
	}
}

func TestValidateAndCanonicalizePlanChangesRejectsOverBroadOrHiddenChanges(t *testing.T) {
	overBroad := scopePlanAfterPostRemoval()
	overBroad.Groups = []PlanGroup{}
	overBroad.Joins[0].Right = PlanInput{Kind: "NODE", ID: "node_2"}
	_, err := validateAndCanonicalizePlanChanges(scopeTestPlan(), overBroad, scopePostRemovalSet())
	if !errors.Is(err, ErrInvalidOutput) || !strings.Contains(err.Error(), "group_before") {
		t.Fatalf("over-broad error = %v", err)
	}

	hidden := scopePlanAfterPostRemoval()
	hidden.Groups[0].Metrics[0].Aggregation = "AVG"
	if _, err := validateAndCanonicalizePlanChanges(scopeTestPlan(), hidden, scopePostRemovalSet()); !errors.Is(err, ErrInvalidOutput) {
		t.Fatalf("hidden group update error = %v", err)
	}
}

func TestPlanDiffIgnoresTopLevelOrderButKeepsNestedOrderSensitive(t *testing.T) {
	current := scopeTestPlan()
	reordered := cloneGraphPlan(current)
	reordered.Nodes[0], reordered.Nodes[1] = reordered.Nodes[1], reordered.Nodes[0]
	reordered.Groups[0], reordered.Groups[1] = reordered.Groups[1], reordered.Groups[0]
	canonical, err := validateAndCanonicalizePlanChanges(current, reordered, ChangeSet{Operations: []ChangeOperation{}})
	if err != nil || len(canonical.Operations) != 0 {
		t.Fatalf("top-level reorder = %#v, %v", canonical, err)
	}

	nested := cloneGraphPlan(current)
	nested.Nodes[0].SelectedColumns[0], nested.Nodes[0].SelectedColumns[1] = nested.Nodes[0].SelectedColumns[1], nested.Nodes[0].SelectedColumns[0]
	expected := ChangeSet{Operations: []ChangeOperation{
		scopeOperation("UPDATE", "NODE", "node_1", []string{"selectedColumns"}, nil),
	}}
	if _, err := validateAndCanonicalizePlanChanges(current, nested, expected); err != nil {
		t.Fatalf("declared nested reorder: %v", err)
	}
	if _, err := validateAndCanonicalizePlanChanges(current, nested, ChangeSet{Operations: []ChangeOperation{}}); !errors.Is(err, ErrInvalidOutput) {
		t.Fatalf("undeclared nested reorder error = %v", err)
	}

	outputs := cloneGraphPlan(current)
	outputs.End.Outputs[0], outputs.End.Outputs[1] = outputs.End.Outputs[1], outputs.End.Outputs[0]
	outputReorder := ChangeSet{Operations: []ChangeOperation{
		scopeOperation("UPDATE", "END", endComponentID, []string{"outputs"}, nil),
	}}
	if _, err := validateAndCanonicalizePlanChanges(current, outputs, outputReorder); err != nil {
		t.Fatalf("explicit output reorder without fieldChanges: %v", err)
	}
}

func TestEqualChangeSetScopeIgnoresReviewTextAndOperationOrder(t *testing.T) {
	left := scopePostRemovalSet()
	right := scopePostRemovalSet()
	right.Operations[0], right.Operations[1] = right.Operations[1], right.Operations[0]
	for index := range right.Operations {
		right.Operations[index].ComponentName = "不同展示名"
		right.Operations[index].Description = "不同说明"
	}
	if !equalChangeSetScope(left, right) {
		t.Fatalf("review metadata changed scope: %#v / %#v", left, right)
	}
	right.Operations[0].Fields = append(right.Operations[0].Fields, "outputs")
	if equalChangeSetScope(left, right) {
		t.Fatal("different fields must change scope")
	}
}

func TestFieldChangeAddsOnlyOneFieldAlongsideExistingOutputs(t *testing.T) {
	current := scopeTestPlan()
	proposal := cloneGraphPlan(current)
	proposal.Nodes[0].SelectedColumns = append(proposal.Nodes[0].SelectedColumns, "segment")
	proposal.Groups[1].Dimensions = append(proposal.Groups[1].Dimensions, PlanDimension{NodeID: "node_1", Column: "segment"})
	proposal.End.Outputs = append(proposal.End.Outputs, PlanOutput{NodeID: "node_1", Column: "segment", Name: "客户分群", Code: "segment"})
	expected := ChangeSet{
		Operations: []ChangeOperation{
			scopeOperation("UPDATE", "NODE", "node_1", []string{"selectedColumns"}, nil),
			scopeOperation("UPDATE", "GROUP", "group_after", []string{"dimensions"}, nil),
			scopeOperation("UPDATE", "END", endComponentID, []string{"outputs"}, nil),
		},
		FieldChanges: []FieldChange{{
			Field: FieldBinding{NodeID: "node_1", TableID: "table-customers", Column: "segment"}, SelectionAction: "ADD", Purpose: "FINAL_OUTPUT",
			GroupUses:  []FieldGroupUse{{GroupID: "group_after", Role: "DIMENSION", Grouping: ""}},
			JoinUses:   []FieldJoinUse{},
			OutputUses: []FieldOutputUse{{EndID: endComponentID, Name: "客户分群", Code: "segment"}},
		}},
	}

	canonical, err := validateAndCanonicalizePlanChanges(current, proposal, expected, fieldChangeTestCatalog())
	if err != nil {
		t.Fatalf("single field change beside existing outputs: %v", err)
	}
	if len(canonical.FieldChanges) != 1 || canonical.FieldChanges[0].Field.Column != "segment" {
		t.Fatalf("canonical fieldChanges = %#v", canonical.FieldChanges)
	}
}

func TestFieldChangeKeepPropagatesAlreadySelectedField(t *testing.T) {
	current := scopeTestPlan()
	proposal := cloneGraphPlan(current)
	proposal.Groups[1].Dimensions = append(proposal.Groups[1].Dimensions, PlanDimension{NodeID: "node_1", Column: "customer_id"})
	proposal.End.Outputs = append(proposal.End.Outputs, PlanOutput{NodeID: "node_1", Column: "customer_id", Name: "客户编号", Code: "customer_id"})
	expected := ChangeSet{
		Operations: []ChangeOperation{
			scopeOperation("UPDATE", "GROUP", "group_after", []string{"dimensions"}, nil),
			scopeOperation("UPDATE", "END", endComponentID, []string{"outputs"}, nil),
		},
		FieldChanges: []FieldChange{{
			Field: FieldBinding{NodeID: "node_1", TableID: "table-customers", Column: "customer_id"}, SelectionAction: "KEEP", Purpose: "FINAL_OUTPUT",
			GroupUses: []FieldGroupUse{{GroupID: "group_after", Role: "DIMENSION", Grouping: ""}},
			JoinUses: []FieldJoinUse{{
				JoinID: "join_1", Side: "LEFT", Peer: FieldBinding{NodeID: "node_2", TableID: "table-orders", Column: "customer_id"},
			}},
			OutputUses: []FieldOutputUse{{EndID: endComponentID, Name: "客户编号", Code: "customer_id"}},
		}},
	}

	if _, err := validateAndCanonicalizePlanChanges(current, proposal, expected, fieldChangeTestCatalog()); err != nil {
		t.Fatalf("KEEP propagation: %v", err)
	}
}

func TestFieldChangeAllowsExplicitInternalGroupOnlyField(t *testing.T) {
	current := scopeTestPlan()
	proposal := cloneGraphPlan(current)
	proposal.Nodes[1].SelectedColumns = append(proposal.Nodes[1].SelectedColumns, "order_date")
	proposal.Groups[0].Dimensions = append(proposal.Groups[0].Dimensions, PlanDimension{NodeID: "node_2", Column: "order_date", Grouping: "DAY"})
	expected := ChangeSet{
		Operations: []ChangeOperation{
			scopeOperation("UPDATE", "NODE", "node_2", []string{"selectedColumns"}, nil),
			scopeOperation("UPDATE", "GROUP", "group_before", []string{"dimensions"}, nil),
		},
		FieldChanges: []FieldChange{{
			Field: FieldBinding{NodeID: "node_2", TableID: "table-orders", Column: "order_date"}, SelectionAction: "ADD", Purpose: "INTERNAL_ONLY",
			GroupUses:  []FieldGroupUse{{GroupID: "group_before", Role: "DIMENSION", Grouping: "DAY"}},
			JoinUses:   []FieldJoinUse{},
			OutputUses: []FieldOutputUse{},
		}},
	}

	if _, err := validateAndCanonicalizePlanChanges(current, proposal, expected, fieldChangeTestCatalog()); err != nil {
		t.Fatalf("internal group-only field: %v", err)
	}
}

func TestFinalOutputFieldMustReachEndThroughBothGroups(t *testing.T) {
	current := scopeTestPlan()
	expected := ChangeSet{
		Operations: []ChangeOperation{
			scopeOperation("UPDATE", "NODE", "node_2", []string{"selectedColumns"}, nil),
			scopeOperation("UPDATE", "GROUP", "group_before", []string{"dimensions"}, nil),
			scopeOperation("UPDATE", "GROUP", "group_after", []string{"dimensions"}, nil),
			scopeOperation("UPDATE", "END", endComponentID, []string{"outputs"}, nil),
		},
		FieldChanges: []FieldChange{{
			Field: FieldBinding{NodeID: "node_2", TableID: "table-orders", Column: "order_date"}, SelectionAction: "ADD", Purpose: "FINAL_OUTPUT",
			GroupUses: []FieldGroupUse{
				{GroupID: "group_before", Role: "DIMENSION", Grouping: "DAY"},
				{GroupID: "group_after", Role: "DIMENSION", Grouping: "DAY"},
			},
			JoinUses:   []FieldJoinUse{},
			OutputUses: []FieldOutputUse{{EndID: endComponentID, Name: "订单日期", Code: "order_date"}},
		}},
	}

	complete := cloneGraphPlan(current)
	complete.Nodes[1].SelectedColumns = append(complete.Nodes[1].SelectedColumns, "order_date")
	complete.Groups[0].Dimensions = append(complete.Groups[0].Dimensions, PlanDimension{NodeID: "node_2", Column: "order_date", Grouping: "DAY"})
	complete.Groups[1].Dimensions = append(complete.Groups[1].Dimensions, PlanDimension{NodeID: "node_2", Column: "order_date", Grouping: "DAY"})
	complete.End.Outputs = append(complete.End.Outputs, PlanOutput{NodeID: "node_2", Column: "order_date", Name: "订单日期", Code: "order_date"})
	if _, err := validateAndCanonicalizePlanChanges(current, complete, expected, fieldChangeTestCatalog()); err != nil {
		t.Fatalf("complete propagation: %v", err)
	}

	upstreamOnly := cloneGraphPlan(complete)
	upstreamOnly.End.Outputs = append([]PlanOutput(nil), current.End.Outputs...)
	upstreamOnly.End.Outputs[0], upstreamOnly.End.Outputs[1] = upstreamOnly.End.Outputs[1], upstreamOnly.End.Outputs[0]

	missingPostGroup := cloneGraphPlan(current)
	missingPostGroup.Nodes[1].SelectedColumns = append(missingPostGroup.Nodes[1].SelectedColumns, "order_date")
	missingPostGroup.Groups[0].Dimensions = append(missingPostGroup.Groups[0].Dimensions, PlanDimension{NodeID: "node_2", Column: "order_date", Grouping: "DAY"})
	missingPostGroup.Groups[1].Dimensions = append(missingPostGroup.Groups[1].Dimensions, PlanDimension{NodeID: "node_1", Column: "customer_id"})
	missingPostGroup.End.Outputs[0], missingPostGroup.End.Outputs[1] = missingPostGroup.End.Outputs[1], missingPostGroup.End.Outputs[0]
	if err := validateGraphPlan(missingPostGroup, fieldChangeTestCatalog()); err != nil {
		t.Fatalf("missing-post-group fixture must remain a valid graph: %v", err)
	}

	nodeOnly := cloneGraphPlan(current)
	nodeOnly.Nodes[1].SelectedColumns = append(nodeOnly.Nodes[1].SelectedColumns, "order_date")
	if err := validateGraphPlan(nodeOnly, fieldChangeTestCatalog()); err != nil {
		t.Fatalf("node-only fixture must remain a valid graph: %v", err)
	}

	for name, proposal := range map[string]GraphPlan{
		"missing end output":                  upstreamOnly,
		"missing post-join group propagation": missingPostGroup,
		"selected on node only":               nodeOnly,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := validateAndCanonicalizePlanChanges(current, proposal, expected, fieldChangeTestCatalog()); !errors.Is(err, ErrInvalidOutput) {
				t.Fatalf("incomplete propagation error = %v", err)
			}
		})
	}
}

func TestSelectedJoinPeerRequiresItsOwnFieldChange(t *testing.T) {
	current := testProposal().Plan
	proposal := cloneGraphPlan(current)
	proposal.Nodes[0].SelectedColumns = append(proposal.Nodes[0].SelectedColumns, "join_key")
	proposal.Nodes[1].SelectedColumns = append(proposal.Nodes[1].SelectedColumns, "join_key")
	proposal.Joins[0].Conditions = append(proposal.Joins[0].Conditions, PlanJoinCondition{
		LeftNodeID: "node_1", LeftColumn: "join_key", RightNodeID: "node_2", RightColumn: "join_key",
	})
	expected := ChangeSet{
		Operations: []ChangeOperation{
			scopeOperation("UPDATE", "NODE", "node_1", []string{"selectedColumns"}, nil),
			scopeOperation("UPDATE", "NODE", "node_2", []string{"selectedColumns"}, nil),
			scopeOperation("UPDATE", "JOIN", "join_1", []string{"conditions"}, nil),
		},
		FieldChanges: []FieldChange{{
			Field: FieldBinding{NodeID: "node_1", TableID: "table-customers", Column: "join_key"}, SelectionAction: "ADD", Purpose: "INTERNAL_ONLY",
			GroupUses: []FieldGroupUse{},
			JoinUses: []FieldJoinUse{{
				JoinID: "join_1", Side: "LEFT", Peer: FieldBinding{NodeID: "node_2", TableID: "table-orders", Column: "join_key"},
			}},
			OutputUses: []FieldOutputUse{},
		}},
	}

	_, err := validateAndCanonicalizePlanChanges(current, proposal, expected, fieldChangeTestCatalog())
	if !errors.Is(err, ErrInvalidOutput) || !strings.Contains(err.Error(), "node_2") {
		t.Fatalf("missing peer FieldChange error = %v", err)
	}
}

func TestSelectedColumnsMembershipChangeRejectsEmptyFieldChanges(t *testing.T) {
	current := scopeTestPlan()
	proposal := cloneGraphPlan(current)
	proposal.Nodes[0].SelectedColumns = append(proposal.Nodes[0].SelectedColumns, "segment")
	expected := ChangeSet{Operations: []ChangeOperation{
		scopeOperation("UPDATE", "NODE", "node_1", []string{"selectedColumns"}, nil),
	}}
	_, err := validateAndCanonicalizePlanChanges(current, proposal, expected, fieldChangeTestCatalog())
	if !errors.Is(err, ErrInvalidOutput) || !strings.Contains(err.Error(), "fieldChange") {
		t.Fatalf("missing fieldChanges error = %v", err)
	}
}

func TestFieldChangeRejectsColumnOutsideAuthoritativeCatalog(t *testing.T) {
	current := scopeTestPlan()
	intent := ChangeIntent{Status: "READY", Candidates: []ComponentRef{}, ChangeSet: ChangeSet{
		Operations: []ChangeOperation{
			scopeOperation("UPDATE", "NODE", "node_1", []string{"selectedColumns"}, nil),
			scopeOperation("UPDATE", "GROUP", "group_after", []string{"dimensions"}, nil),
		},
		FieldChanges: []FieldChange{{
			Field: FieldBinding{NodeID: "node_1", TableID: "table-customers", Column: "not_in_catalog"}, SelectionAction: "ADD", Purpose: "INTERNAL_ONLY",
			GroupUses:  []FieldGroupUse{{GroupID: "group_after", Role: "DIMENSION", Grouping: ""}},
			JoinUses:   []FieldJoinUse{},
			OutputUses: []FieldOutputUse{},
		}},
	}}
	if _, err := normalizeAndValidateChangeIntent(current, intent, fieldChangeTestCatalog()); !errors.Is(err, ErrInvalidOutput) || !strings.Contains(err.Error(), "catalog") {
		t.Fatalf("unavailable catalog field error = %v", err)
	}
}

func TestFieldChangesSupportAddedNodeAndJoin(t *testing.T) {
	catalog := fieldChangeTestCatalog()
	current := GraphPlan{
		Dataset: PlanDataset{Name: "客户明细"},
		Nodes: []PlanNode{{
			ID: "node_1", TableID: "table-customers", Alias: "customers", SelectedColumns: []string{"customer_id", "region"},
		}},
		Joins:  []PlanJoin{},
		Groups: []PlanGroup{},
		End: PlanEnd{Name: "最终输出", Input: PlanInput{Kind: "NODE", ID: "node_1"}, Outputs: []PlanOutput{
			{NodeID: "node_1", Column: "region", Name: "地区", Code: "region"},
		}},
	}
	proposal := cloneGraphPlan(current)
	proposal.Nodes = append(proposal.Nodes, PlanNode{
		ID: "node_2", TableID: "table-orders", Alias: "orders", SelectedColumns: []string{"customer_id", "amount"},
	})
	proposal.Joins = []PlanJoin{{
		ID: "join_1", Name: "客户订单关联", Left: PlanInput{Kind: "NODE", ID: "node_1"}, Right: PlanInput{Kind: "NODE", ID: "node_2"}, JoinType: "LEFT",
		Conditions: []PlanJoinCondition{{LeftNodeID: "node_1", LeftColumn: "customer_id", RightNodeID: "node_2", RightColumn: "customer_id"}},
	}}
	proposal.End.Input = PlanInput{Kind: "JOIN", ID: "join_1"}
	proposal.End.Outputs = append(proposal.End.Outputs, PlanOutput{NodeID: "node_2", Column: "amount", Name: "订单金额", Code: "amount"})
	if err := validateGraphPlan(proposal, catalog); err != nil {
		t.Fatalf("added-node proposal fixture: %v", err)
	}

	expected := ChangeSet{
		Operations: []ChangeOperation{
			scopeOperation("ADD", "NODE", "node_2", nil, nil),
			scopeOperation("ADD", "JOIN", "join_1", nil, nil),
			scopeOperation("UPDATE", "END", endComponentID, []string{"input", "outputs"}, []InputChange{{
				Field: "input", From: PlanInput{Kind: "NODE", ID: "node_1"}, To: PlanInput{Kind: "JOIN", ID: "join_1"},
			}}),
		},
		FieldChanges: []FieldChange{
			{
				Field: FieldBinding{NodeID: "node_1", TableID: "table-customers", Column: "customer_id"}, SelectionAction: "KEEP", Purpose: "INTERNAL_ONLY",
				GroupUses: []FieldGroupUse{},
				JoinUses: []FieldJoinUse{{JoinID: "join_1", Side: "LEFT", Peer: FieldBinding{
					NodeID: "node_2", TableID: "table-orders", Column: "customer_id",
				}}},
				OutputUses: []FieldOutputUse{},
			},
			{
				Field: FieldBinding{NodeID: "node_2", TableID: "table-orders", Column: "customer_id"}, SelectionAction: "ADD", Purpose: "INTERNAL_ONLY",
				GroupUses: []FieldGroupUse{},
				JoinUses: []FieldJoinUse{{JoinID: "join_1", Side: "RIGHT", Peer: FieldBinding{
					NodeID: "node_1", TableID: "table-customers", Column: "customer_id",
				}}},
				OutputUses: []FieldOutputUse{},
			},
			{
				Field: FieldBinding{NodeID: "node_2", TableID: "table-orders", Column: "amount"}, SelectionAction: "ADD", Purpose: "FINAL_OUTPUT",
				GroupUses:  []FieldGroupUse{},
				JoinUses:   []FieldJoinUse{},
				OutputUses: []FieldOutputUse{{EndID: endComponentID, Name: "订单金额", Code: "amount"}},
			},
		},
	}
	if _, err := validateAndCanonicalizePlanChanges(current, proposal, expected, catalog); err != nil {
		t.Fatalf("added node and join: %v", err)
	}

	wrongTable := expected
	wrongTable.FieldChanges = cloneFieldChanges(expected.FieldChanges)
	wrongTable.FieldChanges[0].JoinUses[0].Peer.TableID = "table-customers"
	wrongTable.FieldChanges[1].Field.TableID = "table-customers"
	wrongTable.FieldChanges[2].Field.TableID = "table-customers"
	wrongCatalog := cloneCatalog(catalog)
	wrongCatalog[0].Columns = append(wrongCatalog[0].Columns, CatalogColumn{Name: "amount", CanonicalType: "DECIMAL"})
	if _, err := validateAndCanonicalizePlanChanges(current, proposal, wrongTable, wrongCatalog); !errors.Is(err, ErrInvalidOutput) || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("wrong added-node tableId error = %v", err)
	}

	missingNewField := expected
	missingNewField.FieldChanges = cloneFieldChanges(expected.FieldChanges[:2])
	if _, err := validateAndCanonicalizePlanChanges(current, proposal, missingNewField, catalog); !errors.Is(err, ErrInvalidOutput) {
		t.Fatalf("missing added-node fieldChange error = %v", err)
	}
}

func TestFieldChangesSupportNodeTableIdentityMigration(t *testing.T) {
	catalog := []CatalogTable{
		{ID: "table-a", Columns: []CatalogColumn{{Name: "customer_id", CanonicalType: "STRING"}}},
		{ID: "table-b", Columns: []CatalogColumn{{Name: "customer_id", CanonicalType: "STRING"}}},
		{ID: "table-c", Columns: []CatalogColumn{{Name: "customer_id", CanonicalType: "STRING"}}},
	}
	current := GraphPlan{
		Dataset: PlanDataset{Name: "客户标识"},
		Nodes: []PlanNode{{
			ID: "node_1", TableID: "table-a", Alias: "customers", SelectedColumns: []string{"customer_id"},
		}},
		Joins:  []PlanJoin{},
		Groups: []PlanGroup{},
		End: PlanEnd{Name: "最终输出", Input: PlanInput{Kind: "NODE", ID: "node_1"}, Outputs: []PlanOutput{{
			NodeID: "node_1", Column: "customer_id", Name: "客户编号", Code: "customer_id",
		}}},
	}
	proposal := cloneGraphPlan(current)
	proposal.Nodes[0].TableID = "table-b"
	if err := validateGraphPlan(proposal, catalog); err != nil {
		t.Fatalf("table migration fixture: %v", err)
	}

	changeSet := func(newAction string) ChangeSet {
		return ChangeSet{
			Operations: []ChangeOperation{
				scopeOperation("UPDATE", "NODE", "node_1", []string{"tableId"}, nil),
			},
			FieldChanges: []FieldChange{
				{
					Field: FieldBinding{NodeID: "node_1", TableID: "table-a", Column: "customer_id"}, SelectionAction: "REMOVE", Purpose: "FINAL_OUTPUT",
					GroupUses: []FieldGroupUse{}, JoinUses: []FieldJoinUse{}, OutputUses: []FieldOutputUse{},
				},
				{
					Field: FieldBinding{NodeID: "node_1", TableID: "table-b", Column: "customer_id"}, SelectionAction: newAction, Purpose: "FINAL_OUTPUT",
					GroupUses: []FieldGroupUse{}, JoinUses: []FieldJoinUse{}, OutputUses: []FieldOutputUse{{
						EndID: endComponentID, Name: "客户编号", Code: "customer_id",
					}},
				},
			},
		}
	}

	for _, action := range []string{"ADD", "KEEP"} {
		t.Run("new binding "+action, func(t *testing.T) {
			canonical, err := validateAndCanonicalizePlanChanges(current, proposal, changeSet(action), catalog)
			if err != nil {
				t.Fatalf("table migration with %s: %v", action, err)
			}
			if len(canonical.Operations) != 1 || !reflect.DeepEqual(canonical.Operations[0].Fields, []string{"tableId"}) {
				t.Fatalf("table migration invented array updates: %#v", canonical.Operations)
			}
		})
	}

	wrongProposal := cloneGraphPlan(current)
	wrongProposal.Nodes[0].TableID = "table-c"
	if _, err := validateAndCanonicalizePlanChanges(current, wrongProposal, changeSet("ADD"), catalog); !errors.Is(err, ErrInvalidOutput) || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("wrong table migration target error = %v", err)
	}

	missingOld := changeSet("ADD")
	missingOld.FieldChanges = cloneFieldChanges(missingOld.FieldChanges[1:])
	if _, err := validateAndCanonicalizePlanChanges(current, proposal, missingOld, catalog); !errors.Is(err, ErrInvalidOutput) {
		t.Fatalf("missing old table binding error = %v", err)
	}
	missingNew := changeSet("ADD")
	missingNew.FieldChanges = cloneFieldChanges(missingNew.FieldChanges[:1])
	if _, err := validateAndCanonicalizePlanChanges(current, proposal, missingNew, catalog); !errors.Is(err, ErrInvalidOutput) {
		t.Fatalf("missing new table binding error = %v", err)
	}

	thirdTable := changeSet("ADD")
	thirdTable.FieldChanges = append(cloneFieldChanges(thirdTable.FieldChanges), FieldChange{
		Field: FieldBinding{NodeID: "node_1", TableID: "table-c", Column: "customer_id"}, SelectionAction: "ADD", Purpose: "SELECTED_ONLY",
		GroupUses: []FieldGroupUse{}, JoinUses: []FieldJoinUse{}, OutputUses: []FieldOutputUse{},
	})
	if _, err := validateAndCanonicalizePlanChanges(current, proposal, thirdTable, catalog); !errors.Is(err, ErrInvalidOutput) || !strings.Contains(err.Error(), "conflicting tables") {
		t.Fatalf("third migration table binding error = %v", err)
	}

	sneakyEnd := cloneGraphPlan(proposal)
	sneakyEnd.End.Outputs[0].Name = "被夹带修改的名称"
	if _, err := validateAndCanonicalizePlanChanges(current, sneakyEnd, changeSet("ADD"), catalog); !errors.Is(err, ErrInvalidOutput) {
		t.Fatalf("undeclared END change during table migration error = %v", err)
	}
}

func TestTableMigrationRejectsUndeclaredGroupUseChange(t *testing.T) {
	catalog := []CatalogTable{
		{ID: "table-a", Columns: []CatalogColumn{{Name: "category", CanonicalType: "STRING"}, {Name: "amount", CanonicalType: "DECIMAL"}}},
		{ID: "table-b", Columns: []CatalogColumn{{Name: "category", CanonicalType: "STRING"}, {Name: "amount", CanonicalType: "DECIMAL"}}},
	}
	current := GraphPlan{
		Dataset: PlanDataset{Name: "分类金额"},
		Nodes:   []PlanNode{{ID: "node_1", TableID: "table-a", Alias: "source", SelectedColumns: []string{"category", "amount"}}},
		Joins:   []PlanJoin{},
		Groups: []PlanGroup{{
			ID: "group_1", Name: "分类汇总", Input: PlanInput{Kind: "NODE", ID: "node_1"},
			Dimensions: []PlanDimension{{NodeID: "node_1", Column: "category"}},
			Metrics:    []PlanMetric{{NodeID: "node_1", Column: "amount", Aggregation: "SUM"}},
		}},
		End: PlanEnd{Name: "最终输出", Input: PlanInput{Kind: "GROUP", ID: "group_1"}, Outputs: []PlanOutput{
			{NodeID: "node_1", Column: "category", Name: "分类", Code: "category"},
			{NodeID: "node_1", Column: "amount", Name: "金额", Code: "amount"},
		}},
	}
	proposal := cloneGraphPlan(current)
	proposal.Nodes[0].TableID = "table-b"
	proposal.Groups[0].Metrics[0].Aggregation = "AVG"
	if err := validateGraphPlan(proposal, catalog); err != nil {
		t.Fatalf("group migration fixture: %v", err)
	}
	expected := ChangeSet{
		Operations: []ChangeOperation{scopeOperation("UPDATE", "NODE", "node_1", []string{"tableId"}, nil)},
		FieldChanges: []FieldChange{
			{
				Field: FieldBinding{NodeID: "node_1", TableID: "table-a", Column: "category"}, SelectionAction: "REMOVE", Purpose: "FINAL_OUTPUT",
				GroupUses: []FieldGroupUse{}, JoinUses: []FieldJoinUse{}, OutputUses: []FieldOutputUse{},
			},
			{
				Field: FieldBinding{NodeID: "node_1", TableID: "table-a", Column: "amount"}, SelectionAction: "REMOVE", Purpose: "FINAL_OUTPUT",
				GroupUses: []FieldGroupUse{}, JoinUses: []FieldJoinUse{}, OutputUses: []FieldOutputUse{},
			},
			{
				Field: FieldBinding{NodeID: "node_1", TableID: "table-b", Column: "category"}, SelectionAction: "ADD", Purpose: "FINAL_OUTPUT",
				GroupUses: []FieldGroupUse{{GroupID: "group_1", Role: "DIMENSION", Grouping: ""}}, JoinUses: []FieldJoinUse{},
				OutputUses: []FieldOutputUse{{EndID: endComponentID, Name: "分类", Code: "category"}},
			},
			{
				Field: FieldBinding{NodeID: "node_1", TableID: "table-b", Column: "amount"}, SelectionAction: "ADD", Purpose: "FINAL_OUTPUT",
				GroupUses: []FieldGroupUse{{GroupID: "group_1", Role: "METRIC", Aggregation: "AVG"}}, JoinUses: []FieldJoinUse{},
				OutputUses: []FieldOutputUse{{EndID: endComponentID, Name: "金额", Code: "amount"}},
			},
		},
	}
	if _, err := validateAndCanonicalizePlanChanges(current, proposal, expected, catalog); !errors.Is(err, ErrInvalidOutput) {
		t.Fatalf("undeclared GROUP change during table migration error = %v", err)
	}
}

func TestTableMigrationSupportsMoreThanSixtyFourSelectedColumns(t *testing.T) {
	const columnCount = 65
	columns := make([]CatalogColumn, 0, columnCount)
	selected := make([]string, 0, columnCount)
	for index := 0; index < columnCount; index++ {
		name := fmt.Sprintf("field_%02d", index)
		selected = append(selected, name)
		columns = append(columns, CatalogColumn{Name: name, CanonicalType: "STRING"})
	}
	catalog := []CatalogTable{
		{ID: "table-a", Columns: append([]CatalogColumn(nil), columns...)},
		{ID: "table-b", Columns: append([]CatalogColumn(nil), columns...)},
	}
	current := GraphPlan{
		Dataset: PlanDataset{Name: "宽表迁移"},
		Nodes:   []PlanNode{{ID: "node_1", TableID: "table-a", Alias: "source", SelectedColumns: append([]string(nil), selected...)}},
		Joins:   []PlanJoin{}, Groups: []PlanGroup{},
		End: PlanEnd{Name: "最终输出", Input: PlanInput{Kind: "NODE", ID: "node_1"}, Outputs: []PlanOutput{{
			NodeID: "node_1", Column: selected[0], Name: "主字段", Code: "primary_field",
		}}},
	}
	proposal := cloneGraphPlan(current)
	proposal.Nodes[0].TableID = "table-b"
	expected := ChangeSet{Operations: []ChangeOperation{
		scopeOperation("UPDATE", "NODE", "node_1", []string{"tableId"}, nil),
	}}
	for index, column := range selected {
		expected.FieldChanges = append(expected.FieldChanges,
			FieldChange{
				Field: FieldBinding{NodeID: "node_1", TableID: "table-a", Column: column}, SelectionAction: "REMOVE", Purpose: "FINAL_OUTPUT",
				GroupUses: []FieldGroupUse{}, JoinUses: []FieldJoinUse{}, OutputUses: []FieldOutputUse{},
			},
		)
		newField := FieldChange{
			Field: FieldBinding{NodeID: "node_1", TableID: "table-b", Column: column}, SelectionAction: "ADD", Purpose: "SELECTED_ONLY",
			GroupUses: []FieldGroupUse{}, JoinUses: []FieldJoinUse{}, OutputUses: []FieldOutputUse{},
		}
		if index == 0 {
			newField.Purpose = "FINAL_OUTPUT"
			newField.OutputUses = []FieldOutputUse{{EndID: endComponentID, Name: "主字段", Code: "primary_field"}}
		}
		expected.FieldChanges = append(expected.FieldChanges, newField)
	}
	if len(expected.FieldChanges) != 130 {
		t.Fatalf("fieldChanges count = %d", len(expected.FieldChanges))
	}
	if _, err := validateAndCanonicalizePlanChanges(current, proposal, expected, catalog); err != nil {
		t.Fatalf("65-column table migration: %v", err)
	}
}

func TestSelectedOnlySupportsAddAndKeepWithoutDownstreamUses(t *testing.T) {
	catalog := fieldChangeTestCatalog()
	current := GraphPlan{
		Dataset: PlanDataset{Name: "客户字段"},
		Nodes: []PlanNode{{
			ID: "node_1", TableID: "table-customers", Alias: "customers", SelectedColumns: []string{"region", "segment"},
		}},
		Joins:  []PlanJoin{},
		Groups: []PlanGroup{},
		End: PlanEnd{Name: "最终输出", Input: PlanInput{Kind: "NODE", ID: "node_1"}, Outputs: []PlanOutput{
			{NodeID: "node_1", Column: "region", Name: "地区", Code: "region"},
			{NodeID: "node_1", Column: "segment", Name: "客户分群", Code: "segment"},
		}},
	}

	t.Run("KEEP removes final output but preserves selection", func(t *testing.T) {
		proposal := cloneGraphPlan(current)
		proposal.End.Outputs = proposal.End.Outputs[:1]
		expected := ChangeSet{
			Operations: []ChangeOperation{scopeOperation("UPDATE", "END", endComponentID, []string{"outputs"}, nil)},
			FieldChanges: []FieldChange{{
				Field: FieldBinding{NodeID: "node_1", TableID: "table-customers", Column: "segment"}, SelectionAction: "KEEP", Purpose: "SELECTED_ONLY",
				GroupUses: []FieldGroupUse{}, JoinUses: []FieldJoinUse{}, OutputUses: []FieldOutputUse{},
			}},
		}
		if _, err := validateAndCanonicalizePlanChanges(current, proposal, expected, catalog); err != nil {
			t.Fatalf("KEEP SELECTED_ONLY: %v", err)
		}
	})

	t.Run("ADD keeps a field selected for later", func(t *testing.T) {
		baseline := cloneGraphPlan(current)
		baseline.Nodes[0].SelectedColumns = []string{"region"}
		baseline.End.Outputs = baseline.End.Outputs[:1]
		proposal := cloneGraphPlan(baseline)
		proposal.Nodes[0].SelectedColumns = append(proposal.Nodes[0].SelectedColumns, "segment")
		expected := ChangeSet{
			Operations: []ChangeOperation{scopeOperation("UPDATE", "NODE", "node_1", []string{"selectedColumns"}, nil)},
			FieldChanges: []FieldChange{{
				Field: FieldBinding{NodeID: "node_1", TableID: "table-customers", Column: "segment"}, SelectionAction: "ADD", Purpose: "SELECTED_ONLY",
				GroupUses: []FieldGroupUse{}, JoinUses: []FieldJoinUse{}, OutputUses: []FieldOutputUse{},
			}},
		}
		if _, err := validateAndCanonicalizePlanChanges(baseline, proposal, expected, catalog); err != nil {
			t.Fatalf("ADD SELECTED_ONLY: %v", err)
		}
	})
}

func TestFieldChangesRejectReorderingUnaffectedEntries(t *testing.T) {
	current := scopeTestPlan()
	expected := ChangeSet{
		Operations: []ChangeOperation{
			scopeOperation("UPDATE", "NODE", "node_1", []string{"selectedColumns"}, nil),
			scopeOperation("UPDATE", "GROUP", "group_after", []string{"dimensions"}, nil),
			scopeOperation("UPDATE", "END", endComponentID, []string{"outputs"}, nil),
		},
		FieldChanges: []FieldChange{{
			Field: FieldBinding{NodeID: "node_1", TableID: "table-customers", Column: "segment"}, SelectionAction: "ADD", Purpose: "FINAL_OUTPUT",
			GroupUses:  []FieldGroupUse{{GroupID: "group_after", Role: "DIMENSION", Grouping: ""}},
			JoinUses:   []FieldJoinUse{},
			OutputUses: []FieldOutputUse{{EndID: endComponentID, Name: "客户分群", Code: "segment"}},
		}},
	}
	baseProposal := cloneGraphPlan(current)
	baseProposal.Nodes[0].SelectedColumns = append(baseProposal.Nodes[0].SelectedColumns, "segment")
	baseProposal.Groups[1].Dimensions = append(baseProposal.Groups[1].Dimensions, PlanDimension{NodeID: "node_1", Column: "segment"})
	baseProposal.End.Outputs = append(baseProposal.End.Outputs, PlanOutput{NodeID: "node_1", Column: "segment", Name: "客户分群", Code: "segment"})

	tests := map[string]func(*GraphPlan){
		"selectedColumns": func(plan *GraphPlan) {
			plan.Nodes[0].SelectedColumns[0], plan.Nodes[0].SelectedColumns[1] = plan.Nodes[0].SelectedColumns[1], plan.Nodes[0].SelectedColumns[0]
		},
		"outputs": func(plan *GraphPlan) {
			plan.End.Outputs[0], plan.End.Outputs[1] = plan.End.Outputs[1], plan.End.Outputs[0]
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			proposal := cloneGraphPlan(baseProposal)
			mutate(&proposal)
			if _, err := validateAndCanonicalizePlanChanges(current, proposal, expected, fieldChangeTestCatalog()); !errors.Is(err, ErrInvalidOutput) || !strings.Contains(err.Error(), "outside locked fieldChanges") {
				t.Fatalf("reorder smuggling error = %v", err)
			}
		})
	}
}
