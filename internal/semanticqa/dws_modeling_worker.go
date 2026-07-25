package semanticqa

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	aiplatform "intelligent-report-generation-system/internal/ai"
	"intelligent-report-generation-system/internal/dataset"
	"intelligent-report-generation-system/internal/platform/database"
)

type DWSAnalysisSelector interface {
	Select(
		context.Context,
		string,
		string,
		string,
		dataset.Document,
		[]string,
	) ([]string, string, error)
}

type OrchestratedDWSAnalysisSelector struct {
	ai semanticAIInvoker
}

func NewOrchestratedDWSAnalysisSelector(
	ai semanticAIInvoker,
) *OrchestratedDWSAnalysisSelector {
	return &OrchestratedDWSAnalysisSelector{ai: ai}
}

func (selector *OrchestratedDWSAnalysisSelector) Select(
	ctx context.Context,
	tenantID, actorID, sourceVersionID string,
	document dataset.Document,
	eligible []string,
) ([]string, string, error) {
	fallback := boundedTemplateSelection(eligible)
	if selector == nil || selector.ai == nil || !selector.ai.Configured() {
		return fallback, "", nil
	}
	fields := make([]map[string]string, 0, len(document.Fields))
	for _, field := range document.Fields {
		fields = append(fields, map[string]string{
			"code": field.Code, "name": field.Name, "role": field.Role,
			"canonicalType": field.CanonicalType, "unit": field.Unit,
		})
	}
	payload, err := json.Marshal(map[string]any{
		"businessAction": document.FactContract.BusinessAction,
		"grainKeyFields": document.FactContract.GrainKeyFields,
		"eventTimeField": document.FactContract.EventTimeField,
		"fields":         fields, "eligibleTemplateCodes": eligible,
		"maximumSelections": 3,
	})
	if err != nil {
		return fallback, "", nil
	}
	temperature := 0.0
	result, err := selector.ai.Invoke(ctx, aiplatform.Invocation{
		TenantID: tenantID, ActorID: actorID,
		Purpose:       aiplatform.PurposeDatasetDAGGeneration,
		PromptVersion: "dws-analysis-selection-v1",
		ResourceType:  "DATASET_VERSION", ResourceID: sourceVersionID,
		Request: aiplatform.ProviderRequest{
			Messages: []aiplatform.Message{
				{
					Role: aiplatform.MessageRoleSystem,
					Parts: []aiplatform.ContentPart{{
						Type: aiplatform.ContentTypeText,
						Text: `你只负责从 eligibleTemplateCodes 中选择最多三个市场通用分析意图。不得创造新编码，不得输出 SQL、DDL、物理表名或数据值。缺少适用场景时返回空数组。`,
					}},
				},
				{
					Role: aiplatform.MessageRoleUser,
					Parts: []aiplatform.ContentPart{{
						Type: aiplatform.ContentTypeText, Text: string(payload),
					}},
				},
			},
			ResponseSchema: aiplatform.JSONSchema{
				Name: "dws_analysis_template_selection",
				Schema: json.RawMessage(`{
					"type":"object","additionalProperties":false,
					"required":["templateCodes"],
					"properties":{"templateCodes":{
						"type":"array","maxItems":3,"uniqueItems":true,
						"items":{"enum":[
							"TREND","PERIOD_COMPARISON","DISTRIBUTION",
							"RANKING","DRILLDOWN","ANOMALY"
						]}
					}}
				}`),
			},
			Temperature: &temperature, MaxOutputTokens: 200,
		},
	})
	if err != nil {
		// Provider retry is owned by the shared AI boundary. This durable task
		// falls back to deterministic rules instead of multiplying retries.
		return fallback, "", nil
	}
	var response struct {
		TemplateCodes []string `json:"templateCodes"`
	}
	if json.Unmarshal(result.ProviderResult.Content, &response) != nil {
		return fallback, result.RequestID, nil
	}
	selected := validatedTemplateSelection(response.TemplateCodes, eligible)
	if len(response.TemplateCodes) > 0 && len(selected) == 0 {
		return fallback, result.RequestID, nil
	}
	return selected, result.RequestID, nil
}

func boundedTemplateSelection(eligible []string) []string {
	if len(eligible) > 3 {
		eligible = eligible[:3]
	}
	return append([]string(nil), eligible...)
}

