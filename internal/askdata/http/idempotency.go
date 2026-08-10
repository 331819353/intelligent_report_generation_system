package askdatahttp

import (
	"context"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"

	"intelligent-report-generation-system/internal/askdata"
	platformidempotency "intelligent-report-generation-system/internal/platform/idempotency"
)

type IdempotencyState = platformidempotency.State

const (
	IdempotencyAcquired = platformidempotency.StateAcquired
	IdempotencyReplay   = platformidempotency.StateReplay
	IdempotencyInFlight = platformidempotency.StateInFlight
	IdempotencyReused   = platformidempotency.StateReused
)

type IdempotencyRecord = platformidempotency.Record
type IdempotencyRepository = platformidempotency.Repository
type PostgresIdempotencyRepository = platformidempotency.PostgresRepository

func NewPostgresIdempotencyRepository(pool *pgxpool.Pool) *PostgresIdempotencyRepository {
	return platformidempotency.NewPostgresRepository(pool)
}

func canonicalRequestBody(raw []byte) ([]byte, askdata.ContentHash, error) {
	canonical, hash, err := platformidempotency.CanonicalRequestBody(raw)
	return canonical, askdata.ContentHash(hash), err
}

func idempotencyMiddleware(
	repository IdempotencyRepository,
	identity identityResolver,
	next http.Handler,
) http.Handler {
	return platformidempotency.Middleware(platformidempotency.MiddlewareOptions{
		Repository: repository,
		ResolveIdentity: func(ctx context.Context) (platformidempotency.Identity, error) {
			resolved, err := identity(ctx)
			if err != nil {
				return platformidempotency.Identity{}, err
			}
			return platformidempotency.Identity{
				TenantID: string(resolved.TenantID), ActorID: string(resolved.ActorID),
			}, nil
		},
		Requires:        platformidempotency.RequiresGovernedWrite,
		WriteError:      writeError,
		MaxRequestBytes: maxQuestionBodyBytes,
	}, next)
}

func requiresIdempotency(request *http.Request) bool {
	return platformidempotency.RequiresGovernedWrite(request)
}
