package semanticqa

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"math"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	aiplatform "intelligent-report-generation-system/internal/ai"
	"intelligent-report-generation-system/internal/embedding"
	"intelligent-report-generation-system/internal/platform/database"
)

const (
	tokenSemanticRetrievalLimit = 5
	tokenSemanticVectorPool     = 24
	tokenSemanticMaximumTokens  = 32
	tokenSemanticMinimumScore   = 0.01
	tokenSemanticRepairMaxBytes = 32 << 10
)

type tokenSemanticCorpusItem struct {
	SemanticType  string
	Name          string
	Code          string
	Description   string
	SearchText    string
	DimensionName string
	DimensionCode string
	DimensionType string
	FieldID       string
	Value         string
}

type tokenSemanticLLMOutput struct {
	Intent              string                            `json:"intent"`
	QuestionMetricRanks []int                             `json:"questionMetricRanks"`
	MetricSelections    []tokenSemanticMetricSelection    `json:"metricSelections"`
	DimensionSelections []tokenSemanticDimensionSelection `json:"dimensionSelections"`
	Confidence          float64                           `json:"confidence"`
}

type tokenSemanticMetricSelection struct {
	TokenStart    int `json:"tokenStart"`
	CandidateRank int `json:"candidateRank"`
}

type tokenSemanticDimensionSelection struct {
	SourceTokenStart    int     `json:"sourceTokenStart"`
	CandidateTokenStart int     `json:"candidateTokenStart"`
	CandidateRank       int     `json:"candidateRank"`
	NormalizedValue     string  `json:"normalizedValue"`
	TimeRangeStart      string  `json:"timeRangeStart"`
	TimeRangeEnd        string  `json:"timeRangeEndExclusive"`
	Confidence          float64 `json:"confidence"`
}

func (interpreter *SemanticInterpreter) enrichTokenSemantics(
	ctx context.Context,
	tenantID, actorID, question, timezone string,
	result QueryTokenization,
	allowTrustedMetricAnchorCompletion bool,
	parsingRules semanticParsingRules,
) QueryTokenization {
	result.SemanticRetrievalMode = "LEXICAL_SEMANTIC_DOCUMENT_FALLBACK"
	result.IndexPrerequisites = []QuerySemanticIndexStatus{}
	result.QuestionEmbedding = QueryEmbeddingTrace{Status: "NOT_CONFIGURED"}
	result.QuestionMetricTop5 = []QueryTokenSemanticCandidate{}
	result.SemanticRetrievals = []QueryTokenSemanticRetrieval{}
	result.LLMCompletion = QueryTokenLLMCompletion{
		Status: "PENDING", Intent: "UNKNOWN", MetricNames: []string{},
		DimensionValues: []QueryLLMDimensionValue{},
	}
	if statuses, statusErr := interpreter.store.semanticIndexStatuses(
		ctx, tenantID,
	); statusErr == nil {
		result.IndexPrerequisites = statuses
	}
	metrics, dimensions, err :=
		interpreter.store.tokenSemanticCorpus(ctx, tenantID)
	if err != nil {
		result.LLMCompletion.Status = "FAILED_RETRIEVAL"
		result.LLMCompletion.ErrorCode = "SEMANTIC_CORPUS_UNAVAILABLE"
		return result
	}
	valueTypes, _ := interpreter.store.tokenSemanticDimensionValueTypes(
		ctx, tenantID,
	)
	deterministicCompletion, deterministic := deterministicTokenSemanticCompletion(
		question, timezone, time.Now(), result.Tokens,
		allowTrustedMetricAnchorCompletion, parsingRules,
	)

	searchTokens := make([]QueryToken, 0, tokenSemanticMaximumTokens)
	for _, token := range result.Tokens {
		if token.EntityType == "PUNCTUATION" ||
			strings.TrimSpace(token.Text) == "" {
			continue
		}
		searchTokens = append(searchTokens, token)
		if len(searchTokens) == tokenSemanticMaximumTokens {
			break
		}
	}

	vectorByStart := map[int][]float32{}
	var questionVector []float32
	if !deterministic &&
		interpreter.embedding != nil && interpreter.embedding.Configured() {
		texts := make([]string, 0, len(searchTokens)+1)
		texts = append(texts, question)
		for _, token := range searchTokens {
			texts = append(texts, token.Text)
		}
		if vectors, embedErr := embedSemanticTexts(
			ctx, interpreter.embedding, texts,
		); embedErr == nil && len(vectors) == len(texts) {
			questionVector = vectors[0]
			for index, token := range searchTokens {
				vectorByStart[token.Start] = vectors[index+1]
			}
			result.SemanticRetrievalMode = "VECTOR_SEMANTIC_DOCUMENT"
			result.QuestionEmbedding = QueryEmbeddingTrace{
				Status: "SUCCEEDED", Model: interpreter.embedding.Model(),
				Dimensions: interpreter.embedding.Dimensions(),
			}
		} else {
			result.QuestionEmbedding = QueryEmbeddingTrace{
				Status: "FAILED", Model: interpreter.embedding.Model(),
				Dimensions: interpreter.embedding.Dimensions(),
			}
		}
	}

	if len(questionVector) > 0 {
		if questionMetrics, vectorErr :=
			interpreter.store.vectorTokenSemanticCandidates(
				ctx, tenantID, question, questionVector,
				tokenSemanticRetrievalLimit, true, false,
			); vectorErr == nil {
			result.QuestionMetricTop5 = questionMetrics
		}
	}
	applyTokenSemanticValueTypes(result.QuestionMetricTop5, valueTypes)
	if len(result.QuestionMetricTop5) == 0 {
		result.QuestionMetricTop5 = rankTokenSemanticCorpus(
			QueryToken{
				Text: question, EntityType: "NOUN_CANDIDATE",
			},
			metrics, tokenSemanticRetrievalLimit,
		)
	}

	for _, token := range searchTokens {
		retrieval := QueryTokenSemanticRetrieval{
			Token: token.Text, PartOfSpeech: token.PartOfSpeech,
			EntityType: token.EntityType,
			Start:      token.Start, End: token.End,
			RetrievalStatus:     "LEXICAL_FALLBACK",
			MetricCandidates:    []QueryTokenSemanticCandidate{},
			DimensionCandidates: []QueryTokenSemanticCandidate{},
		}
		searchMetrics, searchDimensions :=
			tokenSemanticSearchTargets(token)
		if !searchMetrics && !searchDimensions {
			retrieval.RetrievalStatus = "SKIPPED_FUNCTION_WORD"
			result.SemanticRetrievals = append(
				result.SemanticRetrievals, retrieval,
			)
			continue
		}
		if vector := vectorByStart[token.Start]; len(vector) > 0 {
			vectorCandidates, vectorErr :=
				interpreter.store.vectorTokenSemanticCandidates(
					ctx, tenantID, token.Text, vector,
					tokenSemanticVectorPool,
					searchMetrics, searchDimensions,
				)
			if vectorErr == nil {
				retrieval.RetrievalStatus = "SUCCEEDED_VECTOR"
				retrieval.MetricCandidates,
					retrieval.DimensionCandidates =
					curateVectorTokenSemanticCandidates(
						token, vectorCandidates,
						tokenSemanticRetrievalLimit,
					)
			}
		}
		if len(retrieval.MetricCandidates) == 0 && searchMetrics {
			retrieval.MetricCandidates = rankTokenSemanticCorpus(
				token, metrics, tokenSemanticRetrievalLimit,
			)
		}
		if len(retrieval.DimensionCandidates) == 0 && searchDimensions {
			retrieval.DimensionCandidates = rankTokenSemanticCorpus(
				token, dimensions, tokenSemanticRetrievalLimit,
			)
		}
		applyTokenSemanticValueTypes(
			retrieval.DimensionCandidates, valueTypes,
		)
		result.SemanticRetrievals = append(
			result.SemanticRetrievals, retrieval,
		)
	}
	if deterministic {
		result.LLMCompletion = deterministicCompletion
		return result
	}
	result.LLMCompletion = interpreter.completeTokenSemantics(
		ctx, tenantID, actorID, question, result.QuestionMetricTop5,
		result.SemanticRetrievals, timezone, time.Now(),
	)
	return result
}

