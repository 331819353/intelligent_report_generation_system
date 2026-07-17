package datasource

import (
	"context"
	"encoding/base64"
	"strings"
	"testing"
)

func TestCredentialManagerRoundTripAndTamperProtection(t *testing.T) {
	key := base64.StdEncoding.EncodeToString([]byte("local_data_source_credential_key"))
	manager, err := NewCredentialManager(key, nil)
	if err != nil {
		t.Fatal(err)
	}
	input := map[string]string{"host": "db.internal", "port": "3306", "database": "sales", "username": "reader", "password": "secret"}
	ref, err := manager.Seal(input)
	if err != nil || !strings.HasPrefix(ref, encryptedSecretPrefix) || strings.Contains(ref, "secret") {
		t.Fatalf("ref=%q err=%v", ref, err)
	}
	resolved, err := manager.Resolve(context.Background(), ref)
	if err != nil || resolved["password"] != input["password"] || resolved["host"] != input["host"] {
		t.Fatalf("resolved=%#v err=%v", resolved, err)
	}
	last := ref[len(ref)-1]
	replacement := byte('A')
	if last == replacement {
		replacement = 'B'
	}
	if _, err := manager.Resolve(context.Background(), ref[:len(ref)-1]+string(replacement)); err == nil {
		t.Fatal("tampered credential was accepted")
	}
}

func TestCredentialManagerRejectsInvalidKey(t *testing.T) {
	if _, err := NewCredentialManager("not-base64", nil); err == nil {
		t.Fatal("invalid credential key was accepted")
	}
}