func validatedTemplateSelection(selected, eligible []string) []string {
	allowed := map[string]bool{}
	for _, code := range eligible {
		allowed[code] = true
	}
	result := []string{}
	seen := map[string]bool{}
	for _, code := range selected {
		code = strings.ToUpper(strings.TrimSpace(code))
		if !allowed[code] || seen[code] || len(result) == 3 {
			continue
		}
		seen[code] = true
		result = append(result, code)
	}
	return result
}

type dwsModelingDatasetService interface {
	DatasetService
	GetVersion(context.Context, string, string, string) (dataset.VersionRecord, error)
}

type DWSModelingWorker struct {
	store    *PostgresStore
	datasets dwsModelingDatasetService
	selector DWSAnalysisSelector
}

func NewDWSModelingWorker(
	store *PostgresStore,
	datasets dwsModelingDatasetService,
	selector DWSAnalysisSelector,
) *DWSModelingWorker {
	return &DWSModelingWorker{
		store: store, datasets: datasets, selector: selector,
	}
}

type dwsModelingClaim struct {
	ID, TenantID, SourceDatasetID, SourceVersionID string
	ActorID, LeaseToken, InputHash                 string
	Selections                                     []string
	Attempt, MaxAttempts                           int
}

type dwsModelingResult struct {
	TemplateCode string `json:"templateCode"`
	DatasetID    string `json:"datasetId,omitempty"`
	Action       string `json:"action"`
	ReasonCode   string `json:"reasonCode,omitempty"`
}

func (worker *DWSModelingWorker) TenantIDs(
	ctx context.Context,
) ([]string, error) {
	if worker == nil || worker.store == nil || worker.store.pool == nil {
		return nil, ErrInvalidRequest
	}
	rows, err := worker.store.pool.Query(ctx, `SELECT id::text
		FROM platform.tenants
		WHERE status='ACTIVE' AND deleted_at IS NULL ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		result = append(result, id)
	}
	return result, rows.Err()
}

func (worker *DWSModelingWorker) ProcessNext(
	ctx context.Context,
	tenantID, workerID string,
	lease time.Duration,
) (bool, error) {
	if worker == nil || worker.store == nil || worker.datasets == nil ||
		uuid.Validate(tenantID) != nil || !validWorkerID(workerID) ||
		lease < time.Second || lease > time.Hour {
		return false, ErrInvalidRequest
	}
	claim, err := worker.claim(ctx, tenantID, workerID, lease)
	if err != nil || claim == nil {
		return false, err
	}
	processCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	heartbeatErr := make(chan error, 1)
	go func() {
		ticker := time.NewTicker(max(time.Second, lease/3))
		defer ticker.Stop()
		for {
			select {
			case <-processCtx.Done():
				return
			case <-ticker.C:
				if err := worker.renew(
					processCtx, *claim, workerID, lease,
				); err != nil {
					select {
					case heartbeatErr <- err:
					default:
					}
					cancel()
					return
				}
			}
		}
	}()
	processErr := worker.process(processCtx, *claim, workerID)
	cancel()
	if processErr != nil {
		select {
		case leaseErr := <-heartbeatErr:
			return true, errors.Join(processErr, leaseErr)
		default:
		}
	}
	return true, processErr
}

func (worker *DWSModelingWorker) renew(
	ctx context.Context,
	claim dwsModelingClaim,
	workerID string,
	lease time.Duration,
) error {
	return database.WithTenantTx(ctx, worker.store.pool, claim.TenantID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE platform.dws_modeling_jobs
			SET lease_expires_at=now()+($1*interval '1 second'),
				updated_at=now()
			WHERE id=$2::uuid AND status='RUNNING'
			  AND lease_owner=$3 AND lease_token=$4::uuid`,
			int64(lease/time.Second), claim.ID, workerID, claim.LeaseToken)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return ErrProjectionLease
		}
		return nil
	})
}

