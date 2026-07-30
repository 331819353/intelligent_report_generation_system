package dataset

import "testing"

func TestNormalizeMigratesLegacyTextCaseComponents(t *testing.T) {
	document := normalize(Document{Transforms: []Transform{
		{ComponentType: "text_upper", Family: "text", Rules: []TransformRule{{Operation: "upper"}}},
		{ComponentType: "text_lower", Family: "text", Rules: []TransformRule{{Operation: "lower"}}},
	}})
	for index, transform := range document.Transforms {
		if transform.ComponentType != "TEXT_CASE" {
			t.Fatalf("transform %d component type = %q, want TEXT_CASE", index, transform.ComponentType)
		}
	}
	if document.Transforms[0].Rules[0].Operation != "UPPER" ||
		document.Transforms[1].Rules[0].Operation != "LOWER" {
		t.Fatalf("legacy operations were not preserved: %#v", document.Transforms)
	}
}

func TestValidateTransformsAcceptsMixedTextCaseRules(t *testing.T) {
	fieldExpression := func(field string) Expression {
		return Expression{
			Type: "FIELD_REF", NodeID: "node_1", Field: field,
		}
	}
	textExpression := func(operation, field string) Expression {
		argument := fieldExpression(field)
		return Expression{Type: operation, Argument: &argument}
	}
	issues := []ValidationIssue{}
	validateTransforms(&issues, []Transform{{
		ID: "transform_1", Name: "大小写转换 1", Family: "TEXT",
		ComponentType: "TEXT_CASE",
		Input:         TransformInput{Kind: "NODE", ID: "node_1"},
		Rules: []TransformRule{
			{
				ID: "rule_1", Operation: "UPPER",
				InputKeys: []string{"node_1.first_name"},
				Output: TransformOutput{
					ID: "upper_name", Name: "大写名称",
					Code: "upper_name", CanonicalType: "STRING",
				},
				Expression: textExpression("UPPER", "first_name"),
			},
			{
				ID: "rule_2", Operation: "LOWER",
				InputKeys: []string{"node_1.last_name"},
				Output: TransformOutput{
					ID: "lower_name", Name: "小写名称",
					Code: "lower_name", CanonicalType: "STRING",
				},
				Expression: textExpression("LOWER", "last_name"),
			},
		},
	}}, map[string]bool{"node_1": true}, map[string]bool{}, map[string]bool{})
	if len(issues) > 0 {
		t.Fatalf("TEXT_CASE validation issues: %#v", issues)
	}
}
