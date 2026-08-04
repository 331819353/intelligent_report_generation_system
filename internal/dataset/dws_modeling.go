package dataset

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	aiplatform "intelligent-report-generation-system/internal/ai"
	"intelligent-report-generation-system/internal/platform/database"
)

var (
	errDWSModelingInvalid   = errors.New("DWS modeling request is invalid")
	errDWSModelingLeaseLost = errors.New("DWS modeling lease was lost")
)

const dwsRecordCountMetricCode = "record_count"

// DWSModelingAI is the narrow shared-AI boundary used by theme modeling.
type DWSModelingAI interface {
	Configured() bool
	Invoke(context.Context, aiplatform.Invocation) (aiplatform.InvocationResult, error)
}

type dwsModelingPlan struct {
	Name           string   `json:"name"`
	Description    string   `json:"description"`
	Subject        string   `json:"subject"`
	DimensionCodes []string `json:"dimensionCodes"`
	MetricCodes    []string `json:"metricCodes"`
}

type dwsScopeReference struct {
	DatasetID string `json:"datasetId"`
	VersionID string `json:"versionId"`
	DSLHash   string `json:"dslHash"`
	Code      string `json:"code"`
	Name      string `json:"name"`
}

type dwsModelingScope struct {
	GroupKey    string              `json:"groupKey"`
	DomainID    string              `json:"domainId"`
	SubjectName string              `json:"subjectName"`
	DWD         []dwsScopeReference `json:"dwd"`
	DIM         []dwsScopeReference `json:"dim"`
}

type dwsPlanningContext struct {
	Reference dwsScopeReference
	Document  Document
}

// OrchestratedDWSModelingPlanner lets the LLM choose the business dimensions,
// measures and vocabulary. The worker then materializes those bounded choices
// into the typed dataset DSL instead of accepting model-produced SQL.
type OrchestratedDWSModelingPlanner struct{ ai DWSModelingAI }

func NewOrchestratedDWSModelingPlanner(ai DWSModelingAI) *OrchestratedDWSModelingPlanner {
	return &OrchestratedDWSModelingPlanner{ai: ai}
}

