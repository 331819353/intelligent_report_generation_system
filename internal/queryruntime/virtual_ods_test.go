package queryruntime

import (
	"errors"
	"testing"

	"intelligent-report-generation-system/internal/dataset"
)

func TestExpandVirtualODSDocumentRewritesSourceFieldsAndKeepsStringContract(t *testing.T) {
	outer := dataset.Document{
		DSLVersion: dataset.DSLVersion,
		Dataset: dataset.Descriptor{
			Code: "employee_fact", Name: "员工明细",
			Type: "SINGLE_SOURCE", Layer: dataset.LayerDWD,
		},
		Nodes: []dataset.Node{{
			ID: "employee_ods", Type: "DATASET",
			DatasetVersionID: "ods-version", Alias: "employee",
			Projection: []string{
				"node_org_code", "employee_name", "hire_date",
			},
			SourceFilters: []dataset.SourceFilter{{
				Field: "node_org_code", Operator: "IS_NOT_NULL",
			}},
		}},
		Fields: []dataset.Field{
			{
				ID: "code", Code: "node_org_code", Name: "节点组织编码",
				Role: "DIMENSION", CanonicalType: "STRING",
				Expression: dataset.Expression{
					Type: "FIELD_REF", NodeID: "employee_ods",
					Field: "node_org_code",
				},
			},
			{
				ID: "name", Code: "employee_name", Name: "员工姓名",
				Role: "DIMENSION", CanonicalType: "STRING",
				Expression: dataset.Expression{
					Type: "FIELD_REF", NodeID: "employee_ods",
					Field: "employee_name",
				},
			},
		},
		Transforms: []dataset.Transform{{
			ID: "date_calculation_1", Name: "日期计算 1",
			Family: "DATE", ComponentType: "DATE_CALCULATION",
			Input: dataset.TransformInput{
				Kind: "NODE", ID: "employee_ods",
			},
			Rules: []dataset.TransformRule{{
				ID: "tenure_days", Operation: "DATE_DIFF",
				InputKeys: []string{"employee_ods.hire_date"},
				Output: dataset.TransformOutput{
					ID: "tenure_days", Code: "tenure_days",
					Name: "入职天数", CanonicalType: "INTEGER",
				},
				Expression: dataset.Expression{
					Type: "DATE_DIFF", Unit: "DAY",
					Arguments: []dataset.Expression{
						{
							Type: "FIELD_REF", NodeID: "employee_ods",
							Field: "hire_date",
						},
						{Type: "CURRENT_DATE"},
					},
				},
				ReplaceSourceKey: "employee_ods.hire_date",
			}},
		}},
		Filters: []dataset.Filter{}, GroupBy: []string{},
		Having: []dataset.Filter{}, Sorts: []dataset.Sort{},
		Parameters: []dataset.Parameter{},
		OutputGrain: dataset.OutputGrain{
			Description: "每行一个员工",
			KeyFields:   []string{"node_org_code"},
		},
		ExecutionPolicy: dataset.ExecutionPolicy{
			Mode: "REALTIME", TimeoutMS: 5000, PreviewLimit: 100,
			ResultLimit:     1000,
			Materialization: dataset.MaterializationPolicy{Enabled: false},
		},
	}
	ods := dataset.Document{
		DSLVersion: dataset.DSLVersion,
		Dataset: dataset.Descriptor{
			Code: "employee_ods", Name: "员工来源映射",
			Type: "SINGLE_SOURCE", Layer: dataset.LayerODS,
		},
		Nodes: []dataset.Node{{
			ID: "source_sheet", Type: "TABLE",
			DataSourceID: "source-1", TableID: "table-1",
			FileVersionID: "file-version-1", Alias: "sheet",
			Projection: []string{
				"节点组织编码", "员工姓名", "入职日期",
			},
			SourceFilters: []dataset.SourceFilter{},
		}},
		Fields: []dataset.Field{
			{
				ID: "ods-code", Code: "node_org_code",
				Name: "节点组织编码", Role: "DIMENSION",
				CanonicalType: "STRING",
				Expression: dataset.Expression{
					Type: "FIELD_REF", NodeID: "source_sheet",
					Field: "节点组织编码",
				},
			},
			{
				ID: "ods-name", Code: "employee_name",
				Name: "员工姓名", Role: "DIMENSION",
				CanonicalType: "STRING",
				Expression: dataset.Expression{
					Type: "FIELD_REF", NodeID: "source_sheet",
					Field: "员工姓名",
				},
			},
			{
				ID: "ods-hire-date", Code: "hire_date",
				Name: "入职日期", Role: "DIMENSION",
				CanonicalType: "DATE",
				Expression: dataset.Expression{
					Type: "FIELD_REF", NodeID: "source_sheet",
					Field: "入职日期",
				},
			},
		},
		Filters: []dataset.Filter{}, GroupBy: []string{},
		Having: []dataset.Filter{}, Sorts: []dataset.Sort{},
		Parameters: []dataset.Parameter{},
		OutputGrain: dataset.OutputGrain{
			Description: "源表行", KeyFields: []string{"node_org_code"},
		},
		ExecutionPolicy: dataset.ExecutionPolicy{
			Mode: "REALTIME", TimeoutMS: 5000, PreviewLimit: 100,
			ResultLimit:     1000,
			Materialization: dataset.MaterializationPolicy{Enabled: false},
		},
	}

	expanded, overrides, err := expandVirtualODSDocument(
		outer, map[string]dataset.Document{"employee_ods": ods},
	)
	if err != nil {
		t.Fatal(err)
	}
	node := expanded.Nodes[0]
	if node.Type != "TABLE" ||
		node.DataSourceID != "source-1" ||
		node.TableID != "table-1" ||
		node.FileVersionID != "file-version-1" ||
		node.DatasetVersionID != "" {
		t.Fatalf("expanded node=%#v", node)
	}
	if len(node.Projection) != 3 ||
		node.Projection[0] != "节点组织编码" ||
		node.Projection[1] != "员工姓名" ||
		node.Projection[2] != "入职日期" ||
		node.SourceFilters[0].Field != "节点组织编码" {
		t.Fatalf("expanded projection/filter=%#v %#v", node.Projection, node.SourceFilters)
	}
	if expanded.Fields[0].Expression.Field != "节点组织编码" {
		t.Fatalf("expanded field expression=%#v", expanded.Fields[0].Expression)
	}
	if overrides["employee_ods"]["节点组织编码"] != "STRING" {
		t.Fatalf("type overrides=%#v", overrides)
	}
	dateRule := expanded.Transforms[0].Rules[0]
	if len(dateRule.InputKeys) != 1 ||
		dateRule.InputKeys[0] != "employee_ods.入职日期" ||
		dateRule.ReplaceSourceKey != "employee_ods.入职日期" ||
		len(dateRule.Expression.Arguments) != 2 ||
		dateRule.Expression.Arguments[0].Field != "入职日期" ||
		dateRule.Expression.Arguments[1].Type != "CURRENT_DATE" {
		t.Fatalf("expanded date transform=%#v", dateRule)
	}
	if err := dataset.Validate(expanded); err != nil {
		if validation, ok := err.(*dataset.ValidationError); ok {
			t.Fatalf(
				"expanded execution document is invalid: %#v",
				validation.Issues,
			)
		}
		t.Fatalf("expanded execution document is invalid: %v", err)
	}
}

