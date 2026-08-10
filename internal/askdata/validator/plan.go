// Package validator applies the final deterministic safety gates to AskData
// query plans before any warehouse statement may execute.
package validator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strings"
	"unicode"
	"unicode/utf8"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/compiler"
	"intelligent-report-generation-system/internal/askdata/registry"
)

const ValidationVersion = "semantic-query-validation-v1"

var (
	ErrInvalidValidator           = errors.New("semantic query validator is invalid")
	ErrPlanNotExecutable          = errors.New("semantic query plan is not executable")
	ErrPlanRejected               = errors.New("semantic query plan was rejected")
	ErrExplainUnavailable         = errors.New("semantic query EXPLAIN is unavailable")
	ErrCoverageValidationRequired = errors.New("time coverage validation is required")
)

const (
	CodeStatementNotSelect     = "STATEMENT_NOT_SELECT"
	CodeForbiddenSQLToken      = "FORBIDDEN_SQL_TOKEN"
	CodeUntrustedRelation      = "UNTRUSTED_RELATION"
	CodeUnsupportedFunction    = "UNSUPPORTED_FUNCTION"
	CodeRowLimitExceeded       = "ROW_LIMIT_EXCEEDED"
	CodeExplainInvalid         = "EXPLAIN_INVALID"
	CodePlanCostExceeded       = "PLAN_COST_EXCEEDED"
	CodeRootRowsExceeded       = "ROOT_ROWS_EXCEEDED"
	CodePlanNodesExceeded      = "PLAN_NODES_EXCEEDED"
	CodePlanNodeRowsExceeded   = "PLAN_NODE_ROWS_EXCEEDED"
	CodeSequentialScanExceeded = "SEQUENTIAL_SCAN_EXCEEDED"
	CodeJoinRowsExceeded       = "JOIN_ROWS_EXCEEDED"
	CodeJoinFanoutExceeded     = "JOIN_FANOUT_EXCEEDED"
)

// Rejection exposes only a stable code. SQL, parameters and raw EXPLAIN JSON
// must not enter error strings, logs or ordinary audit records.
type Rejection struct{ Code string }

func (rejection *Rejection) Error() string { return ErrPlanRejected.Error() + ": " + rejection.Code }
func (rejection *Rejection) Unwrap() error { return ErrPlanRejected }

func reject(code string) error { return &Rejection{Code: code} }

// Limits is configuration, not an LLM/user input. Every field is bounded so a
// caller can only tighten the platform ceiling, never disable a gate.
type Limits struct {
	StatementTimeoutMS    int     `json:"statementTimeoutMs"`
	LockTimeoutMS         int     `json:"lockTimeoutMs"`
	MaxRows               int     `json:"maxRows"`
	MaxTotalCost          float64 `json:"maxTotalCost"`
	MaxPlanNodes          int     `json:"maxPlanNodes"`
	MaxNodeRows           int64   `json:"maxNodeRows"`
	MaxSequentialScanRows int64   `json:"maxSequentialScanRows"`
	MaxJoinRows           int64   `json:"maxJoinRows"`
	MaxJoinFanout         float64 `json:"maxJoinFanout"`
	MaxExplainBytes       int     `json:"maxExplainBytes"`
	MaxPlanDepth          int     `json:"maxPlanDepth"`
}

func DefaultLimits() Limits {
	return Limits{
		StatementTimeoutMS: 25000, LockTimeoutMS: 1000, MaxRows: 10000,
		MaxTotalCost: 10_000_000, MaxPlanNodes: 512, MaxNodeRows: 10_000_000,
		MaxSequentialScanRows: 1_000_000, MaxJoinRows: 1_000_000, MaxJoinFanout: 10,
		MaxExplainBytes: 2 << 20, MaxPlanDepth: 64,
	}
}

