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
	"sync"
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

const dwdFactDesignConcurrency = 4

const (
	dwdStageDomainClassification = "DOMAIN_CLASSIFICATION"
	dwdStageDimensionModeling    = "DIMENSION_MODELING"
	dwdStageFactModeling         = "FACT_MODELING"
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
	StageJobID              string
	Stage                   string
	TenantID                string
	TriggerDatasetID        string
	TriggerDatasetVersionID string
	ActorID                 string
	LeaseToken              string
	Attempt                 int
	MaxAttempts             int
	CheckpointVersion       int
	SourceDatasetIDs        []string
	FactSourceDatasetIDs    []string
	FactScopeSelected       bool
}

type dwdClassificationBatchError struct {
	FailureCount int
	Cause        error
}

func (err *dwdClassificationBatchError) Error() string {
	if err == nil {
		return "<nil>"
	}
	return fmt.Sprintf(
		"%d 张 ODS 分类输出在纠错后仍无效：%v",
		err.FailureCount, err.Cause,
	)
}

func (err *dwdClassificationBatchError) Unwrap() error {
	if err == nil {
		return nil
	}
	return err.Cause
}

type dwdODSAsset struct {
	DatasetID        string
	VersionID        string
	DomainID         string
	DomainName       string
	SchemaHash       string
	Code             string
	Name             string
	Description      string
	SourceSchemaName string
	SourceTableName  string
	Tags             []string
	Document         Document
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
	ErrorMessage    string `json:"errorMessage,omitempty"`
}

type dwdDimensionStageResult struct {
	Prepared                   bool
	AssetsBySourceVersion      map[string]dwdODSAsset
	DimensionVersionBySource   map[string]string
	CheckpointScope            string
	Items                      []dwdModelingResultItem
	Created                    int
	Updated                    int
	Retired                    int
	Skipped                    int
	FailedDesignCount          int
	FailedSourceDatasets       []string
	PendingPublicationCount    int
	PendingPublicationDatasets []string
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
	err = worker.processWithLeaseHeartbeat(ctx, *claim, workerID, lease)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, errDWDModelingLeaseLost):
		// Ownership loss is not a source-version change. The normal expired-lease
		// recovery path will resume from validated checkpoints on the next claim.
		return true, err
	case errors.Is(err, errDWDModelingSubjectChange):
		return true, worker.finishWithoutOutput(ctx, *claim, workerID, "SKIPPED", "SUBJECT_CHANGED", err.Error())
	case errors.Is(err, errDWDModelingTagsNotReady):
		return true, worker.retryOrSkip(ctx, *claim, workerID, "CLASSIFICATION_NOT_READY", err.Error())
	case retryableDWDClassificationFailure(err) &&
		claim.Attempt < claim.MaxAttempts:
		return true, worker.retryOrSkip(
			ctx, *claim, workerID, "AI_INVALID_OUTPUT",
			dwdModelingFailureMessage(err),
		)
	default:
		if terminal, errorCode := terminalDWDModelingFailure(err); terminal {
			finishErr := worker.finishWithoutOutput(
				ctx, *claim, workerID, "FAILED", errorCode,
				dwdModelingFailureMessage(err),
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

func dwdModelingFailureMessage(err error) string {
	detail := strings.TrimSpace(dwdValidationDetail(err))
	diagnostic := strings.TrimSpace(
		aiplatform.InvalidOutputDiagnostic(err),
	)
	if diagnostic != "" && !strings.Contains(detail, diagnostic) {
		detail += "；结构诊断：" + diagnostic
	}
	if detail == "" {
		return "DIM/DWD 分层建模遇到不可重试错误"
	}
	message := "DIM/DWD 建模校验失败：" + detail
	return boundedDWDJobMessage(message)
}

func retryableDWDClassificationFailure(err error) bool {
	var batchError *dwdClassificationBatchError
	if !errors.As(err, &batchError) || batchError == nil {
		return false
	}
	if errors.Is(batchError.Cause, errDWDModelingInvalid) {
		return true
	}
	var providerError *aiplatform.ProviderError
	return errors.As(batchError.Cause, &providerError) &&
		providerError.Code == aiplatform.ErrorCodeInvalidOutput
}

func boundedDWDJobMessage(message string) string {
	message = strings.Map(func(character rune) rune {
		if unicode.IsControl(character) {
			return ' '
		}
		return character
	}, message)
	message = strings.Join(strings.Fields(message), " ")
	runes := []rune(message)
	if len(runes) > 1024 {
		message = string(runes[:1024])
	}
	return message
}

func (worker *DWDModelingWorker) processWithLeaseHeartbeat(
	ctx context.Context,
	claim dwdModelingClaim,
	workerID string,
	lease time.Duration,
) error {
	workCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	heartbeatDone := make(chan error, 1)
	interval := lease / 3
	if interval < time.Second {
		interval = time.Second
	}
	go func() {
		timer := time.NewTicker(interval)
		defer timer.Stop()
		for {
			select {
			case <-workCtx.Done():
				heartbeatDone <- nil
				return
			case <-timer.C:
				if err := worker.renewDWDClaim(
					workCtx, claim, workerID, lease,
				); err != nil {
					cancel()
					heartbeatDone <- err
					return
				}
			}
		}
	}()
	processErr := worker.process(workCtx, claim, workerID, lease)
	cancel()
	heartbeatErr := <-heartbeatDone
	if processErr == nil {
		return nil
	}
	if heartbeatErr != nil && !errors.Is(heartbeatErr, context.Canceled) &&
		ctx.Err() == nil {
		return heartbeatErr
	}
	return processErr
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
		// Serialize claim decisions per tenant so each workflow advances through
		// classification, dimension modeling and fact modeling in order.
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(
			hashtext('platform.dwd_modeling_stage_jobs'),hashtext($1)
		)`, tenantID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE platform.dwd_modeling_stage_jobs AS stage
			SET status='PENDING',attempt=0,next_attempt_at=now(),
				error_code='RESUMING_FROM_CHECKPOINT',
				error_message='上次执行已保存有效检查点，将从缺失阶段继续',
				lease_owner='',lease_token=NULL,lease_expires_at=NULL,
				updated_at=now()
			FROM platform.dwd_modeling_jobs AS workflow
			WHERE stage.workflow_job_id=workflow.id
			  AND stage.tenant_id=workflow.tenant_id
			  AND stage.status='RUNNING' AND stage.lease_expires_at<=now()
			  AND workflow.checkpoint_version>
			      workflow.claimed_checkpoint_version`); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `WITH expired AS (
			UPDATE platform.dwd_modeling_stage_jobs AS stage
			SET status='FAILED',error_code='LEASE_EXPIRED',
				error_message='worker lease expired after maximum attempts',
				lease_owner='',lease_token=NULL,lease_expires_at=NULL,
				completed_at=now(),updated_at=now()
			FROM platform.dwd_modeling_jobs AS workflow
			WHERE stage.workflow_job_id=workflow.id
			  AND stage.tenant_id=workflow.tenant_id
			  AND stage.status='RUNNING' AND stage.lease_expires_at<=now()
			  AND stage.attempt>=stage.max_attempts
			  AND workflow.checkpoint_version=
			      workflow.claimed_checkpoint_version
			RETURNING stage.workflow_job_id
		)
		UPDATE platform.dwd_modeling_jobs AS workflow
		SET status='FAILED',error_code='LEASE_EXPIRED',
			error_message='建模阶段 worker lease expired after maximum attempts',
			completed_at=now(),updated_at=now(),
			lease_owner='',lease_token=NULL,lease_expires_at=NULL
		WHERE workflow.id IN (SELECT workflow_job_id FROM expired)`); err != nil {
			return err
		}
		// 旧版本在上游终态失败后会把下游留在 PENDING；依赖查询又只允许
		// SUCCEEDED 前置，导致这些任务永远无法领取并让 UI 长期停在 75%。
		// 每次领取前收口遗留依赖，保证所有不可执行任务进入明确终态。
		if _, err := tx.Exec(ctx, `UPDATE platform.dwd_modeling_stage_jobs AS stage
			SET status='SKIPPED',error_code='UPSTREAM_NOT_SUCCEEDED',
				error_message='上游建模阶段未成功，当前阶段已终止等待',
				lease_owner='',lease_token=NULL,lease_expires_at=NULL,
				completed_at=now(),updated_at=now()
			WHERE stage.status='PENDING'
			  AND EXISTS(
			    SELECT 1
			    FROM platform.dwd_modeling_stage_jobs AS predecessor
			    WHERE predecessor.workflow_job_id=stage.workflow_job_id
			      AND predecessor.tenant_id=stage.tenant_id
			      AND predecessor.stage_order<stage.stage_order
			      AND predecessor.status IN ('PARTIAL','FAILED','SKIPPED')
			  )`); err != nil {
			return err
		}
		item := dwdModelingClaim{TenantID: tenantID}
		err := tx.QueryRow(ctx, `WITH candidate AS (
			SELECT queued.id
			FROM platform.dwd_modeling_stage_jobs AS queued
			WHERE queued.attempt<queued.max_attempts
			  AND queued.manual_enabled
			  AND queued.not_before<=now()
			  AND (
			    (queued.status='PENDING' AND queued.next_attempt_at<=now())
			    OR (queued.status='RUNNING' AND queued.lease_expires_at<=now())
			  )
			  AND NOT EXISTS(
			    SELECT 1
			    FROM platform.dwd_modeling_stage_jobs AS predecessor
			    WHERE predecessor.workflow_job_id=queued.workflow_job_id
			      AND predecessor.tenant_id=queued.tenant_id
			      AND predecessor.stage_order<queued.stage_order
			      AND predecessor.status<>'SUCCEEDED'
			  )
			  AND NOT EXISTS(
			    SELECT 1
			    FROM platform.dwd_modeling_stage_jobs AS active
			    WHERE active.status='RUNNING'
			      AND active.lease_expires_at>now()
			  )
			ORDER BY queued.next_attempt_at,queued.stage_order,
				queued.created_at,queued.id
			FOR UPDATE SKIP LOCKED
			LIMIT 1
		)
		UPDATE platform.dwd_modeling_stage_jobs AS stage
		SET status='RUNNING',attempt=attempt+1,error_code='',error_message='',
			lease_owner=$1,lease_token=public.gen_random_uuid(),
			lease_expires_at=now()+($2*interval '1 second'),
			started_at=COALESCE(started_at,now()),completed_at=NULL,updated_at=now()
		FROM candidate
		WHERE stage.id=candidate.id
		RETURNING stage.id::text,stage.workflow_job_id::text,stage.stage,
			COALESCE(stage.requested_by::text,''),stage.lease_token::text,
			stage.attempt,stage.max_attempts`,
			workerID, int64(lease/time.Second),
		).Scan(
			&item.StageJobID, &item.ID, &item.Stage, &item.ActorID,
			&item.LeaseToken, &item.Attempt, &item.MaxAttempts,
		)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `UPDATE platform.dwd_modeling_jobs
			SET status='RUNNING',requested_by=$1::uuid,
				claimed_checkpoint_version=checkpoint_version,
				started_at=COALESCE(started_at,now()),completed_at=NULL,
				error_code='',error_message='',updated_at=now()
			WHERE id=$2::uuid
			RETURNING trigger_dataset_id::text,
				trigger_dataset_version_id::text,checkpoint_version,
				COALESCE(source_dataset_ids::text[],'{}'::text[]),
				COALESCE(fact_source_dataset_ids::text[],'{}'::text[]),
				fact_source_dataset_ids IS NOT NULL`,
			item.ActorID, item.ID,
		).Scan(
			&item.TriggerDatasetID, &item.TriggerDatasetVersionID,
			&item.CheckpointVersion, &item.SourceDatasetIDs,
			&item.FactSourceDatasetIDs, &item.FactScopeSelected,
		); err != nil {
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
	planner, ok := worker.planner.(resumableDWDModelingPlanner)
	if !ok {
		return fmt.Errorf(
			"%w: split warehouse stages require a resumable planner",
			errDWDModelingInvalid,
		)
	}
	switch claim.Stage {
	case dwdStageDomainClassification:
		completion, err := worker.runDWDClassificationTask(
			ctx, claim, workerID, lease, input, snapshotHash, planner,
		)
		if err != nil {
			return err
		}
		return worker.finishDWDStageCompletion(
			ctx, claim, workerID, input, completion,
			"SUCCEEDED", "", "",
		)
	case dwdStageDimensionModeling:
		completion, err := worker.runDWDDimensionTask(
			ctx, claim, workerID, input, snapshotHash, planner,
		)
		if err != nil {
			return err
		}
		status, errorCode, errorMessage := "SUCCEEDED", "", ""
		if completion.DimensionStage.FailedDesignCount > 0 {
			status = "PARTIAL"
			errorCode = "DIM_STANDARDIZATION_INCOMPLETE"
			errorMessage = fmt.Sprintf(
				"%d 张维度源未通过角色/字段合同校验或未形成有效 DIM 草稿，请查看失败明细后重试",
				completion.DimensionStage.FailedDesignCount,
			)
		}
		return worker.finishDWDStageCompletion(
			ctx, claim, workerID, input, completion,
			status, errorCode, errorMessage,
		)
	case dwdStageFactModeling:
		completion, waiting, err := worker.runDWDFactTask(
			ctx, claim, workerID, input, snapshotHash, planner,
		)
		if err != nil {
			return err
		}
		if dwdFactProductCount(completion.Plan.Classifications) == 0 {
			return worker.finishDWDStageCompletion(
				ctx, claim, workerID, input, completion,
				"SUCCEEDED", "", "",
			)
		}
		if err := worker.persistLLMPlan(
			ctx, claim, workerID, input, snapshotHash, completion, !waiting,
		); err != nil {
			return err
		}
		if waiting {
			return worker.finishWaitingForPublishedDIMs(
				ctx, claim, workerID,
				completion.DimensionStage.PendingPublicationCount,
			)
		}
		return nil
	default:
		return fmt.Errorf(
			"%w: unknown warehouse modeling stage %s",
			errDWDModelingInvalid, claim.Stage,
		)
	}
}

func (worker *DWDModelingWorker) runDWDClassificationTask(
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
	if err := worker.renewDWDClaim(
		ctx, claim, workerID, lease,
	); err != nil {
		return dwdPlanningCompletion{}, err
	}
	merged, mergeReused, err := worker.loadDWDClassificationMergeCheckpoint(
		ctx, claim, workerID, input, snapshotHash,
	)
	if err != nil {
		return dwdPlanningCompletion{}, err
	}
	if mergeReused {
		completion.AIRequestID = merged.AIRequestID
		completion.Plan.Classifications = append(
			[]dwdLLMClassification(nil), merged.Classifications...,
		)
		completion.CheckpointCount = 1
		completion.ReusedCheckpointCount = 1
		return completion, nil
	}
	type classificationResult struct {
		completion dwdClassificationCompletion
		reused     bool
	}
	tasks := make(
		[]func(context.Context) (classificationResult, error),
		0, len(input.Tables),
	)
	for _, table := range input.Tables {
		versionID := table.VersionID
		tasks = append(tasks, func(taskCtx context.Context) (classificationResult, error) {
			scoped, err := dwdSingleTableClassificationScope(input, versionID)
			if err != nil {
				return classificationResult{}, err
			}
			classification, reused, err :=
				worker.loadDWDSingleClassificationCheckpoint(
					taskCtx, claim, workerID, scoped, snapshotHash, versionID,
				)
			if err != nil {
				return classificationResult{}, err
			}
			if reused {
				return classificationResult{
					completion: classification, reused: true,
				}, nil
			}
			classification, err = planner.Classify(taskCtx, scoped)
			if err != nil {
				return classificationResult{}, err
			}
			classification.Classifications = normalizeDWDClassifications(
				scoped, classification.Classifications,
			)
			if err := validateDWDLLMClassifications(
				scoped, classification.Domain,
				classification.Classifications,
			); err != nil {
				return classificationResult{}, err
			}
			if err := worker.saveDWDModelingCheckpoint(
				taskCtx, claim, workerID, input, snapshotHash,
				"CLASSIFICATION", versionID,
				dwdClassificationPromptVersion, classification.AIRequestID,
				dwdLLMClassificationPlan{
					Domain:          classification.Domain,
					Classifications: classification.Classifications,
				},
			); err != nil {
				return classificationResult{}, err
			}
			return classificationResult{completion: classification}, nil
		})
	}
	results, taskErrors := runBoundedDWDTasksCollect(
		ctx, dwdFactDesignConcurrency, tasks,
	)
	failureCount := 0
	var firstFailure error
	for _, taskErr := range taskErrors {
		if taskErr == nil {
			continue
		}
		if errors.Is(taskErr, context.Canceled) ||
			errors.Is(taskErr, errDWDModelingLeaseLost) ||
			errors.Is(taskErr, errDWDModelingSubjectChange) {
			return dwdPlanningCompletion{}, taskErr
		}
		failureCount++
		if firstFailure == nil {
			firstFailure = taskErr
		}
	}
	for index, result := range results {
		if taskErrors[index] != nil {
			continue
		}
		if result.completion.Domain != input.Domain ||
			len(result.completion.Classifications) != 1 {
			return dwdPlanningCompletion{}, errDWDModelingInvalid
		}
		completion.CheckpointCount++
		if result.reused {
			completion.ReusedCheckpointCount++
		}
		completion.AIRequestID = result.completion.AIRequestID
		completion.Plan.Classifications = append(
			completion.Plan.Classifications,
			result.completion.Classifications[0],
		)
	}
	if firstFailure != nil {
		return dwdPlanningCompletion{}, &dwdClassificationBatchError{
			FailureCount: failureCount,
			Cause:        firstFailure,
		}
	}
	if err := validateDWDLLMClassifications(
		input, completion.Plan.Domain, completion.Plan.Classifications,
	); err != nil {
		return dwdPlanningCompletion{}, err
	}
	merged, err = planner.MergeClassifications(
		ctx, input, completion.Plan.Classifications,
	)
	if err != nil {
		return dwdPlanningCompletion{}, &dwdClassificationBatchError{
			FailureCount: 1,
			Cause:        err,
		}
	}
	merged.Classifications = canonicalizeMergedDWDClassifications(
		input, merged.Classifications, completion.Plan.Classifications,
	)
	if merged.Domain != input.Domain {
		return dwdPlanningCompletion{}, &dwdClassificationBatchError{
			FailureCount: 1,
			Cause:        errDWDModelingInvalid,
		}
	}
	if err := validateDWDLLMClassifications(
		input, merged.Domain, merged.Classifications,
	); err != nil {
		return dwdPlanningCompletion{}, &dwdClassificationBatchError{
			FailureCount: 1,
			Cause:        err,
		}
	}
	if err := worker.saveDWDModelingCheckpoint(
		ctx, claim, workerID, input, snapshotHash,
		"CLASSIFICATION_MERGE", claim.TriggerDatasetVersionID,
		dwdClassificationMergeVersion, merged.AIRequestID,
		dwdLLMClassificationPlan{
			Domain:          merged.Domain,
			Classifications: merged.Classifications,
		},
	); err != nil {
		return dwdPlanningCompletion{}, err
	}
	completion.CheckpointCount++
	completion.AIRequestID = merged.AIRequestID
	completion.Plan.Domain = merged.Domain
	completion.Plan.Classifications = append(
		[]dwdLLMClassification(nil), merged.Classifications...,
	)
	return completion, nil
}

