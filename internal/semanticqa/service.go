package semanticqa

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"intelligent-report-generation-system/internal/dataset"
	"intelligent-report-generation-system/internal/metric"
)

type Store interface {
	GetSettings(context.Context, string) (Settings, error)
	UpdateSettings(context.Context, string, string, Settings) (Settings, error)
	CreateChangeSet(context.Context, string, string, CreateChangeSetInput, string, string) (ChangeSet, error)
	GetChangeSet(context.Context, string, string) (ChangeSet, error)
	FinishChangeValidation(context.Context, string, string, string, int64, []ChangeValidation, bool) (ChangeSet, error)
	FinishChangeApply(context.Context, string, string, string, int64, string, string) (ChangeSet, error)
	MarkChangeConflict(context.Context, string, string, int64, string) error
	RejectChangeSet(context.Context, string, string, string, int64, string) (ChangeSet, error)
	CreateConsumerContract(context.Context, string, string, CreateConsumerContractInput) (ConsumerContract, error)
	GetConsumerContract(context.Context, string, string) (ConsumerContract, error)
	PublishConsumerContract(context.Context, string, string, string, int64) (ConsumerContract, error)
	GetWarehouseDAG(context.Context, string, string) (WarehouseBuildDAG, error)
	GetGraphStatus(context.Context, string) (GraphStatus, error)
	PlanQuery(context.Context, string, string, QueryPlanInput, string) (QueryPlan, error)
	CreateQuestionTemplate(context.Context, string, string, CreateQuestionTemplateInput) (QuestionTemplate, error)
	ListQuestionTemplates(context.Context, string) ([]QuestionTemplate, error)
	CreateGoldenQuestionSet(context.Context, string, string, CreateGoldenQuestionSetInput) (GoldenQuestionSet, error)
	ListGoldenQuestionSets(context.Context, string) ([]GoldenQuestionSet, error)
	ActivateGoldenQuestionSet(context.Context, string, string, string, int64) (GoldenQuestionSet, error)
	CreateGoldenQuestion(context.Context, string, string, CreateGoldenQuestionInput, string) (GoldenQuestion, error)
	ListGoldenQuestions(context.Context, string, string) ([]GoldenQuestion, error)
	GetGoldenQuestion(context.Context, string, string) (GoldenQuestion, error)
	RecordGoldenQuestionReplay(context.Context, string, string, GoldenQuestion, QueryPlan, string, string) (GoldenQuestionReplay, error)
	ListMaterializationRecommendations(context.Context, string, int, int) ([]MaterializationRecommendation, error)
	GetQueryPlan(context.Context, string, string) (QueryPlan, error)
	PrepareQueryPlanExecution(context.Context, string, string, string, string) (QueryPlan, string, string, error)
	FinishQueryPlanExecution(context.Context, string, string, string, string, bool, string, int64, int) (QueryPlan, error)
}

type DatasetService interface {
	Get(context.Context, string, string) (dataset.Record, error)
	Create(context.Context, string, string, dataset.CreateInput) (dataset.Record, error)
	Update(context.Context, string, string, string, dataset.UpdateInput) (dataset.Record, error)
	Validate([]byte) (dataset.Prepared, error)
}

type Service struct {
	store          Store
	datasets       DatasetService
	interpreter    QueryInterpreter
	metricExecutor interface {
		PreviewVersion(context.Context, string, string, string, string, metric.PreviewInput) (dataset.PreviewResult, error)
	}
}

func NewService(
	store Store,
	datasets DatasetService,
	interpreters ...QueryInterpreter,
) *Service {
	service := &Service{store: store, datasets: datasets}
	if len(interpreters) > 0 {
		service.interpreter = interpreters[0]
	}
	return service
}

func (service *Service) SetMetricExecutor(executor interface {
	PreviewVersion(context.Context, string, string, string, string, metric.PreviewInput) (dataset.PreviewResult, error)
}) {
	service.metricExecutor = executor
}

func (service *Service) GetSettings(ctx context.Context, tenantID string) (Settings, error) {
	if service == nil || service.store == nil || uuid.Validate(tenantID) != nil {
		return Settings{}, ErrInvalidRequest
	}
	return service.store.GetSettings(ctx, tenantID)
}

func (service *Service) UpdateSettings(
	ctx context.Context,
	tenantID, actorID string,
	input Settings,
) (Settings, error) {
	if service == nil || service.store == nil ||
		uuid.Validate(tenantID) != nil || uuid.Validate(actorID) != nil ||
		input.MinimumPathConfidence < 0 || input.MinimumPathConfidence > 1 ||
		input.MaximumPathHops < 1 || input.MaximumPathHops > 16 {
		return Settings{}, ErrInvalidRequest
	}
	return service.store.UpdateSettings(ctx, tenantID, actorID, input)
}

