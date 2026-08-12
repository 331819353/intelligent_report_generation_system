package sharing

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/report/store"
)

type ShareType string

const (
	ShareInternalUser    ShareType = "INTERNAL_USER"
	ShareInternalGroup   ShareType = "INTERNAL_GROUP"
	ShareExternalAccount ShareType = "EXTERNAL_ACCOUNT"
)

type Error struct{ Code string }

func (err *Error) Error() string { return err.Code }

type Record struct {
	ID              askdata.ID     `json:"id"`
	TenantID        askdata.ID     `json:"tenantId"`
	ReportID        askdata.ID     `json:"reportId"`
	ReportVersionID askdata.ID     `json:"reportVersionId,omitempty"`
	ReportVersionNo *int           `json:"-"`
	Type            ShareType      `json:"shareType"`
	PrincipalID     askdata.ID     `json:"principalId"`
	TokenHash       string         `json:"-"`
	FilterSnapshot  map[string]any `json:"filterSnapshot,omitempty"`
	CreatedBy       askdata.ID     `json:"createdBy"`
	CreatedAt       time.Time      `json:"createdAt"`
	ExpiresAt       time.Time      `json:"expiresAt"`
	ExpiredAt       *time.Time     `json:"expiredAt,omitempty"`
	RevokedAt       *time.Time     `json:"revokedAt,omitempty"`
}

type Repository interface {
	Create(context.Context, store.Identity, Record) error
	FindByTokenHash(context.Context, store.Identity, string) (Record, error)
	ListCreated(context.Context, store.Identity, askdata.ID, int) ([]Record, error)
	Revoke(context.Context, store.Identity, askdata.ID, time.Time) error
	RecordAccess(context.Context, store.Identity, askdata.ID, time.Time) error
}

type Authorizer interface {
	CheckReportView(context.Context, store.Identity, askdata.ID) error
	CheckReportEdit(context.Context, store.Identity, askdata.ID) error
}

type VersionLoader interface {
	GetVersion(context.Context, store.Identity, askdata.ID, *int) (store.Version, error)
}

type Cache interface {
	InvalidateShare(context.Context, store.Identity, askdata.ID) error
}

type Service struct {
	Repository Repository
	Authorizer Authorizer
	Versions   VersionLoader
	Cache      Cache
	Now        func() time.Time
}

type CreateRequest struct {
	ID              askdata.ID
	ReportID        askdata.ID
	ReportVersionID askdata.ID
	Type            ShareType
	PrincipalID     askdata.ID
	FilterSnapshot  map[string]any
	ExpiresAt       *time.Time
}

type CreatedShare struct {
	Record Record `json:"share"`
	Token  string `json:"token"`
}

func (service Service) Create(ctx context.Context, identity store.Identity, request CreateRequest) (CreatedShare, error) {
	if identity.ActorID == "" {
		return CreatedShare{}, &Error{Code: "SHARE_LOGIN_REQUIRED"}
	}
	if service.Repository == nil || service.Authorizer == nil {
		return CreatedShare{}, errors.New("share service is not configured")
	}
	if request.Type == ShareExternalAccount {
		return CreatedShare{}, &Error{Code: "SHARE_EXTERNAL_NOT_IMPLEMENTED"}
	}
	if request.Type != ShareInternalUser && request.Type != ShareInternalGroup {
		return CreatedShare{}, &Error{Code: "SHARE_TYPE_INVALID"}
	}
	if err := validateCreate(identity, request); err != nil {
		return CreatedShare{}, &Error{Code: "SHARE_REQUEST_INVALID"}
	}
	if err := service.Authorizer.CheckReportEdit(ctx, identity, request.ReportID); err != nil {
		return CreatedShare{}, err
	}
	now := service.now()
	expiresAt := now.Add(30 * 24 * time.Hour)
	if request.ExpiresAt != nil {
		expiresAt = *request.ExpiresAt
	}
	if !expiresAt.After(now) || expiresAt.After(now.Add(180*24*time.Hour)) {
		return CreatedShare{}, &Error{Code: "SHARE_EXPIRY_INVALID"}
	}
	token, err := newToken()
	if err != nil {
		return CreatedShare{}, err
	}
	record := Record{ID: request.ID, TenantID: identity.TenantID, ReportID: request.ReportID,
		ReportVersionID: request.ReportVersionID, Type: request.Type, PrincipalID: request.PrincipalID,
		TokenHash: string(askdata.HashBytes([]byte(token))), FilterSnapshot: cloneSnapshot(request.FilterSnapshot),
		CreatedBy: identity.ActorID, CreatedAt: now, ExpiresAt: expiresAt}
	if err := service.Repository.Create(ctx, identity, record); err != nil {
		return CreatedShare{}, err
	}
	return CreatedShare{Record: record, Token: token}, nil
}