func (planner *OrchestratedDWSModelingPlanner) Plan(
	ctx context.Context,
	tenantID, actorID, scopeHash string,
	source Record,
	document Document,
	dimensions []dwsPlanningContext,
) (dwsModelingPlan, string, error) {
	fallback, err := defaultDWSModelingPlan(source, document)
	if err != nil {
		return dwsModelingPlan{}, "", err
	}
	if planner == nil || planner.ai == nil || !planner.ai.Configured() {
		return fallback, "", nil
	}
	fieldSummary := func(value Document) []map[string]string {
		fields := make([]map[string]string, 0, min(64, len(value.Fields)))
		for _, field := range value.Fields {
			if len(fields) == 64 {
				break
			}
			fields = append(fields, map[string]string{
				"code": field.Code, "name": field.Name, "role": field.Role,
				"canonicalType": field.CanonicalType, "semanticType": field.SemanticType,
			})
		}
		return fields
	}
	dimensionSummary := make([]map[string]any, 0, len(dimensions))
	for _, dimension := range dimensions {
		dimensionSummary = append(dimensionSummary, map[string]any{
			"code": dimension.Reference.Code, "name": dimension.Reference.Name,
			"fields": fieldSummary(dimension.Document),
		})
	}
	payload, err := json.Marshal(map[string]any{
		"dwd": map[string]any{
			"code": source.Code, "name": source.Name,
			"businessAction": func() string {
				if document.FactContract == nil {
					return ""
				}
				return document.FactContract.BusinessAction
			}(),
			"fields": fieldSummary(document),
		},
		"dimensionContext": dimensionSummary,
	})
	if err != nil {
		return fallback, "", nil
	}
	temperature := 0.0
	result, invokeErr := planner.ai.Invoke(ctx, aiplatform.Invocation{
		TenantID: tenantID, ActorID: actorID,
		Purpose:       aiplatform.PurposeDatasetDAGGeneration,
		PromptVersion: "dataset-dws-theme-dag-v1",
		ResourceType:  "DATASET_MODELING_SCOPE", ResourceID: scopeHash,
		Request: aiplatform.ProviderRequest{
			Messages: []aiplatform.Message{
				{Role: aiplatform.MessageRoleSystem, Parts: []aiplatform.ContentPart{{
					Type: aiplatform.ContentTypeText,
					Text: `你负责把一张已发布 DWD 规划成一张可评审的 DWS 主题汇总 DAG。选择最多三个业务维度和最多十六个安全度量；编码只能逐字复制 dwd.fields.code。DIM 只用于理解业务语义，不能作为输出字段来源。优先保留事件时间、可加度量和清晰的业务主题。不得输出 SQL、DDL、物理表名、数据值或未提供的字段。name、description、subject 使用简洁中文。`,
				}}},
				{Role: aiplatform.MessageRoleUser, Parts: []aiplatform.ContentPart{{
					Type: aiplatform.ContentTypeText, Text: string(payload),
				}}},
			},
			ResponseSchema: aiplatform.JSONSchema{
				Name: "dataset_dws_theme_plan",
				Schema: json.RawMessage(`{
					"type":"object","additionalProperties":false,
					"required":["name","description","subject","dimensionCodes","metricCodes"],
					"properties":{
						"name":{"type":"string","minLength":1,"maxLength":120},
						"description":{"type":"string","minLength":1,"maxLength":500},
						"subject":{"type":"string","minLength":1,"maxLength":120},
						"dimensionCodes":{"type":"array","maxItems":3,"uniqueItems":true,"items":{"type":"string"}},
						"metricCodes":{"type":"array","maxItems":16,"uniqueItems":true,"items":{"type":"string"}}
					}
				}`),
			},
			Temperature: &temperature, MaxOutputTokens: 300,
		},
	})
	if invokeErr != nil {
		// The shared AI boundary already owns provider retries and failure audit.
		// A deterministic safe plan keeps this durable workflow from wedging.
		return fallback, "", nil
	}
	var proposed dwsModelingPlan
	if json.Unmarshal(result.ProviderResult.Content, &proposed) != nil {
		return fallback, result.RequestID, nil
	}
	validated := validateDWSModelingPlan(proposed, fallback, document)
	return validated, result.RequestID, nil
}

func defaultDWSModelingPlan(source Record, document Document) (dwsModelingPlan, error) {
	if document.Dataset.Layer != LayerDWD || document.FactContract == nil {
		return dwsModelingPlan{}, errDWSModelingInvalid
	}
	plan := dwsModelingPlan{
		Name:        source.Name + "主题汇总",
		Description: "基于当前已发布 DWD 版本生成的可评审主题汇总 DAG 草稿",
		Subject:     strings.TrimSpace(document.Dataset.Subject),
	}
	if plan.Subject == "" {
		plan.Subject = source.Name
	}
	for _, field := range document.Fields {
		if strings.EqualFold(field.Role, "DIMENSION") && len(plan.DimensionCodes) < 3 {
			plan.DimensionCodes = append(plan.DimensionCodes, field.Code)
		}
	}
	fields := indexDWSFields(document.Fields)
	for _, measure := range document.FactContract.AtomicMeasures {
		field, exists := fields[strings.ToLower(measure.Field)]
		if !exists || !strings.EqualFold(field.Role, "MEASURE") ||
			strings.EqualFold(measure.Additivity, "NON_ADDITIVE") {
			continue
		}
		plan.MetricCodes = append(plan.MetricCodes, field.Code)
		if len(plan.MetricCodes) == 16 {
			break
		}
	}
	if len(plan.MetricCodes) == 0 && dwsSupportsRecordCount(document) {
		plan.MetricCodes = []string{dwsRecordCountMetricCode}
	}
	if len(plan.MetricCodes) == 0 {
		return dwsModelingPlan{}, errDWSModelingInvalid
	}
	return plan, nil
}

