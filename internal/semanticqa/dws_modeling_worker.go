package semanticqa

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	aiplatform "intelligent-report-generation-system/internal/ai"
	"intelligent-report-generation-system/internal/dataset"
	"intelligent-report-generation-system/internal/platform/database"
)

const (
	dwsModelingConcurrency        = 4
	dwsSingleFactPlanningVersion  = "dws-single-fact-planning-v5"
	dwsGroupedFactPlanningVersion = "dws-group-planning-v5"
)

type DWSAnalysisSelector interface {
	Select(
		context.Context,
		string,
		string,
		string,
		dwsModelingScope,
		[]dwsPlanningAsset,
		[]dwsPlanningAsset,
		[]string,
	) ([]dwsAnalysisSelection, string, error)
}

type dwsAnalysisSelection struct {
	TemplateCode   string     `json:"templateCode"`
	DimensionCodes []string   `json:"dimensionCodes"`
	MetricCodes    []string   `json:"metricCodes"`
	GroupingMode   string     `json:"groupingMode,omitempty"`
	GroupingSets   [][]string `json:"groupingSets,omitempty"`
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
	tenantID, actorID, scopeHash string,
	scope dwsModelingScope,
	facts, dimensions []dwsPlanningAsset,
	eligible []string,
) ([]dwsAnalysisSelection, string, error) {
	selectedScope := strings.HasPrefix(scope.GroupKey, "selected-dws:")
	maxSelections := 1
	fallback := []dwsAnalysisSelection{}
	if selectedScope {
		maxSelections = 3
		fallback = boundedTemplateSelection(eligible)
	} else if len(facts) == 1 {
		fallback = consolidatedDWSSelection(eligible, facts[0].Document)
	} else {
		fallback = boundedTemplateSelection(eligible)
	}
	if selector == nil || selector.ai == nil || !selector.ai.Configured() {
		return fallback, "", nil
	}
	assetMetadata := func(assets []dwsPlanningAsset) []map[string]any {
		result := make([]map[string]any, 0, len(assets))
		for _, asset := range assets {
			fields := make([]map[string]string, 0, min(48, len(asset.Document.Fields)))
			for _, field := range asset.Document.Fields {
				if len(fields) == 48 {
					break
				}
				fields = append(fields, map[string]string{
					"code": field.Code, "name": field.Name, "role": field.Role,
					"canonicalType": field.CanonicalType, "unit": field.Unit,
				})
			}
			item := map[string]any{
				"datasetId": asset.Record.ID, "versionId": asset.VersionID,
				"code": asset.Record.Code, "name": asset.Record.Name, "fields": fields,
			}
			if asset.Document.FactContract != nil {
				item["businessAction"] = asset.Document.FactContract.BusinessAction
				item["grainKeyFields"] = asset.Document.FactContract.GrainKeyFields
				item["eventTimeField"] = asset.Document.FactContract.EventTimeField
				fieldsByCode := make(
					map[string]dataset.Field, len(asset.Document.Fields),
				)
				for _, field := range asset.Document.Fields {
					fieldsByCode[field.Code] = field
				}
				atomicMeasures := make(
					[]dataset.AtomicMeasureContract, 0,
					len(asset.Document.FactContract.AtomicMeasures),
				)
				for _, measure := range asset.Document.FactContract.AtomicMeasures {
					if field, exists := fieldsByCode[measure.Field]; exists {
						measure = effectiveDWSAtomicMeasure(measure, field)
					}
					atomicMeasures = append(atomicMeasures, measure)
				}
				item["atomicMeasures"] = atomicMeasures
				if supportsSafeRecordCount(asset.Document) {
					item["derivedMetrics"] = []map[string]string{{
						"code": dwsRecordCountMetricCode,
						"name": "事实记录数", "aggregation": "COUNT",
					}}
				}
			}
			result = append(result, item)
		}
		return result
	}
	payload, err := json.Marshal(map[string]any{
		"groupKey": scope.GroupKey, "domainCode": scope.DomainCode,
		"subjectCode": scope.SubjectCode, "subjectName": scope.SubjectName,
		"dwdFacts": assetMetadata(facts), "dimensionContext": assetMetadata(dimensions),
		"eligibleTemplateCodes": eligible,
		"maximumSelections":     maxSelections,
	})
	if err != nil {
		return fallback, "", nil
	}
	temperature := 0.0
	promptVersion := dwsSingleFactPlanningVersion
	systemPrompt := `你负责基于当前一张 DWD 的事实结构与任务范围内 DIM 语义上下文规划 DWS。默认自动建模必须只返回一个统一主题表：在同一事实粒度可解释的维度优先放入同一张多维表，不要按趋势、分布、排名等消费场景拆成多表。dimensionCodes 只能逐字复制 dwdFacts.fields 中非度量、非时间字段的 code；metricCodes 只能逐字复制 dwdFacts.fields 中 MEASURE 字段或 dwdFacts.derivedMetrics 的 code。DWS 必须保存一份由全部所选维度构成的唯一最细粒度，groupingMode 只能是 STANDARD；CUBE、ROLLUP 和 GROUPING_SETS 由查询层按需汇总，不能写入同一张物化表。累计值和时点值必须依据 atomicMeasures.valueBehavior/timeAggregation 设计，始终保留原生时间粒度，绝不能跨时间 SUM。不得选择跨事实比较，不得创造编码，不得输出 SQL、DDL、物理表名或数据值。`
	if selectedScope {
		systemPrompt = `你负责基于用户明确框选的一张 DWD 与所选 DIM 规划 DWS。保持用户明确范围，可从 eligibleTemplateCodes 中选择最多三个分析意图，并为每个意图选择维度和指标。DWS 必须保存由全部所选维度构成的唯一最细粒度，groupingMode 只能是 STANDARD；维度组合与汇总由查询层完成。累计值和时点值必须保留原生时间粒度且不得跨时间 SUM。字段 code 只能逐字复制输入，不得创造编码、SQL、DDL、物理表名或数据值。`
	}
	if len(facts) > 1 {
		promptVersion = dwsGroupedFactPlanningVersion
		systemPrompt = `你负责把显式选择的全部 DWD 与所选 DIM 作为一个不可拆分的联合分析范围来规划 DWS。必须同时分析全部输入，先判断多张事实是否能在共同粒度安全聚合；再为每个意图明确选择公共维度和指标。字段 code 只能逐字复制输入。不得把输入拆成逐表任务，不得创造编码，不得输出 SQL、DDL、物理表名或数据值；缺少共同粒度时返回空 selections。`
	} else if len(facts) == 0 && len(dimensions) == 1 {
		promptVersion = "dws-dimension-count-planning-v1"
		systemPrompt = `你负责把当前一张 DIM 识别为无事实实体计数主题。只能选择 ENTITY_COUNT，并从 dimensionContext.fields 中选择最多三个非度量、非时间、非实体键说明字段作为 dimensionCodes；metricCodes 必须且只能是 ["entity_count"]。不得臆造其他指标、事实、字段、SQL 或物理对象。`
	}
	result, err := selector.ai.Invoke(ctx, aiplatform.Invocation{
		TenantID: tenantID, ActorID: actorID,
		Purpose:       aiplatform.PurposeDatasetDAGGeneration,
		PromptVersion: promptVersion,
		ResourceType:  "DATASET_MODELING_SCOPE", ResourceID: scopeHash,
		Request: aiplatform.ProviderRequest{
			Messages: []aiplatform.Message{
				{
					Role: aiplatform.MessageRoleSystem,
					Parts: []aiplatform.ContentPart{{
						Type: aiplatform.ContentTypeText,
						Text: systemPrompt,
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
					"required":["selections"],
					"properties":{"selections":{
						"type":"array","maxItems":3,"uniqueItems":true,
						"items":{"type":"object","additionalProperties":false,
							"required":["templateCode","dimensionCodes","metricCodes"],
							"properties":{
								"templateCode":{"enum":[
									"TREND","PERIOD_COMPARISON","DISTRIBUTION",
									"RANKING","DRILLDOWN","ANOMALY",
									"MULTI_FACT_COMPARISON","ENTITY_COUNT"
								]},
								"dimensionCodes":{"type":"array","maxItems":3,
									"uniqueItems":true,"items":{"type":"string"}},
								"metricCodes":{"type":"array","maxItems":16,
									"uniqueItems":true,"items":{"type":"string"}},
								"groupingMode":{"enum":["STANDARD"]},
								"groupingSets":{"type":"array","maxItems":16,
									"items":{"type":"array","maxItems":3,
										"uniqueItems":true,"items":{"type":"string"}}}
							}
						}
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
		Selections []dwsAnalysisSelection `json:"selections"`
	}
	if json.Unmarshal(result.ProviderResult.Content, &response) != nil {
		return fallback, result.RequestID, nil
	}
	selected := validatedAnalysisSelection(
		response.Selections, eligible, facts, dimensions,
		maxSelections, !selectedScope,
	)
	if len(response.Selections) > 0 && len(selected) == 0 {
		return fallback, result.RequestID, nil
	}
	return selected, result.RequestID, nil
}

func boundedTemplateSelection(eligible []string) []dwsAnalysisSelection {
	if len(eligible) > 3 {
		eligible = eligible[:3]
	}
	result := make([]dwsAnalysisSelection, 0, len(eligible))
	for _, code := range eligible {
		result = append(result, dwsAnalysisSelection{
			TemplateCode: code, DimensionCodes: []string{}, MetricCodes: []string{},
		})
	}
	return result
}

func consolidatedDWSSelection(
	eligible []string,
	document dataset.Document,
) []dwsAnalysisSelection {
	if len(eligible) == 0 {
		return nil
	}
	selectedTemplate := eligible[0]
	hasSemiAdditive := false
	if document.FactContract != nil {
		fieldsByCode := make(map[string]dataset.Field, len(document.Fields))
		for _, field := range document.Fields {
			fieldsByCode[field.Code] = field
		}
		for _, measure := range document.FactContract.AtomicMeasures {
			if field, exists := fieldsByCode[measure.Field]; exists {
				measure = effectiveDWSAtomicMeasure(measure, field)
			}
			if measure.Additivity == "SEMI_ADDITIVE" {
				hasSemiAdditive = true
				break
			}
		}
	}
	preferred := []string{"DRILLDOWN", "TREND"}
	if hasSemiAdditive {
		preferred = []string{"TREND", "DRILLDOWN"}
	}
	for _, candidate := range preferred {
		for _, code := range eligible {
			if code == candidate {
				selectedTemplate = candidate
				break
			}
		}
		if selectedTemplate == candidate {
			break
		}
	}
	selection := dwsAnalysisSelection{
		TemplateCode:   selectedTemplate,
		DimensionCodes: []string{},
		MetricCodes:    []string{},
		GroupingMode:   "STANDARD",
		GroupingSets:   [][]string{},
	}
	for _, field := range document.Fields {
		if field.Role == "DIMENSION" && len(selection.DimensionCodes) < 3 {
			selection.DimensionCodes = append(
				selection.DimensionCodes, field.Code,
			)
		}
	}
	if document.FactContract != nil {
		fieldsByCode := make(map[string]dataset.Field, len(document.Fields))
		for _, field := range document.Fields {
			fieldsByCode[field.Code] = field
		}
		for _, measure := range document.FactContract.AtomicMeasures {
			if field, exists := fieldsByCode[measure.Field]; exists {
				measure = effectiveDWSAtomicMeasure(measure, field)
			}
			if measure.Additivity == "NON_ADDITIVE" ||
				len(selection.MetricCodes) == 16 {
				continue
			}
			selection.MetricCodes = append(
				selection.MetricCodes, measure.Field,
			)
		}
	}
	if len(selection.MetricCodes) == 0 &&
		supportsSafeRecordCount(document) {
		selection.MetricCodes = []string{dwsRecordCountMetricCode}
	}
	return []dwsAnalysisSelection{selection}
}

func validatedAnalysisSelection(
	selected []dwsAnalysisSelection,
	eligible []string,
	facts, dimensions []dwsPlanningAsset,
	maxSelections int,
	consolidated bool,
) []dwsAnalysisSelection {
	allowed := map[string]bool{}
	for _, code := range eligible {
		allowed[code] = true
	}
	allowedDimensions := map[string]string{}
	allowedMetrics := map[string]string{}
	selectableAssets := facts
	if len(selectableAssets) == 0 {
		selectableAssets = dimensions
	}
	for _, asset := range selectableAssets {
		for _, field := range asset.Document.Fields {
			switch field.Role {
			case "MEASURE":
				allowedMetrics[strings.ToLower(field.Code)] = field.Code
			case "TIME":
				// Time is selected by the template contract, not as a regular
				// conformed dimension.
			default:
				if len(facts) == 0 || field.Role == "DIMENSION" {
					allowedDimensions[strings.ToLower(field.Code)] = field.Code
				}
			}
		}
		if supportsSafeRecordCount(asset.Document) {
			allowedMetrics[dwsRecordCountMetricCode] = dwsRecordCountMetricCode
		}
	}
	allowedMetrics["entity_count"] = "entity_count"
	result := []dwsAnalysisSelection{}
	seen := map[string]bool{}
	for _, selection := range selected {
		code := strings.ToUpper(strings.TrimSpace(selection.TemplateCode))
		if !allowed[code] || seen[code] || len(result) == maxSelections {
			continue
		}
		seen[code] = true
		selection.TemplateCode = code
		selection.DimensionCodes = validatedDWSFieldCodes(
			selection.DimensionCodes, allowedDimensions, 3,
		)
		selection.MetricCodes = validatedDWSFieldCodes(
			selection.MetricCodes, allowedMetrics, 16,
		)
		if code == "ENTITY_COUNT" {
			selection.MetricCodes = []string{"entity_count"}
		}
		if consolidated && len(facts) == 1 {
			defaults := consolidatedDWSSelection(
				[]string{code}, facts[0].Document,
			)
			if len(defaults) == 1 {
				if len(selection.DimensionCodes) == 0 {
					selection.DimensionCodes = defaults[0].DimensionCodes
				}
				if len(selection.MetricCodes) == 0 {
					selection.MetricCodes = defaults[0].MetricCodes
				}
				if strings.TrimSpace(selection.GroupingMode) == "" {
					selection.GroupingMode = defaults[0].GroupingMode
				}
			}
		}
		selection = validatedDWSGrouping(selection, consolidated)
		result = append(result, selection)
	}
	return result
}

func validatedDWSGrouping(
	selection dwsAnalysisSelection,
	consolidated bool,
) dwsAnalysisSelection {
	_ = consolidated
	// The warehouse materialization contract has one declared output grain.
	// Persisting subtotal rows from CUBE/ROLLUP/GROUPING_SETS in the same table
	// makes that grain nullable and ambiguous, and cannot pass the immutable
	// unique/non-null quality gate. Queryruntime safely rolls up additive DWS
	// measures from this complete detail grain when fewer dimensions are asked.
	selection.GroupingMode = "STANDARD"
	selection.GroupingSets = nil
	return selection
}

func validatedDWSFieldCodes(
	candidates []string,
	allowed map[string]string,
	limit int,
) []string {
	result := make([]string, 0, min(limit, len(candidates)))
	seen := map[string]bool{}
	for _, candidate := range candidates {
		key := strings.ToLower(strings.TrimSpace(candidate))
		exact := allowed[key]
		if exact == "" || seen[key] || len(result) == limit {
			continue
		}
		seen[key] = true
		result = append(result, exact)
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
	ActorID, LeaseToken, InputHash, GroupKey       string
	ScopeHash                                      string
	Scope                                          dwsModelingScope
	Selections                                     []dwsAnalysisSelection
	Attempt, MaxAttempts                           int
}

type dwsScopeAsset struct {
	DatasetID string          `json:"datasetId"`
	VersionID string          `json:"versionId"`
	DSLHash   string          `json:"dslHash"`
	Code      string          `json:"code"`
	Name      string          `json:"name"`
	DSL       json.RawMessage `json:"dsl"`
}

type dwsModelingScope struct {
	GroupKey    string          `json:"groupKey"`
	DomainID    string          `json:"domainId"`
	DomainCode  string          `json:"domainCode"`
	DomainName  string          `json:"domainName"`
	SubjectCode string          `json:"subjectCode"`
	SubjectName string          `json:"subjectName"`
	DWD         []dwsScopeAsset `json:"dwd"`
	DIM         []dwsScopeAsset `json:"dim"`
}

type dwsPlanningAsset struct {
	Record    dataset.Record
	VersionID string
	DSLHash   string
	Document  dataset.Document
}

type dwsSourceReadiness struct {
	Ready          bool
	TerminalFailed bool
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
				error_message='检测到已保存的分析选择，正在恢复 DWS 建模任务',
				lease_owner='',lease_token=NULL,lease_expires_at=NULL,
				updated_at=now()
			WHERE status='RUNNING' AND lease_expires_at<=now()
			  AND jsonb_array_length(selection_json)>0`); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE platform.dws_modeling_jobs
			SET status='FAILED',error_code='LEASE_EXPIRED',
				error_message='任务租约已过期且达到最大尝试次数，请人工重试',
				lease_owner='',lease_token=NULL,lease_expires_at=NULL,
				completed_at=now(),updated_at=now()
			WHERE status='RUNNING' AND lease_expires_at<=now()
			  AND attempt>=max_attempts
			  AND jsonb_array_length(selection_json)=0`); err != nil {
			return err
		}
		item := dwsModelingClaim{TenantID: tenantID}
		var selectionJSON, scopeJSON []byte
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
			SET status='RUNNING',attempt=attempt+1,
				error_code='',error_message='',
				lease_owner=$1,lease_token=public.gen_random_uuid(),
				lease_expires_at=now()+($2*interval '1 second'),
				updated_at=now()
			FROM candidate WHERE job.id=candidate.id
			RETURNING job.id::text,job.source_dwd_dataset_id::text,
				job.source_dwd_version_id::text,job.requested_by::text,
				job.lease_token::text,job.input_hash,job.selection_json,
				job.group_key,job.source_scope,job.scope_hash,
				job.attempt,job.max_attempts`,
			workerID, int64(lease/time.Second),
		).Scan(
			&item.ID, &item.SourceDatasetID, &item.SourceVersionID,
			&item.ActorID, &item.LeaseToken, &item.InputHash,
			&selectionJSON, &item.GroupKey, &scopeJSON, &item.ScopeHash,
			&item.Attempt, &item.MaxAttempts,
		)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if err := json.Unmarshal(selectionJSON, &item.Selections); err != nil {
			var legacy []string
			if legacyErr := json.Unmarshal(selectionJSON, &legacy); legacyErr != nil {
				return err
			}
			item.Selections = boundedTemplateSelection(legacy)
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
	if uuid.Validate(claim.Scope.DomainID) != nil {
		return worker.finish(
			ctx, claim, workerID, "FAILED", "DOMAIN_CONTEXT_INVALID", nil,
		)
	}
	ctx = database.WithAccessContext(ctx, claim.ActorID, claim.Scope.DomainID)
	facts, dimensions, current, err := worker.loadPlanningScope(ctx, claim)
	if err != nil {
		return worker.finish(
			ctx, claim, workerID, "FAILED", "SOURCE_DATASET_UNAVAILABLE", nil,
		)
	}
	if !current {
		return worker.finish(
			ctx, claim, workerID, "SKIPPED", "SUBJECT_CHANGED", nil,
		)
	}
	factlessDimension := len(facts) == 0 && len(dimensions) == 1
	readinessSources := facts
	if factlessDimension {
		readinessSources = dimensions
	}
	readiness, err := worker.sourceReady(ctx, claim, readinessSources)
	if err != nil {
		return err
	}
	if readiness.TerminalFailed {
		return worker.finish(
			ctx, claim, workerID, "FAILED", "DWD_MATERIALIZATION_FAILED", nil,
		)
	}
	if !readiness.Ready {
		return worker.waitForDependency(ctx, claim, workerID)
	}
	if len(facts) == 0 && !factlessDimension {
		return worker.finish(
			ctx, claim, workerID, "SKIPPED", "FACT_CONTRACT_MISSING", nil,
		)
	}
	modelingFacts, eligible := eligibleDWSAnalysisScope(
		facts, factlessDimension,
	)
	if len(eligible) == 0 {
		return worker.finish(
			ctx, claim, workerID, "SKIPPED", "NO_SAFE_TEMPLATE", nil,
		)
	}
	if claim.InputHash != "" && claim.InputHash != claim.ScopeHash {
		return worker.finish(
			ctx, claim, workerID, "SKIPPED", "SUBJECT_CHANGED", nil,
		)
	}
	if len(claim.Selections) == 0 {
		selections := boundedTemplateSelection(eligible)
		if !strings.HasPrefix(claim.GroupKey, "selected-dws:") &&
			len(modelingFacts) == 1 {
			selections = consolidatedDWSSelection(
				eligible, modelingFacts[0].Document,
			)
		}
		requestID := ""
		if worker.selector != nil {
			selected, selectedRequestID, _ := worker.selector.Select(
				ctx, claim.TenantID, claim.ActorID, claim.ScopeHash,
				claim.Scope, facts, dimensions, eligible,
			)
			selections, requestID = selected, selectedRequestID
		}
		if len(selections) == 0 {
			return worker.finish(
				ctx, claim, workerID, "SKIPPED", "NO_MARKET_SCENARIO_SELECTED", nil,
			)
		}
		if err := worker.saveSelection(
			ctx, claim, workerID, claim.ScopeHash, requestID, selections,
		); err != nil {
			return err
		}
		claim.Selections = selections
		claim.InputHash = claim.ScopeHash
	}
	results := make([]dwsModelingResult, 0, len(claim.Selections))
	generated, updated, skipped, unchanged := 0, 0, 0, 0
	multiFactDatasetType := ""
	if len(modelingFacts) > 1 {
		multiFactDatasetType, err = worker.resolveDWSPhysicalSourceType(
			ctx, claim, modelingFacts,
		)
		if err != nil {
			return err
		}
	}
	for _, selection := range claim.Selections {
		templateCode := selection.TemplateCode
		var prepared dataset.Prepared
		var buildErr error
		if factlessDimension {
			prepared, buildErr = buildDimensionCountDWSCandidate(
				dimensions[0].Record, dimensions[0].VersionID,
				selection.DimensionCodes,
			)
		} else if len(modelingFacts) > 1 {
			prepared, buildErr = buildMultiFactDWSCandidate(
				modelingFacts, claim.Scope, templateCode, multiFactDatasetType,
			)
		} else {
			prepared, buildErr = buildSingleFactDWSCandidateWithSelection(
				modelingFacts[0].Record, modelingFacts[0].VersionID,
				templateCode, selection.DimensionCodes, selection.MetricCodes,
				selection.GroupingMode, selection.GroupingSets,
			)
		}
		if buildErr == nil {
			prepared, buildErr = scopeSelectedDWSCandidate(
				prepared, claim.GroupKey,
			)
		}
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
			slog.WarnContext(
				ctx, "DWS modeling output upsert failed",
				"job_id", claim.ID,
				"template_code", templateCode,
				"error", upsertErr,
			)
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
		case "UNCHANGED":
			// 计入 processed 以保持任务中心的进度合同，但它是成功的幂等
			// 结果，不应让整项任务显示为“已跳过”。
			skipped++
			unchanged++
		default:
			skipped++
		}
		results = append(results, dwsModelingResult{
			TemplateCode: templateCode, DatasetID: datasetID, Action: action,
		})
	}
	status := dwsModelingCompletionStatus(
		generated, updated, skipped, unchanged,
	)
	return worker.finishCounts(
		ctx, claim, workerID, status, "", results,
		generated, updated, skipped,
	)
}

func dwsModelingCompletionStatus(
	generated, updated, skipped, unchanged int,
) string {
	failedOrOwned := max(0, skipped-unchanged)
	successful := generated + updated + unchanged
	if failedOrOwned > 0 && successful > 0 {
		return "PARTIAL"
	}
	if failedOrOwned > 0 {
		return "SKIPPED"
	}
	return "SUCCEEDED"
}

func (worker *DWSModelingWorker) resolveDWSPhysicalSourceType(
	ctx context.Context,
	claim dwsModelingClaim,
	facts []dwsPlanningAsset,
) (datasetType string, err error) {
	versionIDs := make([]string, 0, len(facts))
	for _, fact := range facts {
		versionIDs = append(versionIDs, fact.VersionID)
	}
	err = database.WithTenantTx(ctx, worker.store.pool, claim.TenantID, func(tx pgx.Tx) error {
		var sourceCount int
		if err := tx.QueryRow(ctx, `WITH RECURSIVE lineage AS (
				SELECT version.id,version.dsl_json,
					ARRAY[version.id] AS path,0 AS depth
				FROM platform.dataset_versions AS version
				WHERE version.id=ANY($1::uuid[])
				  AND version.status='PUBLISHED'
				UNION ALL
				SELECT upstream.id,upstream.dsl_json,
					lineage.path||upstream.id,lineage.depth+1
				FROM lineage
				CROSS JOIN LATERAL jsonb_array_elements(
					COALESCE(lineage.dsl_json->'nodes','[]'::jsonb)
				) AS node
				JOIN platform.dataset_versions AS upstream
				  ON upstream.tenant_id=platform.current_tenant_id()
				 AND upstream.id=(node->>'datasetVersionId')::uuid
				WHERE node->>'type'='DATASET'
				  AND lineage.depth<16
				  AND NOT upstream.id=ANY(lineage.path)
			)
			SELECT count(DISTINCT node->>'datasourceId')::int
			FROM lineage
			CROSS JOIN LATERAL jsonb_array_elements(
				COALESCE(lineage.dsl_json->'nodes','[]'::jsonb)
			) AS node
			WHERE node->>'type'='TABLE'
			  AND btrim(COALESCE(node->>'datasourceId',''))<>''`,
			versionIDs,
		).Scan(&sourceCount); err != nil {
			return err
		}
		if sourceCount < 1 {
			return ErrUnprovenPath
		}
		datasetType = "SINGLE_SOURCE"
		if sourceCount > 1 {
			datasetType = "CROSS_SOURCE"
		}
		return nil
	})
	return datasetType, err
}

func eligibleDWSAnalysisScope(
	facts []dwsPlanningAsset,
	factlessDimension bool,
) ([]dwsPlanningAsset, []string) {
	if factlessDimension {
		return nil, []string{"ENTITY_COUNT"}
	}
	if len(facts) == 1 {
		return facts, autoEligibleTemplateCodes(facts[0].Document)
	}
	if len(facts) > 1 {
		eligibleFacts := multiFactEligibleSources(facts)
		// 显式多事实范围是不可拆分的合同。任一事实不能安全进入共同粒度时，
		// 整个范围应拒绝模板，而不是静默丢弃该事实后降级成单事实主题。
		if len(eligibleFacts) == len(facts) {
			return facts, []string{"MULTI_FACT_COMPARISON"}
		}
	}
	return nil, nil
}

func scopeSelectedDWSCandidate(
	prepared dataset.Prepared,
	groupKey string,
) (dataset.Prepared, error) {
	if !strings.HasPrefix(groupKey, "selected-dws:") {
		return prepared, nil
	}
	return recodeDWSCandidate(
		prepared,
		scopedDWSCode(prepared.Document.Dataset.Code, groupKey),
	)
}

func scopedDWSCode(baseCode, groupKey string) string {
	sum := sha256.Sum256([]byte(groupKey))
	suffix := hex.EncodeToString(sum[:])[:8]
	baseCode = strings.TrimRight(strings.TrimSpace(baseCode), "_")
	if len(baseCode) > 54 {
		baseCode = strings.TrimRight(baseCode[:54], "_")
	}
	return baseCode + "_" + suffix
}

func recodeDWSCandidate(
	prepared dataset.Prepared,
	code string,
) (dataset.Prepared, error) {
	if prepared.Document.Dataset.Code == code {
		return prepared, nil
	}
	document := prepared.Document
	document.Dataset.Code = code
	raw, err := json.Marshal(document)
	if err != nil {
		return dataset.Prepared{}, err
	}
	return dataset.Prepare(raw)
}

func (worker *DWSModelingWorker) loadPlanningScope(
	ctx context.Context,
	claim dwsModelingClaim,
) (facts, dimensions []dwsPlanningAsset, current bool, err error) {
	load := func(reference dwsScopeAsset, layer dataset.Layer) (dwsPlanningAsset, bool, error) {
		record, loadErr := worker.datasets.Get(ctx, claim.TenantID, reference.DatasetID)
		if loadErr != nil {
			return dwsPlanningAsset{}, false, loadErr
		}
		version, loadErr := worker.datasets.GetVersion(
			ctx, claim.TenantID, reference.DatasetID, reference.VersionID,
		)
		if loadErr != nil {
			return dwsPlanningAsset{}, false, loadErr
		}
		if version.Status != "PUBLISHED" || version.Layer != layer ||
			record.CurrentPublishedVersionID != reference.VersionID ||
			reference.DSLHash != "" && reference.DSLHash != version.DSLHash {
			return dwsPlanningAsset{}, false, nil
		}
		document, decodeErr := dataset.DecodeAndNormalize(version.DSL)
		if decodeErr != nil {
			return dwsPlanningAsset{}, false, nil
		}
		if strings.TrimSpace(document.Dataset.Domain) == "" {
			domainErr := database.WithTenantTx(
				ctx, worker.store.pool, claim.TenantID,
				func(tx pgx.Tx) error {
					return tx.QueryRow(ctx, `SELECT
							platform.dataset_version_effective_domain($1::uuid)`,
						reference.VersionID,
					).Scan(&document.Dataset.Domain)
				},
			)
			if domainErr != nil {
				return dwsPlanningAsset{}, false, domainErr
			}
		}
		if layer == dataset.LayerDWD && (document.FactContract == nil ||
			document.Dataset.SemanticContractVersion != "1.0") {
			return dwsPlanningAsset{}, false, nil
		}
		record.DSL = version.DSL
		return dwsPlanningAsset{
			Record: record, VersionID: version.ID,
			DSLHash: version.DSLHash, Document: document,
		}, true, nil
	}
	for _, reference := range claim.Scope.DWD {
		asset, valid, loadErr := load(reference, dataset.LayerDWD)
		if loadErr != nil || !valid {
			return nil, nil, false, loadErr
		}
		facts = append(facts, asset)
	}
	domain := ""
	for _, fact := range facts {
		currentDomain := strings.TrimSpace(fact.Document.Dataset.Domain)
		if currentDomain == "" {
			return nil, nil, false, nil
		}
		if domain == "" {
			domain = currentDomain
		} else if !strings.EqualFold(domain, currentDomain) {
			return nil, nil, false, nil
		}
	}
	for _, reference := range claim.Scope.DIM {
		asset, valid, loadErr := load(reference, dataset.LayerDIM)
		if loadErr != nil || !valid {
			return nil, nil, false, loadErr
		}
		// 默认全量任务提供当前批次的 DIM 清单；显式选择任务只提供所选
		// DIM。只有和事实处于同一领域的维度才可进入联合分析上下文。
		assetDomain := strings.TrimSpace(asset.Document.Dataset.Domain)
		if domain == "" {
			domain = assetDomain
		}
		if assetDomain == "" || !strings.EqualFold(assetDomain, domain) {
			continue
		}
		dimensions = append(dimensions, asset)
	}
	return facts, dimensions, len(facts) > 0 ||
		len(facts) == 0 && len(dimensions) == 1, nil
}

func (worker *DWSModelingWorker) sourceReady(
	ctx context.Context,
	claim dwsModelingClaim,
	sources []dwsPlanningAsset,
) (readiness dwsSourceReadiness, err error) {
	err = database.WithTenantTx(ctx, worker.store.pool, claim.TenantID, func(tx pgx.Tx) error {
		readiness.Ready = true
		for _, source := range sources {
			var available bool
			var latestBuildStatus string
			layer := source.Document.Dataset.Layer
			if layer != dataset.LayerDWD && layer != dataset.LayerDIM {
				return ErrInvalidRequest
			}
			if err := tx.QueryRow(ctx, `SELECT
				EXISTS(
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
					  AND version.status='PUBLISHED' AND version.layer=$3
				),
				COALESCE((
					SELECT run.status
					FROM platform.dataset_build_runs AS run
					WHERE run.dataset_version_id=$1::uuid
					  AND run.dataset_id=$2::uuid
					ORDER BY run.created_at DESC,run.id DESC
					LIMIT 1
				),'')`,
				source.VersionID, source.Record.ID, layer,
			).Scan(&available, &latestBuildStatus); err != nil {
				return err
			}
			if !available {
				readiness.Ready = false
				if latestBuildStatus == "FAILED" ||
					latestBuildStatus == "CANCELLED" {
					readiness.TerminalFailed = true
				}
			}
		}
		return nil
	})
	return readiness, err
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
				error_message='等待主题建模的上游发布版本完成物化；物化转为可用后会自动继续',
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
	selections []dwsAnalysisSelection,
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
		ctx, claim.TenantID, claim.GroupKey, templateCode,
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
	// 旧版显式范围尚未在物理编码中携带范围后缀。输出已经建立所有权
	// 映射时继续固定其现有编码，允许新 worker 幂等接管而不改写资产身份。
	if current.Code != prepared.Document.Dataset.Code {
		prepared, err = recodeDWSCandidate(prepared, current.Code)
		if err != nil {
			return "", "", err
		}
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
	tenantID, groupKey, templateCode string,
) (item dwsOutput, found bool, err error) {
	err = database.WithTenantTx(ctx, worker.store.pool, tenantID, func(tx pgx.Tx) error {
		// Dataset deletion is intentionally soft, while modeling outputs keep
		// their foreign-key row. A later manual modeling run must not treat
		// that tombstoned dataset as the current generated output: discard the
		// stale link so the deterministic create/recovery path can rebuild it.
		if _, err := tx.Exec(ctx, `DELETE FROM platform.dws_modeling_outputs AS output
			USING platform.datasets AS generated
			WHERE output.group_key=$1
			  AND output.template_code=$2
			  AND generated.tenant_id=output.tenant_id
			  AND generated.id=output.dws_dataset_id
			  AND generated.deleted_at IS NOT NULL`,
			groupKey, templateCode,
		); err != nil {
			return err
		}
		err := tx.QueryRow(ctx, `SELECT dws_dataset_id::text,
				last_generated_dsl_hash
			FROM platform.dws_modeling_outputs
			WHERE group_key=$1 AND template_code=$2`,
			groupKey, templateCode,
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
				last_generated_dsl_hash,last_action,group_key
			) VALUES(
				platform.current_tenant_id(),$1::uuid,$2,$3::uuid,
				$4::uuid,$5::uuid,$6,$7,$8
			)
			ON CONFLICT(tenant_id,group_key,template_code)
			DO UPDATE SET
				source_dwd_dataset_id=EXCLUDED.source_dwd_dataset_id,
				last_source_dwd_version_id=EXCLUDED.last_source_dwd_version_id,
				last_job_id=EXCLUDED.last_job_id,
				last_generated_dsl_hash=EXCLUDED.last_generated_dsl_hash,
				last_action=EXCLUDED.last_action,updated_at=now()
			WHERE platform.dws_modeling_outputs.dws_dataset_id=
				EXCLUDED.dws_dataset_id`,
			claim.SourceDatasetID, templateCode, dwsDatasetID,
			claim.SourceVersionID, claim.ID, dslHash, action, claim.GroupKey)
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
		"groupKey": claim.GroupKey, "scopeHash": claim.ScopeHash,
		"sourceVersionIds": func() []string {
			values := make([]string, 0, len(claim.Scope.DWD))
			for _, item := range claim.Scope.DWD {
				values = append(values, item.VersionID)
			}
			return values
		}(),
	})
	if err != nil {
		return err
	}
	return database.WithTenantTx(ctx, worker.store.pool, claim.TenantID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE platform.dws_modeling_jobs
			SET status=$1,error_code=$2,
				error_message=CASE
				  WHEN $2='' THEN ''
				  WHEN $2='DWD_MATERIALIZATION_FAILED'
				    THEN '上游 DWD 物化已失败，请先在任务运行中心重试数据集构建'
				  ELSE 'DWS 建模执行失败，请在任务运行中心重试'
				END,
				result_json=$3,
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
	var group sync.WaitGroup
	group.Add(dwsModelingConcurrency)
	for index := 1; index <= dwsModelingConcurrency; index++ {
		index := index
		go func() {
			defer group.Done()
			runDWSModelingLoop(
				ctx, logger, worker,
				workerID+"-dws-"+strconv.Itoa(index),
				pollInterval,
			)
		}()
	}
	group.Wait()
}

func runDWSModelingLoop(
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
