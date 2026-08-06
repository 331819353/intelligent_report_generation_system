package validator

import (
	"encoding/json"
	"errors"
	"reflect"
	"sort"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/compiler"
	"intelligent-report-generation-system/internal/askdata/evaluation"
	"intelligent-report-generation-system/internal/askdata/ir"
	"intelligent-report-generation-system/internal/askdata/registry"
	"intelligent-report-generation-system/internal/dataset"
)

const ResultRuleVersion = "semantic-result-rules-v1"

var (
	ErrInvalidResultEvidence = errors.New("semantic result evidence is invalid")
	ErrResultRuleFailure     = errors.New("semantic result failed deterministic rules")
)

type RuleSeverity string

const (
	RuleInfo     RuleSeverity = "INFO"
	RuleWarning  RuleSeverity = "WARNING"
	RuleBlocking RuleSeverity = "BLOCKING"
)

type QualityStatus string

const (
	QualityPass    QualityStatus = "PASS"
	QualityWarning QualityStatus = "WARNING"
	QualityFail    QualityStatus = "FAIL"
	QualityUnknown QualityStatus = "UNKNOWN"
)

type FreshnessEvidence struct {
	DataAsOf      string              `json:"dataAsOf"`
	ObservedAt    string              `json:"observedAt"`
	MaxAgeSeconds int64               `json:"maxAgeSeconds"`
	Evidence      askdata.EvidenceRef `json:"evidence"`
}

type CoverageEvidence struct {
	Start        string              `json:"start"`
	EndExclusive string              `json:"endExclusive"`
	Evidence     askdata.EvidenceRef `json:"evidence"`
}

type QualityCheckEvidence struct {
	Code     string              `json:"code"`
	Severity RuleSeverity        `json:"severity"`
	Passed   bool                `json:"passed"`
	Evidence askdata.EvidenceRef `json:"evidence"`
}

type QualityEvidence struct {
	Status   QualityStatus          `json:"status"`
	Evidence askdata.EvidenceRef    `json:"evidence"`
	Checks   []QualityCheckEvidence `json:"checks"`
}

type DivisionEvidence struct {
	ZeroDenominatorCount int                 `json:"zeroDenominatorCount"`
	Evidence             askdata.EvidenceRef `json:"evidence"`
}

type EmptyResultEvidence struct {
	Role             compiler.QueryRole  `json:"role"`
	MembersExist     bool                `json:"membersExist"`
	TimeHasData      bool                `json:"timeHasData"`
	PermissionPruned bool                `json:"permissionPruned"`
	Evidence         askdata.EvidenceRef `json:"evidence"`
}

type ResultEvidence struct {
	Freshness FreshnessEvidence     `json:"freshness"`
	Coverage  *CoverageEvidence     `json:"coverage,omitempty"`
	Quality   QualityEvidence       `json:"quality"`
	Division  *DivisionEvidence     `json:"division,omitempty"`
	Empty     []EmptyResultEvidence `json:"empty,omitempty"`
}

type ResultRuleRequest struct {
	Query     compiler.QueryArtifact
	IR        ir.SemanticIR
	Execution ExecutionResult
	Evidence  ResultEvidence
}

type RuleCheck struct {
	Code         string                `json:"code"`
	Severity     RuleSeverity          `json:"severity"`
	Passed       bool                  `json:"passed"`
	Count        int                   `json:"count"`
	EvidenceRefs []askdata.EvidenceRef `json:"evidenceRefs"`
}

type RuleArtifact struct {
	Version                 string              `json:"version"`
	QueryPlanHash           askdata.ContentHash `json:"queryPlanHash"`
	ResultHash              askdata.ContentHash `json:"resultHash"`
	Checks                  []RuleCheck         `json:"checks"`
	Passed                  bool                `json:"passed"`
	NoDataConfirmed         bool                `json:"noDataConfirmed"`
	RequiresAnomalyAnalysis bool                `json:"requiresAnomalyAnalysis"`
	RuleHash                askdata.ContentHash `json:"ruleHash"`
}

type ruleEvaluation struct {
	artifact   RuleArtifact
	normalized map[compiler.QueryRole]evaluation.NormalizedResult
	resultRef  askdata.EvidenceRef
}

