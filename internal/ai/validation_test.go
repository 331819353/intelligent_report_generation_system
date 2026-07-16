package ai

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"
)

func TestValidateProviderRequestAcceptsTextAndHTTPSImage(t *testing.T) {
	request := validProviderRequest()
	request.Messages = append([]Message{{
		Role:  MessageRoleSystem,
		Parts: []ContentPart{{Type: ContentTypeText, Text: "只返回结构化结果"}},
	}}, request.Messages...)
	request.Messages[1].Parts = append(request.Messages[1].Parts, ContentPart{
		Type:        ContentTypeImageURL,
		ImageURL:    "https://assets.example.test/report.png?signature=safe",
		ImageDetail: ImageDetailHigh,
	})
	if err := ValidateProviderRequest(request); err != nil {
		t.Fatal(err)
	}
}

func TestValidateProviderRequestRejectsUnsafeImageURLs(t *testing.T) {
	for _, imageURL := range []string{
		"http://assets.example.test/report.png",
		"file:///tmp/report.png",
		"data:image/png;base64,AAAA",
		"https://user:password@assets.example.test/report.png",
		"https://assets.example.test/report.png#credential-fragment",
		" https://assets.example.test/report.png",
	} {
		t.Run(imageURL, func(t *testing.T) {
			request := validProviderRequest()
			request.Messages[0].Parts = []ContentPart{{Type: ContentTypeImageURL, ImageURL: imageURL}}
			requireProviderError(t, ValidateProviderRequest(request), ErrorCodeInvalidRequest)
		})
	}
}

func TestValidateProviderRequestRejectsNonStrictSchemas(t *testing.T) {
	tests := map[string]json.RawMessage{
		"非对象":        json.RawMessage(`[]`),
		"尾随值":        json.RawMessage(`{"type":"object"} {}`),
		"重复键":        json.RawMessage(`{"type":"object","type":"object","properties":{},"required":[],"additionalProperties":false}`),
		"允许额外字段":     json.RawMessage(`{"type":"object","properties":{},"required":[],"additionalProperties":true}`),
		"可选属性":       json.RawMessage(`{"type":"object","properties":{"name":{"type":"string"}},"required":[],"additionalProperties":false}`),
		"未实现条件关键字":   schemaWithSingleProperty(`{"type":"string","if":{"const":"a"},"then":{"const":"b"}}`),
		"未实现数组关键字":   schemaWithSingleProperty(`{"type":"array","items":{"type":"string"},"contains":{"const":"a"}}`),
		"未知格式关键字":    schemaWithSingleProperty(`{"type":"string","format":"date"}`),
		"长度约束类型错误":   schemaWithSingleProperty(`{"type":"string","maxLength":"1"}`),
		"数值约束类型错误":   schemaWithSingleProperty(`{"type":"number","minimum":"10"}`),
		"数组缺少元素合同":   schemaWithSingleProperty(`{"type":"array"}`),
		"对象关键字缺少类型":  schemaWithSingleProperty(`{"properties":{},"required":[],"additionalProperties":false}`),
		"类型声明为 null": schemaWithSingleProperty(`{"type":null}`),
		"引用任意常量对象": json.RawMessage(`{
			"type":"object","additionalProperties":false,"required":["value"],
			"properties":{"value":{"$ref":"#/const"}},"const":{"enum":"bad"}
		}`),
	}
	for name, raw := range tests {
		t.Run(name, func(t *testing.T) {
			request := validProviderRequest()
			request.ResponseSchema.Schema = raw
			requireProviderError(t, ValidateProviderRequest(request), ErrorCodeInvalidRequest)
		})
	}
}

func TestValidateStructuredOutputBoundsRecursiveCompositions(t *testing.T) {
	schema := JSONSchema{Name: "recursive_composition", Schema: json.RawMessage(`{
		"type":"object",
		"additionalProperties":false,
		"required":["node"],
		"properties":{"node":{"$ref":"#/$defs/loop"}},
		"$defs":{"loop":{"oneOf":[{"$ref":"#/$defs/loop"},{"$ref":"#/$defs/loop"}]}}
	}`)}
	_, err := ValidateStructuredOutput(schema, []byte(`{"node":"value"}`))
	requireProviderError(t, err, ErrorCodeInvalidOutput)
}

func TestValidateStructuredOutputUsesJSONSchemaNumericEquality(t *testing.T) {
	schema := JSONSchema{Name: "numeric_equality", Schema: json.RawMessage(`{
		"type":"object","additionalProperties":false,"required":["values"],
		"properties":{"values":{"type":"array","uniqueItems":true,"items":{"type":"number"}}}
	}`)}
	_, err := ValidateStructuredOutput(schema, []byte(`{"values":[1,1.0]}`))
	requireProviderError(t, err, ErrorCodeInvalidOutput)
}