func deterministicTokenSemanticCompletion(
	question, timezone string,
	now time.Time,
	tokens []QueryToken,
	allowTrustedMetricAnchorCompletion bool,
	parsingRules semanticParsingRules,
) (QueryTokenLLMCompletion, bool) {
	location, err := time.LoadLocation(timezone)
	if err != nil {
		location = time.UTC
		timezone = "UTC"
	}
	result := QueryTokenLLMCompletion{
		Status: "SUCCEEDED", Model: "DETERMINISTIC_SEMANTIC_CATALOG",
		Intent:            inferIntent(strings.ToLower(question)),
		AugmentedQuestion: question, MetricNames: []string{},
		DimensionValues: []QueryLLMDimensionValue{},
		ReferenceTime:   now.In(location).Format(time.RFC3339),
		Timezone:        timezone, Confidence: 1,
	}
	administrativeLocationCount := 0
	for _, token := range tokens {
		switch token.EntityType {
		case "METRIC":
			if token.EntityCode == "" || token.EntityName == "" {
				return QueryTokenLLMCompletion{}, false
			}
			result.MetricNames = appendUniqueString(
				result.MetricNames, token.EntityName,
			)
		case "DIMENSION_VALUE":
			if token.EntityCode == "" || token.EntityName == "" {
				return QueryTokenLLMCompletion{}, false
			}
			result.DimensionValues = append(
				result.DimensionValues,
				QueryLLMDimensionValue{
					SourceToken: token.Text, Value: token.Text,
					DimensionName: token.EntityName,
					DimensionCode: token.EntityCode,
					DimensionType: "STANDARD", ValueType: "STRING",
					Confidence: token.Confidence,
				},
			)
		case "TIME":
			// Relative dates and period boundaries still require the bounded
			// completion step to produce a typed, left-closed range.
			return QueryTokenLLMCompletion{}, false
		case "LOCATION", "PERSON", "ORGANIZATION", "PROPER_NOUN", "NUMBER":
			if _, _, _, ok := parsingRules.administrativeLocation(token.Text); !ok {
				return QueryTokenLLMCompletion{}, false
			}
			administrativeLocationCount++
		case "NOUN_CANDIDATE":
			if !parsingRules.isDeterministicResidual(token.Text) {
				if _, _, _, ok := parsingRules.administrativeLocation(token.Text); !ok {
					return QueryTokenLLMCompletion{}, false
				}
				administrativeLocationCount++
			}
		case "TEXT":
			if len([]rune(strings.TrimSpace(token.Text))) > 1 &&
				!parsingRules.isDeterministicResidual(token.Text) {
				return QueryTokenLLMCompletion{}, false
			}
		}
	}
	if len(result.MetricNames) == 0 &&
		!parsingRules.requestsBroadMetricSelection(question) &&
		(!allowTrustedMetricAnchorCompletion ||
			administrativeLocationCount == 0) {
		return QueryTokenLLMCompletion{}, false
	}
	return result, true
}

func tokenSemanticSearchTargets(token QueryToken) (
	metrics bool,
	dimensions bool,
) {
	switch token.EntityType {
	case "TEXT", "PUNCTUATION", "QUERY_WORD",
		"ANALYSIS_WORD", "COMPARISON_WORD", "METRIC":
		return false, false
	case "TIME", "LOCATION", "PERSON", "ORGANIZATION",
		"PROPER_NOUN", "NUMBER", "DIMENSION", "DIMENSION_VALUE":
		return false, true
	default:
		// Metric retrieval is intentionally whole-question first. The metric
		// tool loop may issue a bounded follow-up search for a complete metric
		// phrase when those candidates are insufficient; individual tokenizer
		// fragments are used only to discover dimensions and values.
		return false, true
	}
}

func embedSemanticTexts(
	ctx context.Context,
	provider embedding.Provider,
	texts []string,
) ([][]float32, error) {
	if provider == nil || !provider.Configured() || len(texts) == 0 {
		return nil, embedding.ErrUnavailable
	}
	result := make([][]float32, 0, len(texts))
	for start := 0; start < len(texts); start += 32 {
		end := min(start+32, len(texts))
		vectors, err := provider.Embed(ctx, texts[start:end])
		if err != nil {
			return nil, err
		}
		if len(vectors) != end-start {
			return nil, embedding.ErrInvalidResponse
		}
		result = append(result, vectors...)
	}
	return result, nil
}

func tokenSemanticValueTypeKey(dimensionCode, fieldID string) string {
	return strings.ToLower(strings.TrimSpace(dimensionCode)) + "\x00" +
		strings.ToLower(strings.TrimSpace(fieldID))
}

func applyTokenSemanticValueTypes(
	candidates []QueryTokenSemanticCandidate,
	valueTypes map[string]string,
) {
	for index := range candidates {
		candidates[index].ValueType = valueTypes[tokenSemanticValueTypeKey(
			candidates[index].DimensionCode,
			candidates[index].FieldID,
		)]
	}
}

func (store *PostgresStore) tokenSemanticDimensionValueTypes(
	ctx context.Context,
	tenantID string,
) (items map[string]string, err error) {
	items = map[string]string{}
	err = database.WithTenantTx(ctx, store.pool, tenantID, func(tx pgx.Tx) error {
		rows, queryErr := tx.Query(ctx, `SELECT dimension.code::text,
				dimension.field_id,field.canonical_type
			FROM platform.semantic_dimensions AS dimension
			JOIN platform.datasets AS dataset
			  ON dataset.tenant_id=dimension.tenant_id
			 AND dataset.id=dimension.dataset_id
			 AND dataset.current_published_version_id=
			     dimension.dataset_version_id
			 AND dataset.status='PUBLISHED'
			 AND dataset.deleted_at IS NULL
			JOIN platform.dataset_fields AS field
			  ON field.tenant_id=dimension.tenant_id
			 AND field.dataset_version_id=dimension.dataset_version_id
			 AND field.field_id=dimension.field_id
			WHERE dimension.status='PUBLISHED'
			ORDER BY dimension.code,dimension.field_id`)
		if queryErr != nil {
			return queryErr
		}
		defer rows.Close()
		for rows.Next() {
			var dimensionCode, fieldID, valueType string
			if scanErr := rows.Scan(
				&dimensionCode, &fieldID, &valueType,
			); scanErr != nil {
				return scanErr
			}
			key := tokenSemanticValueTypeKey(dimensionCode, fieldID)
			if existing, found := items[key]; found &&
				!strings.EqualFold(existing, valueType) {
				items[key] = ""
				continue
			}
			items[key] = strings.ToUpper(strings.TrimSpace(valueType))
		}
		return rows.Err()
	})
	return items, err
}

