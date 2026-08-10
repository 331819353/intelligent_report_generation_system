package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestRunRejectsIncompleteOrUnsafeArguments(t *testing.T) {
	for _, args := range [][]string{
		nil,
		{"-tenant-id", "tenant", "-release-id", "release", "-database-url", "postgres://db", "extra"},
		{"-tenant-id", "tenant", "-release-id", "release", "-database-url", "postgres://db", "-timeout", "10s"},
		{"-tenant-id", "tenant", "-release-id", "release", "-database-url", "postgres://db", "-apply"},
	} {
		var stdout, stderr bytes.Buffer
		if code := run(context.Background(), args, &stdout, &stderr); code != 2 {
			t.Fatalf("run(%v)=%d stdout=%q stderr=%q", args, code, stdout.String(), stderr.String())
		}
		if stdout.Len() != 0 || strings.TrimSpace(stderr.String()) == "" {
			t.Fatalf("run(%v) stdout=%q stderr=%q", args, stdout.String(), stderr.String())
		}
	}
}

func TestFirstEnvUsesFirstNonBlankValue(t *testing.T) {
	t.Setenv("ASKDATA_TEST_FIRST", "  ")
	t.Setenv("ASKDATA_TEST_SECOND", "value")
	if got := firstEnv("ASKDATA_TEST_FIRST", "ASKDATA_TEST_SECOND"); got != "value" {
		t.Fatalf("firstEnv()=%q", got)
	}
}
