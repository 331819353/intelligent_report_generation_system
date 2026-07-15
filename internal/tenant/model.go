package tenant

import (
	"errors"
	"strings"
	"time"
)

type Status string

const (
	StatusActive    Status = "ACTIVE"
	StatusSuspended Status = "SUSPENDED"
	StatusDeleted   Status = "DELETED"
)

type Tenant struct {
	ID        string
	Code      string
	Name      string
	Status    Status
	Settings  map[string]any
	Version   int64
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Validate 校验租户编码、名称和生命周期状态是否合法。
func (t Tenant) Validate() error {
	if strings.TrimSpace(t.Code) == "" {
		return errors.New("tenant code is required")
	}
	if strings.TrimSpace(t.Name) == "" {
		return errors.New("tenant name is required")
	}
	switch t.Status {
	case StatusActive, StatusSuspended, StatusDeleted:
		return nil
	default:
		return errors.New("invalid tenant status")
	}
}
