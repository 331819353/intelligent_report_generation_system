package datasource

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestStreamQueryConsumesValidatedBatchesWithoutLosingIntegerPrecision(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/query/stream" {
			t.Fatalf("path=%s", r.URL.Path)
		}
		if r.Header.Get("Accept") != "application/x-ndjson" || r.Header.Get("X-Connector-Token") != "internal-token" {
			t.Fatalf("unexpected headers: %#v", r.Header)
		}
		var input map[string]any
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Fatal(err)
		}
		if input["query_id"] != "build-1:scan-1" || input["batch_size"] != float64(2) || input["max_rows"] != float64(10) {
			t.Fatalf("unexpected payload: %#v", input)
		}
		w.Header().Set("Content-Type", "application/x-ndjson")
		_, _ = w.Write([]byte(
			"{\"type\":\"schema\",\"columns\":[\"id\",\"name\"]}\n" +
				"{\"type\":\"batch\",\"rows\":[[9007199254740993,\"A\"],[2,\"B\"]]}\n" +
				"{\"type\":\"batch\",\"rows\":[[3,\"C\"]]}\n" +
				"{\"type\":\"complete\",\"rowCount\":3,\"durationMs\":12}\n",
		))
	}))
	defer server.Close()

	connector := NewPythonConnector(TypeMySQL, server.URL, "internal-token", staticSecrets{
		"host": "mysql", "port": "3306", "database": "app", "username": "reader", "password": "secret",
	})
	source := Source{ID: "source-1", TenantID: "tenant-1", Type: TypeMySQL, SecretRef: "env://MYSQL"}
	var batches []StreamBatch
	summary, err := connector.StreamQuery(
		context.Background(), source, "build-1:scan-1", "SELECT id, name FROM users", nil, 2, 10,
		func(batch StreamBatch) error {
			batches = append(batches, batch)
			return nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if summary.RowCount != 3 || summary.DurationMS != 12 || len(batches) != 2 {
		t.Fatalf("summary=%#v batches=%#v", summary, batches)
	}
	value, ok := batches[0].Rows[0][0].(json.Number)
	if !ok || value.String() != "9007199254740993" {
		t.Fatalf("large integer lost precision: %#v", batches[0].Rows[0][0])
	}
}

func TestStreamQueryRejectsIncompleteOrInconsistentEventSequences(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "batch before schema", body: "{\"type\":\"batch\",\"rows\":[[1]]}\n"},
		{name: "row width", body: "{\"type\":\"schema\",\"columns\":[\"a\",\"b\"]}\n{\"type\":\"batch\",\"rows\":[[1]]}\n"},
		{name: "count mismatch", body: "{\"type\":\"schema\",\"columns\":[\"a\"]}\n{\"type\":\"batch\",\"rows\":[[1]]}\n{\"type\":\"complete\",\"rowCount\":2,\"durationMs\":1}\n"},
		{name: "early eof", body: "{\"type\":\"schema\",\"columns\":[\"a\"]}\n"},
		{name: "remote error", body: "{\"type\":\"error\",\"code\":\"QUERY_ROW_LIMIT_EXCEEDED\"}\n"},
		{name: "trailing event", body: "{\"type\":\"schema\",\"columns\":[\"a\"]}\n{\"type\":\"complete\",\"rowCount\":0,\"durationMs\":1}\n{\"type\":\"batch\",\"rows\":[[1]]}\n"},
		{name: "case folded duplicate columns", body: "{\"type\":\"schema\",\"columns\":[\"id\",\"ID\"]}\n"},
		{name: "unnormalized column", body: "{\"type\":\"schema\",\"columns\":[\" id\"]}\n"},
		{name: "unknown event field", body: "{\"type\":\"schema\",\"columns\":[\"id\"],\"secret\":\"unexpected\"}\n"},
		{name: "batch exceeds requested size", body: "{\"type\":\"schema\",\"columns\":[\"id\"]}\n{\"type\":\"batch\",\"rows\":[[1],[2]]}\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/x-ndjson")
				_, _ = w.Write([]byte(test.body))
			}))
			defer server.Close()
			connector := NewPythonConnector(TypeMySQL, server.URL, "token", staticSecrets{
				"host": "mysql", "port": "3306", "database": "app", "username": "reader", "password": "secret",
			})
			_, err := connector.StreamQuery(
				context.Background(),
				Source{ID: "source-1", TenantID: "tenant-1", Type: TypeMySQL, SecretRef: "env://MYSQL"},
				"query-1", "SELECT 1", nil, 1, 10,
				func(StreamBatch) error { return nil },
			)
			if err == nil {
				t.Fatal("expected stream validation error")
			}
		})
	}
}

