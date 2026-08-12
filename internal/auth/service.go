package auth

import (
	"context"
	"crypto/hmac"
	"errors"
	"net/mail"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

var (
	ErrInvalidCredentials      = errors.New("invalid credentials")
	ErrInvalidRefresh          = errors.New("invalid refresh token")
	ErrDomainForbidden         = errors.New("business domain is not available to this user")
	ErrNoActiveBusinessDomain  = errors.New("user has no active business domain")
	ErrInvalidRegistration     = errors.New("registration input is invalid")
	ErrRegistrationConflict    = errors.New("account already exists")
	ErrRegistrationUnavailable = errors.New("self registration is unavailable")
	ErrWeakPassword            = errors.New("password does not meet complexity requirements")
)

type LoginUser struct {
	ID           string
	TenantID     string
	EmployeeNo   string
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
	DomainID         string
	RefreshTokenHash []byte
	TokenVersion     int64
	UserStatus       UserStatus
	ExpiresAt        time.Time
	RevokedAt        *time.Time
}

type Store interface {
	FindWorkspaceID(ctx context.Context) (string, error)
	FindUserByIdentifier(ctx context.Context, tenantID, identifier string) (LoginUser, error)
	FindUserByID(ctx context.Context, tenantID, userID string) (LoginUser, error)
	CreateSession(ctx context.Context, session Session, userAgent, ipAddress string) error
	FindSession(ctx context.Context, tenantID, sessionID string) (Session, error)
	RotateSession(ctx context.Context, tenantID, sessionID string, oldHash, newHash []byte, expiresAt time.Time) error
	RevokeSession(ctx context.Context, tenantID, sessionID string, tokenHash []byte, reason string) error
	RecordLoginFailure(ctx context.Context, tenantID, userID, email, requestID, ipAddress, userAgent string)
}

type businessDomainStore interface {
	ResolveBusinessDomain(ctx context.Context, tenantID, userID, requestedDomainID string) (string, error)
	SetSessionDomain(ctx context.Context, tenantID, sessionID, userID, domainID string) error
}

type registrationStore interface {
	RegisterUser(context.Context, RegisterUserRecord) error
}

type profileStore interface {
	LoadCurrentProfile(context.Context, string, string, string) (CurrentProfile, error)
}

type profileUpdateStore interface {
	UpdateCurrentProfile(context.Context, string, string, string) error
}

type passwordChangeStore interface {
	ChangePassword(context.Context, string, string, string) error
}

type CurrentProfile struct {
	UserID      string   `json:"userId"`
	EmployeeNo  string   `json:"employeeNo"`
	Email       string   `json:"email"`
	DisplayName string   `json:"displayName"`
	AvatarURL   string   `json:"avatarUrl"`
	Status      string   `json:"status"`
	DomainID    string   `json:"domainId,omitempty"`
	Roles       []string `json:"roles"`
}

// UpdateCurrentProfile 修改当前用户可自助维护的基础资料。
func (s *Service) UpdateCurrentProfile(ctx context.Context, tenantID, userID, displayName string) error {
	store, ok := s.store.(profileUpdateStore)
	displayName = strings.TrimSpace(displayName)
	if !ok || displayName == "" || utf8.RuneCountInString(displayName) > 100 {
		return ErrInvalidRegistration
	}
	return store.UpdateCurrentProfile(ctx, tenantID, userID, displayName)
}

// ChangePassword 校验旧密码后轮换密码并撤销该用户全部活动会话。
func (s *Service) ChangePassword(ctx context.Context, tenantID, userID, currentPassword, newPassword string) error {
	store, ok := s.store.(passwordChangeStore)
	if !ok || currentPassword == "" || !strongPassword(newPassword) {
		return ErrWeakPassword
	}
	user, err := s.store.FindUserByID(ctx, tenantID, userID)
	if err != nil || !s.passwords.Verify(user.PasswordHash, currentPassword) {
		return ErrInvalidCredentials
	}
	if s.passwords.Verify(user.PasswordHash, newPassword) {
		return ErrWeakPassword
	}
	hash, err := s.passwords.Hash(newPassword)
	if err != nil {
		return err
	}
	return store.ChangePassword(ctx, tenantID, userID, hash)
}

const compatibilityDomainID = "00000000-0000-0000-0000-000000000000"

type Service struct {
	store      Store
	passwords  PasswordManager
	tokens     TokenManager
	refreshTTL time.Duration
	now        func() time.Time
}

type LoginInput struct {
	Identifier string
	// Email is retained for source compatibility with older internal callers.
	Email     string
	Password  string
	RequestID string
	IPAddress string
	UserAgent string
}

type RegisterInput struct {
	EmployeeNo  string
	Email       string
	DisplayName string
	Password    string
	RequestID   string
	IPAddress   string
	UserAgent   string
}

type RegisterUserRecord struct {
	EmployeeNo   string
	Email        string
	DisplayName  string
	PasswordHash string
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

func (s *Service) CurrentProfile(ctx context.Context, tenantID, userID, domainID string) (CurrentProfile, error) {
	store, ok := s.store.(profileStore)
	if !ok {
		user, err := s.store.FindUserByID(ctx, tenantID, userID)
		if err != nil {
			return CurrentProfile{}, err
		}
		return CurrentProfile{UserID: user.ID, DisplayName: user.DisplayName, Status: string(user.Status), DomainID: domainID, Roles: []string{}}, nil
	}
	return store.LoadCurrentProfile(ctx, tenantID, userID, domainID)
}

var employeeNoPattern = regexp.MustCompile(`^[A-Z0-9][A-Z0-9_-]{2,31}$`)

// Register 创建无领域归属的平台账号，并在注册完成后直接登录。
func (s *Service) Register(ctx context.Context, input RegisterInput) (TokenPair, error) {
	store, ok := s.store.(registrationStore)
	if !ok {
		return TokenPair{}, ErrRegistrationUnavailable
	}
	input.EmployeeNo = strings.ToUpper(strings.TrimSpace(input.EmployeeNo))
	input.Email = strings.ToLower(strings.TrimSpace(input.Email))
	input.DisplayName = strings.TrimSpace(input.DisplayName)
	if !employeeNoPattern.MatchString(input.EmployeeNo) ||
		input.DisplayName == "" || len([]rune(input.DisplayName)) > 100 {
		return TokenPair{}, ErrInvalidRegistration
	}
	address, err := mail.ParseAddress(input.Email)
	if err != nil || !strings.EqualFold(address.Address, input.Email) {
		return TokenPair{}, ErrInvalidRegistration
	}
	if !strongPassword(input.Password) {
		return TokenPair{}, ErrWeakPassword
	}
	hash, err := s.passwords.Hash(input.Password)
	if err != nil {
		return TokenPair{}, err
	}
	if err := store.RegisterUser(ctx, RegisterUserRecord{
		EmployeeNo: input.EmployeeNo, Email: input.Email,
		DisplayName: input.DisplayName, PasswordHash: hash,
	}); err != nil {
		return TokenPair{}, err
	}
	return s.Login(ctx, LoginInput{
		Identifier: input.EmployeeNo, Password: input.Password,
		RequestID: input.RequestID, IPAddress: input.IPAddress, UserAgent: input.UserAgent,
	})
}

func strongPassword(value string) bool {
	if utf8.RuneCountInString(value) < 10 || utf8.RuneCountInString(value) > 128 {
		return false
	}
	var lower, upper, digit bool
	for _, character := range value {
		lower = lower || unicode.IsLower(character)
		upper = upper || unicode.IsUpper(character)
		digit = digit || unicode.IsDigit(character)
		if unicode.IsControl(character) || unicode.IsSpace(character) {
			return false
		}
	}
	return lower && upper && digit
}

// Login 在单一平台工作区中验证用户凭据，创建服务端会话并签发令牌对。
func (s *Service) Login(ctx context.Context, input LoginInput) (TokenPair, error) {
	tenantID, err := s.store.FindWorkspaceID(ctx)
	if err != nil {
		return TokenPair{}, ErrInvalidCredentials
	}
	identifier := strings.TrimSpace(input.Identifier)
	if identifier == "" {
		identifier = strings.TrimSpace(input.Email)
	}
	if strings.Contains(identifier, "@") {
		identifier = strings.ToLower(identifier)
	} else {
		identifier = strings.ToUpper(identifier)
	}
	user, err := s.store.FindUserByIdentifier(ctx, tenantID, identifier)
	// 所有身份验证失败统一返回同一错误，避免泄露租户或账号是否存在。
	if err != nil || user.Status != UserStatusActive || !s.passwords.Verify(user.PasswordHash, input.Password) {
		userID := user.ID
		s.store.RecordLoginFailure(ctx, tenantID, userID, identifier, input.RequestID, input.IPAddress, input.UserAgent)
		return TokenPair{}, ErrInvalidCredentials
	}
	domainID, err := s.ResolveBusinessDomain(ctx, tenantID, user.ID, "")
	if err != nil {
		return TokenPair{}, ErrDomainForbidden
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
	session := Session{ID: sessionID, TenantID: tenantID, UserID: user.ID, DomainID: domainID, RefreshTokenHash: refreshHash, TokenVersion: user.TokenVersion, UserStatus: user.Status, ExpiresAt: refreshExpires}
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
	if session.DomainID != "" {
		if _, err := s.ResolveBusinessDomain(
			ctx, tenantID, session.UserID, session.DomainID,
		); err != nil {
			if clearErr := s.setSessionDomain(
				ctx, tenantID, sessionID, session.UserID, "",
			); clearErr != nil {
				return TokenPair{}, ErrInvalidRefresh
			}
			session.DomainID = ""
		}
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

// ValidateAccessSession 复核用户、会话和会话绑定领域的实时状态。
func (s *Service) ValidateAccessSession(
	ctx context.Context, claims AccessClaims,
) (Session, error) {
	user, err := s.store.FindUserByID(ctx, claims.TenantID, claims.Subject)
	if err != nil || user.Status != UserStatusActive || user.TokenVersion != claims.TokenVersion {
		return Session{}, errors.New("access token has been revoked")
	}
	session, err := s.store.FindSession(ctx, claims.TenantID, claims.SessionID)
	if err != nil || session.RevokedAt != nil || session.ExpiresAt.Before(s.now()) || session.UserID != claims.Subject {
		return Session{}, errors.New("access session has been revoked")
	}
	if session.DomainID != "" {
		if _, err := s.ResolveBusinessDomain(
			ctx, claims.TenantID, claims.Subject, session.DomainID,
		); err != nil {
			if clearErr := s.setSessionDomain(
				ctx, claims.TenantID, claims.SessionID, claims.Subject, "",
			); clearErr != nil {
				return Session{}, ErrDomainForbidden
			}
			session.DomainID = ""
		}
	}
	return session, nil
}

// ValidateAccess 保留只关心成功与否的调用接口。
func (s *Service) ValidateAccess(ctx context.Context, claims AccessClaims) error {
	_, err := s.ValidateAccessSession(ctx, claims)
	return err
}

// ResolveBusinessDomain validates the requested domain membership. An omitted
// domain resolves to the user's active default domain.
func (s *Service) ResolveBusinessDomain(
	ctx context.Context, tenantID, userID, requestedDomainID string,
) (string, error) {
	store, ok := s.store.(businessDomainStore)
	if !ok {
		// Keep legacy and test stores source-compatible without allowing them to
		// validate arbitrary client-selected domains.
		if requestedDomainID == "" || requestedDomainID == compatibilityDomainID {
			return compatibilityDomainID, nil
		}
		return "", ErrDomainForbidden
	}
	domainID, err := store.ResolveBusinessDomain(
		ctx, tenantID, userID, requestedDomainID,
	)
	if err != nil || (requestedDomainID != "" && domainID == "") {
		return "", ErrDomainForbidden
	}
	return domainID, nil
}

// SwitchBusinessDomain 验证用户所属领域后更新当前登录会话的领域绑定。
func (s *Service) SwitchBusinessDomain(
	ctx context.Context, claims AccessClaims, requestedDomainID string,
) (string, error) {
	domainID, err := s.ResolveBusinessDomain(
		ctx, claims.TenantID, claims.Subject, requestedDomainID,
	)
	if err != nil {
		return "", err
	}
	if err := s.setSessionDomain(
		ctx, claims.TenantID, claims.SessionID, claims.Subject, domainID,
	); err != nil {
		return "", err
	}
	return domainID, nil
}

func (s *Service) setSessionDomain(
	ctx context.Context, tenantID, sessionID, userID, domainID string,
) error {
	store, ok := s.store.(businessDomainStore)
	if !ok {
		if domainID == "" || domainID == compatibilityDomainID {
			return nil
		}
		return ErrDomainForbidden
	}
	return store.SetSessionDomain(ctx, tenantID, sessionID, userID, domainID)
}

// issuePair 将新访问令牌与当前刷新令牌封装为统一响应。
func (s *Service) issuePair(userID, tenantID, sessionID string, tokenVersion int64, refreshToken string, refreshExpires time.Time) (TokenPair, error) {
	accessToken, accessExpires, err := s.tokens.Issue(userID, tenantID, sessionID, tokenVersion)
	if err != nil {
		return TokenPair{}, err
	}
	return TokenPair{AccessToken: accessToken, AccessExpiresAt: accessExpires, RefreshToken: refreshToken, RefreshExpiresAt: refreshExpires, TokenType: "Bearer"}, nil
}
