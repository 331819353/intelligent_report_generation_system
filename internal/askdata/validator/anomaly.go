package validator

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"math/big"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/cognition"
	"intelligent-report-generation-system/internal/askdata/compiler"
	"intelligent-report-generation-system/internal/askdata/evaluation"
	"intelligent-report-generation-system/internal/askdata/registry"
)

const VerificationVersion = "semantic-result-verification-v1"

var (
	ErrInvalidResultVerifier = errors.New("semantic result verifier is invalid")
	ErrLLMResultVerification = errors.New("semantic LLM result verification failed")
)

type ResultCognition interface {
	Execute(context.Context, cognition.RoundRequest) (cognition.RoundResult, error)
}

type VerifierOptions struct {
	PromptVersion         string
	PreferredModel        string
	MaxOutputTokens       int
	AnomalyRelativeChange float64
}

func DefaultVerifierOptions() VerifierOptions {
	return VerifierOptions{
		PromptVersion: "askdata-result-verification-v1", MaxOutputTokens: 4096,
		AnomalyRelativeChange: 5,
	}
}

func (options VerifierOptions) Validate() error {
	if strings.TrimSpace(options.PromptVersion) == "" || len(options.PromptVersion) > 128 ||
		options.MaxOutputTokens < 1 || options.MaxOutputTokens > 8192 ||
		math.IsNaN(options.AnomalyRelativeChange) || math.IsInf(options.AnomalyRelativeChange, 0) ||
		options.AnomalyRelativeChange <= 0 || options.AnomalyRelativeChange > 1000 {
		return ErrInvalidResultVerifier
	}
	return nil
}

type VerificationRequest struct {
	Rules            ResultRuleRequest
	Conversation     cognition.PromptFact
	ResourceType     string
	ResourceID       string
	SeenActionHashes []askdata.ContentHash
}

type MetricResultSummary struct {
	Column       string `json:"column"`
	NonNullCount int    `json:"nonNullCount"`
	NullCount    int    `json:"nullCount"`
	Minimum      string `json:"minimum,omitempty"`
	Maximum      string `json:"maximum,omitempty"`
	Sum          string `json:"sum,omitempty"`
}

type ColumnResultSummary struct {
	Column        string                `json:"column"`
	Type          evaluation.ScalarType `json:"type"`
	Key           bool                  `json:"key"`
	NullCount     int                   `json:"nullCount"`
	DistinctCount int                   `json:"distinctCount"`
}

type PlanResultSummary struct {
	Role     compiler.QueryRole    `json:"role"`
	RowCount int                   `json:"rowCount"`
	Columns  []ColumnResultSummary `json:"columns"`
	Metrics  []MetricResultSummary `json:"metrics"`
}

type TrendSummary struct {
	Available           bool    `json:"available"`
	Anomalous           bool    `json:"anomalous"`
	ComparedMetrics     int     `json:"comparedMetrics"`
	BaselineZeroMetrics int     `json:"baselineZeroMetrics"`
	IncreasedMetrics    int     `json:"increasedMetrics"`
	DecreasedMetrics    int     `json:"decreasedMetrics"`
	UnchangedMetrics    int     `json:"unchangedMetrics"`
	MaxRelativeChange   float64 `json:"maxRelativeChange"`
}

type SanitizedResultSummary struct {
	QueryPlanHash   askdata.ContentHash `json:"queryPlanHash"`
	ResultHash      askdata.ContentHash `json:"resultHash"`
	Plans           []PlanResultSummary `json:"plans"`
	Trend           TrendSummary        `json:"trend"`
	RuleChecks      []RuleCheck         `json:"ruleChecks"`
	RulePassed      bool                `json:"rulePassed"`
	NoDataConfirmed bool                `json:"noDataConfirmed"`
}

type FinalVerificationVerdict string

const (
	FinalVerificationPass    FinalVerificationVerdict = "PASS"
	FinalVerificationRetry   FinalVerificationVerdict = "RETRY"
	FinalVerificationClarify FinalVerificationVerdict = "CLARIFY"
	FinalVerificationBlock   FinalVerificationVerdict = "BLOCK"
)