func EvaluateResultRules(request ResultRuleRequest) (RuleArtifact, error) {
	evaluated, err := evaluateResultRules(request)
	if err != nil {
		return RuleArtifact{}, err
	}
	return evaluated.artifact, nil
}

func evaluateResultRules(request ResultRuleRequest) (ruleEvaluation, error) {
	if err := validateResultRuleRequest(request); err != nil {
		return ruleEvaluation{}, err
	}
	resultRef := askdata.EvidenceRef{
		EvidenceID: "query-result:" + askdata.ID(request.Execution.Artifact.ResultHash[:16]),
		Kind:       askdata.EvidenceKindQueryResult, SourceID: askdata.ID(request.Execution.Artifact.RunID),
		ContentHash: request.Execution.Artifact.ResultHash,
	}
	checks := make([]RuleCheck, 0, 16+len(request.Evidence.Quality.Checks))
	add := func(code string, severity RuleSeverity, passed bool, count int, refs ...askdata.EvidenceRef) {
		checks = append(checks, RuleCheck{
			Code: code, Severity: severity, Passed: passed, Count: count,
			EvidenceRefs: normalizedEvidenceRefs(refs),
		})
	}
	add("RESULT_ROW_LIMIT", RuleBlocking, true, request.Execution.Artifact.TotalRows, resultRef)

	normalized := make(map[compiler.QueryRole]evaluation.NormalizedResult, len(request.Query.Plans))
	totalDuplicateRows, totalDuplicateKeys, nullViolations, fanoutViolations := 0, 0, 0, 0
	emptyByRole := make(map[compiler.QueryRole]EmptyResultEvidence, len(request.Evidence.Empty))
	for _, item := range request.Evidence.Empty {
		emptyByRole[item.Role] = item
	}
	currentEmpty, anyEmpty := false, false
	noDataConfirmed := false
	for index, plan := range request.Query.Plans {
		rows, available := request.Execution.Rows(plan.Role)
		if !available {
			return ruleEvaluation{}, ErrInvalidResultEvidence
		}
		columns := make([]string, len(request.Execution.Artifact.Plans[index].Columns))
		for columnIndex, column := range request.Execution.Artifact.Plans[index].Columns {
			columns[columnIndex] = column.Name
		}
		schema, fieldByCode, err := resultSchema(plan, request.Query.Timezone, false)
		if err != nil {
			return ruleEvaluation{}, err
		}
		if !resultColumnTypesMatch(request.Execution.Artifact.Plans[index].Columns, schema.Columns) {
			add("RESULT_SCHEMA_VALID", RuleBlocking, false, 1, resultRef)
			artifact := finalizeRuleArtifact(request, checks, false, true)
			if artifact.Validate() != nil {
				return ruleEvaluation{}, ErrInvalidResultEvidence
			}
			return ruleEvaluation{artifact: artifact, resultRef: resultRef}, nil
		}
		normalizedResult, err := evaluation.NormalizeResult(schema, evaluation.ResultSet{Columns: columns, Rows: rows})
		if err != nil {
			add("RESULT_SCHEMA_VALID", RuleBlocking, false, 1, resultRef)
			artifact := finalizeRuleArtifact(request, checks, false, true)
			if artifact.Validate() != nil {
				return ruleEvaluation{}, ErrInvalidResultEvidence
			}
			return ruleEvaluation{artifact: artifact, resultRef: resultRef}, nil
		}
		normalized[plan.Role] = normalizedResult
		duplicateRows := duplicateNormalizedRows(normalizedResult)
		duplicateKeys, nullKeys := duplicateAndNullKeys(normalizedResult, plan.Document.OutputGrain.KeyFields)
		nullFields := invalidNullFields(normalizedResult, fieldByCode)
		totalDuplicateRows += duplicateRows
		totalDuplicateKeys += duplicateKeys
		nullViolations += nullKeys + nullFields
		if duplicateKeys > 0 || (len(plan.Document.GroupBy) == 0 && normalizedResult.RowCount > 1) {
			fanoutViolations++
		}
		if normalizedResult.RowCount == 0 {
			anyEmpty = true
			if plan.Role == compiler.QueryRoleCurrent {
				currentEmpty = true
			}
			diagnosis, exists := emptyByRole[plan.Role]
			if !exists {
				add("EMPTY_RESULT_"+string(plan.Role), RuleBlocking, false, 1, resultRef)
				continue
			}
			passed := diagnosis.MembersExist && !diagnosis.TimeHasData && !diagnosis.PermissionPruned
			add("EMPTY_RESULT_"+string(plan.Role), RuleBlocking, passed, 1, diagnosis.Evidence, resultRef)
			if plan.Role == compiler.QueryRoleCurrent && passed {
				noDataConfirmed = true
			}
		}
	}
	add("RESULT_SCHEMA_VALID", RuleBlocking, true, 0, resultRef)
	add("RESULT_ROWS_UNIQUE", RuleBlocking, totalDuplicateRows == 0, totalDuplicateRows, resultRef)
	add("RESULT_KEYS_UNIQUE", RuleBlocking, totalDuplicateKeys == 0, totalDuplicateKeys, resultRef)
	add("RESULT_NULL_POLICY", RuleBlocking, nullViolations == 0, nullViolations, resultRef)
	add("RESULT_FANOUT", RuleBlocking, fanoutViolations == 0, fanoutViolations, resultRef)

	divisionRequired := queryUsesDivision(request.Query)
	divisionPassed, divisionCount := !divisionRequired, 0
	divisionRefs := []askdata.EvidenceRef{resultRef}
	if request.Evidence.Division != nil {
		divisionCount = request.Evidence.Division.ZeroDenominatorCount
		divisionPassed = divisionCount == 0
		divisionRefs = append(divisionRefs, request.Evidence.Division.Evidence)
	}
	add("DIVISION_BY_ZERO", RuleBlocking, divisionPassed, divisionCount, divisionRefs...)

	freshnessPassed := freshnessIsCurrent(request.Evidence.Freshness)
	add("DATA_FRESHNESS", RuleBlocking, freshnessPassed, boolCount(!freshnessPassed),
		request.Evidence.Freshness.Evidence)
	coveragePassed := true
	coverageRefs := []askdata.EvidenceRef{resultRef}
	if request.IR.TimeRange != nil {
		coveragePassed = request.Evidence.Coverage != nil &&
			coverageContains(*request.Evidence.Coverage, *request.IR.TimeRange)
		if request.Evidence.Coverage != nil {
			coverageRefs = append(coverageRefs, request.Evidence.Coverage.Evidence)
		}
	}
	add("TIME_COVERAGE", RuleBlocking, coveragePassed, boolCount(!coveragePassed), coverageRefs...)

	qualityPassed := request.Evidence.Quality.Status == QualityPass ||
		request.Evidence.Quality.Status == QualityWarning
	blockingQualityFailures := 0
	qualityChecks := append([]QualityCheckEvidence(nil), request.Evidence.Quality.Checks...)
	sort.Slice(qualityChecks, func(i, j int) bool { return qualityChecks[i].Code < qualityChecks[j].Code })
	for _, quality := range qualityChecks {
		severity := quality.Severity
		if severity == RuleBlocking && !quality.Passed {
			blockingQualityFailures++
			qualityPassed = false
		}
		add("QUALITY_RULE_"+quality.Code, severity, quality.Passed, boolCount(!quality.Passed), quality.Evidence)
	}
	if request.Evidence.Quality.Status == QualityFail || request.Evidence.Quality.Status == QualityUnknown {
		qualityPassed = false
	}
	add("QUALITY_STATUS", RuleBlocking, qualityPassed, blockingQualityFailures,
		request.Evidence.Quality.Evidence)

	comparisonShape := len(request.Query.Plans) == 1 && request.Query.Comparison == nil ||
		len(request.Query.Plans) == 2 && request.Query.Comparison != nil &&
			resultSchemasEqual(request.Execution.Artifact.Plans[0], request.Execution.Artifact.Plans[1])
	add("COMPARISON_SHAPE", RuleBlocking, comparisonShape, boolCount(!comparisonShape), resultRef)

	requiresAnomaly := anyEmpty || fanoutViolations > 0 || totalDuplicateKeys > 0 ||
		!freshnessPassed || !coveragePassed || !qualityPassed || comparisonHasAnomalousTrend(request, normalized)
	if currentEmpty && !noDataConfirmed {
		requiresAnomaly = true
	}
	artifact := finalizeRuleArtifact(request, checks, noDataConfirmed, requiresAnomaly)
	if err := artifact.Validate(); err != nil {
		return ruleEvaluation{}, err
	}
	return ruleEvaluation{artifact: artifact, normalized: normalized, resultRef: resultRef}, nil
}