func (worker *DWSModelingWorker) claim(
	ctx context.Context,
	tenantID, workerID string,
	lease time.Duration,
) (claim *dwsModelingClaim, err error) {
	err = database.WithTenantTx(ctx, worker.store.pool, tenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE platform.dws_modeling_jobs
			SET status='PENDING',attempt=0,next_attempt_at=now(),
				error_code='RESUMING_FROM_SELECTION',
				lease_owner='',lease_token=NULL,lease_expires_at=NULL,
				updated_at=now()
			WHERE status='RUNNING' AND lease_expires_at<=now()
			  AND jsonb_array_length(selection_json)>0`); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE platform.dws_modeling_jobs
			SET status='FAILED',error_code='LEASE_EXPIRED',
				lease_owner='',lease_token=NULL,lease_expires_at=NULL,
				completed_at=now(),updated_at=now()
			WHERE status='RUNNING' AND lease_expires_at<=now()
			  AND attempt>=max_attempts
			  AND jsonb_array_length(selection_json)=0`); err != nil {
			return err
		}
		item := dwsModelingClaim{TenantID: tenantID}
		var selectionJSON []byte
		err := tx.QueryRow(ctx, `WITH candidate AS (
				SELECT id FROM platform.dws_modeling_jobs
				WHERE (
				    status IN ('PENDING','WAITING_DEPENDENCY')
				    AND next_attempt_at<=now()
				  ) OR (
				    status='RUNNING' AND lease_expires_at<=now()
				    AND attempt<max_attempts
				  )
				ORDER BY next_attempt_at,created_at,id
				FOR UPDATE SKIP LOCKED LIMIT 1
			)
			UPDATE platform.dws_modeling_jobs AS job
			SET status='RUNNING',attempt=attempt+1,error_code='',
				lease_owner=$1,lease_token=public.gen_random_uuid(),
				lease_expires_at=now()+($2*interval '1 second'),
				updated_at=now()
			FROM candidate WHERE job.id=candidate.id
			RETURNING job.id::text,job.source_dwd_dataset_id::text,
				job.source_dwd_version_id::text,job.requested_by::text,
				job.lease_token::text,job.input_hash,job.selection_json,
				job.attempt,job.max_attempts`,
			workerID, int64(lease/time.Second),
		).Scan(
			&item.ID, &item.SourceDatasetID, &item.SourceVersionID,
			&item.ActorID, &item.LeaseToken, &item.InputHash,
			&selectionJSON, &item.Attempt, &item.MaxAttempts,
		)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if err := json.Unmarshal(selectionJSON, &item.Selections); err != nil {
			return err
		}
		claim = &item
		return nil
	})
	return claim, err
}

func (worker *DWSModelingWorker) process(
	ctx context.Context,
	claim dwsModelingClaim,
	workerID string,
) error {
	source, err := worker.datasets.Get(
		ctx, claim.TenantID, claim.SourceDatasetID,
	)
	if err != nil {
		return worker.finish(
			ctx, claim, workerID, "FAILED", "SOURCE_DATASET_UNAVAILABLE", nil,
		)
	}
	version, err := worker.datasets.GetVersion(
		ctx, claim.TenantID, claim.SourceDatasetID, claim.SourceVersionID,
	)
	if err != nil || version.Status != "PUBLISHED" ||
		version.Layer != dataset.LayerDWD ||
		source.CurrentPublishedVersionID != claim.SourceVersionID {
		return worker.finish(
			ctx, claim, workerID, "SKIPPED", "SUBJECT_CHANGED", nil,
		)
	}
	ready, err := worker.sourceReady(ctx, claim)
	if err != nil {
		return err
	}
	if !ready {
		return worker.waitForDependency(ctx, claim, workerID)
	}
	document, err := dataset.DecodeAndNormalize(version.DSL)
	if err != nil || document.FactContract == nil ||
		document.Dataset.SemanticContractVersion != "1.0" {
		return worker.finish(
			ctx, claim, workerID, "SKIPPED", "FACT_CONTRACT_MISSING", nil,
		)
	}
	eligible := autoEligibleTemplateCodes(document)
	if len(eligible) == 0 {
		return worker.finish(
			ctx, claim, workerID, "SKIPPED", "NO_SAFE_TEMPLATE", nil,
		)
	}
	if claim.InputHash != "" && claim.InputHash != version.DSLHash {
		return worker.finish(
			ctx, claim, workerID, "SKIPPED", "SUBJECT_CHANGED", nil,
		)
	}
	if len(claim.Selections) == 0 {
		selections := boundedTemplateSelection(eligible)
		requestID := ""
		if worker.selector != nil {
			selected, selectedRequestID, _ := worker.selector.Select(
				ctx, claim.TenantID, claim.ActorID, claim.SourceVersionID,
				document, eligible,
			)
			selections, requestID = selected, selectedRequestID
		}
		if len(selections) == 0 {
			return worker.finish(
				ctx, claim, workerID, "SKIPPED", "NO_MARKET_SCENARIO_SELECTED", nil,
			)
		}
		if err := worker.saveSelection(
			ctx, claim, workerID, version.DSLHash, requestID, selections,
		); err != nil {
			return err
		}
		claim.Selections = selections
		claim.InputHash = version.DSLHash
	}
	source.DSL = version.DSL
	results := make([]dwsModelingResult, 0, len(claim.Selections))
	generated, updated, skipped := 0, 0, 0
	for _, templateCode := range claim.Selections {
		prepared, buildErr := buildSingleFactDWSCandidate(
			source, claim.SourceVersionID, templateCode,
		)
		if buildErr != nil {
			skipped++
			results = append(results, dwsModelingResult{
				TemplateCode: templateCode, Action: "SKIPPED",
				ReasonCode: "TEMPLATE_CONTRACT_NOT_SATISFIED",
			})
			continue
		}
		datasetID, action, upsertErr := worker.upsertDWS(
			ctx, claim, templateCode, prepared,
		)
		if upsertErr != nil {
			skipped++
			results = append(results, dwsModelingResult{
				TemplateCode: templateCode, Action: "SKIPPED",
				ReasonCode: "DRAFT_CONFLICT",
			})
			continue
		}
		switch action {
		case "CREATED":
			generated++
		case "UPDATED":
			updated++
		default:
			skipped++
		}
		results = append(results, dwsModelingResult{
			TemplateCode: templateCode, DatasetID: datasetID, Action: action,
		})
	}
	status := "SUCCEEDED"
	if skipped > 0 && generated+updated > 0 {
		status = "PARTIAL"
	} else if skipped > 0 && generated+updated == 0 {
		status = "SKIPPED"
	}
	return worker.finishCounts(
		ctx, claim, workerID, status, "", results,
		generated, updated, skipped,
	)
}