func (worker *DWDModelingWorker) runDWDDimensionTask(
	ctx context.Context,
	claim dwdModelingClaim,
	workerID string,
	input dwdPlanningInput,
	snapshotHash string,
	planner resumableDWDModelingPlanner,
) (dwdPlanningCompletion, error) {
	classification, reused, err := worker.loadDWDClassificationCheckpoint(
		ctx, claim, workerID, input, snapshotHash,
	)
	if err != nil {
		return dwdPlanningCompletion{}, err
	}
	if !reused {
		return dwdPlanningCompletion{}, fmt.Errorf(
			"%w: classification predecessor has no validated output",
			errDWDModelingInvalid,
		)
	}
	completion := dwdPlanningCompletion{
		AIRequestID: classification.AIRequestID,
		Plan: dwdLLMPlan{
			Domain: classification.Domain,
			Classifications: append(
				[]dwdLLMClassification(nil),
				classification.Classifications...,
			),
		},
		CheckpointCount:       1,
		ReusedCheckpointCount: 1,
		DimensionDesigns:      map[string]dwdLLMDimensionDesign{},
	}
	type dimensionResult struct {
		completion dwdDimensionDesignCompletion
		failure    *dwdDimensionDesignFailure
		reused     bool
	}
	tasks := []func(context.Context) (dimensionResult, error){}
	for _, dimensionSpec := range dwdDimensionSpecs(
		classification.Classifications,
	) {
		spec := dimensionSpec
		versionID := spec.Classification.DatasetVersionID
		dimensionIdentity := spec.Identity
		checkpointPromptVersion := dwdDimensionCheckpointPromptVersion(spec)
		tasks = append(
			tasks,
			func(taskCtx context.Context) (dimensionResult, error) {
				design, designReused, err :=
					worker.loadDWDDimensionCheckpoint(
						taskCtx, claim, workerID, input, snapshotHash,
						classification.Classifications, dimensionIdentity,
					)
				if err != nil {
					return dimensionResult{}, err
				}
				if designReused {
					return dimensionResult{
						completion: design, reused: true,
					}, nil
				}
				design, err = planner.DesignDimension(
					taskCtx, input, classification.Classifications,
					dimensionIdentity,
				)
				if err != nil {
					if terminal, errorCode := terminalDWDModelingFailure(err); terminal {
						return dimensionResult{
							failure: &dwdDimensionDesignFailure{
								SourceDatasetVersionID: versionID,
								DimensionIdentity:      dimensionIdentity,
								MappingKey:             spec.MappingKey,
								ErrorCode:              errorCode,
								ErrorMessage:           dwdModelingFailureMessage(err),
							},
						}, nil
					}
					return dimensionResult{}, err
				}
				if err := worker.saveDWDModelingCheckpoint(
					taskCtx, claim, workerID, input, snapshotHash,
					"DIM_DESIGN", versionID,
					checkpointPromptVersion, design.AIRequestID,
					dwdLLMDimensionDesignPayload{Output: design.Output},
				); err != nil {
					return dimensionResult{}, err
				}
				return dimensionResult{completion: design}, nil
			},
		)
	}
	results, err := runBoundedDWDTasks(
		ctx, dwdFactDesignConcurrency, tasks,
	)
	if err != nil {
		return dwdPlanningCompletion{}, err
	}
	for _, result := range results {
		if result.failure != nil {
			completion.DimensionFailures = append(
				completion.DimensionFailures, *result.failure,
			)
			continue
		}
		completion.CheckpointCount++
		if result.reused {
			completion.ReusedCheckpointCount++
		}
		completion.AIRequestID = result.completion.AIRequestID
		completion.DimensionDesigns[result.completion.Output.DimensionIdentity] =
			result.completion.Output
	}
	completion.DimensionStage, err = worker.prepareDIMStage(
		ctx, claim, workerID, input, snapshotHash,
		classification.Classifications, completion.DimensionDesigns,
		completion.DimensionFailures,
	)
	if err != nil {
		return completion, err
	}
	validation, err := worker.validateGeneratedDIMStage(
		ctx, claim, workerID, input, snapshotHash,
		classification.Classifications, completion.DimensionDesigns,
		completion.DimensionStage, planner,
	)
	if err != nil {
		return completion, err
	}
	completion.DimensionStage = validation.Stage
	completion.CheckpointCount += validation.CheckpointCount
	completion.ReusedCheckpointCount += validation.ReusedCheckpointCount
	if validation.AIRequestID != "" {
		completion.AIRequestID = validation.AIRequestID
	}
	return completion, nil
}

func (worker *DWDModelingWorker) runDWDFactTask(
	ctx context.Context,
	claim dwdModelingClaim,
	workerID string,
	input dwdPlanningInput,
	snapshotHash string,
	planner resumableDWDModelingPlanner,
) (dwdPlanningCompletion, bool, error) {
	classification, reused, err := worker.loadDWDClassificationCheckpoint(
		ctx, claim, workerID, input, snapshotHash,
	)
	if err != nil {
		return dwdPlanningCompletion{}, false, err
	}
	if !reused {
		return dwdPlanningCompletion{}, false, fmt.Errorf(
			"%w: classification predecessor has no validated output",
			errDWDModelingInvalid,
		)
	}
	completion := dwdPlanningCompletion{
		AIRequestID: classification.AIRequestID,
		Plan: dwdLLMPlan{
			Domain: classification.Domain,
			Classifications: append(
				[]dwdLLMClassification(nil),
				classification.Classifications...,
			),
		},
		CheckpointCount:       1,
		ReusedCheckpointCount: 1,
	}
	dimensionStage, err := worker.resolveModeledDIMStage(
		ctx, claim, workerID, input, snapshotHash,
		classification.Classifications,
	)
	if err != nil {
		return dwdPlanningCompletion{}, false, err
	}
	completion.DimensionStage = dimensionStage
	if len(dimensionStage.AssetsBySourceVersion) <
		dwdDimensionProductCount(classification.Classifications) {
		return completion, true, nil
	}
	factPlanningInput := planningInputWithModeledDimensions(
		input, dimensionStage, classification.Classifications,
	)
	factClassifications := expandedDWDClassifications(
		classification.Classifications, dimensionStage,
	)
	completion.FactPlanningInput = &factPlanningInput
	completion.FactClassifications = factClassifications
	factVersions, unchangedOutputs := selectIncrementalDWDFacts(
		input, classification.Classifications,
		dimensionStage.DimensionVersionBySource,
	)
	factVersions, unchangedOutputs = scopeDWDFactOutputs(
		input, factVersions, unchangedOutputs,
		claim.FactSourceDatasetIDs, claim.FactScopeSelected,
	)
	completion.UnchangedOutputs = append(
		completion.UnchangedOutputs, unchangedOutputs...,
	)
	type factResult struct {
		completion dwdFactDesignCompletion
		failure    *dwdFactDesignFailure
		reused     bool
	}
	tasks := make([]func(context.Context) (factResult, error), 0, len(factVersions))
	for _, factVersionID := range factVersions {
		versionID := factVersionID
		tasks = append(tasks, func(taskCtx context.Context) (factResult, error) {
			factCompletion, factReused, err := worker.loadDWDFactCheckpoint(
				taskCtx, claim, workerID, factPlanningInput, snapshotHash,
				factClassifications, versionID,
				dimensionStage.CheckpointScope,
			)
			if err != nil {
				return factResult{}, err
			}
			if factReused {
				return factResult{completion: factCompletion, reused: true}, nil
			}
			factCompletion, err = planner.DesignFact(
				taskCtx, factPlanningInput,
				factClassifications, versionID,
			)
			if err != nil {
				if terminal, errorCode := terminalDWDModelingFailure(err); terminal {
					return factResult{failure: &dwdFactDesignFailure{
						FactDatasetVersionID: versionID,
						ErrorCode:            errorCode,
						ErrorMessage:         dwdModelingFailureMessage(err),
					}}, nil
				}
				return factResult{}, err
			}
			if err := validateDWDFactCheckpoint(
				factPlanningInput, factClassifications,
				versionID, factCompletion.Output,
			); err != nil {
				return factResult{failure: &dwdFactDesignFailure{
					FactDatasetVersionID: versionID,
					ErrorCode:            "WAREHOUSE_MODELING_INVALID_OUTPUT",
					ErrorMessage:         dwdModelingFailureMessage(err),
				}}, nil
			}
			if err := worker.saveDWDModelingCheckpoint(
				taskCtx, claim, workerID, input, snapshotHash,
				"FACT_DESIGN", versionID,
				dwdFactCheckpointPromptVersion(
					dimensionStage.CheckpointScope,
				),
				factCompletion.AIRequestID,
				dwdLLMFactDesign{Output: factCompletion.Output},
			); err != nil {
				return factResult{}, err
			}
			return factResult{completion: factCompletion}, nil
		})
	}
	results, err := runBoundedDWDTasks(
		ctx, dwdFactDesignConcurrency, tasks,
	)
	if err != nil {
		return dwdPlanningCompletion{}, false, err
	}
	for _, result := range results {
		if result.failure != nil {
			completion.FactFailures = append(
				completion.FactFailures, *result.failure,
			)
			continue
		}
		completion.CheckpointCount++
		if result.reused {
			completion.ReusedCheckpointCount++
		}
		completion.AIRequestID = result.completion.AIRequestID
		completion.Plan.Outputs = append(
			completion.Plan.Outputs, result.completion.Output,
		)
	}
	factPlan := completion.Plan
	factPlan.Classifications = factClassifications
	if err := validateDWDPartialLLMPlan(
		factPlanningInput, factPlan,
	); err != nil {
		return dwdPlanningCompletion{}, false, err
	}
	return completion, dimensionStage.PendingPublicationCount > 0, nil
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
	completion, err := worker.runDWDClassificationTask(
		ctx, claim, workerID, lease, input, snapshotHash, planner,
	)
	if err != nil {
		return dwdPlanningCompletion{}, err
	}
	classification := dwdClassificationCompletion{
		AIRequestID:     completion.AIRequestID,
		Domain:          completion.Plan.Domain,
		Classifications: completion.Plan.Classifications,
	}

	type dimensionResult struct {
		completion dwdDimensionDesignCompletion
		failure    *dwdDimensionDesignFailure
		reused     bool
	}
	dimensionTasks := []func(context.Context) (dimensionResult, error){}
	for _, dimensionSpec := range dwdDimensionSpecs(
		classification.Classifications,
	) {
		spec := dimensionSpec
		versionID := spec.Classification.DatasetVersionID
		dimensionIdentity := spec.Identity
		checkpointPromptVersion := dwdDimensionCheckpointPromptVersion(spec)
		dimensionTasks = append(
			dimensionTasks,
			func(taskCtx context.Context) (dimensionResult, error) {
				dimensionCompletion, reused, err :=
					worker.loadDWDDimensionCheckpoint(
						taskCtx, claim, workerID, input, snapshotHash,
						classification.Classifications, dimensionIdentity,
					)
				if err != nil {
					return dimensionResult{}, err
				}
				if reused {
					return dimensionResult{
						completion: dimensionCompletion, reused: true,
					}, nil
				}
				dimensionCompletion, err = planner.DesignDimension(
					taskCtx, input, classification.Classifications,
					dimensionIdentity,
				)
				if err != nil {
					if terminal, errorCode := terminalDWDModelingFailure(err); terminal &&
						(errors.Is(err, errDWDModelingInvalid) ||
							errorCode == string(aiplatform.ErrorCodeInvalidOutput)) {
						return dimensionResult{
							failure: &dwdDimensionDesignFailure{
								SourceDatasetVersionID: versionID,
								DimensionIdentity:      dimensionIdentity,
								MappingKey:             spec.MappingKey,
								ErrorCode:              errorCode,
								ErrorMessage:           dwdModelingFailureMessage(err),
							},
						}, nil
					}
					return dimensionResult{}, err
				}
				if err := worker.saveDWDModelingCheckpoint(
					taskCtx, claim, workerID, input, snapshotHash,
					"DIM_DESIGN", versionID,
					checkpointPromptVersion,
					dimensionCompletion.AIRequestID,
					dwdLLMDimensionDesignPayload{
						Output: dimensionCompletion.Output,
					},
				); err != nil {
					return dimensionResult{}, err
				}
				return dimensionResult{completion: dimensionCompletion}, nil
			},
		)
	}
	dimensionResults, err := runBoundedDWDTasks(
		ctx, dwdFactDesignConcurrency, dimensionTasks,
	)
	if err != nil {
		return dwdPlanningCompletion{}, err
	}
	completion.DimensionDesigns = map[string]dwdLLMDimensionDesign{}
	for _, result := range dimensionResults {
		if result.failure != nil {
			completion.DimensionFailures = append(
				completion.DimensionFailures, *result.failure,
			)
			continue
		}
		completion.CheckpointCount++
		if result.reused {
			completion.ReusedCheckpointCount++
		}
		completion.AIRequestID = result.completion.AIRequestID
		completion.DimensionDesigns[result.completion.Output.DimensionIdentity] =
			result.completion.Output
	}
	dimensionStage, err := worker.prepareDIMStage(
		ctx, claim, workerID, input, snapshotHash,
		classification.Classifications, completion.DimensionDesigns,
		completion.DimensionFailures,
	)
	if err != nil {
		return dwdPlanningCompletion{}, err
	}
	validation, err := worker.validateGeneratedDIMStage(
		ctx, claim, workerID, input, snapshotHash,
		classification.Classifications, completion.DimensionDesigns,
		dimensionStage, planner,
	)
	if err != nil {
		return dwdPlanningCompletion{}, err
	}
	dimensionStage = validation.Stage
	completion.CheckpointCount += validation.CheckpointCount
	completion.ReusedCheckpointCount += validation.ReusedCheckpointCount
	if validation.AIRequestID != "" {
		completion.AIRequestID = validation.AIRequestID
	}
	completion.DimensionStage = dimensionStage
	if len(dimensionStage.AssetsBySourceVersion) <
		dwdDimensionProductCount(classification.Classifications) ||
		dimensionStage.FailedDesignCount > 0 {
		if err := validateDWDPartialLLMPlan(input, completion.Plan); err != nil {
			return dwdPlanningCompletion{}, err
		}
		return completion, nil
	}
	factPlanningInput := planningInputWithModeledDimensions(
		input, dimensionStage, classification.Classifications,
	)
	factClassifications := expandedDWDClassifications(
		classification.Classifications, dimensionStage,
	)
	completion.FactPlanningInput = &factPlanningInput
	completion.FactClassifications = factClassifications
	factVersions, unchangedOutputs := selectIncrementalDWDFacts(
		input, classification.Classifications,
		dimensionStage.DimensionVersionBySource,
	)
	factVersions, unchangedOutputs = scopeDWDFactOutputs(
		input, factVersions, unchangedOutputs,
		claim.FactSourceDatasetIDs, claim.FactScopeSelected,
	)
	completion.UnchangedOutputs = append(
		completion.UnchangedOutputs, unchangedOutputs...,
	)
	type factResult struct {
		completion dwdFactDesignCompletion
		failure    *dwdFactDesignFailure
		reused     bool
	}
	tasks := make([]func(context.Context) (factResult, error), 0, len(factVersions))
	for _, factVersionID := range factVersions {
		versionID := factVersionID
		tasks = append(tasks, func(taskCtx context.Context) (factResult, error) {
			factCompletion, factReused, err := worker.loadDWDFactCheckpoint(
				taskCtx, claim, workerID, factPlanningInput, snapshotHash,
				factClassifications, versionID,
				dimensionStage.CheckpointScope,
			)
			if err != nil {
				return factResult{}, err
			}
			if factReused {
				return factResult{completion: factCompletion, reused: true}, nil
			}
			factCompletion, err = planner.DesignFact(
				taskCtx, factPlanningInput, factClassifications, versionID,
			)
			if err != nil {
				if terminal, errorCode := terminalDWDModelingFailure(err); terminal &&
					(errors.Is(err, errDWDModelingInvalid) ||
						errorCode == string(aiplatform.ErrorCodeInvalidOutput)) {
					return factResult{failure: &dwdFactDesignFailure{
						FactDatasetVersionID: versionID,
						ErrorCode:            errorCode,
						ErrorMessage:         dwdModelingFailureMessage(err),
					}}, nil
				}
				return factResult{}, err
			}
			if err := validateDWDFactCheckpoint(
				factPlanningInput, factClassifications,
				versionID, factCompletion.Output,
			); err != nil {
				if errors.Is(err, errDWDModelingInvalid) {
					return factResult{failure: &dwdFactDesignFailure{
						FactDatasetVersionID: versionID,
						ErrorCode:            "WAREHOUSE_MODELING_INVALID_OUTPUT",
						ErrorMessage:         dwdModelingFailureMessage(err),
					}}, nil
				}
				return factResult{}, err
			}
			if err := worker.saveDWDModelingCheckpoint(
				taskCtx, claim, workerID, input, snapshotHash,
				"FACT_DESIGN", versionID,
				dwdFactCheckpointPromptVersion(dimensionStage.CheckpointScope),
				factCompletion.AIRequestID,
				dwdLLMFactDesign{Output: factCompletion.Output},
			); err != nil {
				return factResult{}, err
			}
			return factResult{completion: factCompletion}, nil
		})
	}
	factResults, err := runBoundedDWDTasks(
		ctx, dwdFactDesignConcurrency, tasks,
	)
	if err != nil {
		return dwdPlanningCompletion{}, err
	}
	for _, result := range factResults {
		if result.failure != nil {
			completion.FactFailures = append(
				completion.FactFailures, *result.failure,
			)
			continue
		}
		completion.CheckpointCount++
		if result.reused {
			completion.ReusedCheckpointCount++
		}
		completion.AIRequestID = result.completion.AIRequestID
		completion.Plan.Outputs = append(
			completion.Plan.Outputs, result.completion.Output,
		)
	}
	factPlan := completion.Plan
	factPlan.Classifications = factClassifications
	if err := validateDWDPartialLLMPlan(
		factPlanningInput, factPlan,
	); err != nil {
		return dwdPlanningCompletion{}, err
	}
	return completion, nil
}

