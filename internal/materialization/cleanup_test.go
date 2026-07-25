package materialization

import (
	"errors"
	"strings"
	"testing"
)

func TestCleanupErrorMessageIsBoundedAndRemovesControlCharacters(t *testing.T) {
	message := cleanupErrorMessage(errors.New(strings.Repeat("x", 2100) + "\nsecret"))
	if len(message) != 2048 {
		t.Fatalf("message length=%d, want 2048", len(message))
	}
	if strings.ContainsAny(message, "\r\n\t") {
		t.Fatalf("message still contains control characters: %q", message)
	}
}