func validateDWSModelingPlan(
	proposed, fallback dwsModelingPlan,
	document Document,
) dwsModelingPlan {
	result := fallback
	if value := safeDWSModelText(proposed.Name, 120); value != "" {
		result.Name = value
	}
	if value := safeDWSModelText(proposed.Description, 500); value != "" {
		result.Description = value
	}
	if value := safeDWSModelText(proposed.Subject, 120); value != "" {
		result.Subject = value
	}
	allowedDimensions := map[string]string{}
	allowedMetrics := map[string]string{}
	for _, field := range document.Fields {
		key := strings.ToLower(strings.TrimSpace(field.Code))
		if strings.EqualFold(field.Role, "DIMENSION") {
			allowedDimensions[key] = field.Code
		}
	}
	if document.FactContract != nil {
		fields := indexDWSFields(document.Fields)
		for _, measure := range document.FactContract.AtomicMeasures {
			field, exists := fields[strings.ToLower(measure.Field)]
			if exists && strings.EqualFold(field.Role, "MEASURE") &&
				!strings.EqualFold(measure.Additivity, "NON_ADDITIVE") {
				allowedMetrics[strings.ToLower(field.Code)] = field.Code
			}
		}
	}
	if dwsSupportsRecordCount(document) {
		allowedMetrics[dwsRecordCountMetricCode] = dwsRecordCountMetricCode
	}
	if selected := boundedDWSSelections(proposed.DimensionCodes, allowedDimensions, 3); len(selected) > 0 {
		result.DimensionCodes = selected
	}
	if selected := boundedDWSSelections(proposed.MetricCodes, allowedMetrics, 16); len(selected) > 0 {
		result.MetricCodes = selected
	}
	return result
}

func boundedDWSSelections(values []string, allowed map[string]string, limit int) []string {
	result := make([]string, 0, min(limit, len(values)))
	seen := map[string]bool{}
	for _, value := range values {
		key := strings.ToLower(strings.TrimSpace(value))
		if allowed[key] == "" || seen[key] || len(result) == limit {
			continue
		}
		seen[key] = true
		result = append(result, allowed[key])
	}
	return result
}

func safeDWSModelText(value string, limit int) string {
	value = strings.TrimSpace(strings.Map(func(character rune) rune {
		if character < 32 || character == 127 {
			return -1
		}
		return character
	}, value))
	runes := []rune(value)
	if len(runes) > limit {
		value = string(runes[:limit])
	}
	return value
}

type dwsModelingClaim struct {
	ID, TenantID, SourceDatasetID, SourceVersionID string
	ActorID, LeaseToken, ScopeHash                 string
	Scope                                          dwsModelingScope
}

// DWSModelingWorker consumes theme-modeling jobs and only creates or updates a
// DWS draft. Publication approval remains the sole path that can enqueue a
// materialized warehouse build.
type DWSModelingWorker struct {
	store   *PostgresStore
	service *Service
	planner *OrchestratedDWSModelingPlanner
}

func NewDWSModelingWorker(
	store *PostgresStore,
	planner *OrchestratedDWSModelingPlanner,
) *DWSModelingWorker {
	return &DWSModelingWorker{store: store, service: NewService(store), planner: planner}
}

func (worker *DWSModelingWorker) TenantIDs(ctx context.Context) ([]string, error) {
	if worker == nil || worker.store == nil || worker.store.pool == nil {
		return nil, errDWSModelingInvalid
	}
	rows, err := worker.store.pool.Query(ctx, `SELECT id::text
		FROM platform.tenants WHERE status='ACTIVE' AND deleted_at IS NULL ORDER BY id`)
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
	if worker == nil || worker.store == nil || worker.service == nil || worker.planner == nil ||
		uuid.Validate(tenantID) != nil || strings.TrimSpace(workerID) == "" ||
		len(workerID) > 128 || lease < time.Second || lease > time.Hour {
		return false, errDWSModelingInvalid
	}
	claim, err := worker.claim(ctx, tenantID, workerID, lease)
	if err != nil || claim == nil {
		return false, err
	}
	processErr := worker.process(ctx, *claim, workerID)
	return true, processErr
}

