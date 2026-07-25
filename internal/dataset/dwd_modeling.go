package dataset

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	aiplatform "intelligent-report-generation-system/internal/ai"
	"intelligent-report-generation-system/internal/platform/database"
)

var (
	errDWDModelingInvalid       = errors.New("DWD modeling request is invalid")
	errDWDModelingLeaseLost     = errors.New("DWD modeling lease was lost")
	errDWDModelingTagsNotReady  = errors.New("ODS domain or table role tags are not ready")
	errDWDModelingSubjectChange = errors.New("ODS publication is no longer current")
)

// DWDModelingWorker consumes the durable outbox written when an ODS version becomes
// PUBLISHED. It produces reviewable DIM and DWD drafts; normal publication approval,
// materialization and LLM tag suggestion remain the downstream governance boundaries.
type DWDModelingWorker struct {
	store   *PostgresStore
	planner DWDModelingPlanner
}

func NewDWDModelingWorker(
	store *PostgresStore,
	planners ...DWDModelingPlanner,
) *DWDModelingWorker {
	worker := &DWDModelingWorker{store: store}
	if len(planners) > 0 {
		worker.planner = planners[0]
	}
	return worker
}

type dwdModelingClaim struct {
	ID                      string
	TenantID                string
	TriggerDatasetID        string
	TriggerDatasetVersionID string
	ActorID                 string
	LeaseToken              string
	Attempt                 int
	MaxAttempts             int
	CheckpointVersion       int
}

type dwdODSAsset struct {
	DatasetID   string
	VersionID   string
	SchemaHash  string
	Code        string
	Name        string
	Description string
	Domains     []string
	Tags        []string
	Document    Document
}

type dwdModelingResultItem struct {
	Layer           string `json:"layer"`
	SourceDatasetID string `json:"sourceDatasetId,omitempty"`
	SourceVersionID string `json:"sourceVersionId,omitempty"`
	DatasetID       string `json:"datasetId,omitempty"`
	FactDatasetID   string `json:"factDatasetId,omitempty"`
	FactVersionID   string `json:"factVersionId,omitempty"`
	DWDDatasetID    string `json:"dwdDatasetId,omitempty"`
	Action          string `json:"action"`
	DimensionCount  int    `json:"dimensionCount,omitempty"`
	Reason          string `json:"reason,omitempty"`
}

