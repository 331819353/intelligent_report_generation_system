package ai

import (
	"strings"
	"testing"
)

func TestRedactSensitiveTextHandlesLocalizedAndMarkdownPasswords(t *testing.T) {
	t.Parallel()

	tests := []string{
		"密码：TakeoutUser2026X",
		"password=`TakeoutUser2026X`",
		"| 密码 | `TakeoutUser2026X` |",
		"| Password | TakeoutUser2026X |",
	}
	for _, input := range tests {
		input := input
		t.Run(input, func(t *testing.T) {
			t.Parallel()
			redacted, count := redactSensitiveText(input)
			if count < 1 {
				t.Fatalf("expected a redaction for %q", input)
			}
			if strings.Contains(redacted, "TakeoutUser2026X") {
				t.Fatalf("password remained in redacted text: %q", redacted)
			}
		})
	}
}
