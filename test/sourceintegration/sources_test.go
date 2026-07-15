//go:build sourceintegration

package sourceintegration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"testing"
	"time"

	"intelligent-report-generation-system/internal/spike/federation"
)

type result struct {
	Columns  []string `json:"columns"`
	Rows     [][]any  `json:"rows"`
	RowCount int      `json:"rowCount"`
}

func TestPythonConnectorMySQLOracleAndGoJoin(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	mysql := query(t, ctx, map[string]any{"source_type": "MYSQL", "host": "mysql", "port": 3306, "database": "report_source", "username": "report_reader", "password": "local_mysql_reader_password"}, "SELECT customer_id,customer_name,region_code FROM customers ORDER BY customer_id")
	oracle := query(t, ctx, map[string]any{"source_type": "ORACLE", "host": "oracle", "port": 1521, "database": "FREEPDB1", "username": "report_reader", "password": "local_oracle_reader_password"}, "SELECT order_id,customer_id,amount FROM orders ORDER BY order_id")
	customers := make([]federation.Row, 0, len(mysql.Rows))
	for _, row := range mysql.Rows {
		customers = append(customers, federation.Row{"customer_id": row[0], "customer_name": row[1], "region_code": row[2]})
	}
	orders := make([]federation.Row, 0, len(oracle.Rows))
	for _, row := range oracle.Rows {
		orders = append(orders, federation.Row{"order_id": row[0], "customer_id": row[1], "amount": row[2]})
	}
	joined, err := federation.HashJoin(orders, customers, "customer_id", "customer_id", 100)
	if err != nil || len(joined) != 3 {
		t.Fatalf("cross-source join rows=%d err=%v", len(joined), err)
	}
}

func query(t *testing.T, ctx context.Context, connection map[string]any, sql string) result {
	t.Helper()
	payload, _ := json.Marshal(map[string]any{"connection": connection, "sql": sql, "max_rows": 100})
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, env("CONNECTOR_SERVICE_URL", "http://127.0.0.1:8090")+"/v1/query", bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Connector-Token", env("CONNECTOR_INTERNAL_TOKEN", "local_connector_token_change_me"))
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("connector query status=%s", response.Status)
	}
	var out result
	if err := json.NewDecoder(response.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.RowCount == 0 {
		t.Fatal(fmt.Errorf("connector returned no rows"))
	}
	return out
}
func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