func (store *PostgresStore) semanticIndexStatuses(
	ctx context.Context,
	tenantID string,
) (items []QuerySemanticIndexStatus, err error) {
	items = []QuerySemanticIndexStatus{}
	err = database.WithTenantTx(ctx, store.pool, tenantID, func(tx pgx.Tx) error {
		rows, queryErr := tx.Query(ctx, `WITH index_status AS (
			SELECT 1 AS ordering,
				'METRIC_VECTOR'::text AS index_type,
				'指标名称'::text AS key_shape,
				'指标字段 + 指标名称'::text AS value_shape,
				count(*)::bigint AS total,
				count(*) FILTER(
				  WHERE document.embedding_status='SUCCEEDED'
				)::bigint AS ready,
				COALESCE(
				  max(NULLIF(document.embedding_model,'')),''
				) AS model
			FROM platform.metric_semantic_documents AS document
			JOIN platform.metrics AS metric
			  ON metric.tenant_id=document.tenant_id
			 AND metric.id=document.metric_id
			 AND metric.current_published_version_id=
			     document.metric_version_id
			 AND metric.status='PUBLISHED'
			 AND metric.deleted_at IS NULL
			WHERE document.subject_type='METRIC_VERSION'
			UNION ALL
			SELECT 2,'DIMENSION_VECTOR','维度名称',
				'维度字段 + 维度名称',
				count(*)::bigint,
				count(*) FILTER(
				  WHERE embedding_status='SUCCEEDED'
				)::bigint,
				COALESCE(max(NULLIF(embedding_model,'')),'')
			FROM platform.dimension_semantic_documents
			UNION ALL
			SELECT 3,'SEMANTIC_VECTOR','语义常用词',
				'映射值 + 知识类型',
				count(*)::bigint,
				count(*) FILTER(
				  WHERE embedding_status='SUCCEEDED'
				)::bigint,
				COALESCE(max(NULLIF(embedding_model,'')),'')
			FROM platform.semantic_term_assets
			WHERE status='ACTIVE'
			UNION ALL
			SELECT 4,'DECISION_GRAPH','维度描述 + 维度值',
				'维度名称 + 指标名称 + 表名 + WHERE',
				count(*)::bigint,count(*)::bigint,
				COALESCE(max(NULLIF(embedding_model,'')),'')
			FROM platform.dimension_where_decisions
		)
		SELECT index_type,key_shape,value_shape,total,ready,
			total-ready AS pending,
			CASE
			  WHEN total=0 THEN 'EMPTY'
			  WHEN ready=total THEN 'READY'
			  WHEN ready>0 THEN 'PARTIAL'
			  ELSE 'BUILDING'
			END,
			model
		FROM index_status
		ORDER BY ordering`)
		if queryErr != nil {
			return queryErr
		}
		defer rows.Close()
		for rows.Next() {
			var item QuerySemanticIndexStatus
			if scanErr := rows.Scan(
				&item.IndexType, &item.KeyShape, &item.ValueShape,
				&item.Total, &item.Ready, &item.Pending,
				&item.Status, &item.Model,
			); scanErr != nil {
				return scanErr
			}
			items = append(items, item)
		}
		return rows.Err()
	})
	return items, err
}

func (store *PostgresStore) vectorTokenSemanticCandidates(
	ctx context.Context,
	tenantID, query string,
	vector []float32,
	limit int,
	includeMetrics, includeDimensions bool,
) (items []QueryTokenSemanticCandidate, err error) {
	items = []QueryTokenSemanticCandidate{}
	if store == nil || len(vector) == 0 || limit < 1 ||
		(!includeMetrics && !includeDimensions) {
		return items, nil
	}
	vectorLiteral := formatVector(vector)
	err = database.WithTenantTx(ctx, store.pool, tenantID, func(tx pgx.Tx) error {
		if includeMetrics {
			rows, queryErr := tx.Query(ctx, `WITH eligible AS (
				SELECT metric.name,metric.code::text,
					COALESCE(document.description,'') AS description,
					COALESCE(
					  version.definition_json#>>'{expression,fieldId}',''
					) AS field_id,
					GREATEST(
					  0.0,1.0-(document.embedding <=> $2::halfvec)
					)::float8 AS semantic_score,
					GREATEST(
					  similarity(lower(metric.name),lower($1)),
					  CASE
					    WHEN lower(metric.name)=lower($1) THEN 1.0
					    WHEN position(lower($1) IN lower(metric.name))>0
					      OR position(lower(metric.name) IN lower($1))>0
					    THEN 0.9 ELSE 0 END
					)::float8 AS keyword_score
				FROM platform.metric_semantic_documents AS document
				JOIN platform.metrics AS metric
				  ON metric.tenant_id=document.tenant_id
				 AND metric.id=document.metric_id
				 AND metric.current_published_version_id=
				     document.metric_version_id
				 AND metric.status='PUBLISHED'
				 AND metric.deleted_at IS NULL
				JOIN platform.metric_versions AS version
				  ON version.tenant_id=document.tenant_id
				 AND version.id=document.metric_version_id
				 AND version.status='PUBLISHED'
				WHERE document.subject_type='METRIC_VERSION'
				  AND document.embedding_status='SUCCEEDED'
			)
			SELECT name,code,description,field_id,
				GREATEST(
				  keyword_score,
				  semantic_score*0.82+keyword_score*0.18
				)::float8 AS score
			FROM eligible
			ORDER BY score DESC,name,code
			LIMIT $3`, query, vectorLiteral, limit)
			if queryErr != nil {
				return queryErr
			}
			for rows.Next() {
				var candidate QueryTokenSemanticCandidate
				if scanErr := rows.Scan(
					&candidate.Name, &candidate.Code,
					&candidate.Description, &candidate.FieldID,
					&candidate.Score,
				); scanErr != nil {
					rows.Close()
					return scanErr
				}
				candidate.SemanticType = "METRIC"
				candidate.MatchMethod = "VECTOR_SEMANTIC_DOCUMENT"
				candidate.Score = math.Round(candidate.Score*10000) / 10000
				items = append(items, candidate)
			}
			if rowsErr := rows.Err(); rowsErr != nil {
				rows.Close()
				return rowsErr
			}
			rows.Close()
		}
		if !includeDimensions {
			return nil
		}
		rows, queryErr := tx.Query(ctx, `WITH candidates AS (
			SELECT 'DIMENSION'::text AS semantic_type,
				document.dimension_name AS name,
				document.dimension_code::text AS code,
				document.dimension_description AS description,
				document.dimension_name,document.dimension_code::text,
				document.dimension_type,document.field_id,''::text AS value,
				GREATEST(
				  0.0,1.0-(document.embedding <=> $2::halfvec)
				)::float8 AS semantic_score,
				GREATEST(
				  similarity(lower(document.dimension_name),lower($1)),
				  CASE
				    WHEN lower(document.dimension_name)=lower($1) THEN 1.0
				    WHEN position(lower($1) IN lower(document.dimension_name))>0
				      OR position(
				        lower(document.dimension_name) IN lower($1)
				      )>0
				    THEN 0.9 ELSE 0 END
				)::float8 AS keyword_score
			FROM platform.dimension_semantic_documents AS document
			WHERE document.embedding_status='SUCCEEDED'
			UNION ALL
			SELECT 'DIMENSION',dimension.name,dimension.code::text,
				dimension.description,dimension.name,
				dimension.code::text,dimension.dimension_type,
				dimension.field_id,''::text,
				CASE
				  WHEN asset.embedding_status='SUCCEEDED'
				  THEN GREATEST(0.0,1.0-(asset.embedding <=> $2::halfvec))
				  ELSE 0.0
				END::float8,
				GREATEST(
				  similarity(lower(asset.common_term::text),lower($1)),
				  CASE
				    WHEN lower(asset.common_term::text)=lower($1) THEN 1.0
				    WHEN position(
				      lower($1) IN lower(asset.common_term::text)
				    )>0 OR position(
				      lower(asset.common_term::text) IN lower($1)
				    )>0 THEN 0.9 ELSE 0 END
				)::float8
			FROM platform.semantic_term_assets AS asset
			JOIN platform.semantic_dimensions AS dimension
			  ON dimension.tenant_id=asset.tenant_id
			 AND lower(asset.mapping_value) IN (
			    lower(dimension.code::text),lower(dimension.name)
			  )
			 AND dimension.status='PUBLISHED'
			JOIN platform.datasets AS dataset
			  ON dataset.tenant_id=dimension.tenant_id
			 AND dataset.id=dimension.dataset_id
			 AND dataset.current_published_version_id=
			     dimension.dataset_version_id
			 AND dataset.status='PUBLISHED'
			 AND dataset.deleted_at IS NULL
			WHERE asset.status='ACTIVE'
			UNION ALL
			SELECT 'DIMENSION_VALUE',member_document.member_label,
				member_document.dimension_code::text,
				member_document.document,member_document.dimension_name,
				member_document.dimension_code::text,
				dimension.dimension_type,dimension.field_id,
				member_document.member_label,
				GREATEST(
				  0.0,1.0-(member_document.embedding <=> $2::halfvec)
				)::float8,
				GREATEST(
				  similarity(
				    lower(member_document.member_label),lower($1)
				  ),
				  CASE
				    WHEN lower(member_document.member_label)=lower($1)
				      THEN 1.0
				    WHEN position(
				      lower($1) IN lower(member_document.member_label)
				    )>0 OR position(
				      lower(member_document.member_label) IN lower($1)
				    )>0 THEN 0.9 ELSE 0 END
				)::float8
			FROM platform.dimension_member_semantic_documents
			  AS member_document
			JOIN platform.semantic_dimensions AS dimension
			  ON dimension.tenant_id=member_document.tenant_id
			 AND dimension.id=member_document.dimension_id
			 AND dimension.status='PUBLISHED'
			WHERE member_document.embedding_status='SUCCEEDED'
			UNION ALL
			SELECT 'DIMENSION_VALUE',member_document.member_label,
				member_document.dimension_code::text,
				member_document.document,member_document.dimension_name,
				member_document.dimension_code::text,
				dimension.dimension_type,dimension.field_id,
				member_document.member_label,
				CASE
				  WHEN asset.embedding_status='SUCCEEDED'
				  THEN GREATEST(0.0,1.0-(asset.embedding <=> $2::halfvec))
				  ELSE 0.0
				END::float8,
				GREATEST(
				  similarity(lower(asset.common_term::text),lower($1)),
				  CASE
				    WHEN lower(asset.common_term::text)=lower($1) THEN 1.0
				    WHEN position(
				      lower($1) IN lower(asset.common_term::text)
				    )>0 OR position(
				      lower(asset.common_term::text) IN lower($1)
				    )>0 THEN 0.9 ELSE 0 END
				)::float8
			FROM platform.semantic_term_assets AS asset
			JOIN platform.dimension_member_semantic_documents
			  AS member_document
			  ON member_document.tenant_id=asset.tenant_id
			 AND lower(asset.mapping_value) IN (
			   lower(member_document.member_label),
			   lower(
			     member_document.dimension_code::text||':'||
			     member_document.member_label
			   )
			 )
			 AND member_document.embedding_status='SUCCEEDED'
			JOIN platform.semantic_dimensions AS dimension
			  ON dimension.tenant_id=member_document.tenant_id
			 AND dimension.id=member_document.dimension_id
			 AND dimension.status='PUBLISHED'
			JOIN platform.datasets AS dataset
			  ON dataset.tenant_id=dimension.tenant_id
			 AND dataset.id=dimension.dataset_id
			 AND dataset.current_published_version_id=
			     dimension.dataset_version_id
			 AND dataset.status='PUBLISHED'
			 AND dataset.deleted_at IS NULL
			WHERE asset.status='ACTIVE'
		)
		SELECT semantic_type,name,code,description,dimension_name,
			dimension_code,dimension_type,field_id,value,
			GREATEST(
			  keyword_score,
			  semantic_score*0.82+keyword_score*0.18
			)::float8 AS score
		FROM candidates
		ORDER BY score DESC,
			CASE semantic_type WHEN 'DIMENSION_VALUE' THEN 0 ELSE 1 END,
			name,code
		LIMIT $3`, query, vectorLiteral, limit)
		if queryErr != nil {
			return queryErr
		}
		defer rows.Close()
		for rows.Next() {
			var candidate QueryTokenSemanticCandidate
			if scanErr := rows.Scan(
				&candidate.SemanticType, &candidate.Name,
				&candidate.Code, &candidate.Description,
				&candidate.DimensionName, &candidate.DimensionCode,
				&candidate.DimensionType, &candidate.FieldID,
				&candidate.Value, &candidate.Score,
			); scanErr != nil {
				return scanErr
			}
			candidate.MatchMethod = "VECTOR_SEMANTIC_DOCUMENT"
			candidate.Geographic = candidate.DimensionType == "GEOGRAPHY"
			candidate.Score = math.Round(candidate.Score*10000) / 10000
			items = append(items, candidate)
		}
		return rows.Err()
	})
	return items, err
}

