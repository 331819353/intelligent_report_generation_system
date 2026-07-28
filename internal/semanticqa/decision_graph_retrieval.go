package semanticqa

import (
	"context"
	"strings"

	"github.com/jackc/pgx/v5"
	"intelligent-report-generation-system/internal/platform/database"
)

// decisionGraphMetricCandidate is a metric that can be reached from one or
// more persisted dimension-value decisions mentioned by the user. This is a
// bounded reverse traversal of the governed graph, not a default metric.
type decisionGraphMetricCandidate struct {
	Code         string
	Name         string
	Domain       string
	MatchedTerms []string
	Score        float64
}

// inferPersonCountMetricsFromDecisionGraph handles questions such as
// "国内公办毕业的80后有多少人": the wording explicitly requests a person
// count but omits the catalog metric name. A metric is returned only when
// current, verified decision edges mentioned in the question converge on a
// single published people metric.
func (store *PostgresStore) inferPersonCountMetricsFromDecisionGraph(
	ctx context.Context,
	tenantID, question string,
) (items []decisionGraphMetricCandidate, err error) {
	items = []decisionGraphMetricCandidate{}
	if store == nil || !questionRequestsPersonCount(question) {
		return items, nil
	}
	err = database.WithTenantTx(ctx, store.pool, tenantID, func(tx pgx.Tx) error {
		rows, queryErr := tx.Query(ctx, `WITH eligible AS (
				SELECT decision.metric_code,decision.metric_name,
					platform.dataset_version_effective_domain(
					  version.dataset_version_id
					) AS domain,
					matched.term
				FROM platform.dimension_where_decisions AS decision
				JOIN platform.metrics AS metric
				  ON metric.tenant_id=decision.tenant_id
				 AND metric.id=decision.metric_id
				 AND metric.code=decision.metric_code
				 AND metric.current_published_version_id=
				     decision.metric_version_id
				 AND metric.status='PUBLISHED'
				 AND metric.deleted_at IS NULL
				JOIN platform.metric_versions AS version
				  ON version.tenant_id=metric.tenant_id
				 AND version.id=metric.current_published_version_id
				 AND version.metric_id=metric.id
				 AND version.dataset_version_id=decision.dataset_version_id
				 AND version.status='PUBLISHED'
				JOIN platform.semantic_dimensions AS dimension
				  ON dimension.tenant_id=decision.tenant_id
				 AND dimension.id=decision.dimension_id
				 AND dimension.dataset_version_id=version.dataset_version_id
				 AND dimension.status='PUBLISHED'
				JOIN platform.dimension_metric_compatibility AS compatibility
				  ON compatibility.tenant_id=dimension.tenant_id
				 AND compatibility.dimension_id=dimension.id
				 AND compatibility.metric_id=metric.id
				 AND compatibility.metric_version_id=version.id
				 AND compatibility.metric_dataset_version_id=
				     version.dataset_version_id
				 AND compatibility.status='VERIFIED'
				 AND compatibility.fanout_policy<>'UNSAFE'
				JOIN platform.dataset_materializations AS materialization
				  ON materialization.tenant_id=decision.tenant_id
				 AND materialization.id=decision.materialization_id
				 AND materialization.dataset_version_id=
				     version.dataset_version_id
				 AND materialization.layer='DWS'
				 AND materialization.status='ACTIVE'
				CROSS JOIN LATERAL (
				  SELECT DISTINCT candidate.term
				  FROM (
				    SELECT decision.canonical_value AS term
				    UNION ALL
				    SELECT unnest(decision.aliases)
				  ) AS candidate
				  WHERE char_length(btrim(candidate.term))>=2
				    AND position(
				      lower(btrim(candidate.term)) IN lower($1)
				    )>0
				) AS matched
				WHERE decision.tenant_id=platform.current_tenant_id()
				  AND (
				    metric.name ~* '(员工|人员|人数|人才|人力)'
				    OR version.definition_json#>>'{expression,operator}'='COUNT'
				       AND metric.name ~* '(员工|人员|人才|人)'
				  )
			)
			SELECT metric_code,metric_name,domain,
				array_agg(DISTINCT term ORDER BY term),
				least(1.0,0.70+count(DISTINCT term)*0.10)::float8 AS score
			FROM eligible
			GROUP BY metric_code,metric_name,domain
			ORDER BY score DESC,metric_code
			LIMIT 9`, question)
		if queryErr != nil {
			return queryErr
		}
		defer rows.Close()
		for rows.Next() {
			var item decisionGraphMetricCandidate
			if scanErr := rows.Scan(
				&item.Code, &item.Name, &item.Domain,
				&item.MatchedTerms, &item.Score,
			); scanErr != nil {
				return scanErr
			}
			items = append(items, item)
		}
		return rows.Err()
	})
	// Multiple reachable metrics are a real ambiguity. Never use score to turn
	// a governed multi-metric question into a guessed single metric.
	if err == nil && len(items) != 1 {
		return []decisionGraphMetricCandidate{}, nil
	}
	return items, err
}