type VerificationArtifact struct {
	Version               string                     `json:"version"`
	QueryPlanHash         askdata.ContentHash        `json:"queryPlanHash"`
	ResultHash            askdata.ContentHash        `json:"resultHash"`
	RuleArtifact          RuleArtifact               `json:"ruleArtifact"`
	Summary               SanitizedResultSummary     `json:"summary"`
	Anomaly               *cognition.AnomalyAnalysis `json:"anomaly,omitempty"`
	LLMVerification       cognition.Verification     `json:"llmVerification"`
	FinalVerdict          FinalVerificationVerdict   `json:"finalVerdict"`
	RuleOverridePrevented bool                       `json:"ruleOverridePrevented"`
	EvidenceRefs          []askdata.EvidenceRef      `json:"evidenceRefs"`
	VerificationHash      askdata.ContentHash        `json:"verificationHash"`
}

type Verifier struct {
	cognition ResultCognition
	options   VerifierOptions
}

func NewResultVerifier(
	reviewer ResultCognition,
	configured ...VerifierOptions,
) (*Verifier, error) {
	if reviewer == nil || len(configured) > 1 {
		return nil, ErrInvalidResultVerifier
	}
	options := DefaultVerifierOptions()
	if len(configured) == 1 {
		options = configured[0]
	}
	if options.Validate() != nil {
		return nil, ErrInvalidResultVerifier
	}
	return &Verifier{cognition: reviewer, options: options}, nil
}

