package auth

import (
	"errors"
	"net/mail"
	"strings"
	"time"
)

type UserStatus string

const (
	UserStatusActive   UserStatus = "ACTIVE"
	UserStatusDisabled UserStatus = "DISABLED"
	UserStatusLocked   UserStatus = "LOCKED"
)

type User struct {
	ID           string
	TenantID     string
	Email        string
	DisplayName  string
	PasswordHash string
	Status       UserStatus
	TokenVersion int64
	Version      int64
	LastLoginAt  *time.Time
}

type Role struct {
	ID          string
	TenantID    string
	Code        string
	Name        string
	Description string
	System      bool
	Active      bool
}

type Permission struct {
	ID           string
	TenantID     string
	Code         string
	Name         string
	ResourceType string
	Action       string
}

// Validate 校验用户身份字段、状态以及租户归属。
func (u User) Validate() error {
	if strings.TrimSpace(u.TenantID) == "" {
		return errors.New("tenant ID is required")
	}
	if _, err := mail.ParseAddress(u.Email); err != nil {
		return errors.New("valid email is required")
	}
	if strings.TrimSpace(u.DisplayName) == "" {
		return errors.New("display name is required")
	}
	if strings.TrimSpace(u.PasswordHash) == "" {
		return errors.New("password hash is required")
	}
	switch u.Status {
	case UserStatusActive, UserStatusDisabled, UserStatusLocked:
		return nil
	default:
		return errors.New("invalid user status")
	}
}

// Validate 校验角色编码、名称与作用域。
func (r Role) Validate() error {
	if strings.TrimSpace(r.TenantID) == "" || strings.TrimSpace(r.Code) == "" || strings.TrimSpace(r.Name) == "" {
		return errors.New("tenant ID, role code and name are required")
	}
	return nil
}

// Validate 校验权限的资源和动作定义是否完整。
func (p Permission) Validate() error {
	if strings.TrimSpace(p.TenantID) == "" || strings.TrimSpace(p.Code) == "" || strings.TrimSpace(p.Name) == "" || strings.TrimSpace(p.ResourceType) == "" || strings.TrimSpace(p.Action) == "" {
		return errors.New("permission fields are required")
	}
	return nil
}