func TestExpandVirtualODSDocumentRejectsBusinessTransformationInODS(t *testing.T) {
	outer := dataset.Document{
		Nodes: []dataset.Node{{
			ID: "ods", Type: "DATASET", Projection: []string{"code"},
		}},
	}
	ods := dataset.Document{
		Dataset: dataset.Descriptor{Layer: dataset.LayerODS},
		Nodes: []dataset.Node{{
			ID: "source", Type: "TABLE",
		}},
		Fields: []dataset.Field{{
			Code: "code",
			Expression: dataset.Expression{
				Type: "FUNCTION", Function: "LOWER",
			},
		}},
	}
	_, _, err := expandVirtualODSDocument(
		outer, map[string]dataset.Document{"ods": ods},
	)
	if !errors.Is(err, dataset.ErrPreviewUnsupported) {
		t.Fatalf("error=%v, want preview unsupported", err)
	}
}

func TestEditablePreviewUsesHundredRowsOnlyThroughDWD(t *testing.T) {
	for _, layer := range []dataset.Layer{
		dataset.LayerODS, dataset.LayerDIM, dataset.LayerDWD,
	} {
		if limit := editablePreviewRowLimit(dataset.Document{
			Dataset: dataset.Descriptor{Layer: layer},
		}); limit != 100 {
			t.Fatalf("layer %s preview limit=%d", layer, limit)
		}
	}
	for _, layer := range []dataset.Layer{
		dataset.LayerDWS, dataset.LayerADS,
	} {
		if limit := editablePreviewRowLimit(dataset.Document{
			Dataset: dataset.Descriptor{Layer: layer},
		}); limit != 5 {
			t.Fatalf("layer %s preview limit=%d", layer, limit)
		}
	}
}
