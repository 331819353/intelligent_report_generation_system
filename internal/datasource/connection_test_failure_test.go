package datasource

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSafeConnectionTestFailure(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		ctxErr    error
		testErr   error
		source    Type
		wantCode  string
		wantRetry bool
	}{
		{name: "timeout", ctxErr: context.DeadlineExceeded, source: TypeMySQL, wantCode: "CONNECTION_TIMEOUT", wantRetry: true},
		{name: "auth", testErr: &ConnectorServiceError{Code: "CONNECTION_AUTH_FAILED"}, source: TypeMySQL, wantCode: "CONNECTION_AUTH_FAILED"},
		{name: "dns", testErr: &ConnectorServiceError{Code: "CONNECTION_DNS_FAILED"}, source: TypeOracle, wantCode: "CONNECTION_DNS_FAILED", wantRetry: true},
		{name: "file", testErr: errors.New("opaque"), source: TypeExcel, wantCode: "FILE_UNAVAILABLE"},
		{name: "unknown", testErr: errors.New("driver secret"), source: TypeMySQL, wantCode: "CONNECTION_FAILED"},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			code, retry := safeConnectionTestFailure(test.ctxErr, test.testErr, test.source)
			if code != test.wantCode || retry != test.wantRetry {
				t.Fatalf("got (%q,%v), want (%q,%v)", code, retry, test.wantCode, test.wantRetry)
			}
		})
	}
}

func TestConnectorCallOnlyExposesAllowlistedFailureCode(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		detail    string
		wantCode  string
		forbidden string
	}{
		{name: "stable code", detail: "CONNECTION_AUTH_FAILED", wantCode: "CONNECTION_AUTH_FAILED"},
		{name: "driver detail hidden", detail: "password=secret host=db.internal", forbidden: "secret"},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
				response.Header().Set("Content-Type", "application/json")
				response.WriteHeader(http.StatusBadGateway)
				_, _ = response.Write([]byte(`{"detail":"` + test.detail + `"}`))
			}))
			defer server.Close()
			connector := NewPythonConnector(TypeMySQL, server.URL, "token", nil)
			err := connector.call(context.Background(), "/v1/connections/test", map[string]string{"safe": "value"}, &map[string]any{})
			if test.wantCode != "" {
				var serviceError *ConnectorServiceError
				if !errors.As(err, &serviceError) || serviceError.Code != test.wantCode {
					t.Fatalf("got %v, want stable code %q", err, test.wantCode)
				}
			}
			if test.forbidden != "" && strings.Contains(err.Error(), test.forbidden) {
				t.Fatalf("connector error leaked remote detail: %v", err)
			}
		})
	}
}
