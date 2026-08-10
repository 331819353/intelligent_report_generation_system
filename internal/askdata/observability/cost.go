package observability

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
	"time"

	"intelligent-report-generation-system/internal/askdata"
)

var costLabelPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]{0,127}$`)

type CostRecord struct {
	ID               askdata.ID
	RunID            askdata.ID
	TenantID         askdata.ID
	DomainID         askdata.ID
	ActorID          askdata.ID
	QuestionType     string
	Provider         string
	Model            string
	PromptTokens     int64
	CompletionTokens int64
	CostCents        int64
	QueryScanBytes   int64
	CreatedAt        time.Time
}

func (record CostRecord) Validate() error {
	for label, id := range map[string]askdata.ID{
		"id": record.ID, "run": record.RunID, "tenant": record.TenantID,
		"domain": record.DomainID, "actor": record.ActorID,
	} {
		if id.Validate() != nil {
			return fmt.Errorf("cost record %s is invalid", label)
		}
	}
	for _, label := range []string{record.QuestionType, record.Provider, record.Model} {
		if !costLabelPattern.MatchString(label) {
			return errors.New("cost record label is invalid")
		}
	}
	if record.PromptTokens < 0 || record.CompletionTokens < 0 || record.CostCents < 0 || record.QueryScanBytes < 0 || record.CreatedAt.IsZero() {
		return errors.New("cost record amount is invalid")
	}
	if record.PromptTokens == 0 && record.CompletionTokens == 0 && record.QueryScanBytes == 0 {
		return errors.New("cost record has no attributable usage")
	}
	return nil
}

type BudgetUsageEvidence struct {
	SchemaVersion     string
	RunType           string
	BudgetClass       string
	LLMCallsUsed      int64
	PrimaryQueries    int64
	ValidationQueries int64
}

func ParseBudgetUsageEvidence(raw json.RawMessage) (BudgetUsageEvidence, error) {
	var document struct {
		SchemaVersion string `json:"schemaVersion"`
		RunType       string `json:"runType"`
		BudgetClass   string `json:"budgetClass"`
		Limits        struct {
			MaxLLMCalls          int64 `json:"maxLlmCalls"`
			MaxToolCalls         int64 `json:"maxToolCalls"`
			MaxPrimaryQueries    int64 `json:"maxPrimaryQueries"`
			MaxValidationQueries int64 `json:"maxValidationQueries"`
			MaxCandidateCompares int64 `json:"maxCandidateCompares"`
			MaxJoinHops          int64 `json:"maxJoinHops"`
			HardTimeoutMS        int64 `json:"hardTimeoutMs"`
			P95TargetMS          int64 `json:"p95TargetMs"`
			MaxConcurrentPlans   int64 `json:"maxConcurrentPlans"`
		} `json:"limits"`
		Usage struct {
			LLMCallsUsed          int64 `json:"llmCallsUsed"`
			ToolCallsUsed         int64 `json:"toolCallsUsed"`
			PrimaryQueriesUsed    int64 `json:"primaryQueriesUsed"`
			ValidationQueriesUsed int64 `json:"validationQueriesUsed"`
			CandidateComparesUsed int64 `json:"candidateComparesUsed"`
			MaxJoinHopsUsed       int64 `json:"maxJoinHopsUsed"`
			ElapsedMS             int64 `json:"elapsedMs"`
		} `json:"usage"`
		P95TargetExceeded bool `json:"p95TargetExceeded"`
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&document); err != nil {
		return BudgetUsageEvidence{}, errors.New("budget consumption evidence is invalid")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) || document.SchemaVersion != "run-budget-consumption-v1" ||
		document.RunType == "" || document.BudgetClass == "" || document.Usage.LLMCallsUsed < 0 ||
		document.Usage.ToolCallsUsed < 0 || document.Usage.PrimaryQueriesUsed < 0 ||
		document.Usage.ValidationQueriesUsed < 0 || document.Usage.CandidateComparesUsed < 0 ||
		document.Usage.MaxJoinHopsUsed < 0 || document.Usage.ElapsedMS < 0 {
		return BudgetUsageEvidence{}, errors.New("budget consumption evidence is invalid")
	}
	return BudgetUsageEvidence{
		SchemaVersion: document.SchemaVersion, RunType: document.RunType,
		BudgetClass: document.BudgetClass, LLMCallsUsed: document.Usage.LLMCallsUsed,
		PrimaryQueries:    document.Usage.PrimaryQueriesUsed,
		ValidationQueries: document.Usage.ValidationQueriesUsed,
	}, nil
}

type CostGroup struct {
	TenantID     askdata.ID `json:"tenantId"`
	DomainID     askdata.ID `json:"domainId,omitempty"`
	ActorID      askdata.ID `json:"actorId,omitempty"`
	QuestionType string     `json:"questionType,omitempty"`
}

type CostAggregate struct {
	Group            CostGroup `json:"group"`
	Records          int64     `json:"records"`
	PromptTokens     int64     `json:"promptTokens"`
	CompletionTokens int64     `json:"completionTokens"`
	CostCents        int64     `json:"costCents"`
	QueryScanBytes   int64     `json:"queryScanBytes"`
}

type CostGroupDimension string

const (
	CostByTenant       CostGroupDimension = "TENANT"
	CostByDomain       CostGroupDimension = "DOMAIN"
	CostByUser         CostGroupDimension = "USER"
	CostByQuestionType CostGroupDimension = "QUESTION_TYPE"
)

func AggregateCosts(records []CostRecord, dimension CostGroupDimension) ([]CostAggregate, error) {
	if dimension != CostByTenant && dimension != CostByDomain && dimension != CostByUser && dimension != CostByQuestionType {
		return nil, errors.New("cost aggregation dimension is invalid")
	}
	byKey := make(map[string]*CostAggregate)
	for _, record := range records {
		if err := record.Validate(); err != nil {
			return nil, err
		}
		group := CostGroup{TenantID: record.TenantID}
		key := string(record.TenantID)
		switch dimension {
		case CostByDomain:
			group.DomainID = record.DomainID
			key += "\x00" + string(record.DomainID)
		case CostByUser:
			group.ActorID = record.ActorID
			key += "\x00" + string(record.ActorID)
		case CostByQuestionType:
			group.QuestionType = record.QuestionType
			key += "\x00" + record.QuestionType
		}
		aggregate := byKey[key]
		if aggregate == nil {
			aggregate = &CostAggregate{Group: group}
			byKey[key] = aggregate
		}
		aggregate.Records++
		if !safeAdd(&aggregate.PromptTokens, record.PromptTokens) ||
			!safeAdd(&aggregate.CompletionTokens, record.CompletionTokens) ||
			!safeAdd(&aggregate.CostCents, record.CostCents) ||
			!safeAdd(&aggregate.QueryScanBytes, record.QueryScanBytes) {
			return nil, errors.New("cost aggregation overflow")
		}
	}
	result := make([]CostAggregate, 0, len(byKey))
	for _, aggregate := range byKey {
		result = append(result, *aggregate)
	}
	sort.Slice(result, func(i, j int) bool {
		return costGroupKey(result[i].Group) < costGroupKey(result[j].Group)
	})
	return result, nil
}

func safeAdd(target *int64, value int64) bool {
	if value < 0 || *target > int64(^uint64(0)>>1)-value {
		return false
	}
	*target += value
	return true
}

func costGroupKey(group CostGroup) string {
	return strings.Join([]string{string(group.TenantID), string(group.DomainID), string(group.ActorID), group.QuestionType}, "\x00")
}