func (worker *DWDModelingWorker) TenantIDs(ctx context.Context) ([]string, error) {
	if worker == nil || worker.store == nil || worker.store.pool == nil {
		return nil, errDWDModelingInvalid
	}
	rows, err := worker.store.pool.Query(ctx, `SELECT id::text
		FROM platform.tenants
		WHERE status='ACTIVE' AND deleted_at IS NULL
		ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	tenantIDs := []string{}
	for rows.Next() {
		var tenantID string
		if err := rows.Scan(&tenantID); err != nil {
			return nil, err
		}
		tenantIDs = append(tenantIDs, tenantID)
	}
	return tenantIDs, rows.Err()
}

func (worker *DWDModelingWorker) ProcessNext(
	ctx context.Context,
	tenantID, workerID string,
	lease time.Duration,
) (bool, error) {
	if worker == nil || worker.store == nil || worker.store.pool == nil ||
		uuid.Validate(tenantID) != nil || !validDWDWorkerID(workerID) ||
		lease < time.Second || lease > time.Hour {
		return false, errDWDModelingInvalid
	}
	if worker.planner == nil || !worker.planner.Configured() {
		return false, nil
	}
	claim, err := worker.claimNext(ctx, tenantID, workerID, lease)
	if err != nil || claim == nil {
		return false, err
	}
	err = worker.process(ctx, *claim, workerID, lease)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, errDWDModelingSubjectChange):
		return true, worker.finishWithoutOutput(ctx, *claim, workerID, "SKIPPED", "SUBJECT_CHANGED", err.Error())
	case errors.Is(err, errDWDModelingTagsNotReady):
		return true, worker.retryOrSkip(ctx, *claim, workerID, "CLASSIFICATION_NOT_READY", err.Error())
	default:
		if terminal, errorCode := terminalDWDModelingFailure(err); terminal {
			finishErr := worker.finishWithoutOutput(
				ctx, *claim, workerID, "FAILED", errorCode,
				"DIM/DWD 分层建模遇到不可重试错误",
			)
			return true, errors.Join(err, finishErr)
		}
		retryErr := worker.retryOrSkip(
			ctx, *claim, workerID,
			"WAREHOUSE_MODELING_FAILED", "DIM/DWD 分层建模执行失败",
		)
		return true, errors.Join(err, retryErr)
	}
}

func terminalDWDModelingFailure(err error) (bool, string) {
	if errors.Is(err, errDWDModelingInvalid) {
		return true, "WAREHOUSE_MODELING_INVALID_OUTPUT"
	}
	var providerError *aiplatform.ProviderError
	if !errors.As(err, &providerError) || providerError.Retryable {
		return false, ""
	}
	if providerError.Code == aiplatform.ErrorCodeCanceled {
		// Worker shutdown cancellation leaves the RUNNING claim for normal lease
		// recovery. Treating it as terminal here would also fail because ctx is
		// already canceled and would obscure the resumable path.
		return false, ""
	}
	return true, string(providerError.Code)
}

func validDWDWorkerID(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 128 {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}

func (worker *DWDModelingWorker) claimNext(
	ctx context.Context,
	tenantID, workerID string,
	lease time.Duration,
) (claim *dwdModelingClaim, err error) {
	err = database.WithTenantTx(ctx, worker.store.pool, tenantID, func(tx pgx.Tx) error {
		// Serialize claim decisions per tenant. Without this fence, two worker
		// replicas could claim different ODS triggers from the same domain before
		// the first LLM plan has a chance to coalesce the sibling jobs.
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(
			hashtext('platform.dwd_modeling_jobs'),hashtext($1)
		)`, tenantID); err != nil {
			return err
		}
		// A crashed claim that committed at least one new validated checkpoint
		// made real progress. Reset its consecutive-failure budget before the
		// terminal-expiry sweep so a different FACT can resume from that point.
		if _, err := tx.Exec(ctx, `UPDATE platform.dwd_modeling_jobs
			SET status='PENDING',attempt=0,next_attempt_at=now(),
				error_code='RESUMING_FROM_CHECKPOINT',
				error_message='上次执行已保存有效检查点，将从缺失阶段继续',
				lease_owner='',lease_token=NULL,lease_expires_at=NULL,
				updated_at=now()
			WHERE status='RUNNING' AND lease_expires_at<=now()
			  AND checkpoint_version>claimed_checkpoint_version`); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE platform.dwd_modeling_jobs
			SET status='FAILED',error_code='LEASE_EXPIRED',
				error_message='worker lease expired after maximum attempts',
				lease_owner='',lease_token=NULL,lease_expires_at=NULL,
				completed_at=now(),updated_at=now()
			WHERE status='RUNNING' AND lease_expires_at<=now()
			  AND attempt>=max_attempts
			  AND checkpoint_version=claimed_checkpoint_version`); err != nil {
			return err
		}
		item := dwdModelingClaim{TenantID: tenantID}
		err := tx.QueryRow(ctx, `WITH candidate AS (
			SELECT queued.id
			FROM platform.dwd_modeling_jobs AS queued
			WHERE queued.attempt<queued.max_attempts
			  AND (
			    (queued.status='PENDING' AND queued.next_attempt_at<=now())
			    OR (queued.status='RUNNING' AND queued.lease_expires_at<=now())
			  )
			  AND NOT EXISTS(
			    SELECT 1
			    FROM platform.dwd_modeling_jobs AS active
			    WHERE active.status='RUNNING'
			      AND active.lease_expires_at>now()
			  )
			ORDER BY queued.next_attempt_at,queued.created_at,queued.id
			FOR UPDATE SKIP LOCKED
			LIMIT 1
		)
		UPDATE platform.dwd_modeling_jobs AS job
		SET status='RUNNING',attempt=attempt+1,error_code='',error_message='',
			lease_owner=$1,lease_token=public.gen_random_uuid(),
			lease_expires_at=now()+($2*interval '1 second'),
			prompt_version=$3,
			claimed_checkpoint_version=checkpoint_version,
			started_at=COALESCE(started_at,now()),completed_at=NULL,updated_at=now()
		FROM candidate
		WHERE job.id=candidate.id
		RETURNING job.id::text,job.trigger_dataset_id::text,
			job.trigger_dataset_version_id::text,COALESCE(job.requested_by::text,''),
			job.lease_token::text,job.attempt,job.max_attempts,
			job.claimed_checkpoint_version`,
			workerID, int64(lease/time.Second), dwdModelingPromptVersion,
		).Scan(
			&item.ID, &item.TriggerDatasetID, &item.TriggerDatasetVersionID,
			&item.ActorID, &item.LeaseToken, &item.Attempt, &item.MaxAttempts,
			&item.CheckpointVersion,
		)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		claim = &item
		return nil
	})
	return claim, err
}

func (worker *DWDModelingWorker) process(
	ctx context.Context,
	claim dwdModelingClaim,
	workerID string,
	lease time.Duration,
) error {
	input, snapshotHash, err := worker.loadPlanningInput(ctx, claim, workerID)
	if err != nil {
		return err
	}
	if planner, ok := worker.planner.(resumableDWDModelingPlanner); ok {
		completion, err := worker.planWithCheckpoints(
			ctx, claim, workerID, lease, input, snapshotHash, planner,
		)
		if err != nil {
			return err
		}
		return worker.persistLLMPlan(
			ctx, claim, workerID, input, snapshotHash, completion,
		)
	}
	completion, err := worker.planner.Plan(ctx, input)
	if err != nil {
		return err
	}
	return worker.persistLLMPlan(
		ctx, claim, workerID, input, snapshotHash, completion,
	)
}

func (worker *DWDModelingWorker) planWithCheckpoints(
	ctx context.Context,
	claim dwdModelingClaim,
	workerID string,
	lease time.Duration,
	input dwdPlanningInput,
	snapshotHash string,
	planner resumableDWDModelingPlanner,
) (dwdPlanningCompletion, error) {
	completion := dwdPlanningCompletion{
		Plan: dwdLLMPlan{Domain: input.Domain},
	}
	classification, reused, err := worker.loadDWDClassificationCheckpoint(
		ctx, claim, workerID, input, snapshotHash,
	)
	if err != nil {
		return dwdPlanningCompletion{}, err
	}
	if reused {
		completion.ReusedCheckpointCount++
	} else {
		if err := worker.renewDWDClaim(
			ctx, claim, workerID, lease,
		); err != nil {
			return dwdPlanningCompletion{}, err
		}
		classification, err = planner.Classify(ctx, input)
		if err != nil {
			return dwdPlanningCompletion{}, err
		}
		classification.Classifications = normalizeDWDClassifications(
			input, classification.Classifications,
		)
		if err := validateDWDLLMClassifications(
			input, classification.Domain, classification.Classifications,
		); err != nil {
			return dwdPlanningCompletion{}, err
		}
		if err := worker.saveDWDModelingCheckpoint(
			ctx, claim, workerID, input, snapshotHash,
			"CLASSIFICATION", claim.TriggerDatasetVersionID,
			dwdClassificationPromptVersion, classification.AIRequestID,
			dwdLLMClassificationPlan{
				Domain:          classification.Domain,
				Classifications: classification.Classifications,
			},
		); err != nil {
			return dwdPlanningCompletion{}, err
		}
	}
	completion.CheckpointCount++
	completion.AIRequestID = classification.AIRequestID
	completion.Plan.Domain = classification.Domain
	completion.Plan.Classifications = append(
		[]dwdLLMClassification(nil), classification.Classifications...,
	)

	for _, classificationItem := range classification.Classifications {
		if classificationItem.Role != "FACT" {
			continue
		}
		factCompletion, factReused, err := worker.loadDWDFactCheckpoint(
			ctx, claim, workerID, input, snapshotHash,
			classification.Classifications,
			classificationItem.DatasetVersionID,
		)
		if err != nil {
			return dwdPlanningCompletion{}, err
		}
		if factReused {
			completion.ReusedCheckpointCount++
		} else {
			if err := worker.renewDWDClaim(
				ctx, claim, workerID, lease,
			); err != nil {
				return dwdPlanningCompletion{}, err
			}
			factCompletion, err = planner.DesignFact(
				ctx, input, classification.Classifications,
				classificationItem.DatasetVersionID,
			)
			if err != nil {
				return dwdPlanningCompletion{}, err
			}
			if err := validateDWDFactCheckpoint(
				input, classification.Classifications,
				classificationItem.DatasetVersionID, factCompletion.Output,
			); err != nil {
				return dwdPlanningCompletion{}, err
			}
			if err := worker.saveDWDModelingCheckpoint(
				ctx, claim, workerID, input, snapshotHash,
				"FACT_DESIGN", classificationItem.DatasetVersionID,
				dwdFactDesignPromptVersion, factCompletion.AIRequestID,
				dwdLLMFactDesign{Output: factCompletion.Output},
			); err != nil {
				return dwdPlanningCompletion{}, err
			}
		}
		completion.CheckpointCount++
		completion.AIRequestID = factCompletion.AIRequestID
		completion.Plan.Outputs = append(
			completion.Plan.Outputs, factCompletion.Output,
		)
	}
	if err := validateDWDLLMPlan(input, completion.Plan); err != nil {
		return dwdPlanningCompletion{}, err
	}
	return completion, nil
}

func (worker *DWDModelingWorker) renewDWDClaim(
	ctx context.Context,
	claim dwdModelingClaim,
	workerID string,
	lease time.Duration,
) error {
	return database.WithTenantTx(
		ctx, worker.store.pool, claim.TenantID, func(tx pgx.Tx) error {
			tag, err := tx.Exec(ctx, `UPDATE platform.dwd_modeling_jobs SET
				lease_expires_at=now()+($1*interval '1 second'),
				updated_at=now()
				WHERE id=$2::uuid AND status='RUNNING'
				  AND lease_owner=$3 AND lease_token=$4::uuid
				  AND attempt=$5 AND lease_expires_at>now()`,
				int64(lease/time.Second), claim.ID, workerID,
				claim.LeaseToken, claim.Attempt,
			)
			if err != nil {
				return err
			}
			if tag.RowsAffected() != 1 {
				return errDWDModelingLeaseLost
			}
			return nil
		},
	)
}

func (worker *DWDModelingWorker) loadDWDClassificationCheckpoint(
	ctx context.Context,
	claim dwdModelingClaim,
	workerID string,
	input dwdPlanningInput,
	snapshotHash string,
) (dwdClassificationCompletion, bool, error) {
	var completion dwdClassificationCompletion
	found := false
	err := database.WithTenantTx(
		ctx, worker.store.pool, claim.TenantID, func(tx pgx.Tx) error {
			if err := validateDWDClaimTx(ctx, tx, claim, workerID); err != nil {
				return err
			}
			var raw json.RawMessage
			var payloadHash string
			err := tx.QueryRow(ctx, `SELECT payload_json,payload_hash,
					ai_request_id::text
				FROM platform.dwd_modeling_checkpoints
				WHERE job_id=$1::uuid
				  AND checkpoint_kind='CLASSIFICATION'
				  AND subject_dataset_version_id=$2::uuid
				  AND snapshot_hash=$3 AND prompt_version=$4`,
				claim.ID, claim.TriggerDatasetVersionID, snapshotHash,
				dwdClassificationPromptVersion,
			).Scan(&raw, &payloadHash, &completion.AIRequestID)
			if errors.Is(err, pgx.ErrNoRows) {
				return nil
			}
			if err != nil {
				return err
			}
			found = true
			if err := validateDWDCheckpointHash(raw, payloadHash); err != nil {
				return err
			}
			payload, err := decodeDWDClassificationPlan(raw)
			if err != nil {
				return err
			}
			payload.Classifications = normalizeDWDClassifications(
				input, payload.Classifications,
			)
			if err := validateDWDLLMClassifications(
				input, payload.Domain, payload.Classifications,
			); err != nil {
				return err
			}
			completion.Domain = payload.Domain
			completion.Classifications = payload.Classifications
			return nil
		},
	)
	return completion, found, err
}

func (worker *DWDModelingWorker) loadDWDFactCheckpoint(
	ctx context.Context,
	claim dwdModelingClaim,
	workerID string,
	input dwdPlanningInput,
	snapshotHash string,
	classifications []dwdLLMClassification,
	factVersionID string,
) (dwdFactDesignCompletion, bool, error) {
	var completion dwdFactDesignCompletion
	found := false
	err := database.WithTenantTx(
		ctx, worker.store.pool, claim.TenantID, func(tx pgx.Tx) error {
			if err := validateDWDClaimTx(ctx, tx, claim, workerID); err != nil {
				return err
			}
			var raw json.RawMessage
			var payloadHash string
			err := tx.QueryRow(ctx, `SELECT payload_json,payload_hash,
					ai_request_id::text
				FROM platform.dwd_modeling_checkpoints
				WHERE job_id=$1::uuid
				  AND checkpoint_kind='FACT_DESIGN'
				  AND subject_dataset_version_id=$2::uuid
				  AND snapshot_hash=$3 AND prompt_version=$4`,
				claim.ID, factVersionID, snapshotHash,
				dwdFactDesignPromptVersion,
			).Scan(&raw, &payloadHash, &completion.AIRequestID)
			if errors.Is(err, pgx.ErrNoRows) {
				return nil
			}
			if err != nil {
				return err
			}
			found = true
			if err := validateDWDCheckpointHash(raw, payloadHash); err != nil {
				return err
			}
			payload, err := decodeDWDFactDesign(raw)
			if err != nil {
				return err
			}
			if err := validateDWDFactCheckpoint(
				input, classifications, factVersionID, payload.Output,
			); err != nil {
				return err
			}
			completion.Output = payload.Output
			return nil
		},
	)
	return completion, found, err
}

func validateDWDFactCheckpoint(
	input dwdPlanningInput,
	classifications []dwdLLMClassification,
	factVersionID string,
	output dwdLLMOutput,
) error {
	scoped, scopedClassifications, err := dwdFactPlanningScope(
		input, classifications, factVersionID,
	)
	if err != nil {
		return err
	}
	return validateDWDLLMPlan(scoped, dwdLLMPlan{
		Domain:          scoped.Domain,
		Classifications: scopedClassifications,
		Outputs:         []dwdLLMOutput{output},
	})
}

func (worker *DWDModelingWorker) saveDWDModelingCheckpoint(
	ctx context.Context,
	claim dwdModelingClaim,
	workerID string,
	input dwdPlanningInput,
	snapshotHash, checkpointKind, subjectVersionID, promptVersion,
	aiRequestID string,
	payload any,
) error {
	if uuid.Validate(aiRequestID) != nil {
		return errDWDModelingInvalid
	}
	raw, payloadHash, err := marshalDWDCheckpoint(payload)
	if err != nil {
		return err
	}
	return database.WithTenantTx(
		ctx, worker.store.pool, claim.TenantID, func(tx pgx.Tx) error {
			if err := validateDWDClaimTx(ctx, tx, claim, workerID); err != nil {
				return err
			}
			if err := validateDWDPlanningSnapshotTx(
				ctx, tx, input.Domain, snapshotHash,
			); err != nil {
				return err
			}
			tag, err := tx.Exec(ctx, `INSERT INTO platform.dwd_modeling_checkpoints(
					tenant_id,job_id,checkpoint_kind,subject_dataset_version_id,
					snapshot_hash,prompt_version,ai_request_id,payload_hash,payload_json
				) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)
				ON CONFLICT(
					tenant_id,job_id,checkpoint_kind,subject_dataset_version_id,
					snapshot_hash,prompt_version
				) DO NOTHING`,
				claim.TenantID, claim.ID, checkpointKind, subjectVersionID,
				snapshotHash, promptVersion, aiRequestID, payloadHash,
				json.RawMessage(raw),
			)
			if err != nil {
				return err
			}
			if tag.RowsAffected() == 0 {
				return nil
			}
			tag, err = tx.Exec(ctx, `UPDATE platform.dwd_modeling_jobs SET
					checkpoint_version=checkpoint_version+1,updated_at=now()
				WHERE id=$1::uuid AND status='RUNNING'
				  AND lease_owner=$2 AND lease_token=$3::uuid
				  AND attempt=$4 AND lease_expires_at>now()`,
				claim.ID, workerID, claim.LeaseToken, claim.Attempt,
			)
			if err != nil {
				return err
			}
			if tag.RowsAffected() != 1 {
				return errDWDModelingLeaseLost
			}
			return nil
		},
	)
}

func marshalDWDCheckpoint(payload any) ([]byte, string, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, "", err
	}
	canonical, err := canonicalDWDCheckpointJSON(raw)
	if err != nil {
		return nil, "", err
	}
	sum := sha256.Sum256(canonical)
	return canonical, hex.EncodeToString(sum[:]), nil
}

func validateDWDCheckpointHash(raw []byte, expected string) error {
	canonical, err := canonicalDWDCheckpointJSON(raw)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(canonical)
	if hex.EncodeToString(sum[:]) != expected {
		return fmt.Errorf(
			"%w: warehouse modeling checkpoint hash mismatch",
			errDWDModelingInvalid,
		)
	}
	return nil
}

func canonicalDWDCheckpointJSON(raw []byte) ([]byte, error) {
	if len(raw) == 0 || len(raw) > 2<<20 {
		return nil, errDWDModelingInvalid
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, errDWDModelingInvalid
	}
	return json.Marshal(value)
}

func validateDWDPlanningSnapshotTx(
	ctx context.Context,
	tx pgx.Tx,
	domain, expectedSnapshotHash string,
) error {
	assets, err := loadPublishedODSAssetsTx(ctx, tx)
	if err != nil {
		return err
	}
	sameDomain := make([]dwdODSAsset, 0, len(assets))
	for _, asset := range assets {
		if containsString(asset.Domains, domain) {
			sameDomain = append(sameDomain, asset)
		}
	}
	currentSnapshotHash, err := dwdPlanningSnapshotHash(sameDomain)
	if err != nil {
		return err
	}
	if currentSnapshotHash != expectedSnapshotHash {
		return errDWDModelingSubjectChange
	}
	return nil
}

func (worker *DWDModelingWorker) loadPlanningInput(
	ctx context.Context,
	claim dwdModelingClaim,
	workerID string,
) (input dwdPlanningInput, snapshotHash string, err error) {
	err = database.WithTenantTx(ctx, worker.store.pool, claim.TenantID, func(tx pgx.Tx) error {
		if err := validateDWDClaimTx(ctx, tx, claim, workerID); err != nil {
			return err
		}
		assets, err := loadPublishedODSAssetsTx(ctx, tx)
		if err != nil {
			return err
		}
		var trigger *dwdODSAsset
		for index := range assets {
			if assets[index].DatasetID == claim.TriggerDatasetID &&
				assets[index].VersionID == claim.TriggerDatasetVersionID {
				trigger = &assets[index]
				break
			}
		}
		if trigger == nil {
			return errDWDModelingSubjectChange
		}
		if len(trigger.Domains) == 0 {
			return errDWDModelingTagsNotReady
		}
		domain := trigger.Domains[0]
		sameDomain := make([]dwdODSAsset, 0, len(assets))
		for _, asset := range assets {
			if containsString(asset.Domains, domain) {
				sameDomain = append(sameDomain, asset)
			}
		}
		sort.Slice(sameDomain, func(i, j int) bool {
			return sameDomain[i].VersionID < sameDomain[j].VersionID
		})
		if len(sameDomain) == 0 || len(sameDomain) > 48 {
			return fmt.Errorf("%w: same-domain ODS count is outside 1..48", errDWDModelingInvalid)
		}
		input = dwdPlanningInput{
			TenantID: claim.TenantID, ActorID: claim.ActorID,
			ResourceID: claim.TriggerDatasetVersionID, Domain: domain,
			Trigger: dwdPlanningTrigger{
				DatasetID: claim.TriggerDatasetID,
				VersionID: claim.TriggerDatasetVersionID,
			},
			Tables: make([]dwdPlanningTable, 0, len(sameDomain)),
		}
		for _, asset := range sameDomain {
			table := dwdPlanningTable{
				DatasetID: asset.DatasetID, VersionID: asset.VersionID,
				Name: asset.Name, Description: asset.Description,
				Tags:        append([]string(nil), asset.Tags...),
				OutputGrain: asset.Document.OutputGrain,
				Fields:      make([]dwdPlanningField, 0, len(asset.Document.Fields)),
			}
			for _, field := range asset.Document.Fields {
				table.Fields = append(table.Fields, dwdPlanningField{
					Code: field.Code, Name: field.Name, Description: field.Description,
					Role: field.Role, CanonicalType: field.CanonicalType,
					SemanticType: field.SemanticType, Nullable: field.Nullable,
				})
			}
			input.Tables = append(input.Tables, table)
		}
		snapshotHash, err = dwdPlanningSnapshotHash(sameDomain)
		return err
	})
	return input, snapshotHash, err
}

func dwdPlanningSnapshotHash(assets []dwdODSAsset) (string, error) {
	type snapshotItem struct {
		DatasetID   string   `json:"datasetId"`
		VersionID   string   `json:"versionId"`
		SchemaHash  string   `json:"schemaHash"`
		Code        string   `json:"code"`
		Name        string   `json:"name"`
		Description string   `json:"description"`
		Domains     []string `json:"domains"`
		Tags        []string `json:"tags"`
	}
	items := make([]snapshotItem, 0, len(assets))
	for _, asset := range assets {
		tags := append([]string(nil), asset.Tags...)
		sort.Strings(tags)
		items = append(items, snapshotItem{
			DatasetID: asset.DatasetID, VersionID: asset.VersionID,
			SchemaHash: asset.SchemaHash, Code: asset.Code,
			Name: asset.Name, Description: asset.Description,
			Domains: append([]string(nil), asset.Domains...), Tags: tags,
		})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].VersionID < items[j].VersionID })
	raw, err := json.Marshal(items)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func (worker *DWDModelingWorker) persistLLMPlan(
	ctx context.Context,
	claim dwdModelingClaim,
	workerID string,
	input dwdPlanningInput,
	expectedSnapshotHash string,
	completion dwdPlanningCompletion,
) error {
	return database.WithTenantTx(ctx, worker.store.pool, claim.TenantID, func(tx pgx.Tx) error {
		if err := validateDWDClaimTx(ctx, tx, claim, workerID); err != nil {
			return err
		}
		assets, err := loadPublishedODSAssetsTx(ctx, tx)
		if err != nil {
			return err
		}
		sameDomain := make([]dwdODSAsset, 0, len(assets))
		for _, asset := range assets {
			if containsString(asset.Domains, input.Domain) {
				sameDomain = append(sameDomain, asset)
			}
		}
		currentSnapshotHash, err := dwdPlanningSnapshotHash(sameDomain)
		if err != nil {
			return err
		}
		if currentSnapshotHash != expectedSnapshotHash {
			return errDWDModelingSubjectChange
		}
		if err := validateDWDLLMPlan(input, completion.Plan); err != nil {
			return err
		}
		assetsByVersion := map[string]dwdODSAsset{}
		for _, asset := range sameDomain {
			assetsByVersion[asset.VersionID] = asset
		}
		roleByVersion := map[string]string{}
		for _, classification := range completion.Plan.Classifications {
			roleByVersion[classification.DatasetVersionID] = classification.Role
		}
		triggerRole := roleByVersion[claim.TriggerDatasetVersionID]
		items := make(
			[]dwdModelingResultItem, 0,
			len(completion.Plan.Classifications)+len(completion.Plan.Outputs),
		)
		created, updated, skipped := 0, 0, 0
		// DIM 与 DWD 是同一 ODS 元数据快照上的两个独立设计分支。这里按稳定
		// 顺序持久化以缩短事务锁时间；物理开发引擎会按 DAG 依赖并行调度。
		for _, classification := range completion.Plan.Classifications {
			if classification.Role != "DIMENSION" && classification.Role != "MASTER" {
				continue
			}
			source, exists := assetsByVersion[classification.DatasetVersionID]
			if !exists {
				return errDWDModelingSubjectChange
			}
			document, inputHash, buildErr := buildLLMClassifiedDIMDocument(
				input.Domain, source,
			)
			if buildErr != nil {
				skipped++
				items = append(items, dwdModelingResultItem{
					Layer: string(LayerDIM), SourceDatasetID: source.DatasetID,
					SourceVersionID: source.VersionID, Action: "SKIPPED",
					Reason: "DIM_DAG_INVALID",
				})
				continue
			}
			prepared, prepareErr := Prepare(mustMarshalDWDDocument(document))
			if prepareErr != nil {
				skipped++
				items = append(items, dwdModelingResultItem{
					Layer: string(LayerDIM), SourceDatasetID: source.DatasetID,
					SourceVersionID: source.VersionID, Action: "SKIPPED",
					Reason: "DIM_DAG_INVALID",
				})
				continue
			}
			dimDatasetID, action, upsertErr := worker.upsertGeneratedDIMDraftTx(
				ctx, tx, claim, source, input.Domain, inputHash, prepared,
			)
			if upsertErr != nil {
				if errors.Is(upsertErr, ErrConflict) || errors.Is(upsertErr, ErrAlreadyExists) {
					skipped++
					items = append(items, dwdModelingResultItem{
						Layer: string(LayerDIM), SourceDatasetID: source.DatasetID,
						SourceVersionID: source.VersionID, Action: "SKIPPED",
						Reason: "MANUAL_DRAFT_CHANGED",
					})
					continue
				}
				return upsertErr
			}
			switch action {
			case "CREATED":
				created++
			case "UPDATED":
				updated++
			}
			items = append(items, dwdModelingResultItem{
				Layer: string(LayerDIM), SourceDatasetID: source.DatasetID,
				SourceVersionID: source.VersionID, DatasetID: dimDatasetID,
				Action: action,
			})
		}

		for _, output := range completion.Plan.Outputs {
			fact, exists := assetsByVersion[output.FactDatasetVersionID]
			if !exists {
				return errDWDModelingSubjectChange
			}
			document, inputHash, buildErr := buildLLMDesignedDWDDocument(
				input.Domain, fact, assetsByVersion, output,
			)
			if buildErr != nil {
				skipped++
				items = append(items, dwdModelingResultItem{
					Layer:         string(LayerDWD),
					FactDatasetID: fact.DatasetID, FactVersionID: fact.VersionID,
					Action: "SKIPPED", DimensionCount: len(output.Joins),
					Reason: "LLM_DAG_INVALID",
				})
				continue
			}
			prepared, prepareErr := Prepare(mustMarshalDWDDocument(document))
			if prepareErr != nil {
				skipped++
				items = append(items, dwdModelingResultItem{
					Layer:         string(LayerDWD),
					FactDatasetID: fact.DatasetID, FactVersionID: fact.VersionID,
					Action: "SKIPPED", DimensionCount: len(output.Joins),
					Reason: "LLM_DAG_INVALID",
				})
				continue
			}
			dwdDatasetID, action, upsertErr := worker.upsertGeneratedDWDDraftTx(
				ctx, tx, claim, fact, input.Domain, inputHash, prepared,
			)
			if upsertErr != nil {
				if errors.Is(upsertErr, ErrConflict) || errors.Is(upsertErr, ErrAlreadyExists) {
					skipped++
					items = append(items, dwdModelingResultItem{
						Layer:         string(LayerDWD),
						FactDatasetID: fact.DatasetID, FactVersionID: fact.VersionID,
						Action: "SKIPPED", DimensionCount: len(output.Joins),
						Reason: "MANUAL_DRAFT_CHANGED",
					})
					continue
				}
				return upsertErr
			}
			switch action {
			case "CREATED":
				created++
			case "UPDATED":
				updated++
			}
			items = append(items, dwdModelingResultItem{
				Layer: string(LayerDWD), DatasetID: dwdDatasetID,
				FactDatasetID: fact.DatasetID, FactVersionID: fact.VersionID,
				DWDDatasetID: dwdDatasetID, Action: action,
				DimensionCount: len(output.Joins),
			})
		}
		status, errorCode, errorMessage := "SUCCEEDED", "", ""
		if len(items) == 0 {
			status = "SKIPPED"
			errorCode = "NO_MODELABLE_TABLE"
			errorMessage = "LLM 判断同领域内暂时没有可建模的事实、维度或主数据表"
		} else if skipped > 0 {
			status = "PARTIAL"
			errorCode = "SOME_LAYER_DESIGNS_SKIPPED"
			errorMessage = "部分 DIM/DWD 方案未通过 DSL 校验或目标草稿已被人工修改"
		}
		if err := finishDWDJobTx(
			ctx, tx, claim, workerID, completion.AIRequestID,
			input.Domain, triggerRole, status,
			created, updated, skipped,
			completion.CheckpointCount, completion.ReusedCheckpointCount,
			items, errorCode, errorMessage,
		); err != nil {
			return err
		}
		return coalesceDWDModelingJobsTx(
			ctx, tx, claim, completion, input.Domain,
		)
	})
}

func coalesceDWDModelingJobsTx(
	ctx context.Context,
	tx pgx.Tx,
	claim dwdModelingClaim,
	completion dwdPlanningCompletion,
	domain string,
) error {
	for _, classification := range completion.Plan.Classifications {
		result, err := json.Marshal(map[string]any{
			"domain": domain, "triggerRole": classification.Role,
			"coalescedIntoJobId": claim.ID,
		})
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE platform.dwd_modeling_jobs SET
			status='SKIPPED',domain_key=$1,trigger_role=$2,
			result_json=$3,error_code='DOMAIN_PLAN_COALESCED',
			error_message='同领域 ODS 已由一次 LLM 方案统一分析',
			ai_request_id=NULLIF($4,'')::uuid,
			lease_owner='',lease_token=NULL,lease_expires_at=NULL,
			completed_at=now(),updated_at=now()
			WHERE id<>$5::uuid
			  AND trigger_dataset_version_id=$6::uuid
			  AND status='PENDING'`,
			domain, classification.Role, result, completion.AIRequestID,
			claim.ID, classification.DatasetVersionID,
		); err != nil {
			return err
		}
	}
	return nil
}

