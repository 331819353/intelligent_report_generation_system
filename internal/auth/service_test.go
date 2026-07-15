package auth

import (
	"context"
	"errors"
	"testing"
	"time"
)

type fakeStore struct {
	user    LoginUser
	session Session
	failed  int
	revoked bool
}

func (f *fakeStore) FindTenantID(context.Context, string) (string, error) { return "tenant-1", nil }
func (f *fakeStore) FindUserByEmail(context.Context, string, string) (LoginUser, error) {
	return f.user, nil
}
func (f *fakeStore) FindUserByID(context.Context, string, string) (LoginUser, error) {
	return f.user, nil
}
func (f *fakeStore) CreateSession(_ context.Context, session Session, _, _ string) error {
	f.session = session
	return nil
}
func (f *fakeStore) FindSession(context.Context, string, string) (Session, error) {
	if f.session.ID == "" {
		return Session{}, errors.New("not found")
	}
	return f.session, nil
}
func (f *fakeStore) RotateSession(_ context.Context, _, _ string, _, newHash []byte, expires time.Time) error {
	f.session.RefreshTokenHash = newHash
	f.session.ExpiresAt = expires
	return nil
}
func (f *fakeStore) RevokeSession(context.Context, string, string, []byte, string) error {
	f.revoked = true
	return nil
}
func (f *fakeStore) RecordLoginFailure(context.Context, string, string, string, string, string, string) {
	f.failed++
}

func newTestService(t *testing.T) (*Service, *fakeStore) {
	t.Helper()
	passwords := NewPasswordManager(10)
	hash, err := passwords.Hash("correct-password")
	if err != nil {
		t.Fatal(err)
	}
	store := &fakeStore{user: LoginUser{ID: "user-1", TenantID: "tenant-1", Email: "user@example.com", DisplayName: "User", PasswordHash: hash, Status: UserStatusActive, TokenVersion: 2}}
	service := NewService(store, passwords, NewTokenManager("issuer", "01234567890123456789012345678901", time.Minute), time.Hour)
	return service, store
}

func TestLoginRefreshLogout(t *testing.T) {
	service, store := newTestService(t)
	pair, err := service.Login(context.Background(), LoginInput{TenantCode: "tenant", Email: "user@example.com", Password: "correct-password"})
	if err != nil {
		t.Fatalf("Login() error = %v", err)
	}
	refreshed, err := service.Refresh(context.Background(), pair.RefreshToken)
	if err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}
	if refreshed.RefreshToken == pair.RefreshToken {
		t.Fatal("refresh token was not rotated")
	}
	if err := service.Logout(context.Background(), refreshed.RefreshToken); err != nil {
		t.Fatalf("Logout() error = %v", err)
	}
	if !store.revoked {
		t.Fatal("session was not revoked")
	}
}

func TestLoginRejectsWrongPasswordAndAudits(t *testing.T) {
	service, store := newTestService(t)
	_, err := service.Login(context.Background(), LoginInput{TenantCode: "tenant", Email: "user@example.com", Password: "wrong"})
	if !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("Login() error = %v", err)
	}
	if store.failed != 1 {
		t.Fatalf("failure audit count = %d, want 1", store.failed)
	}
}

func TestValidateAccessRejectsTokenVersionMismatch(t *testing.T) {
	service, _ := newTestService(t)
	err := service.ValidateAccess(context.Background(), AccessClaims{Subject: "user-1", TenantID: "tenant-1", SessionID: service.store.(*fakeStore).session.ID, TokenVersion: 1})
	if err == nil {
		t.Fatal("stale token version accepted")
	}
}