type dimensionDecisionCandidate struct {
	DecisionID          string
	DimensionCode       string
	CanonicalValue      string
	Aliases             []string
	MemberValue         string
	SelectedMemberCount int
	PredicateOperator   string
	WhereCondition      string
	CompiledCondition   string
	LLMModel            string
	LLMReason           string
	EmbeddingModel      string
	TableSchema         string
	TableName           string
	MetricFieldID       string
	SourceType          string
	Score               float64
}

// recallMetricDimensionDecisions searches the persisted
// "dimension description:value -> metric/table/WHERE" graph. It deliberately
// stays inside one already selected metric and dimension.
func (store *PostgresStore) recallMetricDimensionDecisions(
	ctx context.Context,
	tenantID, metricCode, dimensionCode, queryValue string,
	vector []float32,
	limit int,
) (items []dimensionDecisionCandidate, err error) {
	items = []dimensionDecisionCandidate{}
	if store == nil || metricCode == "" || dimensionCode == "" ||
		len(vector) == 0 || limit < 1 || limit > 128 {
		return items, nil
	}
	err = database.WithTenantTx(ctx, store.pool, tenantID, func(tx pgx.Tx) error {
		rows, queryErr := tx.Query(ctx, `WITH eligible AS (
				SELECT decision.id::text,dimension.code::text,
					decision.canonical_value,decision.aliases,
					COALESCE(member.normalized_value,'') AS member_value,
					decision.selected_member_count,
					decision.predicate_operator,decision.where_condition,
					decision.compiled_condition,decision.llm_model,
					decision.llm_reason,decision.embedding_model,
					decision.table_schema,decision.table_name,
					decision.metric_field_id,decision.source_type,
					GREATEST(
					  0.0,1.0-(
					    COALESCE(decision.embedding,document.embedding)
					    <=> $3::halfvec
					  )
					)::float8 AS score,
					CASE
					  WHEN lower(decision.canonical_value)=lower($4)
					    OR EXISTS(
					      SELECT 1 FROM unnest(decision.aliases) AS alias(value)
					      WHERE lower(alias.value)=lower($4)
					    )
					  THEN 1 ELSE 0
					END AS exact_value
				FROM platform.dimension_where_decisions AS decision
				JOIN platform.metrics AS metric
				  ON metric.tenant_id=decision.tenant_id
				 AND metric.id=decision.metric_id
				 AND metric.code=$1
				 AND metric.current_published_version_id=
				     decision.metric_version_id
				 AND metric.status='PUBLISHED'
				 AND metric.deleted_at IS NULL
				JOIN platform.metric_versions AS version
				  ON version.tenant_id=metric.tenant_id
				 AND version.id=metric.current_published_version_id
				 AND version.dataset_version_id=decision.dataset_version_id
				 AND version.status='PUBLISHED'
				JOIN platform.semantic_dimensions AS dimension
				  ON dimension.tenant_id=decision.tenant_id
				 AND dimension.id=decision.dimension_id
				 AND lower(dimension.code::text)=lower($2)
				 AND dimension.dataset_version_id=version.dataset_version_id
				 AND dimension.status='PUBLISHED'
				JOIN platform.dimension_metric_compatibility AS compatibility
				  ON compatibility.tenant_id=dimension.tenant_id
				 AND compatibility.dimension_id=dimension.id
				 AND compatibility.metric_id=metric.id
				 AND compatibility.metric_version_id=version.id
				 AND compatibility.metric_dataset_version_id=
				     version.dataset_version_id
				 AND compatibility.status='VERIFIED'
				 AND compatibility.fanout_policy<>'UNSAFE'
				JOIN platform.dataset_materializations AS materialization
				  ON materialization.tenant_id=decision.tenant_id
				 AND materialization.id=decision.materialization_id
				 AND materialization.dataset_version_id=
				     version.dataset_version_id
				 AND materialization.status='ACTIVE'
				LEFT JOIN platform.dimension_member_semantic_documents AS document
				  ON document.tenant_id=decision.tenant_id
				 AND document.id=decision.embedding_document_id
				 AND document.embedding_status='SUCCEEDED'
				LEFT JOIN platform.dimension_members AS member
				  ON member.tenant_id=decision.tenant_id
				 AND member.id=decision.dimension_member_id
				 AND member.dimension_id=decision.dimension_id
				 AND member.status='ACTIVE'
				WHERE decision.tenant_id=platform.current_tenant_id()
			)
			SELECT id,code,canonical_value,aliases,member_value,
				selected_member_count,predicate_operator,where_condition,
				compiled_condition,llm_model,llm_reason,embedding_model,
				table_schema,table_name,metric_field_id,source_type,score
			FROM eligible
			WHERE score>=0.35
			ORDER BY exact_value DESC,score DESC,
				CASE source_type WHEN 'QUERY_OBSERVED' THEN 0 ELSE 1 END,
				canonical_value,id
			LIMIT $5`,
			metricCode, dimensionCode, formatVector(vector), queryValue, limit,
		)
		if queryErr != nil {
			return queryErr
		}
		defer rows.Close()
		for rows.Next() {
			var item dimensionDecisionCandidate
			if scanErr := rows.Scan(
				&item.DecisionID, &item.DimensionCode,
				&item.CanonicalValue, &item.Aliases, &item.MemberValue,
				&item.SelectedMemberCount, &item.PredicateOperator,
				&item.WhereCondition, &item.CompiledCondition,
				&item.LLMModel, &item.LLMReason, &item.EmbeddingModel,
				&item.TableSchema, &item.TableName, &item.MetricFieldID,
				&item.SourceType, &item.Score,
			); scanErr != nil {
				return scanErr
			}
			items = append(items, item)
		}
		return rows.Err()
	})
	return items, err
}