// prepareDIMStage is the second warehouse-modeling stage. Classification has
// already established the domain-local entity tables, so this stage can
// deterministically add descriptions and value-standardization expressions,
// persist the DIM drafts, and expose their validated contracts to the
// subsequent fact-design stage. Publication remains a separate governance
// boundary and is not allowed to block DWD draft design.
func (worker *DWDModelingWorker) prepareDIMStage(
	ctx context.Context,
	claim dwdModelingClaim,
	workerID string,
	input dwdPlanningInput,
	expectedSnapshotHash string,
	classifications []dwdLLMClassification,
	designs map[string]dwdLLMDimensionDesign,
	failures []dwdDimensionDesignFailure,
) (dwdDimensionStageResult, error) {
	stage := dwdDimensionStageResult{
		Prepared:                 true,
		AssetsBySourceVersion:    map[string]dwdODSAsset{},
		DimensionVersionBySource: map[string]string{},
	}
	err := database.WithTenantTx(
		ctx, worker.store.pool, claim.TenantID, func(tx pgx.Tx) error {
			if err := validateDWDClaimTx(ctx, tx, claim, workerID); err != nil {
				return err
			}
			assets, err := loadPublishedODSAssetsTx(ctx, tx)
			if err != nil {
				return err
			}
			sameDomain := dwdPlanningAssetsForInput(assets, input)
			assetsByVersion := map[string]dwdODSAsset{}
			for _, asset := range sameDomain {
				assetsByVersion[asset.VersionID] = asset
			}
			currentSnapshotHash, err := dwdPlanningSnapshotHash(sameDomain)
			if err != nil {
				return err
			}
			if currentSnapshotHash != expectedSnapshotHash {
				return errDWDModelingSubjectChange
			}
			tableByVersion := make(
				map[string]dwdPlanningTable, len(input.Tables),
			)
			for _, table := range input.Tables {
				tableByVersion[table.VersionID] = table
			}
			// A previous classifier may have created a DIM from an atomic event
			// or snapshot fact. Once the stronger grain contract corrects that
			// role, retire only the untouched unpublished system draft. Published,
			// reviewed, edited or referenced assets remain visible for governance.
			for _, classification := range classifications {
				if classificationProducesDimension(classification) {
					continue
				}
				table, exists := tableByVersion[classification.DatasetVersionID]
				if !exists {
					return errDWDModelingSubjectChange
				}
				factKind := dwdNonEntityFactKind(table)
				if factKind == "" {
					continue
				}
				source, exists := assetsByVersion[classification.DatasetVersionID]
				if !exists {
					return errDWDModelingSubjectChange
				}
				dimDatasetID, mapped, retired, err :=
					worker.retireInvalidFactGeneratedDIMDraftTx(
						ctx, tx, claim, source,
					)
				if err != nil {
					return err
				}
				if !mapped {
					continue
				}
				if retired {
					stage.Retired++
					stage.Items = append(stage.Items, dwdModelingResultItem{
						Layer: string(LayerDIM), SourceDatasetID: source.DatasetID,
						SourceVersionID: source.VersionID, DatasetID: dimDatasetID,
						Action: "RETIRED", Reason: "NON_ENTITY_FACT",
						ErrorMessage: factKind,
					})
					continue
				}
				stage.Skipped++
				stage.Items = append(stage.Items, dwdModelingResultItem{
					Layer: string(LayerDIM), SourceDatasetID: source.DatasetID,
					SourceVersionID: source.VersionID, DatasetID: dimDatasetID,
					Action: "SKIPPED", Reason: "NON_ENTITY_FACT_REQUIRES_REVIEW",
					ErrorMessage: factKind,
				})
			}
			for _, failure := range failures {
				source, exists := assetsByVersion[failure.SourceDatasetVersionID]
				if !exists {
					return errDWDModelingSubjectChange
				}
				stage.Skipped++
				stage.FailedSourceDatasets = append(
					stage.FailedSourceDatasets, source.DatasetID,
				)
				stage.Items = append(stage.Items, dwdModelingResultItem{
					Layer: string(LayerDIM), SourceDatasetID: source.DatasetID,
					SourceVersionID: source.VersionID, Action: "SKIPPED",
					Reason: failure.ErrorCode, ErrorMessage: failure.ErrorMessage,
				})
			}
			for _, spec := range dwdDimensionSpecs(classifications) {
				source, exists := assetsByVersion[spec.Classification.DatasetVersionID]
				if !exists {
					return errDWDModelingSubjectChange
				}
				design, designed := designs[spec.Identity]
				if !designed {
					if !containsString(
						stage.FailedSourceDatasets, source.DatasetID,
					) {
						stage.Skipped++
						stage.FailedSourceDatasets = append(
							stage.FailedSourceDatasets, source.DatasetID,
						)
						stage.Items = append(
							stage.Items, dwdModelingResultItem{
								Layer:           string(LayerDIM),
								SourceDatasetID: source.DatasetID,
								SourceVersionID: source.VersionID,
								Action:          "SKIPPED",
								Reason:          "DIM_DESIGN_MISSING",
							},
						)
					}
					continue
				}
				document, inputHash, buildErr := buildLLMDesignedDIMDocument(
					input.Domain, source, design,
				)
				if buildErr != nil {
					stage.Skipped++
					stage.FailedSourceDatasets = append(
						stage.FailedSourceDatasets, source.DatasetID,
					)
					stage.Items = append(stage.Items, dwdModelingResultItem{
						Layer: string(LayerDIM), SourceDatasetID: source.DatasetID,
						SourceVersionID: source.VersionID, Action: "SKIPPED",
						Reason: "DIM_DAG_INVALID",
					})
					continue
				}
				prepared, prepareErr := Prepare(mustMarshalDWDDocument(document))
				if prepareErr != nil {
					stage.Skipped++
					stage.FailedSourceDatasets = append(
						stage.FailedSourceDatasets, source.DatasetID,
					)
					stage.Items = append(stage.Items, dwdModelingResultItem{
						Layer: string(LayerDIM), SourceDatasetID: source.DatasetID,
						SourceVersionID: source.VersionID, Action: "SKIPPED",
						Reason: "DIM_DAG_INVALID",
					})
					continue
				}
				dimDatasetID, action, upsertErr :=
					worker.upsertGeneratedDIMDraftTx(
						ctx, tx, claim, source, spec.MappingKey,
						input.Domain, inputHash, prepared,
					)
				if upsertErr != nil {
					if !errors.Is(upsertErr, ErrConflict) &&
						!errors.Is(upsertErr, ErrAlreadyExists) {
						return upsertErr
					}
					stage.Skipped++
					action = "SKIPPED"
					if err := tx.QueryRow(ctx, `SELECT dim_dataset_id::text
						FROM platform.dim_modeling_outputs
						WHERE source_dataset_id=$1::uuid
						  AND dimension_key=$2`,
						source.DatasetID, spec.MappingKey,
					).Scan(&dimDatasetID); err != nil &&
						!errors.Is(err, pgx.ErrNoRows) {
						return err
					}
					stage.Items = append(stage.Items, dwdModelingResultItem{
						Layer: string(LayerDIM), SourceDatasetID: source.DatasetID,
						SourceVersionID: source.VersionID, DatasetID: dimDatasetID,
						Action: action, Reason: "MANUAL_DRAFT_CHANGED",
					})
				} else {
					switch action {
					case "CREATED":
						stage.Created++
					case "UPDATED":
						stage.Updated++
					}
					stage.Items = append(stage.Items, dwdModelingResultItem{
						Layer: string(LayerDIM), SourceDatasetID: source.DatasetID,
						SourceVersionID: source.VersionID, DatasetID: dimDatasetID,
						Action: action,
					})
				}
				modeled, published, err := loadCompatibleModeledDIMAssetTx(
					ctx, tx, dimDatasetID, source,
				)
				if err != nil {
					return err
				}
				if modeled == nil {
					if dimDatasetID == "" {
						stage.FailedSourceDatasets = append(
							stage.FailedSourceDatasets, source.DatasetID,
						)
						continue
					}
					stage.PendingPublicationDatasets = append(
						stage.PendingPublicationDatasets, dimDatasetID,
					)
					continue
				}
				stage.AssetsBySourceVersion[spec.Identity] = *modeled
				if spec.MappingKey == "primary" {
					stage.DimensionVersionBySource[source.DatasetID] =
						modeled.VersionID
				}
				if !published {
					stage.PendingPublicationDatasets = append(
						stage.PendingPublicationDatasets, dimDatasetID,
					)
				}
			}
			return nil
		},
	)
	if err != nil {
		return dwdDimensionStageResult{}, err
	}
	sort.Strings(stage.PendingPublicationDatasets)
	stage.PendingPublicationDatasets = uniqueDWDStrings(
		stage.PendingPublicationDatasets,
	)
	stage.PendingPublicationCount = len(stage.PendingPublicationDatasets)
	sort.Strings(stage.FailedSourceDatasets)
	stage.FailedSourceDatasets = uniqueDWDStrings(stage.FailedSourceDatasets)
	stage.FailedDesignCount = len(stage.FailedSourceDatasets)
	scope, err := dwdDimensionStageScope(stage.AssetsBySourceVersion)
	if err != nil {
		return dwdDimensionStageResult{}, err
	}
	stage.CheckpointScope = scope
	return stage, nil
}