func (service *Service) CreateChangeSet(
	ctx context.Context,
	tenantID, actorID string,
	input CreateChangeSetInput,
) (ChangeSet, error) {
	if service == nil || service.store == nil || service.datasets == nil ||
		uuid.Validate(tenantID) != nil || uuid.Validate(actorID) != nil {
		return ChangeSet{}, ErrInvalidRequest
	}
	input.TriggerType = strings.ToUpper(strings.TrimSpace(input.TriggerType))
	input.ChangeKind = strings.ToUpper(strings.TrimSpace(input.ChangeKind))
	input.TargetLayer = strings.ToUpper(strings.TrimSpace(input.TargetLayer))
	input.Title = strings.TrimSpace(input.Title)
	input.RequestKey = strings.TrimSpace(input.RequestKey)
	input.TargetDatasetID = strings.TrimSpace(input.TargetDatasetID)
	input.BaselineDSLHash = strings.ToLower(strings.TrimSpace(input.BaselineDSLHash))
	if !oneOf(input.TriggerType, "AUTOMATION", "QUESTION", "MANUAL") ||
		!oneOf(input.ChangeKind, "CREATE_DATASET", "MODIFY_DATASET", "REPAIR_DAG") ||
		!oneOf(input.TargetLayer, "DIM", "DWD", "DWS", "ADS") ||
		len(input.Title) < 1 || len(input.Title) > 300 ||
		len(input.RequestKey) < 1 || len(input.RequestKey) > 200 ||
		len(input.Operations) < 1 || len(input.Operations) > 256 {
		return ChangeSet{}, ErrInvalidRequest
	}
	if input.TriggerType == "QUESTION" && strings.TrimSpace(input.Question) == "" {
		return ChangeSet{}, ErrInvalidRequest
	}
	if input.ChangeKind == "CREATE_DATASET" {
		if input.TargetDatasetID != "" || input.BaselineDatasetVersion != nil ||
			input.BaselineDSLHash != "" {
			return ChangeSet{}, ErrInvalidRequest
		}
	} else {
		if uuid.Validate(input.TargetDatasetID) != nil ||
			input.BaselineDatasetVersion == nil || *input.BaselineDatasetVersion < 1 ||
			!validHash(input.BaselineDSLHash) {
			return ChangeSet{}, ErrInvalidRequest
		}
	}
	for _, operation := range input.Operations {
		if _, err := patchTokens(operation.Path); err != nil {
			return ChangeSet{}, err
		}
	}
	questionHash := hashText(strings.TrimSpace(input.Question))
	input.Question = ""
	requestHash, err := hashJSON(struct {
		Input        CreateChangeSetInput `json:"input"`
		QuestionHash string               `json:"questionHash"`
	}{Input: input, QuestionHash: questionHash})
	if err != nil {
		return ChangeSet{}, err
	}
	return service.store.CreateChangeSet(
		ctx, tenantID, actorID, input, questionHash, requestHash,
	)
}

// CreateChangeSetFromCandidate lets the existing human-question and visual DAG
// flows enter the durable workflow without granting them an opaque document
// overwrite. The server freezes the current baseline and produces deterministic
// top-level component patches from the normalized candidate.
func (service *Service) CreateChangeSetFromCandidate(
	ctx context.Context,
	tenantID, actorID string,
	input CreateChangeSetFromCandidateInput,
) (ChangeSet, error) {
	if service == nil || service.store == nil || service.datasets == nil ||
		uuid.Validate(tenantID) != nil || uuid.Validate(actorID) != nil {
		return ChangeSet{}, ErrInvalidRequest
	}
	input.TargetDatasetID = strings.TrimSpace(input.TargetDatasetID)
	input.TriggerType = strings.ToUpper(strings.TrimSpace(input.TriggerType))
	input.Title = strings.TrimSpace(input.Title)
	input.RequestKey = strings.TrimSpace(input.RequestKey)
	if !oneOf(input.TriggerType, "AUTOMATION", "QUESTION", "MANUAL") ||
		len(input.Title) < 1 || len(input.Title) > 300 ||
		len(input.RequestKey) < 1 || len(input.RequestKey) > 200 ||
		len(input.CandidateDSL) < 2 || len(input.CandidateDSL) > 2<<20 ||
		(input.TriggerType == "QUESTION" && strings.TrimSpace(input.Question) == "") {
		return ChangeSet{}, ErrInvalidRequest
	}
	prepared, err := service.datasets.Validate(input.CandidateDSL)
	if err != nil {
		return ChangeSet{}, err
	}
	targetLayer := string(prepared.Document.Dataset.Layer)
	if !oneOf(targetLayer, "DIM", "DWD", "DWS", "ADS") {
		return ChangeSet{}, ErrInvalidRequest
	}

	changeInput := CreateChangeSetInput{
		TargetDatasetID: input.TargetDatasetID,
		TriggerType:     input.TriggerType,
		TargetLayer:     targetLayer,
		Title:           input.Title,
		Question:        input.Question,
		RequestKey:      input.RequestKey,
	}
	var baseline json.RawMessage
	if input.TargetDatasetID == "" {
		changeInput.ChangeKind = "CREATE_DATASET"
		baseline = json.RawMessage(`{"dslVersion":"1.0"}`)
	} else {
		if uuid.Validate(input.TargetDatasetID) != nil {
			return ChangeSet{}, ErrInvalidRequest
		}
		current, getErr := service.datasets.Get(ctx, tenantID, input.TargetDatasetID)
		if getErr != nil {
			return ChangeSet{}, getErr
		}
		if current.Code != prepared.Document.Dataset.Code {
			return ChangeSet{}, wrapInvalid("candidate dataset code does not match target")
		}
		changeInput.ChangeKind = "MODIFY_DATASET"
		changeInput.BaselineDatasetVersion = &current.Version
		changeInput.BaselineDSLHash = current.DSLHash
		baseline = current.DSL
	}
	operations, err := componentDiff(baseline, prepared.DSLJSON)
	if err != nil || len(operations) == 0 {
		if err == nil {
			err = wrapInvalid("candidate does not change the baseline")
		}
		return ChangeSet{}, err
	}
	changeInput.Operations = operations
	return service.CreateChangeSet(ctx, tenantID, actorID, changeInput)
}