func (limits Limits) Validate() error {
	if limits.StatementTimeoutMS < 100 || limits.StatementTimeoutMS > 25000 ||
		limits.LockTimeoutMS < 1 || limits.LockTimeoutMS > 5000 ||
		limits.LockTimeoutMS > limits.StatementTimeoutMS || limits.MaxRows < 1 || limits.MaxRows > 10000 ||
		math.IsNaN(limits.MaxTotalCost) || math.IsInf(limits.MaxTotalCost, 0) ||
		limits.MaxTotalCost <= 0 || limits.MaxTotalCost > 1_000_000_000 ||
		limits.MaxPlanNodes < 1 || limits.MaxPlanNodes > 4096 ||
		limits.MaxNodeRows < 1 || limits.MaxNodeRows > 1_000_000_000 ||
		limits.MaxSequentialScanRows < 1 || limits.MaxSequentialScanRows > limits.MaxNodeRows ||
		limits.MaxJoinRows < 1 || limits.MaxJoinRows > limits.MaxNodeRows ||
		math.IsNaN(limits.MaxJoinFanout) || math.IsInf(limits.MaxJoinFanout, 0) ||
		limits.MaxJoinFanout < 1 || limits.MaxJoinFanout > 1000 ||
		limits.MaxExplainBytes < 1024 || limits.MaxExplainBytes > 16<<20 ||
		limits.MaxPlanDepth < 1 || limits.MaxPlanDepth > 128 {
		return ErrInvalidValidator
	}
	return nil
}

type ExplainRequest struct {
	Scope              askdata.PolicyScope     `json:"-"`
	DomainID           askdata.ID              `json:"-"`
	QueryPlanHash      askdata.ContentHash     `json:"-"`
	CompiledPlanHash   askdata.ContentHash     `json:"-"`
	Source             compiler.PhysicalSource `json:"-"`
	Timezone           string                  `json:"-"`
	StatementTimeoutMS int                     `json:"-"`
	LockTimeoutMS      int                     `json:"-"`
	MaxExplainBytes    int                     `json:"-"`
	SQL                string                  `json:"-"`
	Args               []any                   `json:"-"`
}

type Explainer interface {
	Explain(context.Context, ExplainRequest) (json.RawMessage, error)
}

type PlanValidation struct {
	Role             compiler.QueryRole  `json:"role"`
	QueryPlanHash    askdata.ContentHash `json:"queryPlanHash"`
	CompiledPlanHash askdata.ContentHash `json:"compiledPlanHash"`
	MaxRows          int                 `json:"maxRows"`
	Explain          ExplainSummary      `json:"explain"`
}

type ValidationArtifact struct {
	Version               string              `json:"version"`
	Scope                 askdata.PolicyScope `json:"scope"`
	DomainID              askdata.ID          `json:"domainId"`
	QueryArtifactPlanHash askdata.ContentHash `json:"queryArtifactPlanHash"`
	Limits                Limits              `json:"limits"`
	Plans                 []PlanValidation    `json:"plans"`
	ValidationHash        askdata.ContentHash `json:"validationHash"`
}

type Validator struct {
	explainer Explainer
	limits    Limits
}

func NewValidator(explainer Explainer, limits Limits) (*Validator, error) {
	if explainer == nil || limits.Validate() != nil {
		return nil, ErrInvalidValidator
	}
	return &Validator{explainer: explainer, limits: limits}, nil
}

// Validate accepts only a live compiler artifact. Serialized artifacts remain
// replay-verifiable but have no Args and must be rehydrated by the compiler
// before this method can call EXPLAIN.
func (validator *Validator) Validate(ctx context.Context, artifact compiler.QueryArtifact) (ValidationArtifact, error) {
	if artifact.ResolvedTimeSpec != nil {
		return ValidationArtifact{}, ErrCoverageValidationRequired
	}
	return validator.validate(ctx, artifact)
}

// ValidateCovered is the mandatory Plan Validation entry point for queries
// with time ranges. NONE short-circuits before artifact inspection or EXPLAIN;
// FULL/TRUNCATED require the exact post-coverage time spec and sources.
func (validator *Validator) ValidateCovered(
	ctx context.Context,
	artifact compiler.QueryArtifact,
	coverage CoverageVerdict,
) (ValidationArtifact, error) {
	if validator == nil || validator.explainer == nil || validator.limits.Validate() != nil {
		return ValidationArtifact{}, ErrInvalidValidator
	}
	if err := ctx.Err(); err != nil {
		return ValidationArtifact{}, err
	}
	if coverage.Validate() != nil {
		return ValidationArtifact{}, ErrInvalidCoverage
	}
	if coverage.Relation == CoverageNone {
		return ValidationArtifact{}, reject(CodeTimeCoverageNone)
	}
	if artifact.ResolvedTimeSpec == nil ||
		!reflect.DeepEqual(*artifact.ResolvedTimeSpec, coverage.ResolvedTimeSpec) ||
		!coverageMatchesArtifact(coverage.MaterializationIDs, artifact.Plans) {
		return ValidationArtifact{}, ErrInvalidCoverage
	}
	return validator.validate(ctx, artifact)
}