// buildLLMClassifiedDIMDocument materializes the entity branch of the LLM
// classification as a reviewable DIM draft. The model decides that the ODS is
// DIMENSION/MASTER; deterministic policy then preserves every governed field,
// applies mandatory hygiene, and keeps the entity grain. No SQL or physical
// relation name crosses this design boundary.
func buildLLMClassifiedDIMDocument(
	domain string,
	source dwdODSAsset,
) (Document, string, error) {
	if source.Document.Dataset.Layer != LayerODS || len(source.Document.Fields) == 0 {
		return Document{}, "", errDWDModelingInvalid
	}
	keys := make([]string, 0, len(source.Document.OutputGrain.KeyFields))
	fieldCodes := make(map[string]bool, len(source.Document.Fields))
	for _, field := range source.Document.Fields {
		fieldCodes[field.Code] = true
	}
	for _, key := range source.Document.OutputGrain.KeyFields {
		if fieldCodes[key] && !containsString(keys, key) {
			keys = append(keys, key)
		}
	}
	if len(keys) == 0 {
		for _, field := range source.Document.Fields {
			code := strings.ToLower(strings.TrimSpace(field.Code))
			if strings.EqualFold(field.Role, "IDENTIFIER") ||
				strings.HasSuffix(code, "_id") || strings.HasSuffix(code, "_key") {
				keys = append(keys, field.Code)
			}
		}
	}
	if len(keys) == 0 {
		return Document{}, "", fmt.Errorf(
			"%w: DIM source has no governed entity key", errDWDModelingInvalid,
		)
	}

	projection := make([]string, 0, len(source.Document.Fields))
	fields := make([]Field, 0, len(source.Document.Fields))
	for index, sourceField := range source.Document.Fields {
		projection = append(projection, sourceField.Code)
		cleaning := mandatoryDIMCleaning(sourceField)
		expression, canonicalType, nullable, err := applyLLMDWDCleaning(
			"node_entity", sourceField, cleaning,
		)
		if err != nil {
			return Document{}, "", err
		}
		visible := true
		fields = append(fields, Field{
			ID:            fmt.Sprintf("field_%d", index+1),
			Code:          sourceField.Code,
			Name:          sourceField.Name,
			Description:   sourceField.Description,
			Role:          sourceField.Role,
			Expression:    expression,
			CanonicalType: canonicalType,
			SemanticType:  sourceField.SemanticType,
			Aggregation:   "",
			Format:        sourceField.Format,
			Unit:          sourceField.Unit,
			Nullable:      nullable,
			Visible:       &visible,
		})
	}
	sort.Strings(projection)
	timeField := source.Document.OutputGrain.TimeField
	if timeField != "" && !fieldCodes[timeField] {
		timeField = ""
	}
	grainDescription := strings.TrimSpace(source.Document.OutputGrain.Description)
	if grainDescription == "" {
		grainDescription = "每行代表一个" + strings.TrimSpace(source.Name) + "实体"
	}
	document := Document{
		DSLVersion: DSLVersion,
		Dataset: Descriptor{
			Code:                    "dim_auto_" + strings.ReplaceAll(source.DatasetID, "-", ""),
			Name:                    strings.TrimSpace(source.Name) + " DIM",
			Description:             "从 ODS 抽离并清洗的" + strings.TrimSpace(source.Name) + "实体说明信息",
			Type:                    "SINGLE_SOURCE",
			Layer:                   LayerDIM,
			SemanticContractVersion: "1.0",
		},
		Nodes: []Node{{
			ID: "node_entity", Type: "DATASET",
			DatasetVersionID: source.VersionID,
			Alias:            "t1", Projection: projection,
			SourceFilters: []SourceFilter{},
		}},
		Joins: []Join{}, PreAggregations: []PreAggregation{},
		Fields: fields, Filters: []Filter{}, GroupBy: []string{},
		Having: []Filter{}, Sorts: []Sort{}, Parameters: []Parameter{},
		OutputGrain: OutputGrain{
			Description: grainDescription,
			KeyFields:   keys,
			TimeField:   timeField,
			DefaultTimeGrain: func() string {
				if timeField == "" {
					return ""
				}
				if source.Document.OutputGrain.DefaultTimeGrain != "" {
					return source.Document.OutputGrain.DefaultTimeGrain
				}
				return "DAY"
			}(),
		},
		ExecutionPolicy: ExecutionPolicy{
			Mode: "MATERIALIZED_PREFERRED", TimeoutMS: 30_000,
			PreviewLimit: 200, ResultLimit: 100_000, CacheTTLSeconds: 300,
			Materialization: MaterializationPolicy{
				Enabled: true, RefreshMode: "ON_DEMAND",
			},
		},
	}
	raw, err := json.Marshal(struct {
		Domain       string `json:"domain"`
		Source       string `json:"sourceVersionId"`
		SourceSchema string `json:"sourceSchemaHash"`
		Layer        Layer  `json:"layer"`
	}{
		Domain: domain, Source: source.VersionID,
		SourceSchema: source.SchemaHash, Layer: LayerDIM,
	})
	if err != nil {
		return Document{}, "", err
	}
	sum := sha256.Sum256(raw)
	return document, hex.EncodeToString(sum[:]), nil
}

