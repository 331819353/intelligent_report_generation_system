package auth

import "testing"

func TestUserValidate(t *testing.T) {
	valid := User{TenantID: "tenant-1", Email: "user@example.com", DisplayName: "User", PasswordHash: "hash", Status: UserStatusActive}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid user rejected: %v", err)
	}

	invalid := valid
	invalid.Email = "invalid"
	if err := invalid.Validate(); err == nil {
		t.Fatal("invalid email accepted")
	}
}

func TestRoleAndPermissionValidate(t *testing.T) {
	role := Role{TenantID: "tenant-1", Code: "admin", Name: "Admin"}
	if err := role.Validate(); err != nil {
		t.Fatalf("valid role rejected: %v", err)
	}
	permission := Permission{TenantID: "tenant-1", Code: "report.read", Name: "Read reports", ResourceType: "REPORT", Action: "READ"}
	if err := permission.Validate(); err != nil {
		t.Fatalf("valid permission rejected: %v", err)
	}
}