func (verifier *Verifier) Verify(ctx context.Context, request VerificationRequest) (VerificationArtifact, error) {
	if verifier == nil || verifier.cognition == nil || verifier.options.Validate() != nil || ctx == nil ||
		len(request.SeenActionHashes) > 64 {
		return VerificationArtifact{}, ErrInvalidResultVerifier
	}
	for _, hash := range request.SeenActionHashes {
		if hash.Validate() != nil {
			return VerificationArtifact{}, ErrInvalidResultEvidence
		}
	}
	rules, err := evaluateResultRules(request.Rules)
	if err != nil {
		return VerificationArtifact{}, err
	}
	summary, err := buildSanitizedSummary(request.Rules, rules, verifier.options.AnomalyRelativeChange)
	if err != nil {
		return VerificationArtifact{}, err
	}
	if summary.Trend.Anomalous {
		rules.artifact.RequiresAnomalyAnalysis = true
		rules.artifact.RuleHash, err = ruleArtifactHash(rules.artifact)
		if err != nil || rules.artifact.Validate() != nil {
			return VerificationArtifact{}, ErrInvalidResultEvidence
		}
		summary.RuleChecks = rules.artifact.Checks
	}
	resourceID := request.ResourceID
	if resourceID == "" {
		resourceID = request.Rules.Execution.Artifact.RunID
	}
	resourceType := strings.TrimSpace(request.ResourceType)
	if resourceType == "" {
		resourceType = "ASKDATA_QUESTION_RUN"
	}
	if askdata.ID(resourceID).Validate() != nil || askdata.ID(resourceType).Validate() != nil {
		return VerificationArtifact{}, ErrInvalidResultEvidence
	}

	var anomaly *cognition.AnomalyAnalysis
	allEvidence := []askdata.EvidenceRef{}
	if rules.artifact.RequiresAnomalyAnalysis {
		facts, known, err := verificationFacts(request, summary, nil)
		if err != nil {
			return VerificationArtifact{}, err
		}
		messages, err := cognition.BuildMessages(cognition.PromptInput{
			Stage: cognition.StageAnomalyAnalysis, Facts: facts,
		})
		if err != nil {
			return VerificationArtifact{}, ErrLLMResultVerification
		}
		round, err := verifier.cognition.Execute(ctx, cognition.RoundRequest{
			TenantID: string(request.Rules.Query.Scope.TenantID), ActorID: string(request.Rules.Query.Scope.ActorID),
			Stage: cognition.StageAnomalyAnalysis, PromptVersion: verifier.options.PromptVersion,
			ResourceType: resourceType, ResourceID: resourceID, PreferredModel: verifier.options.PreferredModel,
			Messages: messages, SeenActionHashes: request.SeenActionHashes,
			MaxOutputTokens: verifier.options.MaxOutputTokens,
		})
		if err != nil || round.Action.Validate() != nil ||
			round.Action.Action != cognition.ActionAnalyzeAnomaly || round.Action.AnomalyAnalysis == nil ||
			!actionEvidenceKnown(round.Action, known) {
			return VerificationArtifact{}, ErrLLMResultVerification
		}
		copy := *round.Action.AnomalyAnalysis
		copy.EvidenceRefs = append([]askdata.EvidenceRef(nil), copy.EvidenceRefs...)
		anomaly = &copy
		allEvidence = append(allEvidence, actionEvidenceValues(round.Action)...)
	}

	facts, known, err := verificationFacts(request, summary, anomaly)
	if err != nil {
		return VerificationArtifact{}, err
	}
	messages, err := cognition.BuildMessages(cognition.PromptInput{
		Stage: cognition.StageResultVerification, Facts: facts,
	})
	if err != nil {
		return VerificationArtifact{}, ErrLLMResultVerification
	}
	round, err := verifier.cognition.Execute(ctx, cognition.RoundRequest{
		TenantID: string(request.Rules.Query.Scope.TenantID), ActorID: string(request.Rules.Query.Scope.ActorID),
		Stage: cognition.StageResultVerification, PromptVersion: verifier.options.PromptVersion,
		ResourceType: resourceType, ResourceID: resourceID, PreferredModel: verifier.options.PreferredModel,
		Messages: messages, SeenActionHashes: request.SeenActionHashes,
		MaxOutputTokens: verifier.options.MaxOutputTokens,
	})
	if err != nil || round.Action.Validate() != nil ||
		round.Action.Action != cognition.ActionVerifyResult || round.Action.Verification == nil ||
		!actionEvidenceKnown(round.Action, known) || !verificationContractSatisfied(*round.Action.Verification) {
		return VerificationArtifact{}, ErrLLMResultVerification
	}
	llm := cloneCognitionVerification(*round.Action.Verification)
	allEvidence = append(allEvidence, actionEvidenceValues(round.Action)...)
	final, prevented := governedFinalVerdict(rules.artifact, llm)
	artifact := VerificationArtifact{
		Version: VerificationVersion, QueryPlanHash: request.Rules.Query.PlanHash,
		ResultHash: request.Rules.Execution.Artifact.ResultHash, RuleArtifact: rules.artifact,
		Summary: summary, Anomaly: anomaly, LLMVerification: llm, FinalVerdict: final,
		RuleOverridePrevented: prevented, EvidenceRefs: normalizedEvidenceRefs(allEvidence),
	}
	artifact.VerificationHash, err = verificationArtifactHash(artifact)
	if err != nil {
		return VerificationArtifact{}, err
	}
	if err := artifact.Validate(); err != nil {
		return VerificationArtifact{}, err
	}
	return artifact, nil
}

func (artifact VerificationArtifact) Validate() error {
	if artifact.Version != VerificationVersion || artifact.QueryPlanHash.Validate() != nil ||
		artifact.ResultHash.Validate() != nil || artifact.VerificationHash.Validate() != nil ||
		artifact.RuleArtifact.Validate() != nil || artifact.QueryPlanHash != artifact.RuleArtifact.QueryPlanHash ||
		artifact.ResultHash != artifact.RuleArtifact.ResultHash || artifact.LLMVerification.Validate() != nil ||
		!finalVerificationVerdictValid(artifact.FinalVerdict) || len(artifact.EvidenceRefs) < 1 ||
		len(artifact.EvidenceRefs) > 2*cognition.MaxActionEvidence ||
		!sanitizedSummaryValid(artifact.Summary, artifact.RuleArtifact) ||
		!verificationContractSatisfied(artifact.LLMVerification) {
		return ErrInvalidResultEvidence
	}
	for _, evidence := range artifact.EvidenceRefs {
		if evidence.Validate() != nil {
			return ErrInvalidResultEvidence
		}
	}
	if artifact.RuleArtifact.RequiresAnomalyAnalysis != (artifact.Anomaly != nil) ||
		artifact.Anomaly != nil && artifact.Anomaly.Validate() != nil ||
		!artifact.RuleArtifact.Passed && artifact.FinalVerdict == FinalVerificationPass ||
		artifact.RuleOverridePrevented != (!artifact.RuleArtifact.Passed &&
			artifact.LLMVerification.Verdict == cognition.VerificationPass) {
		return ErrInvalidResultEvidence
	}
	contained := make(map[askdata.EvidenceRef]bool, len(artifact.EvidenceRefs))
	for _, evidence := range artifact.EvidenceRefs {
		contained[evidence] = true
	}
	if artifact.Anomaly != nil {
		for _, evidence := range artifact.Anomaly.EvidenceRefs {
			if !contained[evidence] {
				return ErrInvalidResultEvidence
			}
		}
	}
	for _, check := range artifact.LLMVerification.Checks {
		for _, evidence := range check.EvidenceRefs {
			if !contained[evidence] {
				return ErrInvalidResultEvidence
			}
		}
	}
	expected, err := verificationArtifactHash(artifact)
	if err != nil || expected != artifact.VerificationHash {
		return ErrInvalidResultEvidence
	}
	return nil
}

