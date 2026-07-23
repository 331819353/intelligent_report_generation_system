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

func derivedSemanticTestPlan() GraphPlan {
	precision := 0
	return GraphPlan{
		Dataset: PlanDataset{Name: "派生字段语义测试", Description: "覆盖日期、文本与数值派生字段"},
		Nodes: []PlanNode{{
			ID: "node_1", TableID: "table-facts", Alias: "facts",
			SelectedColumns: []string{"event_date", "label", "amount"},
		}},
		Transforms: []PlanTransform{
			{
				ID: "transform_1", Name: "统计月份转换", Family: "DATE", ComponentType: "DATE_FORMAT", Input: PlanInput{Kind: "NODE", ID: "node_1"},
				Rules: []PlanTransformRule{{
					ID: "rule_month", Operation: "DATE_FORMAT", InputKeys: []string{"node_1.event_date"}, Unit: "MONTH",
					Output: PlanTransformOutput{ID: "month_value", Name: "统计月份", Code: "event_month", CanonicalType: "STRING"},
				}},
			},
			{
				ID: "transform_2", Name: "标签清理", Family: "TEXT", ComponentType: "TEXT_TRIM", Input: PlanInput{Kind: "TRANSFORM", ID: "transform_1"},
				Rules: []PlanTransformRule{{
					ID: "rule_label", Operation: "TRIM", InputKeys: []string{"node_1.label"},
					Output: PlanTransformOutput{ID: "clean_label", Name: "清洗后标签", Code: "clean_label", CanonicalType: "STRING"},
				}},
			},
			{
				ID: "transform_3", Name: "金额取整", Family: "NUMBER", ComponentType: "NUMBER_ROUNDING", Input: PlanInput{Kind: "TRANSFORM", ID: "transform_2"},
				Rules: []PlanTransformRule{{
					ID: "rule_amount", Operation: "ROUND", InputKeys: []string{"node_1.amount"}, Precision: &precision,
					Output: PlanTransformOutput{ID: "rounded_amount", Name: "取整金额", Code: "rounded_amount", CanonicalType: "DECIMAL"},
				}},
			},
		},
		Groups: []PlanGroup{{
			ID: "group_1", Name: "派生维度指标汇总", Input: PlanInput{Kind: "TRANSFORM", ID: "transform_3"},
			Dimensions: []PlanDimension{
				{NodeID: "transform_1", Column: "month_value"},
				{NodeID: "transform_2", Column: "clean_label"},
			},
			Metrics: []PlanMetric{{NodeID: "transform_3", Column: "rounded_amount", Aggregation: "SUM"}},
		}},
		End: PlanEnd{
			Name: "最终输出", Input: PlanInput{Kind: "GROUP", ID: "group_1"},
			Outputs: []PlanOutput{
				{NodeID: "node_1", Column: "event_date", Key: "transform_1.month_value", Name: "统计月份", Code: "event_month"},
				{NodeID: "node_1", Column: "label", Key: "transform_2.clean_label", Name: "清洗后标签", Code: "clean_label"},
				{NodeID: "node_1", Column: "amount", Key: "transform_3.rounded_amount", Name: "取整金额", Code: "rounded_amount"},
			},
		},
	}
}

