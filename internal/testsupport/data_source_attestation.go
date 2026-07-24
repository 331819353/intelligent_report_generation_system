package testsupport

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"intelligent-report-generation-system/internal/datasource"
)

// AttestAndPublishDataSource turns the already-committed current draft into an
// ACTIVE published source through the production v70 state machine. The
// control pool may use the app or admin role, but the attestation is always
// completed through a fresh pool using the dedicated connection-test role.
//
// Keeping this sequence in one test helper prevents integration fixtures from
// drifting back to direct writes of protected test evidence or publication
// pointers.
func AttestAndPublishDataSource(
	ctx context.Context,
	controlPool *pgxpool.Pool,
	tenantID, actorID, sourceID string,
) (datasource.Source, error) {
	testerURL := os.Getenv("CONNECTION_TEST_DATABASE_URL")
	if testerURL == "" {
		return datasource.Source{}, fmt.Errorf(
			"CONNECTION_TEST_DATABASE_URL is required for trusted data-source fixtures",
		)
	}
	testerPool, err := pgxpool.New(ctx, testerURL)
	if err != nil {
		return datasource.Source{}, fmt.Errorf("open connection-test role: %w", err)
	}
	defer testerPool.Close()
	if err := testerPool.Ping(ctx); err != nil {
		return datasource.Source{}, fmt.Errorf("ping connection-test role: %w", err)
	}

	controlRepository := datasource.NewPostgresRepository(controlPool)
	connectionTests := datasource.NewPostgresConnectionTestRepository(controlPool)
	service := datasource.NewService(controlRepository)
	service.SetConnectionTestJobRepository(connectionTests)

	draft, err := service.Get(ctx, tenantID, sourceID)
	if err != nil {
		return datasource.Source{}, fmt.Errorf("load current data-source draft: %w", err)
	}
	if draft.ConfigVersionID == "" || draft.ConfigHash == "" {
		return datasource.Source{}, fmt.Errorf(
			"data-source draft %s has no immutable version/hash", sourceID,
		)
	}
	job, err := service.QueueConnectionTest(
		ctx,
		tenantID,
		actorID,
		sourceID,
		"integration-fixture:"+sourceID+":"+draft.ConfigVersionID,
	)
	if err != nil {
		return datasource.Source{}, fmt.Errorf("enqueue connection-test fixture: %w", err)
	}
	if job.DataSourceID != sourceID || job.ConfigVersionID != draft.ConfigVersionID {
		return datasource.Source{}, fmt.Errorf(
			"queued connection test froze source/version %s/%s, want %s/%s",
			job.DataSourceID,
			job.ConfigVersionID,
			sourceID,
			draft.ConfigVersionID,
		)
	}

	testerRepository := datasource.NewPostgresConnectionTestRepository(testerPool)
	claim, err := testerRepository.ClaimConnectionTest(
		ctx,
		tenantID,
		"integration-fixture-"+sourceID,
		time.Minute,
	)
	if err != nil {
		return datasource.Source{}, fmt.Errorf("claim connection-test fixture: %w", err)
	}
	if claim == nil {
		return datasource.Source{}, fmt.Errorf(
			"connection-test fixture %s was not claimable", job.ID,
		)
	}
	if claim.Job.ID != job.ID ||
		claim.Source.ID != sourceID ||
		claim.Source.ConfigVersionID != draft.ConfigVersionID ||
		claim.Source.ConfigHash != draft.ConfigHash {
		return datasource.Source{}, fmt.Errorf(
			"connection-test claim did not preserve exact source version/hash: job=%s source=%s version=%s hash=%s",
			claim.Job.ID,
			claim.Source.ID,
			claim.Source.ConfigVersionID,
			claim.Source.ConfigHash,
		)
	}
	if err := testerRepository.CompleteConnectionTest(
		ctx,
		tenantID,
		claim.Job.ID,
		claim.LeaseToken,
		"integration-fixture",
		0,
	); err != nil {
		return datasource.Source{}, fmt.Errorf("complete connection-test fixture: %w", err)
	}

	published, err := service.Publish(ctx, tenantID, actorID, sourceID)
	if err != nil {
		return datasource.Source{}, fmt.Errorf("publish attested data-source fixture: %w", err)
	}
	if published.Status != datasource.StatusActive ||
		published.PublicationStatus != datasource.PublicationPublished ||
		published.PublishedVersionID != draft.ConfigVersionID ||
		published.ConfigHash != draft.ConfigHash {
		return datasource.Source{}, fmt.Errorf(
			"published data source does not match attested version/hash: status=%s publication=%s version=%s hash=%s",
			published.Status,
			published.PublicationStatus,
			published.PublishedVersionID,
			published.ConfigHash,
		)
	}
	return published, nil
}