func (worker *DWSModelingWorker) sourceReady(
	ctx context.Context,
	claim dwsModelingClaim,
) (ready bool, err error) {
	err = database.WithTenantTx(ctx, worker.store.pool, claim.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT EXISTS(
			SELECT 1
			FROM platform.dataset_versions AS version
			JOIN platform.datasets AS dataset
			  ON dataset.tenant_id=version.tenant_id
			 AND dataset.id=version.dataset_id
			 AND dataset.current_published_version_id=version.id
			 AND dataset.status='PUBLISHED' AND dataset.deleted_at IS NULL
			JOIN platform.dataset_materializations AS materialization
			  ON materialization.tenant_id=version.tenant_id
			 AND materialization.dataset_id=version.dataset_id
			 AND materialization.dataset_version_id=version.id
			 AND materialization.status='ACTIVE'
			 AND materialization.schema_hash=version.schema_hash
			WHERE version.id=$1::uuid AND version.dataset_id=$2::uuid
			  AND version.status='PUBLISHED' AND version.layer='DWD'
		)`, claim.SourceVersionID, claim.SourceDatasetID).Scan(&ready)
	})
	return ready, err
}

func (worker *DWSModelingWorker) waitForDependency(
	ctx context.Context,
	claim dwsModelingClaim,
	workerID string,
) error {
	return database.WithTenantTx(ctx, worker.store.pool, claim.TenantID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE platform.dws_modeling_jobs
			SET status='WAITING_DEPENDENCY',attempt=GREATEST(attempt-1,0),
				error_code='WAITING_ACTIVE_DWD_MATERIALIZATION',
				next_attempt_at=now()+interval '1 minute',
				lease_owner='',lease_token=NULL,lease_expires_at=NULL,
				updated_at=now()
			WHERE id=$1::uuid AND status='RUNNING'
			  AND lease_owner=$2 AND lease_token=$3::uuid`,
			claim.ID, workerID, claim.LeaseToken)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return ErrProjectionLease
		}
		return nil
	})
}