// loadCompatibleModeledDIMAssetTx prefers the exact published DIM contract,
// then falls back to the system-generated draft contract. The fallback is only
// used to build another reviewable warehouse draft; publication validation
// remains strict and rejects draft dependencies.
func loadCompatibleModeledDIMAssetTx(
	ctx context.Context,
	tx pgx.Tx,
	dimDatasetID string,
	source dwdODSAsset,
) (*dwdODSAsset, bool, error) {
	if dimDatasetID == "" {
		return nil, false, nil
	}
	rows, err := tx.Query(ctx, `SELECT dataset.id::text,version.id::text,
			version.schema_hash,dataset.code,dataset.name,dataset.description,
			version.dsl_json,version.status
		FROM platform.datasets AS dataset
		JOIN platform.dataset_versions AS version
		  ON version.id IN (
		       dataset.current_published_version_id,
		       dataset.current_draft_version_id
		     )
		 AND version.dataset_id=dataset.id
		 AND version.tenant_id=dataset.tenant_id
		 AND version.layer='DIM'
		WHERE dataset.id=$1::uuid
		  AND dataset.deleted_at IS NULL
		  AND (
		    (version.id=dataset.current_published_version_id
		     AND version.status='PUBLISHED')
		    OR
		    (version.id=dataset.current_draft_version_id
		     AND version.status='DRAFT')
		  )
		ORDER BY CASE
		  WHEN version.id=dataset.current_published_version_id THEN 0
		  ELSE 1
		END`, dimDatasetID)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	for rows.Next() {
		var asset dwdODSAsset
		var raw json.RawMessage
		var status string
		if err := rows.Scan(
			&asset.DatasetID, &asset.VersionID, &asset.SchemaHash,
			&asset.Code, &asset.Name, &asset.Description, &raw, &status,
		); err != nil {
			return nil, false, err
		}
		document, err := DecodeAndNormalize(raw)
		if err != nil {
			return nil, false, err
		}
		compatible := false
		for _, node := range document.Nodes {
			if node.DatasetVersionID == source.VersionID {
				compatible = true
				break
			}
		}
		if !compatible {
			continue
		}
		asset.Document = document
		asset.Code = document.Dataset.Code
		asset.Name = document.Dataset.Name
		asset.Description = document.Dataset.Description
		asset.Tags = append([]string(nil), source.Tags...)
		return &asset, status == "PUBLISHED", nil
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	return nil, false, nil
}

func planningInputWithModeledDimensions(
	input dwdPlanningInput,
	stage dwdDimensionStageResult,
	classifications []dwdLLMClassification,
) dwdPlanningInput {
	result := input
	result.Tables = make(
		[]dwdPlanningTable, 0,
		len(input.Tables)+len(stage.AssetsBySourceVersion),
	)
	classificationByVersion := make(
		map[string]dwdLLMClassification, len(classifications),
	)
	for _, classification := range classifications {
		classificationByVersion[classification.DatasetVersionID] = classification
	}
	seenDimensionDatasets := map[string]bool{}
	for _, table := range input.Tables {
		classification, classified := classificationByVersion[table.VersionID]
		if !classified {
			result.Tables = append(result.Tables, table)
			continue
		}
		specs := classificationDimensionSpecs(classification)
		if classification.Role == "FACT" || len(specs) == 0 {
			result.Tables = append(result.Tables, table)
		}
		for _, spec := range specs {
			asset, exists := stage.AssetsBySourceVersion[spec.Identity]
			if !exists || seenDimensionDatasets[asset.DatasetID] {
				continue
			}
			seenDimensionDatasets[asset.DatasetID] = true
			dimensionTable := table
			dimensionTable.DatasetID = asset.DatasetID
			dimensionTable.VersionID = spec.Identity
			dimensionTable.SourceCode = asset.Code
			dimensionTable.SourceTableName = ""
			dimensionTable.Name = asset.Document.Dataset.Name
			dimensionTable.Description = asset.Document.Dataset.Description
			dimensionTable.OutputGrain = asset.Document.OutputGrain
			dimensionTable.DimensionStage = "STANDARDIZED_DIM_CONTRACT"
			dimensionTable.Fields = make(
				[]dwdPlanningField, 0, len(asset.Document.Fields),
			)
			for _, field := range asset.Document.Fields {
				dimensionTable.Fields = append(
					dimensionTable.Fields, dwdPlanningField{
						Code: field.Code, Name: field.Name,
						Description: field.Description, Role: field.Role,
						CanonicalType: field.CanonicalType,
						SemanticType:  field.SemanticType,
						Nullable:      field.Nullable,
					},
				)
			}
			result.Tables = append(result.Tables, dimensionTable)
		}
	}
	return result
}

func expandedDWDClassifications(
	classifications []dwdLLMClassification,
	stages ...dwdDimensionStageResult,
) []dwdLLMClassification {
	result := make(
		[]dwdLLMClassification, 0, len(classifications)*2,
	)
	assets := map[string]dwdODSAsset{}
	if len(stages) > 0 {
		assets = stages[0].AssetsBySourceVersion
	}
	seenDimensionDatasets := map[string]bool{}
	for _, classification := range classifications {
		if classification.Role == "FACT" {
			result = append(result, classification)
		}
		specs := classificationDimensionSpecs(classification)
		if len(specs) == 0 {
			if classification.Role != "FACT" {
				result = append(result, classification)
			}
			continue
		}
		for _, spec := range specs {
			dimension := spec.Classification
			if asset, exists := assets[spec.Identity]; exists {
				if seenDimensionDatasets[asset.DatasetID] {
					continue
				}
				seenDimensionDatasets[asset.DatasetID] = true
				dimension = classificationForModeledDIMContract(
					dimension, asset.Document,
				)
			}
			dimension.DatasetVersionID = spec.Identity
			dimension.Role = "DIMENSION"
			dimension.AdditionalDimensions = nil
			if spec.MappingKey != "primary" ||
				classification.Role == "FACT" {
				dimension.Rationale =
					"从同一 ODS 的稳定实体键和说明属性抽取并标准化"
			}
			result = append(result, dimension)
		}
	}
	return result
}

// classificationForModeledDIMContract treats the current modeled DIM as the
// authoritative fact-planning contract. A classification checkpoint describes
// the source ODS at the time DIM design started, while the reviewed/published
// DIM may intentionally omit a source attribute. Carrying that stale attribute
// into fact planning makes a valid DIM publication impossible to consume.
func classificationForModeledDIMContract(
	classification dwdLLMClassification,
	document Document,
) dwdLLMClassification {
	fields := make(
		map[string]dwdPlanningField, len(document.Fields),
	)
	attributeCandidates := make([]string, 0, len(document.Fields))
	for _, field := range document.Fields {
		fields[field.Code] = dwdPlanningField{
			Code: field.Code, Name: field.Name, Description: field.Description,
			Role: field.Role, CanonicalType: field.CanonicalType,
			SemanticType: field.SemanticType, Nullable: field.Nullable,
		}
		attributeCandidates = append(attributeCandidates, field.Code)
	}
	keys := normalizeDWDClassificationFieldCodes(
		fields, document.OutputGrain.KeyFields,
	)
	if len(keys) == 0 {
		keys = normalizeDWDClassificationFieldCodes(
			fields, classification.DimensionKeyFieldCodes,
		)
	}
	classification.DimensionKeyFieldCodes = keys
	classification.DimensionAttributeFieldCodes =
		stableDWDEntityDimensionAttributes(
			fields, keys, attributeCandidates,
		)
	return classification
}

func dwdDimensionStageScope(
	assetsBySourceVersion map[string]dwdODSAsset,
) (string, error) {
	type scopeItem struct {
		SourceVersionID string `json:"sourceVersionId"`
		DIMDatasetID    string `json:"dimDatasetId"`
		SchemaHash      string `json:"schemaHash"`
	}
	items := make([]scopeItem, 0, len(assetsBySourceVersion))
	for sourceVersionID, asset := range assetsBySourceVersion {
		items = append(items, scopeItem{
			SourceVersionID: sourceVersionID,
			DIMDatasetID:    asset.DatasetID,
			SchemaHash:      asset.SchemaHash,
		})
	}
	sort.Slice(items, func(i, j int) bool {
		return items[i].SourceVersionID < items[j].SourceVersionID
	})
	raw, err := json.Marshal(items)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func dwdDimensionProductCount(
	classifications []dwdLLMClassification,
) int {
	return len(dwdDimensionSpecs(classifications))
}

func dwdFactProductCount(
	classifications []dwdLLMClassification,
) int {
	count := 0
	for _, classification := range classifications {
		if classification.Role == "FACT" {
			count++
		}
	}
	return count
}

func uniqueDWDStrings(values []string) []string {
	if len(values) < 2 {
		return values
	}
	result := values[:0]
	for _, value := range values {
		if len(result) == 0 || result[len(result)-1] != value {
			result = append(result, value)
		}
	}
	return result
}

func (worker *DWDModelingWorker) resolveModeledDIMStage(
	ctx context.Context,
	claim dwdModelingClaim,
	workerID string,
	input dwdPlanningInput,
	expectedSnapshotHash string,
	classifications []dwdLLMClassification,
) (dwdDimensionStageResult, error) {
	stage := dwdDimensionStageResult{
		Prepared:                 true,
		AssetsBySourceVersion:    map[string]dwdODSAsset{},
		DimensionVersionBySource: map[string]string{},
	}
	err := database.WithTenantTx(
		ctx, worker.store.pool, claim.TenantID, func(tx pgx.Tx) error {
			if err := validateDWDClaimTx(ctx, tx, claim, workerID); err != nil {
				return err
			}
			assets, err := loadPublishedODSAssetsTx(ctx, tx)
			if err != nil {
				return err
			}
			sameDomain := dwdPlanningAssetsForInput(assets, input)
			assetsByVersion := map[string]dwdODSAsset{}
			assetsByDataset := map[string]dwdODSAsset{}
			for _, asset := range sameDomain {
				assetsByVersion[asset.VersionID] = asset
				assetsByDataset[asset.DatasetID] = asset
			}
			currentSnapshotHash, err := dwdPlanningSnapshotHash(sameDomain)
			if err != nil {
				return err
			}
			if currentSnapshotHash != expectedSnapshotHash {
				return errDWDModelingSubjectChange
			}
			for _, spec := range dwdDimensionSpecs(classifications) {
				source, exists := assetsByVersion[spec.Classification.DatasetVersionID]
				if !exists {
					return errDWDModelingSubjectChange
				}
				var dimDatasetID string
				err := tx.QueryRow(ctx, `SELECT dim_dataset_id::text
					FROM platform.dim_modeling_outputs
					WHERE source_dataset_id=$1::uuid
					  AND dimension_key=$2`,
					source.DatasetID, spec.MappingKey,
				).Scan(&dimDatasetID)
				if err != nil && !errors.Is(err, pgx.ErrNoRows) {
					return err
				}
				modeledSource := source
				if errors.Is(err, pgx.ErrNoRows) &&
					spec.MappingKey == "primary" {
					var canonicalSourceDatasetID string
					suppressionErr := tx.QueryRow(ctx, `SELECT
							canonical_source_dataset_id::text,
							canonical_dim_dataset_id::text
						FROM platform.dim_modeling_suppressions
						WHERE suppressed_source_dataset_id=$1::uuid`,
						source.DatasetID,
					).Scan(&canonicalSourceDatasetID, &dimDatasetID)
					if suppressionErr != nil &&
						!errors.Is(suppressionErr, pgx.ErrNoRows) {
						return suppressionErr
					}
					if suppressionErr == nil {
						canonicalSource, exists :=
							assetsByDataset[canonicalSourceDatasetID]
						if !exists {
							return errDWDModelingSubjectChange
						}
						modeledSource = canonicalSource
					}
				}
				modeled, published, err := loadCompatibleModeledDIMAssetTx(
					ctx, tx, dimDatasetID, modeledSource,
				)
				if err != nil {
					return err
				}
				if modeled == nil {
					pendingID := dimDatasetID
					if pendingID == "" {
						pendingID = source.DatasetID
					}
					stage.PendingPublicationDatasets = append(
						stage.PendingPublicationDatasets, pendingID,
					)
					continue
				}
				stage.AssetsBySourceVersion[spec.Identity] = *modeled
				if spec.MappingKey == "primary" {
					stage.DimensionVersionBySource[source.DatasetID] =
						modeled.VersionID
				}
				if !published {
					stage.PendingPublicationDatasets = append(
						stage.PendingPublicationDatasets, dimDatasetID,
					)
				}
			}
			return nil
		},
	)
	if err != nil {
		return dwdDimensionStageResult{}, err
	}
	sort.Strings(stage.PendingPublicationDatasets)
	stage.PendingPublicationDatasets = uniqueDWDStrings(
		stage.PendingPublicationDatasets,
	)
	stage.PendingPublicationCount = len(stage.PendingPublicationDatasets)
	stage.CheckpointScope, err = dwdDimensionStageScope(
		stage.AssetsBySourceVersion,
	)
	if err != nil {
		return dwdDimensionStageResult{}, err
	}
	return stage, nil
}

func (worker *DWDModelingWorker) finishWaitingForPublishedDIMs(
	ctx context.Context,
	claim dwdModelingClaim,
	workerID string,
	pendingCount int,
) error {
	return database.WithTenantTx(
		ctx, worker.store.pool, claim.TenantID, func(tx pgx.Tx) error {
			if err := validateDWDClaimTx(ctx, tx, claim, workerID); err != nil {
				return err
			}
			var generatedDWDCount int
			if err := tx.QueryRow(ctx, `SELECT count(*)
				FROM platform.dwd_modeling_outputs
				WHERE last_job_id=$1::uuid`, claim.ID).
				Scan(&generatedDWDCount); err != nil {
				return err
			}
			message := boundedDWDJobMessage(fmt.Sprintf(
				"已生成 %d 张 DWD 草稿；%d 张 DIM 草稿待人工审核发布，全部发布后系统将自动续接并绑定正式版本",
				generatedDWDCount, pendingCount,
			))
			tag, err := tx.Exec(ctx, `UPDATE platform.dwd_modeling_stage_jobs
				SET status='PARTIAL',
					generated_count=$1,
					error_code='DIM_PUBLICATION_REQUIRED',error_message=$2,
					lease_owner='',lease_token=NULL,lease_expires_at=NULL,
					completed_at=now(),updated_at=now()
				WHERE id=$3::uuid AND workflow_job_id=$4::uuid
				  AND status='RUNNING' AND lease_owner=$5
				  AND lease_token=$6::uuid AND attempt=$7`,
				generatedDWDCount, message, claim.StageJobID, claim.ID,
				workerID, claim.LeaseToken, claim.Attempt,
			)
			if err != nil {
				return err
			}
			if tag.RowsAffected() != 1 {
				return errDWDModelingLeaseLost
			}
			tag, err = tx.Exec(ctx, `UPDATE platform.dwd_modeling_jobs
				SET status='PARTIAL',generated_count=$1,
					error_code='DIM_PUBLICATION_REQUIRED',error_message=$2,
					lease_owner='',lease_token=NULL,lease_expires_at=NULL,
					completed_at=now(),updated_at=now()
				WHERE id=$3::uuid AND status='RUNNING'`,
				generatedDWDCount, message, claim.ID,
			)
			if err != nil {
				return err
			}
			if tag.RowsAffected() != 1 {
				return errDWDModelingSubjectChange
			}
			return nil
		},
	)
}

func (worker *DWDModelingWorker) finishDWDStageCompletion(
	ctx context.Context,
	claim dwdModelingClaim,
	workerID string,
	input dwdPlanningInput,
	completion dwdPlanningCompletion,
	status, errorCode, errorMessage string,
) error {
	roleByVersion := map[string]string{}
	for _, classification := range completion.Plan.Classifications {
		roleByVersion[classification.DatasetVersionID] = classification.Role
	}
	items := append(
		[]dwdModelingResultItem(nil), completion.DimensionStage.Items...,
	)
	return database.WithTenantTx(
		ctx, worker.store.pool, claim.TenantID, func(tx pgx.Tx) error {
			if err := validateDWDClaimTx(ctx, tx, claim, workerID); err != nil {
				return err
			}
			if claim.Stage == dwdStageDomainClassification &&
				status == "SUCCEEDED" {
				if err := upsertODSClassificationTagsTx(
					ctx, tx, claim, completion.Plan.Classifications,
				); err != nil {
					return err
				}
			}
			return finishDWDJobTx(
				ctx, tx, claim, workerID, completion.AIRequestID,
				input.Domain, roleByVersion[claim.TriggerDatasetVersionID],
				status, completion.DimensionStage.Created,
				completion.DimensionStage.Updated+
					completion.DimensionStage.Retired,
				completion.DimensionStage.Skipped,
				completion.CheckpointCount,
				completion.ReusedCheckpointCount,
				items, completion.Plan.Classifications,
				completion.DimensionStage,
				errorCode, boundedDWDJobMessage(errorMessage),
			)
		},
	)
}

func upsertODSClassificationTagsTx(
	ctx context.Context,
	tx pgx.Tx,
	claim dwdModelingClaim,
	classifications []dwdLLMClassification,
) error {
	type roleTag struct {
		Code      string
		Role      string
		Rationale string
	}
	items := make([]roleTag, 0, len(classifications))
	for _, classification := range classifications {
		fact := classification.Role == "FACT"
		dimension := classificationProducesDimension(classification)
		code, role := "system.function.ods_other", "OTHER"
		switch {
		case fact && dimension:
			code, role = "system.function.ods_fact_dimension", "FACT_DIMENSION"
		case fact:
			code, role = "system.function.ods_fact", "FACT"
		case dimension:
			code, role = "system.function.ods_dimension", "DIMENSION"
		}
		items = append(items, roleTag{
			Code: code, Role: role,
			Rationale: boundedDWDJobMessage(classification.Rationale),
		})
	}
	for index, item := range items {
		classification := classifications[index]
		if _, err := tx.Exec(ctx, `DELETE FROM platform.asset_tag_bindings AS binding
			USING platform.semantic_tags AS tag
			WHERE binding.tag_id=tag.id
			  AND binding.asset_type='DATASET_VERSION'
			  AND binding.dataset_version_id=$1::uuid
			  AND binding.origin='LLM'
			  AND binding.status='SUGGESTED'
			  AND tag.code::text=ANY($2::text[])`,
			classification.DatasetVersionID,
			[]string{
				"system.function.ods_fact",
				"system.function.ods_dimension",
				"system.function.ods_fact_dimension",
				"system.function.ods_other",
			},
		); err != nil {
			return err
		}
		evidence, err := json.Marshal(map[string]any{
			"dwdModelingJobId":          claim.ID,
			"dwdModelingStageJobId":     claim.StageJobID,
			"sourceDatasetVersionId":    classification.DatasetVersionID,
			"promptVersion":             dwdClassificationPromptVersion,
			"classifiedRole":            item.Role,
			"rationale":                 item.Rationale,
			"containsBusinessSamples":   false,
			"decisionPoint":             "ODS_ROLE_CLASSIFICATION",
			"classificationIsVersioned": true,
		})
		if err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `INSERT INTO platform.asset_tag_bindings(
				tenant_id,tag_id,asset_type,dataset_id,dataset_version_id,
				origin,status,confidence,evidence_json,assigned_by
			)
			SELECT
				platform.current_tenant_id(),tag.id,'DATASET_VERSION',
				version.dataset_id,version.id,
				'LLM','SUGGESTED',1.0,$3::jsonb,$4::uuid
			FROM platform.dataset_versions AS version
			JOIN platform.semantic_tags AS tag
			  ON tag.tenant_id=version.tenant_id
			 AND tag.code::text=$2
			 AND tag.category='TABLE_FUNCTION'
			 AND tag.governance='CONTROLLED'
			 AND tag.status='ACTIVE'
			WHERE version.id=$1::uuid
			  AND version.layer='ODS'
			  AND version.status='PUBLISHED'
			ON CONFLICT(
				tenant_id,tag_id,dataset_version_id
			) WHERE asset_type='DATASET_VERSION'
			DO UPDATE SET
				status='SUGGESTED',
				confidence=EXCLUDED.confidence,
				evidence_json=EXCLUDED.evidence_json,
				assigned_by=EXCLUDED.assigned_by,
				approved_by=NULL,approved_at=NULL
			WHERE asset_tag_bindings.origin='LLM'
			  AND asset_tag_bindings.status IN ('SUGGESTED','REJECTED')`,
			classification.DatasetVersionID, item.Code, evidence, claim.ActorID,
		)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return fmt.Errorf(
				"%w: controlled ODS role tag %s is unavailable",
				errDWDModelingInvalid, item.Code,
			)
		}
	}
	return nil
}

func runBoundedDWDTasks[T any](
	ctx context.Context,
	limit int,
	tasks []func(context.Context) (T, error),
) ([]T, error) {
	results := make([]T, len(tasks))
	if len(tasks) == 0 {
		return results, nil
	}
	if limit < 1 {
		limit = 1
	}
	if limit > len(tasks) {
		limit = len(tasks)
	}
	taskCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	indices := make(chan int, len(tasks))
	for index := range tasks {
		indices <- index
	}
	close(indices)
	var (
		firstErr error
		errOnce  sync.Once
		group    sync.WaitGroup
	)
	group.Add(limit)
	for range limit {
		go func() {
			defer group.Done()
			for index := range indices {
				if taskCtx.Err() != nil {
					return
				}
				result, err := tasks[index](taskCtx)
				if err != nil {
					errOnce.Do(func() {
						firstErr = err
						cancel()
					})
					return
				}
				results[index] = result
			}
		}()
	}
	group.Wait()
	return results, firstErr
}

func runBoundedDWDTasksCollect[T any](
	ctx context.Context,
	limit int,
	tasks []func(context.Context) (T, error),
) ([]T, []error) {
	results := make([]T, len(tasks))
	taskErrors := make([]error, len(tasks))
	if len(tasks) == 0 {
		return results, taskErrors
	}
	if limit < 1 {
		limit = 1
	}
	if limit > len(tasks) {
		limit = len(tasks)
	}
	indices := make(chan int, len(tasks))
	for index := range tasks {
		indices <- index
	}
	close(indices)
	var group sync.WaitGroup
	group.Add(limit)
	for range limit {
		go func() {
			defer group.Done()
			for index := range indices {
				if ctx.Err() != nil {
					taskErrors[index] = ctx.Err()
					continue
				}
				results[index], taskErrors[index] = tasks[index](ctx)
			}
		}()
	}
	group.Wait()
	return results, taskErrors
}