func TestValidateStructuredOutputReturnsCanonicalJSON(t *testing.T) {
	content, err := ValidateStructuredOutput(validJSONSchema(), []byte(` { "tags" : ["a"], "name" : "月报", "count" : 2 } `))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(content), `{"count":2,"name":"月报","tags":["a"]}`; got != want {
		t.Fatalf("content = %s, want %s", got, want)
	}
}

func TestValidateStructuredOutputRejectsSchemaViolations(t *testing.T) {
	tests := map[string]string{
		"未知字段":    `{"name":"月报","count":2,"tags":["a"],"invented":true}`,
		"缺少字段":    `{"name":"月报","count":2}`,
		"错误类型":    `{"name":"月报","count":2.5,"tags":["a"]}`,
		"非法枚举":    `{"name":"月报","count":2,"tags":["c"]}`,
		"重复数组":    `{"name":"月报","count":2,"tags":["a","a"]}`,
		"极端指数":    `{"name":"月报","count":1e1000000000,"tags":["a"]}`,
		"尾随 JSON": `{"name":"月报","count":2,"tags":["a"]} {}`,
		"重复对象键":   `{"name":"月报","name":"季报","count":2,"tags":["a"]}`,
	}
	for name, content := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := ValidateStructuredOutput(validJSONSchema(), []byte(content))
			requireProviderError(t, err, ErrorCodeInvalidOutput)
		})
	}
	_, err := ValidateStructuredOutput(validJSONSchema(), []byte{'{', '"', 'n', 'a', 'm', 'e', '"', ':', '"', 0xff, '"', '}'})
	requireProviderError(t, err, ErrorCodeInvalidOutput)
}

func TestValidateStructuredOutputBoundsRecursiveReferences(t *testing.T) {
	schema := JSONSchema{Name: "recursive", Schema: json.RawMessage(`{
		"type":"object",
		"additionalProperties":false,
		"required":["node"],
		"properties":{"node":{"$ref":"#/$defs/node"}},
		"$defs":{"node":{"$ref":"#/$defs/node"}}
	}`)}
	_, err := ValidateStructuredOutput(schema, []byte(`{"node":{}}`))
	requireProviderError(t, err, ErrorCodeInvalidOutput)
}

func TestNormalizeProviderErrorAndClassification(t *testing.T) {
	timeout := NormalizeProviderError(context.DeadlineExceeded)
	if timeout.Code != ErrorCodeTimeout || !timeout.Retryable || !errors.Is(timeout, context.DeadlineExceeded) {
		t.Fatalf("timeout = %#v", timeout)
	}
	canceled := NormalizeProviderError(context.Canceled)
	if canceled.Code != ErrorCodeCanceled || canceled.Retryable || !errors.Is(canceled, context.Canceled) {
		t.Fatalf("canceled = %#v", canceled)
	}
	original := newProviderError(ErrorCodeRateLimited, "safe", 429, true, 3*time.Second, nil)
	if got := NormalizeProviderError(original); got != original {
		t.Fatal("provider error identity was not preserved")
	}
	if got, want := ClassifyError(original), (ErrorClassification{Code: ErrorCodeRateLimited, Retryable: true, RetryAfter: 3 * time.Second}); !reflect.DeepEqual(got, want) {
		t.Fatalf("classification = %#v, want %#v", got, want)
	}
	if NormalizeProviderError(nil) != nil {
		t.Fatal("nil error was not preserved")
	}
}

func validProviderRequest() ProviderRequest {
	temperature := 0.0
	return ProviderRequest{
		Messages: []Message{{
			Role:  MessageRoleUser,
			Parts: []ContentPart{{Type: ContentTypeText, Text: "生成月报"}},
		}},
		ResponseSchema:  validJSONSchema(),
		Temperature:     &temperature,
		MaxOutputTokens: 512,
	}
}

func validJSONSchema() JSONSchema {
	return JSONSchema{
		Name:        "report_outline",
		Description: "报告提纲",
		Schema: json.RawMessage(`{
			"type":"object",
			"additionalProperties":false,
			"required":["name","count","tags"],
			"properties":{
				"name":{"type":"string","minLength":1,"maxLength":20},
				"count":{"type":"integer","minimum":0,"maximum":10},
				"tags":{"type":"array","maxItems":2,"uniqueItems":true,"items":{"type":"string","enum":["a","b"]}}
			}
		}`),
	}
}

func schemaWithSingleProperty(propertySchema string) json.RawMessage {
	return json.RawMessage(`{"type":"object","additionalProperties":false,"required":["value"],"properties":{"value":` + propertySchema + `}}`)
}

func requireProviderError(t *testing.T, err error, code ErrorCode) *ProviderError {
	t.Helper()
	if err == nil {
		t.Fatalf("expected %s, got nil", code)
	}
	var providerErr *ProviderError
	if !errors.As(err, &providerErr) {
		t.Fatalf("error = %T %v, want *ProviderError", err, err)
	}
	if providerErr.Code != code {
		t.Fatalf("error code = %s, want %s", providerErr.Code, code)
	}
	return providerErr
}