func (worker *DWSModelingWorker) claim(
	ctx context.Context,
	tenantID, workerID string,
	lease time.Duration,
) (claim *dwsModelingClaim, err error) {
	err = database.WithTenantTx(ctx, worker.store.pool, tenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE platform.dws_modeling_jobs
			SET status='FAILED',error_code='LEASE_EXPIRED',
				error_message='主题建模任务租约已过期且达到最大尝试次数',
				lease_owner='',lease_token=NULL,lease_expires_at=NULL,
				completed_at=now(),updated_at=now()
			WHERE status='RUNNING' AND lease_expires_at<=now()
			  AND attempt>=max_attempts`); err != nil {
			return err
		}
		item := dwsModelingClaim{TenantID: tenantID}
		var scopeJSON []byte
		scanErr := tx.QueryRow(ctx, `WITH candidate AS (
				SELECT id FROM platform.dws_modeling_jobs
				WHERE (status='PENDING' AND next_attempt_at<=now())
				   OR (status='RUNNING' AND lease_expires_at<=now() AND attempt<max_attempts)
				ORDER BY next_attempt_at,created_at,id
				FOR UPDATE SKIP LOCKED LIMIT 1
			)
			UPDATE platform.dws_modeling_jobs AS job
			SET status='RUNNING',attempt=attempt+1,
				lease_owner=$1,lease_token=public.gen_random_uuid(),
				lease_expires_at=now()+($2*interval '1 second'),
				started_at=COALESCE(started_at,now()),
				error_code='',error_message='',updated_at=now()
			FROM candidate WHERE job.id=candidate.id
			RETURNING job.id::text,job.source_dwd_dataset_id::text,
				job.source_dwd_version_id::text,job.requested_by::text,
				job.lease_token::text,job.scope_hash,job.source_scope`,
			workerID, int64(lease/time.Second),
		).Scan(
			&item.ID, &item.SourceDatasetID, &item.SourceVersionID,
			&item.ActorID, &item.LeaseToken, &item.ScopeHash, &scopeJSON,
		)
		if errors.Is(scanErr, pgx.ErrNoRows) {
			return nil
		}
		if scanErr != nil {
			return scanErr
		}
		if err := json.Unmarshal(scopeJSON, &item.Scope); err != nil {
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
	if uuid.Validate(claim.Scope.DomainID) != nil || len(claim.Scope.DWD) != 1 {
		return worker.finish(ctx, claim, workerID, "FAILED", "SCOPE_INVALID", "", "", "")
	}
	ctx = database.WithAccessContext(ctx, claim.ActorID, claim.Scope.DomainID)
	record, err := worker.store.Get(ctx, claim.TenantID, claim.SourceDatasetID)
	if err != nil {
		return worker.finish(ctx, claim, workerID, "FAILED", "SOURCE_UNAVAILABLE", "", "", "")
	}
	version, err := worker.store.GetVersion(
		ctx, claim.TenantID, claim.SourceDatasetID, claim.SourceVersionID,
	)
	if err != nil || version.Status != "PUBLISHED" || version.Layer != LayerDWD ||
		record.CurrentPublishedVersionID != claim.SourceVersionID {
		return worker.finish(ctx, claim, workerID, "SKIPPED", "SOURCE_CHANGED", "", "", "")
	}
	record.DSL = version.DSL
	record.DSLHash = version.DSLHash
	document, err := DecodeAndNormalize(version.DSL)
	if err != nil || document.FactContract == nil {
		return worker.finish(ctx, claim, workerID, "FAILED", "SOURCE_CONTRACT_INVALID", "", "", "")
	}
	dimensions := worker.loadDimensionContext(ctx, claim)
	plan, requestID, planErr := worker.planner.Plan(
		ctx, claim.TenantID, claim.ActorID, claim.ScopeHash,
		record, document, dimensions,
	)
	if planErr != nil {
		return worker.finish(ctx, claim, workerID, "FAILED", "DAG_PLAN_INVALID", requestID, "", "")
	}
	prepared, buildErr := buildDWSThemeCandidate(record, version.ID, document, plan)
	if buildErr != nil {
		return worker.finish(ctx, claim, workerID, "FAILED", "DAG_BUILD_INVALID", requestID, "", "")
	}
	datasetID, action, upsertErr := worker.upsertDraft(ctx, claim, prepared)
	if upsertErr != nil {
		return worker.finish(ctx, claim, workerID, "FAILED", "DRAFT_CONFLICT", requestID, "", "")
	}
	status := "SUCCEEDED"
	if action == "MANUAL_OWNED" {
		status = "SKIPPED"
	}
	return worker.finish(ctx, claim, workerID, status, "", requestID, datasetID, action)
}

func (worker *DWSModelingWorker) loadDimensionContext(
	ctx context.Context,
	claim dwsModelingClaim,
) []dwsPlanningContext {
	result := make([]dwsPlanningContext, 0, len(claim.Scope.DIM))
	for _, reference := range claim.Scope.DIM {
		record, err := worker.store.Get(ctx, claim.TenantID, reference.DatasetID)
		if err != nil || record.CurrentPublishedVersionID != reference.VersionID {
			continue
		}
		version, err := worker.store.GetVersion(
			ctx, claim.TenantID, reference.DatasetID, reference.VersionID,
		)
		if err != nil || version.Status != "PUBLISHED" || version.Layer != LayerDIM ||
			(reference.DSLHash != "" && reference.DSLHash != version.DSLHash) {
			continue
		}
		document, err := DecodeAndNormalize(version.DSL)
		if err == nil {
			result = append(result, dwsPlanningContext{Reference: reference, Document: document})
		}
	}
	return result
}

type dwsModelingOutput struct {
	DatasetID, LastGeneratedHash string
}

func (worker *DWSModelingWorker) upsertDraft(
	ctx context.Context,
	claim dwsModelingClaim,
	prepared Prepared,
) (string, string, error) {
	output, found, err := worker.getOutput(ctx, claim.TenantID, claim.SourceDatasetID)
	if err != nil {
		return "", "", err
	}
	if !found {
		record, err := worker.service.Create(ctx, claim.TenantID, claim.ActorID, CreateInput{
			Code: prepared.Document.Dataset.Code, Name: prepared.Document.Dataset.Name,
			Description: prepared.Document.Dataset.Description,
			Type:        prepared.Document.Dataset.Type, Layer: LayerDWS, DSL: prepared.DSLJSON,
		})
		if err != nil {
			return "", "", err
		}
		if err := worker.saveOutput(ctx, claim, record.ID, prepared.DSLHash, "CREATED"); err != nil {
			return "", "", err
		}
		return record.ID, "CREATED", nil
	}
	current, err := worker.store.Get(ctx, claim.TenantID, output.DatasetID)
	if err != nil {
		return "", "", err
	}
	if current.DSLHash != output.LastGeneratedHash {
		_ = worker.saveOutput(ctx, claim, current.ID, output.LastGeneratedHash, "MANUAL_OWNED")
		return current.ID, "MANUAL_OWNED", nil
	}
	action := "UNCHANGED"
	if current.DSLHash != prepared.DSLHash {
		updated, err := worker.service.Update(
			ctx, claim.TenantID, claim.ActorID, current.ID, UpdateInput{
				Name:            prepared.Document.Dataset.Name,
				Description:     prepared.Document.Dataset.Description,
				ExpectedVersion: current.Version, DSL: prepared.DSLJSON,
			},
		)
		if err != nil {
			return "", "", err
		}
		current = updated
		action = "UPDATED"
	}
	if err := worker.saveOutput(ctx, claim, current.ID, prepared.DSLHash, action); err != nil {
		return "", "", err
	}
	return current.ID, action, nil
}

func (worker *DWSModelingWorker) getOutput(
	ctx context.Context,
	tenantID, sourceDatasetID string,
) (output dwsModelingOutput, found bool, err error) {
	err = database.WithTenantTx(ctx, worker.store.pool, tenantID, func(tx pgx.Tx) error {
		scanErr := tx.QueryRow(ctx, `SELECT dws_dataset_id::text,last_generated_dsl_hash
			FROM platform.dws_modeling_outputs
			WHERE source_dwd_dataset_id=$1::uuid`, sourceDatasetID,
		).Scan(&output.DatasetID, &output.LastGeneratedHash)
		if errors.Is(scanErr, pgx.ErrNoRows) {
			return nil
		}
		if scanErr == nil {
			found = true
		}
		return scanErr
	})
	return output, found, err
}

func (worker *DWSModelingWorker) saveOutput(
	ctx context.Context,
	claim dwsModelingClaim,
	datasetID, dslHash, action string,
) error {
	return database.WithTenantTx(ctx, worker.store.pool, claim.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO platform.dws_modeling_outputs(
				tenant_id,source_dwd_dataset_id,dws_dataset_id,
				last_source_dwd_version_id,last_job_id,last_generated_dsl_hash,last_action
			) VALUES($1,$2,$3,$4,$5,$6,$7)
			ON CONFLICT(tenant_id,source_dwd_dataset_id) DO UPDATE SET
				dws_dataset_id=EXCLUDED.dws_dataset_id,
				last_source_dwd_version_id=EXCLUDED.last_source_dwd_version_id,
				last_job_id=EXCLUDED.last_job_id,
				last_generated_dsl_hash=EXCLUDED.last_generated_dsl_hash,
				last_action=EXCLUDED.last_action,updated_at=now()`,
			claim.TenantID, claim.SourceDatasetID, datasetID,
			claim.SourceVersionID, claim.ID, dslHash, action,
		)
		return err
	})
}

