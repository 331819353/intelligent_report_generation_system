package datasource

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

const encryptedSecretPrefix = "encrypted://v1/"

var credentialAssociatedData = []byte("intelligent-report:data-source-credential:v1")

// CredentialManager 负责把页面提交的连接凭据封装为不可回显的内部引用，并兼容解析旧 ENV 引用。
type CredentialManager interface {
	SecretResolver
	Seal(map[string]string) (string, error)
}

type encryptedCredentialManager struct {
	aead     cipher.AEAD
	fallback SecretResolver
}

// NewCredentialManager 使用独立的 256 位密钥创建 AES-GCM 凭证管理器。
func NewCredentialManager(encodedKey string, fallback SecretResolver) (CredentialManager, error) {
	key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(encodedKey))
	if err != nil || len(key) != 32 {
		return nil, errors.New("data source credential key must be base64-encoded 32 bytes")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &encryptedCredentialManager{aead: aead, fallback: fallback}, nil
}

// Seal 加密完整连接凭据；随机 Nonce 使相同密码不会产生相同数据库值。
func (m *encryptedCredentialManager) Seal(value map[string]string) (string, error) {
	plain, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, m.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	sealed := m.aead.Seal(nonce, nonce, plain, credentialAssociatedData)
	return encryptedSecretPrefix + base64.RawURLEncoding.EncodeToString(sealed), nil
}

// Resolve 只在调用 Connector 前解密内部凭据；旧 env:// 引用继续交给兼容解析器。
func (m *encryptedCredentialManager) Resolve(ctx context.Context, ref string) (map[string]string, error) {
	if !strings.HasPrefix(ref, encryptedSecretPrefix) {
		if m.fallback == nil {
			return nil, errors.New("unsupported secret reference")
		}
		return m.fallback.Resolve(ctx, ref)
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(ref, encryptedSecretPrefix))
	if err != nil || len(raw) < m.aead.NonceSize() {
		return nil, errors.New("encrypted credential is invalid")
	}
	nonce, ciphertext := raw[:m.aead.NonceSize()], raw[m.aead.NonceSize():]
	plain, err := m.aead.Open(nil, nonce, ciphertext, credentialAssociatedData)
	if err != nil {
		return nil, errors.New("encrypted credential could not be opened")
	}
	var value map[string]string
	if err := json.Unmarshal(plain, &value); err != nil {
		return nil, fmt.Errorf("decode encrypted credential: %w", err)
	}
	return value, nil
}
