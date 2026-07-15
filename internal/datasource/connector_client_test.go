package datasource

import (
	"context"
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
