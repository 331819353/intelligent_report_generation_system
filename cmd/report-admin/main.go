package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/platform/database"
	reportstore "intelligent-report-generation-system/internal/report/store"
)

func main() {
	if len(os.Args) < 2 || os.Args[1] != "rebuild-indexes" {
		fmt.Fprintln(os.Stderr, "usage: report-admin rebuild-indexes --tenant-id ID --actor-id ID --domain-id ID --report-id ID")
		os.Exit(2)
	}
	flags := flag.NewFlagSet("rebuild-indexes", flag.ExitOnError)
	tenantID := flags.String("tenant-id", "", "tenant UUID")
	actorID := flags.String("actor-id", "", "authorized actor UUID")
	domainID := flags.String("domain-id", "", "selected business domain UUID")
	reportID := flags.String("report-id", "", "report UUID")
	_ = flags.Parse(os.Args[2:])
	databaseURL := os.Getenv("DATABASE_URL")
	identity := reportstore.Identity{TenantID: askdata.ID(*tenantID), ActorID: askdata.ID(*actorID), DomainID: askdata.ID(*domainID)}
	if databaseURL == "" || identity.Validate() != nil || askdata.ID(*reportID).Validate() != nil {
		fmt.Fprintln(os.Stderr, "DATABASE_URL and four canonical IDs are required")
		os.Exit(2)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	pool, err := database.Open(ctx, databaseURL)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer pool.Close()
	result, err := reportstore.NewPostgresStore(pool).RebuildAllIndexes(ctx, identity, askdata.ID(*reportID))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	_ = json.NewEncoder(os.Stdout).Encode(result)
}