func (service *Service) GetChangeSet(
	ctx context.Context,
	tenantID, id string,
) (ChangeSet, error) {
	if service == nil || service.store == nil ||
		uuid.Validate(tenantID) != nil || uuid.Validate(id) != nil {
		return ChangeSet{}, ErrInvalidRequest
	}
	return service.store.GetChangeSet(ctx, tenantID, id)
}

func (service *Service) RejectChangeSet(
	ctx context.Context,
	tenantID, actorID, id string,
	input RejectChangeSetInput,
) (ChangeSet, error) {
	input.ReasonCode = strings.ToUpper(strings.TrimSpace(input.ReasonCode))
	if service == nil || service.store == nil ||
		uuid.Validate(tenantID) != nil || uuid.Validate(actorID) != nil ||
		uuid.Validate(id) != nil || input.ExpectedRecordVersion < 1 ||
		!validReasonCode(input.ReasonCode) {
		return ChangeSet{}, ErrInvalidRequest
	}
	return service.store.RejectChangeSet(
		ctx, tenantID, actorID, id,
		input.ExpectedRecordVersion, input.ReasonCode,
	)
}

func (service *Service) ValidateChangeSet(
	ctx context.Context,
	tenantID, actorID, id string,
	input ValidateChangeSetInput,
) (ChangeSet, error) {
	if service == nil || service.store == nil || service.datasets == nil ||
		uuid.Validate(tenantID) != nil || uuid.Validate(actorID) != nil ||
		uuid.Validate(id) != nil || input.ExpectedRecordVersion < 1 {
		return ChangeSet{}, ErrInvalidRequest
	}
	changeSet, raw, validations, err := service.prepareChange(
		ctx, tenantID, id, input.ExpectedRecordVersion,
	)
	if err != nil {
		return ChangeSet{}, err
	}
	valid := len(validations) == 0
	if valid {
		prepared, prepareErr := service.datasets.Validate(raw)
		if prepareErr != nil {
			validations = append(validations, datasetValidationIssues(prepareErr)...)
			valid = false
		} else if string(prepared.Document.Dataset.Layer) != changeSet.TargetLayer {
			validations = append(validations, ChangeValidation{
				Severity: "ERROR", Code: "TARGET_LAYER_MISMATCH",
				Path: "dataset.layer", Message: "候选 DSL 层级与 ChangeSet 目标层不一致",
			})
			valid = false
		}
	}
	return service.store.FinishChangeValidation(
		ctx, tenantID, actorID, id, input.ExpectedRecordVersion, validations, valid,
	)
}