func derivedSemanticTestCatalog() []CatalogTable {
	return []CatalogTable{{ID: "table-facts", Columns: []CatalogColumn{
		{Name: "event_date", CanonicalType: "DATE"},
		{Name: "label", CanonicalType: "STRING"},
		{Name: "amount", CanonicalType: "DECIMAL"},
	}}}
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

func TestBuildPromptEditContextIndexesGenericDerivedSemantics(t *testing.T) {
	plan := derivedSemanticTestPlan()
	context := buildPromptEditContext(&plan)
	if context == nil || len(context.DerivedFields) != 3 {
		t.Fatalf("context = %#v", context)
	}
	month := context.DerivedFields[0]
	if month.TransformID != "transform_1" || month.Output.Name != "统计月份" || month.Output.Code != "event_month" ||
		month.PhysicalField != (FieldBinding{NodeID: "node_1", TableID: "table-facts", Column: "event_date"}) {
		t.Fatalf("month semantic = %#v", month)
	}
	if !reflect.DeepEqual(month.Consumers, []string{"TRANSFORM:transform_2.input"}) ||
		!reflect.DeepEqual(month.References, []promptDerivedReference{
			{ComponentKind: "END", ComponentID: endComponentID, Field: "outputs", Role: "FINAL_OUTPUT", Name: "统计月份", Code: "event_month"},
			{ComponentKind: "GROUP", ComponentID: "group_1", Field: "dimensions", Role: "DIMENSION"},
		}) {
		t.Fatalf("month uses = %#v / %#v", month.Consumers, month.References)
	}
	if got := context.DerivedFields[1]; got.ComponentType != "TEXT_TRIM" || got.PhysicalField.Column != "label" || got.References[1].Role != "DIMENSION" {
		t.Fatalf("text semantic = %#v", got)
	}
	if got := context.DerivedFields[2]; got.ComponentType != "NUMBER_ROUNDING" || got.PhysicalField.Column != "amount" || got.References[1].Role != "METRIC" {
		t.Fatalf("number semantic = %#v", got)
	}
}

func TestCanonicalizeUnavailableTransformBindingUsesUniquePhysicalNode(t *testing.T) {
	current := scopeTestPlan()
	binding := FieldBinding{NodeID: "transform_2", TableID: "table-customers", Column: "region"}
	got := canonicalizeUnavailableBindingNode(current, binding)
	if got.NodeID != "node_1" {
		t.Fatalf("canonical binding = %#v", got)
	}
	derived := canonicalizeUnavailableBindingNode(current, FieldBinding{
		NodeID: "transform_2", TableID: "table-customers", Column: "region_trimmed",
	})
	if derived.NodeID != "node_1" || derived.Column != "region" {
		t.Fatalf("derived binding = %#v", derived)
	}

	current.Nodes = append(current.Nodes, PlanNode{
		ID: "node_3", TableID: "table-customers", Alias: "customers_again", SelectedColumns: []string{"region"},
	})
	if ambiguous := canonicalizeUnavailableBindingNode(current, binding); ambiguous != binding {
		t.Fatalf("ambiguous binding was rewritten = %#v", ambiguous)
	}
	addedNode := FieldBinding{NodeID: "node_4", TableID: "table-customers", Column: "region"}
	if got := canonicalizeUnavailableBindingNode(scopeTestPlan(), addedNode); got != addedNode {
		t.Fatalf("added node binding was rewritten = %#v", got)
	}
}

func TestCanonicalizeCurrentDerivedBindingByOutputIDAndCode(t *testing.T) {
	current := derivedSemanticTestPlan()
	cases := []struct {
		name    string
		binding FieldBinding
		column  string
	}{
		{name: "date output id", binding: FieldBinding{NodeID: "transform_1", TableID: "table-facts", Column: "month_value"}, column: "event_date"},
		{name: "date output code", binding: FieldBinding{NodeID: "transform_1", TableID: "table-facts", Column: "event_month"}, column: "event_date"},
		{name: "text output", binding: FieldBinding{NodeID: "transform_2", TableID: "table-facts", Column: "clean_label"}, column: "label"},
		{name: "number output", binding: FieldBinding{NodeID: "transform_3", TableID: "table-facts", Column: "rounded_amount"}, column: "amount"},
		{name: "replacement id unique code", binding: FieldBinding{NodeID: "transform_99", TableID: "table-facts", Column: "event_month"}, column: "event_date"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			got := canonicalizeUnavailableBindingNode(current, test.binding)
			want := FieldBinding{NodeID: "node_1", TableID: "table-facts", Column: test.column}
			if got != want {
				t.Fatalf("binding = %#v, want %#v", got, want)
			}
		})
	}

	ambiguous := cloneGraphPlan(current)
	ambiguous.Transforms = append(ambiguous.Transforms, PlanTransform{
		ID: "transform_4", Name: "另一月份", Family: "DATE", ComponentType: "DATE_FORMAT", Input: PlanInput{Kind: "NODE", ID: "node_1"},
		Rules: []PlanTransformRule{{
			ID: "rule_other", Operation: "DATE_FORMAT", InputKeys: []string{"node_1.event_date"},
			Output: PlanTransformOutput{ID: "other_month", Name: "另一月份", Code: "event_month", CanonicalType: "STRING"},
		}},
	})
	original := FieldBinding{NodeID: "transform_99", TableID: "table-facts", Column: "event_month"}
	if got := canonicalizeUnavailableBindingNode(ambiguous, original); got != original {
		t.Fatalf("ambiguous derived alias was rewritten = %#v", got)
	}
}

func TestGenericDerivedDimensionRemovalNormalizesAndValidates(t *testing.T) {
	current := derivedSemanticTestPlan()
	intent := ChangeIntent{Status: "READY", ChangeSet: ChangeSet{
		Operations: []ChangeOperation{
			scopeOperation("UPDATE", "GROUP", "group_1", []string{"dimensions"}, nil),
			scopeOperation("UPDATE", "END", endComponentID, []string{"outputs"}, nil),
		},
		FieldChanges: []FieldChange{{
			Field:           FieldBinding{NodeID: "transform_1", TableID: "table-facts", Column: "event_month"},
			SelectionAction: "KEEP", Purpose: "SELECTED_ONLY",
			GroupUses: []FieldGroupUse{}, JoinUses: []FieldJoinUse{}, OutputUses: []FieldOutputUse{},
		}},
	}}
	normalized, err := normalizeAndValidateChangeIntent(current, intent, derivedSemanticTestCatalog())
	if err != nil {
		t.Fatalf("normalize generic derived removal: %v", err)
	}
	if got := normalized.ChangeSet.FieldChanges[0].Field; got != (FieldBinding{NodeID: "node_1", TableID: "table-facts", Column: "event_date"}) {
		t.Fatalf("physical scope = %#v", got)
	}
	operations := indexChangeOperations(normalized.ChangeSet.Operations)
	for _, required := range []struct{ kind, id, field string }{
		{kind: "GROUP", id: "group_1", field: "dimensions"},
		{kind: "END", id: endComponentID, field: "outputs"},
		{kind: "TRANSFORM", id: "transform_2", field: "input"},
	} {
		key, _ := componentKey(required.kind, required.id)
		if !containsString(operations[key].Fields, required.field) {
			t.Fatalf("missing completed %s:%s.%s in %#v", required.kind, required.id, required.field, normalized.ChangeSet.Operations)
		}
	}
	removeKey, _ := componentKey("TRANSFORM", "transform_1")
	if operations[removeKey].Action != "REMOVE" {
		t.Fatalf("unused transform removal was not completed: %#v", normalized.ChangeSet.Operations)
	}

	proposal := cloneGraphPlan(current)
	proposal = materializeLockedFieldChanges(current, proposal, normalized.ChangeSet)
	proposal = materializeLockedTransformRemovals(proposal, normalized.ChangeSet)
	if len(proposal.Transforms) != 2 || proposal.Transforms[0].ID != "transform_2" || proposal.Transforms[0].Input != (PlanInput{Kind: "NODE", ID: "node_1"}) {
		t.Fatalf("materialized transform cleanup = %#v", proposal.Transforms)
	}
	canonical, err := validateAndCanonicalizePlanChanges(current, proposal, normalized.ChangeSet, derivedSemanticTestCatalog())
	if err != nil {
		t.Fatalf("validate generic derived removal: %v", err)
	}
	if len(canonical.FieldChanges) != 1 || len(proposal.Groups[0].Dimensions) != 1 || len(proposal.End.Outputs) != 2 {
		t.Fatalf("canonical/proposal = %#v / %#v", canonical, proposal)
	}
}

