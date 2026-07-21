package datasetai

import (
	"context"
	"strings"
	"testing"

	aiplatform "intelligent-report-generation-system/internal/ai"
)

func proposalWithUsedUpperTransform() Proposal {
	proposal := testProposal()
	proposal.Plan.Transforms = []PlanTransform{{
		ID: "transform_1", Name: "地区大写转换", Family: "TEXT", ComponentType: "TEXT_UPPER",
		Input: PlanInput{Kind: "GROUP", ID: "group_1"},
		Rules: []PlanTransformRule{{
			ID: "rule_1", Operation: "UPPER", InputKeys: []string{"node_1.region"},
			Output: PlanTransformOutput{ID: "output_1", Name: "地区大写", Code: "region_upper", CanonicalType: "STRING"},
		}},
	}}
	proposal.Plan.End.Input = PlanInput{Kind: "TRANSFORM", ID: "transform_1"}
	proposal.Plan.End.Outputs[0] = PlanOutput{
		NodeID: "node_1", Column: "region", Key: "node_1.region_upper",
		Name: "地区大写", Code: "region_upper",
	}
	return proposal
}

func proposalWithCompleteComponentWorkflow() Proposal {
	return Proposal{
		SchemaVersion: SchemaVersion,
		Mode:          "CREATE",
		Summary:       "客户字段处理后预聚合并关联订单，再汇总和格式化输出",
		Assumptions:   []string{},
		Warnings:      []string{},
		Plan: GraphPlan{
			Dataset: PlanDataset{Name: "完整组件工作流", Description: "覆盖关联前后处理链"},
			Nodes: []PlanNode{
				{ID: "node_1", TableID: "table-customers", Alias: "customers", SelectedColumns: []string{"customer_id", "region"}},
				{ID: "node_2", TableID: "table-orders", Alias: "orders", SelectedColumns: []string{"customer_id", "amount"}},
			},
			Transforms: []PlanTransform{
				{
					ID: "transform_1", Name: "地区大写转换", Family: "TEXT", ComponentType: "TEXT_UPPER", Input: PlanInput{Kind: "NODE", ID: "node_1"},
					Rules: []PlanTransformRule{{ID: "rule_1", Operation: "UPPER", InputKeys: []string{"node_1.region"}, Output: PlanTransformOutput{ID: "region_upper", Name: "大写地区", Code: "region_upper", CanonicalType: "STRING"}}},
				},
				{
					ID: "transform_2", Name: "地区小写展示", Family: "TEXT", ComponentType: "TEXT_LOWER", Input: PlanInput{Kind: "GROUP", ID: "group_2"},
					Rules: []PlanTransformRule{{ID: "rule_2", Operation: "LOWER", InputKeys: []string{"transform_1.region_upper"}, Output: PlanTransformOutput{ID: "region_lower", Name: "小写地区", Code: "region_lower", CanonicalType: "STRING"}}},
				},
			},
			Groups: []PlanGroup{
				{
					ID: "group_1", Name: "客户地区预聚合", Input: PlanInput{Kind: "TRANSFORM", ID: "transform_1"},
					Dimensions: []PlanDimension{{NodeID: "node_1", Column: "customer_id"}, {NodeID: "transform_1", Column: "region_upper"}},
					Metrics:    []PlanMetric{{NodeID: "node_1", Column: "region", Aggregation: "COUNT"}},
				},
				{
					ID: "group_2", Name: "关联后地区汇总", Input: PlanInput{Kind: "JOIN", ID: "join_1"},
					Dimensions: []PlanDimension{{NodeID: "transform_1", Column: "region_upper"}},
					Metrics:    []PlanMetric{{NodeID: "node_2", Column: "amount", Aggregation: "SUM"}},
				},
			},
			Joins: []PlanJoin{{
				ID: "join_1", Name: "客户订单关联", Left: PlanInput{Kind: "GROUP", ID: "group_1"}, Right: PlanInput{Kind: "NODE", ID: "node_2"}, JoinType: "LEFT",
				Conditions: []PlanJoinCondition{{LeftNodeID: "node_1", LeftColumn: "customer_id", RightNodeID: "node_2", RightColumn: "customer_id"}},
			}},
			End: PlanEnd{Name: "最终输出", Input: PlanInput{Kind: "TRANSFORM", ID: "transform_2"}, Outputs: []PlanOutput{
				{NodeID: "node_1", Column: "region", Key: "transform_2.region_lower", Name: "小写地区", Code: "region_lower"},
				{NodeID: "node_2", Column: "amount", Key: "node_2.amount", Name: "订单金额", Code: "order_amount"},
			}},
		},
	}
}