func validateResultRuleRequest(request ResultRuleRequest) error {
	_, _, irHash, err := ir.Canonicalize(request.IR)
	if err != nil || irHash != request.Query.IRHash || request.Query.Validate() != nil ||
		request.Execution.Artifact.Validate() != nil ||
		request.Execution.Artifact.QueryArtifactPlanHash != request.Query.PlanHash ||
		request.Execution.Artifact.ValidationHash.Validate() != nil ||
		request.Execution.Artifact.Scope.PolicyHash != request.Query.Scope.PolicyHash ||
		request.Execution.Artifact.DomainID != request.Query.DomainID ||
		len(request.Execution.Artifact.Plans) != len(request.Query.Plans) {
		return ErrInvalidResultEvidence
	}
	for index, plan := range request.Query.Plans {
		executed := request.Execution.Artifact.Plans[index]
		if executed.Role != plan.Role || executed.QueryPlanHash != plan.PlanHash ||
			executed.CompiledPlanHash != plan.CompiledPlanHash {
			return ErrInvalidResultEvidence
		}
	}
	if err := validateResultEvidence(request); err != nil {
		return err
	}
	return nil
}

func validateResultEvidence(request ResultRuleRequest) error {
	if !qualityStatusValid(request.Evidence.Quality.Status) ||
		request.Evidence.Freshness.MaxAgeSeconds < 1 || request.Evidence.Freshness.MaxAgeSeconds > 366*24*3600 ||
		request.Evidence.Freshness.Evidence.Validate() != nil ||
		request.Evidence.Quality.Evidence.Validate() != nil {
		return ErrInvalidResultEvidence
	}
	if _, err := time.Parse(time.RFC3339Nano, request.Evidence.Freshness.DataAsOf); err != nil {
		return ErrInvalidResultEvidence
	}
	if _, err := time.Parse(time.RFC3339Nano, request.Evidence.Freshness.ObservedAt); err != nil {
		return ErrInvalidResultEvidence
	}
	seenQuality := map[string]bool{}
	for _, check := range request.Evidence.Quality.Checks {
		if !stableRuleCode(check.Code) || !ruleSeverityValid(check.Severity) ||
			check.Evidence.Validate() != nil || seenQuality[check.Code] {
			return ErrInvalidResultEvidence
		}
		seenQuality[check.Code] = true
	}
	if request.IR.TimeRange != nil {
		if request.Evidence.Coverage == nil || request.Evidence.Coverage.Evidence.Validate() != nil ||
			request.Evidence.Coverage.Start == "" || request.Evidence.Coverage.EndExclusive == "" {
			return ErrInvalidResultEvidence
		}
		location, err := time.LoadLocation(request.IR.TimeRange.Timezone)
		if err != nil {
			return ErrInvalidResultEvidence
		}
		start, startErr := parseCoverageBoundary(request.Evidence.Coverage.Start, location)
		end, endErr := parseCoverageBoundary(request.Evidence.Coverage.EndExclusive, location)
		if startErr != nil || endErr != nil || !start.Before(end) {
			return ErrInvalidResultEvidence
		}
	}
	if queryUsesDivision(request.Query) {
		if request.Evidence.Division == nil || request.Evidence.Division.ZeroDenominatorCount < 0 ||
			request.Evidence.Division.Evidence.Validate() != nil {
			return ErrInvalidResultEvidence
		}
	} else if request.Evidence.Division != nil && (request.Evidence.Division.ZeroDenominatorCount < 0 ||
		request.Evidence.Division.Evidence.Validate() != nil) {
		return ErrInvalidResultEvidence
	}
	seenEmpty := map[compiler.QueryRole]bool{}
	for _, empty := range request.Evidence.Empty {
		if (empty.Role != compiler.QueryRoleCurrent && empty.Role != compiler.QueryRoleBaseline) ||
			empty.Evidence.Validate() != nil || seenEmpty[empty.Role] {
			return ErrInvalidResultEvidence
		}
		seenEmpty[empty.Role] = true
	}
	if !resultEvidenceRefsConsistent(request.Evidence) {
		return ErrInvalidResultEvidence
	}
	return nil
}

