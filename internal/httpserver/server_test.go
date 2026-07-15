package httpserver

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"intelligent-report-generation-system/internal/config"
)

func TestHealthEndpoints(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	server := New(config.Config{
		HTTPAddr: ":0",
	}, logger)

	for _, test := range []struct {
		path string
		want string
	}{
		{"/health/live", "live"},
		{"/health/ready", "ready"},
	} {
		t.Run(test.path, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, test.path, nil)
			response := httptest.NewRecorder()
			server.Handler().ServeHTTP(response, request)

			if response.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", response.Code)
			}
			if response.Header().Get(requestIDHeader) == "" {
				t.Fatal("missing request ID response header")
			}
			var body map[string]string
			if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if body["status"] != test.want {
				t.Fatalf("status body = %q, want %q", body["status"], test.want)
			}
		})
	}
}