func curateVectorTokenSemanticCandidates(
	token QueryToken,
	candidates []QueryTokenSemanticCandidate,
	limit int,
) (metrics, dimensions []QueryTokenSemanticCandidate) {
	metrics = []QueryTokenSemanticCandidate{}
	dimensions = []QueryTokenSemanticCandidate{}
	if limit < 1 {
		return metrics, dimensions
	}
	metricSeen := map[string]bool{}
	dimensionSeen := map[string]bool{}
	for _, candidate := range candidates {
		if candidate.SemanticType == "METRIC" {
			key := strings.ToLower(strings.Join(
				[]string{candidate.Code, candidate.FieldID}, "\x00",
			))
			if len(metrics) >= limit || metricSeen[key] {
				continue
			}
			metricSeen[key] = true
			metrics = append(metrics, candidate)
			continue
		}
		if !tokenSemanticDimensionCandidateAllowed(token, candidate) {
			continue
		}
		keyParts := []string{
			candidate.SemanticType, candidate.DimensionCode,
			candidate.FieldID,
		}
		if candidate.SemanticType == "DIMENSION_VALUE" {
			keyParts = append(
				keyParts, normalizeSemanticSearchText(candidate.Value),
			)
		}
		key := strings.ToLower(strings.Join(keyParts, "\x00"))
		if dimensionSeen[key] {
			continue
		}
		dimensionSeen[key] = true
		dimensions = append(dimensions, candidate)
	}
	if len(dimensions) > limit {
		dimensions = dimensions[:limit]
	}
	return metrics, dimensions
}

func tokenSemanticDimensionCandidateAllowed(
	token QueryToken,
	candidate QueryTokenSemanticCandidate,
) bool {
	if token.EntityType == "TIME" && candidate.DimensionType != "TIME" {
		return false
	}
	if candidate.SemanticType != "DIMENSION_VALUE" {
		return true
	}
	source := normalizeSemanticSearchText(token.Text)
	return source != "" &&
		(source == normalizeSemanticSearchText(candidate.Value) ||
			source == normalizeSemanticSearchText(candidate.Name))
}

