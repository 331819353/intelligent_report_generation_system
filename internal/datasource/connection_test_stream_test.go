package datasource

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

type connectionTestSecretResolver struct{}

func (connectionTestSecretResolver) Resolve(context.Context, string) (map[string]string, error) {
	return map[string]string{
		"host": "db.internal", "port": "3306", "database": "sales",
		"username": "reader", "password": "test-only-password",
	}, nil
}

func TestPythonConnectorReportsLiveConnectionStages(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/connections/test/stream" || request.Header.Get("X-Connector-Token") != "token" {
			t.Fatalf("unexpected staged connector request: %s", request.URL.Path)
		}
		response.Header().Set("Content-Type", "application/x-ndjson")
		_, _ = response.Write([]byte(
			"{\"type\":\"stage\",\"stage\":\"ADDRESS\",\"status\":\"RUNNING\"}\n" +
				"{\"type\":\"stage\",\"stage\":\"ADDRESS\",\"status\":\"PASSED\"}\n" +
				"{\"type\":\"stage\",\"stage\":\"PORT\",\"status\":\"RUNNING\"}\n" +
				"{\"type\":\"stage\",\"stage\":\"PORT\",\"status\":\"PASSED\"}\n" +
				"{\"type\":\"stage\",\"stage\":\"DATABASE\",\"status\":\"RUNNING\"}\n" +
				"{\"type\":\"stage\",\"stage\":\"DATABASE\",\"status\":\"PASSED\"}\n" +
				"{\"type\":\"stage\",\"stage\":\"AUTHENTICATION\",\"status\":\"RUNNING\"}\n" +
				"{\"type\":\"stage\",\"stage\":\"AUTHENTICATION\",\"status\":\"PASSED\"}\n" +
				"{\"type\":\"complete\",\"serverVersion\":\"MySQL 8\",\"latencyMs\":17}\n",
		))
	}))
	defer server.Close()

	connector := NewPythonConnector(TypeMySQL, server.URL, "token", connectionTestSecretResolver{})
	stages := make([]ConnectionTestStage, 0, 4)
	result, err := connector.TestWithProgress(
		context.Background(),
		Source{ID: "source", TenantID: "tenant", Type: TypeMySQL, SecretRef: "env://TEST"},
		func(stage ConnectionTestStage) error {
			stages = append(stages, stage)
			return nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	want := []ConnectionTestStage{
		ConnectionTestStageAddress, ConnectionTestStagePort,
		ConnectionTestStageDatabase, ConnectionTestStageAuthentication,
	}
	if !reflect.DeepEqual(stages, want) {
		t.Fatalf("got stages %v, want %v", stages, want)
	}
	if result.ServerVersion != "MySQL 8" || result.LatencyMS != 17 {
		t.Fatalf("unexpected staged result: %+v", result)
	}
}

func TestPythonConnectorStopsAtReportedFailureStage(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("Content-Type", "application/x-ndjson")
		_, _ = response.Write([]byte(
			"{\"type\":\"stage\",\"stage\":\"ADDRESS\",\"status\":\"RUNNING\"}\n" +
				"{\"type\":\"stage\",\"stage\":\"ADDRESS\",\"status\":\"PASSED\"}\n" +
				"{\"type\":\"stage\",\"stage\":\"PORT\",\"status\":\"RUNNING\"}\n" +
				"{\"type\":\"error\",\"stage\":\"PORT\",\"code\":\"PORT_REFUSED\"}\n",
		))
	}))
	defer server.Close()

	connector := NewPythonConnector(TypeMySQL, server.URL, "token", connectionTestSecretResolver{})
	var stages []ConnectionTestStage
	_, err := connector.TestWithProgress(
		context.Background(),
		Source{ID: "source", TenantID: "tenant", Type: TypeMySQL, SecretRef: "env://TEST"},
		func(stage ConnectionTestStage) error {
			stages = append(stages, stage)
			return nil
		},
	)
	var serviceError *ConnectorServiceError
	if !errors.As(err, &serviceError) || serviceError.Code != "PORT_REFUSED" {
		t.Fatalf("unexpected staged failure: %v", err)
	}
	if !reflect.DeepEqual(stages, []ConnectionTestStage{ConnectionTestStageAddress, ConnectionTestStagePort}) {
		t.Fatalf("unexpected failure stages: %v", stages)
	}
}
