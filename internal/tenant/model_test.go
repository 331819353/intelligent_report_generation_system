package tenant

import "testing"

func TestTenantValidate(t *testing.T) {
	tests := []struct {
		name    string
		tenant  Tenant
		wantErr bool
	}{
		{"valid", Tenant{Code: "acme", Name: "Acme", Status: StatusActive}, false},
		{"blank code", Tenant{Name: "Acme", Status: StatusActive}, true},
		{"blank name", Tenant{Code: "acme", Status: StatusActive}, true},
		{"invalid status", Tenant{Code: "acme", Name: "Acme", Status: "UNKNOWN"}, true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := test.tenant.Validate(); (got != nil) != test.wantErr {
				t.Fatalf("Validate() error = %v, wantErr %v", got, test.wantErr)
			}
		})
	}
}
