package main

import (
	"io"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestParseConfigDefaultsToRedactedOutput(t *testing.T) {
	getenv := func(key string) string {
		if key == "DATABASE_URL" {
			return "postgres://inventory@example.invalid/control"
		}
		return ""
	}
	config, err := parseConfig([]string{"--tenant-id", "0b3ee268-009a-47ca-8797-615eab7d70d5"}, getenv, io.Discard)
	if err != nil {
		t.Fatalf("parseConfig() error = %v", err)
	}
	if config.IncludePhysicalIdentifiers {
		t.Fatal("physical identifiers must be redacted by default")
	}
	if config.Timeout != 15*time.Second {
		t.Fatalf("Timeout = %s, want 15s", config.Timeout)
	}
}

func TestParseSuggestAdditivityDefaultsToDryRunAndRequiresActor(t *testing.T) {
	tenantID, domainID, actorID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	getenv := func(key string) string {
		switch key {
		case "DATABASE_URL":
			return "postgres://inventory@example.invalid/control"
		case "ASKDATA_ACTOR_ID":
			return actorID
		default:
			return ""
		}
	}
	config, err := parseSuggestAdditivityConfig([]string{
		"--tenant-id", tenantID, "--domain", domainID,
	}, getenv, io.Discard)
	if err != nil {
		t.Fatalf("parseSuggestAdditivityConfig() error = %v", err)
	}
	if !config.DryRun || config.ActorID != actorID {
		t.Fatalf("config = %+v, want dry-run with environment actor", config)
	}
	config, err = parseSuggestAdditivityConfig([]string{
		"--tenant-id", tenantID, "--domain", domainID, "--actor-id", actorID,
		"--dry-run=false",
	}, getenv, io.Discard)
	if err != nil || config.DryRun {
		t.Fatalf("explicit write config = %+v, err = %v", config, err)
	}

	_, err = parseSuggestAdditivityConfig([]string{
		"--tenant-id", tenantID, "--domain", domainID,
	}, func(key string) string {
		if key == "DATABASE_URL" {
			return "postgres://control"
		}
		return ""
	}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "actor-id") {
		t.Fatalf("missing actor error = %v", err)
	}
}

func TestParseConfigRequiresControlDatabaseAndTenant(t *testing.T) {
	tests := []struct {
		name   string
		args   []string
		getenv func(string) string
		want   string
	}{
		{name: "database", args: []string{"--tenant-id", "tenant"}, getenv: func(string) string { return "" }, want: "DATABASE_URL"},
		{name: "tenant", args: nil, getenv: func(string) string { return "postgres://control" }, want: "tenant-id"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := parseConfig(test.args, test.getenv, io.Discard)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("parseConfig() error = %v, want substring %q", err, test.want)
			}
		})
	}
}
