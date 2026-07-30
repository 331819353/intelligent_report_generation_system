package datasetai

import "testing"

func TestTextCaseRequirementsPreserveRequestedOperation(t *testing.T) {
	requirements := deriveCreateTransformRequirements("把姓名转为大写，把邮箱转为小写")
	if len(requirements) != 2 {
		t.Fatalf("requirement count = %d, want 2: %#v", len(requirements), requirements)
	}
	if requirements[0].ComponentType != "TEXT_CASE" || requirements[0].Operation != "UPPER" {
		t.Fatalf("upper requirement = %#v", requirements[0])
	}
	if requirements[1].ComponentType != "TEXT_CASE" || requirements[1].Operation != "LOWER" {
		t.Fatalf("lower requirement = %#v", requirements[1])
	}
}

func TestValidateTextCaseRequirementsAcceptsMixedRules(t *testing.T) {
	plan := GraphPlan{
		Transforms: []PlanTransform{{
			ID: "transform_1", ComponentType: "TEXT_CASE",
			Rules: []PlanTransformRule{
				{ID: "rule_1", Operation: "UPPER", Output: PlanTransformOutput{ID: "upper_name"}},
				{ID: "rule_2", Operation: "LOWER", Output: PlanTransformOutput{ID: "lower_email"}},
			},
		}},
		End: PlanEnd{Outputs: []PlanOutput{
			{Key: "transform_1.upper_name"},
			{Key: "transform_1.lower_email"},
		}},
	}
	requirements := []TransformRequirement{
		{ComponentType: "TEXT_CASE", Operation: "UPPER"},
		{ComponentType: "TEXT_CASE", Operation: "LOWER"},
	}
	if err := validateTransformRequirements(plan, requirements); err != nil {
		t.Fatal(err)
	}
}