func (worker *DWSModelingWorker) finish(
	ctx context.Context,
	claim dwsModelingClaim,
	workerID, status, errorCode, requestID, datasetID, action string,
) error {
	generated, updated, skipped := 0, 0, 0
	switch action {
	case "CREATED":
		generated = 1
	case "UPDATED":
		updated = 1
	case "MANUAL_OWNED":
		skipped = 1
	}
	resultJSON, _ := json.Marshal(map[string]any{
		"datasetId": datasetID, "action": action,
		"sourceVersionId": claim.SourceVersionID, "scopeHash": claim.ScopeHash,
	})
	message := ""
	if errorCode != "" {
		message = "主题建模未完成，请查看任务状态后重试"
	}
	return database.WithTenantTx(ctx, worker.store.pool, claim.TenantID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE platform.dws_modeling_jobs SET
				status=$1,error_code=$2,error_message=$3,
				ai_request_id=NULLIF($4,'')::uuid,result_json=$5,
				generated_count=$6,updated_count=$7,skipped_count=$8,
				lease_owner='',lease_token=NULL,lease_expires_at=NULL,
				completed_at=now(),updated_at=now()
			WHERE id=$9::uuid AND status='RUNNING'
			  AND lease_owner=$10 AND lease_token=$11::uuid`,
			status, errorCode, message, requestID, resultJSON,
			generated, updated, skipped, claim.ID, workerID, claim.LeaseToken,
		)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return errDWSModelingLeaseLost
		}
		return nil
	})
}

func buildDWSThemeCandidate(
	source Record,
	sourceVersionID string,
	sourceDocument Document,
	plan dwsModelingPlan,
) (Prepared, error) {
	if sourceDocument.Dataset.Layer != LayerDWD || sourceDocument.FactContract == nil {
		return Prepared{}, errDWSModelingInvalid
	}
	fields := indexDWSFields(sourceDocument.Fields)
	projection := make([]string, 0, len(sourceDocument.Fields))
	for _, field := range sourceDocument.Fields {
		projection = append(projection, field.Code)
	}
	outputFields := []Field{}
	groupBy := []string{}
	grainFields := []string{}
	conformedDimensions := []string{}
	timeCode := dwsEffectiveTimeField(sourceDocument)
	if timeField, exists := fields[strings.ToLower(timeCode)]; exists {
		output := timeField
		output.ID, output.Code, output.Name, output.Role = "field_stat_date", "stat_date", "统计日期", "TIME"
		output.CanonicalType = "DATE"
		output.Expression = Expression{Type: "DATE_TRUNC", Unit: "DAY", Argument: &Expression{
			Type: "FIELD_REF", NodeID: "fact", Field: timeField.Code,
		}}
		outputFields = append(outputFields, output)
		groupBy = append(groupBy, output.ID)
		grainFields = append(grainFields, output.Code)
		conformedDimensions = append(conformedDimensions, output.Code)
	}
	for _, code := range plan.DimensionCodes {
		field, exists := fields[strings.ToLower(code)]
		if !exists || !strings.EqualFold(field.Role, "DIMENSION") {
			continue
		}
		field.ID = "field_" + safeDWSIdentifier(field.Code, "dimension")
		field.Expression = Expression{Type: "FIELD_REF", NodeID: "fact", Field: field.Code}
		outputFields = append(outputFields, field)
		groupBy = append(groupBy, field.ID)
		grainFields = append(grainFields, field.Code)
		conformedDimensions = append(conformedDimensions, field.Code)
	}
	if len(grainFields) == 0 {
		visible := true
		outputFields = append(outputFields, Field{
			ID: "field_summary_scope", Code: "summary_scope", Name: "汇总范围",
			Role: "DIMENSION", CanonicalType: "STRING", Nullable: false, Visible: &visible,
			Expression: Expression{Type: "LITERAL", Value: "ALL"},
		})
		groupBy = append(groupBy, "field_summary_scope")
		grainFields = append(grainFields, "summary_scope")
		conformedDimensions = append(conformedDimensions, "summary_scope")
	}
	selectedMetrics := map[string]bool{}
	for _, code := range plan.MetricCodes {
		selectedMetrics[strings.ToLower(code)] = true
	}
	analysisMeasures := []AnalysisMeasureContract{}
	for _, contract := range sourceDocument.FactContract.AtomicMeasures {
		field, exists := fields[strings.ToLower(contract.Field)]
		if !exists || !selectedMetrics[strings.ToLower(field.Code)] ||
			!strings.EqualFold(field.Role, "MEASURE") || strings.EqualFold(contract.Additivity, "NON_ADDITIVE") {
			continue
		}
		aggregation := strings.ToUpper(strings.TrimSpace(contract.DefaultAggregation))
		if aggregation != "SUM" && aggregation != "MIN" && aggregation != "MAX" && aggregation != "AVG" {
			aggregation = "SUM"
		}
		field.ID = "field_" + safeDWSIdentifier(field.Code, "measure")
		field.Expression = Expression{Type: "AGGREGATE", Function: aggregation, Argument: &Expression{
			Type: "FIELD_REF", NodeID: "fact", Field: field.Code,
		}}
		outputFields = append(outputFields, field)
		analysisMeasures = append(analysisMeasures, AnalysisMeasureContract{
			Field: field.Code, SourceNodeIDs: []string{"fact"}, Aggregation: aggregation,
			Additivity: contract.Additivity, ValueBehavior: contract.ValueBehavior,
			TimeAggregation: contract.TimeAggregation, Unit: contract.Unit, Currency: contract.Currency,
		})
	}
	if selectedMetrics[dwsRecordCountMetricCode] || len(analysisMeasures) == 0 {
		if dwsSupportsRecordCount(sourceDocument) {
			visible := true
			outputFields = append(outputFields, Field{
				ID: "field_record_count", Code: dwsRecordCountMetricCode, Name: "事实记录数",
				Description: "按当前 DWS 输出粒度统计的原子事实记录数",
				Role:        "MEASURE", CanonicalType: "INTEGER", Nullable: false, Visible: &visible,
				Expression: Expression{Type: "AGGREGATE", Function: "COUNT"},
			})
			analysisMeasures = append(analysisMeasures, AnalysisMeasureContract{
				Field: dwsRecordCountMetricCode, SourceNodeIDs: []string{"fact"},
				Aggregation: "COUNT", Additivity: "ADDITIVE",
				ValueBehavior: "FLOW", TimeAggregation: "SUM",
			})
		}
	}
	if len(analysisMeasures) == 0 {
		return Prepared{}, errDWSModelingInvalid
	}
	code := generatedDWSThemeCode(source.Code)
	document := Document{
		DSLVersion: DSLVersion,
		Dataset: Descriptor{
			Code: code, Name: plan.Name, Description: plan.Description,
			Domain: sourceDocument.Dataset.Domain, Subject: plan.Subject,
			Type: sourceDocument.Dataset.Type, Layer: LayerDWS,
			SemanticContractVersion: "1.0",
		},
		Nodes: []Node{{
			ID: "fact", Type: "DATASET", DatasetVersionID: sourceVersionID,
			Alias: "fact", Projection: projection, SourceFilters: []SourceFilter{},
		}},
		Joins: []Join{}, Fields: outputFields, Filters: []Filter{}, GroupBy: groupBy,
		Having: []Filter{}, Sorts: []Sort{}, Parameters: []Parameter{},
		OutputGrain: OutputGrain{
			Description: "每行代表 " + strings.Join(grainFields, " + "),
			KeyFields:   grainFields,
		},
		AnalysisContract: &AnalysisContract{
			Intent: "DRILLDOWN", InputMode: "SINGLE_FACT",
			CommonGrainFields: grainFields, ConformedDimensions: conformedDimensions,
			Measures: analysisMeasures,
		},
		ExecutionPolicy: ExecutionPolicy{
			Mode: "MATERIALIZED_PREFERRED", TimeoutMS: 30000,
			PreviewLimit: 10, ResultLimit: 100000, CacheTTLSeconds: 300,
			Materialization: MaterializationPolicy{Enabled: true, RefreshMode: "MANUAL"},
		},
	}
	if len(outputFields) > 0 && outputFields[0].Code == "stat_date" {
		document.OutputGrain.TimeField = "stat_date"
		document.OutputGrain.DefaultTimeGrain = "DAY"
		document.AnalysisContract.TimeField = "stat_date"
		document.AnalysisContract.TimeGrain = "DAY"
		document.Sorts = []Sort{{FieldID: "field_stat_date", Direction: "ASC"}}
	}
	raw, err := json.Marshal(document)
	if err != nil {
		return Prepared{}, err
	}
	return Prepare(raw)
}

func indexDWSFields(fields []Field) map[string]Field {
	result := make(map[string]Field, len(fields))
	for _, field := range fields {
		result[strings.ToLower(strings.TrimSpace(field.Code))] = field
	}
	return result
}

func dwsEffectiveTimeField(document Document) string {
	fields := indexDWSFields(document.Fields)
	candidates := []string{}
	if document.FactContract != nil {
		candidates = append(candidates, document.FactContract.EventTimeField)
	}
	candidates = append(candidates, document.OutputGrain.TimeField)
	for _, code := range candidates {
		field, exists := fields[strings.ToLower(strings.TrimSpace(code))]
		if exists && (field.CanonicalType == "DATE" || field.CanonicalType == "DATETIME") {
			return field.Code
		}
	}
	return ""
}

func dwsSupportsRecordCount(document Document) bool {
	if document.FactContract == nil || len(document.FactContract.GrainKeyFields) == 0 {
		return false
	}
	fields := indexDWSFields(document.Fields)
	for _, code := range document.FactContract.GrainKeyFields {
		if _, exists := fields[strings.ToLower(code)]; !exists {
			return false
		}
	}
	return true
}

func safeDWSIdentifier(value, fallback string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var builder strings.Builder
	lastUnderscore := false
	for _, character := range value {
		valid := character >= 'a' && character <= 'z' || character >= '0' && character <= '9'
		if valid {
			builder.WriteRune(character)
			lastUnderscore = false
		} else if builder.Len() > 0 && !lastUnderscore {
			builder.WriteByte('_')
			lastUnderscore = true
		}
	}
	result := strings.Trim(builder.String(), "_")
	if result == "" {
		return fallback
	}
	return result
}

func generatedDWSThemeCode(sourceCode string) string {
	base := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(sourceCode)), "dwd_")
	value := "dws_" + safeDWSIdentifier(base, "theme") + "_summary"
	if len(value) <= 63 {
		return value
	}
	sum := sha256.Sum256([]byte(value))
	return value[:54] + "_" + hex.EncodeToString(sum[:])[:8]
}