func sanitizedSummaryValid(summary SanitizedResultSummary, rules RuleArtifact) bool {
	if summary.QueryPlanHash != rules.QueryPlanHash || summary.ResultHash != rules.ResultHash ||
		summary.RulePassed != rules.Passed || summary.NoDataConfirmed != rules.NoDataConfirmed ||
		!reflect.DeepEqual(summary.RuleChecks, rules.Checks) || len(summary.Plans) > 2 ||
		math.IsNaN(summary.Trend.MaxRelativeChange) || math.IsInf(summary.Trend.MaxRelativeChange, 0) ||
		summary.Trend.MaxRelativeChange < 0 || summary.Trend.ComparedMetrics < 0 ||
		summary.Trend.BaselineZeroMetrics < 0 || summary.Trend.IncreasedMetrics < 0 ||
		summary.Trend.DecreasedMetrics < 0 || summary.Trend.UnchangedMetrics < 0 {
		return false
	}
	seenRoles := map[compiler.QueryRole]bool{}
	for _, plan := range summary.Plans {
		if (plan.Role != compiler.QueryRoleCurrent && plan.Role != compiler.QueryRoleBaseline) ||
			seenRoles[plan.Role] || plan.RowCount < 0 || len(plan.Columns) < 1 || len(plan.Columns) > 1024 {
			return false
		}
		seenRoles[plan.Role] = true
		for _, column := range plan.Columns {
			if column.Column == "" || column.NullCount < 0 || column.NullCount > plan.RowCount ||
				column.DistinctCount < 0 || column.DistinctCount > plan.RowCount {
				return false
			}
		}
		for _, metric := range plan.Metrics {
			if metric.Column == "" || metric.NonNullCount < 0 || metric.NullCount < 0 ||
				metric.NonNullCount+metric.NullCount != plan.RowCount ||
				!validRationalSummary(metric.Minimum) || !validRationalSummary(metric.Maximum) ||
				!validRationalSummary(metric.Sum) {
				return false
			}
		}
	}
	return true
}

func validRationalSummary(value string) bool {
	if value == "" {
		return true
	}
	_, ok := new(big.Rat).SetString(value)
	return ok
}