func (service *Service) ApplyChangeSet(
	ctx context.Context,
	tenantID, actorID, id string,
	input ApplyChangeSetInput,
) (ApplyChangeSetResult, error) {
	if service == nil || service.store == nil || service.datasets == nil ||
		uuid.Validate(tenantID) != nil || uuid.Validate(actorID) != nil ||
		uuid.Validate(id) != nil || input.ExpectedRecordVersion < 1 {
		return ApplyChangeSetResult{}, ErrInvalidRequest
	}
	changeSet, raw, validations, err := service.prepareChange(
		ctx, tenantID, id, input.ExpectedRecordVersion,
	)
	if err != nil {
		return ApplyChangeSetResult{}, err
	}
	if changeSet.Status != "VALIDATED" || len(validations) > 0 {
		return ApplyChangeSetResult{}, ErrInvalidState
	}
	prepared, err := service.datasets.Validate(raw)
	if err != nil {
		_ = service.store.MarkChangeConflict(
			ctx, tenantID, id, input.ExpectedRecordVersion, "DSL_REVALIDATION_FAILED",
		)
		return ApplyChangeSetResult{}, err
	}
	var record dataset.Record
	if changeSet.ChangeKind == "CREATE_DATASET" {
		record, err = service.datasets.Create(ctx, tenantID, actorID, dataset.CreateInput{
			Code: prepared.Document.Dataset.Code, Name: prepared.Document.Dataset.Name,
			Description: prepared.Document.Dataset.Description,
			Type:        prepared.Document.Dataset.Type,
			Layer:       prepared.Document.Dataset.Layer,
			DSL:         prepared.DSLJSON,
		})
	} else {
		record, err = service.datasets.Update(
			ctx, tenantID, actorID, changeSet.TargetDatasetID,
			dataset.UpdateInput{
				Name:            prepared.Document.Dataset.Name,
				Description:     prepared.Document.Dataset.Description,
				ExpectedVersion: *changeSet.BaselineDatasetVersion,
				DSL:             prepared.DSLJSON,
			},
		)
	}
	if err != nil {
		code := "DATASET_APPLY_FAILED"
		if errors.Is(err, dataset.ErrConflict) {
			code = "BASELINE_CHANGED"
		}
		_ = service.store.MarkChangeConflict(
			ctx, tenantID, id, input.ExpectedRecordVersion, code,
		)
		return ApplyChangeSetResult{}, err
	}
	applied, err := service.store.FinishChangeApply(
		ctx, tenantID, actorID, id, input.ExpectedRecordVersion,
		record.ID, prepared.DSLHash,
	)
	if err != nil {
		return ApplyChangeSetResult{}, err
	}
	return ApplyChangeSetResult{
		ChangeSet: applied, DatasetID: record.ID, DSLHash: record.DSLHash,
	}, nil
}

func (service *Service) prepareChange(
	ctx context.Context,
	tenantID, id string,
	expectedRecordVersion int64,
) (ChangeSet, json.RawMessage, []ChangeValidation, error) {
	changeSet, err := service.store.GetChangeSet(ctx, tenantID, id)
	if err != nil {
		return ChangeSet{}, nil, nil, err
	}
	if changeSet.RecordVersion != expectedRecordVersion {
		return ChangeSet{}, nil, nil, ErrConflict
	}
	var baseline json.RawMessage
	validations := []ChangeValidation{}
	if changeSet.ChangeKind == "CREATE_DATASET" {
		baseline = json.RawMessage(`{"dslVersion":"1.0"}`)
	} else {
		current, getErr := service.datasets.Get(
			ctx, tenantID, changeSet.TargetDatasetID,
		)
		if getErr != nil {
			return ChangeSet{}, nil, nil, getErr
		}
		if changeSet.BaselineDatasetVersion == nil ||
			current.Version != *changeSet.BaselineDatasetVersion ||
			current.DSLHash != changeSet.BaselineDSLHash {
			return ChangeSet{}, nil, nil, ErrConflict
		}
		baseline = current.DSL
	}
	raw, patchErr := applyChangeOperations(baseline, changeSet.Operations)
	if patchErr != nil {
		validations = append(validations, ChangeValidation{
			Severity: "ERROR", Code: "UNSAFE_PATCH", Path: "",
			Message: "结构化 DAG 变更无法安全应用到冻结基线",
		})
	}
	return changeSet, raw, validations, nil
}

func (service *Service) CreateConsumerContract(
	ctx context.Context,
	tenantID, actorID string,
	input CreateConsumerContractInput,
) (ConsumerContract, error) {
	if service == nil || service.store == nil ||
		uuid.Validate(tenantID) != nil || uuid.Validate(actorID) != nil {
		return ConsumerContract{}, ErrInvalidRequest
	}
	input.Code = strings.TrimSpace(input.Code)
	input.Name = strings.TrimSpace(input.Name)
	input.Purpose = strings.TrimSpace(input.Purpose)
	if len(input.Code) < 1 || len(input.Code) > 128 ||
		len(input.Name) < 1 || len(input.Name) > 200 ||
		len(input.Purpose) < 1 || len(input.Purpose) > 2000 ||
		!validJSONObject(input.OutputGrain, 64<<10) ||
		!validJSONObject(input.ServiceLevel, 64<<10) ||
		len(input.Inputs) < 1 || len(input.Inputs) > 64 {
		return ConsumerContract{}, ErrInvalidRequest
	}
	seen := map[string]bool{}
	for _, item := range input.Inputs {
		if uuid.Validate(item.DatasetID) != nil ||
			uuid.Validate(item.DatasetVersionID) != nil || seen[item.DatasetVersionID] {
			return ConsumerContract{}, ErrInvalidRequest
		}
		seen[item.DatasetVersionID] = true
	}
	return service.store.CreateConsumerContract(ctx, tenantID, actorID, input)
}

