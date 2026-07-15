package datasource

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

type staticSecrets map[string]string

func (s staticSecrets) Resolve(context.Context, string) (map[string]string, error) { return s, nil }

func TestOracleConnectionPayloadIncludesSafeOptionsAndQuota(t *testing.T) {
	connector := NewPythonConnector(TypeOracle, "http://connector", "token", staticSecrets{
		"host": "oracle", "port": "1521", "database": "FREEPDB1", "username": "reader", "password": "secret",
	})
	payload, err := connector.connection(context.Background(), Source{
		ID: "source-id", TenantID: "tenant-id", Type: TypeOracle, SecretRef: "env://ORACLE",
		Config:       map[string]any{"oracleConnectMode": "SID", "schemas": []any{"APP", "BI"}},
		RuntimeQuota: Quota{MaxConnectionsPerSource: 3, MaxConcurrentQueries: 7},
	})
	if err != nil {
		t.Fatal(err)
	}
	if payload["oracle_connect_mode"] != "SID" || payload["max_connections_per_source"] != 3 || payload["max_concurrent_queries"] != 7 {
		t.Fatalf("unexpected payload: %#v", payload)
	}
	schemas, ok := payload["schemas"].([]string)
	if !ok || len(schemas) != 2 {
		t.Fatalf("unexpected schemas: %#v", payload["schemas"])
	}
}

func TestQueryAndCancelUseConnectorContract(t *testing.T) {
	paths := []string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if r.Header.Get("X-Connector-Token") != "internal-token" {
			t.Fatal("connector token is missing")
		}
		var input map[string]any
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/v1/query" {
			if input["query_id"] != "query-1" || input["sql"] != "SELECT %s" || input["max_rows"] != float64(10) {
				t.Fatalf("unexpected query payload: %#v", input)
			}
			_, _ = w.Write([]byte(`{"columns":["value"],"rows":[[9007199254740993]],"rowCount":1,"durationMs":2,"warnings":[{"code":"FORGED","message":"source text"}],"sourceStats":[{"nodeId":"forged","rowCount":999,"status":"SUCCEEDED"}]}`))
			return
		}
		if input["query_id"] != "query-1" {
			t.Fatalf("unexpected cancel payload: %#v", input)
		}
		_, _ = w.Write([]byte(`{"cancelled":true}`))
	}))
	defer server.Close()
	connector := NewPythonConnector(TypeMySQL, server.URL, "internal-token", staticSecrets{
		"host": "mysql", "port": "3306", "database": "app", "username": "reader", "password": "secret",
	})
	source := Source{ID: "source-1", TenantID: "tenant-1", Type: TypeMySQL, SecretRef: "env://MYSQL", RuntimeQuota: Quota{MaxConnectionsPerSource: 2, MaxConcurrentQueries: 3}}
	result, err := connector.Query(context.Background(), source, "query-1", "SELECT %s", []any{1}, 10)
	if err != nil || result.RowCount != 1 {
		t.Fatalf("Query() result=%#v err=%v", result, err)
	}
	value, ok := result.Rows[0][0].(json.Number)
	if !ok || value.String() != "9007199254740993" {
		t.Fatalf("large integer lost precision: %#v", result.Rows[0][0])
	}
	if len(result.Warnings) != 0 {
		t.Fatalf("remote connector warnings were trusted: %#v", result.Warnings)
	}
	if len(result.SourceStats) != 0 {
		t.Fatalf("remote connector source stats were trusted: %#v", result.SourceStats)
	}
	if cancelled, err := connector.Cancel(context.Background(), "query-1"); err != nil || !cancelled {
		t.Fatal(err)
	}
	if len(paths) != 2 || paths[0] != "/v1/query" || paths[1] != "/v1/query/cancel" {
		t.Fatalf("paths=%#v", paths)
	}
}
