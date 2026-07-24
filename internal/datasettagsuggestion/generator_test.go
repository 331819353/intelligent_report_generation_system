package datasettagsuggestion

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	aiplatform "intelligent-report-generation-system/internal/ai"
	"intelligent-report-generation-system/internal/dataset"
)

type fakeInvoker struct {
	configured bool
	content    json.RawMessage
	err        error
	invocation aiplatform.Invocation
}

func (invoker *fakeInvoker) Configured() bool { return invoker.configured }

func (invoker *fakeInvoker) Invoke(
	_ context.Context,
	invocation aiplatform.Invocation,
) (aiplatform.InvocationResult, error) {
	invoker.invocation = invocation
	if invoker.err != nil {
		return aiplatform.InvocationResult{}, invoker.err
	}
	return aiplatform.InvocationResult{
		RequestID: uuid.NewString(),
		ProviderResult: aiplatform.ProviderResult{
			Content: invoker.content,
		},
	}, nil
}

func testClaim() Claim {
	return Claim{
		ID: uuid.NewString(), TenantID: uuid.NewString(),
		DatasetID: uuid.NewString(), DatasetVersionID: uuid.NewString(),
		SchemaHash: strings.Repeat("a", 64), Layer: "ODS",
		PromptVersion: PromptVersion, ActorID: uuid.NewString(),
		LeaseToken: uuid.NewString(), Attempt: 1, MaxAttempts: 3,
	}
}

func testInput(tags ...TaxonomyTag) Input {
	return Input{
		Dataset: DatasetContext{
			Code: "orders", Name: "订单明细", Description: "订单业务明细",
			Layer: "ODS", Type: "SINGLE_SOURCE", VersionID: uuid.NewString(),
			SchemaHash: strings.Repeat("a", 64),
		},
		Fields: []FieldContext{{
			ID: "order_id", Code: "order_id", Name: "订单编号",
			Description: "订单唯一标识；关联订单实体的主键",
			Role:        "DIMENSION", CanonicalType: "STRING", SemanticType: "IDENTIFIER",
			Expression: "orders.order_id",
		}},
		DAG: DAGContext{
			Nodes: []NodeContext{{
				ID: "orders", Type: "TABLE", Alias: "o",
				Projection: []string{"order_id"},
			}},
			OutputGrain: "一行一笔订单", OutputKeys: []string{"order_id"},
		},
		SourceTables: []SourceTableContext{{
			ID: uuid.NewString(), DataSourceType: "MYSQL",
			SchemaName: "sales", TableName: "orders", TableType: "TABLE",
			BusinessDescription: "订单明细事实表",
			Tags:                []string{"作用:事实表"},
			Columns: []SourceColumnContext{{
				Name: "order_id", CanonicalType: "STRING",
				BusinessDescription: "订单唯一标识", PrimaryKey: true,
			}},
		}},
		Taxonomy: tags,
	}
}

func TestGeneratorUsesControlledTaxonomyAndProducesDeterministicSuggestions(t *testing.T) {
	tagA := TaxonomyTag{
		ID: uuid.NewString(), Code: "fact_table", Name: "事实表",
		Category: "TABLE_FUNCTION", Description: "保存业务事实",
	}
	tagB := TaxonomyTag{
		ID: uuid.NewString(), Code: "order_entity", Name: "订单",
		Category: "BUSINESS_ENTITY", Description: "订单业务实体",
	}
	raw, err := json.Marshal(map[string]any{"items": []map[string]any{
		{"tagId": tagA.ID, "confidence": 0.93, "rationale": "表粒度和主键元数据支持事实表判断"},
		{"tagId": tagB.ID, "confidence": 0.96, "rationale": "字段说明明确关联订单实体"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	invoker := &fakeInvoker{configured: true, content: raw}
	claim := testClaim()
	input := testInput(tagA, tagB)
	input.Dataset.VersionID = claim.DatasetVersionID
	completion, err := NewGenerator(invoker, time.Second).Generate(
		context.Background(), claim, input,
	)
	if err != nil {
		t.Fatal(err)
	}
	if completion.AIRequestID == "" || len(completion.Suggestions) != 2 ||
		completion.Suggestions[0].Category != "BUSINESS_ENTITY" ||
		completion.Suggestions[1].Category != "TABLE_FUNCTION" ||
		!validCompletion(completion) {
		t.Fatalf("completion=%+v", completion)
	}
	if invoker.invocation.Purpose != aiplatform.PurposeDatasetTagSuggestion ||
		invoker.invocation.ResourceID != claim.DatasetVersionID ||
		invoker.invocation.PromptVersion != PromptVersion {
		t.Fatalf("invocation=%+v", invoker.invocation)
	}
	if err := aiplatform.ValidateProviderRequest(invoker.invocation.Request); err != nil {
		t.Fatalf("provider request must use a valid strict schema: %v", err)
	}
	userPayload := invoker.invocation.Request.Messages[1].Parts[0].Text
	for _, forbidden := range []string{`"rows"`, `"sampleRows"`, `"rawData"`, `"password"`} {
		if strings.Contains(userPayload, forbidden) {
			t.Fatalf("prompt contains forbidden business-row/credential key %q", forbidden)
		}
	}
}

func TestGeneratorRejectsDuplicateOrUnknownProviderTags(t *testing.T) {
	tag := TaxonomyTag{
		ID: uuid.NewString(), Code: "fact_table", Name: "事实表",
		Category: "TABLE_FUNCTION",
	}
	for name, raw := range map[string]string{
		"duplicate": `{"items":[` +
			`{"tagId":"` + tag.ID + `","confidence":0.9,"rationale":"a"},` +
			`{"tagId":"` + tag.ID + `","confidence":0.8,"rationale":"b"}]}`,
		"unknown": `{"items":[{"tagId":"` + uuid.NewString() +
			`","confidence":0.9,"rationale":"a"}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			invoker := &fakeInvoker{
				configured: true, content: json.RawMessage(raw),
			}
			_, err := NewGenerator(invoker, time.Second).Generate(
				context.Background(), testClaim(), testInput(tag),
			)
			if !errors.Is(err, ErrInvalidRequest) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestExpressionShapeNeverIncludesLiteralValues(t *testing.T) {
	expression := dataset.Expression{
		Type: "EQUALS",
		Left: &dataset.Expression{
			Type: "FIELD_REF", NodeID: "orders", Field: "status",
		},
		Right: &dataset.Expression{Type: "LITERAL", Value: "secret-row-value"},
	}
	shape := expressionShape(expression, 0)
	if strings.Contains(shape, "secret-row-value") || !strings.Contains(shape, "LITERAL") {
		t.Fatalf("unsafe expression shape %q", shape)
	}
}

func TestSuggestionSchemaAllowsEvidenceDrivenCountWithinSafetyCap(t *testing.T) {
	tags := make([]TaxonomyTag, MaxSuggestions+10)
	for index := range tags {
		tags[index] = TaxonomyTag{
			ID: uuid.NewString(), Code: "tag_" + uuid.NewString(),
			Name: "标签", Category: "USAGE_SCOPE",
		}
	}
	raw, err := suggestionSchema(tags)
	if err != nil {
		t.Fatal(err)
	}
	var schema map[string]any
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatal(err)
	}
	properties := schema["properties"].(map[string]any)
	items := properties["items"].(map[string]any)
	if got := int(items["maxItems"].(float64)); got != MaxSuggestions {
		t.Fatalf("maxItems=%d", got)
	}
}