func (service *Service) GetConsumerContract(
	ctx context.Context,
	tenantID, id string,
) (ConsumerContract, error) {
	if service == nil || service.store == nil ||
		uuid.Validate(tenantID) != nil || uuid.Validate(id) != nil {
		return ConsumerContract{}, ErrInvalidRequest
	}
	return service.store.GetConsumerContract(ctx, tenantID, id)
}

func (service *Service) PublishConsumerContract(
	ctx context.Context,
	tenantID, actorID, id string,
	input PublishConsumerContractInput,
) (ConsumerContract, error) {
	if service == nil || service.store == nil ||
		uuid.Validate(tenantID) != nil || uuid.Validate(actorID) != nil ||
		uuid.Validate(id) != nil || input.ExpectedVersion < 1 {
		return ConsumerContract{}, ErrInvalidRequest
	}
	return service.store.PublishConsumerContract(
		ctx, tenantID, actorID, id, input.ExpectedVersion,
	)
}

func (service *Service) GetWarehouseDAG(
	ctx context.Context,
	tenantID, datasetVersionID string,
) (WarehouseBuildDAG, error) {
	if service == nil || service.store == nil ||
		uuid.Validate(tenantID) != nil || uuid.Validate(datasetVersionID) != nil {
		return WarehouseBuildDAG{}, ErrInvalidRequest
	}
	return service.store.GetWarehouseDAG(ctx, tenantID, datasetVersionID)
}

func (service *Service) GetGraphStatus(
	ctx context.Context,
	tenantID string,
) (GraphStatus, error) {
	if service == nil || service.store == nil || uuid.Validate(tenantID) != nil {
		return GraphStatus{}, ErrInvalidRequest
	}
	return service.store.GetGraphStatus(ctx, tenantID)
}

func (service *Service) PlanQuery(
	ctx context.Context,
	tenantID, actorID string,
	input QueryPlanInput,
) (QueryPlan, error) {
	if service == nil || service.store == nil ||
		uuid.Validate(tenantID) != nil || uuid.Validate(actorID) != nil {
		return QueryPlan{}, ErrInvalidRequest
	}
	input.Question = strings.TrimSpace(input.Question)
	input.Intent = strings.ToUpper(strings.TrimSpace(input.Intent))
	input.MemberValue = strings.TrimSpace(input.MemberValue)
	input.DimensionCode = strings.TrimSpace(input.DimensionCode)
	input.MetricCode = strings.TrimSpace(input.MetricCode)
	if len(input.Question) < 1 || len(input.Question) > 4000 ||
		len(input.MemberValue) > 1024 || len(input.DimensionCode) > 128 ||
		len(input.MetricCode) > 128 ||
		!oneOf(input.Intent,
			"LOOKUP", "METRIC", "TREND", "COMPARISON", "RANKING",
			"DRILLDOWN", "DISTRIBUTION", "FUNNEL", "RETENTION",
			"ANOMALY", "UNKNOWN",
		) || input.MaximumPathHops < 0 || input.MaximumPathHops > 16 {
		return QueryPlan{}, ErrInvalidRequest
	}
	if service.interpreter != nil &&
		(input.MetricCode == "" || input.Intent == "UNKNOWN") {
		slots, err := service.interpreter.Interpret(
			ctx, tenantID, actorID, input.Question,
		)
		if err == nil {
			if input.MetricCode == "" {
				input.MetricCode = slots.MetricCode
			}
			if input.DimensionCode == "" {
				input.DimensionCode = slots.DimensionCode
			}
			if input.MemberValue == "" {
				input.MemberValue = slots.MemberValue
			}
			if input.Intent == "UNKNOWN" && slots.Intent != "" {
				input.Intent = slots.Intent
			}
		}
	}
	if input.MetricCode == "" {
		return QueryPlan{}, ErrUnprovenPath
	}
	questionHash := hashText(input.Question)
	input.Question = ""
	return service.store.PlanQuery(ctx, tenantID, actorID, input, questionHash)
}

func (service *Service) GetQueryPlan(
	ctx context.Context,
	tenantID, id string,
) (QueryPlan, error) {
	if service == nil || service.store == nil ||
		uuid.Validate(tenantID) != nil || uuid.Validate(id) != nil {
		return QueryPlan{}, ErrInvalidRequest
	}
	return service.store.GetQueryPlan(ctx, tenantID, id)
}