func (worker *DWDModelingWorker) renewDWDClaim(
	ctx context.Context,
	claim dwdModelingClaim,
	workerID string,
	lease time.Duration,
) error {
	return database.WithTenantTx(
		ctx, worker.store.pool, claim.TenantID, func(tx pgx.Tx) error {
			tag, err := tx.Exec(ctx, `UPDATE platform.dwd_modeling_stage_jobs SET
				lease_expires_at=now()+($1*interval '1 second'),
				updated_at=now()
				WHERE id=$2::uuid AND status='RUNNING'
				  AND lease_owner=$3 AND lease_token=$4::uuid
				  AND attempt=$5 AND lease_expires_at>now()`,
				int64(lease/time.Second), claim.StageJobID, workerID,
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
	merged, found, err := worker.loadDWDClassificationMergeCheckpoint(
		ctx, claim, workerID, input, snapshotHash,
	)
	if err != nil || found {
		return merged, found, err
	}
	completion := dwdClassificationCompletion{Domain: input.Domain}
	foundCount := 0
	for _, table := range input.Tables {
		scoped, err := dwdSingleTableClassificationScope(
			input, table.VersionID,
		)
		if err != nil {
			return dwdClassificationCompletion{}, false, err
		}
		item, found, err := worker.loadDWDSingleClassificationCheckpoint(
			ctx, claim, workerID, scoped, snapshotHash, table.VersionID,
		)
		if err != nil {
			return dwdClassificationCompletion{}, false, err
		}
		if !found {
			continue
		}
		foundCount++
		if item.Domain != input.Domain || len(item.Classifications) != 1 {
			return dwdClassificationCompletion{}, false,
				errDWDModelingInvalid
		}
		completion.AIRequestID = item.AIRequestID
		completion.Classifications = append(
			completion.Classifications, item.Classifications[0],
		)
	}
	if foundCount == 0 {
		return worker.loadDWDLegacyClassificationCheckpoint(
			ctx, claim, workerID, input, snapshotHash,
		)
	}
	if foundCount != len(input.Tables) {
		return dwdClassificationCompletion{}, false, nil
	}
	completion.Classifications = canonicalizeMergedDWDClassifications(
		input, completion.Classifications,
	)
	if err := validateDWDLLMClassifications(
		input, completion.Domain, completion.Classifications,
	); err != nil {
		return dwdClassificationCompletion{}, false, err
	}
	return completion, true, nil
}

func (worker *DWDModelingWorker) loadDWDClassificationMergeCheckpoint(
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
				  AND checkpoint_kind='CLASSIFICATION_MERGE'
				  AND subject_dataset_version_id=$2::uuid
				  AND snapshot_hash=$3 AND prompt_version=$4`,
				claim.ID, claim.TriggerDatasetVersionID, snapshotHash,
				dwdClassificationMergeVersion,
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
			authority, complete, err :=
				loadDWDClassificationAuthorityTx(
					ctx, tx, claim, input, snapshotHash,
				)
			if err != nil {
				return err
			}
			if complete {
				payload.Classifications =
					canonicalizeMergedDWDClassifications(
						input, payload.Classifications, authority,
					)
			} else {
				payload.Classifications =
					canonicalizeMergedDWDClassifications(
						input, payload.Classifications,
					)
			}
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

func loadDWDClassificationAuthorityTx(
	ctx context.Context,
	tx pgx.Tx,
	claim dwdModelingClaim,
	input dwdPlanningInput,
	snapshotHash string,
) ([]dwdLLMClassification, bool, error) {
	authority := make(
		[]dwdLLMClassification, 0, len(input.Tables),
	)
	for _, table := range input.Tables {
		var raw json.RawMessage
		var payloadHash string
		err := tx.QueryRow(ctx, `SELECT payload_json,payload_hash
			FROM platform.dwd_modeling_checkpoints
			WHERE job_id=$1::uuid
			  AND checkpoint_kind='CLASSIFICATION'
			  AND subject_dataset_version_id=$2::uuid
			  AND snapshot_hash=$3 AND prompt_version=$4`,
			claim.ID, table.VersionID, snapshotHash,
			dwdClassificationPromptVersion,
		).Scan(&raw, &payloadHash)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, false, nil
		}
		if err != nil {
			return nil, false, err
		}
		if err := validateDWDCheckpointHash(raw, payloadHash); err != nil {
			return nil, false, err
		}
		payload, err := decodeDWDClassificationPlan(raw)
		if err != nil {
			return nil, false, err
		}
		scoped, err := dwdSingleTableClassificationScope(
			input, table.VersionID,
		)
		if err != nil {
			return nil, false, err
		}
		payload.Classifications = normalizeDWDClassifications(
			scoped, payload.Classifications,
		)
		if err := validateDWDLLMClassifications(
			scoped, payload.Domain, payload.Classifications,
		); err != nil {
			return nil, false, err
		}
		authority = append(authority, payload.Classifications[0])
	}
	authority = normalizeDWDClassifications(input, authority)
	if err := validateDWDLLMClassifications(
		input, input.Domain, authority,
	); err != nil {
		return nil, false, err
	}
	return authority, true, nil
}

func (worker *DWDModelingWorker) loadDWDLegacyClassificationCheckpoint(
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
				dwdLegacyClassificationVersion,
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
			payload.Classifications = canonicalizeMergedDWDClassifications(
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

func (worker *DWDModelingWorker) loadDWDSingleClassificationCheckpoint(
	ctx context.Context,
	claim dwdModelingClaim,
	workerID string,
	input dwdPlanningInput,
	snapshotHash, subjectVersionID string,
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
				claim.ID, subjectVersionID, snapshotHash,
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

func (worker *DWDModelingWorker) loadDWDDimensionCheckpoint(
	ctx context.Context,
	claim dwdModelingClaim,
	workerID string,
	input dwdPlanningInput,
	snapshotHash string,
	classifications []dwdLLMClassification,
	dimensionIdentity string,
) (dwdDimensionDesignCompletion, bool, error) {
	spec, exists := dwdDimensionSpecByIdentity(
		classifications, dimensionIdentity,
	)
	if !exists {
		return dwdDimensionDesignCompletion{}, false, errDWDModelingInvalid
	}
	sourceVersionID := spec.Classification.DatasetVersionID
	promptVersion := dwdDimensionCheckpointPromptVersion(spec)
	var completion dwdDimensionDesignCompletion
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
				  AND checkpoint_kind='DIM_DESIGN'
				  AND subject_dataset_version_id=$2::uuid
				  AND snapshot_hash=$3 AND prompt_version=$4`,
				claim.ID, sourceVersionID, snapshotHash,
				promptVersion,
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
			payload, err := decodeDWDDimensionDesign(raw)
			if err != nil {
				return err
			}
			table, _, err := dwdDimensionPlanningScope(
				input, classifications, dimensionIdentity,
			)
			if err != nil {
				return err
			}
			completion.Output, err = normalizeDWDDimensionDesign(
				table, payload.Output,
			)
			completion.Output.DimensionIdentity = dimensionIdentity
			return err
		},
	)
	return completion, found, err
}

func dwdDimensionCheckpointPromptVersion(spec dwdDimensionSpec) string {
	if spec.MappingKey == "" || spec.MappingKey == "primary" {
		return dwdDimensionDesignPromptVersion
	}
	sum := sha256.Sum256([]byte(spec.MappingKey))
	return dwdDimensionDesignPromptVersion + "-" +
		hex.EncodeToString(sum[:4])
}

func (worker *DWDModelingWorker) loadDWDFactCheckpoint(
	ctx context.Context,
	claim dwdModelingClaim,
	workerID string,
	input dwdPlanningInput,
	snapshotHash string,
	classifications []dwdLLMClassification,
	factVersionID string,
	dimensionScope string,
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
				dwdFactCheckpointPromptVersion(dimensionScope),
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
			normalized, err := normalizeDWDFactCheckpoint(
				input, classifications, factVersionID, payload.Output,
			)
			if err != nil {
				return err
			}
			completion.Output = normalized
			return nil
		},
	)
	return completion, found, err
}

func dwdFactCheckpointPromptVersion(dimensionScope string) string {
	dimensionScope = strings.TrimSpace(dimensionScope)
	if len(dimensionScope) > 12 {
		dimensionScope = dimensionScope[:12]
	}
	if dimensionScope == "" {
		return dwdFactDesignPromptVersion
	}
	return dwdFactDesignPromptVersion + "-" + dimensionScope
}

func validateDWDFactCheckpoint(
	input dwdPlanningInput,
	classifications []dwdLLMClassification,
	factVersionID string,
	output dwdLLMOutput,
) error {
	_, err := normalizeDWDFactCheckpoint(
		input, classifications, factVersionID, output,
	)
	return err
}