func mandatoryDIMCleaning(field Field) []string {
	canonical := strings.ToUpper(strings.TrimSpace(field.CanonicalType))
	role := strings.ToUpper(strings.TrimSpace(field.Role))
	dimensionRelated := role == "IDENTIFIER" || role == "DIMENSION" ||
		role == "ATTRIBUTE" || role == "TIME"
	nullFillEligible := role == "IDENTIFIER" || role == "DIMENSION" ||
		role == "ATTRIBUTE"
	operations := make([]string, 0, 3)
	if canonical == "STRING" && dimensionRelated {
		operations = append(operations, "TRIM")
	}
	if field.Nullable && nullFillEligible {
		switch canonical {
		case "STRING":
			operations = append(operations, "COALESCE_UNKNOWN")
		case "INTEGER", "DECIMAL":
			operations = append(operations, "COALESCE_NEGATIVE_ONE")
		}
	}
	switch canonical {
	case "DATE":
		operations = append(operations, "CAST_DATE")
	case "DATETIME":
		operations = append(operations, "CAST_DATETIME")
	case "STRING":
		if role == "TIME" {
			if strings.EqualFold(field.SemanticType, "DATE") ||
				strings.Contains(strings.ToLower(field.Code), "date") {
				operations = append(operations, "CAST_DATE")
			} else {
				operations = append(operations, "CAST_DATETIME")
			}
		}
	}
	return operations
}