func resultEvidenceRefsConsistent(evidence ResultEvidence) bool {
	values := []askdata.EvidenceRef{evidence.Freshness.Evidence, evidence.Quality.Evidence}
	if evidence.Coverage != nil {
		values = append(values, evidence.Coverage.Evidence)
	}
	if evidence.Division != nil {
		values = append(values, evidence.Division.Evidence)
	}
	for _, check := range evidence.Quality.Checks {
		values = append(values, check.Evidence)
	}
	for _, empty := range evidence.Empty {
		values = append(values, empty.Evidence)
	}
	seen := make(map[askdata.ID]askdata.EvidenceRef, len(values))
	for _, value := range values {
		previous, exists := seen[value.EvidenceID]
		if exists && previous != value {
			return false
		}
		seen[value.EvidenceID] = value
	}
	return true
}

func resultSchema(
	plan compiler.QueryPlan,
	timezone string,
	withKeys bool,
) (evaluation.ResultSchema, map[string]struct {
	nullable bool
	key      bool
}, error) {
	if timezone == "" {
		timezone = "UTC"
	}
	keys := make(map[string]bool, len(plan.Document.OutputGrain.KeyFields))
	for _, key := range plan.Document.OutputGrain.KeyFields {
		keys[key] = true
	}
	fields := make(map[string]struct {
		nullable bool
		key      bool
	}, len(plan.Document.Fields))
	columns := make([]evaluation.Column, 0, len(plan.Document.Fields))
	for _, field := range plan.Document.Fields {
		scalar, err := resultScalarType(field.CanonicalType)
		if err != nil {
			return evaluation.ResultSchema{}, nil, err
		}
		column := evaluation.Column{Name: field.Code, Type: scalar, Key: withKeys && keys[field.Code]}
		if scalar == evaluation.ScalarDateTime {
			column.Timezone = timezone
		}
		columns = append(columns, column)
		fields[field.Code] = struct {
			nullable bool
			key      bool
		}{field.Nullable, keys[field.Code]}
	}
	return evaluation.ResultSchema{Columns: columns}, fields, nil
}