func (service Service) AccessShare(ctx context.Context, token string, viewer store.Identity) (store.Version, map[string]any, error) {
	// Authentication deliberately precedes token lookup: a token locates a
	// record, but never grants authority or leaks whether the record exists.
	if viewer.ActorID == "" {
		return store.Version{}, nil, &Error{Code: "SHARE_LOGIN_REQUIRED"}
	}
	if service.Repository == nil || service.Authorizer == nil || service.Versions == nil {
		return store.Version{}, nil, errors.New("share service is not configured")
	}
	token = strings.TrimSpace(token)
	if token == "" || len(token) > 512 || viewer.Validate() != nil {
		return store.Version{}, nil, &Error{Code: "SHARE_NOT_FOUND"}
	}
	record, err := service.Repository.FindByTokenHash(ctx, viewer, string(askdata.HashBytes([]byte(token))))
	if err != nil || record.RevokedAt != nil {
		return store.Version{}, nil, &Error{Code: "SHARE_NOT_FOUND"}
	}
	if record.ExpiredAt != nil || !service.now().Before(record.ExpiresAt) {
		return store.Version{}, nil, &Error{Code: "SHARE_EXPIRED"}
	}
	if record.Type == ShareExternalAccount {
		return store.Version{}, nil, &Error{Code: "SHARE_EXTERNAL_NOT_IMPLEMENTED"}
	}
	if record.Type == ShareInternalUser && record.PrincipalID != viewer.ActorID {
		return store.Version{}, nil, &Error{Code: "SHARE_NOT_FOUND"}
	}
	if err := service.Authorizer.CheckReportView(ctx, viewer, record.ReportID); err != nil {
		return store.Version{}, nil, err
	}
	version, err := service.Versions.GetVersion(ctx, viewer, record.ReportID, record.ReportVersionNo)
	if err != nil {
		return store.Version{}, nil, err
	}
	if record.ReportVersionID != "" && version.ID != record.ReportVersionID {
		return store.Version{}, nil, &Error{Code: "SHARE_VERSION_UNAVAILABLE"}
	}
	if err := service.Repository.RecordAccess(ctx, viewer, record.ID, service.now()); err != nil {
		return store.Version{}, nil, err
	}
	return version, cloneSnapshot(record.FilterSnapshot), nil
}

// Access remains as a compatibility alias for callers created before the
// AccessShare contract was named explicitly.
func (service Service) Access(ctx context.Context, token string, viewer store.Identity) (store.Version, map[string]any, error) {
	return service.AccessShare(ctx, token, viewer)
}

func (service Service) Revoke(ctx context.Context, identity store.Identity, shareID askdata.ID) error {
	if identity.ActorID == "" {
		return &Error{Code: "SHARE_LOGIN_REQUIRED"}
	}
	if service.Repository == nil || identity.Validate() != nil || !validUUID(shareID) {
		return errors.New("share service is not configured or request is invalid")
	}
	if err := service.Repository.Revoke(ctx, identity, shareID, service.now()); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return &Error{Code: "SHARE_NOT_FOUND"}
		}
		return err
	}
	if service.Cache != nil {
		return service.Cache.InvalidateShare(ctx, identity, shareID)
	}
	return nil
}

func (service Service) List(ctx context.Context, identity store.Identity, reportID askdata.ID, limit int) ([]Record, error) {
	if identity.ActorID == "" {
		return nil, &Error{Code: "SHARE_LOGIN_REQUIRED"}
	}
	if service.Repository == nil || identity.Validate() != nil || !validUUID(reportID) || limit < 1 || limit > 200 {
		return nil, &Error{Code: "SHARE_REQUEST_INVALID"}
	}
	return service.Repository.ListCreated(ctx, identity, reportID, limit)
}

func (service Service) now() time.Time {
	if service.Now != nil {
		return service.Now()
	}
	return time.Now().UTC()
}

func newToken() (string, error) {
	value := make([]byte, 32)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func cloneSnapshot(value map[string]any) map[string]any {
	if value == nil {
		return nil
	}
	result := make(map[string]any, len(value))
	for key, item := range value {
		result[key] = item
	}
	return result
}

func validateCreate(identity store.Identity, request CreateRequest) error {
	if identity.Validate() != nil || !validUUID(request.ID) || !validUUID(request.ReportID) ||
		!validUUID(request.PrincipalID) || request.ReportVersionID != "" && !validUUID(request.ReportVersionID) {
		return errors.New("invalid share identity")
	}
	return nil
}

func validUUID(value askdata.ID) bool {
	_, err := uuid.Parse(string(value))
	return err == nil
}