func TestGenericDerivedTextAndMetricRemovalsNormalizeAndValidate(t *testing.T) {
	tests := []struct {
		name           string
		transformID    string
		field          FieldBinding
		consumerKind   string
		consumerID     string
		consumerFields []string
		inputFrom      PlanInput
		inputTo        PlanInput
		groupField     string
		wantDimensions int
		wantMetrics    int
		wantOutputs    int
	}{
		{
			name: "text-derived dimension", transformID: "transform_2",
			field:        FieldBinding{NodeID: "transform_2", TableID: "table-facts", Column: "clean_label"},
			consumerKind: "TRANSFORM", consumerID: "transform_3", consumerFields: []string{"input"},
			inputFrom: PlanInput{Kind: "TRANSFORM", ID: "transform_2"}, inputTo: PlanInput{Kind: "TRANSFORM", ID: "transform_1"},
			groupField: "dimensions", wantDimensions: 1, wantMetrics: 1, wantOutputs: 2,
		},
		{
			name: "number-derived metric", transformID: "transform_3",
			field:        FieldBinding{NodeID: "transform_3", TableID: "table-facts", Column: "rounded_amount"},
			consumerKind: "GROUP", consumerID: "group_1", consumerFields: []string{"input", "metrics"},
			inputFrom: PlanInput{Kind: "TRANSFORM", ID: "transform_3"}, inputTo: PlanInput{Kind: "TRANSFORM", ID: "transform_2"},
			groupField: "metrics", wantDimensions: 2, wantMetrics: 0, wantOutputs: 2,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			current := derivedSemanticTestPlan()
			consumerOperation := scopeOperation("UPDATE", test.consumerKind, test.consumerID, test.consumerFields, []InputChange{{
				Field: "input", From: test.inputFrom, To: test.inputTo,
			}})
			operations := []ChangeOperation{
				scopeOperation("REMOVE", "TRANSFORM", test.transformID, nil, nil),
				consumerOperation,
				scopeOperation("UPDATE", "END", endComponentID, []string{"outputs"}, nil),
			}
			if test.consumerKind != "GROUP" {
				operations = append(operations, scopeOperation("UPDATE", "GROUP", "group_1", []string{test.groupField}, nil))
			}
			intent := ChangeIntent{Status: "READY", ChangeSet: ChangeSet{
				Operations: operations,
				FieldChanges: []FieldChange{{
					Field: test.field, SelectionAction: "KEEP", Purpose: "SELECTED_ONLY",
					GroupUses: []FieldGroupUse{}, JoinUses: []FieldJoinUse{}, OutputUses: []FieldOutputUse{},
				}},
			}}
			normalized, err := normalizeAndValidateChangeIntent(current, intent, derivedSemanticTestCatalog())
			if err != nil {
				t.Fatalf("normalize: %v", err)
			}

			proposal := cloneGraphPlan(current)
			kept := make([]PlanTransform, 0, len(proposal.Transforms)-1)
			for _, transform := range proposal.Transforms {
				if transform.ID != test.transformID {
					kept = append(kept, transform)
				}
			}
			proposal.Transforms = kept
			if test.consumerKind == "TRANSFORM" {
				for index := range proposal.Transforms {
					if proposal.Transforms[index].ID == test.consumerID {
						proposal.Transforms[index].Input = test.inputTo
					}
				}
			} else {
				proposal.Groups[0].Input = test.inputTo
			}
			proposal = materializeLockedFieldChanges(current, proposal, normalized.ChangeSet)
			if _, err := validateAndCanonicalizePlanChanges(current, proposal, normalized.ChangeSet, derivedSemanticTestCatalog()); err != nil {
				t.Fatalf("validate: %v", err)
			}
			if len(proposal.Groups[0].Dimensions) != test.wantDimensions || len(proposal.Groups[0].Metrics) != test.wantMetrics || len(proposal.End.Outputs) != test.wantOutputs {
				t.Fatalf("result dimensions/metrics/outputs = %d/%d/%d", len(proposal.Groups[0].Dimensions), len(proposal.Groups[0].Metrics), len(proposal.End.Outputs))
			}
		})
	}
}