func normalizeDWDFactCheckpoint(
	input dwdPlanningInput,
	classifications []dwdLLMClassification,
	factVersionID string,
	output dwdLLMOutput,
) (dwdLLMOutput, error) {
	input.FactLookupAssociations = dwdFactAssociationMap(
		output.FactAssociations,
	)
	scoped, scopedClassifications, err := dwdFactPlanningScope(
		input, classifications, factVersionID,
	)
	if err != nil {
		return dwdLLMOutput{}, err
	}
	plan := dwdLLMPlan{
		Domain:          scoped.Domain,
		Classifications: scopedClassifications,
		Outputs:         []dwdLLMOutput{output},
	}
	plan = completeDWDFactAssociations(scoped, plan)
	plan = normalizeDWDSafeJoinAssociations(scoped, plan)
	plan = completeDWDOutputContract(scoped, plan)
	plan = normalizeDWDJoinOutputProjection(plan)
	plan = completeMandatoryDWDPolicyCleaning(scoped, plan)
	plan = dropInvalidDWDProcessing(scoped, plan)
	if err := validateDWDLLMPlan(scoped, plan); err != nil {
		return dwdLLMOutput{}, err
	}
	plan.Outputs[0].FactAssociations = append(
		[]dwdFactAssociation(nil), output.FactAssociations...,
	)
	return plan.Outputs[0], nil
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
				ctx, tx, input, snapshotHash,
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
				  AND EXISTS(
				    SELECT 1
				    FROM platform.dwd_modeling_stage_jobs AS stage
				    WHERE stage.id=$2::uuid
				      AND stage.workflow_job_id=$1::uuid
				      AND stage.status='RUNNING'
				      AND stage.lease_owner=$3
				      AND stage.lease_token=$4::uuid
				      AND stage.attempt=$5
				      AND stage.lease_expires_at>now()
				  )`,
				claim.ID, claim.StageJobID, workerID,
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
	input dwdPlanningInput,
	expectedSnapshotHash string,
) error {
	assets, err := loadPublishedODSAssetsTx(ctx, tx)
	if err != nil {
		return err
	}
	sameDomain := dwdPlanningAssetsForInput(assets, input)
	currentSnapshotHash, err := dwdPlanningSnapshotHash(sameDomain)
	if err != nil {
		return err
	}
	if currentSnapshotHash != expectedSnapshotHash {
		return errDWDModelingSubjectChange
	}
	return nil
}

func dwdPlanningAssetsForInput(
	assets []dwdODSAsset,
	input dwdPlanningInput,
) []dwdODSAsset {
	versions := make(map[string]bool, len(input.Tables))
	for _, table := range input.Tables {
		versions[table.VersionID] = true
	}
	result := make([]dwdODSAsset, 0, len(versions))
	for _, asset := range assets {
		if asset.DomainID == input.DomainID && versions[asset.VersionID] {
			result = append(result, asset)
		}
	}
	return result
}

func scopeDWDFactOutputs(
	input dwdPlanningInput,
	factVersions []string,
	unchanged []dwdHistoricalOutput,
	selectedDatasetIDs []string,
	selected bool,
) ([]string, []dwdHistoricalOutput) {
	if !selected {
		return factVersions, unchanged
	}
	allowedDatasets := make(map[string]bool, len(selectedDatasetIDs))
	for _, datasetID := range selectedDatasetIDs {
		allowedDatasets[datasetID] = true
	}
	versionDataset := make(map[string]string, len(input.Tables))
	for _, table := range input.Tables {
		versionDataset[table.VersionID] = table.DatasetID
	}
	scopedVersions := make([]string, 0, len(factVersions))
	for _, versionID := range factVersions {
		if allowedDatasets[versionDataset[versionID]] {
			scopedVersions = append(scopedVersions, versionID)
		}
	}
	scopedUnchanged := make([]dwdHistoricalOutput, 0, len(unchanged))
	for _, output := range unchanged {
		if allowedDatasets[output.FactDatasetID] {
			scopedUnchanged = append(scopedUnchanged, output)
		}
	}
	return scopedVersions, scopedUnchanged
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
		if trigger.DomainID == "" || strings.TrimSpace(trigger.DomainName) == "" {
			return errDWDModelingSubjectChange
		}
		domain := strings.TrimSpace(trigger.DomainName)
		selected := make(map[string]bool, len(claim.SourceDatasetIDs))
		for _, datasetID := range claim.SourceDatasetIDs {
			selected[datasetID] = true
		}
		sameDomain := make([]dwdODSAsset, 0, len(assets))
		for _, asset := range assets {
			if asset.DomainID == trigger.DomainID &&
				(len(selected) == 0 || selected[asset.DatasetID]) {
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
			ResourceID: claim.TriggerDatasetVersionID,
			DomainID:   trigger.DomainID, Domain: domain,
			FactScopeSelected: claim.FactScopeSelected,
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
				Tags:            append([]string(nil), asset.Tags...),
				OutputGrain:     asset.Document.OutputGrain,
				Fields:          make([]dwdPlanningField, 0, len(asset.Document.Fields)),
				SourceCode:      asset.Code,
				SourceTableName: asset.SourceTableName,
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
		input.History, err = loadDWDPlanningHistoryTx(ctx, tx, domain)
		if err != nil {
			return err
		}
		snapshotHash, err = dwdPlanningSnapshotHash(sameDomain)
		return err
	})
	return input, snapshotHash, err
}

func loadDWDPlanningHistoryTx(
	ctx context.Context,
	tx pgx.Tx,
	domain string,
) (dwdPlanningHistory, error) {
	history := dwdPlanningHistory{
		OutputsByFactDataset:   map[string]dwdHistoricalOutput{},
		DomainVersionByDataset: map[string]string{},
	}
	versionDataset := map[string]string{}
	rows, err := tx.Query(ctx, `SELECT id::text,dataset_id::text
		FROM platform.dataset_versions`)
	if err != nil {
		return dwdPlanningHistory{}, err
	}
	for rows.Next() {
		var versionID, datasetID string
		if err := rows.Scan(&versionID, &datasetID); err != nil {
			rows.Close()
			return dwdPlanningHistory{}, err
		}
		versionDataset[versionID] = datasetID
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return dwdPlanningHistory{}, err
	}
	rows.Close()

	type publishedDIMSource struct {
		SourceDatasetID string
		SourceVersionID string
	}
	dimSourceByDataset := map[string]publishedDIMSource{}
	rows, err = tx.Query(ctx, `SELECT output.source_dataset_id::text,
			output.dim_dataset_id::text,version.dsl_json
		FROM platform.dim_modeling_outputs AS output
		JOIN platform.datasets AS dataset
		  ON dataset.id=output.dim_dataset_id
		 AND dataset.tenant_id=output.tenant_id
		 AND dataset.deleted_at IS NULL
		JOIN platform.dataset_versions AS version
		  ON version.id=dataset.current_published_version_id
		 AND version.dataset_id=dataset.id
		 AND version.tenant_id=dataset.tenant_id
		 AND version.status='PUBLISHED'
		 AND version.layer='DIM'
		WHERE output.domain_key=$1`, domain)
	if err != nil {
		return dwdPlanningHistory{}, err
	}
	for rows.Next() {
		var sourceDatasetID, dimDatasetID string
		var raw json.RawMessage
		if err := rows.Scan(
			&sourceDatasetID, &dimDatasetID, &raw,
		); err != nil {
			rows.Close()
			return dwdPlanningHistory{}, err
		}
		document, err := DecodeAndNormalize(raw)
		if err != nil {
			rows.Close()
			return dwdPlanningHistory{}, err
		}
		for _, node := range document.Nodes {
			if versionDataset[node.DatasetVersionID] != sourceDatasetID {
				continue
			}
			dimSourceByDataset[dimDatasetID] = publishedDIMSource{
				SourceDatasetID: sourceDatasetID,
				SourceVersionID: node.DatasetVersionID,
			}
			break
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return dwdPlanningHistory{}, err
	}
	rows.Close()
	suppressedSourcesByDIM := map[string][]publishedDIMSource{}
	rows, err = tx.Query(ctx, `SELECT
			suppression.canonical_dim_dataset_id::text,
			suppression.suppressed_source_dataset_id::text,
			source.current_published_version_id::text
		FROM platform.dim_modeling_suppressions AS suppression
		JOIN platform.dim_modeling_outputs AS canonical_output
		  ON canonical_output.dim_dataset_id=
		       suppression.canonical_dim_dataset_id
		 AND canonical_output.source_dataset_id=
		       suppression.canonical_source_dataset_id
		 AND canonical_output.tenant_id=suppression.tenant_id
		JOIN platform.datasets AS source
		  ON source.id=suppression.suppressed_source_dataset_id
		 AND source.tenant_id=suppression.tenant_id
		 AND source.deleted_at IS NULL
		WHERE canonical_output.domain_key=$1
		  AND source.current_published_version_id IS NOT NULL`,
		domain,
	)
	if err != nil {
		return dwdPlanningHistory{}, err
	}
	for rows.Next() {
		var dimDatasetID string
		var source publishedDIMSource
		if err := rows.Scan(
			&dimDatasetID, &source.SourceDatasetID,
			&source.SourceVersionID,
		); err != nil {
			rows.Close()
			return dwdPlanningHistory{}, err
		}
		suppressedSourcesByDIM[dimDatasetID] = append(
			suppressedSourcesByDIM[dimDatasetID], source,
		)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return dwdPlanningHistory{}, err
	}
	rows.Close()

	var classificationRaw json.RawMessage
	err = tx.QueryRow(ctx, `SELECT checkpoint.payload_json
		FROM platform.dwd_modeling_checkpoints AS checkpoint
		JOIN platform.dwd_modeling_jobs AS job
		  ON job.id=checkpoint.job_id
		 AND job.tenant_id=checkpoint.tenant_id
		WHERE checkpoint.checkpoint_kind='CLASSIFICATION'
		  AND checkpoint.payload_json->>'domain'=$1
		ORDER BY checkpoint.created_at DESC
		LIMIT 1`, domain).Scan(&classificationRaw)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return dwdPlanningHistory{}, err
	}
	if err == nil {
		classification, decodeErr := decodeDWDClassificationPlan(classificationRaw)
		if decodeErr != nil {
			return dwdPlanningHistory{}, decodeErr
		}
		for _, item := range classification.Classifications {
			if datasetID := versionDataset[item.DatasetVersionID]; datasetID != "" {
				history.DomainVersionByDataset[datasetID] = item.DatasetVersionID
			}
		}
	}

	rows, err = tx.Query(ctx, `SELECT output.fact_dataset_id::text,
			output.dwd_dataset_id::text,dataset.code,version.dsl_json
		FROM platform.dwd_modeling_outputs AS output
		JOIN platform.datasets AS dataset
		  ON dataset.id=output.dwd_dataset_id
		 AND dataset.tenant_id=output.tenant_id
		 AND dataset.deleted_at IS NULL
		JOIN platform.dataset_versions AS version
		  ON version.id=COALESCE(
		       dataset.current_draft_version_id,
		       dataset.current_published_version_id
		     )
		 AND version.dataset_id=dataset.id
		 AND version.tenant_id=dataset.tenant_id
		WHERE output.domain_key=$1
		ORDER BY output.fact_dataset_id`, domain)
	if err != nil {
		return dwdPlanningHistory{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var factDatasetID, dwdDatasetID, dwdDatasetCode string
		var raw json.RawMessage
		if err := rows.Scan(
			&factDatasetID, &dwdDatasetID, &dwdDatasetCode, &raw,
		); err != nil {
			return dwdPlanningHistory{}, err
		}
		document, err := DecodeAndNormalize(raw)
		if err != nil {
			return dwdPlanningHistory{}, err
		}
		item := dwdHistoricalOutput{
			FactDatasetID:                   factDatasetID,
			DWDDatasetID:                    dwdDatasetID,
			DWDDatasetCode:                  dwdDatasetCode,
			SourceVersionByDataset:          map[string]string{},
			DimensionVersionBySourceDataset: map[string]string{},
		}
		for _, node := range document.Nodes {
			datasetID := versionDataset[node.DatasetVersionID]
			if source, generatedDIM := dimSourceByDataset[datasetID]; generatedDIM {
				item.SourceVersionByDataset[source.SourceDatasetID] =
					source.SourceVersionID
				item.DimensionVersionBySourceDataset[source.SourceDatasetID] =
					node.DatasetVersionID
				for _, suppressed := range suppressedSourcesByDIM[datasetID] {
					item.SourceVersionByDataset[suppressed.SourceDatasetID] =
						suppressed.SourceVersionID
					item.DimensionVersionBySourceDataset[suppressed.SourceDatasetID] = node.DatasetVersionID
				}
				continue
			}
			if datasetID != "" {
				item.SourceVersionByDataset[datasetID] = node.DatasetVersionID
			}
		}
		history.OutputsByFactDataset[factDatasetID] = item
	}
	return history, rows.Err()
}

func dwdPlanningSnapshotHash(assets []dwdODSAsset) (string, error) {
	type snapshotItem struct {
		DatasetID   string   `json:"datasetId"`
		VersionID   string   `json:"versionId"`
		DomainID    string   `json:"domainId"`
		SchemaHash  string   `json:"schemaHash"`
		Code        string   `json:"code"`
		Name        string   `json:"name"`
		Description string   `json:"description"`
		Tags        []string `json:"tags"`
	}
	items := make([]snapshotItem, 0, len(assets))
	for _, asset := range assets {
		tags := append([]string(nil), asset.Tags...)
		sort.Strings(tags)
		items = append(items, snapshotItem{
			DatasetID: asset.DatasetID, VersionID: asset.VersionID,
			DomainID:   asset.DomainID,
			SchemaHash: asset.SchemaHash, Code: asset.Code,
			Name: asset.Name, Description: asset.Description,
			Tags: tags,
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
	finalize bool,
) error {
	return database.WithTenantTx(ctx, worker.store.pool, claim.TenantID, func(tx pgx.Tx) error {
		if err := validateDWDClaimTx(ctx, tx, claim, workerID); err != nil {
			return err
		}
		assets, err := loadPublishedODSAssetsTx(ctx, tx)
		if err != nil {
			return err
		}
		sameDomain := dwdPlanningAssetsForInput(assets, input)
		currentSnapshotHash, err := dwdPlanningSnapshotHash(sameDomain)
		if err != nil {
			return err
		}
		if currentSnapshotHash != expectedSnapshotHash {
			return errDWDModelingSubjectChange
		}
		validationInput := input
		validationPlan := completion.Plan
		if completion.FactPlanningInput != nil {
			validationInput = *completion.FactPlanningInput
			validationPlan.Classifications =
				completion.FactClassifications
		}
		if err := validateDWDPartialLLMPlan(
			validationInput, validationPlan,
		); err != nil {
			return err
		}
		assetsByVersion := map[string]dwdODSAsset{}
		assetsByDataset := map[string]dwdODSAsset{}
		for _, asset := range sameDomain {
			assetsByVersion[asset.VersionID] = asset
			assetsByDataset[asset.DatasetID] = asset
		}
		roleByVersion := map[string]string{}
		for _, classification := range completion.Plan.Classifications {
			roleByVersion[classification.DatasetVersionID] = classification.Role
		}
		triggerRole := roleByVersion[claim.TriggerDatasetVersionID]
		items := append(
			[]dwdModelingResultItem(nil), completion.DimensionStage.Items...,
		)
		created := completion.DimensionStage.Created
		updated := completion.DimensionStage.Updated +
			completion.DimensionStage.Retired
		skipped := completion.DimensionStage.Skipped
		for _, historical := range completion.UnchangedOutputs {
			fact, exists := assetsByDataset[historical.FactDatasetID]
			if !exists {
				return errDWDModelingSubjectChange
			}
			if _, err := tx.Exec(ctx, `UPDATE platform.dwd_modeling_outputs SET
					last_job_id=$1,last_action='UNCHANGED'
				WHERE fact_dataset_id=$2::uuid`,
				claim.ID, historical.FactDatasetID,
			); err != nil {
				return err
			}
			items = append(items, dwdModelingResultItem{
				Layer:         string(LayerDWD),
				DatasetID:     historical.DWDDatasetID,
				FactDatasetID: historical.FactDatasetID,
				FactVersionID: fact.VersionID,
				DWDDatasetID:  historical.DWDDatasetID,
				Action:        "UNCHANGED",
				Reason:        "SOURCE_COMPOSITION_UNCHANGED",
			})
		}
		for _, failure := range completion.FactFailures {
			fact, exists := assetsByVersion[failure.FactDatasetVersionID]
			if !exists {
				return errDWDModelingSubjectChange
			}
			skipped++
			items = append(items, dwdModelingResultItem{
				Layer:         string(LayerDWD),
				FactDatasetID: fact.DatasetID,
				FactVersionID: fact.VersionID,
				Action:        "SKIPPED",
				Reason:        failure.ErrorCode,
			})
		}
		// Non-resumable planners retain the legacy all-at-once path. The durable
		// planner has already committed DIM drafts in stage two before any fact
		// design begins, so it must not repeat those writes here.
		if !completion.DimensionStage.Prepared {
			for _, classification := range completion.Plan.Classifications {
				if !classificationProducesDimension(classification) {
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
					ctx, tx, claim, source, "primary",
					input.Domain, inputHash, prepared,
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
		}

		for _, output := range completion.Plan.Outputs {
			fact, exists := assetsByVersion[output.FactDatasetVersionID]
			if !exists {
				return errDWDModelingSubjectChange
			}
			document, inputHash, buildErr := buildLLMDesignedDWDDocument(
				input.Domain, fact, assetsByVersion,
				completion.DimensionStage.AssetsBySourceVersion, output,
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
		if !finalize {
			// DIM publication is a governance boundary, not a design boundary.
			// The DWD draft has been persisted against exact DIM draft versions;
			// keep the task pending so a later pass can rebind those nodes to
			// exact published DIM versions before declaring the workflow done.
			return nil
		}
		status, errorCode, errorMessage := "SUCCEEDED", "", ""
		if completion.DimensionStage.FailedDesignCount > 0 {
			status = "PARTIAL"
			errorCode = "DIM_STANDARDIZATION_INCOMPLETE"
			detail := ""
			for _, item := range completion.DimensionStage.Items {
				if item.Layer == string(LayerDIM) &&
					item.ErrorMessage != "" {
					detail = "；" + item.ErrorMessage
					break
				}
			}
			errorMessage = fmt.Sprintf(
				"领域分类已完成，但 %d 张维度源未通过角色/字段合同校验或未形成有效 DIM 草稿；事实表设计已暂停，请查看失败明细后重试%s",
				completion.DimensionStage.FailedDesignCount,
				detail,
			)
		} else if completion.DimensionStage.PendingPublicationCount > 0 {
			status = "PARTIAL"
			errorCode = "DIM_PUBLICATION_REQUIRED"
			errorMessage = fmt.Sprintf(
				"已完成领域分类和维度加工；%d 张 DIM 尚未发布。请发布 DIM 后在任务运行中心重试，系统将继续事实表设计",
				completion.DimensionStage.PendingPublicationCount,
			)
		} else if len(items) == 0 {
			status = "SKIPPED"
			errorCode = "NO_MODELABLE_TABLE"
			errorMessage = "LLM 判断同领域内暂时没有可建模的事实、维度或主数据表"
		} else if skipped > 0 {
			status = "PARTIAL"
			errorCode = "SOME_LAYER_DESIGNS_SKIPPED"
			errorMessage = "部分 DIM/DWD 方案未通过业务关联或 DSL 校验，其余模型已继续完成"
		}
		errorMessage = boundedDWDJobMessage(errorMessage)
		if err := finishDWDJobTx(
			ctx, tx, claim, workerID, completion.AIRequestID,
			input.Domain, triggerRole, status,
			created, updated, skipped,
			completion.CheckpointCount, completion.ReusedCheckpointCount,
			items, completion.Plan.Classifications,
			completion.DimensionStage, errorCode, errorMessage,
		); err != nil {
			return err
		}
		return nil
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
			"reasonCode":         "DOMAIN_PLAN_COALESCED",
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

func businessModeledDatasetCode(
	layer Layer,
	domain string,
	source dwdODSAsset,
	grainKeys []string,
) (string, error) {
	candidates := make([]string, 0, len(grainKeys)+2)
	if layer == LayerDIM {
		candidates = append(candidates, grainKeys...)
		candidates = append(candidates, source.SourceTableName)
	} else {
		candidates = append(candidates, source.SourceTableName)
		candidates = append(candidates, grainKeys...)
	}
	for _, field := range source.Document.Fields {
		if strings.EqualFold(field.Role, "IDENTIFIER") {
			candidates = append(candidates, field.Code)
		}
	}
	if !looksSyntheticDatasetCode(source.Code) {
		candidates = append(candidates, source.Code)
	}
	return businessModeledDatasetCodeFromCandidates(
		layer, domain, source.Tags, candidates,
	)
}

func businessModeledDatasetCodeForPlanningTable(
	layer Layer,
	domain string,
	source dwdPlanningTable,
) (string, error) {
	candidates := []string{source.SourceTableName}
	candidates = append(candidates, source.OutputGrain.KeyFields...)
	for _, field := range source.Fields {
		if strings.EqualFold(field.Role, "IDENTIFIER") {
			candidates = append(candidates, field.Code)
		}
	}
	if !looksSyntheticDatasetCode(source.SourceCode) {
		candidates = append(candidates, source.SourceCode)
	}
	return businessModeledDatasetCodeFromCandidates(
		layer, domain, source.Tags, candidates,
	)
}

func businessModeledDatasetCodeFromCandidates(
	layer Layer,
	domain string,
	tags []string,
	candidates []string,
) (string, error) {
	for _, candidate := range candidates {
		base := normalizeBusinessIdentifier(candidate)
		base = trimWarehouseCodePrefix(base)
		base = strings.TrimSuffix(base, "_id")
		base = strings.TrimSuffix(base, "_key")
		base = strings.Trim(base, "_")
		if base == "" || base == "entity" || base == "table" {
			continue
		}
		code, err := modeledDatasetPhysicalCode(
			layer, domain, tags, base,
		)
		if err == nil {
			return code, nil
		}
	}
	return "", fmt.Errorf(
		"%w: business model code cannot be derived from source table or entity key",
		errDWDModelingInvalid,
	)
}

func normalizeBusinessIdentifier(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	builder := strings.Builder{}
	separator := false
	for _, character := range value {
		if (character >= 'a' && character <= 'z') ||
			(character >= '0' && character <= '9') {
			if separator && builder.Len() > 0 {
				builder.WriteByte('_')
			}
			builder.WriteRune(character)
			separator = false
			continue
		}
		separator = true
	}
	return strings.Trim(builder.String(), "_")
}

func trimWarehouseCodePrefix(value string) string {
	for {
		trimmed := value
		for _, prefix := range []string{
			"ods_", "dim_", "dwd_", "dws_", "ads_", "fact_", "fct_",
			"agg_", "master_", "mst_", "ref_", "mapped_", "t_",
		} {
			trimmed = strings.TrimPrefix(trimmed, prefix)
		}
		if trimmed == value {
			return value
		}
		value = trimmed
	}
}

func looksSyntheticDatasetCode(value string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	if !strings.HasPrefix(value, "mapped_") &&
		!strings.HasPrefix(value, "dim_auto_") &&
		!strings.HasPrefix(value, "dwd_auto_") {
		return false
	}
	suffix := value[strings.IndexByte(value, '_')+1:]
	suffix = strings.TrimPrefix(suffix, "auto_")
	if len(suffix) < 16 {
		return false
	}
	for _, character := range suffix {
		if (character < '0' || character > '9') &&
			(character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

// buildLLMClassifiedDIMDocument is retained for the non-resumable compatibility
// path and deterministic tests. Production uses buildLLMDesignedDIMDocument
// after the dedicated LLM dimension-design checkpoint.
func buildLLMClassifiedDIMDocument(
	domain string,
	source dwdODSAsset,
) (Document, string, error) {
	fields := make(
		[]dwdLLMDimensionFieldDesign, 0, len(source.Document.Fields),
	)
	for _, sourceField := range source.Document.Fields {
		description := strings.TrimSpace(sourceField.Description)
		if description == "" {
			description = standardizedDIMFieldDescription(
				source.Name, sourceField,
			)
		}
		fields = append(fields, dwdLLMDimensionFieldDesign{
			SourceFieldCode:   sourceField.Code,
			OutputName:        sourceField.Name,
			OutputDescription: description,
			Standardization:   mandatoryDIMCleaning(sourceField),
		})
	}
	keys := append(
		[]string(nil), source.Document.OutputGrain.KeyFields...,
	)
	if len(keys) == 0 {
		for _, field := range source.Document.Fields {
			code := strings.ToLower(strings.TrimSpace(field.Code))
			if strings.EqualFold(field.Role, "IDENTIFIER") ||
				strings.HasSuffix(code, "_id") ||
				strings.HasSuffix(code, "_key") {
				keys = append(keys, field.Code)
				break
			}
		}
	}
	return buildLLMDesignedDIMDocument(
		domain, source, dwdLLMDimensionDesign{
			SourceDatasetVersionID: source.VersionID,
			Name:                   strings.TrimSpace(source.Name),
			Description: "从 ODS 抽离并清洗的" +
				strings.TrimSpace(source.Name) + "实体说明信息",
			GrainKeyFieldCodes: keys,
			Fields:             fields,
			Rationale:          "兼容路径的确定性维度设计",
		},
	)
}

// buildLLMDesignedDIMDocument materializes the validated second-stage design as
// a reviewable DIM draft. The LLM supplies governed names/descriptions; the
// compiler owns the mandatory hygiene plan and preserves source grain, exact
// lineage and the expression whitelist.
func buildLLMDesignedDIMDocument(
	domain string,
	source dwdODSAsset,
	design dwdLLMDimensionDesign,
) (Document, string, error) {
	if source.Document.Dataset.Layer != LayerODS || len(source.Document.Fields) == 0 {
		return Document{}, "", errDWDModelingInvalid
	}
	if design.SourceDatasetVersionID != source.VersionID ||
		strings.TrimSpace(design.Name) == "" ||
		strings.TrimSpace(design.Description) == "" ||
		len(design.Fields) == 0 ||
		len(design.Fields) > len(source.Document.Fields) {
		return Document{}, "", errDWDModelingInvalid
	}
	designByField := make(
		map[string]dwdLLMDimensionFieldDesign, len(design.Fields),
	)
	for _, field := range design.Fields {
		if _, duplicate := designByField[field.SourceFieldCode]; duplicate {
			return Document{}, "", errDWDModelingInvalid
		}
		designByField[field.SourceFieldCode] = field
	}
	keys := append([]string(nil), design.GrainKeyFieldCodes...)
	fieldCodes := make(map[string]bool, len(source.Document.Fields))
	for _, field := range source.Document.Fields {
		fieldCodes[field.Code] = true
	}
	if len(keys) == 0 {
		keys = append(keys, source.Document.OutputGrain.KeyFields...)
	}
	if len(keys) == 0 {
		return Document{}, "", fmt.Errorf(
			"%w: DIM source has no governed entity key", errDWDModelingInvalid,
		)
	}
	scopedGrain := source.Document.OutputGrain
	scopedGrain.KeyFields = append([]string(nil), keys...)
	scoped := dwdPlanningTable{
		DatasetID: source.DatasetID, VersionID: source.VersionID,
		Name: design.Name, Description: design.Description,
		OutputGrain: scopedGrain,
		Fields:      make([]dwdPlanningField, 0, len(designByField)),
	}
	for _, field := range source.Document.Fields {
		if _, selected := designByField[field.Code]; !selected {
			continue
		}
		scoped.Fields = append(scoped.Fields, dwdPlanningField{
			Code: field.Code, Name: field.Name,
			Description: field.Description, Role: field.Role,
			CanonicalType: field.CanonicalType,
			SemanticType:  field.SemanticType, Nullable: field.Nullable,
		})
	}
	if factKind := dwdNonEntityFactKind(scoped); factKind != "" {
		return Document{}, "", fmt.Errorf(
			"%w: DIM projection retains %s",
			errDWDModelingInvalid, factKind,
		)
	}
	datasetCode, err := businessModeledDatasetCode(
		LayerDIM, domain, source, keys,
	)
	if err != nil {
		return Document{}, "", err
	}

	for _, key := range keys {
		if !fieldCodes[key] {
			return Document{}, "", fmt.Errorf(
				"%w: DIM entity key is not available in its source",
				errDWDModelingInvalid,
			)
		}
	}
	projection := make([]string, 0, len(design.Fields))
	fields := make([]Field, 0, len(design.Fields))
	for index, sourceField := range source.Document.Fields {
		planned, exists := designByField[sourceField.Code]
		if !exists {
			continue
		}
		if strings.TrimSpace(planned.OutputName) == "" ||
			strings.TrimSpace(planned.OutputDescription) == "" {
			return Document{}, "", errDWDModelingInvalid
		}
		// 检查点可能来自旧提示词。生成边界重新按当前平台合同计算，避免旧
		// CAST_DATETIME 或模型漏项绕过所有 STRING TRIM 和日粒度标准化。
		standardization := mandatoryDIMCleaning(sourceField)
		if err := validateDWDCleaning(
			dwdPlanningField{
				Code: sourceField.Code, Name: sourceField.Name,
				Description: sourceField.Description, Role: sourceField.Role,
				CanonicalType: sourceField.CanonicalType,
				SemanticType:  sourceField.SemanticType,
				Nullable:      sourceField.Nullable,
			},
			standardization,
		); err != nil {
			return Document{}, "", fmt.Errorf(
				"%w: DIM field %s standardization is invalid",
				errDWDModelingInvalid, sourceField.Code,
			)
		}
		projection = append(projection, sourceField.Code)
		expression, canonicalType, nullable, err := applyLLMDWDCleaning(
			"node_entity", sourceField, standardization,
		)
		if err != nil {
			return Document{}, "", err
		}
		visible := true
		fields = append(fields, Field{
			ID:            fmt.Sprintf("field_%d", index+1),
			Code:          sourceField.Code,
			Name:          strings.TrimSpace(planned.OutputName),
			Description:   strings.TrimSpace(planned.OutputDescription),
			Role:          sourceField.Role,
			Expression:    expression,
			CanonicalType: canonicalType,
			SemanticType:  sourceField.SemanticType,
			Aggregation:   "",
			Format: normalizedDWDDatasetFieldFormat(
				canonicalType, sourceField.Format,
			),
			Unit:     sourceField.Unit,
			Nullable: nullable,
			Visible:  &visible,
		})
	}
	if len(fields) != len(design.Fields) {
		return Document{}, "", errDWDModelingInvalid
	}
	sort.Strings(projection)
	timeField := source.Document.OutputGrain.TimeField
	selectedCodes := map[string]bool{}
	for _, code := range projection {
		selectedCodes[code] = true
	}
	for _, key := range keys {
		if !selectedCodes[key] {
			return Document{}, "", fmt.Errorf(
				"%w: DIM entity key is not part of the governed projection",
				errDWDModelingInvalid,
			)
		}
	}
	if timeField != "" && !selectedCodes[timeField] {
		timeField = ""
	}
	grainDescription := strings.TrimSpace(source.Document.OutputGrain.Description)
	if grainDescription == "" {
		grainDescription = "每行代表一个" + strings.TrimSpace(design.Name) + "实体"
	}
	document := Document{
		DSLVersion: DSLVersion,
		Dataset: Descriptor{
			Code:                    datasetCode,
			Name:                    strings.TrimSpace(design.Name),
			Description:             strings.TrimSpace(design.Description),
			Domain:                  modeledDomainName(domain),
			Subject:                 modeledTopic(source.Tags),
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
		Fields: fields,
		Distinct: len(fields) < len(source.Document.Fields) ||
			!sameDWDStringSet(
				keys, source.Document.OutputGrain.KeyFields,
			),
		Filters: []Filter{}, GroupBy: []string{},
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
			PreviewLimit: 10, ResultLimit: 100_000, CacheTTLSeconds: 300,
			Materialization: MaterializationPolicy{
				Enabled: true, RefreshMode: "ON_DEMAND",
			},
		},
	}
	raw, err := json.Marshal(struct {
		Domain       string                `json:"domain"`
		Source       string                `json:"sourceVersionId"`
		SourceSchema string                `json:"sourceSchemaHash"`
		Layer        Layer                 `json:"layer"`
		Design       dwdLLMDimensionDesign `json:"design"`
	}{
		Domain: domain, Source: source.VersionID,
		SourceSchema: source.SchemaHash, Layer: LayerDIM, Design: design,
	})
	if err != nil {
		return Document{}, "", err
	}
	sum := sha256.Sum256(raw)
	return document, hex.EncodeToString(sum[:]), nil
}

func standardizedDIMFieldDescription(entityName string, field Field) string {
	entityName = strings.TrimSpace(entityName)
	fieldName := strings.TrimSpace(field.Name)
	if fieldName == "" {
		fieldName = field.Code
	}
	switch strings.ToUpper(strings.TrimSpace(field.Role)) {
	case "IDENTIFIER":
		return entityName + "的唯一标识"
	case "TIME":
		return entityName + "的" + fieldName + "标准时间属性"
	case "DIMENSION", "ATTRIBUTE":
		return entityName + "的" + fieldName + "标准属性"
	default:
		return entityName + "的" + fieldName + "说明"
	}
}

func mandatoryDIMCleaning(field Field) []string {
	return mandatoryDWDFieldCleaning(dwdPlanningField{
		Code:          field.Code,
		Role:          field.Role,
		CanonicalType: field.CanonicalType,
		SemanticType:  field.SemanticType,
		Nullable:      field.Nullable,
	}, nil)
}

// normalizedDWDDatasetFieldFormat makes the display contract explicit while
// retaining DATE as the logical type used by time filters and time intelligence.
// CAST_DATE removes the time portion; YYYYMMDD controls its governed output form.
func normalizedDWDDatasetFieldFormat(
	canonicalType string,
	sourceFormat string,
) string {
	if strings.EqualFold(strings.TrimSpace(canonicalType), "DATE") {
		return "YYYYMMDD"
	}
	return strings.TrimSpace(sourceFormat)
}

func buildLLMDesignedDWDDocument(
	domain string,
	fact dwdODSAsset,
	assetsByVersion map[string]dwdODSAsset,
	dimensionAssetsBySourceVersion map[string]dwdODSAsset,
	output dwdLLMOutput,
) (Document, string, error) {
	if fact.VersionID != output.FactDatasetVersionID ||
		fact.Document.Dataset.Layer != LayerODS || len(output.Fields) == 0 {
		return Document{}, "", errDWDModelingInvalid
	}
	datasetCode, err := businessModeledDatasetCode(
		LayerDWD, domain, fact, output.GrainKeyOutputCodes,
	)
	if err != nil {
		return Document{}, "", err
	}
	businessName := ModeledDWDDisplayName(
		output.Name,
		fact.Document.Dataset.Name,
	)
	if businessName == "" {
		return Document{}, "", errDWDModelingInvalid
	}
	modelingAssetsBySourceVersion := make(
		map[string]dwdODSAsset, len(assetsByVersion),
	)
	for versionID, asset := range assetsByVersion {
		modelingAssetsBySourceVersion[versionID] = asset
	}
	for sourceVersionID, asset := range dimensionAssetsBySourceVersion {
		// FACT ODS may also emit an extracted DIM. Its planning identity is
		// synthetic and therefore is not present in the original ODS map, but it
		// is still a validated dimension contract required by the DWD compiler.
		modelingAssetsBySourceVersion[sourceVersionID] = asset
	}
	projectionsByVersion := map[string]map[string]bool{}
	for versionID := range assetsByVersion {
		projectionsByVersion[versionID] = map[string]bool{}
	}
	for identity := range dimensionAssetsBySourceVersion {
		if projectionsByVersion[identity] == nil {
			projectionsByVersion[identity] = map[string]bool{}
		}
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
	factLookupAssociations := dwdFactAssociationMap(output.FactAssociations)
	for index, join := range output.Joins {
		sourceVersionID := join.DimensionDatasetVersionID
		dimension, exists := modelingAssetsBySourceVersion[sourceVersionID]
		if !exists {
			return Document{}, "", errDWDModelingInvalid
		}
		if dimensionAssetsBySourceVersion != nil {
			if _, processed := dimensionAssetsBySourceVersion[sourceVersionID]; !processed {
				if _, approvedFactLookup :=
					factLookupAssociations[sourceVersionID]; !approvedFactLookup {
					return Document{}, "", errDWDModelingInvalid
				}
			}
		}
		nodeID := fmt.Sprintf("node_dim_%d", index+1)
		nodeByVersion[sourceVersionID] = nodeID
		nodes = append(nodes, Node{
			ID: nodeID, Type: "DATASET", DatasetVersionID: dimension.VersionID,
			Alias:         fmt.Sprintf("t%d", index+2),
			Projection:    sortedDWDProjection(projectionsByVersion[sourceVersionID]),
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
			// LLM 只允许从已发布的字段合同中选择关联键；本地校验通过后
			// 生成的关联已经是可执行合同，不应再要求用户逐个点击确认。
			ManualConfirmed: true,
			Conditions:      conditions,
		})
	}
	fields := make([]Field, 0, len(output.Fields))
	for index, planned := range output.Fields {
		sourceAsset, exists :=
			modelingAssetsBySourceVersion[planned.SourceDatasetVersionID]
		nodeID := nodeByVersion[planned.SourceDatasetVersionID]
		source, fieldExists := dwdDocumentFieldByCode(
			sourceAsset.Document, planned.SourceFieldCode,
		)
		if !exists || !fieldExists || nodeID == "" {
			return Document{}, "", errDWDModelingInvalid
		}
		// 与 DIM 相同，DWD 生成边界根据可信源字段和最终字段角色重新计算
		// 卫生组件，使旧检查点和模型漏项也无法跳过平台合同。
		cleaningSource := dwdPlanningField{
			Code:          source.Code,
			Role:          planned.Role,
			CanonicalType: source.CanonicalType,
			SemanticType:  source.SemanticType,
			Nullable:      source.Nullable,
		}
		cleaning := mandatoryDWDFieldCleaning(cleaningSource, planned.Cleaning)
		if err := validateDWDCleaning(cleaningSource, cleaning); err != nil {
			return Document{}, "", err
		}
		expression, canonicalType, nullable, err := applyLLMDWDCleaning(
			nodeID, source, cleaning,
		)
		if err != nil {
			return Document{}, "", err
		}
		expression, canonicalType, nullable, err = applyLLMDWDProcessing(
			expression, canonicalType, nullable, planned.Processing,
			modelingAssetsBySourceVersion, nodeByVersion,
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
			Format: normalizedDWDDatasetFieldFormat(
				canonicalType, source.Format,
			),
			Unit:    source.Unit,
			Visible: &visible,
		})
	}
	atomicMeasures := make([]AtomicMeasureContract, 0)
	for index, field := range fields {
		if field.Role != "MEASURE" {
			continue
		}
		atomicMeasures = append(
			atomicMeasures,
			dwdAtomicMeasureContract(
				output.Fields[index], field,
			),
		)
	}
	document := Document{
		DSLVersion: DSLVersion,
		Dataset: Descriptor{
			Code:        datasetCode,
			Name:        businessName,
			Description: strings.TrimSpace(output.Description),
			Domain:      modeledDomainName(domain),
			Subject:     modeledTopic(fact.Tags),
			Type: dwdModeledDatasetType(
				fact, assetsByVersion, output,
			),
			Layer:                   LayerDWD,
			SemanticContractVersion: "1.0",
		},
		Nodes: nodes, Joins: joins, PreAggregations: []PreAggregation{},
		FactContract: &FactContract{
			BusinessAction: businessName,
			GrainKeyFields: append([]string(nil), output.GrainKeyOutputCodes...),
			EventTimeField: output.TimeOutputCode,
			AtomicMeasures: atomicMeasures,
		},
		Fields: fields, Filters: []Filter{}, GroupBy: []string{},
		Having: []Filter{}, Sorts: []Sort{}, Parameters: []Parameter{},
		OutputGrain: OutputGrain{
			Description: "每行代表" + businessName + "的一条事实明细",
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
			PreviewLimit: 10, ResultLimit: 100_000, CacheTTLSeconds: 300,
			Materialization: MaterializationPolicy{
				Enabled: true, RefreshMode: "ON_DEMAND",
			},
		},
	}
	dimensionVersions := map[string]string{}
	for sourceVersionID, asset := range dimensionAssetsBySourceVersion {
		dimensionVersions[sourceVersionID] = asset.VersionID
	}
	raw, err := json.Marshal(struct {
		Domain            string            `json:"domain"`
		Fact              string            `json:"factVersionId"`
		DimensionVersions map[string]string `json:"dimensionVersions"`
		Output            dwdLLMOutput      `json:"output"`
	}{
		Domain: domain, Fact: fact.VersionID,
		DimensionVersions: dimensionVersions, Output: output,
	})
	if err != nil {
		return Document{}, "", err
	}
	sum := sha256.Sum256(raw)
	return document, hex.EncodeToString(sum[:]), nil
}

func dwdAtomicMeasureContract(
	planned dwdLLMField,
	field Field,
) AtomicMeasureContract {
	text := strings.ToLower(strings.Join([]string{
		field.Code, field.Name, field.Description, field.SemanticType,
		planned.OutputCode, planned.OutputName, planned.OutputDescription,
	}, " "))
	behavior := strings.ToUpper(strings.TrimSpace(planned.MeasureBehavior))
	switch {
	case containsAnyDWDMeasureMarker(text, []string{
		"累计", "累积", "截至", "本年累计", "本月累计",
		"cumulative", "running_total", "running total", "to_date",
		"ytd", "mtd", "qtd",
	}):
		behavior = "CUMULATIVE"
	case containsAnyDWDMeasureMarker(text, []string{
		"余额", "库存", "存量", "时点", "期末", "期初", "结余",
		"在手", "保有", "未结", "balance", "inventory", "stock",
		"on_hand", "on hand", "outstanding", "closing", "ending",
		"as_of", "as of",
	}):
		behavior = "POINT_IN_TIME"
	case behavior != "FLOW" && behavior != "CUMULATIVE" &&
		behavior != "POINT_IN_TIME" && behavior != "NON_ADDITIVE":
		behavior = "FLOW"
	}
	contract := AtomicMeasureContract{
		Field:              field.Code,
		ValueBehavior:      behavior,
		DefaultAggregation: "SUM",
		Unit:               field.Unit,
		NullPolicy:         "PRESERVE",
	}
	switch behavior {
	case "CUMULATIVE", "POINT_IN_TIME":
		contract.Additivity = "SEMI_ADDITIVE"
		contract.TimeAggregation = "LAST"
	case "NON_ADDITIVE":
		contract.Additivity = "NON_ADDITIVE"
		contract.DefaultAggregation = "AVG"
		contract.TimeAggregation = "NONE"
	default:
		contract.ValueBehavior = "FLOW"
		contract.Additivity = "ADDITIVE"
		contract.TimeAggregation = "SUM"
	}
	return contract
}

func containsAnyDWDMeasureMarker(value string, markers []string) bool {
	for _, marker := range markers {
		if strings.Contains(value, marker) {
			return true
		}
	}
	return false
}

func dwdModeledDatasetType(
	fact dwdODSAsset,
	assetsByVersion map[string]dwdODSAsset,
	output dwdLLMOutput,
) string {
	sourceIDs := map[string]bool{}
	addAsset := func(asset dwdODSAsset) {
		for _, node := range asset.Document.Nodes {
			if node.DataSourceID != "" {
				sourceIDs[node.DataSourceID] = true
			}
		}
	}
	addAsset(fact)
	for _, join := range output.Joins {
		asset, exists := assetsByVersion[join.DimensionDatasetVersionID]
		if exists {
			addAsset(asset)
		}
	}
	if len(sourceIDs) > 1 {
		return "CROSS_SOURCE"
	}
	return "SINGLE_SOURCE"
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
		case "COALESCE_DEFAULT":
			value, supported := dwdDefaultNullValue(canonicalType)
			if !supported {
				return Expression{}, "", false, errDWDModelingInvalid
			}
			expression = Expression{
				Type: "COALESCE",
				Arguments: []Expression{
					expression, {Type: "LITERAL", Value: value},
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

func dwdDefaultNullValue(canonicalType string) (any, bool) {
	switch strings.ToUpper(strings.TrimSpace(canonicalType)) {
	case "STRING":
		return "UNKNOWN", true
	case "DATE", "DATETIME":
		return "1970-01-01", true
	case "INTEGER", "DECIMAL":
		return 999999999, true
	case "BOOLEAN":
		return false, true
	default:
		return nil, false
	}
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
	var subjectCurrent bool
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
		JOIN platform.business_domains AS business_domain
		  ON business_domain.id=dataset.domain_id
		 AND business_domain.tenant_id=dataset.tenant_id
		 AND business_domain.status='ACTIVE'
		JOIN platform.domain_memberships AS membership
		  ON membership.tenant_id=actor.tenant_id
		 AND membership.user_id=actor.id
		 AND membership.domain_id=business_domain.id
		 AND membership.status='ACTIVE'
		WHERE job.id=$1::uuid
	)`, claim.ID).Scan(&subjectCurrent)
	if err != nil {
		return err
	}
	if !subjectCurrent {
		return errDWDModelingSubjectChange
	}
	var leaseCurrent bool
	err = tx.QueryRow(ctx, `SELECT EXISTS(
		SELECT 1
		FROM platform.dwd_modeling_stage_jobs AS stage
		WHERE stage.id=$1::uuid
		  AND stage.workflow_job_id=$2::uuid
		  AND stage.status='RUNNING'
		  AND stage.lease_owner=$3
		  AND stage.lease_token=$4::uuid
		  AND stage.attempt=$5
		  AND stage.lease_expires_at>now()
	)`, claim.StageJobID, claim.ID, workerID, claim.LeaseToken,
		claim.Attempt).Scan(&leaseCurrent)
	if err != nil {
		return err
	}
	if !leaseCurrent {
		return errDWDModelingLeaseLost
	}
	return nil
}

