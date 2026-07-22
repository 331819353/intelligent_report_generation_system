package embedding

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOpenAICompatibleProviderEmbedsAndOrdersVectors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/embeddings" || request.Header.Get("Authorization") != "Bearer secret" {
			t.Fatalf("unexpected request %s", request.URL.Path)
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"data":[{"index":1,"embedding":[0.3,0.4]},{"index":0,"embedding":[0.1,0.2]}]}`))
	}))
	defer server.Close()
	provider := NewOpenAICompatibleProvider(server.URL+"/v1", "secret", "embedding-model", 2, server.Client())
	vectors, err := provider.Embed(context.Background(), []string{"区域销售", "月度销售"})
	if err != nil {
		t.Fatal(err)
	}
	if len(vectors) != 2 || vectors[0][0] != float32(0.1) || vectors[1][1] != float32(0.4) {
		t.Fatalf("unexpected vectors %#v", vectors)
	}
}

func TestOpenAICompatibleProviderRejectsInvalidDimensionAndRedirect(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(writer, `{"data":[{"index":0,"embedding":[0.1]}]}`)
	}))
	defer server.Close()
	provider := NewOpenAICompatibleProvider(server.URL, "secret", "model", 2, server.Client())
	if _, err := provider.Embed(context.Background(), []string{"text"}); err == nil {
		t.Fatal("expected dimension mismatch")
	}
	redirect := httptest.NewServer(http.RedirectHandler(server.URL, http.StatusTemporaryRedirect))
	defer redirect.Close()
	provider = NewOpenAICompatibleProvider(redirect.URL, "secret", "model", 2, redirect.Client())
	if _, err := provider.Embed(context.Background(), []string{"text"}); err == nil || !strings.Contains(err.Error(), "status") {
		t.Fatalf("redirect error = %v", err)
	}
}
