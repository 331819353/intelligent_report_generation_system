package registry

import (
	"errors"
	"testing"
	"time"
)

func TestMetricCursorRoundTripAndRejectsTampering(t *testing.T) {
	now := time.Date(2026, 8, 5, 10, 30, 0, 123, time.FixedZone("CST", 8*60*60))
	encoded, err := encodeMetricCursor(metricCursor{UpdatedAt: &now, ID: validationRow})
	if err != nil {
		t.Fatalf("encodeMetricCursor() error = %v", err)
	}
	decoded, err := decodeMetricCursor(encoded)
	if err != nil {
		t.Fatalf("decodeMetricCursor() error = %v", err)
	}
	if decoded.ID != validationRow || !decoded.UpdatedAt.Equal(now) || decoded.UpdatedAt.Location() != time.UTC {
		t.Fatalf("decoded cursor = %#v", decoded)
	}
	for _, invalid := range []string{"not-base64!", "e30", encoded + "extra"} {
		if _, err := decodeMetricCursor(invalid); err == nil {
			t.Fatalf("decodeMetricCursor(%q) accepted invalid cursor", invalid)
		}
	}
}

func TestRegistryErrorCodeIsStable(t *testing.T) {
	tests := []struct {
		err  error
		code string
	}{
		{ErrRegistryNotFound, "REG_NOT_FOUND"},
		{ErrRegistryVersionConflict, "REG_VERSION_CONFLICT"},
		{ErrRegistryConflict, "REG_CONFLICT"},
		{ValidationErrors{Issues: []ValidationIssue{{Code: "X", Path: "x"}}}, "REG_VALIDATION_FAILED"},
		{errors.New("database unavailable"), "REG_INTERNAL"},
	}
	for _, test := range tests {
		if got := RegistryErrorCode(test.err); got != test.code {
			t.Fatalf("RegistryErrorCode(%v) = %q, want %q", test.err, got, test.code)
		}
	}
}