func loadPublishedODSAssetsTx(ctx context.Context, tx pgx.Tx) ([]dwdODSAsset, error) {
	rows, err := tx.Query(ctx, `SELECT dataset.id::text,version.id::text,
			dataset.domain_id::text,business_domain.name,
			version.schema_hash,dataset.code::text,dataset.name,dataset.description,
			COALESCE(metadata_table.schema_name,''),
			COALESCE(metadata_table.table_name,''),version.dsl_json,
			COALESCE(metadata_table.tags,'{}'::text[])
			  || COALESCE(binding_tags.tags,'{}'::text[]) AS tags
		FROM platform.datasets AS dataset
		JOIN platform.business_domains AS business_domain
		  ON business_domain.id=dataset.domain_id
		 AND business_domain.tenant_id=dataset.tenant_id
		 AND business_domain.status='ACTIVE'
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
		    WHEN tag.category='TABLE_FUNCTION' THEN '作用:'||tag.name
		    ELSE tag.name
		  END ORDER BY CASE
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
		    -- ODS role classification is an output of this workflow. Feeding
		    -- it back into the planning snapshot makes a successful
		    -- classification stage invalidate its own checkpoints before the
		    -- DIM stage can consume them.
		    AND NOT (
		      binding.origin='LLM'
		      AND binding.evidence_json->>'decisionPoint'=
		          'ODS_ROLE_CLASSIFICATION'
		    )
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
			&asset.DatasetID, &asset.VersionID,
			&asset.DomainID, &asset.DomainName, &asset.SchemaHash,
			&asset.Code, &asset.Name, &asset.Description,
			&asset.SourceSchemaName, &asset.SourceTableName, &raw, &tags,
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
		asset.Tags = append([]string(nil), tags...)
		assets = append(assets, asset)
	}
	return assets, rows.Err()
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

// retireSuppressedGeneratedDIMDraftTx removes only an untouched, unpublished
// auto-generated DIM whose entity is now owned by another authoritative source.
// Any sign of governance, manual editing or downstream use leaves the asset
// visible for explicit review instead of deleting user work.
func (worker *DWDModelingWorker) retireSuppressedGeneratedDIMDraftTx(
	ctx context.Context,
	tx pgx.Tx,
	claim dwdModelingClaim,
	source dwdODSAsset,
	authoritativeEntitySignatures map[string]bool,
) (datasetID string, mapped, retired bool, err error) {
	return worker.retireGeneratedDIMDraftTx(
		ctx, tx, claim, source, authoritativeEntitySignatures,
		true, false, "DUPLICATE_DIM_KEEP_ONE",
	)
}

func (worker *DWDModelingWorker) retireInvalidFactGeneratedDIMDraftTx(
	ctx context.Context,
	tx pgx.Tx,
	claim dwdModelingClaim,
	source dwdODSAsset,
) (datasetID string, mapped, retired bool, err error) {
	return worker.retireGeneratedDIMDraftTx(
		ctx, tx, claim, source, nil, false, true, "NON_ENTITY_FACT",
	)
}

func (worker *DWDModelingWorker) retireGeneratedDIMDraftTx(
	ctx context.Context,
	tx pgx.Tx,
	claim dwdModelingClaim,
	source dwdODSAsset,
	authoritativeEntitySignatures map[string]bool,
	requireAuthoritativeSignature bool,
	requireFactOccurrenceGrain bool,
	reason string,
) (datasetID string, mapped, retired bool, err error) {
	var (
		datasetVersion, publishedVersionCount  int64
		datasetStatus, draftStatus             string
		currentSchemaHash, generatedSchemaHash string
		rawDraftDSL                            json.RawMessage
		deleted                                bool
		publicationRequestCount                int64
		dependencyCount                        int64
		materializationCount, buildRunCount    int64
		queryRunCount                          int64
	)
	err = tx.QueryRow(ctx, `SELECT
			output.dim_dataset_id::text,
			dataset.version,dataset.status,dataset.deleted_at IS NOT NULL,
			draft.status,draft.schema_hash,draft.dsl_json,
			output.last_generated_schema_hash,
			(SELECT count(*) FROM platform.dataset_versions AS version
			  WHERE version.dataset_id=dataset.id
			    AND version.status IN ('PUBLISHED','STALE')),
			(SELECT count(*) FROM platform.dataset_publication_requests AS request
			  WHERE request.dataset_id=dataset.id),
			(SELECT count(*)
			 FROM platform.dataset_dependencies AS dependency
			 JOIN platform.dataset_versions AS source_version
			   ON source_version.id::text=dependency.source_id
			 JOIN platform.dataset_versions AS downstream_version
			   ON downstream_version.id=dependency.dataset_version_id
			 JOIN platform.datasets AS downstream_dataset
			   ON downstream_dataset.id=downstream_version.dataset_id
			  AND downstream_dataset.deleted_at IS NULL
			 WHERE dependency.source_type='DATASET_VERSION'
			   AND source_version.dataset_id=dataset.id
			   AND downstream_version.status<>'DEPRECATED'),
			(SELECT count(*) FROM platform.dataset_materializations AS materialization
			  WHERE materialization.dataset_id=dataset.id),
			(SELECT count(*) FROM platform.dataset_build_runs AS build
			  WHERE build.dataset_id=dataset.id
			    AND build.status IN ('QUEUED','RUNNING')),
			(SELECT count(*) FROM platform.query_runs AS run
			  WHERE run.dataset_id=dataset.id AND run.status='RUNNING')
		FROM platform.dim_modeling_outputs AS output
		JOIN platform.datasets AS dataset
		  ON dataset.id=output.dim_dataset_id
		 AND dataset.tenant_id=output.tenant_id
		JOIN platform.dataset_versions AS draft
		  ON draft.id=dataset.current_draft_version_id
		 AND draft.dataset_id=dataset.id
		 AND draft.tenant_id=dataset.tenant_id
		WHERE output.source_dataset_id=$1::uuid
		  AND output.dimension_key='primary'
		FOR UPDATE OF output,dataset`,
		source.DatasetID,
	).Scan(
		&datasetID, &datasetVersion, &datasetStatus, &deleted,
		&draftStatus, &currentSchemaHash, &rawDraftDSL,
		&generatedSchemaHash,
		&publishedVersionCount, &publicationRequestCount,
		&dependencyCount, &materializationCount,
		&buildRunCount, &queryRunCount,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, false, nil
	}
	if err != nil {
		return "", false, false, err
	}
	mapped = true
	document, decodeErr := DecodeAndNormalize(rawDraftDSL)
	if decodeErr != nil {
		return datasetID, true, false, nil
	}
	generatedTable := dwdPlanningTable{
		OutputGrain: document.OutputGrain,
		Fields:      make([]dwdPlanningField, 0, len(document.Fields)),
	}
	for _, field := range document.Fields {
		generatedTable.Fields = append(
			generatedTable.Fields, dwdPlanningField{
				Code:          field.Code,
				Role:          field.Role,
				CanonicalType: field.CanonicalType,
				SemanticType:  field.SemanticType,
				Nullable:      field.Nullable,
			},
		)
	}
	// Correcting a source to FACT is not sufficient authority to delete a
	// previously extracted stable entity DIM. Automatic cleanup is limited
	// further to drafts whose own grain is an occurrence key such as EVENT_ID
	// or ORDER_ID.
	if requireFactOccurrenceGrain &&
		!hasDWDFactOccurrenceGrain(generatedTable) {
		return datasetID, true, false, nil
	}
	generatedSignature, strong := dwdDimensionEntitySignature(
		generatedTable, document.OutputGrain.KeyFields,
	)
	if requireAuthoritativeSignature &&
		(!strong || !authoritativeEntitySignatures[generatedSignature]) {
		return datasetID, true, false, nil
	}
	safe := !deleted &&
		datasetStatus == "DRAFT" &&
		draftStatus == "DRAFT" &&
		currentSchemaHash == generatedSchemaHash &&
		publishedVersionCount == 0 &&
		publicationRequestCount == 0 &&
		dependencyCount == 0 &&
		materializationCount == 0 &&
		buildRunCount == 0 &&
		queryRunCount == 0
	if !safe {
		return datasetID, true, false, nil
	}
	tag, err := tx.Exec(ctx, `DELETE FROM platform.dim_modeling_outputs
		WHERE source_dataset_id=$1::uuid AND dim_dataset_id=$2::uuid`,
		source.DatasetID, datasetID,
	)
	if err != nil {
		return "", false, false, err
	}
	if tag.RowsAffected() != 1 {
		return "", false, false, ErrConflict
	}
	tag, err = tx.Exec(ctx, `UPDATE platform.datasets SET
			status='DEPRECATED',current_published_version_id=NULL,
			disabled_from_status=NULL,disabled_published_version_id=NULL,
			code=left(code,100)||'_deleted_'||substr(id::text,1,8),
			deleted_at=now(),version=version+1,updated_by=$1
		WHERE id=$2::uuid AND version=$3 AND deleted_at IS NULL`,
		claim.ActorID, datasetID, datasetVersion,
	)
	if err != nil {
		return "", false, false, err
	}
	if tag.RowsAffected() != 1 {
		return "", false, false, ErrConflict
	}
	if _, err := tx.Exec(ctx, `INSERT INTO platform.audit_logs(
			tenant_id,actor_user_id,action,resource_type,resource_id,detail
		) VALUES($1,$2,'AUTO_DIM_RETIRE','DATASET',$3,
			jsonb_build_object(
			  'sourceDatasetId',$4::text,
			  'sourceDatasetVersionId',$5::text,
			  'dwdModelingJobId',$6::text,
			  'reason',$7::text
			))`,
		claim.TenantID, claim.ActorID, datasetID, source.DatasetID,
		source.VersionID, claim.ID, reason,
	); err != nil {
		return "", false, false, err
	}
	return datasetID, true, true, nil
}

func (worker *DWDModelingWorker) upsertGeneratedDIMDraftTx(
	ctx context.Context,
	tx pgx.Tx,
	claim dwdModelingClaim,
	source dwdODSAsset,
	dimensionKey, domain, inputHash string,
	prepared Prepared,
) (datasetID, action string, err error) {
	if !validDWDDimensionProjectionCode(dimensionKey) {
		return "", "", errDWDModelingInvalid
	}
	var existingDatasetID, lastSchemaHash string
	err = tx.QueryRow(ctx, `SELECT output.dim_dataset_id::text,
			output.last_generated_schema_hash
		FROM platform.dim_modeling_outputs AS output
		WHERE output.source_dataset_id=$1::uuid
		  AND output.dimension_key=$2
		FOR UPDATE`, source.DatasetID, dimensionKey).
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
		datasetID, err = createDatasetTxWithOptions(
			ctx, tx, claim.TenantID, claim.ActorID, input, prepared, "",
			derivedWriteOptions{domainID: source.DomainID},
		)
		if err != nil {
			return "", "", err
		}
		_, err = tx.Exec(ctx, `INSERT INTO platform.dim_modeling_outputs(
			tenant_id,source_dataset_id,dimension_key,dim_dataset_id,domain_key,
			last_job_id,last_input_hash,last_generated_schema_hash,last_action
		) VALUES($1,$2,$3,$4,$5,$6,$7,$8,'CREATED')`,
			claim.TenantID, source.DatasetID, dimensionKey, datasetID, domain,
			claim.ID, inputHash, prepared.DSLHash,
		)
		if err == nil {
			_, err = tx.Exec(ctx, `DELETE FROM platform.dim_modeling_suppressions
				WHERE suppressed_source_dataset_id=$1::uuid`,
				source.DatasetID,
			)
		}
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
				code=$1,name=$2,description=$3,dataset_type=$4,layer='DIM',
				version=version+1,updated_by=$5
			WHERE id=$6::uuid AND version=$7`,
			prepared.Document.Dataset.Code, prepared.Document.Dataset.Name,
			prepared.Document.Dataset.Description, prepared.Document.Dataset.Type,
			claim.ActorID, existingDatasetID, aggregateVersion,
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
		WHERE source_dataset_id=$6::uuid AND dimension_key=$7`,
		domain, claim.ID, inputHash, prepared.DSLHash, action, source.DatasetID,
		dimensionKey,
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
		datasetID, err = createDatasetTxWithOptions(
			ctx, tx, claim.TenantID, claim.ActorID, input, prepared, "",
			derivedWriteOptions{
				allowDraftDatasetDependencies: true,
				domainID:                      fact.DomainID,
			},
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
				code=$1,name=$2,description=$3,dataset_type=$4,layer='DWD',
				version=version+1,updated_by=$5
			WHERE id=$6::uuid AND version=$7`,
			prepared.Document.Dataset.Code, prepared.Document.Dataset.Name,
			prepared.Document.Dataset.Description, prepared.Document.Dataset.Type,
			claim.ActorID, existingDatasetID, aggregateVersion,
		); err != nil {
			return "", "", err
		}
		if err := replaceDerivedWithOptions(
			ctx, tx, claim.TenantID, existingDatasetID,
			draftVersionID, prepared.Document, true,
			derivedWriteOptions{allowDraftDatasetDependencies: true},
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
	classifications []dwdLLMClassification,
	dimensionStage dwdDimensionStageResult,
	errorCode, errorMessage string,
) error {
	factCount, dimensionCount, otherCount := 0, 0, 0
	for _, classification := range classifications {
		if classification.Role == "FACT" {
			factCount++
		}
		if classificationProducesDimension(classification) {
			dimensionCount++
		} else if classification.Role != "FACT" {
			otherCount++
		}
	}
	result, err := json.Marshal(map[string]any{
		"domain": domain, "triggerRole": role, "outputs": items,
		"classificationSummary": map[string]any{
			"factTableCount":      factCount,
			"dimensionTableCount": dimensionCount,
			"otherTableCount":     otherCount,
		},
		"dimensionStage": map[string]any{
			"pendingPublicationCount": dimensionStage.PendingPublicationCount,
			"pendingDatasetIds":       dimensionStage.PendingPublicationDatasets,
			"failedDesignCount":       dimensionStage.FailedDesignCount,
			"failedSourceDatasetIds":  dimensionStage.FailedSourceDatasets,
			"retiredCount":            dimensionStage.Retired,
		},
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
					"name":        "DOMAIN_ODS_CLASSIFICATION",
					"inputLayers": []string{"ODS"},
					"outputs":     []string{"FACT_CLASSIFICATION", "DIMENSION_CLASSIFICATION"},
				},
				{
					"order": 2, "parallel": true,
					"name":   "DIM_STANDARDIZATION",
					"layers": []string{"DIM"}, "inputLayers": []string{"ODS"},
				},
				{
					"order": 3, "parallel": true,
					"name":        "FACT_MODELING",
					"layers":      []string{"DWD"},
					"inputLayers": []string{"ODS_FACT", "STANDARDIZED_DIM_CONTRACT"},
				},
				{
					"order": 4, "parallel": true,
					"name":   "SUBJECT_MODELING",
					"layers": []string{"DWS"}, "inputLayers": []string{"DWD"},
				},
				{
					"order": 5, "parallel": false,
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
	tag, err := tx.Exec(ctx, `UPDATE platform.dwd_modeling_stage_jobs SET
		status=$1,
		generated_count=$2,updated_count=$3,skipped_count=$4,
		result_json=$5,error_code=$6,error_message=$7,
		ai_request_id=NULLIF($8,'')::uuid,
		lease_owner='',lease_token=NULL,lease_expires_at=NULL,
		completed_at=now(),updated_at=now()
		WHERE id=$9::uuid AND workflow_job_id=$13::uuid
		  AND status='RUNNING'
		  AND lease_owner=$10 AND lease_token=$11::uuid AND attempt=$12`,
		status, created, updated, skipped, result,
		errorCode, errorMessage, aiRequestID,
		claim.StageJobID, workerID, claim.LeaseToken, claim.Attempt,
		claim.ID,
	)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return errDWDModelingLeaseLost
	}
	if status != "SUCCEEDED" {
		// 非成功终态意味着所有下游阶段都已失去可执行前置。立即把仍在
		// 等待的阶段一并终结；人工重试入口会按既有合同重新排队下游。
		if _, err := tx.Exec(ctx, `UPDATE platform.dwd_modeling_stage_jobs
			SET status='SKIPPED',error_code='UPSTREAM_NOT_SUCCEEDED',
				error_message='上游建模阶段未成功，当前阶段已终止等待',
				lease_owner='',lease_token=NULL,lease_expires_at=NULL,
				completed_at=now(),updated_at=now()
			WHERE tenant_id=$1::uuid AND workflow_job_id=$2::uuid
			  AND stage_order>$3 AND status IN ('PENDING','RUNNING')`,
			claim.TenantID, claim.ID, dwdStageOrder(claim.Stage),
		); err != nil {
			return err
		}
	}
	workflowStatus := status
	workflowErrorCode := errorCode
	workflowErrorMessage := errorMessage
	workflowCompleted := true
	hasEnabledSuccessor := false
	if status == "SUCCEEDED" {
		if err := tx.QueryRow(ctx, `SELECT EXISTS(
			SELECT 1
			FROM platform.dwd_modeling_stage_jobs
			WHERE tenant_id=$1::uuid
			  AND workflow_job_id=$2::uuid
			  AND stage_order>$3
			  AND manual_enabled
			  AND status IN ('PENDING','RUNNING')
		)`, claim.TenantID, claim.ID, dwdStageOrder(claim.Stage),
		).Scan(&hasEnabledSuccessor); err != nil {
			return err
		}
	}
	if hasEnabledSuccessor {
		workflowStatus = "RUNNING"
		workflowErrorCode = ""
		workflowErrorMessage = ""
		workflowCompleted = false
	}
	tag, err = tx.Exec(ctx, `UPDATE platform.dwd_modeling_jobs SET
		status=$1,
		domain_key=COALESCE(NULLIF($2,''),domain_key),
		trigger_role=COALESCE(NULLIF($3,''),trigger_role),
		generated_count=$4,updated_count=$5,skipped_count=$6,
		result_json=$7,error_code=$8,error_message=$9,
		ai_request_id=NULLIF($10,'')::uuid,
		lease_owner='',lease_token=NULL,lease_expires_at=NULL,
		completed_at=CASE WHEN $11 THEN now() ELSE NULL END,
		updated_at=now()
		WHERE id=$12::uuid`,
		workflowStatus, domain, role, created, updated, skipped, result,
		workflowErrorCode, workflowErrorMessage, aiRequestID,
		workflowCompleted, claim.ID,
	)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return errDWDModelingSubjectChange
	}
	return nil
}

func dwdStageOrder(stage string) int {
	switch stage {
	case dwdStageDomainClassification:
		return 1
	case dwdStageDimensionModeling:
		return 2
	case dwdStageFactModeling:
		return 3
	default:
		return 0
	}
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
				nil, dwdDimensionStageResult{},
				errorCode, errorMessage,
			)
		}
		nextAttempt := claim.Attempt
		if progressed {
			nextAttempt = 0
		}
		tag, err := tx.Exec(ctx, `UPDATE platform.dwd_modeling_stage_jobs SET
			status='PENDING',next_attempt_at=now()+interval '1 minute',
			attempt=$1,error_code=$2,error_message=$3,
			lease_owner='',lease_token=NULL,lease_expires_at=NULL,updated_at=now()
			WHERE id=$4::uuid AND workflow_job_id=$8::uuid
			  AND status='RUNNING'
			  AND lease_owner=$5 AND lease_token=$6::uuid AND attempt=$7`,
			nextAttempt, errorCode, errorMessage, claim.StageJobID, workerID,
			claim.LeaseToken, claim.Attempt,
			claim.ID,
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
			[]dwdModelingResultItem{}, nil, dwdDimensionStageResult{},
			errorCode, errorMessage,
		)
	})
}