func TestUnusedDerivedTransformCleanupCollapsesConsecutiveChain(t *testing.T) {
	current := GraphPlan{
		Dataset: PlanDataset{Name: "连续派生清理"},
		Nodes: []PlanNode{{
			ID: "node_1", TableID: "table-facts", Alias: "facts", SelectedColumns: []string{"event_date", "amount"},
		}},
		Transforms: []PlanTransform{
			{
				ID: "transform_1", Name: "月份转换", Family: "DATE", ComponentType: "DATE_FORMAT", Input: PlanInput{Kind: "NODE", ID: "node_1"},
				Rules: []PlanTransformRule{{
					ID: "rule_1", Operation: "DATE_FORMAT", InputKeys: []string{"node_1.event_date"}, Unit: "MONTH",
					Output: PlanTransformOutput{ID: "month", Name: "月份", Code: "month", CanonicalType: "STRING"},
				}},
			},
			{
				ID: "transform_2", Name: "月份文本清理", Family: "TEXT", ComponentType: "TEXT_TRIM", Input: PlanInput{Kind: "TRANSFORM", ID: "transform_1"},
				Rules: []PlanTransformRule{{
					ID: "rule_2", Operation: "TRIM", InputKeys: []string{"transform_1.month"},
					Output: PlanTransformOutput{ID: "clean_month", Name: "清理后月份", Code: "clean_month", CanonicalType: "STRING"},
				}},
			},
		},
		Groups: []PlanGroup{{
			ID: "group_1", Name: "月份汇总", Input: PlanInput{Kind: "TRANSFORM", ID: "transform_2"},
			Dimensions: []PlanDimension{{NodeID: "transform_2", Column: "clean_month"}},
			Metrics:    []PlanMetric{{NodeID: "node_1", Column: "amount", Aggregation: "SUM"}},
		}},
		End: PlanEnd{Name: "最终输出", Input: PlanInput{Kind: "GROUP", ID: "group_1"}, Outputs: []PlanOutput{
			{NodeID: "node_1", Column: "event_date", Key: "transform_2.clean_month", Name: "清理后月份", Code: "clean_month"},
			{NodeID: "node_1", Column: "amount", Name: "金额", Code: "amount"},
		}},
	}
	components, err := indexPlanComponents(current)
	if err != nil {
		t.Fatalf("index components: %v", err)
	}
	operations := []ChangeOperation{
		scopeOperation("UPDATE", "GROUP", "group_1", []string{"dimensions"}, nil),
		scopeOperation("UPDATE", "END", endComponentID, []string{"outputs"}, nil),
	}
	fieldChanges := []FieldChange{{
		Field:           FieldBinding{NodeID: "node_1", TableID: "table-facts", Column: "event_date"},
		SelectionAction: "KEEP", Purpose: "SELECTED_ONLY",
		GroupUses: []FieldGroupUse{}, JoinUses: []FieldJoinUse{}, OutputUses: []FieldOutputUse{},
	}}
	completed, err := completeUnusedDerivedTransformRemovals(current, components, operations, fieldChanges)
	if err != nil {
		t.Fatalf("complete consecutive cleanup: %v", err)
	}
	indexed := indexChangeOperations(completed)
	for _, id := range []string{"transform_1", "transform_2"} {
		key, _ := componentKey("TRANSFORM", id)
		if indexed[key].Action != "REMOVE" {
			t.Fatalf("missing removal for %s: %#v", id, completed)
		}
	}
	groupKey, _ := componentKey("GROUP", "group_1")
	groupOperation := indexed[groupKey]
	if !containsString(groupOperation.Fields, "input") || len(groupOperation.InputChanges) != 1 ||
		groupOperation.InputChanges[0].To != (PlanInput{Kind: "NODE", ID: "node_1"}) {
		t.Fatalf("collapsed group bypass = %#v", groupOperation)
	}
}

func TestCollapseTransformLineageFieldChangesPrefersDerivedFinalUse(t *testing.T) {
	current := scopeTestPlan()
	physical := FieldChange{
		Field:           FieldBinding{NodeID: "node_1", TableID: "table-customers", Column: "region"},
		SelectionAction: "KEEP", Purpose: "SELECTED_ONLY", GroupUses: []FieldGroupUse{}, JoinUses: []FieldJoinUse{}, OutputUses: []FieldOutputUse{},
	}
	derived := FieldChange{
		Field:           FieldBinding{NodeID: "transform_2", TableID: "table-customers", Column: "region_trimmed"},
		SelectionAction: "KEEP", Purpose: "FINAL_OUTPUT",
		GroupUses: []FieldGroupUse{{GroupID: "group_after", Role: "DIMENSION"}}, JoinUses: []FieldJoinUse{},
		OutputUses: []FieldOutputUse{{EndID: endComponentID, Name: "地区", Code: "region"}},
	}
	got := collapseTransformLineageFieldChanges(current, []FieldChange{physical, derived})
	if len(got) != 1 || got[0].Field.NodeID != "node_1" || got[0].Field.Column != "region" || len(got[0].OutputUses) != 1 {
		t.Fatalf("collapsed = %#v", got)
	}
	conflict := derived
	conflict.SelectionAction = "REMOVE"
	if got := collapseTransformLineageFieldChanges(current, []FieldChange{physical, conflict}); len(got) != 2 {
		t.Fatalf("conflicting declarations were collapsed = %#v", got)
	}
}

func TestNormalizeNameUpdateKeepsDesiredTargetName(t *testing.T) {
	intent := ChangeIntent{Status: "READY", ChangeSet: ChangeSet{Operations: []ChangeOperation{{
		Action: "UPDATE", ComponentKind: "DATASET", ComponentID: datasetComponentID,
		ComponentName: "客户月度支付与订单统计", Fields: []string{"name"}, InputChanges: []InputChange{},
		Description: "修改数据集名称",
	}}, FieldChanges: []FieldChange{}}}
	normalized, err := normalizeAndValidateChangeIntent(scopeTestPlan(), intent)
	if err != nil {
		t.Fatalf("normalize intent: %v", err)
	}
	if got := normalized.ChangeSet.Operations[0].ComponentName; got != "客户月度支付与订单统计" {
		t.Fatalf("target name = %q", got)
	}
}

func TestNormalizePrunesRepeatedCurrentNameFromMixedUpdate(t *testing.T) {
	current := derivedSemanticTestPlan()
	intent := ChangeIntent{Status: "READY", ChangeSet: ChangeSet{Operations: []ChangeOperation{
		{
			Action: "UPDATE", ComponentKind: "END", ComponentID: endComponentID,
			ComponentName: current.End.Name, Fields: []string{"name", "outputs"}, InputChanges: []InputChange{},
			Description: "删除一个派生输出，结束节点名称保持不变",
		},
	}, FieldChanges: []FieldChange{}}}
	normalized, err := normalizeAndValidateChangeIntent(current, intent)
	if err != nil {
		t.Fatalf("normalize repeated current name: %v", err)
	}
	if len(normalized.ChangeSet.Operations) != 1 || !reflect.DeepEqual(normalized.ChangeSet.Operations[0].Fields, []string{"outputs"}) {
		t.Fatalf("normalized operations = %#v", normalized.ChangeSet.Operations)
	}

	intent.ChangeSet.Operations[0].Fields = []string{"name"}
	normalized, err = normalizeAndValidateChangeIntent(current, intent)
	if err != nil {
		t.Fatalf("normalize pure no-op name: %v", err)
	}
	if len(normalized.ChangeSet.Operations) != 0 {
		t.Fatalf("pure no-op name update = %#v", normalized.ChangeSet.Operations)
	}
}

