package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"intelligent-report-generation-system/internal/askdata"
)

var ErrInvalidLease = errors.New("question run lease request is invalid")

// ResumeMode says what a claimed run is safe to do next. The claim function
// decides it from durable state; callers must not infer it themselves.
type ResumeMode string

const (
	// ResumeFresh is a run still at RECEIVED that has never executed.
	ResumeFresh ResumeMode = "FRESH"
	// ResumeAbandoned is a run whose worker died mid-flight. Its budget was
	// already charged and its tool calls may already have reached the
	// warehouse, so it is only ever finalised, never re-executed.
	ResumeAbandoned ResumeMode = "ABANDONED"
)

// LeasedRun is one claimed question run plus the token proving ownership.
type LeasedRun struct {
	RunID              askdata.ID
	DomainID           askdata.ID
	ActorID            askdata.ID
	ReleaseID          askdata.ID
	ReleaseContentHash askdata.ContentHash
	CurrentState       State
	RecordVersion      int64
	LeaseToken         askdata.ID
	Attempt            int
	ResumeMode         ResumeMode
}

// LeaseStore claims and maintains question run execution leases.
type LeaseStore struct{ pool *pgxpool.Pool }

func NewLeaseStore(pool *pgxpool.Pool) *LeaseStore { return &LeaseStore{pool: pool} }

// ListTenantIDs enumerates tenants that may have claimable runs.
func (store *LeaseStore) ListTenantIDs(ctx context.Context) ([]string, error) {
	if store == nil || store.pool == nil {
		return nil, ErrInvalidLease
	}
	rows, err := store.pool.Query(ctx, `SELECT DISTINCT tenant_id::text
		FROM askdata.question_runs
		WHERE current_state NOT IN (
		  'CLARIFICATION_REQUIRED','CLARIFICATION_EXPIRED',
		  'OUT_OF_SCOPE','ANSWERED','BLOCKED'
		)
		ORDER BY 1`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	tenants := []string{}
	for rows.Next() {
		var tenantID string
		if err := rows.Scan(&tenantID); err != nil {
			return nil, err
		}
		tenants = append(tenants, tenantID)
	}
	return tenants, rows.Err()
}

// Claim leases the next runnable question run for this worker, or reports that
// nothing is claimable. The lease functions run SECURITY DEFINER because a
// worker has no actor session; tenant scoping is an explicit argument.
func (store *LeaseStore) Claim(
	ctx context.Context,
	tenantID, workerID string,
	lease time.Duration,
) (LeasedRun, bool, error) {
	if store == nil || store.pool == nil || tenantID == "" || workerID == "" {
		return LeasedRun{}, false, ErrInvalidLease
	}
	seconds := int(lease.Seconds())
	if seconds < 30 || seconds > 600 {
		return LeasedRun{}, false, fmt.Errorf("%w: lease must be 30s-600s", ErrInvalidLease)
	}
	var claimed LeasedRun
	var mode string
	err := store.pool.QueryRow(ctx,
		`SELECT claimed_run_id::text,claimed_domain_id::text,claimed_actor_id::text,
			claimed_release_id::text,claimed_release_content_hash,claimed_current_state,
			claimed_record_version,claimed_lease_token::text,claimed_attempt,claimed_resume_mode
		 FROM askdata.claim_question_run($1::uuid,$2,$3)`,
		tenantID, workerID, seconds,
	).Scan(
		&claimed.RunID, &claimed.DomainID, &claimed.ActorID, &claimed.ReleaseID,
		&claimed.ReleaseContentHash, &claimed.CurrentState, &claimed.RecordVersion,
		&claimed.LeaseToken, &claimed.Attempt, &mode,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return LeasedRun{}, false, nil
	}
	if err != nil {
		return LeasedRun{}, false, err
	}
	claimed.ResumeMode = ResumeMode(mode)
	return claimed, true, nil
}

// Heartbeat extends a held lease. It returns false when the lease has already
// expired and been taken over, which is the signal to abandon the work rather
// than keep writing to a run someone else now owns.
func (store *LeaseStore) Heartbeat(
	ctx context.Context,
	runID, token askdata.ID,
	lease time.Duration,
) (bool, error) {
	if store == nil || store.pool == nil {
		return false, ErrInvalidLease
	}
	seconds := int(lease.Seconds())
	if seconds < 30 || seconds > 600 {
		return false, fmt.Errorf("%w: lease must be 30s-600s", ErrInvalidLease)
	}
	var extended bool
	if err := store.pool.QueryRow(ctx,
		`SELECT askdata.heartbeat_question_run($1::uuid,$2::uuid,$3)`,
		string(runID), string(token), seconds,
	).Scan(&extended); err != nil {
		return false, err
	}
	return extended, nil
}

// Release drops a held lease. Whether the run succeeded is recorded on the run
// itself; the lease table only ever tracks the right to execute.
func (store *LeaseStore) Release(ctx context.Context, runID, token askdata.ID) error {
	if store == nil || store.pool == nil {
		return ErrInvalidLease
	}
	var released bool
	return store.pool.QueryRow(ctx,
		`SELECT askdata.release_question_run($1::uuid,$2::uuid)`,
		string(runID), string(token),
	).Scan(&released)
}
