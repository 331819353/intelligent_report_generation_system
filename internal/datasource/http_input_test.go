package datasource

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

func testCredentialManager(t *testing.T, fallback SecretResolver) CredentialManager {
	t.Helper()
	manager, err := NewCredentialManager(base64.StdEncoding.EncodeToString([]byte("local_data_source_credential_key")), fallback)
	if err != nil {
		t.Fatal(err)
	}
	return manager
}

func TestSourceFromInputEncryptsPasswordAndOnlyPublishesSafeFields(t *testing.T) {
	manager := testCredentialManager(t, nil)
	input := dataSourceInput{Code: "sales", Name: "Sales", Type: TypeMySQL, Host: "mysql.internal", Port: 3306, Database: "sales", Username: "reader", Password: "plain-secret"}
	source, err := sourceFromInput(context.Background(), nil, manager, "tenant-1", "", input, false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(source.SecretRef, encryptedSecretPrefix) || source.Config["password"] != nil || source.Config["host"] != "mysql.internal" {
		t.Fatalf("source=%#v", source)
	}
	public := publicDataSource(context.Background(), source, manager)
	payload, err := json.Marshal(public)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(payload), "plain-secret") || strings.Contains(string(payload), encryptedSecretPrefix) || strings.Contains(string(payload), "secretRef") {
		t.Fatalf("response leaked credentials: %s", payload)
	}
}

func TestSourceFromInputKeepsPasswordWhileChangingConnectionFields(t *testing.T) {
	fallback := staticSecrets{"host": "old", "port": "3306", "database": "sales", "username": "reader", "password": "existing-secret"}
	manager := testCredentialManager(t, fallback)
	r := &repo{source: Source{ID: "source-1", TenantID: "tenant-1", Type: TypeMySQL, SecretRef: "env://MYSQL"}}
	service := NewService(r)
	input := dataSourceInput{Code: "sales", Name: "Sales", Type: TypeMySQL, Host: "new.internal", Port: 3307, Database: "sales_v2", Username: "reader"}
	source, err := sourceFromInput(context.Background(), service, manager, "tenant-1", "source-1", input, true)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := manager.Resolve(context.Background(), source.SecretRef)
	if err != nil || resolved["password"] != "existing-secret" || resolved["host"] != "new.internal" || resolved["port"] != "3307" {
		t.Fatalf("resolved=%#v err=%v", resolved, err)
	}
}

func TestSourceFromInputRejectsJDBCAndPasswordInConfig(t *testing.T) {
	manager := testCredentialManager(t, nil)
	input := dataSourceInput{Code: "sales", Name: "Sales", Type: TypeMySQL, Host: "jdbc:mysql://db", Port: 3306, Database: "sales", Username: "reader", Password: "secret"}
	if _, err := sourceFromInput(context.Background(), nil, manager, "tenant-1", "", input, false); err == nil {
		t.Fatal("JDBC host was accepted")
	}
	input.Host = "db"
	input.Config = map[string]any{"password": "secret"}
	if _, err := sourceFromInput(context.Background(), nil, manager, "tenant-1", "", input, false); err == nil {
		t.Fatal("password in public config was accepted")
	}
}
