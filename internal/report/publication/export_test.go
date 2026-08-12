package publication

import (
	"strings"
	"testing"
)

func TestPrefixedExportJobColumnsKeepsCoalesceLiteralValid(t *testing.T) {
	projection := prefixedExportJobColumns("job")
	if strings.Contains(projection, "job.''") || !strings.Contains(projection, "COALESCE(job.lease_token::text,'')") {
		t.Fatalf("invalid export job projection: %s", projection)
	}
	if got := strings.Count(projection, ","); got != 26 {
		t.Fatalf("projection commas = %d; want 26: %s", got, projection)
	}
}