func (store *PostgresStore) tokenSemanticCorpus(
	ctx context.Context,
	tenantID string,
) (metrics, dimensions []tokenSemanticCorpusItem, err error) {
	metrics = []tokenSemanticCorpusItem{}
	dimensions = []tokenSemanticCorpusItem{}
	err = database.WithTenantTx(ctx, store.pool, tenantID, func(tx pgx.Tx) error {
		metricRows, queryErr := tx.Query(ctx, `SELECT metric.name,
				metric.code::text,
				COALESCE(document.description,''),
				concat_ws(' ',metric.name,COALESCE(document.description,''),
					COALESCE(document.caliber,''),
					COALESCE(document.document,''),
					array_to_string(COALESCE(document.tags,'{}'::text[]),' '),
					array_to_string(COALESCE(alias.aliases,'{}'::text[]),' ')),
				COALESCE(alias.aliases,'{}'::text[]),
				COALESCE(version.definition_json#>>'{expression,fieldId}','')
			FROM platform.metrics AS metric
			JOIN platform.metric_versions AS version
			  ON version.tenant_id=metric.tenant_id
			 AND version.id=metric.current_published_version_id
			 AND version.status='PUBLISHED'
			JOIN platform.datasets AS dataset
			  ON dataset.tenant_id=version.tenant_id
			 AND dataset.id=version.dataset_id
			 AND dataset.current_published_version_id=version.dataset_version_id
			 AND dataset.status='PUBLISHED'
			 AND dataset.deleted_at IS NULL
			LEFT JOIN platform.metric_semantic_documents AS document
			  ON document.tenant_id=version.tenant_id
			 AND document.metric_version_id=version.id
			 AND document.subject_type='METRIC_VERSION'
			LEFT JOIN LATERAL (
			  SELECT array_agg(asset.common_term::text ORDER BY
			    char_length(asset.common_term::text) DESC,
			    lower(asset.common_term::text)
			  ) AS aliases
			  FROM platform.semantic_term_assets AS asset
			  WHERE asset.tenant_id=metric.tenant_id
			    AND asset.status='ACTIVE'
			    AND lower(asset.mapping_value) IN (
			      lower(metric.code::text),lower(metric.name)
			    )
			) AS alias ON true
			WHERE metric.status='PUBLISHED'
			  AND metric.deleted_at IS NULL
			ORDER BY metric.name,metric.code
			LIMIT 512`)
		if queryErr != nil {
			return queryErr
		}
		for metricRows.Next() {
			var item tokenSemanticCorpusItem
			var aliases []string
			if scanErr := metricRows.Scan(
				&item.Name, &item.Code, &item.Description,
				&item.SearchText, &aliases, &item.FieldID,
			); scanErr != nil {
				metricRows.Close()
				return scanErr
			}
			item.SemanticType = "METRIC"
			item.SearchText += " " + strings.Join(aliases, " ")
			metrics = append(metrics, item)
		}
		if rowsErr := metricRows.Err(); rowsErr != nil {
			metricRows.Close()
			return rowsErr
		}
		metricRows.Close()

		dimensionRows, queryErr := tx.Query(ctx, `SELECT 'DIMENSION'::text,
				dimension.name,dimension.code::text,dimension.description,
				dimension.name,dimension.code::text,
				dimension.dimension_type,dimension.field_id,''::text,
				concat_ws(' ',dimension.name,dimension.code::text,
					dimension.description,dimension.dimension_type,
					array_to_string(
					  COALESCE(alias.aliases,'{}'::text[]),' '
					))
			FROM platform.semantic_dimensions AS dimension
			JOIN platform.datasets AS dataset
			  ON dataset.tenant_id=dimension.tenant_id
			 AND dataset.id=dimension.dataset_id
			 AND dataset.current_published_version_id=dimension.dataset_version_id
			 AND dataset.status='PUBLISHED'
			 AND dataset.deleted_at IS NULL
			LEFT JOIN LATERAL (
			  SELECT array_agg(asset.common_term::text ORDER BY
			    char_length(asset.common_term::text) DESC,
			    lower(asset.common_term::text)
			  ) AS aliases
			  FROM platform.semantic_term_assets AS asset
			  WHERE asset.tenant_id=dimension.tenant_id
			    AND asset.status='ACTIVE'
			    AND lower(asset.mapping_value) IN (
			      lower(dimension.code::text),lower(dimension.name)
			    )
			) AS alias ON true
			WHERE dimension.status='PUBLISHED'
			UNION ALL
			SELECT 'DIMENSION_VALUE'::text,
				document.member_label,document.dimension_code::text,
				document.document,document.dimension_name,
				document.dimension_code::text,
				dimension.dimension_type,dimension.field_id,
				document.member_label,
				concat_ws(' ',document.dimension_name,
					document.dimension_code::text,document.member_label,
					document.document,dimension.description,
					array_to_string(
					  COALESCE(alias.aliases,'{}'::text[]),' '
					))
			FROM platform.dimension_member_semantic_documents AS document
			JOIN platform.semantic_dimensions AS dimension
			  ON dimension.tenant_id=document.tenant_id
			 AND dimension.id=document.dimension_id
			 AND dimension.status='PUBLISHED'
			JOIN platform.datasets AS dataset
			  ON dataset.tenant_id=document.tenant_id
			 AND dataset.id=document.dataset_id
			 AND dataset.current_published_version_id=document.dataset_version_id
			 AND dataset.status='PUBLISHED'
			 AND dataset.deleted_at IS NULL
			LEFT JOIN LATERAL (
			  SELECT array_agg(asset.common_term::text ORDER BY
			    char_length(asset.common_term::text) DESC,
			    lower(asset.common_term::text)
			  ) AS aliases
			  FROM platform.semantic_term_assets AS asset
			  WHERE asset.tenant_id=document.tenant_id
			    AND asset.status='ACTIVE'
			    AND lower(asset.mapping_value) IN (
			      lower(document.member_label),
			      lower(
			        document.dimension_code::text||':'||
			        document.member_label
			      )
			    )
			) AS alias ON true
			WHERE document.embedding_status='SUCCEEDED'
			ORDER BY 1,2,3
			LIMIT 4096`)
		if queryErr != nil {
			return queryErr
		}
		defer dimensionRows.Close()
		for dimensionRows.Next() {
			var item tokenSemanticCorpusItem
			if scanErr := dimensionRows.Scan(
				&item.SemanticType, &item.Name, &item.Code,
				&item.Description, &item.DimensionName,
				&item.DimensionCode, &item.DimensionType,
				&item.FieldID, &item.Value, &item.SearchText,
			); scanErr != nil {
				return scanErr
			}
			dimensions = append(dimensions, item)
		}
		return dimensionRows.Err()
	})
	return metrics, dimensions, err
}

func rankTokenSemanticCorpus(
	token QueryToken,
	corpus []tokenSemanticCorpusItem,
	limit int,
) []QueryTokenSemanticCandidate {
	type scoredItem struct {
		item  tokenSemanticCorpusItem
		score float64
	}
	scored := make([]scoredItem, 0, len(corpus))
	for _, item := range corpus {
		if token.EntityType == "TIME" &&
			item.DimensionName != "" &&
			item.DimensionType != "TIME" {
			continue
		}
		score := tokenSemanticLexicalScore(token.Text, item)
		switch {
		case token.EntityType == "TIME" &&
			item.SemanticType == "DIMENSION" &&
			item.DimensionType == "TIME":
			score = max(score, 0.72)
		case token.EntityType == "TIME" &&
			item.SemanticType == "DIMENSION_VALUE" &&
			score < 0.8:
			score = 0
		case token.EntityType == "LOCATION" &&
			item.SemanticType == "DIMENSION" &&
			item.DimensionType == "GEOGRAPHY":
			score = max(score, 0.62)
		}
		if score < tokenSemanticMinimumScore {
			continue
		}
		scored = append(scored, scoredItem{item: item, score: score})
	}
	sort.SliceStable(scored, func(left, right int) bool {
		if scored[left].score != scored[right].score {
			return scored[left].score > scored[right].score
		}
		if scored[left].item.SemanticType != scored[right].item.SemanticType {
			return scored[left].item.SemanticType <
				scored[right].item.SemanticType
		}
		if scored[left].item.Name != scored[right].item.Name {
			return scored[left].item.Name < scored[right].item.Name
		}
		return scored[left].item.Code < scored[right].item.Code
	})
	result := []QueryTokenSemanticCandidate{}
	seen := map[string]bool{}
	for _, scoredCandidate := range scored {
		item := scoredCandidate.item
		key := strings.Join([]string{
			item.SemanticType, item.Code, item.DimensionCode, item.Value,
		}, "\x00")
		if seen[key] {
			continue
		}
		seen[key] = true
		result = append(result, QueryTokenSemanticCandidate{
			SemanticType: item.SemanticType, Name: item.Name,
			Code: item.Code, Description: truncateRunes(item.Description, 240),
			DimensionName: item.DimensionName,
			DimensionCode: item.DimensionCode,
			DimensionType: item.DimensionType,
			FieldID:       item.FieldID, Value: item.Value,
			Geographic:  item.DimensionType == "GEOGRAPHY",
			Score:       math.Round(scoredCandidate.score*10000) / 10000,
			MatchMethod: "LEXICAL_SEMANTIC_DOCUMENT",
		})
		if len(result) == limit {
			break
		}
	}
	return result
}

func tokenSemanticLexicalScore(
	query string,
	item tokenSemanticCorpusItem,
) float64 {
	query = normalizeSemanticSearchText(query)
	if query == "" {
		return 0
	}
	label := normalizeSemanticSearchText(item.Name)
	searchText := normalizeSemanticSearchText(item.SearchText)
	queryLength := len([]rune(query))
	switch {
	case query == label:
		return 1
	case strings.Contains(label, query) || strings.Contains(query, label):
		ratio := float64(min(len([]rune(query)), len([]rune(label)))) /
			float64(max(len([]rune(query)), len([]rune(label))))
		return min(0.96, 0.72+0.22*ratio)
	case queryLength > 1 && strings.Contains(searchText, query):
		return 0.74
	}
	if queryLength == 1 {
		return 0
	}
	labelDice := semanticNGramDice(query, label)
	searchCoverage := semanticNGramCoverage(query, searchText)
	runeOverlap := semanticRuneOverlap(query, label)
	return min(0.71,
		0.55*labelDice+0.35*searchCoverage+0.1*runeOverlap,
	)
}

