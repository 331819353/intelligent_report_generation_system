package sharing

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/report/store"
)

func TestServiceCreateAccessExpiryRevocationAndNoAnonymousContract(t *testing.T) {
	now := time.Date(2026, 8, 10, 8, 0, 0, 0, time.UTC)
	owner := sharingIdentity()
	viewer := store.Identity{TenantID: owner.TenantID, ActorID: askdata.ID(uuid.NewString()), DomainID: owner.DomainID}
	reportID := askdata.ID(uuid.NewString())
	versionID := askdata.ID(uuid.NewString())
	repository := &repositoryFixture{}
	authorizer := &authorizerFixture{editAllowed: true, viewAllowed: true}
	versions := &versionFixture{version: store.Version{ID: versionID, ReportID: reportID, VersionNo: 1}}
	cache := &cacheFixture{}
	service := Service{
		Repository: repository, Authorizer: authorizer, Versions: versions, Cache: cache,
		Now: func() time.Time { return now },
	}

	created, err := service.Create(context.Background(), owner, CreateRequest{
		ID: askdata.ID(uuid.NewString()), ReportID: reportID, ReportVersionID: versionID,
		Type: ShareInternalUser, PrincipalID: viewer.ActorID,
		FilterSnapshot: map[string]any{"region": "east"},
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if created.Token == "" || created.Record.TokenHash == created.Token ||
		created.Record.TokenHash != string(askdata.HashBytes([]byte(created.Token))) {
		t.Fatalf("token was not returned once with a persisted hash: %#v", created)
	}
	if !created.Record.ExpiresAt.Equal(now.Add(30 * 24 * time.Hour)) {
		t.Fatalf("default expiry = %v", created.Record.ExpiresAt)
	}
	response, err := json.Marshal(created)
	if err != nil || strings.Contains(string(response), created.Record.TokenHash) ||
		!strings.Contains(string(response), created.Token) {
		t.Fatalf("created-share response leaked token hash or hid one-time token: %s, %v", response, err)
	}

	one := 1
	repository.record.ReportVersionNo = &one
	loaded, filters, err := service.AccessShare(context.Background(), created.Token, viewer)
	if err != nil || loaded.ID != versionID || filters["region"] != "east" || repository.accesses != 1 {
		t.Fatalf("AccessShare() = %#v, %#v, %v; accesses=%d", loaded, filters, err, repository.accesses)
	}

	authorizer.viewAllowed = false
	if _, _, err := service.AccessShare(context.Background(), created.Token, viewer); !errors.Is(err, errViewDenied) {
		t.Fatalf("viewer without report permission error = %v", err)
	}
	authorizer.viewAllowed = true
	now = created.Record.ExpiresAt
	if _, _, err := service.AccessShare(context.Background(), created.Token, viewer); errorCode(err) != "SHARE_EXPIRED" {
		t.Fatalf("runtime expiry error = %v", err)
	}

	now = created.Record.CreatedAt.Add(time.Hour)
	if err := service.Revoke(context.Background(), owner, created.Record.ID); err != nil {
		t.Fatalf("Revoke() error = %v", err)
	}
	if cache.invalidated != created.Record.ID || repository.record.RevokedAt == nil {
		t.Fatalf("revocation/cache = %#v / %#v", repository.record.RevokedAt, cache.invalidated)
	}
	if _, _, err := service.AccessShare(context.Background(), created.Token, viewer); errorCode(err) != "SHARE_NOT_FOUND" {
		t.Fatalf("access after revoke error = %v", err)
	}

	if _, _, err := service.AccessShare(context.Background(), created.Token, store.Identity{}); errorCode(err) != "SHARE_LOGIN_REQUIRED" {
		t.Fatalf("anonymous access error = %v", err)
	}
	if _, err := service.Create(context.Background(), owner, CreateRequest{
		ID: askdata.ID(uuid.NewString()), ReportID: reportID, Type: ShareExternalAccount,
		PrincipalID: viewer.ActorID,
	}); errorCode(err) != "SHARE_EXTERNAL_NOT_IMPLEMENTED" {
		t.Fatalf("external account create error = %v", err)
	}
	tooLate := now.Add(180*24*time.Hour + time.Second)
	if _, err := service.Create(context.Background(), owner, CreateRequest{
		ID: askdata.ID(uuid.NewString()), ReportID: reportID, Type: ShareInternalUser,
		PrincipalID: viewer.ActorID, ExpiresAt: &tooLate,
	}); errorCode(err) != "SHARE_EXPIRY_INVALID" {
		t.Fatalf("overlong expiry error = %v", err)
	}
}

func TestReportShareDDLHasClosedNonAnonymousType(t *testing.T) {
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve sharing test path")
	}
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(filename), "..", "..", "..", "migrations",
		"000238_report_v2_shares.up.sql"))
	if err != nil {
		t.Fatal(err)
	}
	ddl := string(raw)
	want := "share_type text NOT NULL CHECK(share_type IN ('INTERNAL_USER','INTERNAL_GROUP','EXTERNAL_ACCOUNT'))"
	if !strings.Contains(ddl, want) || strings.Contains(ddl, "'ANONYMOUS'") || strings.Contains(ddl, "'PUBLIC'") {
		t.Fatalf("report share type CHECK is not the closed authenticated set")
	}
}