func buildLLMDesignedDWDDocument(
	domain string,
	fact dwdODSAsset,
	assetsByVersion map[string]dwdODSAsset,
	output dwdLLMOutput,
) (Document, string, error) {
	if fact.VersionID != output.FactDatasetVersionID ||
		fact.Document.Dataset.Layer != LayerODS || len(output.Fields) == 0 {
		return Document{}, "", errDWDModelingInvalid
	}
	projectionsByVersion := map[string]map[string]bool{}
	for versionID := range assetsByVersion {
		projectionsByVersion[versionID] = map[string]bool{}
	}
	for _, field := range output.Fields {
		if projectionsByVersion[field.SourceDatasetVersionID] == nil {
			return Document{}, "", errDWDModelingInvalid
		}
		projectionsByVersion[field.SourceDatasetVersionID][field.SourceFieldCode] = true
		for _, processing := range field.Processing {
			if processing.SecondarySourceDatasetVersionID != "" &&
				processing.SecondarySourceFieldCode != "" {
				if projectionsByVersion[processing.SecondarySourceDatasetVersionID] == nil {
					return Document{}, "", errDWDModelingInvalid
				}
				projectionsByVersion[processing.SecondarySourceDatasetVersionID][processing.SecondarySourceFieldCode] = true
			}
		}
	}
	for _, join := range output.Joins {
		for _, condition := range join.Conditions {
			projectionsByVersion[fact.VersionID][condition.FactFieldCode] = true
			projectionsByVersion[join.DimensionDatasetVersionID][condition.DimensionFieldCode] = true
		}
	}
	nodes := []Node{{
		ID: "node_fact", Type: "DATASET", DatasetVersionID: fact.VersionID,
		Alias: "t1", Projection: sortedDWDProjection(projectionsByVersion[fact.VersionID]),
		SourceFilters: []SourceFilter{},
	}}
	nodeByVersion := map[string]string{fact.VersionID: "node_fact"}
	joins := make([]Join, 0, len(output.Joins))
	for index, join := range output.Joins {
		dimension, exists := assetsByVersion[join.DimensionDatasetVersionID]
		if !exists {
			return Document{}, "", errDWDModelingInvalid
		}
		nodeID := fmt.Sprintf("node_dim_%d", index+1)
		nodeByVersion[dimension.VersionID] = nodeID
		nodes = append(nodes, Node{
			ID: nodeID, Type: "DATASET", DatasetVersionID: dimension.VersionID,
			Alias:         fmt.Sprintf("t%d", index+2),
			Projection:    sortedDWDProjection(projectionsByVersion[dimension.VersionID]),
			SourceFilters: []SourceFilter{},
		})
		conditions := make([]JoinCondition, 0, len(join.Conditions))
		for _, condition := range join.Conditions {
			conditions = append(conditions, JoinCondition{
				LeftExpression: Expression{
					Type: "FIELD_REF", NodeID: "node_fact",
					Field: condition.FactFieldCode,
				},
				Operator: "EQUALS",
				RightExpression: Expression{
					Type: "FIELD_REF", NodeID: nodeID,
					Field: condition.DimensionFieldCode,
				},
			})
		}
		joins = append(joins, Join{
			ID:         fmt.Sprintf("join_%d", index+1),
			LeftNodeID: "node_fact", RightNodeID: nodeID,
			JoinType: "LEFT", Cardinality: "MANY_TO_ONE",
			RelationshipType: "DIRECT", FanoutPolicy: "SAFE",
			// LLM 只允许从已发布的字段合同中选择关联键；本地校验通过后
			// 生成的关联已经是可执行合同，不应再要求用户逐个点击确认。
			ManualConfirmed: true,
			Conditions:      conditions,
		})
	}
	fields := make([]Field, 0, len(output.Fields))
	for index, planned := range output.Fields {
		sourceAsset, exists := assetsByVersion[planned.SourceDatasetVersionID]
		nodeID := nodeByVersion[planned.SourceDatasetVersionID]
		source, fieldExists := dwdDocumentFieldByCode(
			sourceAsset.Document, planned.SourceFieldCode,
		)
		if !exists || !fieldExists || nodeID == "" {
			return Document{}, "", errDWDModelingInvalid
		}
		expression, canonicalType, nullable, err := applyLLMDWDCleaning(
			nodeID, source, planned.Cleaning,
		)
		if err != nil {
			return Document{}, "", err
		}
		expression, canonicalType, nullable, err = applyLLMDWDProcessing(
			expression, canonicalType, nullable, planned.Processing,
			assetsByVersion, nodeByVersion,
		)
		if err != nil {
			return Document{}, "", err
		}
		visible := true
		fields = append(fields, Field{
			ID:   fmt.Sprintf("field_%d", index+1),
			Code: planned.OutputCode, Name: planned.OutputName,
			Description: planned.OutputDescription, Role: planned.Role,
			Expression: expression, CanonicalType: canonicalType,
			SemanticType: source.SemanticType, Nullable: nullable,
			Visible: &visible,
		})
	}
	atomicMeasures := make([]AtomicMeasureContract, 0)
	for _, field := range fields {
		if field.Role != "MEASURE" {
			continue
		}
		atomicMeasures = append(atomicMeasures, AtomicMeasureContract{
			Field:      field.Code,
			Additivity: "ADDITIVE",
			Unit:       field.Unit,
			NullPolicy: "PRESERVE",
		})
	}
	document := Document{
		DSLVersion: DSLVersion,
		Dataset: Descriptor{
			Code:                    "dwd_auto_" + strings.ReplaceAll(fact.DatasetID, "-", ""),
			Name:                    strings.TrimSpace(output.Name),
			Description:             strings.TrimSpace(output.Description),
			Type:                    "SINGLE_SOURCE",
			Layer:                   LayerDWD,
			SemanticContractVersion: "1.0",
		},
		Nodes: nodes, Joins: joins, PreAggregations: []PreAggregation{},
		FactContract: &FactContract{
			BusinessAction: strings.TrimSpace(output.Name),
			GrainKeyFields: append([]string(nil), output.GrainKeyOutputCodes...),
			EventTimeField: output.TimeOutputCode,
			AtomicMeasures: atomicMeasures,
		},
		Fields: fields, Filters: []Filter{}, GroupBy: []string{},
		Having: []Filter{}, Sorts: []Sort{}, Parameters: []Parameter{},
		OutputGrain: OutputGrain{
			Description: "每行代表" + strings.TrimSpace(output.Name) + "的一条事实明细",
			KeyFields:   append([]string(nil), output.GrainKeyOutputCodes...),
			TimeField:   output.TimeOutputCode,
			DefaultTimeGrain: func() string {
				if output.TimeOutputCode != "" {
					return "DAY"
				}
				return ""
			}(),
		},
		ExecutionPolicy: ExecutionPolicy{
			Mode: "MATERIALIZED_PREFERRED", TimeoutMS: 30_000,
			PreviewLimit: 200, ResultLimit: 100_000, CacheTTLSeconds: 300,
			Materialization: MaterializationPolicy{
				Enabled: true, RefreshMode: "ON_DEMAND",
			},
		},
	}
	raw, err := json.Marshal(struct {
		Domain string       `json:"domain"`
		Fact   string       `json:"factVersionId"`
		Output dwdLLMOutput `json:"output"`
	}{Domain: domain, Fact: fact.VersionID, Output: output})
	if err != nil {
		return Document{}, "", err
	}
	sum := sha256.Sum256(raw)
	return document, hex.EncodeToString(sum[:]), nil
}

func sortedDWDProjection(fields map[string]bool) []string {
	projection := make([]string, 0, len(fields))
	for field := range fields {
		projection = append(projection, field)
	}
	sort.Strings(projection)
	return projection
}

func dwdDocumentFieldByCode(document Document, code string) (Field, bool) {
	for _, field := range document.Fields {
		if field.Code == code {
			return field, true
		}
	}
	return Field{}, false
}

func applyLLMDWDCleaning(
	nodeID string,
	source Field,
	operations []string,
) (Expression, string, bool, error) {
	expression := Expression{Type: "FIELD_REF", NodeID: nodeID, Field: source.Code}
	canonicalType := source.CanonicalType
	nullable := source.Nullable
	for _, operation := range operations {
		switch operation {
		case "TRIM":
			argument := expression
			expression = Expression{Type: "TRIM", Argument: &argument}
		case "COALESCE_UNKNOWN":
			expression = Expression{
				Type: "COALESCE",
				Arguments: []Expression{
					expression, {Type: "LITERAL", Value: "UNKNOWN"},
				},
			}
			nullable = false
		case "COALESCE_NEGATIVE_ONE":
			expression = Expression{
				Type: "COALESCE",
				Arguments: []Expression{
					expression, {Type: "LITERAL", Value: -1},
				},
			}
			nullable = false
		case "CAST_DATE":
			argument := expression
			expression = Expression{Type: "CAST", TargetType: "DATE", Argument: &argument}
			canonicalType = "DATE"
		case "CAST_DATETIME":
			argument := expression
			expression = Expression{Type: "CAST", TargetType: "DATETIME", Argument: &argument}
			canonicalType = "DATETIME"
		default:
			return Expression{}, "", false, errDWDModelingInvalid
		}
	}
	return expression, canonicalType, nullable, nil
}

