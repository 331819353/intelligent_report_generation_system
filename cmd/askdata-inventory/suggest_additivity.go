package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/google/uuid"

	"intelligent-report-generation-system/internal/askdata/registry"
	"intelligent-report-generation-system/internal/platform/database"
)

type suggestAdditivityConfig struct {
	DatabaseURL string
	TenantID    string
	DomainID    string
	ActorID     string
	Timeout     time.Duration
	DryRun      bool
	Pretty      bool
}

type suggestAdditivityOutput struct {
	DryRun         bool                           `json:"dryRun"`
	Candidates     []registry.AdditivityCandidate `json:"candidates"`
	PersistedCount int                            `json:"persistedCount"`
}

func runSuggestAdditivity(
	args []string,
	getenv func(string) string,
	stdout, stderr io.Writer,
) error {
	config, err := parseSuggestAdditivityConfig(args, getenv, stderr)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), config.Timeout)
	defer cancel()
	ctx = database.WithAccessContext(ctx, config.ActorID, config.DomainID)
	pool, err := database.Open(ctx, config.DatabaseURL)
	if err != nil {
		return fmt.Errorf("connect control database: %w", err)
	}
	defer pool.Close()
	store := registry.NewPostgresStore(pool)
	scope := registry.AdminScope{
		TenantID: config.TenantID, DomainID: config.DomainID, ActorID: config.ActorID,
	}
	var candidates []registry.AdditivityCandidate
	for cursor := ""; ; {
		page, err := store.ListUnconfirmedAdditivity(ctx, scope, "", cursor, 200)
		if err != nil {
			return err
		}
		candidates = append(candidates, page.Items...)
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
	}
	result := suggestAdditivityOutput{DryRun: config.DryRun, Candidates: candidates}
	if !config.DryRun {
		result.PersistedCount, err = store.PersistAdditivitySuggestions(ctx, scope, candidates)
		if err != nil {
			return err
		}
	}
	encoder := json.NewEncoder(stdout)
	encoder.SetEscapeHTML(true)
	if config.Pretty {
		encoder.SetIndent("", "  ")
	}
	if err := encoder.Encode(result); err != nil {
		return fmt.Errorf("encode additivity suggestions: %w", err)
	}
	return nil
}

func parseSuggestAdditivityConfig(
	args []string,
	getenv func(string) string,
	stderr io.Writer,
) (suggestAdditivityConfig, error) {
	if getenv == nil {
		return suggestAdditivityConfig{}, errors.New("environment reader is required")
	}
	flags := flag.NewFlagSet("askdata-inventory suggest-additivity", flag.ContinueOnError)
	flags.SetOutput(stderr)
	tenantID := flags.String("tenant-id", "", "tenant UUID")
	domainID := flags.String("domain", "", "business domain UUID")
	actorID := flags.String("actor-id", getenv("ASKDATA_ACTOR_ID"), "authorized semantic owner UUID")
	timeout := flags.Duration("timeout", 30*time.Second, "operation timeout")
	dryRun := flags.Bool("dry-run", true, "preview suggestions without writing; explicitly set false to persist")
	pretty := flags.Bool("pretty", true, "indent JSON output")
	if err := flags.Parse(args); err != nil {
		return suggestAdditivityConfig{}, err
	}
	if flags.NArg() != 0 {
		return suggestAdditivityConfig{}, errors.New("positional arguments are not supported")
	}
	databaseURL := getenv("DATABASE_URL")
	if databaseURL == "" {
		return suggestAdditivityConfig{}, errors.New("DATABASE_URL is required")
	}
	for name, value := range map[string]string{
		"tenant-id": *tenantID, "domain": *domainID, "actor-id": *actorID,
	} {
		parsed, err := uuid.Parse(value)
		if err != nil || parsed.String() != value {
			return suggestAdditivityConfig{}, fmt.Errorf("--%s must be a canonical UUID", name)
		}
	}
	if *timeout <= 0 || *timeout > 5*time.Minute {
		return suggestAdditivityConfig{}, errors.New("--timeout must be between 1ns and 5m")
	}
	return suggestAdditivityConfig{
		DatabaseURL: databaseURL, TenantID: *tenantID, DomainID: *domainID,
		ActorID: *actorID, Timeout: *timeout, DryRun: *dryRun, Pretty: *pretty,
	}, nil
}