func (service *Service) ExecuteQueryPlan(
	ctx context.Context,
	tenantID, actorID, id string,
	input ExecuteQueryPlanInput,
) (QueryPlanExecution, error) {
	if service == nil || service.store == nil || service.metricExecutor == nil ||
		uuid.Validate(tenantID) != nil || uuid.Validate(actorID) != nil ||
		uuid.Validate(id) != nil ||
		uuid.Validate(input.ExpectedGraphGenerationID) != nil ||
		!validHash(input.ExpectedPathHash) ||
		uuid.Validate(input.QueryID) != nil ||
		input.MaxRows < 0 || input.MaxRows > 500 {
		return QueryPlanExecution{}, ErrInvalidRequest
	}
	if input.Parameters == nil {
		input.Parameters = map[string]any{}
	}
	plan, dimensionFieldID, memberKey, err := service.store.PrepareQueryPlanExecution(
		ctx, tenantID, id,
		input.ExpectedGraphGenerationID, input.ExpectedPathHash,
	)
	if err != nil {
		return QueryPlanExecution{}, err
	}
	dimensionFields := []string{}
	if dimensionFieldID != "" {
		dimensionFields = append(dimensionFields, dimensionFieldID)
	}
	dimensionFilters := []metric.DimensionFilter{}
	if memberKey != "" {
		dimensionFilters = append(dimensionFilters, metric.DimensionFilter{
			FieldID: dimensionFieldID, Operator: "EQUALS", Value: memberKey,
		})
	}
	result, executeErr := service.metricExecutor.PreviewVersion(
		ctx, tenantID, actorID, plan.SelectedMetricID,
		plan.SelectedMetricVersionID,
		metric.PreviewInput{
			QueryID: input.QueryID, Parameters: input.Parameters,
			DimensionFieldIDs: dimensionFields,
			DimensionFilters:  dimensionFilters, MaxRows: input.MaxRows,
		},
	)
	if executeErr != nil {
		_, _ = service.store.FinishQueryPlanExecution(
			ctx, tenantID, id, input.QueryID,
			"METRIC_EXECUTION_FAILED", false, input.ExpectedGraphGenerationID,
			0, 0,
		)
		return QueryPlanExecution{}, executeErr
	}
	plan, err = service.store.FinishQueryPlanExecution(
		ctx, tenantID, id, result.QueryID, "", true,
		input.ExpectedGraphGenerationID, result.DurationMS, result.RowCount,
	)
	if err != nil {
		// The generation or relationship changed while the query was running.
		// Discard the rows rather than presenting results with stale evidence.
		return QueryPlanExecution{}, err
	}
	return QueryPlanExecution{
		QueryPlan: plan,
		Result:    result,
		Evidence: AnswerEvidence{
			GraphGenerationID:     plan.GraphGenerationID,
			GraphGeneration:       plan.GraphGeneration,
			PathHash:              plan.PathHash,
			MetricID:              plan.SelectedMetricID,
			MetricVersionID:       plan.SelectedMetricVersionID,
			DimensionID:           plan.SelectedDimensionID,
			DatasetVersionID:      plan.SelectedDatasetVersionID,
			MaterializationID:     plan.SelectedMaterializationID,
			Lineage:               append([]QueryEvidence(nil), plan.Evidence...),
			PermissionDecision:    "REVALIDATED_BY_METRIC_RUNTIME",
			FreshnessDecision:     "ACTIVE_MATERIALIZATION_EXACT_VERSION",
			CompatibilityDecision: "VERIFIED_NON_UNSAFE",
			ExecutionRevalidated:  true,
		},
	}, nil
}

func (service *Service) CreateQuestionTemplate(
	ctx context.Context,
	tenantID, actorID string,
	input CreateQuestionTemplateInput,
) (QuestionTemplate, error) {
	if service == nil || service.store == nil ||
		uuid.Validate(tenantID) != nil || uuid.Validate(actorID) != nil {
		return QuestionTemplate{}, ErrInvalidRequest
	}
	input.Code = strings.TrimSpace(input.Code)
	input.Name = strings.TrimSpace(input.Name)
	input.Intent = strings.ToUpper(strings.TrimSpace(input.Intent))
	if !validCode(input.Code) || len(input.Name) < 1 || len(input.Name) > 200 ||
		!validIntent(input.Intent) || !validJSONArray(input.RequiredSlots, 64<<10) {
		return QuestionTemplate{}, ErrInvalidRequest
	}
	return service.store.CreateQuestionTemplate(ctx, tenantID, actorID, input)
}

func (service *Service) ListQuestionTemplates(
	ctx context.Context,
	tenantID string,
) ([]QuestionTemplate, error) {
	if service == nil || service.store == nil || uuid.Validate(tenantID) != nil {
		return nil, ErrInvalidRequest
	}
	return service.store.ListQuestionTemplates(ctx, tenantID)
}

