package orchestrator

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/platform/database"
)

var ErrInvalidShadowDispatch = errors.New("release shadow dispatch is invalid")

type ShadowJob struct {
	ID                       askdata.ID
	TenantID                 askdata.ID
	DomainID                 askdata.ID
	RolloutID                askdata.ID
	ActorID                  askdata.ID
	ConversationID           askdata.ID
	SourceRunID              askdata.ID
	SourceReleaseID          askdata.ID
	SourceReleaseContentHash askdata.ContentHash
	CandidateReleaseID       askdata.ID
	CandidateContentHash     askdata.ContentHash
	QuestionHash             askdata.ContentHash
	Limits                   BudgetLimits
	LeaseToken               askdata.ID
}

// ShadowJobStore owns the RLS-safe queue functions. The queue contains only
// identifiers, immutable release pins and bounded budgets; question plaintext
// remains exclusively in the envelope store.
type ShadowJobStore struct{ pool *pgxpool.Pool }

func NewShadowJobStore(pool *pgxpool.Pool) *ShadowJobStore { return &ShadowJobStore{pool: pool} }

func (store *ShadowJobStore) ListTenantIDs(ctx context.Context) ([]string, error) {
	if store == nil || store.pool == nil {
		return nil, ErrInvalidShadowDispatch
	}
	rows, err := store.pool.Query(ctx, `SELECT tenant_id::text FROM askdata.list_release_shadow_job_tenants()`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []string{}
	for rows.Next() {
		var tenantID string
		if err := rows.Scan(&tenantID); err != nil {
			return nil, err
		}
		result = append(result, tenantID)
	}
	return result, rows.Err()
}

func (store *ShadowJobStore) Claim(
	ctx context.Context, tenantID, workerID string, lease time.Duration,
) (ShadowJob, bool, error) {
	if store == nil || store.pool == nil || tenantID == "" || workerID == "" {
		return ShadowJob{}, false, ErrInvalidShadowDispatch
	}
	seconds := int(lease.Seconds())
	if seconds < 30 || seconds > 600 {
		return ShadowJob{}, false, ErrInvalidShadowDispatch
	}
	var job ShadowJob
	err := store.pool.QueryRow(ctx, `SELECT
		claimed_job_id::text,claimed_domain_id::text,claimed_rollout_id::text,
		claimed_actor_id::text,claimed_conversation_id::text,claimed_source_run_id::text,
		claimed_source_release_id::text,claimed_source_release_content_hash,
		claimed_candidate_release_id::text,claimed_candidate_content_hash,claimed_question_hash,
		claimed_max_steps,claimed_max_llm_calls,claimed_max_tool_calls,
		claimed_max_formal_queries,claimed_max_validation_queries,claimed_max_duration_ms,
		claimed_lease_token::text
		FROM askdata.claim_release_shadow_job($1::uuid,$2,$3)`, tenantID, workerID, seconds).Scan(
		&job.ID, &job.DomainID, &job.RolloutID, &job.ActorID, &job.ConversationID,
		&job.SourceRunID, &job.SourceReleaseID, &job.SourceReleaseContentHash,
		&job.CandidateReleaseID, &job.CandidateContentHash, &job.QuestionHash,
		&job.Limits.MaxSteps, &job.Limits.MaxLLMCalls, &job.Limits.MaxToolCalls,
		&job.Limits.MaxFormalQueries, &job.Limits.MaxValidationQueries, &job.Limits.MaxDurationMS,
		&job.LeaseToken,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return ShadowJob{}, false, nil
	}
	if err != nil {
		return ShadowJob{}, false, err
	}
	job.TenantID = askdata.ID(tenantID)
	return job, true, nil
}

func (store *ShadowJobStore) Complete(
	ctx context.Context, jobID, leaseToken, shadowRunID askdata.ID, errorCode string,
) error {
	if store == nil || store.pool == nil {
		return ErrInvalidShadowDispatch
	}
	var completed bool
	if err := store.pool.QueryRow(ctx, `SELECT askdata.complete_release_shadow_job($1,$2,$3,$4)`,
		jobID, leaseToken, nullableID(shadowRunID), errorCode).Scan(&completed); err != nil {
		return err
	}
	if !completed {
		return ErrInvalidShadowDispatch
	}
	return nil
}

// ShadowDispatcher converts one terminal control run into one hidden candidate
// run. Its deterministic idempotency key makes a lease retry converge on the
// exact same run instead of double-spending model or warehouse work.
type ShadowDispatcher struct {
	jobs      *ShadowJobStore
	leases    *LeaseStore
	runs      *PostgresStore
	envelopes *PostgresQuestionEnvelopeStore
	workerID  string
	lease     time.Duration
}

func NewShadowDispatcher(
	jobs *ShadowJobStore,
	leases *LeaseStore,
	runs *PostgresStore,
	envelopes *PostgresQuestionEnvelopeStore,
	workerID string,
) (*ShadowDispatcher, error) {
	if jobs == nil || leases == nil || runs == nil || envelopes == nil || workerID == "" {
		return nil, ErrInvalidShadowDispatch
	}
	return &ShadowDispatcher{
		jobs: jobs, leases: leases, runs: runs, envelopes: envelopes,
		workerID: workerID, lease: 5 * time.Minute,
	}, nil
}

func (dispatcher *ShadowDispatcher) ProcessNext(ctx context.Context, tenantID string) (bool, error) {
	if dispatcher == nil {
		return false, ErrInvalidShadowDispatch
	}
	job, ok, err := dispatcher.jobs.Claim(ctx, tenantID, dispatcher.workerID, dispatcher.lease)
	if err != nil || !ok {
		return false, err
	}
	executionCtx := database.WithAccessContext(ctx, string(job.ActorID), string(job.DomainID))
	roleIDs, err := dispatcher.leases.ActorRoleIDs(executionCtx, job.TenantID, job.ActorID, job.SourceRunID)
	if err != nil {
		return true, errors.Join(err, dispatcher.jobs.Complete(executionCtx, job.ID, job.LeaseToken, "", "SHADOW_ROLE_SCOPE_UNAVAILABLE"))
	}
	sourceScope, err := askdata.NewPolicyScope(job.TenantID, job.ActorID, []askdata.ID{job.DomainID}, roleIDs,
		askdata.ReleaseRef{ReleaseID: job.SourceReleaseID, ContentHash: job.SourceReleaseContentHash})
	if err != nil {
		return true, errors.Join(err, dispatcher.jobs.Complete(executionCtx, job.ID, job.LeaseToken, "", "SHADOW_SOURCE_SCOPE_INVALID"))
	}
	candidateScope, err := askdata.NewPolicyScope(job.TenantID, job.ActorID, []askdata.ID{job.DomainID}, roleIDs,
		askdata.ReleaseRef{ReleaseID: job.CandidateReleaseID, ContentHash: job.CandidateContentHash})
	if err != nil {
		return true, errors.Join(err, dispatcher.jobs.Complete(executionCtx, job.ID, job.LeaseToken, "", "SHADOW_CANDIDATE_SCOPE_INVALID"))
	}
	idempotencyHash := askdata.HashBytes([]byte("release-shadow-run-v1\x00" + string(job.RolloutID) + "\x00" + string(job.SourceRunID)))
	created, err := dispatcher.runs.CreateRun(executionCtx, CreateRunRequest{
		Scope: candidateScope, DomainID: job.DomainID, ConversationID: job.ConversationID,
		ExecutionMode: "SHADOW", SourceRunID: job.SourceRunID,
		IdempotencyKeyHash: idempotencyHash, QuestionHash: job.QuestionHash, Limits: job.Limits,
	})
	if err != nil {
		return true, errors.Join(err, dispatcher.jobs.Complete(executionCtx, job.ID, job.LeaseToken, "", "SHADOW_RUN_CREATE_FAILED"))
	}
	now := time.Now().UTC()
	err = dispatcher.envelopes.SaveShadowQuestion(executionCtx,
		QuestionRetentionBinding{Scope: sourceScope, DomainID: job.DomainID, ConversationID: job.ConversationID, RunID: job.SourceRunID, QuestionHash: job.QuestionHash},
		QuestionRetentionBinding{Scope: candidateScope, DomainID: job.DomainID, ConversationID: job.ConversationID, RunID: created.Run.ID, QuestionHash: job.QuestionHash},
		now,
	)
	if err != nil {
		details := []byte(`{"code":"SHADOW_CONTEXT_UNAVAILABLE"}`)
		// Publish the run/job binding before the terminal transition so the
		// observation trigger can pair this explicit failure with its control.
		if completeErr := dispatcher.jobs.Complete(executionCtx, job.ID, job.LeaseToken, created.Run.ID, "SHADOW_CONTEXT_UNAVAILABLE"); completeErr != nil {
			return true, errors.Join(err, completeErr)
		}
		_, closeErr := dispatcher.runs.Transition(executionCtx, TransitionRequest{
			Scope: candidateScope, DomainID: job.DomainID, RunID: created.Run.ID,
			ExpectedVersion: created.Run.RecordVersion, TargetState: StateBlocked, Usage: created.Run.Usage,
			Completion: &CompletionArtifactInput{Code: "SHADOW_CONTEXT_UNAVAILABLE", Type: ArtifactBlock, SchemaVersion: RunBlockSchemaVersion, Payload: details},
			Event:      TransitionEventInput{Stage: string(StateBlocked), Status: EventBlocked, Code: "SHADOW_CONTEXT_UNAVAILABLE", Details: details},
		})
		return true, errors.Join(err, closeErr)
	}
	return true, dispatcher.jobs.Complete(executionCtx, job.ID, job.LeaseToken, created.Run.ID, "")
}

func (dispatcher *ShadowDispatcher) ListTenantIDs(ctx context.Context) ([]string, error) {
	return dispatcher.jobs.ListTenantIDs(ctx)
}