func TestDeriveCreateTransformRequirementsRecognizesFineGrainedComponents(t *testing.T) {
	tests := []struct {
		instruction   string
		componentType string
	}{
		{"将姓名转为大写", "TEXT_UPPER"},
		{"清理名称首尾空格", "TEXT_TRIM"},
		{"对备注做文本替换", "TEXT_REPLACE"},
		{"将编码转为小写", "TEXT_LOWER"},
		{"使用字段截取获得前六位", "TEXT_SUBSTRING"},
		{"把省份和城市做字段拼接", "TEXT_CONCAT"},
		{"金额取绝对值", "NUMBER_ABSOLUTE"},
		{"金额四舍五入", "NUMBER_ROUNDING"},
		{"对两个字段进行数值运算", "NUMBER_ARITHMETIC"},
		{"将订单日期转换为年月字段", "DATE_FORMAT"},
		{"为空时填充默认值", "NULL"},
		{"把金额转为字符串", "CAST"},
		{"按金额区间做条件映射", "CONDITION"},
	}
	for _, test := range tests {
		t.Run(test.componentType, func(t *testing.T) {
			requirements := deriveCreateTransformRequirements(test.instruction)
			if len(requirements) != 1 || requirements[0].ComponentType != test.componentType {
				t.Fatalf("requirements = %#v, want %s", requirements, test.componentType)
			}
		})
	}
}

func TestDeriveCreateTransformRequirementsDoesNotConfuseDateGroupingWithFormatting(t *testing.T) {
	for _, instruction := range []string{"按月汇总订单金额", "按季度统计客户数量", "生成按年分组的趋势"} {
		if requirements := deriveCreateTransformRequirements(instruction); len(requirements) != 0 {
			t.Fatalf("deriveCreateTransformRequirements(%q) = %#v", instruction, requirements)
		}
	}
	if requirements := deriveCreateTransformRequirements("将日期字段类型转换为字符串"); len(requirements) != 1 || requirements[0].ComponentType != "CAST" {
		t.Fatalf("cast requirements = %#v", requirements)
	}
}

func TestNormalizeProposalCanonicalizesUniqueTransformOutputKey(t *testing.T) {
	raw := proposalWithUsedUpperTransform()
	raw.Plan.End.Outputs[0].NodeID = "transform_1"
	raw.Plan.End.Outputs[0].Column = "output_1"
	proposal := normalizeProposal(raw, "CREATE")
	if got := proposal.Plan.End.Outputs[0].Key; got != "transform_1.output_1" {
		t.Fatalf("canonical output key = %q", got)
	}
	if output := proposal.Plan.End.Outputs[0]; output.NodeID != "node_1" || output.Column != "region" {
		t.Fatalf("canonical output lineage = %#v", output)
	}
	if err := validateProposal(proposal, testCatalog()); err != nil {
		t.Fatalf("validateProposal() error = %v", err)
	}
}

func TestValidateProposalAcceptsCompleteOptionalComponentWorkflow(t *testing.T) {
	proposal := normalizeProposal(proposalWithCompleteComponentWorkflow(), "CREATE")
	if err := validateProposal(proposal, testCatalog()); err != nil {
		t.Fatalf("validateProposal() error = %v", err)
	}
	requirements := []TransformRequirement{{ComponentType: "TEXT_UPPER"}, {ComponentType: "TEXT_LOWER"}}
	if err := validateTransformRequirements(proposal.Plan, requirements); err != nil {
		t.Fatalf("validateTransformRequirements() error = %v", err)
	}
}

func TestValidateTransformRequirementsRejectsUnusedTransformOutput(t *testing.T) {
	proposal := proposalWithUsedUpperTransform()
	proposal.Plan.End.Input = PlanInput{Kind: "GROUP", ID: "group_1"}
	proposal.Plan.End.Outputs[0] = PlanOutput{NodeID: "node_1", Column: "region", Key: "node_1.region", Name: "地区", Code: "region"}
	err := validateTransformRequirements(proposal.Plan, []TransformRequirement{{ComponentType: "TEXT_UPPER", Reason: "用户要求文本大写转换"}})
	if err == nil || !strings.Contains(err.Error(), "outputs are unused") {
		t.Fatalf("validateTransformRequirements() error = %v", err)
	}
}

func TestServicePlanRepairsMissingRequiredCreateTransform(t *testing.T) {
	valid := proposalWithUsedUpperTransform()
	invoker := &fakeInvoker{configured: true, results: []aiplatform.InvocationResult{
		plannerResult(t, testProposal(), "request-transform-missing"),
		plannerResult(t, valid, "request-transform-repaired"),
	}}
	result, err := NewService(plannerCatalog(), invoker).Plan(context.Background(), "tenant-1", "actor-1", "", PlanRequest{Instruction: "将地区转为大写后输出订单汇总"})
	if err != nil {
		t.Fatalf("Plan() error = %v", err)
	}
	if result.RequestID != "request-transform-repaired" || len(result.Proposal.Plan.Transforms) != 1 || len(invoker.inputs) != 2 {
		t.Fatalf("result/calls = %#v/%d", result, len(invoker.inputs))
	}
	envelope := requestEnvelope(t, invoker.inputs[0])
	if len(envelope.TransformRequirements) != 1 || envelope.TransformRequirements[0].ComponentType != "TEXT_UPPER" {
		t.Fatalf("transform requirements = %#v", envelope.TransformRequirements)
	}
	repairMessage := invoker.inputs[1].Request.Messages[len(invoker.inputs[1].Request.Messages)-1].Parts[0].Text
	if !strings.Contains(repairMessage, InvalidOutputReasonTransform) || !strings.Contains(repairMessage, "transformRequirements") {
		t.Fatalf("repair message = %s", repairMessage)
	}
}
