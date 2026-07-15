package auth

import (
	"testing"
	"time"
)

func TestAccessTokenRoundTrip(t *testing.T) {
	manager := NewTokenManager("issuer", "01234567890123456789012345678901", 15*time.Minute)
	manager.now = func() time.Time { return time.Unix(1_700_000_000, 0) }
	token, _, err := manager.Issue("user-1", "tenant-1", "session-1", 3)
	if err != nil {
		t.Fatalf("Issue() error = %v", err)
	}
	claims, err := manager.Parse(token)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if claims.Subject != "user-1" || claims.TenantID != "tenant-1" || claims.TokenVersion != 3 {
		t.Fatalf("unexpected claims: %+v", claims)
	}
}

func TestAccessTokenRejectsTampering(t *testing.T) {
	manager := NewTokenManager("issuer", "01234567890123456789012345678901", time.Minute)
	token, _, _ := manager.Issue("user-1", "tenant-1", "session-1", 1)
	if _, err := manager.Parse(token + "x"); err == nil {
		t.Fatal("tampered token accepted")
	}
}

func TestRefreshTokenRoundTrip(t *testing.T) {
	token, hash, err := NewRefreshToken("tenant-1", "session-1")
	if err != nil {
		t.Fatal(err)
	}
	tenantID, sessionID, parsedHash, err := ParseRefreshToken(token)
	if err != nil || tenantID != "tenant-1" || sessionID != "session-1" || string(hash) != string(parsedHash) {
		t.Fatalf("refresh token round trip failed")
	}
}