func questionRequestsPersonCount(question string) bool {
	question = strings.ToLower(strings.Join(strings.Fields(question), ""))
	for _, phrase := range []string{
		"多少人", "有几人", "几个人", "人数", "人员数量", "员工数量",
		"人员总数", "员工总数", "headcount",
	} {
		if strings.Contains(question, phrase) {
			return true
		}
	}
	return false
}

func metricTracesWithDecisionGraphSelection(
	question string,
	catalog []recallCandidate,
	candidate decisionGraphMetricCandidate,
) []QueryMetricCandidateTrace {
	result := metricCandidateTraces(
		question, catalog, []string{candidate.Code}, "DECISION_GRAPH",
	)
	matchedTerm := personCountPhrase(question)
	found := false
	for index := range result {
		if strings.EqualFold(result[index].Code, candidate.Code) {
			result[index].Label = candidate.Name
			result[index].MatchedTerm = matchedTerm
			result[index].Score = candidate.Score
			found = true
		}
	}
	if !found {
		result = append(result, QueryMetricCandidateTrace{
			Code: candidate.Code, Label: candidate.Name,
			MatchedTerm: matchedTerm, MatchMethod: "DECISION_GRAPH",
			Score: candidate.Score, Selected: true, Source: "CURRENT_TURN",
		})
	}
	return result
}

func personCountPhrase(question string) string {
	compact := strings.ToLower(strings.Join(strings.Fields(question), ""))
	for _, phrase := range []string{
		"有多少人", "多少人", "有几人", "几个人", "人数",
		"人员数量", "员工数量", "人员总数", "员工总数", "headcount",
	} {
		if strings.Contains(compact, phrase) {
			return phrase
		}
	}
	return ""
}

func decisionMatchesLookup(
	decision dimensionDecisionCandidate,
	lookup QueryDimensionValueLookupTrace,
) bool {
	if !strings.EqualFold(decision.DimensionCode, lookup.DimensionCode) ||
		decision.SelectedMemberCount != len(lookup.SelectedMemberKeys) ||
		decision.DecisionID == "" || decision.WhereCondition == "" ||
		decision.CompiledCondition == "" {
		return false
	}
	if len(lookup.SelectedMemberKeys) == 1 && decision.MemberValue != "" &&
		!strings.EqualFold(
			decision.MemberValue, lookup.SelectedMemberKeys[0],
		) {
		return false
	}
	for _, expected := range []string{lookup.CanonicalValue, lookup.Term} {
		if expected == "" {
			continue
		}
		if strings.EqualFold(expected, decision.CanonicalValue) {
			return true
		}
		for _, alias := range decision.Aliases {
			if strings.EqualFold(expected, alias) {
				return true
			}
		}
	}
	// A very high semantic score is accepted only after the governed member
	// identity/count checks above have also passed.
	return decision.Score >= 0.92
}
