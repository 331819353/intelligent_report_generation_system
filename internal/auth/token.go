package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

var rawURL = base64.RawURLEncoding

type AccessClaims struct {
	Issuer       string `json:"iss"`
	Subject      string `json:"sub"`
	TenantID     string `json:"tenantId"`
	TokenVersion int64  `json:"tokenVersion"`
	IssuedAt     int64  `json:"iat"`
	ExpiresAt    int64  `json:"exp"`
	JWTID        string `json:"jti"`
	SessionID    string `json:"sid"`
}

type TokenManager struct {
	issuer string
	secret []byte
	ttl    time.Duration
	now    func() time.Time
}

// NewTokenManager 创建基于 HS256 的短期访问令牌管理器。
func NewTokenManager(issuer, secret string, ttl time.Duration) TokenManager {
	return TokenManager{issuer: issuer, secret: []byte(secret), ttl: ttl, now: time.Now}
}

// Issue 签发包含租户、会话和令牌版本的访问令牌。
func (m TokenManager) Issue(userID, tenantID, sessionID string, tokenVersion int64) (string, time.Time, error) {
	now := m.now().UTC()
	expiresAt := now.Add(m.ttl)
	jti, err := randomString(16)
	if err != nil {
		return "", time.Time{}, err
	}
	claims := AccessClaims{Issuer: m.issuer, Subject: userID, TenantID: tenantID, SessionID: sessionID, TokenVersion: tokenVersion, IssuedAt: now.Unix(), ExpiresAt: expiresAt.Unix(), JWTID: jti}
	header, _ := json.Marshal(map[string]string{"alg": "HS256", "typ": "JWT"})
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", time.Time{}, err
	}
	unsigned := rawURL.EncodeToString(header) + "." + rawURL.EncodeToString(payload)
	return unsigned + "." + rawURL.EncodeToString(m.sign(unsigned)), expiresAt, nil
}

// Parse 校验签名、签发方、必要声明和有效期后返回访问声明。
func (m TokenManager) Parse(token string) (AccessClaims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return AccessClaims{}, errors.New("invalid access token")
	}
	unsigned := parts[0] + "." + parts[1]
	signature, err := rawURL.DecodeString(parts[2])
	if err != nil || !hmac.Equal(signature, m.sign(unsigned)) {
		return AccessClaims{}, errors.New("invalid access token signature")
	}
	payload, err := rawURL.DecodeString(parts[1])
	if err != nil {
		return AccessClaims{}, errors.New("invalid access token payload")
	}
	var claims AccessClaims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return AccessClaims{}, errors.New("invalid access token claims")
	}
	if claims.Issuer != m.issuer || claims.Subject == "" || claims.TenantID == "" || claims.SessionID == "" || claims.ExpiresAt <= m.now().Unix() {
		return AccessClaims{}, errors.New("expired or invalid access token")
	}
	return claims, nil
}

// sign 使用服务端密钥计算 HMAC-SHA256 签名。
func (m TokenManager) sign(value string) []byte {
	mac := hmac.New(sha256.New, m.secret)
	_, _ = mac.Write([]byte(value))
	return mac.Sum(nil)
}

// NewRefreshToken 生成刷新令牌，持久层只保存随机密文的摘要。
func NewRefreshToken(tenantID, sessionID string) (string, []byte, error) {
	secret, err := randomString(32)
	if err != nil {
		return "", nil, err
	}
	return fmt.Sprintf("%s.%s.%s", tenantID, sessionID, secret), hashRefreshSecret(secret), nil
}

// ParseRefreshToken 拆分刷新令牌并计算密文摘要，供会话校验使用。
func ParseRefreshToken(token string) (tenantID, sessionID string, hash []byte, err error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return "", "", nil, errors.New("invalid refresh token")
	}
	return parts[0], parts[1], hashRefreshSecret(parts[2]), nil
}

// hashRefreshSecret 使用 SHA-256 避免在数据库中保存刷新令牌明文。
func hashRefreshSecret(secret string) []byte {
	sum := sha256.Sum256([]byte(secret))
	return sum[:]
}

// randomString 从加密安全随机源生成 URL 安全文本。
func randomString(size int) (string, error) {
	value := make([]byte, size)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return rawURL.EncodeToString(value), nil
}

// randomUUID 从加密安全随机源生成 UUID v4。
func randomUUID() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	value[6] = (value[6] & 0x0f) | 0x40
	value[8] = (value[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", value[0:4], value[4:6], value[6:8], value[8:10], value[10:16]), nil
}
