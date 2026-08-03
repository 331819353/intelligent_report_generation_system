package semanticqa

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/google/uuid"
	"intelligent-report-generation-system/internal/dataset"
	"intelligent-report-generation-system/internal/metric"
)

var (
	queryCutoffMonthPattern = regexp.MustCompile(
		`(?:截至|截止(?:到)?|到)(?:([0-9]{4})年)?(1[0-2]|0?[1-9])月(?:底|末)?`,
	)
	queryCutoffPresetPattern = regexp.MustCompile(
		`^THROUGH_(?:([0-9]{4})_)?(0[1-9]|1[0-2])$`,
	)
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
	ResolveQueryContext(context.Context, string, string) (QuerySlots, error)
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
	PrepareQueryPlanExecution(context.Context, string, string, string, string) (QueryPlan, QueryExecutionBinding, error)
	FinishQueryPlanExecution(context.Context, string, string, string, string, bool, string, int64, int) (QueryPlan, error)
	UpsertQueryFeedback(context.Context, string, string, string, SubmitQueryFeedbackInput) (QueryFeedback, error)
}

type DatasetService interface {
	Get(context.Context, string, string) (dataset.Record, error)
	Create(context.Context, string, string, dataset.CreateInput) (dataset.Record, error)
	Update(context.Context, string, string, string, dataset.UpdateInput) (dataset.Record, error)
	Validate([]byte) (dataset.Prepared, error)
}

type Service struct {
	store           Store
	datasets        DatasetService
	interpreter     QueryInterpreter
	questionMu      sync.Mutex
	activeQuestions map[string]context.CancelFunc
	metricExecutor  interface {
		PreviewVersion(context.Context, string, string, string, string, metric.PreviewInput) (dataset.PreviewResult, error)
	}
}

