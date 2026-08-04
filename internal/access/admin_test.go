package access

import (
	"errors"
	"testing"
)

func TestValidateDomainApplicationReviewer(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		platformAdmin bool
		domainAdmin   bool
		wantErr       error
	}{
		{
			name:    "ordinary user cannot review",
			wantErr: ErrDomainApplicationForbidden,
		},
		{
			name:        "domain administrator can review within domain",
			domainAdmin: true,
		},
		{
			name:          "platform administrator can review",
			platformAdmin: true,
		},
		{
			name:          "combined administrator authority can review",
			platformAdmin: true, domainAdmin: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			err := validateDomainApplicationReviewer(
				test.platformAdmin, test.domainAdmin,
			)
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("validateDomainApplicationReviewer() error = %v, want %v", err, test.wantErr)
			}
		})
	}
}
