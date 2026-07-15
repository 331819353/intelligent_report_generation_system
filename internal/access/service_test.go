package access

import (
	"context"
	"testing"
)

type matrixStore map[string]bool

func (m matrixStore) Allowed(_ context.Context, c Check) (bool, error) {
	return m[c.UserID+":"+c.ResourceType+":"+c.Action+":"+c.ObjectID], nil
}

func TestRoleAndObjectPermissionMatrix(t *testing.T) {
	service := NewService(matrixStore{
		"admin:REPORT:DELETE:":    true,
		"designer:REPORT:UPDATE:": true,
		"viewer:REPORT:READ:":     true,
		"viewer:REPORT:UPDATE:550e8400-e29b-41d4-a716-446655440000": true,
	})
	tests := []struct {
		name, user, action, object string
		want                       bool
	}{
		{"admin functional allow", "admin", "DELETE", "", true},
		{"designer functional allow", "designer", "UPDATE", "", true},
		{"viewer functional deny", "viewer", "DELETE", "", false},
		{"viewer object allow", "viewer", "UPDATE", "550e8400-e29b-41d4-a716-446655440000", true},
		{"other object deny", "viewer", "UPDATE", "550e8400-e29b-41d4-a716-446655440001", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := service.Allowed(context.Background(), Check{TenantID: "tenant", UserID: tt.user, ResourceType: "REPORT", Action: tt.action, ObjectID: tt.object})
			if err != nil || got != tt.want {
				t.Fatalf("got %v err=%v want %v", got, err, tt.want)
			}
		})
	}
}