func resultScalarType(canonical string) (evaluation.ScalarType, error) {
	switch canonical {
	case "STRING":
		return evaluation.ScalarString, nil
	case "INTEGER":
		return evaluation.ScalarInteger, nil
	case "DECIMAL":
		return evaluation.ScalarDecimal, nil
	case "BOOLEAN":
		return evaluation.ScalarBoolean, nil
	case "DATE":
		return evaluation.ScalarDate, nil
	case "DATETIME":
		return evaluation.ScalarDateTime, nil
	default:
		return "", ErrInvalidResultEvidence
	}
}

func resultColumnTypesMatch(columns []ResultColumn, schema []evaluation.Column) bool {
	if len(columns) != len(schema) {
		return false
	}
	byName := make(map[string]evaluation.ScalarType, len(schema))
	for _, column := range schema {
		byName[column.Name] = column.Type
	}
	for _, column := range columns {
		scalar, exists := byName[column.Name]
		if !exists || !resultOIDMatchesScalar(column.DataTypeOID, scalar) {
			return false
		}
	}
	return true
}

func resultOIDMatchesScalar(oid uint32, scalar evaluation.ScalarType) bool {
	switch scalar {
	case evaluation.ScalarString:
		return oid == pgtype.TextOID || oid == pgtype.VarcharOID || oid == pgtype.BPCharOID || oid == pgtype.UUIDOID
	case evaluation.ScalarInteger:
		return oid == pgtype.Int2OID || oid == pgtype.Int4OID || oid == pgtype.Int8OID
	case evaluation.ScalarDecimal:
		return oid == pgtype.NumericOID
	case evaluation.ScalarBoolean:
		return oid == pgtype.BoolOID
	case evaluation.ScalarDate:
		return oid == pgtype.DateOID
	case evaluation.ScalarDateTime:
		return oid == pgtype.TimestampOID || oid == pgtype.TimestamptzOID
	default:
		return false
	}
}

func duplicateNormalizedRows(result evaluation.NormalizedResult) int {
	seen := make(map[string]bool, len(result.Rows))
	duplicates := 0
	for _, row := range result.Rows {
		payload, _ := json.Marshal(row)
		key := string(payload)
		if seen[key] {
			duplicates++
		}
		seen[key] = true
	}
	return duplicates
}