func TestStreamQueryStopsWhenConsumerFails(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		_, _ = w.Write([]byte("{\"type\":\"schema\",\"columns\":[\"a\"]}\n{\"type\":\"batch\",\"rows\":[[1]]}\n{\"type\":\"complete\",\"rowCount\":1,\"durationMs\":1}\n"))
	}))
	defer server.Close()
	connector := NewPythonConnector(TypeMySQL, server.URL, "token", staticSecrets{
		"host": "mysql", "port": "3306", "database": "app", "username": "reader", "password": "secret",
	})
	sentinel := errors.New("copy failed")
	_, err := connector.StreamQuery(
		context.Background(),
		Source{ID: "source-1", TenantID: "tenant-1", Type: TypeMySQL, SecretRef: "env://MYSQL"},
		"query-1", "SELECT 1", nil, 1, 10,
		func(StreamBatch) error { return sentinel },
	)
	if !errors.Is(err, sentinel) {
		t.Fatalf("err=%v", err)
	}
}

func TestStreamQueryRejectsMismatchedConnectorBeforeSendingCredentials(t *testing.T) {
	connector := NewPythonConnector(TypeMySQL, "http://connector.invalid", "token", staticSecrets{
		"host": "oracle", "port": "1521", "database": "app", "username": "reader", "password": "secret",
	})
	_, err := connector.StreamQuery(
		context.Background(),
		Source{ID: "source-1", TenantID: "tenant-1", Type: TypeOracle, SecretRef: "env://ORACLE"},
		"query-1", "SELECT 1", nil, 1, 10,
		func(StreamBatch) error { return nil },
	)
	if err == nil || err.Error() != "stream source type does not match the connector" {
		t.Fatalf("err=%v", err)
	}
}

func TestStreamQueryRejectsCellAndWholeResponseByteLimits(t *testing.T) {
	tests := []struct {
		name   string
		body   string
		limits func(ConnectorLimits) ConnectorLimits
		want   error
	}{
		{
			name: "cell",
			body: "{\"type\":\"schema\",\"columns\":[\"value\"]}\n" +
				"{\"type\":\"batch\",\"rows\":[[\"0123456789\"]]}\n" +
				"{\"type\":\"complete\",\"rowCount\":1,\"durationMs\":1}\n",
			limits: func(value ConnectorLimits) ConnectorLimits {
				value.MaxStreamCellBytes = 8
				value.MaxStreamRowBytes = 64
				return value
			},
			want: ErrConnectorResourceLimitExceeded,
		},
		{
			name: "whole response",
			body: "{\"type\":\"schema\",\"columns\":[\"value\"]}\n" +
				"{\"type\":\"batch\",\"rows\":[[\"0123456789\"]]}\n" +
				"{\"type\":\"complete\",\"rowCount\":1,\"durationMs\":1}\n",
			limits: func(value ConnectorLimits) ConnectorLimits {
				value.MaxStreamBytes = 64
				return value
			},
			want: ErrConnectorResponseBytesExceeded,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/x-ndjson")
				_, _ = w.Write([]byte(test.body))
			}))
			defer server.Close()
			connector := NewPythonConnectorWithLimits(
				TypeMySQL, server.URL, "token",
				staticSecrets{
					"host": "mysql", "port": "3306", "database": "app",
					"username": "reader", "password": "secret",
				},
				test.limits(DefaultConnectorLimits()),
			)
			_, err := connector.StreamQuery(
				context.Background(),
				Source{
					ID: "source-1", TenantID: "tenant-1",
					Type: TypeMySQL, SecretRef: "env://MYSQL",
				},
				"query-1", "SELECT value FROM items", nil, 1, 10,
				func(StreamBatch) error { return nil },
			)
			if !errors.Is(err, test.want) {
				t.Fatalf("err=%v want=%v", err, test.want)
			}
		})
	}
}

