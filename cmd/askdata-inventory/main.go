package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"intelligent-report-generation-system/internal/askdata/registry"
	"intelligent-report-generation-system/internal/platform/database"
)

type commandConfig struct {
	DatabaseURL                string
	TenantID                   string
	Timeout                    time.Duration
	IncludePhysicalIdentifiers bool
	Pretty                     bool
}

func main() {
	if err := run(os.Args[1:], os.Getenv, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintf(os.Stderr, "askdata inventory failed: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string, getenv func(string) string, stdout, stderr io.Writer) error {
	config, err := parseConfig(args, getenv, stderr)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), config.Timeout)
	defer cancel()
	pool, err := database.Open(ctx, config.DatabaseURL)
	if err != nil {
		return fmt.Errorf("connect control database: %w", err)
	}
	defer pool.Close()
	service := registry.NewInventoryService(registry.NewPostgresInventoryStore(pool))
	inventory, err := service.List(ctx, config.TenantID, registry.InventoryOptions{
		IncludePhysicalIdentifiers: config.IncludePhysicalIdentifiers,
	})
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(stdout)
	encoder.SetEscapeHTML(true)
	if config.Pretty {
		encoder.SetIndent("", "  ")
	}
	if err := encoder.Encode(inventory); err != nil {
		return fmt.Errorf("encode inventory: %w", err)
	}
	return nil
}

func parseConfig(args []string, getenv func(string) string, stderr io.Writer) (commandConfig, error) {
	if getenv == nil {
		return commandConfig{}, errors.New("environment reader is required")
	}
	flags := flag.NewFlagSet("askdata-inventory", flag.ContinueOnError)
	flags.SetOutput(stderr)
	tenantID := flags.String("tenant-id", "", "tenant UUID to inventory")
	timeout := flags.Duration("timeout", 15*time.Second, "read-only inventory timeout")
	includePhysical := flags.Bool("include-physical-identifiers", false, "include published schema/view names; default output is redacted")
	pretty := flags.Bool("pretty", true, "indent JSON output")
	if err := flags.Parse(args); err != nil {
		return commandConfig{}, err
	}
	if flags.NArg() != 0 {
		return commandConfig{}, errors.New("positional arguments are not supported")
	}
	databaseURL := getenv("DATABASE_URL")
	if databaseURL == "" {
		return commandConfig{}, errors.New("DATABASE_URL is required")
	}
	if *tenantID == "" {
		return commandConfig{}, errors.New("--tenant-id is required")
	}
	if *timeout <= 0 || *timeout > 5*time.Minute {
		return commandConfig{}, errors.New("--timeout must be between 1ns and 5m")
	}
	return commandConfig{
		DatabaseURL: databaseURL, TenantID: *tenantID, Timeout: *timeout,
		IncludePhysicalIdentifiers: *includePhysical, Pretty: *pretty,
	}, nil
}