func (service *Service) CreateGoldenQuestionSet(
	ctx context.Context,
	tenantID, actorID string,
	input CreateGoldenQuestionSetInput,
) (GoldenQuestionSet, error) {
	if service == nil || service.store == nil ||
		uuid.Validate(tenantID) != nil || uuid.Validate(actorID) != nil {
		return GoldenQuestionSet{}, ErrInvalidRequest
	}
	input.Code = strings.TrimSpace(input.Code)
	input.Name = strings.TrimSpace(input.Name)
	input.BusinessDomain = strings.TrimSpace(input.BusinessDomain)
	if !validCode(input.Code) || len(input.Name) < 1 || len(input.Name) > 200 ||
		len(input.BusinessDomain) < 1 || len(input.BusinessDomain) > 128 ||
		input.Version < 1 ||
		input.CorrectnessThreshold < 0 || input.CorrectnessThreshold > 1 ||
		input.SafetyThreshold < 0 || input.SafetyThreshold > 1 {
		return GoldenQuestionSet{}, ErrInvalidRequest
	}
	return service.store.CreateGoldenQuestionSet(ctx, tenantID, actorID, input)
}

func (service *Service) ListGoldenQuestionSets(
	ctx context.Context,
	tenantID string,
) ([]GoldenQuestionSet, error) {
	if service == nil || service.store == nil || uuid.Validate(tenantID) != nil {
		return nil, ErrInvalidRequest
	}
	return service.store.ListGoldenQuestionSets(ctx, tenantID)
}

func (service *Service) ActivateGoldenQuestionSet(
	ctx context.Context,
	tenantID, actorID, id string,
	input ActivateGoldenQuestionSetInput,
) (GoldenQuestionSet, error) {
	if service == nil || service.store == nil ||
		uuid.Validate(tenantID) != nil || uuid.Validate(actorID) != nil ||
		uuid.Validate(id) != nil || input.ExpectedRecordVersion < 1 {
		return GoldenQuestionSet{}, ErrInvalidRequest
	}
	return service.store.ActivateGoldenQuestionSet(
		ctx, tenantID, actorID, id, input.ExpectedRecordVersion,
	)
}

func (service *Service) CreateGoldenQuestion(
	ctx context.Context,
	tenantID, actorID string,
	input CreateGoldenQuestionInput,
) (GoldenQuestion, error) {
	if service == nil || service.store == nil ||
		uuid.Validate(tenantID) != nil || uuid.Validate(actorID) != nil ||
		uuid.Validate(input.SetID) != nil ||
		(input.TemplateID != "" && uuid.Validate(input.TemplateID) != nil) {
		return GoldenQuestion{}, ErrInvalidRequest
	}
	input.Question = strings.TrimSpace(input.Question)
	input.ExpectedStatus = strings.ToUpper(strings.TrimSpace(input.ExpectedStatus))
	input.Fixture.QueryPlan.Intent = strings.ToUpper(
		strings.TrimSpace(input.Fixture.QueryPlan.Intent),
	)
	input.Fixture.QueryPlan.MetricCode = strings.TrimSpace(
		input.Fixture.QueryPlan.MetricCode,
	)
	input.Fixture.QueryPlan.DimensionCode = strings.TrimSpace(
		input.Fixture.QueryPlan.DimensionCode,
	)
	input.Fixture.QueryPlan.MemberValue = strings.TrimSpace(
		input.Fixture.QueryPlan.MemberValue,
	)
	input.Fixture.QueryPlan.Question = ""
	if len(input.Question) < 1 || len(input.Question) > 4000 ||
		!validHash(input.ExpectedPathHash) ||
		!oneOf(input.ExpectedStatus, "READY", "AMBIGUOUS", "GAP", "REJECTED") ||
		!validIntent(input.Fixture.QueryPlan.Intent) ||
		len(input.Fixture.QueryPlan.MetricCode) < 1 ||
		len(input.Fixture.QueryPlan.MetricCode) > 128 ||
		len(input.Fixture.QueryPlan.DimensionCode) > 128 ||
		len(input.Fixture.QueryPlan.MemberValue) > 1024 ||
		input.Fixture.QueryPlan.MaximumPathHops < 0 ||
		input.Fixture.QueryPlan.MaximumPathHops > 16 ||
		len(input.Fixture.ExpectedFailureCode) > 128 {
		return GoldenQuestion{}, ErrInvalidRequest
	}
	questionHash := hashText(input.Question)
	input.Question = ""
	return service.store.CreateGoldenQuestion(
		ctx, tenantID, actorID, input, questionHash,
	)
}

func (service *Service) ListGoldenQuestions(
	ctx context.Context,
	tenantID, setID string,
) ([]GoldenQuestion, error) {
	if service == nil || service.store == nil ||
		uuid.Validate(tenantID) != nil || uuid.Validate(setID) != nil {
		return nil, ErrInvalidRequest
	}
	return service.store.ListGoldenQuestions(ctx, tenantID, setID)
}

