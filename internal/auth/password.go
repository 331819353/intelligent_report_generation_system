package auth

import "golang.org/x/crypto/bcrypt"

type PasswordManager struct{ cost int }

// NewPasswordManager 创建使用指定 bcrypt 成本的密码管理器。
func NewPasswordManager(cost int) PasswordManager { return PasswordManager{cost: cost} }

// Hash 生成不可逆的 bcrypt 密码摘要。
func (m PasswordManager) Hash(password string) (string, error) {
	value, err := bcrypt.GenerateFromPassword([]byte(password), m.cost)
	return string(value), err
}

// Verify 校验明文密码是否匹配 bcrypt 摘要。
func (m PasswordManager) Verify(hash, password string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}