func duplicateAndNullKeys(result evaluation.NormalizedResult, keyNames []string) (int, int) {
	indexes := make([]int, 0, len(keyNames))
	keySet := make(map[string]bool, len(keyNames))
	for _, key := range keyNames {
		keySet[key] = true
	}
	for index, column := range result.Columns {
		if keySet[column.Name] {
			indexes = append(indexes, index)
		}
	}
	if len(indexes) != len(keySet) {
		return len(result.Rows), len(result.Rows)
	}
	seen := make(map[string]bool, len(result.Rows))
	duplicates, nulls := 0, 0
	for _, row := range result.Rows {
		values := make([]evaluation.NormalizedValue, len(indexes))
		for valueIndex, columnIndex := range indexes {
			values[valueIndex] = row[columnIndex]
			if row[columnIndex].Null {
				nulls++
			}
		}
		payload, _ := json.Marshal(values)
		key := string(payload)
		if seen[key] {
			duplicates++
		}
		seen[key] = true
	}
	return duplicates, nulls
}

func invalidNullFields(
	result evaluation.NormalizedResult,
	fields map[string]struct {
		nullable bool
		key      bool
	},
) int {
	violations := 0
	for columnIndex, column := range result.Columns {
		contract := fields[column.Name]
		for _, row := range result.Rows {
			if row[columnIndex].Null && (!contract.nullable || contract.key) {
				violations++
			}
		}
	}
	return violations
}

func queryUsesDivision(artifact compiler.QueryArtifact) bool {
	for _, plan := range artifact.Plans {
		for _, field := range plan.Document.Fields {
			if expressionContainsType(field.Expression, "DIVIDE", 0) {
				return true
			}
		}
	}
	return false
}

func expressionContainsType(expression dataset.Expression, target string, depth int) bool {
	if depth > 64 {
		return false
	}
	if expression.Type == target {
		return true
	}
	children := make([]dataset.Expression, 0, len(expression.Arguments)+len(expression.PartitionBy)+8)
	for _, child := range []*dataset.Expression{
		expression.Argument, expression.Left, expression.Right, expression.Lower, expression.Upper, expression.Else,
	} {
		if child != nil {
			children = append(children, *child)
		}
	}
	children = append(children, expression.Arguments...)
	children = append(children, expression.PartitionBy...)
	for _, branch := range expression.Whens {
		children = append(children, branch.When, branch.Then)
	}
	for _, order := range expression.OrderBy {
		children = append(children, order.Expression)
	}
	for _, child := range children {
		if expressionContainsType(child, target, depth+1) {
			return true
		}
	}
	return false
}

func freshnessIsCurrent(evidence FreshnessEvidence) bool {
	dataAsOf, firstErr := time.Parse(time.RFC3339Nano, evidence.DataAsOf)
	observedAt, secondErr := time.Parse(time.RFC3339Nano, evidence.ObservedAt)
	return firstErr == nil && secondErr == nil && !dataAsOf.After(observedAt) &&
		observedAt.Sub(dataAsOf) <= time.Duration(evidence.MaxAgeSeconds)*time.Second
}

func coverageContains(evidence CoverageEvidence, requested ir.TimeRange) bool {
	location, err := time.LoadLocation(requested.Timezone)
	if err != nil {
		return false
	}
	evidenceStart, err := parseCoverageBoundary(evidence.Start, location)
	if err != nil {
		return false
	}
	evidenceEnd, err := parseCoverageBoundary(evidence.EndExclusive, location)
	if err != nil {
		return false
	}
	requestedStart, err := parseCoverageBoundary(requested.Start, location)
	if err != nil {
		return false
	}
	requestedEnd, err := parseCoverageBoundary(requested.EndExclusive, location)
	if err != nil {
		return false
	}
	return evidenceStart.Before(evidenceEnd) && !evidenceStart.After(requestedStart) && !evidenceEnd.Before(requestedEnd)
}

func parseCoverageBoundary(value string, location *time.Location) (time.Time, error) {
	if parsed, err := time.ParseInLocation("2006-01-02", value, location); err == nil {
		return parsed, nil
	}
	if parsed, err := time.Parse(time.RFC3339Nano, value); err == nil {
		return parsed.In(location), nil
	}
	return time.Time{}, ErrInvalidResultEvidence
}