func (validator *Validator) validate(ctx context.Context, artifact compiler.QueryArtifact) (ValidationArtifact, error) {
	if validator == nil || validator.explainer == nil || validator.limits.Validate() != nil {
		return ValidationArtifact{}, ErrInvalidValidator
	}
	if err := ctx.Err(); err != nil {
		return ValidationArtifact{}, err
	}
	if err := artifact.Validate(); err != nil {
		return ValidationArtifact{}, fmt.Errorf("%w: query artifact", ErrPlanNotExecutable)
	}
	result := ValidationArtifact{
		Version: ValidationVersion, Scope: artifact.Scope, DomainID: artifact.DomainID,
		QueryArtifactPlanHash: artifact.PlanHash, Limits: validator.limits,
		Plans: []PlanValidation{},
	}
	for _, plan := range artifact.Plans {
		compiled, available := plan.CompiledQuery()
		if !available || compiled.PlanHash != string(plan.CompiledPlanHash) || compiled.MaxRows < 1 {
			return ValidationArtifact{}, ErrPlanNotExecutable
		}
		if compiled.MaxRows > validator.limits.MaxRows {
			return ValidationArtifact{}, reject(CodeRowLimitExceeded)
		}
		if err := validateSQL(compiled.SQL, plan.Source); err != nil {
			return ValidationArtifact{}, err
		}
		timeout := validator.limits.StatementTimeoutMS
		if plan.Document.ExecutionPolicy.TimeoutMS < timeout {
			timeout = plan.Document.ExecutionPolicy.TimeoutMS
		}
		timezone := artifact.Timezone
		if timezone == "" {
			timezone = "UTC"
		}
		raw, err := validator.explainer.Explain(ctx, ExplainRequest{
			Scope: artifact.Scope, DomainID: artifact.DomainID,
			QueryPlanHash: plan.PlanHash, CompiledPlanHash: plan.CompiledPlanHash,
			Source: plan.Source, Timezone: timezone,
			StatementTimeoutMS: timeout, LockTimeoutMS: validator.limits.LockTimeoutMS,
			MaxExplainBytes: validator.limits.MaxExplainBytes,
			SQL:             compiled.SQL, Args: append([]any(nil), compiled.Args...),
		})
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return ValidationArtifact{}, err
			}
			return ValidationArtifact{}, ErrExplainUnavailable
		}
		summary, err := analyzeExplain(raw, validator.limits, compiled.MaxRows)
		if err != nil {
			return ValidationArtifact{}, err
		}
		result.Plans = append(result.Plans, PlanValidation{
			Role: plan.Role, QueryPlanHash: plan.PlanHash,
			CompiledPlanHash: plan.CompiledPlanHash, MaxRows: compiled.MaxRows, Explain: summary,
		})
	}
	var err error
	result.ValidationHash, err = validationArtifactHash(result)
	if err != nil {
		return ValidationArtifact{}, err
	}
	if err := result.Validate(); err != nil {
		return ValidationArtifact{}, err
	}
	return result, nil
}

func coverageMatchesArtifact(ids []string, plans []compiler.QueryPlan) bool {
	expected := make(map[string]bool, len(ids))
	for _, id := range ids {
		expected[id] = true
	}
	actual := make(map[string]bool, len(plans))
	for _, plan := range plans {
		id := string(plan.Source.MaterializationID)
		if !expected[id] {
			return false
		}
		actual[id] = true
	}
	return len(actual) == len(expected)
}

func (artifact ValidationArtifact) Validate() error {
	if artifact.Version != ValidationVersion || artifact.Scope.Validate() != nil ||
		artifact.DomainID.Validate() != nil || !containsID(artifact.Scope.DomainIDs, artifact.DomainID) ||
		artifact.QueryArtifactPlanHash.Validate() != nil || artifact.ValidationHash.Validate() != nil ||
		artifact.Limits.Validate() != nil || len(artifact.Plans) < 1 || len(artifact.Plans) > 2 {
		return ErrPlanNotExecutable
	}
	for index, plan := range artifact.Plans {
		if plan.QueryPlanHash.Validate() != nil || plan.CompiledPlanHash.Validate() != nil ||
			plan.MaxRows < 1 || plan.MaxRows > artifact.Limits.MaxRows || plan.Explain.Validate() != nil ||
			plan.Explain.TotalCost > artifact.Limits.MaxTotalCost ||
			plan.Explain.RootPlanRows > int64(plan.MaxRows) ||
			plan.Explain.PlanNodes > artifact.Limits.MaxPlanNodes ||
			plan.Explain.MaxNodeRows > artifact.Limits.MaxNodeRows ||
			plan.Explain.MaxSequentialRows > artifact.Limits.MaxSequentialScanRows ||
			plan.Explain.MaxJoinRows > artifact.Limits.MaxJoinRows ||
			plan.Explain.MaxJoinFanout > artifact.Limits.MaxJoinFanout ||
			(index == 0 && plan.Role != compiler.QueryRoleCurrent) ||
			(index == 1 && plan.Role != compiler.QueryRoleBaseline) {
			return ErrPlanNotExecutable
		}
	}
	expected, err := validationArtifactHash(artifact)
	if err != nil || expected != artifact.ValidationHash {
		return ErrPlanNotExecutable
	}
	return nil
}

