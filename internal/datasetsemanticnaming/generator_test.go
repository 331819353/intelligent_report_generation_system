package datasetsemanticnaming

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	aiplatform "intelligent-report-generation-system/internal/ai"
	"intelligent-report-generation-system/internal/dataset"
)

type namingTestCatalog struct {
	context Context
}

func (catalog namingTestCatalog) Load(
	context.Context, string, dataset.Document,
) (Context, error) {
	return catalog.context, nil
}

type namingTestInvoker struct {
	invocation aiplatform.Invocation
	content    json.RawMessage
}

func (invoker *namingTestInvoker) Configured() bool { return true }

func (invoker *namingTestInvoker) Invoke(
	_ context.Context,
	invocation aiplatform.Invocation,
) (aiplatform.InvocationResult, error) {
	invoker.invocation = invocation
	return aiplatform.InvocationResult{
		RequestID:      "550e8400-e29b-41d4-a716-446655440099",
		ProviderResult: aiplatform.ProviderResult{Content: invoker.content},
	}, nil
}

func TestGeneratorIncludesDWDInSaveTimeSemanticNaming(t *testing.T) {
	tag := TaxonomyTag{
		ID:   "550e8400-e29b-41d4-a716-446655440000",
		Code: "system.entity.employee", Name: "主题:员工",
		Category: "BUSINESS_ENTITY",
	}
	content, err := json.Marshal(providerOutput{
		Dataset: DatasetNaming{
			Code: "dwd_employee_detail", Name: "员工明细事实表",
			Description: "员工基础属性明细",
		},
		Fields: map[string]FieldNaming{
			"field_employee_id": {
				Code: "employee_id", Name: "员工编码", Description: "员工唯一编码",
			},
		},
		Tags: []providerTagChoice{{
			TagID: tag.ID, Confidence: 0.98, Rationale: "员工基础明细语义",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	invoker := &namingTestInvoker{content: content}
	generator := NewGenerator(
		namingTestCatalog{context: Context{Taxonomy: []TaxonomyTag{tag}}},
		invoker, 0,
	)
	result, err := generator.Enrich(
		context.Background(), "tenant-1", "actor-1", "draft-1",
		dataset.Document{
			Dataset: dataset.Descriptor{
				Code: "draft_dwd", Name: "待命名", Type: "SINGLE_SOURCE",
				Layer: dataset.LayerDWD,
			},
			Fields: []dataset.Field{{
				ID: "field_employee_id", Code: "field_1", Name: "字段1",
			}},
		},
		false,
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.Document.Dataset.Name != "员工明细事实表" ||
		result.Document.Fields[0].Code != "employee_id" ||
		invoker.invocation.Purpose != aiplatform.PurposeDatasetSemanticNaming ||
		invoker.invocation.PromptVersion != PromptVersion {
		t.Fatalf("DWD semantic naming result=%#v invocation=%#v", result, invoker.invocation)
	}
}

func TestApplyOutputRenamesAggregateAndDesignerWithoutChangingIdentity(t *testing.T) {
	document := dataset.Document{
		Dataset: dataset.Descriptor{
			Code: "dataset_old", Name: "用户信息聚合表",
			Description: "员工各维度人数", Type: "SINGLE_SOURCE",
			Layer: dataset.LayerDWS, Domain: "企业", Subject: "企业画像",
		},
		Fields: []dataset.Field{
			{
				ID: "field_gender", Code: "t1_field_1", Name: "性别",
				Role: "DIMENSION", CanonicalType: "STRING",
			},
			{
				ID: "field_birth_count", Code: "t1_field_2_metric",
				Name: "出生日期指标", Role: "MEASURE", CanonicalType: "INTEGER",
			},
		},
		OutputGrain: dataset.OutputGrain{
			Description: "每行一个性别", KeyFields: []string{"t1_field_1"},
		},
		AnalysisContract: &dataset.AnalysisContract{
			Intent: "PROFILE", CommonGrainFields: []string{"t1_field_1"},
			Measures: []dataset.AnalysisMeasureContract{{
				Field: "t1_field_2_metric", Aggregation: "COUNT",
			}},
		},
		Transforms: []dataset.Transform{{
			ID: "transform_1", Name: "出生日期指标转换",
			Rules: []dataset.TransformRule{{
				ID: "rule_1",
				Output: dataset.TransformOutput{
					ID: "output_1", Code: "t1_field_2_metric",
					Name: "出生日期指标", CanonicalType: "INTEGER",
				},
			}},
		}},
		Designer: map[string]any{
			"end": map[string]any{"outputs": []any{
				map[string]any{"key": "group_1.metric", "code": "t1_field_2_metric", "name": "出生日期指标"},
			}},
			"groups": []any{map[string]any{"metrics": []any{
				map[string]any{"code": "t1_field_2_metric", "name": "出生日期指标"},
			}}},
		},
	}
	taxonomy := []TaxonomyTag{{
		ID:   "550e8400-e29b-41d4-a716-446655440000",
		Code: "system.entity.employee", Name: "主题:员工",
		Category: "BUSINESS_ENTITY",
	}}
	output := providerOutput{
		Dataset: DatasetNaming{
			Code: "dws_employee_profile", Name: "员工画像汇总",
			Description: "按员工属性统计出生日期非空记录数",
		},
		Fields: map[string]FieldNaming{
			"field_gender": {Code: "gender", Name: "性别", Description: "员工性别"},
			"field_birth_count": {
				Code: "birth_date_non_null_count",
				Name: "出生日期非空记录数", Description: "出生日期不为空的员工记录数量",
			},
		},
		Tags: []providerTagChoice{{
			TagID: taxonomy[0].ID, Confidence: 0.98, Rationale: "上游与输出字段均为员工画像语义",
		}},
	}

	renamed, tags, err := applyOutput(document, output, taxonomy, false)
	if err != nil {
		t.Fatal(err)
	}
	if renamed.Dataset.Code != "dws_employee_profile" ||
		renamed.Dataset.Name != "员工画像汇总" {
		t.Fatalf("dataset naming=%#v", renamed.Dataset)
	}
	if renamed.Dataset.Domain != "企业" || renamed.Dataset.Subject != "企业画像" {
		t.Fatalf("governed domain/subject changed: %#v", renamed.Dataset)
	}
	if renamed.Fields[1].ID != "field_birth_count" ||
		renamed.Fields[1].Code != "birth_date_non_null_count" ||
		renamed.Fields[1].Name != "出生日期非空记录数" {
		t.Fatalf("aggregate field naming=%#v", renamed.Fields[1])
	}
	if renamed.OutputGrain.KeyFields[0] != "gender" ||
		renamed.AnalysisContract.CommonGrainFields[0] != "gender" ||
		renamed.AnalysisContract.Measures[0].Field != "birth_date_non_null_count" {
		t.Fatalf("semantic references were not remapped: %#v %#v",
			renamed.OutputGrain, renamed.AnalysisContract)
	}
	if renamed.Transforms[0].Rules[0].Output.Code != "birth_date_non_null_count" ||
		renamed.Transforms[0].Rules[0].Output.Name != "出生日期非空记录数" {
		t.Fatalf("transform output was not remapped: %#v",
			renamed.Transforms[0].Rules[0].Output)
	}
	end := renamed.Designer["end"].(map[string]any)
	outputItem := end["outputs"].([]any)[0].(map[string]any)
	if outputItem["code"] != "birth_date_non_null_count" ||
		outputItem["name"] != "出生日期非空记录数" {
		t.Fatalf("designer output was not remapped: %#v", outputItem)
	}
	if len(tags) != 1 || tags[0].TagCode != "system.entity.employee" {
		t.Fatalf("controlled tags=%#v", tags)
	}
}

func TestApplyOutputRejectsDuplicateFieldCodeAndLockedDatasetCode(t *testing.T) {
	document := dataset.Document{
		Dataset: dataset.Descriptor{Code: "dws_locked", Name: "原名称"},
		Fields: []dataset.Field{
			{ID: "field_a", Code: "field_a"},
			{ID: "field_b", Code: "field_b"},
		},
	}
	output := providerOutput{
		Dataset: DatasetNaming{Code: "dws_changed", Name: "新名称"},
		Fields: map[string]FieldNaming{
			"field_a": {Code: "duplicate_code", Name: "字段A"},
			"field_b": {Code: "duplicate_code", Name: "字段B"},
		},
	}
	renamed, _, err := applyOutput(document, output, nil, false)
	if err != nil {
		t.Fatalf("duplicate code should be deterministically disambiguated: %v", err)
	}
	if renamed.Fields[0].Code != "duplicate_code" || renamed.Fields[1].Code != "duplicate_code_2" {
		t.Fatalf("disambiguated codes=%#v", renamed.Fields)
	}
	output.Fields["field_b"] = FieldNaming{Code: "field_b_named", Name: "字段B"}
	if _, _, err := applyOutput(document, output, nil, true); !errors.Is(err, dataset.ErrSemanticNamingInvalid) {
		t.Fatalf("locked dataset code error=%v", err)
	}
}