func resultSchemasEqual(left, right PlanExecution) bool {
	return reflect.DeepEqual(left.Columns, right.Columns)
}

func finalizeRuleArtifact(
	request ResultRuleRequest,
	checks []RuleCheck,
	noDataConfirmed bool,
	requiresAnomaly bool,
) RuleArtifact {
	passed := true
	for _, check := range checks {
		if check.Severity == RuleBlocking && !check.Passed {
			passed = false
		}
	}
	artifact := RuleArtifact{
		Version: ResultRuleVersion, QueryPlanHash: request.Query.PlanHash,
		ResultHash: request.Execution.Artifact.ResultHash, Checks: checks, Passed: passed,
		NoDataConfirmed: noDataConfirmed, RequiresAnomalyAnalysis: requiresAnomaly,
	}
	artifact.RuleHash, _ = ruleArtifactHash(artifact)
	return artifact
}

func (artifact RuleArtifact) Validate() error {
	if artifact.Version != ResultRuleVersion || artifact.QueryPlanHash.Validate() != nil ||
		artifact.ResultHash.Validate() != nil || artifact.RuleHash.Validate() != nil ||
		len(artifact.Checks) < 1 || len(artifact.Checks) > 64 {
		return ErrInvalidResultEvidence
	}
	passed := true
	seen := map[string]bool{}
	for _, check := range artifact.Checks {
		if !stableRuleCode(check.Code) || !ruleSeverityValid(check.Severity) || check.Count < 0 ||
			len(check.EvidenceRefs) < 1 || len(check.EvidenceRefs) > 16 || seen[check.Code] {
			return ErrInvalidResultEvidence
		}
		seen[check.Code] = true
		for _, evidence := range check.EvidenceRefs {
			if evidence.Validate() != nil {
				return ErrInvalidResultEvidence
			}
		}
		if check.Severity == RuleBlocking && !check.Passed {
			passed = false
		}
	}
	if artifact.Passed != passed || artifact.NoDataConfirmed && !artifact.RequiresAnomalyAnalysis {
		return ErrInvalidResultEvidence
	}
	expected, err := ruleArtifactHash(artifact)
	if err != nil || expected != artifact.RuleHash {
		return ErrInvalidResultEvidence
	}
	return nil
}

func ruleArtifactHash(artifact RuleArtifact) (askdata.ContentHash, error) {
	copy := artifact
	copy.RuleHash = ""
	payload, err := registry.CanonicalValue(copy)
	if err != nil {
		return "", err
	}
	return askdata.HashBytes(payload), nil
}

func normalizedEvidenceRefs(values []askdata.EvidenceRef) []askdata.EvidenceRef {
	result := append([]askdata.EvidenceRef(nil), values...)
	sort.Slice(result, func(i, j int) bool { return result[i].EvidenceID < result[j].EvidenceID })
	compact := result[:0]
	for _, value := range result {
		if len(compact) == 0 || compact[len(compact)-1].EvidenceID != value.EvidenceID {
			compact = append(compact, value)
		}
	}
	return compact
}

func stableRuleCode(value string) bool {
	if value == "" || len(value) > 128 || value[0] < 'A' || value[0] > 'Z' {
		return false
	}
	for _, character := range value {
		if (character < 'A' || character > 'Z') && (character < '0' || character > '9') && character != '_' {
			return false
		}
	}
	return true
}

func ruleSeverityValid(value RuleSeverity) bool {
	return value == RuleInfo || value == RuleWarning || value == RuleBlocking
}

func qualityStatusValid(value QualityStatus) bool {
	return value == QualityPass || value == QualityWarning || value == QualityFail || value == QualityUnknown
}

func boolCount(value bool) int {
	if value {
		return 1
	}
	return 0
}

func comparisonHasAnomalousTrend(
	request ResultRuleRequest,
	normalized map[compiler.QueryRole]evaluation.NormalizedResult,
) bool {
	if request.Query.Comparison == nil {
		return false
	}
	current := normalized[compiler.QueryRoleCurrent]
	baseline := normalized[compiler.QueryRoleBaseline]
	if current.RowCount == 0 || baseline.RowCount == 0 {
		return true
	}
	return false
}