func buildSanitizedSummary(
	request ResultRuleRequest,
	rules ruleEvaluation,
	anomalyThreshold float64,
) (SanitizedResultSummary, error) {
	summary := SanitizedResultSummary{
		QueryPlanHash: request.Query.PlanHash, ResultHash: request.Execution.Artifact.ResultHash,
		Plans: []PlanResultSummary{}, RuleChecks: append([]RuleCheck(nil), rules.artifact.Checks...),
		RulePassed: rules.artifact.Passed, NoDataConfirmed: rules.artifact.NoDataConfirmed,
	}
	for _, plan := range request.Query.Plans {
		normalized, exists := rules.normalized[plan.Role]
		if !exists {
			continue
		}
		keys := map[string]bool{}
		for _, key := range plan.Document.OutputGrain.KeyFields {
			keys[key] = true
		}
		fields := map[string]string{}
		for _, field := range plan.Document.Fields {
			fields[field.Code] = field.Role
		}
		planSummary := PlanResultSummary{Role: plan.Role, RowCount: normalized.RowCount}
		for columnIndex, column := range normalized.Columns {
			nulls, distinct := 0, map[string]bool{}
			metric := MetricResultSummary{Column: column.Name}
			var minimum, maximum, sum *big.Rat
			for _, row := range normalized.Rows {
				value := row[columnIndex]
				if value.Null {
					nulls++
					metric.NullCount++
					continue
				}
				distinct[string(value.Type)+":"+value.Value] = true
				if fields[column.Name] == "MEASURE" &&
					(column.Type == evaluation.ScalarInteger || column.Type == evaluation.ScalarDecimal) {
					number := new(big.Rat)
					if _, ok := number.SetString(value.Value); !ok {
						return SanitizedResultSummary{}, ErrInvalidResultEvidence
					}
					metric.NonNullCount++
					if minimum == nil || number.Cmp(minimum) < 0 {
						minimum = new(big.Rat).Set(number)
					}
					if maximum == nil || number.Cmp(maximum) > 0 {
						maximum = new(big.Rat).Set(number)
					}
					if sum == nil {
						sum = new(big.Rat)
					}
					sum.Add(sum, number)
				}
			}
			planSummary.Columns = append(planSummary.Columns, ColumnResultSummary{
				Column: column.Name, Type: column.Type, Key: keys[column.Name],
				NullCount: nulls, DistinctCount: len(distinct),
			})
			if fields[column.Name] == "MEASURE" {
				if minimum != nil {
					metric.Minimum, metric.Maximum, metric.Sum = minimum.RatString(), maximum.RatString(), sum.RatString()
				}
				planSummary.Metrics = append(planSummary.Metrics, metric)
			}
		}
		summary.Plans = append(summary.Plans, planSummary)
	}
	summary.Trend = summarizeTrend(request, rules.normalized, anomalyThreshold)
	return summary, nil
}

func summarizeTrend(
	request ResultRuleRequest,
	results map[compiler.QueryRole]evaluation.NormalizedResult,
	threshold float64,
) TrendSummary {
	if request.Query.Comparison == nil {
		return TrendSummary{}
	}
	current, currentOK := results[compiler.QueryRoleCurrent]
	baseline, baselineOK := results[compiler.QueryRoleBaseline]
	if !currentOK || !baselineOK || current.RowCount == 0 || baseline.RowCount == 0 {
		return TrendSummary{Available: false, Anomalous: true}
	}
	currentSums := metricSums(current, request.Query.Plans[0])
	baselineSums := metricSums(baseline, request.Query.Plans[1])
	trend := TrendSummary{Available: true}
	for column, currentValue := range currentSums {
		baselineValue, exists := baselineSums[column]
		if !exists {
			continue
		}
		trend.ComparedMetrics++
		comparison := currentValue.Cmp(baselineValue)
		switch {
		case comparison > 0:
			trend.IncreasedMetrics++
		case comparison < 0:
			trend.DecreasedMetrics++
		default:
			trend.UnchangedMetrics++
		}
		if baselineValue.Sign() == 0 {
			if currentValue.Sign() != 0 {
				trend.BaselineZeroMetrics++
				trend.Anomalous = true
			}
			continue
		}
		difference := new(big.Rat).Sub(new(big.Rat).Set(currentValue), baselineValue)
		difference.Abs(difference)
		denominator := new(big.Rat).Abs(new(big.Rat).Set(baselineValue))
		ratio, _ := new(big.Rat).Quo(difference, denominator).Float64()
		if ratio > trend.MaxRelativeChange {
			trend.MaxRelativeChange = ratio
		}
	}
	trend.MaxRelativeChange, _ = strconv.ParseFloat(strconv.FormatFloat(trend.MaxRelativeChange, 'f', 6, 64), 64)
	trend.Anomalous = trend.Anomalous || trend.MaxRelativeChange >= threshold
	return trend
}