func validationArtifactHash(artifact ValidationArtifact) (askdata.ContentHash, error) {
	copy := artifact
	copy.ValidationHash = ""
	payload, err := registry.CanonicalValue(copy)
	if err != nil {
		return "", fmt.Errorf("hash query validation artifact: %w", err)
	}
	return askdata.HashBytes(payload), nil
}

type sqlTokenKind uint8

const (
	sqlWord sqlTokenKind = iota + 1
	sqlQuotedIdentifier
	sqlString
	sqlNumber
	sqlPlaceholder
	sqlSymbol
)

type sqlToken struct {
	kind  sqlTokenKind
	value string
}

var forbiddenSQLWords = map[string]bool{
	"ALTER": true, "ANALYZE": true, "BEGIN": true, "CALL": true, "CLUSTER": true,
	"COMMENT": true, "COMMIT": true, "COPY": true, "CREATE": true, "DEALLOCATE": true,
	"DELETE": true, "DISCARD": true, "DO": true, "DROP": true, "EXECUTE": true,
	"EXPLAIN": true, "FOR": true, "GRANT": true, "IMPORT": true, "INSERT": true,
	"INTO": true, "LISTEN": true, "LOCK": true, "MERGE": true, "NOTIFY": true,
	"PREPARE": true, "REFRESH": true, "REINDEX": true, "RELEASE": true, "RESET": true,
	"RETURNING": true, "REVOKE": true, "ROLLBACK": true, "SAVEPOINT": true, "SECURITY": true,
	"SET": true, "SHOW": true, "START": true, "TRUNCATE": true, "UNLISTEN": true,
	"UPDATE": true, "VACUUM": true,
}

var allowedSQLFunctions = map[string]bool{
	"ABS": true, "ARRAY_AGG": true, "AVG": true, "CAST": true, "CEIL": true, "COALESCE": true,
	"COUNT": true, "DATE_TRUNC": true, "DENSE_RANK": true, "EXTRACT": true,
	"FLOOR": true, "LOWER": true, "MAX": true, "MIN": true, "NULLIF": true, "RANK": true,
	"REPLACE": true, "ROUND": true, "ROW_NUMBER": true, "STRPOS": true,
	"SUBSTRING": true, "SUM": true, "TO_CHAR": true, "TRIM": true, "UPPER": true,
	"CUBE": true, "ROLLUP": true,
}

var allowedSQLGroupingKeywords = map[string]bool{
	"AND": true, "AS": true, "BY": true, "FROM": true, "HAVING": true,
	"IN": true, "JOIN": true, "NOT": true, "ON": true, "OR": true,
	"OVER": true, "SELECT": true, "SETS": true, "WHEN": true, "WHERE": true,
}