func TestExpiryWorkerUsesBoundedTenantBatch(t *testing.T) {
	tenantID := uuid.NewString()
	now := time.Date(2026, 8, 10, 9, 0, 0, 0, time.UTC)
	fixture := &expiryStoreFixture{tenantIDs: []string{tenantID}, marked: 3}
	worker := &ExpiryWorker{store: fixture}
	if tenants, err := worker.TenantIDs(context.Background()); err != nil || len(tenants) != 1 {
		t.Fatalf("TenantIDs() = %#v, %v", tenants, err)
	}
	if count, err := worker.ProcessTenant(context.Background(), tenantID, now, MaxShareExpiryBatch); err != nil || count != 3 {
		t.Fatalf("ProcessTenant() = %d, %v", count, err)
	}
	if fixture.tenantID != tenantID || !fixture.now.Equal(now) || fixture.limit != MaxShareExpiryBatch {
		t.Fatalf("expiry request = %#v", fixture)
	}
	for _, request := range []struct {
		tenant string
		now    time.Time
		limit  int
	}{
		{"invalid", now, 1}, {tenantID, time.Time{}, 1}, {tenantID, now, 0},
		{tenantID, now, MaxShareExpiryBatch + 1},
	} {
		if _, err := worker.ProcessTenant(context.Background(), request.tenant, request.now, request.limit); err == nil {
			t.Fatalf("ProcessTenant(%#v) unexpectedly succeeded", request)
		}
	}
}

var errViewDenied = errors.New("report view denied")

type repositoryFixture struct {
	record   Record
	accesses int
}

func (fixture *repositoryFixture) Create(_ context.Context, _ store.Identity, record Record) error {
	fixture.record = record
	return nil
}

func (fixture *repositoryFixture) FindByTokenHash(_ context.Context, _ store.Identity, hash string) (Record, error) {
	if fixture.record.TokenHash != hash {
		return Record{}, store.ErrNotFound
	}
	return fixture.record, nil
}

func (fixture *repositoryFixture) Revoke(_ context.Context, _ store.Identity, id askdata.ID, now time.Time) error {
	if fixture.record.ID != id || fixture.record.RevokedAt != nil {
		return store.ErrNotFound
	}
	fixture.record.RevokedAt = &now
	return nil
}

func (fixture *repositoryFixture) RecordAccess(_ context.Context, _ store.Identity, id askdata.ID, _ time.Time) error {
	if fixture.record.ID != id {
		return store.ErrNotFound
	}
	fixture.accesses++
	return nil
}

type authorizerFixture struct{ editAllowed, viewAllowed bool }

func (fixture *authorizerFixture) CheckReportView(context.Context, store.Identity, askdata.ID) error {
	if !fixture.viewAllowed {
		return errViewDenied
	}
	return nil
}

func (fixture *authorizerFixture) CheckReportEdit(context.Context, store.Identity, askdata.ID) error {
	if !fixture.editAllowed {
		return errors.New("report edit denied")
	}
	return nil
}

type versionFixture struct{ version store.Version }

func (fixture *versionFixture) GetVersion(context.Context, store.Identity, askdata.ID, *int) (store.Version, error) {
	return fixture.version, nil
}

type cacheFixture struct{ invalidated askdata.ID }

func (fixture *cacheFixture) InvalidateShare(_ context.Context, _ store.Identity, id askdata.ID) error {
	fixture.invalidated = id
	return nil
}

type expiryStoreFixture struct {
	tenantIDs []string
	marked    int
	tenantID  string
	now       time.Time
	limit     int
}

func (fixture *expiryStoreFixture) TenantIDs(context.Context) ([]string, error) {
	return fixture.tenantIDs, nil
}

func (fixture *expiryStoreFixture) MarkExpired(_ context.Context, tenantID string, now time.Time, limit int) (int, error) {
	fixture.tenantID, fixture.now, fixture.limit = tenantID, now, limit
	return fixture.marked, nil
}

func sharingIdentity() store.Identity {
	return store.Identity{
		TenantID: askdata.ID(uuid.NewString()), ActorID: askdata.ID(uuid.NewString()),
		DomainID: askdata.ID(uuid.NewString()),
	}
}

func errorCode(err error) string {
	var target *Error
	if errors.As(err, &target) {
		return target.Code
	}
	return ""
}