func metricSums(result evaluation.NormalizedResult, plan compiler.QueryPlan) map[string]*big.Rat {
	metricFields := map[string]bool{}
	for _, field := range plan.Document.Fields {
		if field.Role == "MEASURE" {
			metricFields[field.Code] = true
		}
	}
	sums := map[string]*big.Rat{}
	for columnIndex, column := range result.Columns {
		if !metricFields[column.Name] ||
			(column.Type != evaluation.ScalarInteger && column.Type != evaluation.ScalarDecimal) {
			continue
		}
		sum := new(big.Rat)
		for _, row := range result.Rows {
			if row[columnIndex].Null {
				continue
			}
			value := new(big.Rat)
			if _, ok := value.SetString(row[columnIndex].Value); ok {
				sum.Add(sum, value)
			}
		}
		sums[column.Name] = sum
	}
	return sums
}

func verificationFacts(
	request VerificationRequest,
	summary SanitizedResultSummary,
	anomaly *cognition.AnomalyAnalysis,
) ([]cognition.PromptFact, map[askdata.ID]askdata.EvidenceRef, error) {
	if request.Conversation.Kind != cognition.FactConversation {
		return nil, nil, ErrInvalidResultEvidence
	}
	planPayload, err := json.Marshal(struct {
		IR            interface{}         `json:"semanticIr"`
		IRHash        askdata.ContentHash `json:"semanticIrHash"`
		QueryPlanHash askdata.ContentHash `json:"queryPlanHash"`
	}{request.Rules.IR, request.Rules.Query.IRHash, request.Rules.Query.PlanHash})
	if err != nil {
		return nil, nil, err
	}
	planFact, err := cognition.NewPromptFact(
		"verification-plan:"+askdata.ID(request.Rules.Query.PlanHash[:16]), cognition.FactPlanEvidence, planPayload,
	)
	if err != nil {
		return nil, nil, err
	}
	resultPayload, err := json.Marshal(struct {
		Summary SanitizedResultSummary     `json:"summary"`
		Anomaly *cognition.AnomalyAnalysis `json:"anomaly,omitempty"`
	}{summary, anomaly})
	if err != nil {
		return nil, nil, err
	}
	resultEvidenceID := "verification-result-initial:"
	if anomaly != nil {
		resultEvidenceID = "verification-result-reviewed:"
	}
	resultFact, err := cognition.NewPromptFact(
		askdata.ID(resultEvidenceID)+askdata.ID(request.Rules.Execution.Artifact.ResultHash[:16]),
		cognition.FactQueryResultSummary, resultPayload,
	)
	if err != nil {
		return nil, nil, err
	}
	qualityPayload, err := registry.CanonicalValue(request.Rules.Evidence)
	if err != nil {
		return nil, nil, err
	}
	qualityFact, err := cognition.NewPromptFact(
		"verification-quality:"+askdata.ID(askdata.HashBytes(qualityPayload)[:16]),
		cognition.FactQualityEvidence, qualityPayload,
	)
	if err != nil {
		return nil, nil, err
	}
	policyPayload, err := json.Marshal(struct {
		DomainID   askdata.ID          `json:"domainId"`
		PolicyHash askdata.ContentHash `json:"policyHash"`
		Release    askdata.ReleaseRef  `json:"release"`
	}{request.Rules.Query.DomainID, request.Rules.Query.Scope.PolicyHash, request.Rules.Query.Scope.Release})
	if err != nil {
		return nil, nil, err
	}
	policyFact, err := cognition.NewPromptFact(
		"verification-policy:"+askdata.ID(request.Rules.Query.Scope.PolicyHash[:16]),
		cognition.FactPolicyEvidence, policyPayload,
	)
	if err != nil {
		return nil, nil, err
	}
	facts := []cognition.PromptFact{request.Conversation, planFact, resultFact, qualityFact, policyFact}
	known := map[askdata.ID]askdata.EvidenceRef{}
	for _, fact := range facts {
		kind := askdata.EvidenceKindRule
		source := askdata.ID(request.Rules.Query.PlanHash)
		switch fact.Kind {
		case cognition.FactConversation:
			kind, source = askdata.EvidenceKindConversation, request.Rules.Query.Scope.ActorID
		case cognition.FactPlanEvidence:
			kind = askdata.EvidenceKindQueryPlan
		case cognition.FactQueryResultSummary:
			kind, source = askdata.EvidenceKindQueryResult, askdata.ID(request.Rules.Execution.Artifact.RunID)
		case cognition.FactQualityEvidence:
			kind = askdata.EvidenceKindDataQuality
		case cognition.FactPolicyEvidence:
			kind, source = askdata.EvidenceKindPolicy, askdata.ID(request.Rules.Query.Scope.PolicyHash)
		}
		known[fact.EvidenceID] = askdata.EvidenceRef{
			EvidenceID: fact.EvidenceID, Kind: kind, SourceID: source, ContentHash: fact.ContentHash,
		}
	}
	return facts, known, nil
}