func applyLLMDWDProcessing(
	expression Expression,
	canonicalType string,
	nullable bool,
	steps []dwdLLMProcessingStep,
	assetsByVersion map[string]dwdODSAsset,
	nodeByVersion map[string]string,
) (Expression, string, bool, error) {
	for _, rawStep := range steps {
		step, err := normalizeDWDProcessingStep(rawStep)
		if err != nil {
			return Expression{}, "", false, err
		}
		operation := strings.ToUpper(strings.TrimSpace(step.Operation))
		switch operation {
		case "DATE_FORMAT", "DATE_TRUNC":
			argument := expression
			expression = Expression{
				Type: operation, Unit: strings.ToUpper(step.Unit), Argument: &argument,
			}
			if operation == "DATE_FORMAT" {
				canonicalType = "STRING"
			}
		case "CAST":
			argument := expression
			canonicalType = strings.ToUpper(strings.TrimSpace(step.TargetType))
			expression = Expression{
				Type: "CAST", TargetType: canonicalType, Argument: &argument,
			}
		case "TRIM", "UPPER", "LOWER":
			argument := dwdStringExpression(expression, canonicalType)
			expression = Expression{Type: operation, Argument: &argument}
			canonicalType = "STRING"
		case "REPLACE":
			expression = Expression{
				Type: "REPLACE",
				Arguments: []Expression{
					dwdStringExpression(expression, canonicalType),
					{Type: "LITERAL", Value: step.SearchValue},
					{Type: "LITERAL", Value: step.ReplacementValue},
				},
			}
			canonicalType = "STRING"
		case "SUBSTRING":
			expression = Expression{
				Type: "SUBSTRING",
				Arguments: []Expression{
					dwdStringExpression(expression, canonicalType),
					{Type: "LITERAL", Value: step.Start},
					{Type: "LITERAL", Value: step.Length},
				},
			}
			canonicalType = "STRING"
		case "CONCAT", "ADD", "SUBTRACT", "MULTIPLY", "DIVIDE":
			secondary, err := dwdSecondaryFieldExpression(
				step, assetsByVersion, nodeByVersion,
			)
			if err != nil {
				return Expression{}, "", false, err
			}
			if operation == "CONCAT" {
				left := dwdNullSafeStringExpression(expression, canonicalType)
				rightType := secondary.canonicalType
				right := dwdNullSafeStringExpression(secondary.expression, rightType)
				arguments := []Expression{left, right}
				if step.Separator != "" {
					arguments = []Expression{
						left, {Type: "LITERAL", Value: step.Separator}, right,
					}
				}
				expression = Expression{Type: "CONCAT", Arguments: arguments}
				canonicalType = "STRING"
			} else {
				expression = Expression{
					Type: operation, Arguments: []Expression{expression, secondary.expression},
				}
				canonicalType = "DECIMAL"
			}
		case "COALESCE":
			fallback, err := dwdTypedLiteral(step.FallbackValue, canonicalType)
			if err != nil {
				return Expression{}, "", false, err
			}
			expression = Expression{
				Type: "COALESCE",
				Arguments: []Expression{
					expression, {Type: "LITERAL", Value: fallback},
				},
			}
			nullable = false
		case "ROUND":
			expression = Expression{
				Type: "ROUND",
				Arguments: []Expression{
					expression, {Type: "LITERAL", Value: step.Precision},
				},
			}
		case "ABS", "FLOOR", "CEIL":
			argument := expression
			expression = Expression{Type: operation, Argument: &argument}
		case "CASE":
			condition, err := dwdCaseCondition(expression, canonicalType, step)
			if err != nil {
				return Expression{}, "", false, err
			}
			thenValue := Expression{Type: "LITERAL", Value: step.ThenValue}
			elseValue := Expression{Type: "LITERAL", Value: step.ElseValue}
			expression = Expression{
				Type:  "CASE",
				Whens: []CaseBranch{{When: condition, Then: thenValue}},
				Else:  &elseValue,
			}
			canonicalType = "STRING"
			nullable = false
		default:
			return Expression{}, "", false, errDWDModelingInvalid
		}
	}
	return expression, canonicalType, nullable, nil
}

func normalizeDWDProcessingStep(
	step dwdLLMProcessingStep,
) (dwdLLMProcessingStep, error) {
	if step.Arguments == nil {
		return step, nil
	}
	operation := strings.ToUpper(strings.TrimSpace(step.Operation))
	expect := func(count int) error {
		if len(step.Arguments) != count {
			return errDWDModelingInvalid
		}
		return nil
	}
	switch operation {
	case "DATE_FORMAT", "DATE_TRUNC":
		if err := expect(1); err != nil {
			return step, err
		}
		step.Unit = strings.ToUpper(strings.TrimSpace(step.Arguments[0]))
	case "CAST":
		if err := expect(1); err != nil {
			return step, err
		}
		step.TargetType = strings.ToUpper(strings.TrimSpace(step.Arguments[0]))
	case "REPLACE":
		if err := expect(2); err != nil {
			return step, err
		}
		step.SearchValue, step.ReplacementValue = step.Arguments[0], step.Arguments[1]
	case "SUBSTRING":
		if err := expect(2); err != nil {
			return step, err
		}
		start, startErr := strconv.Atoi(strings.TrimSpace(step.Arguments[0]))
		length, lengthErr := strconv.Atoi(strings.TrimSpace(step.Arguments[1]))
		if startErr != nil || lengthErr != nil {
			return step, errDWDModelingInvalid
		}
		step.Start, step.Length = start, length
	case "CONCAT":
		if err := expect(1); err != nil {
			return step, err
		}
		step.Separator = step.Arguments[0]
	case "COALESCE":
		if err := expect(1); err != nil {
			return step, err
		}
		step.FallbackValue = step.Arguments[0]
	case "ROUND":
		if err := expect(1); err != nil {
			return step, err
		}
		precision, parseErr := strconv.Atoi(strings.TrimSpace(step.Arguments[0]))
		if parseErr != nil {
			return step, errDWDModelingInvalid
		}
		step.Precision = precision
	case "CASE":
		if err := expect(4); err != nil {
			return step, err
		}
		step.ConditionOperator = strings.ToUpper(strings.TrimSpace(step.Arguments[0]))
		step.MatchValue = step.Arguments[1]
		step.ThenValue = step.Arguments[2]
		step.ElseValue = step.Arguments[3]
	case "TRIM", "UPPER", "LOWER", "ADD", "SUBTRACT", "MULTIPLY", "DIVIDE",
		"ABS", "FLOOR", "CEIL":
		if err := expect(0); err != nil {
			return step, err
		}
	default:
		return step, errDWDModelingInvalid
	}
	return step, nil
}

type dwdSecondaryExpression struct {
	expression    Expression
	canonicalType string
}

func dwdSecondaryFieldExpression(
	step dwdLLMProcessingStep,
	assetsByVersion map[string]dwdODSAsset,
	nodeByVersion map[string]string,
) (dwdSecondaryExpression, error) {
	asset, exists := assetsByVersion[step.SecondarySourceDatasetVersionID]
	nodeID := nodeByVersion[step.SecondarySourceDatasetVersionID]
	field, fieldExists := dwdDocumentFieldByCode(
		asset.Document, step.SecondarySourceFieldCode,
	)
	if !exists || !fieldExists || nodeID == "" {
		return dwdSecondaryExpression{}, errDWDModelingInvalid
	}
	return dwdSecondaryExpression{
		expression: Expression{
			Type: "FIELD_REF", NodeID: nodeID, Field: field.Code,
		},
		canonicalType: field.CanonicalType,
	}, nil
}

func dwdStringExpression(expression Expression, canonicalType string) Expression {
	if strings.EqualFold(canonicalType, "STRING") {
		return expression
	}
	return Expression{
		Type: "CAST", TargetType: "STRING", Argument: &expression,
	}
}

func dwdNullSafeStringExpression(expression Expression, canonicalType string) Expression {
	return Expression{
		Type: "COALESCE",
		Arguments: []Expression{
			dwdStringExpression(expression, canonicalType),
			{Type: "LITERAL", Value: ""},
		},
	}
}

func dwdTypedLiteral(raw, canonicalType string) (any, error) {
	switch strings.ToUpper(strings.TrimSpace(canonicalType)) {
	case "STRING", "DATE", "DATETIME":
		return raw, nil
	case "INTEGER":
		value, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
		if err != nil {
			return nil, errDWDModelingInvalid
		}
		return value, nil
	case "DECIMAL":
		value, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
		if err != nil {
			return nil, errDWDModelingInvalid
		}
		return value, nil
	case "BOOLEAN":
		value, err := strconv.ParseBool(strings.TrimSpace(raw))
		if err != nil {
			return nil, errDWDModelingInvalid
		}
		return value, nil
	default:
		return nil, errDWDModelingInvalid
	}
}

func dwdCaseCondition(
	expression Expression,
	canonicalType string,
	step dwdLLMProcessingStep,
) (Expression, error) {
	operator := strings.ToUpper(strings.TrimSpace(step.ConditionOperator))
	if operator == "IS_NULL" || operator == "IS_NOT_NULL" {
		return Expression{Type: operator, Argument: &expression}, nil
	}
	match, err := dwdTypedLiteral(step.MatchValue, canonicalType)
	if operator == "CONTAINS" || operator == "NOT_CONTAINS" {
		match, err = step.MatchValue, nil
		expression = dwdStringExpression(expression, canonicalType)
	}
	if err != nil {
		return Expression{}, err
	}
	right := Expression{Type: "LITERAL", Value: match}
	return Expression{
		Type: operator, Left: &expression, Right: &right,
	}, nil
}

