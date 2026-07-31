package semanticqa

import (
	"context"
	"strings"

	"github.com/jackc/pgx/v5"
	"intelligent-report-generation-system/internal/platform/database"
)

type semanticParsingRule struct {
	RuleType      string
	Pattern       string
	MatchMode     string
	Action        string
	OutputName    string
	OutputCode    string
	MinimumLength int
	MaximumLength int
	Priority      int
}

type semanticParsingRules struct {
	metricNameSuffixes  []semanticParsingRule
	adminRegionSuffixes []semanticParsingRule
	queryResidualTerms  map[string]bool
	broadMetricPhrases  []semanticParsingRule
}

// semanticParsingRules resolves tenant overrides before filtering ACTIVE rows.
// A deprecated tenant rule therefore acts as a tombstone for the corresponding
// platform default. Rules are read for every request so configuration changes
// take effect without restarting or publishing the service.
func (store *PostgresStore) semanticParsingRules(
	ctx context.Context,
	tenantID string,
) (rules semanticParsingRules, err error) {
	rules.queryResidualTerms = map[string]bool{}
	if store == nil {
		return rules, ErrInvalidRequest
	}
	err = database.WithTenantTx(ctx, store.pool, tenantID, func(tx pgx.Tx) error {
		rows, queryErr := tx.Query(ctx, `WITH ranked AS (
			SELECT rule_type,pattern,match_mode,action,output_name,output_code,
				minimum_length,maximum_length,priority,status,
				row_number() OVER (
					PARTITION BY rule_type,lower(pattern)
					ORDER BY (tenant_id IS NOT NULL) DESC,version DESC,updated_at DESC,id
				) AS precedence
			FROM platform.semantic_parsing_rules
			WHERE tenant_id IS NULL OR tenant_id=platform.current_tenant_id()
		)
		SELECT rule_type,pattern,match_mode,action,output_name,output_code,
			minimum_length,maximum_length,priority
		FROM ranked
		WHERE precedence=1 AND status='ACTIVE'
		ORDER BY priority DESC,char_length(pattern) DESC,lower(pattern)`)
		if queryErr != nil {
			return queryErr
		}
		defer rows.Close()
		for rows.Next() {
			var rule semanticParsingRule
			if scanErr := rows.Scan(
				&rule.RuleType, &rule.Pattern, &rule.MatchMode, &rule.Action,
				&rule.OutputName, &rule.OutputCode, &rule.MinimumLength,
				&rule.MaximumLength, &rule.Priority,
			); scanErr != nil {
				return scanErr
			}
			switch rule.RuleType {
			case "METRIC_NAME_SUFFIX":
				rules.metricNameSuffixes = append(rules.metricNameSuffixes, rule)
			case "ADMIN_REGION_SUFFIX":
				rules.adminRegionSuffixes = append(rules.adminRegionSuffixes, rule)
			case "QUERY_RESIDUAL_TERM":
				rules.queryResidualTerms[normalizeParsingRuleText(rule.Pattern)] = true
			case "BROAD_METRIC_PHRASE":
				rules.broadMetricPhrases = append(rules.broadMetricPhrases, rule)
			}
		}
		return rows.Err()
	})
	return rules, err
}

func (rules semanticParsingRules) metricTerms(candidate recallCandidate) []string {
	terms := append(
		[]string{candidate.Label, candidate.Code},
		candidate.Aliases...,
	)
	for _, source := range append(
		[]string{candidate.Label}, candidate.Aliases...,
	) {
		source = strings.TrimSpace(source)
		for _, rule := range rules.metricNameSuffixes {
			if !strings.HasSuffix(source, rule.Pattern) {
				continue
			}
			stem := strings.TrimSpace(strings.TrimSuffix(source, rule.Pattern))
			if len([]rune(stem)) < rule.MinimumLength {
				continue
			}
			terms = appendUniqueString(terms, stem)
			break
		}
	}
	return terms
}

func (rules semanticParsingRules) administrativeLocation(
	text string,
) (value, name, code string, found bool) {
	text = strings.TrimSpace(text)
	for _, rule := range rules.adminRegionSuffixes {
		if !strings.HasSuffix(text, rule.Pattern) {
			continue
		}
		value = strings.TrimSpace(strings.TrimSuffix(text, rule.Pattern))
		length := len([]rune(value))
		if length < rule.MinimumLength ||
			(rule.MaximumLength > 0 && length > rule.MaximumLength) {
			return "", "", "", false
		}
		return value, rule.OutputName, rule.OutputCode, true
	}
	return "", "", "", false
}

func (rules semanticParsingRules) administrativeSuffixAtStart(text string) string {
	text = strings.TrimSpace(text)
	for _, rule := range rules.adminRegionSuffixes {
		if !strings.HasPrefix(text, rule.Pattern) {
			continue
		}
		remainder := strings.TrimPrefix(text, rule.Pattern)
		if remainder == "" || rules.isDeterministicResidual(remainder) {
			return rule.Pattern
		}
	}
	return ""
}

func (rules semanticParsingRules) isDeterministicResidual(text string) bool {
	text = normalizeParsingRuleText(text)
	return text == "" || rules.queryResidualTerms[text]
}

func (rules semanticParsingRules) requestsBroadMetricSelection(
	question string,
) bool {
	question = normalizeParsingRuleText(question)
	for _, rule := range rules.broadMetricPhrases {
		if strings.Contains(question, normalizeParsingRuleText(rule.Pattern)) {
			return true
		}
	}
	return false
}

func normalizeParsingRuleText(text string) string {
	return strings.ToLower(strings.Join(strings.Fields(strings.TrimSpace(text)), ""))
}
