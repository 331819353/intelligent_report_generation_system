package datasource

import (
	"context"
	"encoding/base64"
	"strings"
	"testing"
)

func TestCredentialManagerRejectsRevokedReference(t *testing.T) {
	manager, err := NewCredentialManager(
		base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef")),
		EnvSecretResolver{},
	)
	if err != nil {
		t.Fatalf("NewCredentialManager() error = %v", err)
	}
	if _, err := manager.Resolve(context.Background(), "revoked://data-source/example"); err == nil || !strings.Contains(err.Error(), "revoked") {
		t.Fatalf("Resolve() error = %v, want explicit revoked credential failure", err)
	}
}