func validateDWDClaimTx(
	ctx context.Context,
	tx pgx.Tx,
	claim dwdModelingClaim,
	workerID string,
) error {
	var valid bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(
		SELECT 1
		FROM platform.dwd_modeling_jobs AS job
		JOIN platform.datasets AS dataset
		  ON dataset.id=job.trigger_dataset_id
		 AND dataset.tenant_id=job.tenant_id
		 AND dataset.deleted_at IS NULL
		 AND dataset.current_published_version_id=job.trigger_dataset_version_id
		JOIN platform.dataset_versions AS version
		  ON version.id=job.trigger_dataset_version_id
		 AND version.dataset_id=job.trigger_dataset_id
		 AND version.tenant_id=job.tenant_id
		 AND version.layer='ODS'
		 AND version.status='PUBLISHED'
		JOIN platform.users AS actor
		  ON actor.id=job.requested_by
		 AND actor.tenant_id=job.tenant_id
		 AND actor.status='ACTIVE'
		 AND actor.deleted_at IS NULL
		WHERE job.id=$1::uuid
		  AND job.status='RUNNING'
		  AND job.lease_owner=$2
		  AND job.lease_token=$3::uuid
		  AND job.attempt=$4
		  AND job.lease_expires_at>now()
	)`, claim.ID, workerID, claim.LeaseToken, claim.Attempt).Scan(&valid)
	if err != nil {
		return err
	}
	if !valid {
		return errDWDModelingSubjectChange
	}
	return nil
}

func loadPublishedODSAssetsTx(ctx context.Context, tx pgx.Tx) ([]dwdODSAsset, error) {
	rows, err := tx.Query(ctx, `SELECT dataset.id::text,version.id::text,
			version.schema_hash,dataset.code::text,dataset.name,dataset.description,
			version.dsl_json,
			COALESCE(metadata_table.tags,'{}'::text[])
			  || COALESCE(binding_tags.tags,'{}'::text[]) AS tags
		FROM platform.datasets AS dataset
		JOIN platform.dataset_versions AS version
		  ON version.id=dataset.current_published_version_id
		 AND version.dataset_id=dataset.id
		 AND version.tenant_id=dataset.tenant_id
		 AND version.layer='ODS'
		 AND version.status='PUBLISHED'
		LEFT JOIN platform.metadata_tables AS metadata_table
		  ON metadata_table.id=dataset.origin_table_id
		 AND metadata_table.tenant_id=dataset.tenant_id
		LEFT JOIN LATERAL (
		  SELECT array_agg(DISTINCT CASE
		    WHEN tag.category='BUSINESS_DOMAIN' THEN '领域:'||tag.name
		    WHEN tag.category='TABLE_FUNCTION' THEN '作用:'||tag.name
		    ELSE tag.name
		  END ORDER BY CASE
		    WHEN tag.category='BUSINESS_DOMAIN' THEN '领域:'||tag.name
		    WHEN tag.category='TABLE_FUNCTION' THEN '作用:'||tag.name
		    ELSE tag.name
		  END) AS tags
		  FROM platform.asset_tag_bindings AS binding
		  JOIN platform.semantic_tags AS tag
		    ON tag.id=binding.tag_id
		   AND tag.tenant_id=binding.tenant_id
		  WHERE binding.asset_type='DATASET_VERSION'
		    AND binding.dataset_id=dataset.id
		    AND binding.dataset_version_id=version.id
		    AND binding.status IN ('SUGGESTED','APPROVED')
		    AND tag.status IN ('DRAFT','ACTIVE')
		) AS binding_tags ON true
		WHERE dataset.deleted_at IS NULL
		ORDER BY dataset.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	assets := []dwdODSAsset{}
	for rows.Next() {
		var asset dwdODSAsset
		var raw json.RawMessage
		var tags []string
		if err := rows.Scan(
			&asset.DatasetID, &asset.VersionID, &asset.SchemaHash,
			&asset.Code, &asset.Name, &asset.Description, &raw, &tags,
		); err != nil {
			return nil, err
		}
		document, err := DecodeAndNormalize(raw)
		if err != nil {
			return nil, err
		}
		asset.Document = document
		// The immutable published DSL, not the mutable aggregate row, is the
		// authority for metadata sent to the DWD planner.
		asset.Code = document.Dataset.Code
		asset.Name = document.Dataset.Name
		asset.Description = document.Dataset.Description
		asset.Domains = extractDWDDomains(tags)
		asset.Tags = append([]string(nil), tags...)
		assets = append(assets, asset)
	}
	return assets, rows.Err()
}

func extractDWDDomains(tags []string) []string {
	seen := map[string]bool{}
	domains := []string{}
	for _, raw := range tags {
		value := strings.TrimSpace(strings.ReplaceAll(raw, "：", ":"))
		if !strings.HasPrefix(value, "领域:") {
			continue
		}
		name := strings.TrimSpace(strings.TrimPrefix(value, "领域:"))
		if name == "" {
			continue
		}
		key := "领域:" + name
		if !seen[key] {
			seen[key] = true
			domains = append(domains, key)
		}
	}
	sort.Strings(domains)
	return domains
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func dwdCanonicalTypesCompatible(left, right string) bool {
	left, right = strings.ToUpper(strings.TrimSpace(left)), strings.ToUpper(strings.TrimSpace(right))
	if left == right {
		return true
	}
	return (left == "INTEGER" || left == "DECIMAL") &&
		(right == "INTEGER" || right == "DECIMAL")
}
func mustMarshalDWDDocument(document Document) []byte {
	raw, _ := json.Marshal(document)
	return raw
}

func (worker *DWDModelingWorker) upsertGeneratedDIMDraftTx(
	ctx context.Context,
	tx pgx.Tx,
	claim dwdModelingClaim,
	source dwdODSAsset,
	domain, inputHash string,
	prepared Prepared,
) (datasetID, action string, err error) {
	var existingDatasetID, lastSchemaHash string
	err = tx.QueryRow(ctx, `SELECT output.dim_dataset_id::text,
			output.last_generated_schema_hash
		FROM platform.dim_modeling_outputs AS output
		WHERE output.source_dataset_id=$1::uuid
		FOR UPDATE`, source.DatasetID).
		Scan(&existingDatasetID, &lastSchemaHash)
	if errors.Is(err, pgx.ErrNoRows) {
		var codeExists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(
			SELECT 1 FROM platform.datasets
			WHERE code=$1 AND deleted_at IS NULL
		)`, prepared.Document.Dataset.Code).Scan(&codeExists); err != nil {
			return "", "", err
		}
		if codeExists {
			return "", "", ErrAlreadyExists
		}
		input := CreateInput{
			Code:        prepared.Document.Dataset.Code,
			Name:        prepared.Document.Dataset.Name,
			Description: prepared.Document.Dataset.Description,
			Type:        prepared.Document.Dataset.Type,
			Layer:       LayerDIM,
			DSL:         prepared.DSLJSON,
		}
		datasetID, err = createDatasetTx(
			ctx, tx, claim.TenantID, claim.ActorID, input, prepared, "",
		)
		if err != nil {
			return "", "", err
		}
		_, err = tx.Exec(ctx, `INSERT INTO platform.dim_modeling_outputs(
			tenant_id,source_dataset_id,dim_dataset_id,domain_key,last_job_id,
			last_input_hash,last_generated_schema_hash,last_action
		) VALUES($1,$2,$3,$4,$5,$6,$7,'CREATED')`,
			claim.TenantID, source.DatasetID, datasetID, domain, claim.ID,
			inputHash, prepared.DSLHash,
		)
		return datasetID, "CREATED", err
	}
	if err != nil {
		return "", "", err
	}

	var aggregateVersion, draftRecordVersion int64
	var draftVersionID, currentSchemaHash, layer string
	err = tx.QueryRow(ctx, `SELECT dataset.version,dataset.current_draft_version_id::text,
			draft.record_version,draft.schema_hash,draft.layer
		FROM platform.datasets AS dataset
		JOIN platform.dataset_versions AS draft
		  ON draft.id=dataset.current_draft_version_id
		 AND draft.dataset_id=dataset.id
		 AND draft.tenant_id=dataset.tenant_id
		 AND draft.status='DRAFT'
		WHERE dataset.id=$1::uuid AND dataset.deleted_at IS NULL
		FOR UPDATE OF dataset,draft`, existingDatasetID).
		Scan(
			&aggregateVersion, &draftVersionID, &draftRecordVersion,
			&currentSchemaHash, &layer,
		)
	if errors.Is(err, pgx.ErrNoRows) || layer != string(LayerDIM) {
		return "", "", ErrConflict
	}
	if err != nil {
		return "", "", err
	}
	if currentSchemaHash != lastSchemaHash {
		return "", "", ErrConflict
	}
	action = "UNCHANGED"
	if currentSchemaHash != prepared.DSLHash {
		newDraftRecordVersion := draftRecordVersion + 1
		if tag, err := tx.Exec(ctx, `UPDATE platform.dataset_versions SET
			layer='DIM',dsl_json=$1,schema_hash=$2,logical_plan_json=$3,plan_hash=$4,
			record_version=record_version+1,updated_by=$5
			WHERE id=$6::uuid AND status='DRAFT' AND record_version=$7`,
			prepared.DSLJSON, prepared.DSLHash, prepared.LogicalPlanJSON,
			prepared.PlanHash, claim.ActorID, draftVersionID, draftRecordVersion,
		); err != nil {
			return "", "", err
		} else if tag.RowsAffected() != 1 {
			return "", "", ErrConflict
		}
		if _, err := tx.Exec(ctx, `UPDATE platform.datasets SET
			name=$1,description=$2,dataset_type=$3,layer='DIM',
			version=version+1,updated_by=$4
			WHERE id=$5::uuid AND version=$6`,
			prepared.Document.Dataset.Name, prepared.Document.Dataset.Description,
			prepared.Document.Dataset.Type, claim.ActorID,
			existingDatasetID, aggregateVersion,
		); err != nil {
			return "", "", err
		}
		if err := replaceDerived(
			ctx, tx, claim.TenantID, existingDatasetID,
			draftVersionID, prepared.Document, true,
		); err != nil {
			return "", "", err
		}
		if err := insertDraftRevisionTx(
			ctx, tx, claim.TenantID, existingDatasetID, claim.ActorID,
			draftVersionID, aggregateVersion+1, newDraftRecordVersion,
			"SAVE", "", prepared,
		); err != nil {
			return "", "", err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO platform.audit_logs(
			tenant_id,actor_user_id,action,resource_type,resource_id,detail
		) VALUES($1,$2,'AUTO_DIM_UPDATE','DATASET',$3,
			jsonb_build_object(
			  'sourceDatasetId',$4::text,'domain',$5::text,
			  'dwdModelingJobId',$6::text,'dslHash',$7::text
			))`,
			claim.TenantID, claim.ActorID, existingDatasetID,
			source.DatasetID, domain, claim.ID, prepared.DSLHash,
		); err != nil {
			return "", "", err
		}
		action = "UPDATED"
	}
	if _, err := tx.Exec(ctx, `UPDATE platform.dim_modeling_outputs SET
		domain_key=$1,last_job_id=$2,last_input_hash=$3,
		last_generated_schema_hash=$4,last_action=$5
		WHERE source_dataset_id=$6::uuid`,
		domain, claim.ID, inputHash, prepared.DSLHash, action, source.DatasetID,
	); err != nil {
		return "", "", err
	}
	return existingDatasetID, action, nil
}