func TestNormalizeFieldPurposeIsDerivedFromFinalUses(t *testing.T) {
	current := scopeTestPlan()
	intent := ChangeIntent{Status: "READY", ChangeSet: ChangeSet{
		Operations: []ChangeOperation{scopeOperation("UPDATE", "END", endComponentID, []string{"outputs"}, nil)},
		FieldChanges: []FieldChange{{
			Field:           FieldBinding{NodeID: "node_1", TableID: "table-customers", Column: "region"},
			SelectionAction: "KEEP", Purpose: "SELECTED_ONLY",
			GroupUses: []FieldGroupUse{{GroupID: "group_after", Role: "DIMENSION"}},
			JoinUses:  []FieldJoinUse{}, OutputUses: []FieldOutputUse{},
		}},
	}}
	normalized, err := normalizeAndValidateChangeIntent(current, intent, fieldChangeTestCatalog())
	if err != nil {
		t.Fatalf("normalize intent: %v", err)
	}
	if got := normalized.ChangeSet.FieldChanges[0].Purpose; got != "INTERNAL_ONLY" {
		t.Fatalf("derived purpose = %q", got)
	}
}

func TestNormalizeAndValidateReadyIntentCanonicalizesTrustedStructure(t *testing.T) {
	intent := ChangeIntent{Status: " ready ", Question: "", Candidates: nil, ChangeSet: scopePostRemovalSet()}
	// inputChanges already locks the exact topology. The redundant top-level input
	// field may be omitted by the model and is completed deterministically.
	intent.ChangeSet.Operations[1].Fields = []string{}
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
	if !reflect.DeepEqual(rewired.Fields, []string{"input"}) {
		t.Fatalf("completed input fields = %#v", rewired.Fields)
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
		ChangeSet: ChangeSet{
			Operations: []ChangeOperation{scopeOperation("UPDATE", "GROUP", "group_after", []string{"metrics"}, nil)},
			FieldChanges: []FieldChange{{
				Field: FieldBinding{NodeID: "node_2", TableID: "table-orders", Column: "amount"}, SelectionAction: "KEEP", Purpose: "SELECTED_ONLY",
			}},
		},
	}
	normalized, err := normalizeAndValidateChangeIntent(scopeTestPlan(), intent)
	if err != nil || normalized.Candidates[0].ComponentKind != "GROUP" {
		t.Fatalf("clarify normalization = %#v, %v", normalized, err)
	}
	if len(normalized.ChangeSet.Operations) != 0 || len(normalized.ChangeSet.FieldChanges) != 0 {
		t.Fatalf("clarify tentative scope was retained: %#v", normalized.ChangeSet)
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

func TestLockedOperationLifecycleMatrixMaterializesAndValidates(t *testing.T) {
	catalog := fieldChangeTestCatalog()
	materialize := func(current, planner GraphPlan, locked ChangeSet, requirements []TransformRequirement) GraphPlan {
		plan := materializeLockedComponentState(current, planner, locked)
		plan = materializeLockedScalarChanges(plan, locked)
		plan = materializeLockedNodeTableMigrations(current, plan, locked)
		plan = materializeLockedFieldChanges(current, plan, locked)
		plan = materializeLockedGraphStructure(plan, locked)
		plan = materializeLockedTransformRouting(plan, locked, requirements)
		return preserveProtectedDatasetMetadata(current, plan, locked)
	}
	assertValid := func(t *testing.T, current, planner GraphPlan, locked ChangeSet, requirements []TransformRequirement) GraphPlan {
		t.Helper()
		components, err := indexPlanComponents(normalizeGraphPlan(cloneGraphPlan(current)))
		if err != nil {
			t.Fatal(err)
		}
		normalized, err := normalizeAndValidateChangeSet(current, components, locked, catalog)
		if err != nil {
			t.Fatalf("normalize locked changeSet: %v", err)
		}
		plan := materialize(current, planner, normalized, requirements)
		proposal := Proposal{SchemaVersion: SchemaVersion, Mode: "MODIFY", Summary: "验证完整修改生命周期", Assumptions: []string{}, Warnings: []string{}, Plan: plan}
		if err := validateProposal(proposal, catalog); err != nil {
			t.Fatalf("validate materialized proposal: %v", err)
		}
		if _, err := validateAndCanonicalizePlanChanges(current, plan, normalized, catalog); err != nil {
			t.Fatalf("validate exact locked diff: %v", err)
		}
		if err := validateTransformRequirements(plan, requirements); err != nil {
			t.Fatalf("validate transform requirements: %v", err)
		}
		if err := validateLockedTransformUsage(plan, normalized); err != nil {
			t.Fatalf("validate locked transform usage: %v", err)
		}
		return plan
	}

	t.Run("新建组件并接入指标链路", func(t *testing.T) {
		current := scopeTestPlan()
		precision := 0
		planner := cloneGraphPlan(current)
		planner.Transforms = append(planner.Transforms, PlanTransform{
			ID: "transform_1", Name: "金额取整", Family: "NUMBER", ComponentType: "NUMBER_ROUNDING",
			Input: PlanInput{Kind: "JOIN", ID: "join_1"},
			Rules: []PlanTransformRule{{
				ID: "rule_round", Operation: "ROUND", InputKeys: []string{"node_2.amount"}, Precision: &precision,
				Output: PlanTransformOutput{ID: "rounded_amount", Name: "取整金额", Code: "rounded_amount", CanonicalType: "DECIMAL"},
			}},
		})
		locked := ChangeSet{Operations: []ChangeOperation{
			{Action: "ADD", ComponentKind: "TRANSFORM", ComponentID: "transform_1", ComponentName: "金额取整", Fields: []string{}, InputChanges: []InputChange{}, Description: "新增金额取整组件"},
			scopeOperation("UPDATE", "GROUP", "group_after", []string{"input"}, []InputChange{{
				Field: "input", From: PlanInput{Kind: "JOIN", ID: "join_1"}, To: PlanInput{Kind: "TRANSFORM", ID: "transform_1"},
			}}),
		}}
		plan := assertValid(t, current, planner, locked, nil)
		if plan.Groups[1].Input != (PlanInput{Kind: "TRANSFORM", ID: "transform_1"}) ||
			plan.Groups[1].Metrics[0] != (PlanMetric{NodeID: "transform_1", Column: "rounded_amount", Aggregation: "SUM"}) ||
			plan.End.Outputs[1].Key != "transform_1.rounded_amount" {
			t.Fatalf("new transform routing was incomplete: %#v / %#v", plan.Groups[1], plan.End.Outputs)
		}
	})

	t.Run("更换同语义组件并保持字段口径", func(t *testing.T) {
		current := scopeTestPlan()
		planner := cloneGraphPlan(current)
		planner.Groups = append(planner.Groups, PlanGroup{
			ID: "group_replacement", Name: "替换后的地区汇总", Input: PlanInput{Kind: "JOIN", ID: "join_1"},
			Dimensions: []PlanDimension{{NodeID: "node_1", Column: "region"}},
			Metrics:    []PlanMetric{{NodeID: "node_2", Column: "amount", Aggregation: "SUM"}},
		})
		locked := ChangeSet{
			Operations: []ChangeOperation{
				scopeOperation("REMOVE", "GROUP", "group_after", nil, nil),
				{Action: "ADD", ComponentKind: "GROUP", ComponentID: "group_replacement", ComponentName: "替换后的地区汇总", Fields: []string{}, InputChanges: []InputChange{}, Description: "使用新分组替换原分组"},
				scopeOperation("UPDATE", "END", endComponentID, []string{"input"}, []InputChange{{
					Field: "input", From: PlanInput{Kind: "GROUP", ID: "group_after"}, To: PlanInput{Kind: "GROUP", ID: "group_replacement"},
				}}),
			},
			FieldChanges: []FieldChange{
				{
					Field: FieldBinding{NodeID: "node_1", TableID: "table-customers", Column: "region"}, SelectionAction: "KEEP", Purpose: "FINAL_OUTPUT",
					GroupUses: []FieldGroupUse{{GroupID: "group_replacement", Role: "DIMENSION", Grouping: ""}}, JoinUses: []FieldJoinUse{},
					OutputUses: []FieldOutputUse{{EndID: endComponentID, Name: "地区", Code: "region"}},
				},
				{
					Field: FieldBinding{NodeID: "node_2", TableID: "table-orders", Column: "amount"}, SelectionAction: "KEEP", Purpose: "FINAL_OUTPUT",
					GroupUses: []FieldGroupUse{{GroupID: "group_before", Role: "METRIC", Aggregation: "SUM"}, {GroupID: "group_replacement", Role: "METRIC", Aggregation: "SUM"}}, JoinUses: []FieldJoinUse{},
					OutputUses: []FieldOutputUse{{EndID: endComponentID, Name: "金额", Code: "amount"}},
				},
			},
		}
		plan := assertValid(t, current, planner, locked, nil)
		if len(plan.Groups) != 2 || plan.Groups[1].ID != "group_replacement" || plan.End.Input.ID != "group_replacement" {
			t.Fatalf("replacement was not materialized: %#v / %#v", plan.Groups, plan.End.Input)
		}
	})

	t.Run("移除组件并旁路直接消费者", func(t *testing.T) {
		current := scopeTestPlan()
		plan := assertValid(t, current, current, scopePostRemovalSet(), nil)
		if len(plan.Groups) != 1 || plan.End.Input != (PlanInput{Kind: "JOIN", ID: "join_1"}) {
			t.Fatalf("removal bypass was not materialized: %#v / %#v", plan.Groups, plan.End.Input)
		}
	})

	t.Run("换位时保留组件身份并改全前后链路", func(t *testing.T) {
		current := scopeTestPlan()
		current.Transforms = []PlanTransform{{
			ID: "transform_1", Name: "地区大写", Family: "TEXT", ComponentType: "TEXT_UPPER", Input: PlanInput{Kind: "NODE", ID: "node_1"},
			Rules: []PlanTransformRule{{ID: "rule_upper", Operation: "UPPER", InputKeys: []string{"node_1.region"}, Output: PlanTransformOutput{ID: "region_upper", Name: "大写地区", Code: "region_upper", CanonicalType: "STRING"}}},
		}}
		current.Joins[0].Left = PlanInput{Kind: "TRANSFORM", ID: "transform_1"}
		current.Groups[1].Dimensions[0] = PlanDimension{NodeID: "transform_1", Column: "region_upper"}
		current.End.Outputs[0].Key = "transform_1.region_upper"
		planner := cloneGraphPlan(current)
		locked := ChangeSet{Operations: []ChangeOperation{
			scopeOperation("UPDATE", "JOIN", "join_1", []string{"left"}, []InputChange{{Field: "left", From: PlanInput{Kind: "TRANSFORM", ID: "transform_1"}, To: PlanInput{Kind: "NODE", ID: "node_1"}}}),
			scopeOperation("UPDATE", "TRANSFORM", "transform_1", []string{"input"}, []InputChange{{Field: "input", From: PlanInput{Kind: "NODE", ID: "node_1"}, To: PlanInput{Kind: "JOIN", ID: "join_1"}}}),
			scopeOperation("UPDATE", "GROUP", "group_after", []string{"input"}, []InputChange{{Field: "input", From: PlanInput{Kind: "JOIN", ID: "join_1"}, To: PlanInput{Kind: "TRANSFORM", ID: "transform_1"}}}),
		}}
		plan := assertValid(t, current, planner, locked, nil)
		if plan.Joins[0].Left.Kind != "NODE" || plan.Transforms[0].Input.Kind != "JOIN" || plan.Groups[1].Input.Kind != "TRANSFORM" {
			t.Fatalf("move did not rewire the complete chain: %#v / %#v / %#v", plan.Joins[0], plan.Transforms[0], plan.Groups[1])
		}
	})

	t.Run("修改配置不改变拓扑和其他字段", func(t *testing.T) {
		current := scopeTestPlan()
		planner := cloneGraphPlan(current)
		planner.Joins[0].JoinType = "INNER"
		locked := ChangeSet{Operations: []ChangeOperation{scopeOperation("UPDATE", "JOIN", "join_1", []string{"joinType"}, nil)}}
		plan := assertValid(t, current, planner, locked, nil)
		if plan.Joins[0].JoinType != "INNER" || plan.Joins[0].Left != current.Joins[0].Left || plan.Joins[0].Right != current.Joins[0].Right {
			t.Fatalf("configuration edit changed protected topology: %#v", plan.Joins[0])
		}
	})
}

func TestNormalizeChangeSetAnchorsSingleAddedTransformFromStructuredFieldUses(t *testing.T) {
	current := scopeTestPlan()
	current.Groups = current.Groups[1:]
	current.Joins[0].Right = PlanInput{Kind: "NODE", ID: "node_2"}
	amount := FieldBinding{NodeID: "node_2", TableID: "table-orders", Column: "amount"}
	intent := ChangeIntent{Status: "READY", ChangeSet: ChangeSet{
		Operations: []ChangeOperation{{
			Action: "ADD", ComponentKind: "TRANSFORM", ComponentID: "transform_1", ComponentName: "金额取整",
			Fields: []string{}, InputChanges: []InputChange{}, Description: "新增金额取整组件",
		}},
		FieldChanges: []FieldChange{{
			Field: amount, SelectionAction: "KEEP", Purpose: "FINAL_OUTPUT",
			GroupUses: []FieldGroupUse{{GroupID: "group_after", Role: "METRIC", Aggregation: "SUM"}}, JoinUses: []FieldJoinUse{},
			OutputUses: []FieldOutputUse{{EndID: endComponentID, Name: "取整金额", Code: "rounded_amount"}},
		}},
	}}

	normalized, err := normalizeAndValidateChangeIntent(current, intent, fieldChangeTestCatalog())
	if err != nil {
		t.Fatalf("normalize transform insertion: %v", err)
	}
	operations := indexChangeOperations(normalized.ChangeSet.Operations)
	groupKey, _ := componentKey("GROUP", "group_after")
	groupOperation := operations[groupKey]
	if !containsString(groupOperation.Fields, "input") || len(groupOperation.InputChanges) != 1 ||
		groupOperation.InputChanges[0] != (InputChange{Field: "input", From: PlanInput{Kind: "JOIN", ID: "join_1"}, To: PlanInput{Kind: "TRANSFORM", ID: "transform_1"}}) {
		t.Fatalf("derived transform anchor = %#v", groupOperation)
	}
}

func TestNormalizeChangeSetRejectsUnanchoredAddedComponents(t *testing.T) {
	current := scopeTestPlan()
	intent := ChangeIntent{Status: "READY", ChangeSet: ChangeSet{Operations: []ChangeOperation{{
		Action: "ADD", ComponentKind: "TRANSFORM", ComponentID: "transform_1", ComponentName: "孤立处理",
		Fields: []string{}, InputChanges: []InputChange{}, Description: "新增但未声明下游的组件",
	}}, FieldChanges: []FieldChange{}}}
	if _, err := normalizeAndValidateChangeIntent(current, intent, fieldChangeTestCatalog()); !errors.Is(err, ErrInvalidOutput) || !strings.Contains(err.Error(), "downstream input change") {
		t.Fatalf("unanchored add error = %v", err)
	}
}

func TestValidateAndCanonicalizePlanChangesAcceptsTransformOutputWithPhysicalLineage(t *testing.T) {
	current := scopeTestPlan()
	current.Nodes[1].SelectedColumns = append(current.Nodes[1].SelectedColumns, "order_date")
	current.Groups[0].Dimensions = append(current.Groups[0].Dimensions, PlanDimension{NodeID: "node_2", Column: "order_date"})
	current.Groups[1].Dimensions = append(current.Groups[1].Dimensions, PlanDimension{NodeID: "node_2", Column: "order_date"})
	current.End.Outputs = append(current.End.Outputs, PlanOutput{NodeID: "node_2", Column: "order_date", Name: "下单日期", Code: "order_date"})

	proposal := cloneGraphPlan(current)
	proposal.Transforms = []PlanTransform{{
		ID: "transform_1", Name: "下单月份转换", Family: "DATE", ComponentType: "DATE_FORMAT", Input: PlanInput{Kind: "JOIN", ID: "join_1"},
		Rules: []PlanTransformRule{{
			ID: "rule_1", Operation: "DATE_FORMAT", InputKeys: []string{"node_2.order_date"}, Unit: "MONTH",
			Output: PlanTransformOutput{ID: "order_month", Name: "下单月份", Code: "order_month", CanonicalType: "STRING"},
		}},
	}}
	proposal.Groups[1].Input = PlanInput{Kind: "TRANSFORM", ID: "transform_1"}
	proposal.Groups[1].Dimensions[1] = PlanDimension{NodeID: "transform_1", Column: "order_month"}
	proposal.End.Outputs[2] = PlanOutput{NodeID: "node_2", Column: "order_date", Key: "transform_1.order_month", Name: "下单月份", Code: "order_month"}

	expected := ChangeSet{
		Operations: []ChangeOperation{
			scopeOperation("ADD", "TRANSFORM", "transform_1", nil, nil),
			// The intent model only has to lock the semantic field propagation once in
			// fieldChanges. Missing redundant GROUP.dimensions and END.outputs operation
			// fields are completed deterministically before the planner is invoked.
			scopeOperation("UPDATE", "GROUP", "group_after", []string{"input"}, []InputChange{{
				Field: "input", From: PlanInput{Kind: "JOIN", ID: "join_1"}, To: PlanInput{Kind: "TRANSFORM", ID: "transform_1"},
			}}),
		},
		FieldChanges: []FieldChange{{
			Field: FieldBinding{NodeID: "node_2", TableID: "table-orders", Column: "order_date"}, SelectionAction: "KEEP", Purpose: "FINAL_OUTPUT",
			GroupUses: []FieldGroupUse{
				{GroupID: "group_after", Role: "DIMENSION", Grouping: ""},
				{GroupID: "group_before", Role: "DIMENSION", Grouping: ""},
			},
			JoinUses:   []FieldJoinUse{},
			OutputUses: []FieldOutputUse{{EndID: endComponentID, Name: "下单月份", Code: "order_month"}},
		}},
	}
	if binding := planFieldBinding(proposal, "transform_1", "order_month"); binding != (FieldBinding{NodeID: "node_2", TableID: "table-orders", Column: "order_date"}) {
		t.Fatalf("transform output lineage = %#v", binding)
	}
	if !fieldAvailableAtInput(proposal, proposal.End.Input, expected.FieldChanges[0].Field, map[string]bool{}) {
		t.Fatal("transformed physical field must remain available at END")
	}

	// Simulate the common provider defect where the transform is inserted and wired,
	// but the GROUP dimension still points at the inherited physical date field.
	unrouted := cloneGraphPlan(proposal)
	unrouted.Groups[1].Dimensions[1] = current.Groups[1].Dimensions[1]
	routed := materializeLockedTransformRouting(unrouted, expected, []TransformRequirement{{ComponentType: "DATE_FORMAT"}})
	if routed.Groups[1].Dimensions[1] != (PlanDimension{NodeID: "transform_1", Column: "order_month", Grouping: ""}) {
		t.Fatalf("materialized transform routing = %#v", routed.Groups[1].Dimensions[1])
	}

	canonical, err := validateAndCanonicalizePlanChanges(current, routed, expected, fieldChangeTestCatalog())
	if err != nil {
		t.Fatalf("date transform insertion must retain trusted physical scope: %v", err)
	}
	var groupFields, endFields []string
	for _, operation := range canonical.Operations {
		if operation.ComponentKind == "GROUP" && operation.ComponentID == "group_after" {
			groupFields = operation.Fields
		}
		if operation.ComponentKind == "END" && operation.ComponentID == endComponentID {
			endFields = operation.Fields
		}
	}
	if !reflect.DeepEqual(groupFields, []string{"input"}) || !reflect.DeepEqual(endFields, []string{"outputs"}) {
		t.Fatalf("completed operation fields = GROUP %v / END %v", groupFields, endFields)
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

func TestFieldChangeKeepPrunesRedundantSelectedColumnsOperation(t *testing.T) {
	current := scopeTestPlan()
	proposal := cloneGraphPlan(current)
	proposal.Groups[1].Dimensions = append(proposal.Groups[1].Dimensions, PlanDimension{NodeID: "node_1", Column: "customer_id"})
	proposal.End.Outputs = append(proposal.End.Outputs, PlanOutput{NodeID: "node_1", Column: "customer_id", Name: "客户编号", Code: "customer_id"})
	expected := ChangeSet{
		Operations: []ChangeOperation{
			// The intent model may redundantly request selectedColumns even though
			// selectionAction KEEP proves that the field is already selected.
			scopeOperation("UPDATE", "NODE", "node_1", []string{"selectedColumns"}, nil),
			scopeOperation("UPDATE", "GROUP", "group_after", []string{"dimensions"}, nil),
			scopeOperation("UPDATE", "END", endComponentID, []string{"outputs"}, nil),
		},
		FieldChanges: []FieldChange{{
			Field: FieldBinding{NodeID: "node_1", TableID: "table-customers", Column: "customer_id"}, SelectionAction: "KEEP", Purpose: "FINAL_OUTPUT",
			GroupUses: []FieldGroupUse{{GroupID: "group_after", Role: "DIMENSION", Grouping: ""}},
			// The model omitted the existing join use while describing the new
			// downstream use. JOIN.conditions is not authorized, so normalization
			// must preserve the current condition rather than request its removal.
			JoinUses:   []FieldJoinUse{},
			OutputUses: []FieldOutputUse{{EndID: endComponentID, Name: "客户编号", Code: "customer_id"}},
		}},
	}

	canonical, err := validateAndCanonicalizePlanChanges(current, proposal, expected, fieldChangeTestCatalog())
	if err != nil {
		t.Fatalf("KEEP propagation: %v", err)
	}
	for _, operation := range canonical.Operations {
		if (operation.ComponentKind == "NODE" && operation.ComponentID == "node_1") ||
			(operation.ComponentKind == "JOIN" && operation.ComponentID == "join_1") {
			t.Fatalf("redundant protected-field operation was retained: %#v", canonical.Operations)
		}
	}
	if len(canonical.FieldChanges) != 1 || len(canonical.FieldChanges[0].JoinUses) != 1 {
		t.Fatalf("existing join use was not preserved: %#v", canonical.FieldChanges)
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
	materializedMigration := materializeLockedNodeTableMigrations(current, current, changeSet("ADD"))
	if materializedMigration.Nodes[0].TableID != "table-b" {
		t.Fatalf("locked table replacement was not materialized: %#v", materializedMigration.Nodes[0])
	}
	if _, err := validateAndCanonicalizePlanChanges(current, materializedMigration, changeSet("ADD"), catalog); err != nil {
		t.Fatalf("validate deterministically materialized table replacement: %v", err)
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