func normalizeSemanticSearchText(value string) string {
	var builder strings.Builder
	for _, character := range strings.ToLower(strings.TrimSpace(value)) {
		if unicode.IsSpace(character) ||
			unicode.IsPunct(character) ||
			unicode.IsSymbol(character) {
			continue
		}
		builder.WriteRune(character)
	}
	return builder.String()
}

func semanticNGramDice(left, right string) float64 {
	leftGrams := semanticNGrams(left)
	rightGrams := semanticNGrams(right)
	if len(leftGrams) == 0 || len(rightGrams) == 0 {
		return 0
	}
	intersection := 0
	remaining := map[string]int{}
	for _, gram := range rightGrams {
		remaining[gram]++
	}
	for _, gram := range leftGrams {
		if remaining[gram] > 0 {
			intersection++
			remaining[gram]--
		}
	}
	return 2 * float64(intersection) /
		float64(len(leftGrams)+len(rightGrams))
}

func semanticNGramCoverage(query, candidate string) float64 {
	grams := semanticNGrams(query)
	if len(grams) == 0 {
		return 0
	}
	matched := 0
	for _, gram := range grams {
		if strings.Contains(candidate, gram) {
			matched++
		}
	}
	return float64(matched) / float64(len(grams))
}

func semanticNGrams(value string) []string {
	runes := []rune(value)
	if len(runes) == 0 {
		return nil
	}
	if len(runes) == 1 {
		return []string{value}
	}
	result := make([]string, 0, len(runes)-1)
	for index := 0; index+1 < len(runes); index++ {
		result = append(result, string(runes[index:index+2]))
	}
	return result
}

func semanticRuneOverlap(left, right string) float64 {
	leftRunes := map[rune]bool{}
	rightRunes := map[rune]bool{}
	for _, character := range left {
		leftRunes[character] = true
	}
	for _, character := range right {
		rightRunes[character] = true
	}
	if len(leftRunes) == 0 || len(rightRunes) == 0 {
		return 0
	}
	intersection := 0
	for character := range leftRunes {
		if rightRunes[character] {
			intersection++
		}
	}
	return float64(intersection) /
		float64(max(len(leftRunes), len(rightRunes)))
}

func truncateRunes(value string, limit int) string {
	value = strings.TrimSpace(value)
	if utf8.RuneCountInString(value) <= limit {
		return value
	}
	return string([]rune(value)[:limit]) + "…"
}