func (service *Service) ReplayGoldenQuestion(
	ctx context.Context,
	tenantID, actorID, id string,
) (GoldenQuestionReplay, error) {
	if service == nil || service.store == nil ||
		uuid.Validate(tenantID) != nil || uuid.Validate(actorID) != nil ||
		uuid.Validate(id) != nil {
		return GoldenQuestionReplay{}, ErrInvalidRequest
	}
	item, err := service.store.GetGoldenQuestion(ctx, tenantID, id)
	if err != nil {
		return GoldenQuestionReplay{}, err
	}
	plan, planErr := service.store.PlanQuery(
		ctx, tenantID, actorID, item.Fixture.QueryPlan, item.QuestionHash,
	)
	failureStage, failureCode := "", ""
	if planErr != nil {
		failureStage, failureCode = "PLANNING", "QUERY_PLAN_FAILED"
		if errors.Is(planErr, ErrGraphNotReady) {
			failureCode = "GRAPH_NOT_READY"
		}
		plan = QueryPlan{Status: "FAILED", FailureCode: failureCode}
	} else if plan.Status != item.ExpectedStatus {
		failureStage, failureCode = "PLANNING", "STATUS_MISMATCH"
	} else if item.ExpectedStatus == "READY" &&
		plan.PathHash != item.ExpectedPathHash {
		failureStage, failureCode = "RELATIONSHIP", "PATH_HASH_MISMATCH"
	} else if item.Fixture.ExpectedFailureCode != "" &&
		plan.FailureCode != item.Fixture.ExpectedFailureCode {
		failureStage, failureCode = "PLANNING", "FAILURE_CODE_MISMATCH"
	}
	return service.store.RecordGoldenQuestionReplay(
		ctx, tenantID, actorID, item, plan, failureStage, failureCode,
	)
}

func (service *Service) ListMaterializationRecommendations(
	ctx context.Context,
	tenantID string,
	lookbackDays, minimumHits int,
) ([]MaterializationRecommendation, error) {
	if service == nil || service.store == nil || uuid.Validate(tenantID) != nil {
		return nil, ErrInvalidRequest
	}
	if lookbackDays == 0 {
		lookbackDays = 30
	}
	if minimumHits == 0 {
		minimumHits = 20
	}
	if lookbackDays < 1 || lookbackDays > 365 || minimumHits < 1 || minimumHits > 100000 {
		return nil, ErrInvalidRequest
	}
	return service.store.ListMaterializationRecommendations(
		ctx, tenantID, lookbackDays, minimumHits,
	)
}

func datasetValidationIssues(err error) []ChangeValidation {
	var validationError *dataset.ValidationError
	if errors.As(err, &validationError) {
		items := make([]ChangeValidation, 0, len(validationError.Issues))
		for _, issue := range validationError.Issues {
			items = append(items, ChangeValidation{
				Severity: "ERROR", Code: "DATASET_DSL_INVALID",
				Path: issue.Path, Message: issue.Reason,
			})
		}
		return items
	}
	return []ChangeValidation{{
		Severity: "ERROR", Code: "DATASET_DSL_INVALID",
		Message: "候选数据集 DSL 未通过本地合同校验",
	}}
}

func validJSONObject(raw json.RawMessage, maximum int) bool {
	if len(raw) < 2 || len(raw) > maximum {
		return false
	}
	var value map[string]any
	return json.Unmarshal(raw, &value) == nil && value != nil
}

func validJSONArray(raw json.RawMessage, maximum int) bool {
	if len(raw) < 2 || len(raw) > maximum {
		return false
	}
	var value []any
	return json.Unmarshal(raw, &value) == nil && value != nil
}

func validCode(value string) bool {
	if len(value) < 1 || len(value) > 128 {
		return false
	}
	for index, character := range value {
		if index == 0 && !((character >= 'A' && character <= 'Z') ||
			(character >= 'a' && character <= 'z')) {
			return false
		}
		if index > 0 && !((character >= 'A' && character <= 'Z') ||
			(character >= 'a' && character <= 'z') ||
			(character >= '0' && character <= '9') || character == '_') {
			return false
		}
	}
	return true
}

func validReasonCode(value string) bool {
	if len(value) < 2 || len(value) > 128 ||
		value[0] < 'A' || value[0] > 'Z' {
		return false
	}
	for _, character := range value {
		if !((character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') || character == '_') {
			return false
		}
	}
	return true
}

func validIntent(value string) bool {
	return oneOf(
		value,
		"LOOKUP", "METRIC", "TREND", "COMPARISON", "RANKING",
		"DRILLDOWN", "DISTRIBUTION", "FUNNEL", "RETENTION", "ANOMALY",
		"UNKNOWN",
	)
}

func hashText(value string) string {
	if value == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func hashJSON(value any) (string, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func validHash(value string) bool {
	if len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func oneOf(value string, allowed ...string) bool {
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
}

func wrapInvalid(reason string) error {
	return fmt.Errorf("%w: %s", ErrInvalidRequest, reason)
}
