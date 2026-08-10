package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	askdatagraph "intelligent-report-generation-system/internal/askdata/graph"
	"intelligent-report-generation-system/internal/platform/database"
)

type rebuildReceipt struct {
	SchemaVersion      string `json:"schemaVersion"`
	TenantID           string `json:"tenantId"`
	DomainID           string `json:"domainId"`
	ReleaseID          string `json:"releaseId"`
	SemanticVersion    string `json:"semanticVersion"`
	ReleaseContentHash string `json:"releaseContentHash"`
	GraphContentHash   string `json:"graphContentHash"`
	ManifestCount      int    `json:"manifestCount"`
	ObjectCount        int    `json:"objectCount"`
	VertexCount        int    `json:"vertexCount"`
	EdgeCount          int    `json:"edgeCount"`
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Stdout, os.Stderr))
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("askdata-graph-rebuild", flag.ContinueOnError)
	flags.SetOutput(stderr)
	var tenantID, releaseID, databaseURL string
	var addresses, username, password, space string
	var apply, tlsEnabled bool
	var timeout time.Duration
	flags.StringVar(&tenantID, "tenant-id", "", "tenant UUID owning the release")
	flags.StringVar(&releaseID, "release-id", "", "immutable semantic release UUID")
	flags.StringVar(&databaseURL, "database-url", firstEnv("ASKDATA_CONTROL_DATABASE_URL", "WORKER_DATABASE_URL"), "PostgreSQL control database URL")
	flags.StringVar(&addresses, "nebula-addresses", os.Getenv("ASKDATA_NEBULA_ADDRESSES"), "comma-separated NebulaGraph host:port addresses")
	flags.StringVar(&username, "nebula-username", os.Getenv("ASKDATA_NEBULA_USERNAME"), "NebulaGraph projection user")
	flags.StringVar(&password, "nebula-password", os.Getenv("ASKDATA_NEBULA_PASSWORD"), "NebulaGraph projection password")
	flags.StringVar(&space, "nebula-space", os.Getenv("ASKDATA_NEBULA_SPACE"), "NebulaGraph Space")
	flags.BoolVar(&apply, "apply", false, "write the canonical snapshot to the configured empty Space")
	flags.BoolVar(&tlsEnabled, "nebula-tls", envBool("ASKDATA_NEBULA_TLS_ENABLED"), "enable TLS for NebulaGraph")
	flags.DurationVar(&timeout, "timeout", 15*time.Minute, "overall snapshot and projection timeout")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 || strings.TrimSpace(tenantID) == "" || strings.TrimSpace(releaseID) == "" ||
		strings.TrimSpace(databaseURL) == "" || timeout < time.Minute || timeout > time.Hour {
		fmt.Fprintln(stderr, "tenant-id, release-id and database-url are required; timeout must be between 1m and 1h")
		return 2
	}
	if apply && (strings.TrimSpace(addresses) == "" || strings.TrimSpace(username) == "" ||
		strings.TrimSpace(password) == "" || strings.TrimSpace(space) == "") {
		fmt.Fprintln(stderr, "apply requires NebulaGraph addresses, username, password and Space")
		return 2
	}

	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	pool, err := database.Open(runCtx, databaseURL)
	if err != nil {
		fmt.Fprintf(stderr, "connect control database: %v\n", err)
		return 1
	}
	defer pool.Close()
	snapshot, err := askdatagraph.NewPostgresProjectionStore(pool).LoadReleaseSnapshot(
		runCtx, tenantID, releaseID,
	)
	if err != nil {
		fmt.Fprintf(stderr, "load immutable release snapshot: %v\n", err)
		return 1
	}
	proof, err := snapshot.Proof()
	if err != nil {
		fmt.Fprintf(stderr, "build canonical graph proof: %v\n", err)
		return 1
	}
	if apply {
		graphPool, openErr := askdatagraph.OpenSessionPool(addresses, username, password, space, tlsEnabled)
		if openErr != nil {
			fmt.Fprintf(stderr, "connect NebulaGraph: %v\n", openErr)
			return 1
		}
		defer graphPool.Close()
		writer, writerErr := askdatagraph.NewNebulaProjector(graphPool)
		if writerErr != nil {
			fmt.Fprintf(stderr, "initialize graph projector: %v\n", writerErr)
			return 1
		}
		appliedProof, applyErr := writer.Apply(runCtx, snapshot, func(heartbeatCtx context.Context) error {
			return heartbeatCtx.Err()
		})
		if applyErr != nil {
			fmt.Fprintf(stderr, "apply graph snapshot: %v\n", applyErr)
			return 1
		}
		if appliedProof != proof {
			fmt.Fprintln(stderr, "applied graph proof differs from canonical release proof")
			return 1
		}
	}
	receipt := rebuildReceipt{
		SchemaVersion:      proof.SchemaVersion,
		TenantID:           string(snapshot.TenantID),
		DomainID:           string(snapshot.DomainID),
		ReleaseID:          string(snapshot.ReleaseID),
		SemanticVersion:    snapshot.SemanticVersion,
		ReleaseContentHash: string(snapshot.ContentHash),
		GraphContentHash:   string(proof.GraphHash),
		ManifestCount:      snapshot.ManifestCount,
		ObjectCount:        proof.ObjectCount,
		VertexCount:        proof.VertexCount,
		EdgeCount:          proof.EdgeCount,
	}
	encoder := json.NewEncoder(stdout)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(receipt); err != nil {
		fmt.Fprintf(stderr, "write rebuild receipt: %v\n", err)
		return 1
	}
	return 0
}

func firstEnv(names ...string) string {
	for _, name := range names {
		if value := strings.TrimSpace(os.Getenv(name)); value != "" {
			return value
		}
	}
	return ""
}

func envBool(name string) bool {
	value, err := strconv.ParseBool(strings.TrimSpace(os.Getenv(name)))
	return err == nil && value
}