func (worker *DWSModelingWorker) saveSelection(
	ctx context.Context,
	claim dwsModelingClaim,
	workerID, inputHash, requestID string,
	selections []string,
) error {
	raw, err := json.Marshal(selections)
	if err != nil {
		return err
	}
	return database.WithTenantTx(ctx, worker.store.pool, claim.TenantID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE platform.dws_modeling_jobs
			SET input_hash=$1,selection_json=$2,
				ai_request_id=NULLIF($3,'')::uuid,updated_at=now()
			WHERE id=$4::uuid AND status='RUNNING'
			  AND lease_owner=$5 AND lease_token=$6::uuid
			  AND source_dwd_version_id=$7::uuid`,
			inputHash, raw, requestID, claim.ID, workerID,
			claim.LeaseToken, claim.SourceVersionID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return ErrProjectionLease
		}
		return nil
	})
}

type dwsOutput struct {
	DatasetID, LastGeneratedHash string
}

func (worker *DWSModelingWorker) upsertDWS(
	ctx context.Context,
	claim dwsModelingClaim,
	templateCode string,
	prepared dataset.Prepared,
) (string, string, error) {
	output, found, err := worker.getOutput(
		ctx, claim.TenantID, claim.SourceDatasetID, templateCode,
	)
	if err != nil {
		return "", "", err
	}
	if !found {
		recoveredID, recovered, err := worker.recoverUnlinkedGeneratedDWS(
			ctx, claim.TenantID, claim.ActorID,
			prepared.Document.Dataset.Code, prepared.DSLHash,
		)
		if err != nil {
			return "", "", err
		}
		if recovered {
			if err := worker.saveOutput(
				ctx, claim, templateCode, recoveredID,
				prepared.DSLHash, "CREATED",
			); err != nil {
				return "", "", err
			}
			return recoveredID, "UNCHANGED", nil
		}
		record, err := worker.datasets.Create(
			ctx, claim.TenantID, claim.ActorID, dataset.CreateInput{
				Code:        prepared.Document.Dataset.Code,
				Name:        prepared.Document.Dataset.Name,
				Description: prepared.Document.Dataset.Description,
				Type:        prepared.Document.Dataset.Type,
				Layer:       prepared.Document.Dataset.Layer,
				DSL:         prepared.DSLJSON,
			},
		)
		if err != nil {
			return "", "", err
		}
		if err := worker.saveOutput(
			ctx, claim, templateCode, record.ID, prepared.DSLHash, "CREATED",
		); err != nil {
			return "", "", err
		}
		return record.ID, "CREATED", nil
	}
	current, err := worker.datasets.Get(ctx, claim.TenantID, output.DatasetID)
	if err != nil {
		return "", "", err
	}
	if current.DSLHash != output.LastGeneratedHash {
		_ = worker.saveOutput(
			ctx, claim, templateCode, current.ID,
			output.LastGeneratedHash, "MANUAL_OWNED",
		)
		return current.ID, "MANUAL_OWNED", nil
	}
	action := "UNCHANGED"
	if current.DSLHash != prepared.DSLHash {
		current, err = worker.datasets.Update(
			ctx, claim.TenantID, claim.ActorID, current.ID,
			dataset.UpdateInput{
				Name:            prepared.Document.Dataset.Name,
				Description:     prepared.Document.Dataset.Description,
				ExpectedVersion: current.Version, DSL: prepared.DSLJSON,
			},
		)
		if err != nil {
			return "", "", err
		}
		action = "UPDATED"
	}
	if err := worker.saveOutput(
		ctx, claim, templateCode, current.ID, prepared.DSLHash, action,
	); err != nil {
		return "", "", err
	}
	return current.ID, action, nil
}

// recoverUnlinkedGeneratedDWS closes the only cross-service crash window:
// dataset.Create may commit before dws_modeling_outputs is saved. Recovery
// adopts an asset only when deterministic code, creator, DWS layer and exact
// generated DSL hash all match; an ordinary human draft is never adopted.
func (worker *DWSModelingWorker) recoverUnlinkedGeneratedDWS(
	ctx context.Context,
	tenantID, actorID, code, dslHash string,
) (datasetID string, found bool, err error) {
	err = database.WithTenantTx(ctx, worker.store.pool, tenantID, func(tx pgx.Tx) error {
		scanErr := tx.QueryRow(ctx, `SELECT dataset.id::text
			FROM platform.datasets AS dataset
			JOIN platform.dataset_versions AS version
			  ON version.tenant_id=dataset.tenant_id
			 AND version.id=dataset.current_draft_version_id
			 AND version.dataset_id=dataset.id
			WHERE dataset.code=$1
			  AND dataset.layer='DWS'
			  AND dataset.created_by=$2::uuid
			  AND dataset.deleted_at IS NULL
			  AND version.schema_hash=$3
			ORDER BY dataset.id
			LIMIT 1`, code, actorID, dslHash).Scan(&datasetID)
		if errors.Is(scanErr, pgx.ErrNoRows) {
			return nil
		}
		if scanErr == nil {
			found = true
		}
		return scanErr
	})
	return datasetID, found, err
}

func (worker *DWSModelingWorker) getOutput(
	ctx context.Context,
	tenantID, sourceDatasetID, templateCode string,
) (item dwsOutput, found bool, err error) {
	err = database.WithTenantTx(ctx, worker.store.pool, tenantID, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `SELECT dws_dataset_id::text,
				last_generated_dsl_hash
			FROM platform.dws_modeling_outputs
			WHERE source_dwd_dataset_id=$1::uuid AND template_code=$2`,
			sourceDatasetID, templateCode,
		).Scan(&item.DatasetID, &item.LastGeneratedHash)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err == nil {
			found = true
		}
		return err
	})
	return item, found, err
}

func (worker *DWSModelingWorker) saveOutput(
	ctx context.Context,
	claim dwsModelingClaim,
	templateCode, dwsDatasetID, dslHash, action string,
) error {
	return database.WithTenantTx(ctx, worker.store.pool, claim.TenantID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `INSERT INTO platform.dws_modeling_outputs(
				tenant_id,source_dwd_dataset_id,template_code,dws_dataset_id,
				last_source_dwd_version_id,last_job_id,
				last_generated_dsl_hash,last_action
			) VALUES(
				platform.current_tenant_id(),$1::uuid,$2,$3::uuid,
				$4::uuid,$5::uuid,$6,$7
			)
			ON CONFLICT(tenant_id,source_dwd_dataset_id,template_code)
			DO UPDATE SET
				last_source_dwd_version_id=EXCLUDED.last_source_dwd_version_id,
				last_job_id=EXCLUDED.last_job_id,
				last_generated_dsl_hash=EXCLUDED.last_generated_dsl_hash,
				last_action=EXCLUDED.last_action,updated_at=now()
			WHERE platform.dws_modeling_outputs.dws_dataset_id=
				EXCLUDED.dws_dataset_id`,
			claim.SourceDatasetID, templateCode, dwsDatasetID,
			claim.SourceVersionID, claim.ID, dslHash, action)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return ErrConflict
		}
		return nil
	})
}

func (worker *DWSModelingWorker) finish(
	ctx context.Context,
	claim dwsModelingClaim,
	workerID, status, errorCode string,
	results []dwsModelingResult,
) error {
	return worker.finishCounts(
		ctx, claim, workerID, status, errorCode, results, 0, 0, 0,
	)
}

func (worker *DWSModelingWorker) finishCounts(
	ctx context.Context,
	claim dwsModelingClaim,
	workerID, status, errorCode string,
	results []dwsModelingResult,
	generated, updated, skipped int,
) error {
	if results == nil {
		results = []dwsModelingResult{}
	}
	resultJSON, err := json.Marshal(map[string]any{
		"outputs": results, "sourceVersionId": claim.SourceVersionID,
	})
	if err != nil {
		return err
	}
	return database.WithTenantTx(ctx, worker.store.pool, claim.TenantID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE platform.dws_modeling_jobs
			SET status=$1,error_code=$2,result_json=$3,
				generated_count=$4,updated_count=$5,skipped_count=$6,
				lease_owner='',lease_token=NULL,lease_expires_at=NULL,
				completed_at=now(),updated_at=now()
			WHERE id=$7::uuid AND status='RUNNING'
			  AND lease_owner=$8 AND lease_token=$9::uuid`,
			status, errorCode, resultJSON, generated, updated, skipped,
			claim.ID, workerID, claim.LeaseToken)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return ErrProjectionLease
		}
		return nil
	})
}

func RunDWSModelingWorker(
	ctx context.Context,
	logger *slog.Logger,
	worker *DWSModelingWorker,
	workerID string,
	pollInterval time.Duration,
) {
	if pollInterval <= 0 {
		pollInterval = 2 * time.Second
	}
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		processed := false
		tenantIDs, err := worker.TenantIDs(ctx)
		if err != nil {
			logger.Error("list DWS modeling tenants", "error", err)
		} else {
			for _, tenantID := range tenantIDs {
				didProcess, processErr := worker.ProcessNext(
					ctx, tenantID, workerID, 2*time.Minute,
				)
				if processErr != nil {
					logger.Error(
						"process DWS modeling",
						"tenant_id", tenantID, "error", processErr,
					)
				}
				processed = processed || didProcess
			}
		}
		if processed {
			timer.Reset(10 * time.Millisecond)
		} else {
			timer.Reset(pollInterval)
		}
	}
}
