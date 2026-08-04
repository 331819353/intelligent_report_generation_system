package datasource

import (
	"context"
	"encoding/base64"
	"testing"
)

func TestSourceFromInputPersistsOracleConnectionMode(t *testing.T) {
	t.Parallel()
	credentials, err := NewCredentialManager(
		base64.StdEncoding.EncodeToString(make([]byte, 32)),
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	source, err := sourceFromInput(
		context.Background(), nil, credentials, "tenant", "source",
		dataSourceInput{
			Type: TypeOracle, Host: "db.internal", Port: 1521,
			Database: "ORCL", OracleConnectMode: "sid",
			Username: "REPORT_USER", Password: "test-only-password",
		},
		false,
	)
	if err != nil {
		t.Fatal(err)
	}
	if source.Config["oracleConnectMode"] != "SID" {
		t.Fatalf("expected normalized SID mode, got %#v", source.Config)
	}
}

func TestSourceFromInputDefaultsOracleConnectionModeToServiceName(t *testing.T) {
	t.Parallel()
	credentials, err := NewCredentialManager(
		base64.StdEncoding.EncodeToString(make([]byte, 32)),
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	source, err := sourceFromInput(
		context.Background(), nil, credentials, "tenant", "source",
		dataSourceInput{
			Type: TypeOracle, Host: "db.internal", Port: 1521,
			Database: "FREEPDB1", Username: "REPORT_USER",
			Password: "test-only-password",
		},
		false,
	)
	if err != nil {
		t.Fatal(err)
	}
	if source.Config["oracleConnectMode"] != "SERVICE_NAME" {
		t.Fatalf("expected default SERVICE_NAME mode, got %#v", source.Config)
	}
}
