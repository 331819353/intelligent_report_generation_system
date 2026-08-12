package observability

import (
	"testing"
	"time"
)

func TestOperationalWindow(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		input    string
		label    string
		duration time.Duration
		ok       bool
	}{
		{"", "24h", 24 * time.Hour, true},
		{"1h", "1h", time.Hour, true},
		{"6h", "6h", 6 * time.Hour, true},
		{"7d", "7d", 7 * 24 * time.Hour, true},
		{"30d", "", 0, false},
	} {
		label, duration, ok := operationalWindow(test.input)
		if label != test.label || duration != test.duration || ok != test.ok {
			t.Fatalf("operationalWindow(%q)=(%q,%v,%v), want (%q,%v,%v)", test.input, label, duration, ok, test.label, test.duration, test.ok)
		}
	}
}

func TestOperationalHealthSignals(t *testing.T) {
	t.Parallel()
	if got := ratio(3, 4); got != 75 {
		t.Fatalf("ratio(3,4)=%v, want 75", got)
	}
	if got := queueStatus(QueueHealth{Pending: 1, OldestPendingSeconds: 120}); got != "ATTENTION" {
		t.Fatalf("pending queue status=%s, want ATTENTION", got)
	}
	if got := queueStatus(QueueHealth{OldestPendingSeconds: 901}); got != "CRITICAL" {
		t.Fatalf("stale queue status=%s, want CRITICAL", got)
	}
	if got := snapshotHealth(OperationalSnapshot{AI: AIUsage{TokenUtilization: 91}}); got != "CRITICAL" {
		t.Fatalf("quota health=%s, want CRITICAL", got)
	}
}