func NewService(
	store Store,
	datasets DatasetService,
	interpreters ...QueryInterpreter,
) *Service {
	service := &Service{
		store: store, datasets: datasets,
		activeQuestions: map[string]context.CancelFunc{},
	}
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

func (service *Service) TokenizeQuery(
	ctx context.Context,
	tenantID, actorID string,
	input QueryTokenizeInput,
) (QueryTokenization, error) {
	result := QueryTokenization{
		Strategy: queryTokenizationStrategy,
		Tokens:   []QueryToken{},
	}
	input.Question = strings.TrimSpace(input.Question)
	input.Timezone = strings.TrimSpace(input.Timezone)
	if input.Timezone == "" {
		input.Timezone = "UTC"
	}
	if service == nil || service.store == nil ||
		uuid.Validate(tenantID) != nil || uuid.Validate(actorID) != nil ||
		len(input.Question) < 1 || len(input.Question) > 4000 {
		return result, ErrInvalidRequest
	}
	if _, err := time.LoadLocation(input.Timezone); err != nil {
		return result, ErrInvalidRequest
	}
	if tokenizer, ok := service.interpreter.(QueryTokenizer); ok {
		return tokenizer.Tokenize(
			ctx, tenantID, actorID, input.Question, input.Timezone,
		)
	}
	return tokenizeQuery(input.Question, nil), nil
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
	input.ContextQueryPlanID = strings.TrimSpace(input.ContextQueryPlanID)
	memberFilters, err := normalizeQueryMemberFilters(
		input.DimensionCode, input.MemberValue, input.MemberFilters,
	)
	if err != nil {
		return QueryPlan{}, ErrInvalidRequest
	}
	input.MemberFilters = memberFilters
	input.SortDirection = strings.ToUpper(strings.TrimSpace(input.SortDirection))
	input.TimePreset = strings.ToUpper(strings.TrimSpace(input.TimePreset))
	input.Timezone = strings.TrimSpace(input.Timezone)
	input.ComparisonMode = strings.ToUpper(strings.TrimSpace(input.ComparisonMode))
	if input.TimeRange == nil && input.TimePreset == "" {
		input.TimePreset = inferQueryTimePreset(input.Question)
	}
	if input.ComparisonMode == "" {
		input.ComparisonMode = inferQueryComparisonMode(input.Question)
	}
	if input.ComparisonRange != nil && input.ComparisonMode == "" {
		input.ComparisonMode = "CUSTOM"
	}
	if input.TimePreset != "" && input.Timezone == "" {
		input.Timezone = "UTC"
	}
	if input.ComparisonMode != "" && input.Timezone == "" {
		input.Timezone = "UTC"
	}
	if input.TimeRange != nil {
		normalized, err := normalizeQueryTimeRange(*input.TimeRange)
		if err != nil {
			return QueryPlan{}, ErrInvalidRequest
		}
		input.TimeRange = &normalized
	}
	if input.ComparisonRange != nil {
		normalized, err := normalizeQueryTimeRange(*input.ComparisonRange)
		if err != nil {
			return QueryPlan{}, ErrInvalidRequest
		}
		input.ComparisonRange = &normalized
	}
	if input.Intent == "RANKING" && input.TopN == 0 {
		input.TopN = 10
	}
	if input.TopN > 0 && input.SortDirection == "" {
		input.SortDirection = "DESC"
	}
	if len(input.Question) < 1 || len(input.Question) > 4000 ||
		len(input.MemberValue) > 1024 || len(input.DimensionCode) > 128 ||
		len(input.MetricCode) > 128 || len(input.Timezone) > 128 ||
		len(input.MemberFilters) > 8 ||
		!oneOf(input.Intent,
			"LOOKUP", "METRIC", "TREND", "COMPARISON", "RANKING",
			"DRILLDOWN", "DISTRIBUTION", "FUNNEL", "RETENTION",
			"ANOMALY", "UNKNOWN",
		) || input.MaximumPathHops < 0 || input.MaximumPathHops > 16 ||
		input.TopN < 0 || input.TopN > 500 ||
		!oneOf(input.SortDirection, "", "ASC", "DESC") ||
		(input.SortDirection != "" && input.TopN == 0) ||
		!validQueryTimePreset(input.TimePreset) ||
		(input.TimeRange != nil && input.TimePreset != "") ||
		(input.TimePreset == "" && input.Timezone != "" &&
			input.ComparisonMode == "") ||
		!oneOf(
			input.ComparisonMode, "", "PREVIOUS_PERIOD",
			"YEAR_OVER_YEAR", "CUSTOM",
		) ||
		(input.ComparisonMode == "CUSTOM" && input.ComparisonRange == nil) ||
		(input.ComparisonRange != nil &&
			input.TimeRange == nil && input.TimePreset == "") {
		return QueryPlan{}, ErrInvalidRequest
	}
	if input.TimePreset != "" {
		if _, err := time.LoadLocation(input.Timezone); err != nil {
			return QueryPlan{}, ErrInvalidRequest
		}
	}
	if service.interpreter != nil &&
		(input.MetricCode == "" || input.Intent == "UNKNOWN") {
		slots, err := service.interpreter.Interpret(
			ctx, tenantID, actorID, input.Question,
		)
		if err == nil {
			if input.MetricCode == "" {
				input.MetricCode = slots.MetricCode
				input.MetricCandidateCount = slots.MetricCandidateCount
				input.MetricMatchMethod = slots.MetricMatchMethod
				input.Domain = slots.Domain
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
	if input.ContextQueryPlanID != "" {
		if uuid.Validate(input.ContextQueryPlanID) != nil {
			return QueryPlan{}, ErrInvalidRequest
		}
		contextSlots, err := service.store.ResolveQueryContext(
			ctx, tenantID, input.ContextQueryPlanID,
		)
		if err != nil {
			return QueryPlan{}, err
		}
		inheritQueryContext(&input, contextSlots)
		input.MemberFilters, err = normalizeQueryMemberFilters(
			input.DimensionCode, input.MemberValue, input.MemberFilters,
		)
		if err != nil {
			return QueryPlan{}, ErrInvalidRequest
		}
	}
	if input.Intent == "RANKING" && input.TopN == 0 {
		input.TopN, input.SortDirection = 10, "DESC"
	}
	if input.ComparisonMode != "" && input.Intent != "COMPARISON" {
		return QueryPlan{}, ErrInvalidRequest
	}
	if input.MetricCode == "" {
		return QueryPlan{}, ErrUnprovenPath
	}
	if input.MetricMatchMethod == "" {
		input.MetricMatchMethod = "EXPLICIT_CODE"
	}
	questionHash := hashText(input.Question)
	return service.store.PlanQuery(ctx, tenantID, actorID, input, questionHash)
}

// PlanQueryTurn expands one conversational turn into one independently
// governed QueryPlan per requested metric. A follow-up without a newly named
// metric inherits every compatible metric from the immediately supplied
// context. "再加/同时/also" phrasing explicitly unions new metrics with context;
// otherwise current-turn metric mentions replace prior metric selection.
func (service *Service) PlanQueryTurn(
	ctx context.Context,
	tenantID, actorID string,
	input QueryTurnInput,
) (result QueryTurnPlan, runErr error) {
	machine := newQuestionStateMachine("")
	defer func() {
		if runErr != nil {
			blockQuestionState(&result, machine)
		}
	}()
	result = QueryTurnPlan{
		Status: "PLANNING", MetricCodes: []string{},
		ContextQueryPlanIDs: []string{},
		Plans:               []QueryPlan{},
		Trace: QueryTurnTrace{
			ConversationQuestions: []string{},
			ContextPolicy:         "CURRENT_TURN_OVERRIDES_SAME_DIMENSION_THEN_LATEST_VERIFIED_PLAN",
			MetricCandidates:      []QueryMetricCandidateTrace{},
			DimensionValueLookups: []QueryDimensionValueLookupTrace{},
			FinalSelections:       []QueryFinalSelectionTrace{},
			Assessments:           []QueryTraceAssessment{},
			Extraction: QueryTurnExtraction{
				MetricTerms: []string{}, DimensionValueTerms: []string{},
			},
		},
	}
	if service == nil || service.store == nil ||
		uuid.Validate(tenantID) != nil || uuid.Validate(actorID) != nil {
		return result, ErrInvalidRequest
	}
	input.Question = strings.TrimSpace(input.Question)
	input.Timezone = strings.TrimSpace(input.Timezone)
	if input.Timezone == "" {
		input.Timezone = "UTC"
	}
	if len(input.Question) < 1 || len(input.Question) > 4000 ||
		input.MaximumPathHops < 0 || input.MaximumPathHops > 16 ||
		len(input.ContextQueryPlanIDs) > 8 ||
		len(input.PriorQuestions) > 2 ||
		len(input.ConfirmedMetricCodes) > 8 ||
		len(input.ConfirmedDecisions) > 16 ||
		!validQuerySemanticHints(input.SemanticHints) {
		return result, ErrInvalidRequest
	}
	input.ConfirmedMetricCodes = uniqueStrings(
		input.ConfirmedMetricCodes, 8,
	)
	for _, confirmed := range input.ConfirmedDecisions {
		if strings.TrimSpace(confirmed.MetricCode) == "" ||
			uuid.Validate(strings.TrimSpace(confirmed.DecisionID)) != nil {
			return result, ErrInvalidRequest
		}
	}
	if _, err := time.LoadLocation(input.Timezone); err != nil {
		return result, ErrInvalidRequest
	}
	for index := range input.PriorQuestions {
		input.PriorQuestions[index] = strings.TrimSpace(
			input.PriorQuestions[index],
		)
		if len(input.PriorQuestions[index]) < 1 ||
			len(input.PriorQuestions[index]) > 4000 {
			return result, ErrInvalidRequest
		}
	}
	result.QuestionHash = hashText(input.Question)
	if err := advanceTurnLifecycle(
		&result, machine, QuestionStateReceived,
	); err != nil {
		return result, err
	}
	if err := persistQuestionStateMachine(
		ctx, service, tenantID, actorID, result.QuestionHash, machine,
	); err != nil {
		return result, err
	}
	if err := advanceTurnLifecycle(
		&result, machine, QuestionStateAuthorized,
	); err != nil {
		return result, err
	}
	reportQueryTurnProgress(
		ctx, QueryProgressStageRequest, QueryProgressStatusSucceeded,
		"问题已接收，开始验证会话上下文和语义路径",
	)
	result.Trace.ConversationQuestions = append(
		append([]string(nil), input.PriorQuestions...),
		input.Question,
	)
	contextIDs := uniqueStrings(input.ContextQueryPlanIDs, 8)
	if len(contextIDs) != len(input.ContextQueryPlanIDs) {
		return result, ErrInvalidRequest
	}
	contextSlots := make([]QuerySlots, 0, len(contextIDs))
	contextByMetric := map[string]struct {
		id    string
		slots QuerySlots
	}{}
	if len(contextIDs) > 0 {
		reportQueryTurnProgress(
			ctx, QueryProgressStageContext, QueryProgressStatusRunning,
			"正在读取上一轮已验证的指标、维度和时间条件",
		)
	}
	for _, id := range contextIDs {
		if uuid.Validate(id) != nil {
			return result, ErrInvalidRequest
		}
		slots, err := service.store.ResolveQueryContext(ctx, tenantID, id)
		if err != nil {
			return result, err
		}
		if slots.MetricCode == "" {
			return result, ErrUnprovenPath
		}
		contextSlots = append(contextSlots, slots)
		contextByMetric[strings.ToLower(slots.MetricCode)] = struct {
			id    string
			slots QuerySlots
		}{id: id, slots: slots}
	}
	if err := advanceTurnLifecycle(
		&result, machine, QuestionStateContextReady,
	); err != nil {
		return result, err
	}
	if len(contextIDs) > 0 {
		reportQueryTurnProgress(
			ctx, QueryProgressStageContext, QueryProgressStatusSucceeded,
			"已完成会话上下文继承校验",
		)
	}

	turnSlots := QueryTurnSlots{
		Intent: "UNKNOWN", MetricCodes: []string{}, Domains: map[string]string{},
	}
	reportQueryTurnProgress(
		ctx, QueryProgressStageMetricSelection, QueryProgressStatusRunning,
		"正在理解指标意图并锁定已发布指标",
	)
	if len(input.ConfirmedMetricCodes) > 0 {
		resolver, ok := service.interpreter.(QueryMetricConfirmationResolver)
		if !ok {
			return result, ErrUnprovenPath
		}
		confirmed, err := resolver.ConfirmMetricCodes(
			ctx, tenantID, input.ConfirmedMetricCodes,
		)
		if err != nil {
			return result, err
		}
		turnSlots = confirmed
	} else if interpreter, ok := service.interpreter.(QueryTurnInterpreter); ok {
		interpreted, err := interpreter.InterpretMany(
			ctx, tenantID, actorID, input.Question,
		)
		if err != nil {
			return result, err
		}
		turnSlots = interpreted
	} else if service.interpreter != nil {
		interpreted, err := service.interpreter.Interpret(
			ctx, tenantID, actorID, input.Question,
		)
		if err != nil {
			return result, err
		}
		if interpreted.MetricCode != "" {
			turnSlots = QueryTurnSlots{
				Intent:               interpreted.Intent,
				MetricCodes:          []string{interpreted.MetricCode},
				MetricCandidateCount: interpreted.MetricCandidateCount,
				MetricMatchMethod:    interpreted.MetricMatchMethod,
				Domains: map[string]string{
					interpreted.MetricCode: interpreted.Domain,
				},
			}
		}
	}
	hadCurrentMetrics := len(turnSlots.MetricCodes) > 0
	if hadCurrentMetrics {
		reportQueryTurnProgress(
			ctx, QueryProgressStageMetricSelection,
			QueryProgressStatusSucceeded,
			fmt.Sprintf("已锁定 %d 个指标，准备解析维度条件", len(turnSlots.MetricCodes)),
		)
	}
	metricCodes := selectTurnMetricCodes(
		turnSlots.MetricCodes, contextSlots,
		hadCurrentMetrics && questionAddsMetrics(input.Question),
	)
	if len(metricCodes) == 0 {
		result.QuestionHash = hashText(input.Question)
		result.Intent = strings.ToUpper(strings.TrimSpace(turnSlots.Intent))
		if result.Intent == "" {
			result.Intent = "UNKNOWN"
		}
		result.Status = "NEEDS_METRIC_CONFIRMATION"
		clarificationCandidates := turnSlots.MetricCandidates
		if len(clarificationCandidates) > 8 {
			clarificationCandidates = clarificationCandidates[:8]
		}
		result.Clarification = &QueryClarification{
			Type:    "METRIC",
			Message: "请选择本次问题对应的指标；候选均来自当前已发布指标目录。",
			MetricCandidates: append(
				[]QueryMetricCandidateTrace(nil),
				clarificationCandidates...,
			),
		}
		result.Trace = buildQueryTurnTrace(
			result.Trace.ConversationQuestions, result.Trace.ContextPolicy,
			turnSlots, contextSlots, result,
		)
		if err := advanceTurnLifecycle(
			&result, machine, QuestionStateClarificationRequired,
		); err != nil {
			return result, err
		}
		reportQueryTurnProgress(
			ctx, QueryProgressStageMetricSelection, QueryProgressStatusWarn,
			"指标候选无法唯一确定，等待用户确认",
		)
		reportQueryTurnProgress(
			ctx, QueryProgressStageComplete, QueryProgressStatusWarn,
			"语义检索已暂停，确认指标后将继续",
		)
		return result, nil
	}
	// Metric selection is stage one. Only after a governed metric (or trusted
	// context metric) is fixed do Jieba tokens enter the dimension/time semantic
	// completion used by stage two. This prevents early dimension retrieval from
	// influencing which metric catalog is selected.
	if tokenizer, ok := service.interpreter.(QueryTokenizer); ok {
		reportQueryTurnProgress(
			ctx, QueryProgressStageDimensionEnrichment,
			QueryProgressStatusRunning,
			"正在识别问题中的维度、维度值和时间表达",
		)
		var tokenization QueryTokenization
		var err error
		if queryTurnTokenizer, supported :=
			service.interpreter.(QueryTurnTokenizer); supported {
			tokenization, err = queryTurnTokenizer.TokenizeQueryTurn(
				ctx, tenantID, actorID, input.Question, input.Timezone, true,
			)
		} else {
			tokenization, err = tokenizer.Tokenize(
				ctx, tenantID, actorID, input.Question, input.Timezone,
			)
		}
		if err != nil {
			return result, err
		}
		result.Tokenization = &tokenization
		hints, _ := semanticHintsFromTokenization(tokenization)
		hints = supplementAdministrativeLocationHints(tokenization, hints)
		if validQuerySemanticHints(hints) &&
			(hints.Intent != "" || len(hints.MetricNames) > 0 ||
				len(hints.DimensionValues) > 0) {
			input.SemanticHints = hints
		}
		reportQueryTurnProgress(
			ctx, QueryProgressStageDimensionEnrichment,
			QueryProgressStatusSucceeded,
			"已完成维度、维度值和时间表达补全",
		)
	}
	if hintIntent := strings.ToUpper(strings.TrimSpace(
		input.SemanticHints.Intent,
	)); hintIntent != "" && hintIntent != "UNKNOWN" {
		turnSlots.Intent = hintIntent
	}
	planningQuestion := queryWithSemanticHints(
		input.Question, input.SemanticHints,
	)
	intent := strings.ToUpper(strings.TrimSpace(turnSlots.Intent))
	if !hadCurrentMetrics {
		intent = "UNKNOWN"
	}
	if intent == "" {
		intent = "UNKNOWN"
	}
	if err := advanceTurnLifecycle(
		&result, machine, QuestionStateValidating,
	); err != nil {
		return result, err
	}
	dimensionBaseQuestion := planningQuestion
	if strings.TrimSpace(turnSlots.AugmentedQuestion) != "" {
		dimensionBaseQuestion = queryWithSemanticHints(
			turnSlots.AugmentedQuestion, input.SemanticHints,
		)
	}
	for _, metricCode := range metricCodes {
		planInput := QueryPlanInput{
			Question: dimensionBaseQuestion, Intent: intent, MetricCode: metricCode,
			MaximumPathHops:      input.MaximumPathHops,
			MetricCandidateCount: turnSlots.MetricCandidateCount,
			MetricMatchMethod:    turnSlots.MetricMatchMethod,
			Domain:               turnSlots.Domains[metricCode],
		}
		if timeRange, found := semanticHintTimeRange(
			input.SemanticHints.DimensionValues,
		); found {
			planInput.TimeRange = &timeRange
		}
		if context, ok := contextByMetric[strings.ToLower(metricCode)]; ok {
			planInput.ContextQueryPlanID = context.id
			if !hadCurrentMetrics {
				planInput.MetricMatchMethod = "CONTEXT"
				planInput.MetricCandidateCount = 1
				planInput.Domain = context.slots.Domain
			}
		}
		if planInput.MetricMatchMethod == "" {
			planInput.MetricMatchMethod = "EXPLICIT_CODE"
		}
		dimensionHandledByTool := false
		if resolver, ok := service.interpreter.(QueryDimensionToolLoopResolver); ok &&
			len(input.ConfirmedDecisions) == 0 {
			resolution, resolveErr := resolver.ResolveDimensionsWithToolLoop(
				ctx, tenantID, actorID, metricCode, dimensionBaseQuestion,
				input.SemanticHints.DimensionValues,
			)
			if resolveErr != nil && !semanticDeterministicFallbackAllowed(resolveErr) {
				return result, resolveErr
			}
			if resolveErr == nil {
				dimensionHandledByTool = true
				if resolution.Trace != nil {
					result.Trace.DimensionToolLoops = append(
						result.Trace.DimensionToolLoops, *resolution.Trace,
					)
				}
				if strings.TrimSpace(resolution.AugmentedQuestion) != "" {
					planInput.Question = resolution.AugmentedQuestion
				}
				lookups := resolution.Lookups
				planInput.DimensionValueLookups = lookups
				if filters, complete :=
					memberFiltersFromResolvedLookups(lookups); complete {
					planInput.MemberFilters = filters
					planInput.DimensionResolutionComplete = true
				} else if hasActionableDimensionLookups(lookups) ||
					hasActionableSemanticDimensionHint(
						input.SemanticHints.DimensionValues,
					) {
					result.Trace.DimensionValueLookups = append(
						result.Trace.DimensionValueLookups, lookups...,
					)
					if choices := dimensionClarificationChoices(
						lookups, metricCode,
					); len(choices) > 0 && result.Clarification == nil {
						result.Clarification = &QueryClarification{
							Type:                "DIMENSION",
							Message:             "请选择问题中维度值对应的维度字段和值。",
							DimensionCandidates: choices,
						}
					}
					continue
				}
			}
		}
		if !dimensionHandledByTool {
			if enricher, ok := service.interpreter.(QueryDimensionHintEnricher); ok &&
				len(input.SemanticHints.DimensionValues) > 0 {
				lookups, enrichErr := enricher.EnrichDimensionLookupsWithHints(
					ctx, tenantID, actorID, metricCode, dimensionBaseQuestion,
					input.SemanticHints.DimensionValues,
				)
				if enrichErr != nil {
					return result, enrichErr
				}
				planInput.DimensionValueLookups = lookups
				var confirmedValid bool
				lookups, confirmedValid = applyConfirmedDecisions(
					lookups, input.ConfirmedDecisions, metricCode,
				)
				if !confirmedValid {
					return result, ErrUnprovenPath
				}
				planInput.DimensionValueLookups = lookups
				if filters, complete :=
					memberFiltersFromResolvedLookups(lookups); complete {
					planInput.MemberFilters = filters
					planInput.DimensionResolutionComplete = true
				} else if hasActionableSemanticDimensionHint(
					input.SemanticHints.DimensionValues,
				) {
					// Decision-graph recall and final WHERE selection are separate
					// observable stages. Preserve every retrieved candidate in the
					// turn trace, but do not create an unfiltered executable plan
					// when the downstream filter cannot prove one selection.
					result.Trace.DimensionValueLookups = append(
						result.Trace.DimensionValueLookups, lookups...,
					)
					if choices := dimensionClarificationChoices(
						lookups, metricCode,
					); len(choices) > 0 && result.Clarification == nil {
						result.Clarification = &QueryClarification{
							Type:                "DIMENSION",
							Message:             "请选择问题中维度值对应的维度字段和值。",
							DimensionCandidates: choices,
						}
					}
					continue
				}
			} else if enricher, ok :=
				service.interpreter.(QueryDimensionLookupEnricher); ok {
				lookups, enrichErr := enricher.EnrichDimensionLookups(
					ctx, tenantID, actorID, metricCode, dimensionBaseQuestion,
				)
				if enrichErr != nil {
					return result, enrichErr
				}
				planInput.DimensionValueLookups = lookups
				if filters, complete :=
					memberFiltersFromResolvedLookups(lookups); complete {
					planInput.MemberFilters = filters
					planInput.DimensionResolutionComplete = true
				}
			}
		}
		reportQueryTurnProgress(
			ctx, QueryProgressStagePlan, QueryProgressStatusRunning,
			"正在验证指标、维度、数据集与物化结果的完整路径",
		)
		plan, err := service.PlanQuery(ctx, tenantID, actorID, planInput)
		if err != nil {
			return result, err
		}
		result.Plans = append(result.Plans, plan)
		reportQueryTurnProgress(
			ctx, QueryProgressStagePlan, QueryProgressStatusSucceeded,
			"已生成一个通过权限、版本和血缘校验的查询计划",
		)
	}
	result.QuestionHash = hashText(input.Question)
	result.MetricCodes = metricCodes
	result.ContextQueryPlanIDs = contextIDs
	result.ContextInherited = len(contextIDs) > 0 &&
		(!hadCurrentMetrics || questionAddsMetrics(input.Question))
	if len(result.Plans) > 0 {
		result.Intent = result.Plans[0].Intent
	} else {
		result.Intent = intent
	}
	result.Trace = buildQueryTurnTrace(
		result.Trace.ConversationQuestions, result.Trace.ContextPolicy,
		turnSlots, contextSlots, result,
	)
	finalizeQueryTurnStatus(&result)
	if result.Clarification != nil {
		if err := advanceTurnLifecycle(
			&result, machine, QuestionStateClarificationRequired,
		); err != nil {
			return result, err
		}
		reportQueryTurnProgress(
			ctx, QueryProgressStageComplete, QueryProgressStatusWarn,
			"维度候选无法唯一确定，等待用户确认",
		)
	} else {
		if err := advanceTurnLifecycle(
			&result, machine, QuestionStatePlanReady,
		); err != nil {
			return result, err
		}
		reportQueryTurnProgress(
			ctx, QueryProgressStageComplete, QueryProgressStatusSucceeded,
			fmt.Sprintf("语义路径验证完成，共生成 %d 个查询计划", len(result.Plans)),
		)
	}
	return result, nil
}

func finalizeQueryTurnStatus(result *QueryTurnPlan) {
	if result == nil {
		return
	}
	if result.Clarification != nil {
		result.Status = "NEEDS_DIMENSION_CONFIRMATION"
		return
	}
	if len(result.Plans) == 0 {
		result.Status = "SEMANTIC_GAP"
		result.Clarification = &QueryClarification{
			Type:    "SEMANTIC_GAP",
			Message: "已识别问题中的指标和维度值，但当前已发布的指标-维度关系或决策图不足以安全生成查询条件。",
		}
		return
	}
	for _, plan := range result.Plans {
		if plan.Status != "READY" {
			result.Status = "SEMANTIC_GAP"
			message := "查询计划未通过语义图、版本、权限或血缘门禁。"
			if strings.TrimSpace(plan.FailureCode) != "" {
				message += " 阻断代码：" + plan.FailureCode
			}
			result.Clarification = &QueryClarification{
				Type: "SEMANTIC_GAP", Message: message,
			}
			return
		}
	}
	result.Status = "PLANNED"
}

func applyConfirmedDecisions(
	lookups []QueryDimensionValueLookupTrace,
	confirmed []QueryConfirmedDecision,
	metricCode string,
) ([]QueryDimensionValueLookupTrace, bool) {
	decisionIDs := []string{}
	for _, item := range confirmed {
		if strings.EqualFold(
			strings.TrimSpace(item.MetricCode), metricCode,
		) {
			decisionIDs = append(decisionIDs, strings.TrimSpace(item.DecisionID))
		}
	}
	if len(decisionIDs) == 0 {
		return lookups, true
	}
	selected, valid := applyToolSelectedDecisions(lookups, decisionIDs)
	if !valid {
		return nil, false
	}
	for index := range selected {
		if selected[index].Selected {
			selected[index].WhereDesignStatus =
				"USER_CONFIRMED_DECISION_GRAPH"
		}
	}
	return selected, true
}

func dimensionClarificationChoices(
	lookups []QueryDimensionValueLookupTrace,
	metricCode string,
) []QueryDimensionCandidateChoice {
	result := []QueryDimensionCandidateChoice{}
	seen := map[string]bool{}
	for _, lookup := range lookups {
		if lookup.Selected || !dimensionLookupRelevantForClarification(lookup) {
			continue
		}
		for _, candidate := range lookup.DecisionCandidates {
			if candidate.DecisionID == "" || candidate.MemberValue == "" ||
				seen[candidate.DecisionID] {
				continue
			}
			seen[candidate.DecisionID] = true
			result = append(result, QueryDimensionCandidateChoice{
				MetricCode: metricCode, Term: lookup.Term,
				DecisionID:     candidate.DecisionID,
				DimensionCode:  lookup.DimensionCode,
				DimensionName:  lookup.DimensionName,
				CanonicalValue: candidate.CanonicalValue,
				TableSchema:    candidate.TableSchema,
				TableName:      candidate.TableName,
			})
			if len(result) == 16 {
				return result
			}
		}
	}
	return result
}

func dimensionLookupRelevantForClarification(
	lookup QueryDimensionValueLookupTrace,
) bool {
	if lookup.Selected {
		return false
	}
	if !strings.EqualFold(
		strings.TrimSpace(lookup.WhereDesignStatus),
		"NO_SAFE_DECISION_SELECTED",
	) {
		return true
	}
	bestScore := lookup.VectorTopScore
	for _, candidate := range lookup.DecisionCandidates {
		if candidate.Score > bestScore {
			bestScore = candidate.Score
		}
	}
	// A clarification is useful only when the governed decision graph contains
	// a genuinely close, but ambiguous, candidate. Showing low-similarity
	// members for an unknown value encourages users to confirm an unrelated
	// WHERE predicate.
	return bestScore >= 0.94
}

func hasActionableSemanticDimensionHint(
	hints []QuerySemanticDimensionHint,
) bool {
	for _, hint := range hints {
		if strings.TrimSpace(hint.Value) != "" &&
			!strings.EqualFold(
				strings.TrimSpace(hint.DimensionType), "TIME",
			) {
			return true
		}
	}
	return false
}

func hasActionableDimensionLookups(
	lookups []QueryDimensionValueLookupTrace,
) bool {
	for _, lookup := range lookups {
		if strings.TrimSpace(lookup.Term) != "" {
			return true
		}
	}
	return false
}

func semanticHintsFromTokenization(
	tokenization QueryTokenization,
) (QuerySemanticHints, bool) {
	completion := tokenization.LLMCompletion
	if completion.Status != "SUCCEEDED" {
		return QuerySemanticHints{}, false
	}
	hints := QuerySemanticHints{
		Intent:      completion.Intent,
		MetricNames: append([]string(nil), completion.MetricNames...),
		DimensionValues: make(
			[]QuerySemanticDimensionHint, 0, len(completion.DimensionValues),
		),
	}
	for _, dimension := range completion.DimensionValues {
		hints.DimensionValues = append(
			hints.DimensionValues,
			QuerySemanticDimensionHint{
				SourceToken: dimension.SourceToken,
				Value:       dimension.Value, DimensionName: dimension.DimensionName,
				DimensionCode: dimension.DimensionCode,
				DimensionType: dimension.DimensionType,
				ValueType:     dimension.ValueType,
				TimeRange:     dimension.TimeRange,
			},
		)
	}
	if !validQuerySemanticHints(hints) {
		return QuerySemanticHints{}, false
	}
	return hints, true
}

func supplementAdministrativeLocationHints(
	tokenization QueryTokenization,
	hints QuerySemanticHints,
) QuerySemanticHints {
	covered := func(source, value string) bool {
		source = strings.ToLower(strings.TrimSpace(source))
		value = strings.ToLower(strings.TrimSpace(value))
		for _, hint := range hints.DimensionValues {
			hintSource := strings.ToLower(strings.TrimSpace(hint.SourceToken))
			hintValue := strings.ToLower(strings.TrimSpace(hint.Value))
			if hintValue == value || hintSource == source ||
				(hintValue != "" && value != "" &&
					(strings.Contains(hintValue, value) ||
						strings.Contains(value, hintValue))) {
				return true
			}
		}
		return false
	}
	for _, token := range tokenization.Tokens {
		text := strings.TrimSpace(token.Text)
		if token.Source != "SEMANTIC_PARSING_RULE" ||
			token.EntityType != "LOCATION" {
			continue
		}
		value := strings.TrimSpace(token.Normalized)
		if value == "" || token.EntityName == "" || token.EntityCode == "" ||
			covered(text, value) {
			continue
		}
		hints.DimensionValues = append(
			hints.DimensionValues,
			QuerySemanticDimensionHint{
				SourceToken: text, Value: value,
				DimensionName: token.EntityName,
				DimensionCode: token.EntityCode,
				DimensionType: "STANDARD", ValueType: "STRING",
			},
		)
	}
	return hints
}

func semanticHintTimeRange(
	hints []QuerySemanticDimensionHint,
) (QueryTimeRange, bool) {
	for _, hint := range hints {
		if strings.EqualFold(
			strings.TrimSpace(hint.DimensionType), "TIME",
		) && hint.TimeRange != nil {
			return *hint.TimeRange, true
		}
	}
	return QueryTimeRange{}, false
}

func validQuerySemanticHints(hints QuerySemanticHints) bool {
	intent := strings.ToUpper(strings.TrimSpace(hints.Intent))
	if intent != "" && !oneOf(
		intent, "LOOKUP", "METRIC", "TREND", "COMPARISON", "RANKING",
		"DRILLDOWN", "DISTRIBUTION", "FUNNEL", "RETENTION", "ANOMALY",
		"UNKNOWN",
	) {
		return false
	}
	if len(hints.MetricNames) > 8 || len(hints.DimensionValues) > 16 {
		return false
	}
	for _, metricName := range hints.MetricNames {
		metricName = strings.TrimSpace(metricName)
		if metricName == "" || len(metricName) > 256 ||
			strings.ContainsAny(metricName, "\x00\r\n") {
			return false
		}
	}
	for _, hint := range hints.DimensionValues {
		if strings.TrimSpace(hint.Value) == "" ||
			len(hint.Value) > 1024 ||
			strings.TrimSpace(hint.DimensionName) == "" ||
			len(hint.DimensionName) > 256 ||
			strings.TrimSpace(hint.DimensionCode) == "" ||
			len(hint.DimensionCode) > 128 ||
			len(hint.SourceToken) > 1024 ||
			len(hint.DimensionType) > 64 ||
			len(hint.ValueType) > 64 ||
			strings.ContainsAny(
				hint.Value+hint.DimensionName+hint.DimensionCode+
					hint.SourceToken+hint.DimensionType+hint.ValueType,
				"\x00\r\n",
			) {
			return false
		}
		isTime := strings.EqualFold(
			strings.TrimSpace(hint.DimensionType), "TIME",
		)
		if hint.TimeRange != nil {
			normalized, rangeErr := normalizeQueryTimeRange(
				*hint.TimeRange,
			)
			if !isTime || rangeErr != nil {
				return false
			}
			_, startDateOnly, startErr :=
				parseQueryBoundary(normalized.Start)
			_, endDateOnly, endErr :=
				parseQueryBoundary(normalized.EndExclusive)
			valueType := strings.ToUpper(strings.TrimSpace(hint.ValueType))
			if startErr != nil || endErr != nil ||
				startDateOnly != endDateOnly ||
				!oneOf(valueType, "DATE", "DATETIME") ||
				(valueType == "DATE") != startDateOnly {
				return false
			}
		} else if isTime && strings.TrimSpace(hint.ValueType) != "" {
			// A typed time value must already be normalized by the LLM stage.
			return false
		}
	}
	var firstTimeRange *QueryTimeRange
	for _, hint := range hints.DimensionValues {
		if hint.TimeRange == nil {
			continue
		}
		if firstTimeRange == nil {
			copied := *hint.TimeRange
			firstTimeRange = &copied
			continue
		}
		if firstTimeRange.Start != hint.TimeRange.Start ||
			firstTimeRange.EndExclusive != hint.TimeRange.EndExclusive {
			return false
		}
	}
	return true
}

func queryWithSemanticHints(
	question string,
	hints QuerySemanticHints,
) string {
	parts := []string{}
	intent := strings.ToUpper(strings.TrimSpace(hints.Intent))
	if intent != "" && intent != "UNKNOWN" {
		parts = append(parts, "意图："+intent)
	}
	metricNames := uniqueStrings(hints.MetricNames, 8)
	if len(metricNames) > 0 {
		parts = append(parts, "指标："+strings.Join(metricNames, "、"))
	}
	dimensions := []string{}
	for _, hint := range hints.DimensionValues {
		name := strings.TrimSpace(hint.DimensionName)
		value := strings.TrimSpace(hint.Value)
		if name == "" || value == "" {
			continue
		}
		dimensions = append(dimensions, name+"="+value)
	}
	if len(dimensions) > 0 {
		parts = append(parts, "维度："+strings.Join(dimensions, "、"))
	}
	if len(parts) == 0 {
		return question
	}
	return question + "【语义补充：" + strings.Join(parts, "；") + "】"
}

// memberFiltersFromResolvedLookups converts the interpreter's selected,
// governed candidates into the same opaque member-filter contract PlanQuery
// validates against the immutable semantic graph. Unselected alternatives stay
// in the trace for auditability; resolution is complete when every extracted
// term has one selected dimension.
func memberFiltersFromResolvedLookups(
	lookups []QueryDimensionValueLookupTrace,
) ([]QueryMemberFilterInput, bool) {
	termSelected := map[string]bool{}
	termSeen := map[string]bool{}
	dimensionOrder := []string{}
	valuesByDimension := map[string][]string{}
	codeByDimension := map[string]string{}
	for _, lookup := range lookups {
		if lookup.Source != "" &&
			lookup.Source != "CURRENT_TURN" &&
			lookup.Source != "LLM_INTENT_COMPLETION" {
			continue
		}
		term := strings.ToLower(strings.TrimSpace(lookup.Term))
		if term == "" {
			continue
		}
		termSeen[term] = true
		if !lookup.Selected {
			continue
		}
		if lookup.Sensitive || len(lookup.SelectedMemberKeys) == 0 {
			return nil, false
		}
		termSelected[term] = true
		dimensionKey := strings.ToLower(
			strings.TrimSpace(lookup.DimensionCode),
		)
		if dimensionKey == "" {
			return nil, false
		}
		if _, exists := valuesByDimension[dimensionKey]; !exists {
			dimensionOrder = append(dimensionOrder, dimensionKey)
			codeByDimension[dimensionKey] = lookup.DimensionCode
		}
		for _, value := range lookup.SelectedMemberKeys {
			valuesByDimension[dimensionKey] = appendUniqueString(
				valuesByDimension[dimensionKey], value,
			)
		}
	}
	if len(termSeen) == 0 {
		return nil, false
	}
	for term := range termSeen {
		if !termSelected[term] {
			return nil, false
		}
	}
	result := make([]QueryMemberFilterInput, 0, len(dimensionOrder))
	for _, dimensionKey := range dimensionOrder {
		values := valuesByDimension[dimensionKey]
		sort.Strings(values)
		if len(values) == 0 {
			return nil, false
		}
		result = append(result, QueryMemberFilterInput{
			DimensionCode: codeByDimension[dimensionKey],
			MemberValues:  values,
		})
	}
	return result, len(result) > 0
}

func selectTurnMetricCodes(
	current []string,
	contexts []QuerySlots,
	appendContext bool,
) []string {
	values := append([]string(nil), current...)
	if len(values) == 0 || appendContext {
		for _, context := range contexts {
			values = append(values, context.MetricCode)
		}
	}
	return uniqueStrings(values, 8)
}

func questionAddsMetrics(question string) bool {
	question = strings.ToLower(question)
	for _, phrase := range []string{
		"再加", "加上", "以及", "同时", "还有", "并且", "连同",
		"also", "along with", "in addition",
	} {
		if strings.Contains(question, phrase) {
			return true
		}
	}
	return false
}

func buildQueryTurnTrace(
	conversationQuestions []string,
	contextPolicy string,
	turnSlots QueryTurnSlots,
	contextSlots []QuerySlots,
	turn QueryTurnPlan,
) QueryTurnTrace {
	trace := QueryTurnTrace{
		ConversationQuestions: append(
			[]string(nil), conversationQuestions...,
		),
		ContextPolicy:  contextPolicy,
		MetricToolLoop: turnSlots.MetricToolLoop,
		DimensionToolLoops: append(
			[]QueryDimensionToolLoopTrace(nil),
			turn.Trace.DimensionToolLoops...,
		),
		MetricCandidates: []QueryMetricCandidateTrace{},
		DimensionValueLookups: append(
			[]QueryDimensionValueLookupTrace(nil),
			turn.Trace.DimensionValueLookups...,
		),
		FinalSelections: []QueryFinalSelectionTrace{},
		Assessments:     []QueryTraceAssessment{},
		Extraction: QueryTurnExtraction{
			Intent: turn.Intent, MetricTerms: []string{},
			DimensionValueTerms: []string{},
		},
	}
	selectedMetricCodes := map[string]bool{}
	for _, code := range turn.MetricCodes {
		selectedMetricCodes[strings.ToLower(code)] = true
	}
	metricNames := map[string]string{}
	metricConfidence := map[string]float64{}
	for _, plan := range turn.Plans {
		code := strings.ToLower(plan.Conditions.MetricCode)
		metricNames[code] = queryPlanEvidenceLabel(
			plan, "METRIC", plan.SelectedMetricVersionID,
		)
		metricConfidence[code] = plan.Confidence
	}
	candidateIndex := map[string]int{}
	for _, candidate := range turnSlots.MetricCandidates {
		key := strings.ToLower(candidate.Code)
		candidate.Selected = selectedMetricCodes[key]
		if candidate.Selected && metricNames[key] != "" {
			candidate.Label = metricNames[key]
		}
		candidateIndex[key] = len(trace.MetricCandidates)
		trace.MetricCandidates = append(trace.MetricCandidates, candidate)
	}
	contextMetricCodes := map[string]bool{}
	for _, slots := range contextSlots {
		contextMetricCodes[strings.ToLower(slots.MetricCode)] = true
	}
	for _, code := range turn.MetricCodes {
		key := strings.ToLower(code)
		index, exists := candidateIndex[key]
		if exists {
			if contextMetricCodes[key] &&
				len(turnSlots.MetricCodes) == 0 {
				trace.MetricCandidates[index].MatchMethod = "CONTEXT_PLAN"
				trace.MetricCandidates[index].Source = "CONTEXT_PLAN"
				trace.MetricCandidates[index].MatchedTerm =
					metricNames[key]
			}
			continue
		}
		source, method := "CURRENT_TURN", turnSlots.MetricMatchMethod
		if contextMetricCodes[key] {
			source, method = "CONTEXT_PLAN", "CONTEXT_PLAN"
		}
		trace.MetricCandidates = append(
			trace.MetricCandidates,
			QueryMetricCandidateTrace{
				Code: code, Label: metricNames[key],
				MatchedTerm: metricNames[key], MatchMethod: method,
				Score: metricConfidence[key], Selected: true,
				Source: source,
			},
		)
	}
	lookupSeen := map[string]bool{}
	for _, lookup := range trace.DimensionValueLookups {
		keyBytes, _ := json.Marshal(lookup)
		lookupSeen[string(keyBytes)] = true
	}
	for _, plan := range turn.Plans {
		for _, lookup := range plan.PlanningTrace {
			if lookup.MetricCode == "" {
				lookup.MetricCode = plan.Conditions.MetricCode
			}
			keyBytes, _ := json.Marshal(lookup)
			key := string(keyBytes)
			if lookupSeen[key] {
				continue
			}
			lookupSeen[key] = true
			trace.DimensionValueLookups = append(
				trace.DimensionValueLookups, lookup,
			)
		}
		trace.FinalSelections = append(
			trace.FinalSelections, queryFinalSelectionTrace(plan),
		)
	}
	for index := range trace.FinalSelections {
		whereConditions := []string{}
		compiledConditions := []string{}
		for _, lookup := range trace.DimensionValueLookups {
			if !lookup.Selected || !strings.EqualFold(
				lookup.MetricCode,
				trace.FinalSelections[index].MetricCode,
			) {
				continue
			}
			whereConditions = appendUniqueTraceTerm(
				whereConditions, lookup.WhereCondition,
			)
			compiledConditions = appendUniqueTraceTerm(
				compiledConditions, lookup.CompiledCondition,
			)
		}
		trace.FinalSelections[index].WhereCondition =
			strings.Join(whereConditions, " AND ")
		trace.FinalSelections[index].CompiledCondition =
			strings.Join(compiledConditions, " AND ")
	}
	for _, candidate := range trace.MetricCandidates {
		if !candidate.Selected {
			continue
		}
		term := candidate.MatchedTerm
		if term == "" {
			term = candidate.Label
		}
		trace.Extraction.MetricTerms = appendUniqueTraceTerm(
			trace.Extraction.MetricTerms, term,
		)
	}
	for _, lookup := range trace.DimensionValueLookups {
		trace.Extraction.DimensionValueTerms = appendUniqueTraceTerm(
			trace.Extraction.DimensionValueTerms, lookup.Term,
		)
	}
	trace.StandaloneQuestion = buildStandaloneQuestion(
		trace.ConversationQuestions, trace.FinalSelections,
		trace.DimensionValueLookups,
	)
	allPlansReady := len(turn.Plans) > 0 &&
		len(turn.Plans) == len(turn.MetricCodes)
	for _, plan := range turn.Plans {
		allPlansReady = allPlansReady && plan.Status == "READY"
	}
	trace.Assessments = append(trace.Assessments, QueryTraceAssessment{
		Step: "CONTEXT_SYNTHESIS", Status: "PASS",
		Decision: contextPolicy,
		Detail: fmt.Sprintf(
			"已纳入最近 %d 轮提问；同维度以当前轮为准，其余条件仅从已验证计划继承",
			len(trace.ConversationQuestions),
		),
	})
	intentStatus := "PASS"
	if turn.Intent == "" || turn.Intent == "UNKNOWN" {
		intentStatus = "WARN"
	}
	trace.Assessments = append(trace.Assessments, QueryTraceAssessment{
		Step: "INTENT_EXTRACTION", Status: intentStatus,
		Decision: turn.Intent,
		Detail: fmt.Sprintf(
			"提取 %d 个指标、%d 个维度值词",
			len(trace.Extraction.MetricTerms),
			len(trace.Extraction.DimensionValueTerms),
		),
	})
	metricStatus := "BLOCKED"
	if len(turn.MetricCodes) > 0 {
		metricStatus = "PASS"
	}
	trace.Assessments = append(trace.Assessments, QueryTraceAssessment{
		Step: "METRIC_RETRIEVAL", Status: metricStatus,
		Decision: "PUBLISHED_METRIC_CATALOG",
		Detail: fmt.Sprintf(
			"审查 %d 个真实候选，选中 %d 个已发布指标",
			len(trace.MetricCandidates), len(turn.MetricCodes),
		),
	})
	dimensionStatus, dimensionDecision := "PASS", "NO_DIMENSION_VALUE_REQUEST"
	vectorizedLookups := 0
	vectorEligibleLookups := 0
	selectedLookups := 0
	if len(trace.DimensionValueLookups) > 0 {
		dimensionDecision = "PERSISTED_DECISION_GRAPH_VECTOR_RETRIEVAL"
		for _, lookup := range trace.DimensionValueLookups {
			if !lookup.Sensitive {
				vectorEligibleLookups++
			}
			if lookup.VectorSearchStatus == "SUCCEEDED" &&
				lookup.VectorCandidateCount > 0 {
				vectorizedLookups++
			}
			if lookup.Selected {
				selectedLookups++
			}
		}
		if vectorizedLookups < vectorEligibleLookups {
			dimensionStatus = "BLOCKED"
		}
		trace.Assessments = append(trace.Assessments, QueryTraceAssessment{
			Step: "DIMENSION_VALUE_RETRIEVAL", Status: dimensionStatus,
			Decision: dimensionDecision,
			Detail: fmt.Sprintf(
				"形成 %d 组“维度描述:维度值”检索键，%d 组从持久化决策图召回了候选",
				len(trace.DimensionValueLookups), vectorizedLookups,
			),
		})
		filterStatus := "PASS"
		if selectedLookups < vectorEligibleLookups {
			filterStatus = "BLOCKED"
		}
		trace.Assessments = append(trace.Assessments, QueryTraceAssessment{
			Step: "WHERE_FILTER", Status: filterStatus,
			Decision: "LLM_FILTER_THEN_GOVERNED_COMPILE",
			Detail: fmt.Sprintf(
				"%d/%d 组决策图候选获得唯一保留项并通过安全编译",
				selectedLookups, vectorEligibleLookups,
			),
		})
	} else {
		trace.Assessments = append(trace.Assessments, QueryTraceAssessment{
			Step: "DIMENSION_VALUE_RETRIEVAL", Status: dimensionStatus,
			Decision: dimensionDecision,
			Detail:   "当前问题未要求维度值筛选",
		})
		trace.Assessments = append(trace.Assessments, QueryTraceAssessment{
			Step: "WHERE_FILTER", Status: "PASS",
			Decision: "NO_DIMENSION_WHERE_REQUIRED",
			Detail:   "当前问题没有需要过滤和拼接的维度 WHERE 条件",
		})
	}
	finalStatus := "BLOCKED"
	if allPlansReady {
		finalStatus = "PASS"
	}
	trace.Assessments = append(trace.Assessments, QueryTraceAssessment{
		Step: "FINAL_PLAN", Status: finalStatus,
		Decision: "GOVERNED_QUERY_PLAN",
		Detail: fmt.Sprintf(
			"%d/%d 个计划通过权限、兼容性、版本和血缘门禁",
			countReadyQueryPlans(turn.Plans), len(turn.Plans),
		),
	})
	return trace
}

func queryFinalSelectionTrace(plan QueryPlan) QueryFinalSelectionTrace {
	result := QueryFinalSelectionTrace{
		MetricCode: plan.Conditions.MetricCode,
		MetricName: queryPlanEvidenceLabel(
			plan, "METRIC", plan.SelectedMetricVersionID,
		),
		MetricFieldID:    plan.MetricFieldID,
		MetricVersionID:  plan.SelectedMetricVersionID,
		DatasetVersionID: plan.SelectedDatasetVersionID,
		Dimensions:       []QueryFinalDimensionTrace{},
		TimeRange:        plan.Conditions.TimeRange,
		PlanID:           plan.ID, PlanStatus: plan.Status,
	}
	for _, dimension := range plan.Conditions.Dimensions {
		values := append([]string(nil), dimension.MemberKeys...)
		if dimension.MemberKey != "" {
			values = append(values, dimension.MemberKey)
		}
		result.Dimensions = append(
			result.Dimensions,
			QueryFinalDimensionTrace{
				DimensionCode: dimension.DimensionCode,
				DimensionName: queryPlanEvidenceLabel(
					plan, "DIMENSION", dimension.DimensionID,
				),
				MemberKeys: values,
			},
		)
	}
	return result
}

func queryPlanEvidenceLabel(
	plan QueryPlan,
	subjectType, subjectRef string,
) string {
	for _, evidence := range plan.Evidence {
		if evidence.SubjectType == subjectType &&
			(subjectRef == "" || evidence.SubjectRef == subjectRef) {
			return evidence.Label
		}
	}
	if subjectType == "METRIC" {
		return plan.Conditions.MetricCode
	}
	return ""
}

func buildStandaloneQuestion(
	conversationQuestions []string,
	selections []QueryFinalSelectionTrace,
	lookups []QueryDimensionValueLookupTrace,
) string {
	if len(selections) == 0 {
		return ""
	}
	clauses := make([]string, 0, len(selections))
	for _, selection := range selections {
		dimensions := make([]string, 0, len(selection.Dimensions))
		for _, dimension := range selection.Dimensions {
			name := dimension.DimensionName
			if name == "" {
				name = dimension.DimensionCode
			}
			var chosen *QueryDimensionValueLookupTrace
			for index := range lookups {
				lookup := &lookups[index]
				if lookup.Selected &&
					strings.EqualFold(
						lookup.MetricCode, selection.MetricCode,
					) &&
					strings.EqualFold(
						lookup.DimensionCode, dimension.DimensionCode,
					) {
					chosen = lookup
				}
			}
			if chosen == nil {
				dimensions = append(
					dimensions,
					fmt.Sprintf(
						"%s∈[%s]", name,
						strings.Join(dimension.MemberKeys, "、"),
					),
				)
				continue
			}
			mapping := strings.Join(dimension.MemberKeys, "、")
			if len(dimension.MemberKeys) > 8 {
				mapping = fmt.Sprintf(
					"%d 个已治理标准值", len(dimension.MemberKeys),
				)
			}
			dimensions = append(
				dimensions,
				fmt.Sprintf("%s=%s（映射：%s）", name, chosen.Term, mapping),
			)
		}
		metricName := selection.MetricName
		if metricName == "" {
			metricName = selection.MetricCode
		}
		if len(dimensions) == 0 {
			clauses = append(clauses, metricName)
		} else {
			clauses = append(
				clauses,
				fmt.Sprintf(
					"%s条件下的%s", strings.Join(dimensions, "、"), metricName,
				),
			)
		}
	}
	scope := ""
	for _, question := range conversationQuestions {
		if strings.Contains(strings.ToLower(question), "小微") {
			scope = "在小微人员范围内，"
			break
		}
	}
	return scope + "查询" + strings.Join(clauses, "；") + "。"
}

func appendUniqueTraceTerm(values []string, value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return values
	}
	for _, existing := range values {
		if strings.EqualFold(existing, value) {
			return values
		}
	}
	return append(values, value)
}

func countReadyQueryPlans(plans []QueryPlan) int {
	total := 0
	for _, plan := range plans {
		if plan.Status == "READY" {
			total++
		}
	}
	return total
}

func inheritQueryContext(input *QueryPlanInput, contextSlots QuerySlots) {
	if input.MetricCode == "" {
		input.MetricCode = contextSlots.MetricCode
		if input.MetricCode != "" {
			input.MetricCandidateCount = 1
			input.MetricMatchMethod = "CONTEXT"
			input.Domain = contextSlots.Domain
		}
	}
	if input.DimensionCode == "" {
		input.DimensionCode = contextSlots.DimensionCode
	}
	input.MemberFilters = mergeQueryMemberFilters(
		contextSlots.MemberFilters, input.MemberFilters,
	)
	input.DimensionValueLookups = append(
		append(
			[]QueryDimensionValueLookupTrace(nil),
			contextSlots.DimensionValueLookups...,
		),
		input.DimensionValueLookups...,
	)
	if input.TimeRange == nil && input.TimePreset == "" &&
		contextSlots.TimeRange != nil {
		inherited := *contextSlots.TimeRange
		input.TimeRange = &inherited
	}
	if input.Intent == "UNKNOWN" && contextSlots.Intent != "" {
		input.Intent = contextSlots.Intent
	}
}

// mergeQueryMemberFilters applies conversational refinement semantics:
// dimensions named in the current turn replace the same prior dimension,
// while every other governed prior filter remains in scope.
func mergeQueryMemberFilters(
	context, current []QueryMemberFilterInput,
) []QueryMemberFilterInput {
	currentDimensions := make(map[string]bool, len(current))
	for _, filter := range current {
		currentDimensions[strings.ToLower(
			strings.TrimSpace(filter.DimensionCode),
		)] = true
	}
	result := make(
		[]QueryMemberFilterInput, 0, len(context)+len(current),
	)
	for _, filter := range context {
		if !currentDimensions[strings.ToLower(
			strings.TrimSpace(filter.DimensionCode),
		)] {
			result = append(result, filter)
		}
	}
	return append(result, current...)
}

func normalizeQueryTimeRange(value QueryTimeRange) (QueryTimeRange, error) {
	value.Start = strings.TrimSpace(value.Start)
	value.EndExclusive = strings.TrimSpace(value.EndExclusive)
	start, startDateOnly, err := parseQueryBoundary(value.Start)
	if err != nil {
		return QueryTimeRange{}, err
	}
	end, endDateOnly, err := parseQueryBoundary(value.EndExclusive)
	if err != nil || !start.Before(end) {
		return QueryTimeRange{}, ErrInvalidRequest
	}
	if startDateOnly != endDateOnly {
		return QueryTimeRange{}, ErrInvalidRequest
	}
	if startDateOnly {
		value.Start = start.Format(time.DateOnly)
		value.EndExclusive = end.Format(time.DateOnly)
	} else {
		value.Start = start.UTC().Format(time.RFC3339)
		value.EndExclusive = end.UTC().Format(time.RFC3339)
	}
	return value, nil
}

func normalizeQueryMemberFilters(
	primaryDimension, primaryMember string,
	filters []QueryMemberFilterInput,
) ([]QueryMemberFilterInput, error) {
	if len(filters) > 8 {
		return nil, ErrInvalidRequest
	}
	seenDimensions := map[string]bool{}
	if primaryMember != "" && primaryDimension != "" {
		seenDimensions[strings.ToLower(primaryDimension)] = true
	}
	result := make([]QueryMemberFilterInput, len(filters))
	for index, memberFilter := range filters {
		memberFilter.DimensionCode = strings.TrimSpace(memberFilter.DimensionCode)
		memberFilter.MemberValue = strings.TrimSpace(memberFilter.MemberValue)
		values := memberFilter.MemberValues
		if memberFilter.MemberValue != "" {
			if len(values) > 0 {
				return nil, ErrInvalidRequest
			}
			values = []string{memberFilter.MemberValue}
		}
		if len(values) == 0 || len(values) > maxSemanticMemberSetSize {
			return nil, ErrInvalidRequest
		}
		normalizedValues := make([]string, 0, len(values))
		seenValues := map[string]bool{}
		for _, value := range values {
			value = strings.TrimSpace(value)
			key := strings.ToLower(value)
			if value == "" || len(value) > 1024 || seenValues[key] {
				return nil, ErrInvalidRequest
			}
			seenValues[key] = true
			normalizedValues = append(normalizedValues, value)
		}
		key := strings.ToLower(memberFilter.DimensionCode)
		if memberFilter.DimensionCode == "" ||
			len(memberFilter.DimensionCode) > 128 ||
			seenDimensions[key] {
			return nil, ErrInvalidRequest
		}
		seenDimensions[key] = true
		if len(normalizedValues) == 1 {
			memberFilter.MemberValue = normalizedValues[0]
			memberFilter.MemberValues = nil
		} else {
			memberFilter.MemberValue = ""
			memberFilter.MemberValues = normalizedValues
		}
		result[index] = memberFilter
	}
	return result, nil
}

func parseQueryBoundary(value string) (time.Time, bool, error) {
	if parsed, err := time.Parse(time.DateOnly, value); err == nil {
		return parsed.UTC(), true, nil
	}
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}, false, ErrInvalidRequest
	}
	return parsed, false, nil
}

func inferQueryTimePreset(question string) string {
	question = strings.ToLower(strings.TrimSpace(question))
	compactQuestion := strings.Join(strings.Fields(question), "")
	if match := queryCutoffMonthPattern.FindStringSubmatch(compactQuestion); len(match) == 3 {
		month, err := strconv.Atoi(match[2])
		if err == nil && month >= 1 && month <= 12 {
			if match[1] != "" {
				return fmt.Sprintf("THROUGH_%s_%02d", match[1], month)
			}
			return fmt.Sprintf("THROUGH_%02d", month)
		}
	}
	for _, candidate := range []struct {
		preset  string
		phrases []string
	}{
		{preset: "YESTERDAY", phrases: []string{"昨天", "昨日", "yesterday"}},
		{preset: "LAST_7_DAYS", phrases: []string{"最近7天", "近7天", "过去7天", "last 7 days"}},
		{preset: "LAST_30_DAYS", phrases: []string{"最近30天", "近30天", "过去30天", "last 30 days"}},
		{preset: "LAST_MONTH", phrases: []string{"上个月", "上月", "last month"}},
		{preset: "THIS_MONTH", phrases: []string{"这个月", "本月", "this month"}},
		{preset: "LAST_YEAR", phrases: []string{"去年", "上年", "last year"}},
		{preset: "THIS_YEAR", phrases: []string{"今年", "本年", "this year"}},
		{preset: "TODAY", phrases: []string{"今天", "今日", "today"}},
	} {
		for _, phrase := range candidate.phrases {
			if strings.Contains(question, phrase) ||
				strings.Contains(
					compactQuestion,
					strings.Join(strings.Fields(phrase), ""),
				) {
				return candidate.preset
			}
		}
	}
	return ""
}

func validQueryTimePreset(preset string) bool {
	return oneOf(
		preset, "", "TODAY", "YESTERDAY", "LAST_7_DAYS",
		"LAST_30_DAYS", "THIS_MONTH", "LAST_MONTH",
		"THIS_YEAR", "LAST_YEAR",
	) || queryCutoffPresetPattern.MatchString(preset)
}

func parseQueryCutoffPreset(
	preset string,
	defaultYear int,
) (year int, month time.Month, ok bool) {
	match := queryCutoffPresetPattern.FindStringSubmatch(preset)
	if len(match) != 3 {
		return 0, 0, false
	}
	year = defaultYear
	var err error
	if match[1] != "" {
		year, err = strconv.Atoi(match[1])
		if err != nil || year < 1970 || year > 9999 {
			return 0, 0, false
		}
	}
	monthNumber, err := strconv.Atoi(match[2])
	if err != nil || monthNumber < 1 || monthNumber > 12 {
		return 0, 0, false
	}
	return year, time.Month(monthNumber), true
}

func inferQueryComparisonMode(question string) string {
	question = strings.ToLower(strings.TrimSpace(question))
	switch {
	case strings.Contains(question, "同比"),
		strings.Contains(question, "year over year"),
		strings.Contains(question, "year-over-year"):
		return "YEAR_OVER_YEAR"
	case strings.Contains(question, "环比"),
		strings.Contains(question, "较上期"),
		strings.Contains(question, "previous period"):
		return "PREVIOUS_PERIOD"
	default:
		return ""
	}
}

func resolveQueryTimePreset(
	preset, timezone, fieldType string,
	now time.Time,
) (QueryTimeRange, error) {
	location, err := time.LoadLocation(timezone)
	if err != nil || !oneOf(fieldType, "DATE", "DATETIME") {
		return QueryTimeRange{}, ErrInvalidRequest
	}
	localNow := now.In(location)
	today := time.Date(
		localNow.Year(), localNow.Month(), localNow.Day(),
		0, 0, 0, 0, location,
	)
	var start, end time.Time
	if year, month, cutoff := parseQueryCutoffPreset(
		preset, localNow.Year(),
	); cutoff {
		// “截至某月”是累计上限，不是“该月内”。平台业务数据的可移植
		// 时间纪元固定为 1970-01-01，结束边界取目标月下一月首日。
		start = time.Date(1970, 1, 1, 0, 0, 0, 0, location)
		end = time.Date(year, month, 1, 0, 0, 0, 0, location).
			AddDate(0, 1, 0)
	} else {
		switch preset {
		case "TODAY":
			start, end = today, today.AddDate(0, 0, 1)
		case "YESTERDAY":
			start, end = today.AddDate(0, 0, -1), today
		case "LAST_7_DAYS":
			start, end = today.AddDate(0, 0, -6), today.AddDate(0, 0, 1)
		case "LAST_30_DAYS":
			start, end = today.AddDate(0, 0, -29), today.AddDate(0, 0, 1)
		case "THIS_MONTH":
			start = time.Date(localNow.Year(), localNow.Month(), 1, 0, 0, 0, 0, location)
			end = start.AddDate(0, 1, 0)
		case "LAST_MONTH":
			end = time.Date(localNow.Year(), localNow.Month(), 1, 0, 0, 0, 0, location)
			start = end.AddDate(0, -1, 0)
		case "THIS_YEAR":
			start = time.Date(localNow.Year(), 1, 1, 0, 0, 0, 0, location)
			end = start.AddDate(1, 0, 0)
		case "LAST_YEAR":
			end = time.Date(localNow.Year(), 1, 1, 0, 0, 0, 0, location)
			start = end.AddDate(-1, 0, 0)
		default:
			return QueryTimeRange{}, ErrInvalidRequest
		}
	}
	if fieldType == "DATE" {
		return QueryTimeRange{
			Start: start.Format(time.DateOnly), EndExclusive: end.Format(time.DateOnly),
		}, nil
	}
	return QueryTimeRange{
		Start:        start.UTC().Format(time.RFC3339),
		EndExclusive: end.UTC().Format(time.RFC3339),
	}, nil
}

func deriveQueryComparisonRange(
	current QueryTimeRange,
	mode, preset, timezone, fieldType string,
) (QueryTimeRange, error) {
	if !oneOf(mode, "PREVIOUS_PERIOD", "YEAR_OVER_YEAR") {
		return QueryTimeRange{}, ErrInvalidRequest
	}
	location, err := time.LoadLocation(timezone)
	if err != nil || !oneOf(fieldType, "DATE", "DATETIME") {
		return QueryTimeRange{}, ErrInvalidRequest
	}
	parse := func(value string) (time.Time, error) {
		if fieldType == "DATE" {
			return time.ParseInLocation(time.DateOnly, value, location)
		}
		parsed, err := time.Parse(time.RFC3339, value)
		if err != nil {
			return time.Time{}, err
		}
		return parsed.In(location), nil
	}
	currentStart, err := parse(current.Start)
	if err != nil {
		return QueryTimeRange{}, ErrInvalidRequest
	}
	currentEnd, err := parse(current.EndExclusive)
	if err != nil || !currentStart.Before(currentEnd) {
		return QueryTimeRange{}, ErrInvalidRequest
	}
	var baselineStart, baselineEnd time.Time
	if mode == "YEAR_OVER_YEAR" {
		baselineStart = currentStart.AddDate(-1, 0, 0)
		baselineEnd = currentEnd.AddDate(-1, 0, 0)
	} else {
		baselineEnd = currentStart
		switch preset {
		case "TODAY", "YESTERDAY":
			baselineStart = baselineEnd.AddDate(0, 0, -1)
		case "LAST_7_DAYS":
			baselineStart = baselineEnd.AddDate(0, 0, -7)
		case "LAST_30_DAYS":
			baselineStart = baselineEnd.AddDate(0, 0, -30)
		case "THIS_MONTH", "LAST_MONTH":
			baselineStart = baselineEnd.AddDate(0, -1, 0)
		case "THIS_YEAR", "LAST_YEAR":
			baselineStart = baselineEnd.AddDate(-1, 0, 0)
		default:
			baselineStart = baselineEnd.Add(-currentEnd.Sub(currentStart))
		}
	}
	if fieldType == "DATE" {
		return QueryTimeRange{
			Start:        baselineStart.Format(time.DateOnly),
			EndExclusive: baselineEnd.Format(time.DateOnly),
		}, nil
	}
	return QueryTimeRange{
		Start:        baselineStart.UTC().Format(time.RFC3339),
		EndExclusive: baselineEnd.UTC().Format(time.RFC3339),
	}, nil
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
) (output QueryPlanExecution, runErr error) {
	var machine *questionStateMachine
	defer func() {
		if runErr != nil {
			blockQuestionState(nil, machine)
		}
	}()
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
	machine = newQuestionStateMachine(input.QueryID)
	if err := machine.advance(QuestionStateReceived); err != nil {
		return QueryPlanExecution{}, err
	}
	if err := persistQuestionStateMachine(
		ctx, service, tenantID, actorID,
		hashText(id+"\x00"+input.ExpectedPathHash), machine,
	); err != nil {
		return QueryPlanExecution{}, err
	}
	for _, state := range []QuestionState{
		QuestionStateAuthorized,
		QuestionStateContextReady, QuestionStatePlanReady,
		QuestionStateValidating,
	} {
		if err := machine.advance(state); err != nil {
			return QueryPlanExecution{}, err
		}
	}
	plan, binding, err := service.store.PrepareQueryPlanExecution(
		ctx, tenantID, id,
		input.ExpectedGraphGenerationID, input.ExpectedPathHash,
	)
	if err != nil {
		return QueryPlanExecution{}, err
	}
	if err := machine.advance(QuestionStateCostApproved); err != nil {
		return QueryPlanExecution{}, err
	}
	dimensionFields := []string{}
	if binding.DimensionFieldID != "" {
		dimensionFields = append(dimensionFields, binding.DimensionFieldID)
	}
	if plan.Intent == "TREND" && binding.TimeFieldID != "" &&
		binding.TimeFieldID != binding.DimensionFieldID {
		dimensionFields = append(dimensionFields, binding.TimeFieldID)
	}
	maxRows := input.MaxRows
	if binding.TopN > 0 && (maxRows == 0 || maxRows > binding.TopN) {
		maxRows = binding.TopN
	}
	executeRange := func(
		queryID string,
		timeRange *QueryTimeRange,
	) (dataset.PreviewResult, error) {
		dimensionFilters := []metric.DimensionFilter{}
		for _, memberFilter := range binding.MemberFilters {
			operator := "EQUALS"
			value := any(memberFilter.MemberKey)
			if len(memberFilter.MemberKeys) > 1 {
				operator = "IN"
				value = memberFilter.MemberKeys
			}
			dimensionFilters = append(dimensionFilters, metric.DimensionFilter{
				FieldID:  memberFilter.FieldID,
				Operator: operator, Value: value,
			})
		}
		if timeRange != nil {
			dimensionFilters = append(dimensionFilters,
				metric.DimensionFilter{
					FieldID:  binding.TimeFieldID,
					Operator: "GTE", Value: timeRange.Start,
				},
				metric.DimensionFilter{
					FieldID:  binding.TimeFieldID,
					Operator: "LT", Value: timeRange.EndExclusive,
				},
			)
		}
		return service.metricExecutor.PreviewVersion(
			ctx, tenantID, actorID, plan.SelectedMetricID,
			plan.SelectedMetricVersionID,
			metric.PreviewInput{
				QueryID: queryID, Parameters: input.Parameters,
				DimensionFieldIDs:   dimensionFields,
				DimensionFilters:    dimensionFilters,
				MetricSortDirection: binding.SortDirection,
				MaxRows:             maxRows,
			},
		)
	}
	var baselineResult *dataset.PreviewResult
	if err := machine.advance(QuestionStateExecuting); err != nil {
		return QueryPlanExecution{}, err
	}
	if binding.ComparisonRange != nil {
		baselineQueryID := uuid.NewSHA1(
			uuid.MustParse(input.QueryID),
			[]byte("semantic-comparison-baseline"),
		).String()
		value, executeErr := executeRange(
			baselineQueryID, binding.ComparisonRange,
		)
		if executeErr != nil {
			_, _ = service.store.FinishQueryPlanExecution(
				ctx, tenantID, id, baselineQueryID,
				"METRIC_COMPARISON_EXECUTION_FAILED", false,
				input.ExpectedGraphGenerationID, 0, 0,
			)
			return QueryPlanExecution{}, executeErr
		}
		baselineResult = &value
	}
	result, executeErr := executeRange(input.QueryID, binding.TimeRange)
	if executeErr != nil {
		_, _ = service.store.FinishQueryPlanExecution(
			ctx, tenantID, id, input.QueryID,
			"METRIC_EXECUTION_FAILED", false, input.ExpectedGraphGenerationID,
			0, 0,
		)
		return QueryPlanExecution{}, executeErr
	}
	answerEvidence, evidenceErr := buildAnswerEvidence(
		plan, result, baselineResult, time.Now().UTC(),
	)
	if evidenceErr != nil {
		_, _ = service.store.FinishQueryPlanExecution(
			ctx, tenantID, id, input.QueryID,
			"RESULT_EVIDENCE_HASH_FAILED", false,
			input.ExpectedGraphGenerationID, 0, 0,
		)
		return QueryPlanExecution{}, fmt.Errorf(
			"%w: result evidence hash: %v", ErrUnprovenPath, evidenceErr,
		)
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
	execution := QueryPlanExecution{
		QuestionRunID: machine.runID,
		QueryPlan:     plan,
		Result:        result,
		Evidence:      answerEvidence,
	}
	// FinishQueryPlanExecution reloads the authoritative plan after the final
	// generation check. Use that copy for the evidence returned to the client.
	execution.Evidence.Lineage = append([]QueryEvidence(nil), plan.Evidence...)
	if baselineResult != nil &&
		binding.TimeRange != nil && binding.ComparisonRange != nil {
		execution.Comparison = &QueryComparisonExecution{
			Mode:          binding.ComparisonMode,
			CurrentRange:  *binding.TimeRange,
			BaselineRange: *binding.ComparisonRange,
			Baseline:      *baselineResult,
		}
	}
	if err := machine.advance(QuestionStateResultVerified); err != nil {
		return QueryPlanExecution{}, err
	}
	if err := machine.advance(QuestionStateAnswered); err != nil {
		return QueryPlanExecution{}, err
	}
	execution.State = machine.state
	execution.Lifecycle = machine.lifecycle()
	return execution, nil
}

// buildAnswerEvidence creates the public, reproducible proof that an executed
// result may be shown. It contains hashes and named validator outcomes, not
// model reasoning, prompts, SQL text or warehouse credentials.
func buildAnswerEvidence(
	plan QueryPlan,
	result dataset.PreviewResult,
	baseline *dataset.PreviewResult,
	verifiedAt time.Time,
) (AnswerEvidence, error) {
	queryPlanHash, err := hashJSON(struct {
		Intent     string                 `json:"intent"`
		Conditions QueryConditionDocument `json:"conditions"`
		PathHash   string                 `json:"pathHash"`
	}{
		Intent: plan.Intent, Conditions: plan.Conditions, PathHash: plan.PathHash,
	})
	if err != nil {
		return AnswerEvidence{}, err
	}
	type resultSnapshot struct {
		Columns  []string `json:"columns"`
		Rows     [][]any  `json:"rows"`
		RowCount int      `json:"rowCount"`
	}
	type resultEnvelope struct {
		Current  resultSnapshot  `json:"current"`
		Baseline *resultSnapshot `json:"baseline,omitempty"`
	}
	envelope := resultEnvelope{Current: resultSnapshot{
		Columns: append([]string(nil), result.Columns...),
		Rows:    result.Rows, RowCount: result.RowCount,
	}}
	if baseline != nil {
		envelope.Baseline = &resultSnapshot{
			Columns: append([]string(nil), baseline.Columns...),
			Rows:    baseline.Rows, RowCount: baseline.RowCount,
		}
	}
	resultHash, err := hashJSON(envelope)
	if err != nil {
		return AnswerEvidence{}, err
	}
	return AnswerEvidence{
		GraphGenerationID: plan.GraphGenerationID,
		GraphGeneration:   plan.GraphGeneration,
		SemanticVersion: fmt.Sprintf(
			"semantic-graph-%d", plan.GraphGeneration,
		),
		PathHash:              plan.PathHash,
		QueryPlanHash:         queryPlanHash,
		ResultHash:            resultHash,
		QueryTraceID:          result.QueryID,
		VerifiedAt:            verifiedAt.UTC().Format(time.RFC3339Nano),
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
		ValidatorChecks: []string{
			"policy_pass",
			"semantic_version_pass",
			"graph_path_pass",
			"metric_contract_pass",
			"dimension_compatibility_pass",
			"freshness_pass",
			"result_execution_pass",
		},
	}, nil
}

func (service *Service) SubmitQueryFeedback(
	ctx context.Context,
	tenantID, actorID, queryPlanID string,
	input SubmitQueryFeedbackInput,
) (QueryFeedback, error) {
	input.Rating = strings.ToUpper(strings.TrimSpace(input.Rating))
	input.Comment = strings.TrimSpace(input.Comment)
	if service == nil || service.store == nil ||
		uuid.Validate(tenantID) != nil || uuid.Validate(actorID) != nil ||
		uuid.Validate(queryPlanID) != nil ||
		!oneOf(input.Rating, "ACCURATE", "INACCURATE") ||
		len([]rune(input.Comment)) > 2000 ||
		containsControl(input.Comment) {
		return QueryFeedback{}, ErrInvalidRequest
	}
	return service.store.UpsertQueryFeedback(
		ctx, tenantID, actorID, queryPlanID, input,
	)
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

func containsControl(value string) bool {
	for _, character := range value {
		if unicode.IsControl(character) {
			return true
		}
	}
	return false
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