func (worker *DWDModelingWorker) upsertGeneratedDWDDraftTx(
	ctx context.Context,
	tx pgx.Tx,
	claim dwdModelingClaim,
	fact dwdODSAsset,
	domain, inputHash string,
	prepared Prepared,
) (datasetID, action string, err error) {
	var existingDatasetID, lastInputHash, lastSchemaHash string
	err = tx.QueryRow(ctx, `SELECT output.dwd_dataset_id::text,
			output.last_input_hash,output.last_generated_schema_hash
		FROM platform.dwd_modeling_outputs AS output
		WHERE output.fact_dataset_id=$1::uuid
		FOR UPDATE`, fact.DatasetID).
		Scan(&existingDatasetID, &lastInputHash, &lastSchemaHash)
	if errors.Is(err, pgx.ErrNoRows) {
		var codeExists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(
			SELECT 1 FROM platform.datasets
			WHERE code=$1 AND deleted_at IS NULL
		)`, prepared.Document.Dataset.Code).Scan(&codeExists); err != nil {
			return "", "", err
		}
		if codeExists {
			return "", "", ErrAlreadyExists
		}
		input := CreateInput{
			Code:        prepared.Document.Dataset.Code,
			Name:        prepared.Document.Dataset.Name,
			Description: prepared.Document.Dataset.Description,
			Type:        prepared.Document.Dataset.Type,
			Layer:       LayerDWD,
			DSL:         prepared.DSLJSON,
		}
		datasetID, err = createDatasetTx(
			ctx, tx, claim.TenantID, claim.ActorID, input, prepared, "",
		)
		if err != nil {
			return "", "", err
		}
		_, err = tx.Exec(ctx, `INSERT INTO platform.dwd_modeling_outputs(
			tenant_id,fact_dataset_id,dwd_dataset_id,domain_key,last_job_id,
			last_input_hash,last_generated_schema_hash,last_action
		) VALUES($1,$2,$3,$4,$5,$6,$7,'CREATED')`,
			claim.TenantID, fact.DatasetID, datasetID, domain, claim.ID,
			inputHash, prepared.DSLHash,
		)
		return datasetID, "CREATED", err
	}
	if err != nil {
		return "", "", err
	}

	var aggregateVersion, draftRecordVersion int64
	var draftVersionID, currentSchemaHash, layer string
	err = tx.QueryRow(ctx, `SELECT dataset.version,dataset.current_draft_version_id::text,
			draft.record_version,draft.schema_hash,draft.layer
		FROM platform.datasets AS dataset
		JOIN platform.dataset_versions AS draft
		  ON draft.id=dataset.current_draft_version_id
		 AND draft.dataset_id=dataset.id
		 AND draft.tenant_id=dataset.tenant_id
		 AND draft.status='DRAFT'
		WHERE dataset.id=$1::uuid AND dataset.deleted_at IS NULL
		FOR UPDATE OF dataset,draft`, existingDatasetID).
		Scan(
			&aggregateVersion, &draftVersionID, &draftRecordVersion,
			&currentSchemaHash, &layer,
		)
	if errors.Is(err, pgx.ErrNoRows) || layer != string(LayerDWD) {
		return "", "", ErrConflict
	}
	if err != nil {
		return "", "", err
	}
	if currentSchemaHash != lastSchemaHash {
		return "", "", ErrConflict
	}
	action = "UNCHANGED"
	if currentSchemaHash != prepared.DSLHash {
		newDraftRecordVersion := draftRecordVersion + 1
		if tag, err := tx.Exec(ctx, `UPDATE platform.dataset_versions SET
			layer='DWD',dsl_json=$1,schema_hash=$2,logical_plan_json=$3,plan_hash=$4,
			record_version=record_version+1,updated_by=$5
			WHERE id=$6::uuid AND status='DRAFT' AND record_version=$7`,
			prepared.DSLJSON, prepared.DSLHash, prepared.LogicalPlanJSON,
			prepared.PlanHash, claim.ActorID, draftVersionID, draftRecordVersion,
		); err != nil {
			return "", "", err
		} else if tag.RowsAffected() != 1 {
			return "", "", ErrConflict
		}
		if _, err := tx.Exec(ctx, `UPDATE platform.datasets SET
			name=$1,description=$2,dataset_type=$3,layer='DWD',
			version=version+1,updated_by=$4
			WHERE id=$5::uuid AND version=$6`,
			prepared.Document.Dataset.Name, prepared.Document.Dataset.Description,
			prepared.Document.Dataset.Type, claim.ActorID,
			existingDatasetID, aggregateVersion,
		); err != nil {
			return "", "", err
		}
		if err := replaceDerived(
			ctx, tx, claim.TenantID, existingDatasetID,
			draftVersionID, prepared.Document, true,
		); err != nil {
			return "", "", err
		}
		if err := insertDraftRevisionTx(
			ctx, tx, claim.TenantID, existingDatasetID, claim.ActorID,
			draftVersionID, aggregateVersion+1, newDraftRecordVersion,
			"SAVE", "", prepared,
		); err != nil {
			return "", "", err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO platform.audit_logs(
			tenant_id,actor_user_id,action,resource_type,resource_id,detail
		) VALUES($1,$2,'AUTO_DWD_UPDATE','DATASET',$3,
			jsonb_build_object(
			  'factDatasetId',$4::text,'domain',$5::text,
			  'dwdModelingJobId',$6::text,'dslHash',$7::text
			))`,
			claim.TenantID, claim.ActorID, existingDatasetID,
			fact.DatasetID, domain, claim.ID, prepared.DSLHash,
		); err != nil {
			return "", "", err
		}
		action = "UPDATED"
	}
	if _, err := tx.Exec(ctx, `UPDATE platform.dwd_modeling_outputs SET
		domain_key=$1,last_job_id=$2,last_input_hash=$3,
		last_generated_schema_hash=$4,last_action=$5
		WHERE fact_dataset_id=$6::uuid`,
		domain, claim.ID, inputHash, prepared.DSLHash, action, fact.DatasetID,
	); err != nil {
		return "", "", err
	}
	_ = lastInputHash
	return existingDatasetID, action, nil
}

func finishDWDJobTx(
	ctx context.Context,
	tx pgx.Tx,
	claim dwdModelingClaim,
	workerID, aiRequestID, domain, role, status string,
	created, updated, skipped, checkpointCount, reusedCheckpointCount int,
	items []dwdModelingResultItem,
	errorCode, errorMessage string,
) error {
	result, err := json.Marshal(map[string]any{
		"domain": domain, "triggerRole": role, "outputs": items,
		"resume": map[string]any{
			"checkpointCount":       checkpointCount,
			"reusedCheckpointCount": reusedCheckpointCount,
		},
		"developmentFlow": map[string]any{
			"designer": "LLM",
			"executor": "DAG_DEVELOPMENT_ENGINE",
			"stages": []map[string]any{
				{
					"order": 1, "parallel": true,
					"layers": []string{"DIM", "DWD"}, "inputLayers": []string{"ODS"},
				},
				{
					"order": 2, "parallel": false,
					"layers": []string{"DWS"}, "inputLayers": []string{"DWD"},
				},
				{
					"order": 3, "parallel": false,
					"layers": []string{"ADS"}, "inputLayers": []string{"DWS"},
					"status":   "AUTO_GENERATION_DISABLED",
					"requires": []string{"PUBLISHED_CONSUMER_CONTRACT"},
				},
			},
		},
	})
	if err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `UPDATE platform.dwd_modeling_jobs SET
		status=$1,domain_key=$2,trigger_role=$3,
		generated_count=$4,updated_count=$5,skipped_count=$6,
		result_json=$7,error_code=$8,error_message=$9,
		ai_request_id=NULLIF($10,'')::uuid,
		lease_owner='',lease_token=NULL,lease_expires_at=NULL,
		completed_at=now(),updated_at=now()
		WHERE id=$11::uuid AND status='RUNNING'
		  AND lease_owner=$12 AND lease_token=$13::uuid AND attempt=$14`,
		status, domain, role, created, updated, skipped, result,
		errorCode, errorMessage, aiRequestID,
		claim.ID, workerID, claim.LeaseToken, claim.Attempt,
	)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return errDWDModelingLeaseLost
	}
	return nil
}

func (worker *DWDModelingWorker) retryOrSkip(
	ctx context.Context,
	claim dwdModelingClaim,
	workerID, errorCode, errorMessage string,
) error {
	return database.WithTenantTx(ctx, worker.store.pool, claim.TenantID, func(tx pgx.Tx) error {
		if err := validateDWDClaimTx(ctx, tx, claim, workerID); err != nil {
			return err
		}
		var checkpointVersion int
		if err := tx.QueryRow(ctx, `SELECT checkpoint_version
			FROM platform.dwd_modeling_jobs
			WHERE id=$1::uuid
			FOR UPDATE`, claim.ID).Scan(&checkpointVersion); err != nil {
			return err
		}
		progressed := checkpointVersion > claim.CheckpointVersion
		if !progressed && claim.Attempt >= claim.MaxAttempts {
			return finishDWDJobTx(
				ctx, tx, claim, workerID, "", "", "", "SKIPPED",
				0, 0, 0, 0, 0, []dwdModelingResultItem{},
				errorCode, errorMessage,
			)
		}
		nextAttempt := claim.Attempt
		if progressed {
			nextAttempt = 0
		}
		tag, err := tx.Exec(ctx, `UPDATE platform.dwd_modeling_jobs SET
			status='PENDING',next_attempt_at=now()+interval '1 minute',
			attempt=$1,error_code=$2,error_message=$3,
			lease_owner='',lease_token=NULL,lease_expires_at=NULL,updated_at=now()
			WHERE id=$4::uuid AND status='RUNNING'
			  AND lease_owner=$5 AND lease_token=$6::uuid AND attempt=$7`,
			nextAttempt, errorCode, errorMessage, claim.ID, workerID,
			claim.LeaseToken, claim.Attempt,
		)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return errDWDModelingLeaseLost
		}
		return nil
	})
}

func (worker *DWDModelingWorker) finishWithoutOutput(
	ctx context.Context,
	claim dwdModelingClaim,
	workerID, status, errorCode, errorMessage string,
) error {
	return database.WithTenantTx(ctx, worker.store.pool, claim.TenantID, func(tx pgx.Tx) error {
		return finishDWDJobTx(
			ctx, tx, claim, workerID, "", "", "", status,
			0, 0, 0, 0, 0,
			[]dwdModelingResultItem{}, errorCode, errorMessage,
		)
	})
}