func validateSQL(sql string, source compiler.PhysicalSource) error {
	tokens, err := tokenizeSQL(sql)
	if err != nil || len(tokens) == 0 {
		return reject(CodeForbiddenSQLToken)
	}
	first := tokenKeyword(tokens[0])
	if first != "SELECT" && first != "WITH" {
		return reject(CodeStatementNotSelect)
	}
	for _, token := range tokens {
		if token.kind == sqlWord && forbiddenSQLWords[strings.ToUpper(token.value)] {
			return reject(CodeForbiddenSQLToken)
		}
	}
	cteNames, err := collectCTENames(tokens)
	if err != nil {
		return err
	}
	for index := 0; index+1 < len(tokens); index++ {
		if !isIdentifierToken(tokens[index]) || tokens[index+1].kind != sqlSymbol || tokens[index+1].value != "(" {
			continue
		}
		name := strings.ToUpper(tokens[index].value)
		if allowedSQLGroupingKeywords[name] {
			continue
		}
		// PostgreSQL's trusted compiler emits NUMERIC(38,10) only as a CAST
		// target. A type modifier is not a function invocation.
		if name == "NUMERIC" && index > 0 && tokenKeyword(tokens[index-1]) == "AS" {
			continue
		}
		if index > 0 && tokens[index-1].kind == sqlSymbol && tokens[index-1].value == "." || !allowedSQLFunctions[name] {
			return reject(CodeUnsupportedFunction)
		}
	}
	physicalRelations := 0
	for index, token := range tokens {
		keyword := tokenKeyword(token)
		if keyword != "FROM" && keyword != "JOIN" {
			continue
		}
		if keyword == "FROM" && insideExtract(tokens, index) {
			continue
		}
		next := index + 1
		if next >= len(tokens) {
			return reject(CodeUntrustedRelation)
		}
		if tokens[next].kind == sqlSymbol && tokens[next].value == "(" {
			if next+1 >= len(tokens) || (tokenKeyword(tokens[next+1]) != "SELECT" && tokenKeyword(tokens[next+1]) != "WITH") {
				return reject(CodeUntrustedRelation)
			}
			continue
		}
		if !isIdentifierToken(tokens[next]) {
			return reject(CodeUntrustedRelation)
		}
		if next+2 < len(tokens) && tokens[next+1].kind == sqlSymbol && tokens[next+1].value == "." &&
			isIdentifierToken(tokens[next+2]) {
			if tokens[next].value != source.PublishedSchema || tokens[next+2].value != source.PublishedName {
				return reject(CodeUntrustedRelation)
			}
			physicalRelations++
			continue
		}
		if !cteNames[normalizedSQLIdentifier(tokens[next])] {
			return reject(CodeUntrustedRelation)
		}
	}
	if physicalRelations < 1 {
		return reject(CodeUntrustedRelation)
	}
	return nil
}

func collectCTENames(tokens []sqlToken) (map[string]bool, error) {
	result := map[string]bool{}
	if tokenKeyword(tokens[0]) != "WITH" {
		return result, nil
	}
	index := 1
	if index < len(tokens) && tokenKeyword(tokens[index]) == "RECURSIVE" {
		return nil, reject(CodeForbiddenSQLToken)
	}
	for {
		if index >= len(tokens) || !isIdentifierToken(tokens[index]) {
			return nil, reject(CodeStatementNotSelect)
		}
		name := normalizedSQLIdentifier(tokens[index])
		if result[name] {
			return nil, reject(CodeStatementNotSelect)
		}
		result[name] = true
		index++
		if index >= len(tokens) || tokenKeyword(tokens[index]) != "AS" {
			return nil, reject(CodeStatementNotSelect)
		}
		index++
		if index >= len(tokens) || tokens[index].kind != sqlSymbol || tokens[index].value != "(" ||
			index+1 >= len(tokens) || (tokenKeyword(tokens[index+1]) != "SELECT" && tokenKeyword(tokens[index+1]) != "WITH") {
			return nil, reject(CodeStatementNotSelect)
		}
		depth := 1
		index += 2
		for index < len(tokens) && depth > 0 {
			if tokens[index].kind == sqlSymbol {
				switch tokens[index].value {
				case "(":
					depth++
				case ")":
					depth--
				}
			}
			index++
		}
		if depth != 0 || index >= len(tokens) {
			return nil, reject(CodeStatementNotSelect)
		}
		if tokens[index].kind == sqlSymbol && tokens[index].value == "," {
			index++
			continue
		}
		if tokenKeyword(tokens[index]) != "SELECT" {
			return nil, reject(CodeStatementNotSelect)
		}
		return result, nil
	}
}