func TestStreamQueryBoundsSingleLineBeforeFixedScannerMaximum(t *testing.T) {
	const streamBudget = int64(1 << 20)
	oversizedLine := strings.Repeat("x", int(streamBudget)+1)
	server := httptest.NewServer(http.HandlerFunc(func(
		w http.ResponseWriter,
		_ *http.Request,
	) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		_, _ = io.WriteString(w, oversizedLine)
	}))
	defer server.Close()
	limits := DefaultConnectorLimits()
	limits.MaxStreamBytes = streamBudget
	connector := NewPythonConnectorWithLimits(
		TypeMySQL, server.URL, "token",
		staticSecrets{
			"host": "mysql", "port": "3306", "database": "app",
			"username": "reader", "password": "secret",
		},
		limits,
	)
	consumed := false
	_, err := connector.StreamQuery(
		context.Background(),
		Source{
			ID: "source-1", TenantID: "tenant-1",
			Type: TypeMySQL, SecretRef: "env://MYSQL",
		},
		"query-1", "SELECT value FROM items", nil, 1, 10,
		func(StreamBatch) error {
			consumed = true
			return nil
		},
	)
	if !errors.Is(err, ErrConnectorResponseBytesExceeded) {
		t.Fatalf("err=%v want=%v", err, ErrConnectorResponseBytesExceeded)
	}
	if consumed {
		t.Fatal("oversized single NDJSON event reached the consumer")
	}
}

func TestStreamQueryHonorsCancellationBeforeConsumingRows(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		<-r.Context().Done()
	}))
	defer server.Close()
	connector := NewPythonConnector(
		TypeMySQL, server.URL, "token",
		staticSecrets{
			"host": "mysql", "port": "3306", "database": "app",
			"username": "reader", "password": "secret",
		},
	)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := connector.StreamQuery(
		ctx,
		Source{
			ID: "source-1", TenantID: "tenant-1",
			Type: TypeMySQL, SecretRef: "env://MYSQL",
		},
		"query-1", "SELECT value FROM items", nil, 1, 10,
		func(StreamBatch) error { return nil },
	)
	if err == nil || called {
		t.Fatalf("err=%v called=%v", err, called)
	}
}

func TestStreamQueryDoesNotEchoUntrustedRemoteErrorCode(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		_, _ = w.Write([]byte(
			"{\"type\":\"error\",\"code\":\"password=must-not-leak\"}\n",
		))
	}))
	defer server.Close()
	connector := NewPythonConnector(
		TypeMySQL, server.URL, "token",
		staticSecrets{
			"host": "mysql", "port": "3306", "database": "app",
			"username": "reader", "password": "secret",
		},
	)
	_, err := connector.StreamQuery(
		context.Background(),
		Source{
			ID: "source-1", TenantID: "tenant-1",
			Type: TypeMySQL, SecretRef: "env://MYSQL",
		},
		"query-1", "SELECT 1", nil, 1, 10,
		func(StreamBatch) error { return nil },
	)
	if err == nil || strings.Contains(err.Error(), "must-not-leak") ||
		!strings.Contains(err.Error(), "QUERY_FAILED") {
		t.Fatalf("err=%v", err)
	}
}