func actionEvidenceKnown(action cognition.Action, known map[askdata.ID]askdata.EvidenceRef) bool {
	values := actionEvidenceValues(action)
	for _, evidence := range values {
		expected, exists := known[evidence.EvidenceID]
		if !exists || expected != evidence {
			return false
		}
	}
	return true
}

func actionEvidenceValues(action cognition.Action) []askdata.EvidenceRef {
	values := append([]askdata.EvidenceRef(nil), action.EvidenceRefs...)
	if action.AnomalyAnalysis != nil {
		values = append(values, action.AnomalyAnalysis.EvidenceRefs...)
	}
	if action.Verification != nil {
		for _, check := range action.Verification.Checks {
			values = append(values, check.EvidenceRefs...)
		}
	}
	return values
}

func verificationContractSatisfied(verification cognition.Verification) bool {
	answersQuestion := false
	allPassed := true
	for _, check := range verification.Checks {
		if check.Code == "RESULT_ANSWERS_QUESTION" {
			answersQuestion = true
		}
		if !check.Passed {
			allPassed = false
		}
	}
	return answersQuestion && (verification.Verdict != cognition.VerificationPass || allPassed)
}

func cloneCognitionVerification(value cognition.Verification) cognition.Verification {
	copy := value
	copy.Checks = make([]cognition.VerificationCheck, len(value.Checks))
	for index, check := range value.Checks {
		copy.Checks[index] = check
		copy.Checks[index].EvidenceRefs = append([]askdata.EvidenceRef(nil), check.EvidenceRefs...)
	}
	return copy
}

func governedFinalVerdict(rule RuleArtifact, llm cognition.Verification) (FinalVerificationVerdict, bool) {
	if !rule.Passed {
		verdict := FinalVerificationRetry
		for _, check := range rule.Checks {
			if check.Severity == RuleBlocking && !check.Passed &&
				(check.Code == "DATA_FRESHNESS" || check.Code == "QUALITY_STATUS" ||
					strings.HasPrefix(check.Code, "QUALITY_RULE_")) {
				verdict = FinalVerificationBlock
			}
		}
		return verdict, llm.Verdict == cognition.VerificationPass
	}
	switch llm.Verdict {
	case cognition.VerificationPass:
		return FinalVerificationPass, false
	case cognition.VerificationRetry:
		return FinalVerificationRetry, false
	case cognition.VerificationClarify:
		return FinalVerificationClarify, false
	default:
		return FinalVerificationBlock, false
	}
}

func finalVerificationVerdictValid(value FinalVerificationVerdict) bool {
	return value == FinalVerificationPass || value == FinalVerificationRetry ||
		value == FinalVerificationClarify || value == FinalVerificationBlock
}

func verificationArtifactHash(artifact VerificationArtifact) (askdata.ContentHash, error) {
	copy := artifact
	copy.VerificationHash = ""
	payload, err := registry.CanonicalValue(copy)
	if err != nil {
		return "", err
	}
	return askdata.HashBytes(payload), nil
}

func sortedEvidence(values []askdata.EvidenceRef) []askdata.EvidenceRef {
	result := append([]askdata.EvidenceRef(nil), values...)
	sort.Slice(result, func(i, j int) bool { return result[i].EvidenceID < result[j].EvidenceID })
	return result
}
