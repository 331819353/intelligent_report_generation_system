package metadataai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestOpenAICompatibleProviderUsesStrictSchemaAndParsesUsage(t *testing.T) {
	input, output := validCompletion()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret" {
			t.Error("missing provider authorization")
		}
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		format := request["response_format"].(map[string]any)
		if format["type"] != "json_schema" {
			t.Errorf("response_format = %#v", format)
		}
		content, _ := json.Marshal(output)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"model":   "model-v1",
			"choices": []any{map[string]any{"message": map[string]any{"content": string(content)}}},
			"usage":   map[string]int{"prompt_tokens": 10, "completion_tokens": 20, "total_tokens": 30},
		})
	}))
	defer server.Close()
	provider := NewOpenAICompatibleProvider(server.URL, "secret", "model", server.Client())
	result, err := provider.Complete(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if result.Model != "model-v1" || result.Usage.TotalTokens != 30 {
		t.Fatalf("result = %#v", result)
	}
}

func TestOpenAICompatibleProviderRejectsUnknownStructuredFields(t *testing.T) {
	input, _ := validCompletion()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{"message": map[string]any{"content": `{"schemaVersion":"1.0","table":{},"columns":[],"invented":true}`}}},
		})
	}))
	defer server.Close()
	provider := NewOpenAICompatibleProvider(server.URL, "secret", "model", server.Client())
	if _, err := provider.Complete(context.Background(), input); err == nil {
		t.Fatal("unknown structured field was accepted")
	}
}