func (interpreter *SemanticInterpreter) completeTokenSemantics(
	ctx context.Context,
	tenantID, actorID, question string,
	questionMetrics []QueryTokenSemanticCandidate,
	retrievals []QueryTokenSemanticRetrieval,
	timezone string,
	now time.Time,
) QueryTokenLLMCompletion {
	location, locationErr := time.LoadLocation(timezone)
	if locationErr != nil {
		location = time.UTC
		timezone = "UTC"
	}
	referenceTime := now.In(location).Format(time.RFC3339)
	fallback := QueryTokenLLMCompletion{
		Status: "SKIPPED_AI_NOT_CONFIGURED", Intent: "UNKNOWN",
		AugmentedQuestion: question,
		MetricNames:       []string{},
		DimensionValues:   []QueryLLMDimensionValue{},
		ReferenceTime:     referenceTime,
		Timezone:          timezone,
	}
	if interpreter == nil || interpreter.ai == nil ||
		!interpreter.ai.Configured() {
		return fallback
	}
	payload, err := json.Marshal(struct {
		Question           string                        `json:"question"`
		ReferenceTime      string                        `json:"referenceTime"`
		Timezone           string                        `json:"timezone"`
		QuestionMetricTop5 []QueryTokenSemanticCandidate `json:"questionMetricTop5"`
		Retrievals         []QueryTokenSemanticRetrieval `json:"retrievals"`
	}{
		Question: question, ReferenceTime: referenceTime,
		Timezone: timezone, QuestionMetricTop5: questionMetrics,
		Retrievals: retrievals,
	})
	if err != nil {
		fallback.Status = "FAILED"
		fallback.ErrorCode = "FAILED_PAYLOAD"
		return fallback
	}
	temperature := 0.0
	invocation := aiplatform.Invocation{
		TenantID: tenantID, ActorID: actorID,
		Purpose:       aiplatform.PurposeSemanticQueryPlanning,
		PromptVersion: "semantic-token-question-completion-v3",
		ResourceType:  "SEMANTIC_TOKEN_COMPLETION",
		ResourceID:    hashText(question),
		Request: aiplatform.ProviderRequest{
			Messages: []aiplatform.Message{
				{
					Role: aiplatform.MessageRoleSystem,
					Parts: []aiplatform.ContentPart{{
						Type: aiplatform.ContentTypeText,
						Text: `你是只读语义意图与候选选择器。输入包含原问题、整句指标语义 Top 5，以及每个分词的维度语义 Top 5。指标必须优先根据保留完整上下文的 questionMetricTop5 判断；分词仅用于识别维度和值。不要复制或改写名称，只返回意图、候选排名和分词位置。
intent 只能是 LOOKUP、METRIC、TREND、COMPARISON、RANKING、DRILLDOWN、DISTRIBUTION、FUNNEL、RETENTION、ANOMALY、UNKNOWN。
LOOKUP 只用于用户明确要求记录、列表、明细或逐行数据；“多少、几笔、金额、数量、总计、是多少”这类聚合值问题必须是 METRIC。
questionMetricRanks：从 questionMetricTop5 选择候选，排名只能是 1 到 5。
metricSelections：保留为协议兼容字段，必须返回空数组；不得通过普通分词选择指标。
整句指标 Top 5 只是候选，不是结果清单。对原问题中的每一个独立指标表达，只能从整句候选中选出一个语义最吻合的指标，严禁因为候选进入 Top 5 就全部选择。只有原问题明确表达多个不同指标时，才允许选择多个指标；没有等价候选时输出空数组，后续受控工具循环会按完整指标短语补充检索。
dimensionSelections：sourceTokenStart 是原问题中维度值分词的 start；candidateTokenStart 是提供维度语义候选的分词 start；candidateRank 是该分词 dimensionCandidates 的 1 到 5。可以使用相邻词召回的维度候选，但必须依据候选中已发布的 dimensionName、dimensionCode、dimensionType、valueType 和 description 判断其是否符合完整问题，不得根据程序外的固定业务词表推断。同一个 sourceTokenStart 只能选择一个最符合完整问题的维度。
每个选择都必须返回 normalizedValue、timeRangeStart、timeRangeEndExclusive。非 TIME 维度的 normalizedValue 必须逐字复制原分词，两个时间边界必须是空字符串，不能改写成另一个成员值。TIME 维度必须结合原问题中的时间关系、referenceTime、timezone 和候选 valueType 完成规范化：normalizedValue 输出便于读者核对的明确日期或时间范围；DATE 边界使用 YYYY-MM-DD，DATETIME 边界使用带时区的 RFC3339；timeRangeStart/timeRangeEndExclusive 表示左闭右开的实际查询范围。
必须先区分三种时间关系：
1. 时间点：normalizedValue 是明确时间点，查询范围覆盖该时间点所属的最小业务粒度。
2. 期间：normalizedValue 是明确起止范围，查询范围只覆盖该期间。
3. 截止上限：原问题含“截止、截至、到……为止、as of、through”等上限含义时，不能改写成目标周期内查询。normalizedValue 必须是明确的截止日期或时刻；DATE 的 timeRangeStart 使用平台累计纪元 1970-01-01，timeRangeEndExclusive 使用截止日期下一日，从而包含截止日。
相对时间和周期边界必须先根据 referenceTime 与 timezone 计算成实际日历值，不能继续输出原始描述词；无法可靠转换时不要选择该维度。
TIME 分词只有在候选中真实存在 dimensionType=TIME 且 valueType 为 DATE 或 DATETIME 时才必须选择；没有可用时间维度候选时必须省略。DIMENSION_VALUE 候选必须与原词精确同值。sourceTokenStart 只能指向 LOCATION、TIME、PERSON、ORGANIZATION、PROPER_NOUN、NUMBER、DIMENSION_VALUE，或精确命中 DIMENSION_VALUE 候选的分词，不能指向普通 NOUN_CANDIDATE、查询词、助词、指标词或标点。整句候选与分词候选冲突时结合原问题和已发布语义说明选择；证据不足时输出空数组。不得输出 SQL、名称、解释或额外字段。`,
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
				Name: "semantic_token_question_completion",
				Schema: json.RawMessage(`{
					"type":"object","additionalProperties":false,
					"required":["intent","questionMetricRanks","metricSelections","dimensionSelections","confidence"],
					"properties":{
						"intent":{"enum":["LOOKUP","METRIC","TREND","COMPARISON","RANKING","DRILLDOWN","DISTRIBUTION","FUNNEL","RETENTION","ANOMALY","UNKNOWN"]},
						"questionMetricRanks":{
							"type":"array","maxItems":3,
							"items":{"type":"integer","minimum":1,"maximum":5}
						},
						"metricSelections":{
							"type":"array","maxItems":3,
							"items":{
								"type":"object","additionalProperties":false,
								"required":["tokenStart","candidateRank"],
								"properties":{
									"tokenStart":{"type":"integer","minimum":0},
									"candidateRank":{"type":"integer","minimum":1,"maximum":5}
								}
							}
						},
						"dimensionSelections":{
							"type":"array","maxItems":16,
							"items":{
								"type":"object","additionalProperties":false,
								"required":["sourceTokenStart","candidateTokenStart","candidateRank","normalizedValue","timeRangeStart","timeRangeEndExclusive","confidence"],
								"properties":{
									"sourceTokenStart":{"type":"integer","minimum":0},
									"candidateTokenStart":{"type":"integer","minimum":0},
									"candidateRank":{"type":"integer","minimum":1,"maximum":5},
									"normalizedValue":{"type":"string","maxLength":1024},
									"timeRangeStart":{"type":"string","maxLength":64},
									"timeRangeEndExclusive":{"type":"string","maxLength":64},
									"confidence":{"type":"number","minimum":0,"maximum":1}
								}
							}
						},
						"confidence":{"type":"number","minimum":0,"maximum":1}
					}
				}`),
			},
			Temperature: &temperature, MaxOutputTokens: 1000,
		},
	}
	invocationResult, err := interpreter.ai.Invoke(ctx, invocation)
	if invalidContent, diagnostic, repairable :=
		aiplatform.InvalidOutputDetails(err); repairable {
		slog.WarnContext(
			ctx, "semantic token completion requires structured output repair",
			"output_diagnostic", diagnostic,
		)
		repairInvocation := tokenSemanticRepairInvocation(
			interpreter.ai, invocation, invalidContent,
			`上一份输出未通过 JSON Schema 校验。请重新输出一个完整 JSON 对象：必须包含 intent、questionMetricRanks、metricSelections、dimensionSelections、confidence；数组为空时输出 []；每个 dimensionSelections 选择项必须包含 normalizedValue、timeRangeStart、timeRangeEndExclusive；选择项只使用输入中真实存在的 token start 和 1 到 5 的 candidateRank；不要输出名称、解释、Markdown 或额外字段。结构诊断：`+diagnostic,
		)
		invocationResult, err = interpreter.ai.Invoke(ctx, repairInvocation)
	}
	if err != nil {
		fallback.Status = whereDesignFailureStatus(err)
		fallback.ErrorCode = tokenSemanticCompletionErrorCode(err)
		return fallback
	}
	var output tokenSemanticLLMOutput
	decodeValid := json.Unmarshal(
		invocationResult.ProviderResult.Content, &output,
	) == nil
	resolved, outputValid := resolveTokenSemanticLLMOutput(
		question, questionMetrics, retrievals, output,
	)
	outputValid = decodeValid && outputValid
	if !outputValid {
		slog.WarnContext(
			ctx, "semantic token completion requires domain output repair",
			"output_diagnostic", "selection outside retrieved Top 5",
		)
		repairInvocation := tokenSemanticRepairInvocation(
			interpreter.ai, invocation,
			invocationResult.ProviderResult.Content,
			`上一份 JSON 的意图、位置、排名、实体类型或规范值越过了服务端白名单。请重新输出：questionMetricRanks 必须对应整句 Top 5；metricSelections.tokenStart 必须对应一个拥有该 candidateRank 指标候选的分词；dimensionSelections.sourceTokenStart 必须对应允许作为维度值的实体分词，candidateTokenStart 必须对应一个拥有该 candidateRank 维度候选的分词；同一个 sourceTokenStart 只能选一个维度；维度含义只能依据候选中已发布的名称、编码、类型、valueType 和说明判断；TIME 选择必须依据 referenceTime 与 timezone 输出符合 DATE 或 DATETIME 类型的明确左闭右开范围，不能保留相对描述；必须区分时间点、期间和截止上限，截止上限不得改写成目标周期内范围；非 TIME normalizedValue 必须复制原词且时间边界为空；不得选择与原词不同的 DIMENSION_VALUE；candidateRank 只能是 1 到 5；无法证明时使用空数组。不要输出名称或额外字段。`,
		)
		invocationResult, err = interpreter.ai.Invoke(ctx, repairInvocation)
		if err != nil {
			fallback.Status = whereDesignFailureStatus(err)
			fallback.ErrorCode = tokenSemanticCompletionErrorCode(err)
			return fallback
		}
		output = tokenSemanticLLMOutput{}
		decodeValid = json.Unmarshal(
			invocationResult.ProviderResult.Content, &output,
		) == nil
		resolved, outputValid = resolveTokenSemanticLLMOutput(
			question, questionMetrics, retrievals, output,
		)
		outputValid = decodeValid && outputValid
	}
	if !outputValid {
		fallback.Status = "FAILED_VALIDATION"
		fallback.Model = invocationResult.ProviderResult.Model
		fallback.ErrorCode = "LLM_OUTPUT_OUTSIDE_RETRIEVAL"
		return fallback
	}
	resolved.Status = "SUCCEEDED"
	resolved.Model = invocationResult.ProviderResult.Model
	resolved.ReferenceTime = referenceTime
	resolved.Timezone = timezone
	return resolved
}

func tokenSemanticRepairInvocation(
	invoker semanticAIInvoker,
	base aiplatform.Invocation,
	invalidContent json.RawMessage,
	instruction string,
) aiplatform.Invocation {
	repair := base
	if fallbackProvider, ok := invoker.(interface {
		FallbackModel() string
	}); ok {
		repair.PreferredModel = strings.TrimSpace(
			fallbackProvider.FallbackModel(),
		)
	}
	messages := append(
		[]aiplatform.Message(nil), base.Request.Messages...,
	)
	if len(invalidContent) > 0 &&
		len(invalidContent) <= tokenSemanticRepairMaxBytes {
		messages = append(messages, aiplatform.Message{
			Role: aiplatform.MessageRoleAssistant,
			Parts: []aiplatform.ContentPart{{
				Type: aiplatform.ContentTypeText,
				Text: string(invalidContent),
			}},
		})
	}
	messages = append(messages, aiplatform.Message{
		Role: aiplatform.MessageRoleUser,
		Parts: []aiplatform.ContentPart{{
			Type: aiplatform.ContentTypeText,
			Text: instruction,
		}},
	})
	repair.Request.Messages = messages
	return repair
}

func tokenSemanticCompletionErrorCode(err error) string {
	var providerErr *aiplatform.ProviderError
	if errors.As(err, &providerErr) {
		return string(providerErr.Code)
	}
	switch {
	case errors.Is(err, aiplatform.ErrQuotaExceeded):
		return "AI_QUOTA_EXCEEDED"
	case errors.Is(err, aiplatform.ErrTenantAIForbidden):
		return "AI_TENANT_FORBIDDEN"
	default:
		return "AI_COMPLETION_FAILED"
	}
}

func resolveTokenSemanticLLMOutput(
	question string,
	questionMetrics []QueryTokenSemanticCandidate,
	retrievals []QueryTokenSemanticRetrieval,
	output tokenSemanticLLMOutput,
) (QueryTokenLLMCompletion, bool) {
	intent := strings.ToUpper(strings.TrimSpace(output.Intent))
	result := QueryTokenLLMCompletion{
		Intent:            intent,
		AugmentedQuestion: question,
		MetricNames:       []string{},
		DimensionValues:   []QueryLLMDimensionValue{},
		Confidence:        output.Confidence,
	}
	if !oneOf(
		intent, "LOOKUP", "METRIC", "TREND", "COMPARISON",
		"RANKING", "DRILLDOWN", "DISTRIBUTION", "FUNNEL",
		"RETENTION", "ANOMALY", "UNKNOWN",
	) || output.Confidence < 0 || output.Confidence > 1 {
		return result, false
	}
	for _, rank := range output.QuestionMetricRanks {
		index := rank - 1
		if index < 0 || index >= len(questionMetrics) {
			return result, false
		}
		result.MetricNames = appendUniqueString(
			result.MetricNames, questionMetrics[index].Name,
		)
	}
	retrievalByStart := map[int]QueryTokenSemanticRetrieval{}
	for _, retrieval := range retrievals {
		retrievalByStart[retrieval.Start] = retrieval
	}
	// Per-token metric selections remain readable for responses produced by an
	// older prompt, but the current retrieval path leaves those candidate lists
	// empty and resolves metrics from the whole-question shortlist instead.
	if len(output.QuestionMetricRanks) == 0 {
		for _, selection := range output.MetricSelections {
			retrieval, found := retrievalByStart[selection.TokenStart]
			index := selection.CandidateRank - 1
			if !found || index < 0 ||
				index >= len(retrieval.MetricCandidates) {
				return result, false
			}
			name := retrieval.MetricCandidates[index].Name
			result.MetricNames = appendUniqueString(
				result.MetricNames, name,
			)
		}
	}
	dimensionPositionBySource := map[int]int{}
	dimensionScoreBySource := map[int]float64{}
	for _, selection := range output.DimensionSelections {
		source, sourceFound := retrievalByStart[selection.SourceTokenStart]
		candidateRetrieval, candidateFound :=
			retrievalByStart[selection.CandidateTokenStart]
		index := selection.CandidateRank - 1
		if !sourceFound || !candidateFound ||
			!validTokenDimensionValueSource(source) ||
			index < 0 ||
			index >= len(candidateRetrieval.DimensionCandidates) ||
			selection.Confidence < 0 || selection.Confidence > 1 {
			return result, false
		}
		candidate := candidateRetrieval.DimensionCandidates[index]
		if !tokenSemanticDimensionCandidateAllowed(
			QueryToken{
				Text: source.Token, EntityType: source.EntityType,
			},
			candidate,
		) {
			return result, false
		}
		value := source.Token
		var timeRange *QueryTimeRange
		if candidate.DimensionType == "TIME" {
			normalizedValue := strings.TrimSpace(
				selection.NormalizedValue,
			)
			normalizedRange, valid := tokenSemanticTimeRange(
				selection, candidate.ValueType,
			)
			if !valid || normalizedValue == "" {
				return result, false
			}
			value = normalizedValue
			timeRange = &normalizedRange
		} else {
			normalizedValue := strings.TrimSpace(
				selection.NormalizedValue,
			)
			if (normalizedValue != "" &&
				normalizedValue != source.Token) ||
				strings.TrimSpace(selection.TimeRangeStart) != "" ||
				strings.TrimSpace(selection.TimeRangeEnd) != "" {
				return result, false
			}
		}
		dimension := QueryLLMDimensionValue{
			SourceToken: source.Token, Value: value,
			DimensionName: candidate.DimensionName,
			DimensionCode: candidate.DimensionCode,
			DimensionType: candidate.DimensionType,
			ValueType:     candidate.ValueType,
			FieldID:       candidate.FieldID,
			TimeRange:     timeRange,
			Confidence:    selection.Confidence,
		}
		if position, exists :=
			dimensionPositionBySource[selection.SourceTokenStart]; exists {
			if candidate.Score >
				dimensionScoreBySource[selection.SourceTokenStart] {
				result.DimensionValues[position] = dimension
				dimensionScoreBySource[selection.SourceTokenStart] =
					candidate.Score
			}
		} else {
			dimensionPositionBySource[selection.SourceTokenStart] =
				len(result.DimensionValues)
			dimensionScoreBySource[selection.SourceTokenStart] =
				candidate.Score
			result.DimensionValues = append(
				result.DimensionValues, dimension,
			)
		}
	}
	for _, retrieval := range retrievals {
		if retrieval.EntityType == "TIME" &&
			hasTimeDimensionCandidate(retrieval.DimensionCandidates) {
			if _, selected := dimensionPositionBySource[retrieval.Start]; !selected {
				return result, false
			}
		}
	}
	result.AugmentedQuestion = augmentTokenSemanticQuestion(
		question, result.Intent, result.MetricNames, result.DimensionValues,
	)
	return result, true
}

func hasTimeDimensionCandidate(
	candidates []QueryTokenSemanticCandidate,
) bool {
	for _, candidate := range candidates {
		if candidate.DimensionType == "TIME" &&
			oneOf(candidate.ValueType, "DATE", "DATETIME") {
			return true
		}
	}
	return false
}

func tokenSemanticTimeRange(
	selection tokenSemanticDimensionSelection,
	valueType string,
) (QueryTimeRange, bool) {
	start := strings.TrimSpace(selection.TimeRangeStart)
	end := strings.TrimSpace(selection.TimeRangeEnd)
	valueType = strings.ToUpper(strings.TrimSpace(valueType))
	if start == "" || end == "" ||
		!oneOf(valueType, "DATE", "DATETIME") {
		return QueryTimeRange{}, false
	}
	normalized, err := normalizeQueryTimeRange(QueryTimeRange{
		Start: start, EndExclusive: end,
	})
	if err != nil {
		return QueryTimeRange{}, false
	}
	_, startDateOnly, startErr := parseQueryBoundary(normalized.Start)
	_, endDateOnly, endErr := parseQueryBoundary(normalized.EndExclusive)
	if startErr != nil || endErr != nil || startDateOnly != endDateOnly ||
		(valueType == "DATE") != startDateOnly {
		return QueryTimeRange{}, false
	}
	return normalized, true
}

func augmentTokenSemanticQuestion(
	question string,
	intent string,
	metrics []string,
	dimensions []QueryLLMDimensionValue,
) string {
	parts := []string{}
	if intent = strings.ToUpper(strings.TrimSpace(intent)); intent != "" && intent != "UNKNOWN" {
		parts = append(parts, "意图："+intent)
	}
	if len(metrics) > 0 {
		parts = append(parts, "指标："+strings.Join(metrics, "、"))
	}
	if len(dimensions) > 0 {
		values := make([]string, 0, len(dimensions))
		for _, dimension := range dimensions {
			values = append(values, dimension.DimensionName+"="+dimension.Value)
		}
		parts = append(parts, "维度："+strings.Join(values, "、"))
	}
	if len(parts) == 0 {
		return question
	}
	return question + "【语义补充：" + strings.Join(parts, "；") + "】"
}

func validTokenDimensionValueSource(
	retrieval QueryTokenSemanticRetrieval,
) bool {
	switch retrieval.EntityType {
	case "LOCATION", "TIME", "PERSON", "ORGANIZATION",
		"PROPER_NOUN", "NUMBER", "DIMENSION_VALUE":
		return true
	}
	for _, candidate := range retrieval.DimensionCandidates {
		if candidate.SemanticType == "DIMENSION_VALUE" &&
			candidate.Score >= 0.5 &&
			(candidate.Value == retrieval.Token ||
				candidate.Name == retrieval.Token) {
			return true
		}
	}
	return false
}