func tokenizeSQL(sql string) ([]sqlToken, error) {
	if len(sql) == 0 || len(sql) > 1<<20 || !utf8.ValidString(sql) {
		return nil, errors.New("SQL size or encoding is invalid")
	}
	result := make([]sqlToken, 0, len(sql)/4)
	for index := 0; index < len(sql); {
		r, size := utf8.DecodeRuneInString(sql[index:])
		if unicode.IsSpace(r) {
			index += size
			continue
		}
		if len(result) >= 100000 {
			return nil, errors.New("SQL token count exceeds limit")
		}
		switch sql[index] {
		case ';', '\x00':
			return nil, errors.New("SQL contains a statement separator")
		case '-':
			if index+1 < len(sql) && sql[index+1] == '-' {
				return nil, errors.New("SQL comments are forbidden")
			}
			result = append(result, sqlToken{kind: sqlSymbol, value: "-"})
			index++
			continue
		case '/':
			if index+1 < len(sql) && sql[index+1] == '*' {
				return nil, errors.New("SQL comments are forbidden")
			}
			result = append(result, sqlToken{kind: sqlSymbol, value: "/"})
			index++
			continue
		case '\'':
			start := index
			index++
			closed := false
			for index < len(sql) {
				if sql[index] != '\'' {
					_, runeSize := utf8.DecodeRuneInString(sql[index:])
					index += runeSize
					continue
				}
				if index+1 < len(sql) && sql[index+1] == '\'' {
					index += 2
					continue
				}
				index++
				closed = true
				break
			}
			if !closed {
				return nil, errors.New("SQL string is not closed")
			}
			result = append(result, sqlToken{kind: sqlString, value: sql[start:index]})
			continue
		case '"':
			index++
			var builder strings.Builder
			closed := false
			for index < len(sql) {
				if sql[index] != '"' {
					runeValue, runeSize := utf8.DecodeRuneInString(sql[index:])
					builder.WriteRune(runeValue)
					index += runeSize
					continue
				}
				if index+1 < len(sql) && sql[index+1] == '"' {
					builder.WriteByte('"')
					index += 2
					continue
				}
				index++
				closed = true
				break
			}
			if !closed || builder.Len() == 0 {
				return nil, errors.New("SQL identifier is not closed")
			}
			result = append(result, sqlToken{kind: sqlQuotedIdentifier, value: builder.String()})
			continue
		case '$':
			start := index
			index++
			for index < len(sql) && sql[index] >= '0' && sql[index] <= '9' {
				index++
			}
			if index == start+1 {
				return nil, errors.New("SQL dollar quoting is forbidden")
			}
			result = append(result, sqlToken{kind: sqlPlaceholder, value: sql[start:index]})
			continue
		}
		if isSQLWordStart(r) {
			start := index
			index += size
			for index < len(sql) {
				next, nextSize := utf8.DecodeRuneInString(sql[index:])
				if !isSQLWordContinue(next) {
					break
				}
				index += nextSize
			}
			result = append(result, sqlToken{kind: sqlWord, value: sql[start:index]})
			continue
		}
		if r >= '0' && r <= '9' {
			start := index
			index += size
			for index < len(sql) && ((sql[index] >= '0' && sql[index] <= '9') || sql[index] == '.') {
				index++
			}
			result = append(result, sqlToken{kind: sqlNumber, value: sql[start:index]})
			continue
		}
		if strings.ContainsRune("(),.+*=<>!|[]", r) {
			value := string(r)
			index += size
			if (index < len(sql) && strings.Contains("<>!=|", value) && sql[index] == '=') ||
				(value == "|" && index < len(sql) && sql[index] == '|') {
				value += string(sql[index])
				index++
			}
			result = append(result, sqlToken{kind: sqlSymbol, value: value})
			continue
		}
		return nil, fmt.Errorf("unsupported SQL character %q", r)
	}
	return result, nil
}

func tokenKeyword(token sqlToken) string {
	if token.kind != sqlWord {
		return ""
	}
	return strings.ToUpper(token.value)
}

func isIdentifierToken(token sqlToken) bool {
	return token.kind == sqlWord || token.kind == sqlQuotedIdentifier
}

func normalizedSQLIdentifier(token sqlToken) string {
	if token.kind == sqlQuotedIdentifier {
		return token.value
	}
	return strings.ToLower(token.value)
}

func isSQLWordStart(value rune) bool {
	return value == '_' || unicode.IsLetter(value)
}

func isSQLWordContinue(value rune) bool {
	return isSQLWordStart(value) || unicode.IsDigit(value) || value == '$'
}

func insideExtract(tokens []sqlToken, index int) bool {
	depth := 0
	for cursor := index - 1; cursor >= 0; cursor-- {
		if tokens[cursor].kind != sqlSymbol {
			continue
		}
		switch tokens[cursor].value {
		case ")":
			depth++
		case "(":
			if depth > 0 {
				depth--
				continue
			}
			return cursor > 0 && tokenKeyword(tokens[cursor-1]) == "EXTRACT"
		}
	}
	return false
}

func containsID(values []askdata.ID, target askdata.ID) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
