package auth

import (
	"context"
	"crypto/hmac"
	"errors"
	"time"
)

var (
	ErrInvalidCredentials = errors.New("invalid credentials")
	ErrInvalidRefresh     = errors.New("invalid refresh token")
)

type LoginUser struct {
	ID           string
	TenantID     string
	Email        string
	DisplayName  string
	PasswordHash string
	Status       UserStatus
	TokenVersion int64
}

type Session struct {
	ID               string
	TenantID         string
	UserID           string
	RefreshTokenHash []byte
	TokenVersion     int64
	UserStatus       UserStatus
	ExpiresAt        time.Time
	RevokedAt        *time.Time
}

type Store interface {
	FindTenantID(ctx context.Context, code string) (string, error)
	FindUserByEmail(ctx context.Context, tenantID, email string) (LoginUser, error)
	FindUserByID(ctx context.Context, tenantID, userID string) (LoginUser, error)
	CreateSession(ctx context.Context, session Session, userAgent, ipAddress string) error
	FindSession(ctx context.Context, tenantID, sessionID string) (Session, error)
	RotateSession(ctx context.Context, tenantID, sessionID string, oldHash, newHash []byte, expiresAt time.Time) error
	RevokeSession(ctx context.Context, tenantID, sessionID string, tokenHash []byte, reason string) error
	RecordLoginFailure(ctx context.Context, tenantID, userID, email, requestID, ipAddress, userAgent string)
}

type Service struct {
	store      Store
	passwords  PasswordManager
	tokens     TokenManager
	refreshTTL time.Duration
	now        func() time.Time
}

type LoginInput struct {
	TenantCode string
	Email      string
	Password   string
	RequestID  string
	IPAddress  string
	UserAgent  string
}

type TokenPair struct {
	AccessToken      string    `json:"accessToken"`
	AccessExpiresAt  time.Time `json:"accessExpiresAt"`
	RefreshToken     string    `json:"refreshToken"`
	RefreshExpiresAt time.Time `json:"refreshExpiresAt"`
	TokenType        string    `json:"tokenType"`
}

// NewService 组合身份存储、密码校验与令牌签发能力。
func NewService(store Store, passwords PasswordManager, tokens TokenManager, refreshTTL time.Duration) *Service {
	return &Service{store: store, passwords: passwords, tokens: tokens, refreshTTL: refreshTTL, now: time.Now}
}

// Login 验证租户与用户凭据，创建服务端会话并签发令牌对。
func (s *Service) Login(ctx context.Context, input LoginInput) (TokenPair, error) {
	tenantID, err := s.store.FindTenantID(ctx, input.TenantCode)
	if err != nil {
		return TokenPair{}, ErrInvalidCredentials
	}
	user, err := s.store.FindUserByEmail(ctx, tenantID, input.Email)
	// 所有身份验证失败统一返回同一错误，避免泄露租户或账号是否存在。
	if err != nil || user.Status != UserStatusActive || !s.passwords.Verify(user.PasswordHash, input.Password) {
		userID := user.ID
		s.store.RecordLoginFailure(ctx, tenantID, userID, input.Email, input.RequestID, input.IPAddress, input.UserAgent)
		return TokenPair{}, ErrInvalidCredentials
	}

	sessionID, err := randomUUID()
	if err != nil {
		return TokenPair{}, err
	}
	refreshToken, refreshHash, err := NewRefreshToken(tenantID, sessionID)
	if err != nil {
		return TokenPair{}, err
	}
	refreshExpires := s.now().UTC().Add(s.refreshTTL)
	session := Session{ID: sessionID, TenantID: tenantID, UserID: user.ID, RefreshTokenHash: refreshHash, TokenVersion: user.TokenVersion, UserStatus: user.Status, ExpiresAt: refreshExpires}
	if err := s.store.CreateSession(ctx, session, input.UserAgent, input.IPAddress); err != nil {
		return TokenPair{}, err
	}
	return s.issuePair(user.ID, tenantID, sessionID, user.TokenVersion, refreshToken, refreshExpires)
}

// Refresh 校验会话后轮换刷新令牌，旧令牌会立即失效。
func (s *Service) Refresh(ctx context.Context, refreshToken string) (TokenPair, error) {
	tenantID, sessionID, oldHash, err := ParseRefreshToken(refreshToken)
	if err != nil {
		return TokenPair{}, ErrInvalidRefresh
	}
	session, err := s.store.FindSession(ctx, tenantID, sessionID)
	// 摘要使用恒定时间比较，降低通过耗时推断令牌内容的风险。
	if err != nil || session.RevokedAt != nil || session.ExpiresAt.Before(s.now()) || session.UserStatus != UserStatusActive || !hmac.Equal(session.RefreshTokenHash, oldHash) {
		return TokenPair{}, ErrInvalidRefresh
	}
	newToken, newHash, err := NewRefreshToken(tenantID, sessionID)
	if err != nil {
		return TokenPair{}, err
	}
	refreshExpires := s.now().UTC().Add(s.refreshTTL)
	if err := s.store.RotateSession(ctx, tenantID, sessionID, oldHash, newHash, refreshExpires); err != nil {
		return TokenPair{}, ErrInvalidRefresh
	}
	return s.issuePair(session.UserID, tenantID, sessionID, session.TokenVersion, newToken, refreshExpires)
}

// Logout 撤销刷新会话，使关联访问令牌在会话复核时同步失效。
func (s *Service) Logout(ctx context.Context, refreshToken string) error {
	tenantID, sessionID, tokenHash, err := ParseRefreshToken(refreshToken)
	if err != nil {
		return ErrInvalidRefresh
	}
	if err := s.store.RevokeSession(ctx, tenantID, sessionID, tokenHash, "LOGOUT"); err != nil {
		return ErrInvalidRefresh
	}
	return nil
}

// ValidateAccess 复核用户状态、令牌版本与会话状态，支持即时撤权。
func (s *Service) ValidateAccess(ctx context.Context, claims AccessClaims) error {
	user, err := s.store.FindUserByID(ctx, claims.TenantID, claims.Subject)
	if err != nil || user.Status != UserStatusActive || user.TokenVersion != claims.TokenVersion {
		return errors.New("access token has been revoked")
	}
	session, err := s.store.FindSession(ctx, claims.TenantID, claims.SessionID)
	if err != nil || session.RevokedAt != nil || session.ExpiresAt.Before(s.now()) || session.UserID != claims.Subject {
		return errors.New("access session has been revoked")
	}
	return nil
}

// issuePair 将新访问令牌与当前刷新令牌封装为统一响应。
func (s *Service) issuePair(userID, tenantID, sessionID string, tokenVersion int64, refreshToken string, refreshExpires time.Time) (TokenPair, error) {
	accessToken, accessExpires, err := s.tokens.Issue(userID, tenantID, sessionID, tokenVersion)
	if err != nil {
		return TokenPair{}, err
	}
	return TokenPair{AccessToken: accessToken, AccessExpiresAt: accessExpires, RefreshToken: refreshToken, RefreshExpiresAt: refreshExpires, TokenType: "Bearer"}, nil
}
