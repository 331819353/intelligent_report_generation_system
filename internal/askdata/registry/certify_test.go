package registry

import (
	"context"
	"errors"
	"testing"
)

func TestSafeTermRegexpRejectsBacktrackingAndUnboundedPatterns(t *testing.T) {
	for _, pattern := range []string{`^收入[0-9]{1,8}$`, `^[A-Z]{2}$`, `^[一-龥]{1,32}$`} {
		if !safeTermRegexp(pattern) {
			t.Fatalf("safeTermRegexp(%q) = false", pattern)
		}
	}
	for _, pattern := range []string{
		`(a+)+`, `a*`, `(a)\1`, `a{1,33}`, `a|b`, `(?=a)`, `.`, `a{1,}`, `a{2,1}`,
	} {
		if safeTermRegexp(pattern) {
			t.Fatalf("safeTermRegexp(%q) = true", pattern)
		}
	}
}

func TestSafeTermRegexpMatchesWithinGovernedDeadline(t *testing.T) {
	expression, err := CompileSafeTermRegexp(`^收入[0-9]{1,8}$`)
	if err != nil {
		t.Fatalf("CompileSafeTermRegexp() error = %v", err)
	}
	matched, err := expression.Match(context.Background(), "收入20260807")
	if err != nil || !matched {
		t.Fatalf("Match() = %v, %v", matched, err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := expression.Match(cancelled, "收入1"); !errors.Is(err, ErrTermRegexTimeout) {
		t.Fatalf("cancelled Match() error = %v", err)
	}
	if _, err := expression.Match(context.Background(), string(make([]byte, MaxTermRegexpInputBytes+1))); !errors.Is(err, ErrTermRegexUnsafe) {
		t.Fatalf("oversized Match() error = %v", err)
	}
}
