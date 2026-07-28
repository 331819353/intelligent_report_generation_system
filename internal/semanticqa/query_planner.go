package semanticqa

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"intelligent-report-generation-system/internal/platform/database"
)

type resolvedGraphNode struct {
	NodeKey     string
	SubjectRef  string
	Label       string
	PayloadHash string
	Payload     json.RawMessage
}

type metricGraphPayload struct {
	MetricID         string `json:"metricId"`
	MetricVersionID  string `json:"metricVersionId"`
	DatasetVersionID string `json:"datasetVersionId"`
}

type scopedMemberMatch struct {
	MemberValue          string
	DimensionID          string
	DimensionCode        string
	DimensionName        string
	DimensionFieldID     string
	DimensionDescription string
	MatchedValue         string
	MatchMethod          string
	SetMapped            bool
	Sensitive            bool
}

type metricScopedQuestionResolution struct {
	DimensionCode  string
	MemberValue    string
	MemberFilters  []QueryMemberFilterInput
	CandidateCount int
	FailureCode    string
	Trace          []QueryDimensionValueLookupTrace
}

// PreviewMetricDimensionLookups performs the same metric-scoped governed
// member resolution used by PlanQuery, but without creating a plan. The
// service uses the resulting governed field description and member term as
// bounded vector queries before the immutable plan is persisted.
func (store *PostgresStore) PreviewMetricDimensionLookups(
	ctx context.Context,
	tenantID, metricCode, question string,
) (result []QueryDimensionValueLookupTrace, err error) {
	result = []QueryDimensionValueLookupTrace{}
	err = database.WithTenantTx(ctx, store.pool, tenantID, func(tx pgx.Tx) error {
		var minimumConfidence float64
		if err := tx.QueryRow(ctx, `SELECT minimum_path_confidence::float8
			FROM platform.semantic_qa_settings
			WHERE tenant_id=platform.current_tenant_id()`,
		).Scan(&minimumConfidence); err != nil {
			return err
		}
		var generationID string
		if err := tx.QueryRow(ctx, `SELECT generation.id::text
			FROM platform.semantic_graph_projection_state AS state
			JOIN platform.semantic_graph_generations AS generation
			  ON generation.id=state.current_generation_id
			 AND generation.tenant_id=state.tenant_id
			WHERE state.tenant_id=platform.current_tenant_id()
			  AND state.status='READY'
			  AND state.applied_event_version=state.requested_event_version
			  AND generation.status='READY'`,
		).Scan(&generationID); err != nil {
			return err
		}
		metrics, err := graphNodesByCode(
			ctx, tx, generationID, "METRIC", metricCode,
		)
		if err != nil || len(metrics) != 1 {
			return err
		}
		var payload metricGraphPayload
		if err := json.Unmarshal(metrics[0].Payload, &payload); err != nil {
			return err
		}
		resolution, err := resolveMetricScopedQuestion(
			ctx, tx, generationID, metrics[0], payload,
			question, "", minimumConfidence,
		)
		if err != nil {
			return err
		}
		result = resolution.Trace
		var metricName, metricFieldID, materializationID string
		var tableSchema, tableName string
		if err := tx.QueryRow(ctx, `SELECT metric.name,
				COALESCE(version.definition_json#>>'{expression,fieldId}',''),
				materialization.id::text,materialization.published_schema,
				materialization.published_name
			FROM platform.metric_versions AS version
			JOIN platform.metrics AS metric
			  ON metric.tenant_id=version.tenant_id
			 AND metric.id=version.metric_id
			JOIN platform.dataset_materializations AS materialization
			  ON materialization.tenant_id=version.tenant_id
			 AND materialization.dataset_id=version.dataset_id
			 AND materialization.dataset_version_id=version.dataset_version_id
			 AND materialization.layer='DWS'
			 AND materialization.status='ACTIVE'
			WHERE version.id=$1::uuid
			  AND version.metric_id=$2::uuid
			  AND version.dataset_version_id=$3::uuid
			  AND version.status='PUBLISHED'
			ORDER BY materialization.activated_at DESC,materialization.id
			LIMIT 1`,
			payload.MetricVersionID, payload.MetricID, payload.DatasetVersionID,
		).Scan(
			&metricName, &metricFieldID, &materializationID,
			&tableSchema, &tableName,
		); err != nil {
			return err
		}
		for index := range result {
			result[index].MetricCode = metricCode
			result[index].MetricName = metricName
			result[index].MetricFieldID = metricFieldID
			result[index].MetricVersionID = payload.MetricVersionID
			result[index].DatasetVersionID = payload.DatasetVersionID
			result[index].MaterializationID = materializationID
			result[index].TableSchema = tableSchema
			result[index].TableName = tableName
		}
		return nil
	})
	return result, err
}

func (store *PostgresStore) PlanQuery(
	ctx context.Context,
	tenantID, actorID string,
	input QueryPlanInput,
	questionHash string,
) (plan QueryPlan, err error) {
	err = database.WithTenantTx(ctx, store.pool, tenantID, func(tx pgx.Tx) error {
		var enabled bool
		var minimumConfidence float64
		var maximumHops int
		if err := tx.QueryRow(ctx, `SELECT enabled,minimum_path_confidence::float8,
				maximum_path_hops
			FROM platform.semantic_qa_settings
			WHERE tenant_id=platform.current_tenant_id()`).
			Scan(&enabled, &minimumConfidence, &maximumHops); err != nil {
			return err
		}
		if !enabled {
			return ErrDisabled
		}
		if input.MaximumPathHops > 0 && input.MaximumPathHops < maximumHops {
			maximumHops = input.MaximumPathHops
		}
		var generationStatus string
		if err := tx.QueryRow(ctx, `SELECT generation.id::text,generation.generation,
				generation.status
			FROM platform.semantic_graph_projection_state AS state
			JOIN platform.semantic_graph_generations AS generation
			  ON generation.id=state.current_generation_id
			 AND generation.tenant_id=state.tenant_id
			WHERE state.tenant_id=platform.current_tenant_id()
			  AND state.status='READY'
			  AND state.applied_event_version=state.requested_event_version`).
			Scan(
				&plan.GraphGenerationID, &plan.GraphGeneration, &generationStatus,
			); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrGraphNotReady
			}
			return err
		}
		if generationStatus != "READY" {
			return ErrGraphNotReady
		}
		plan.QuestionHash = questionHash
		plan.Intent = input.Intent
		plan.Status = "GAP"
		plan.FailureCode = "METRIC_NOT_FOUND"
		plan.Evidence = []QueryEvidence{}
		plan.PlanningTrace = append(
			[]QueryDimensionValueLookupTrace(nil),
			input.DimensionValueLookups...,
		)
		if input.MetricMatchMethod == "" {
			input.MetricMatchMethod = "EXPLICIT_CODE"
		}
		intentDecision := "DETERMINISTIC_INTENT"
		switch input.MetricMatchMethod {
		case "CATALOG_RERANK":
			intentDecision = "CATALOG_CONSTRAINED_INTERPRETATION"
		case "DECISION_GRAPH":
			intentDecision = "DIMENSION_DECISION_GRAPH_BACKED_INTENT"
		case "EXPLICIT_CODE":
			intentDecision = "CALLER_STRUCTURED_INTENT"
		case "CONTEXT":
			intentDecision = "CONTEXT_WITH_CURRENT_TURN_INTENT"
		}
		plan.Resolution = []QueryResolutionStep{{
			Stage: "INTENT_RECOGNITION", Status: "RESOLVED",
			SelectedCode: input.Intent, Decision: intentDecision,
		}}

		metricCandidates, err := graphNodesByCode(
			ctx, tx, plan.GraphGenerationID, "METRIC", input.MetricCode,
		)
		if err != nil {
			return err
		}
		if len(metricCandidates) > 1 {
			plan.Resolution = append(plan.Resolution, QueryResolutionStep{
				Stage: "METRIC_CATALOG", Status: "AMBIGUOUS",
				CandidateCount: len(metricCandidates), Decision: "EXACT_PUBLISHED_CODE",
			})
			plan.Status, plan.FailureCode = "AMBIGUOUS", "METRIC_AMBIGUOUS"
			return persistQueryPlan(ctx, tx, actorID, input, &plan)
		}
		if len(metricCandidates) == 0 {
			plan.Resolution = append(plan.Resolution, QueryResolutionStep{
				Stage: "METRIC_CATALOG", Status: "NOT_FOUND",
				CandidateCount: input.MetricCandidateCount,
				Decision:       input.MetricMatchMethod,
			})
			return persistQueryPlan(ctx, tx, actorID, input, &plan)
		}
		metricNode := metricCandidates[0]
		metricCandidateCount := input.MetricCandidateCount
		if metricCandidateCount == 0 {
			metricCandidateCount = len(metricCandidates)
		}
		plan.Resolution = append(plan.Resolution, QueryResolutionStep{
			Stage: "METRIC_CATALOG", Status: "RESOLVED",
			CandidateCount: metricCandidateCount, SelectedCode: input.MetricCode,
			Decision: input.MetricMatchMethod,
		})
		var metricPayload metricGraphPayload
		if err := json.Unmarshal(metricNode.Payload, &metricPayload); err != nil {
			return err
		}
		plan.SelectedMetricID = metricPayload.MetricID
		plan.SelectedMetricVersionID = metricPayload.MetricVersionID
		plan.SelectedDatasetVersionID = metricPayload.DatasetVersionID
		var selectedDomain string
		if err := tx.QueryRow(ctx, `SELECT
				platform.dataset_version_effective_domain(id)
			FROM platform.dataset_versions
			WHERE id=$1::uuid AND status='PUBLISHED'`,
			metricPayload.DatasetVersionID,
		).Scan(&selectedDomain); err != nil {
			return err
		}
		domainStep := QueryResolutionStep{
			Stage: "DOMAIN_CATALOG", Status: "RESOLVED",
			CandidateCount: 1, SelectedCode: selectedDomain,
			Decision: "DOMAIN_SCOPED_HYBRID_RECALL",
		}
		if input.Domain != "" && !strings.EqualFold(input.Domain, selectedDomain) {
			domainStep.Status = "REJECTED"
			domainStep.Decision = "METRIC_DOMAIN_MISMATCH"
			plan.Resolution = append(
				plan.Resolution[:1],
				append([]QueryResolutionStep{domainStep}, plan.Resolution[1:]...)...,
			)
			plan.Status, plan.FailureCode = "REJECTED", "METRIC_DOMAIN_MISMATCH"
			return persistQueryPlan(ctx, tx, actorID, input, &plan)
		}
		input.Domain = selectedDomain
		plan.Resolution = append(
			plan.Resolution[:1],
			append([]QueryResolutionStep{domainStep}, plan.Resolution[1:]...)...,
		)
		plan.Conditions = QueryConditionDocument{
			Domain: selectedDomain, MetricCode: input.MetricCode,
			MetricVersionID:  metricPayload.MetricVersionID,
			DatasetVersionID: metricPayload.DatasetVersionID,
			Dimensions:       []QueryDimensionClause{}, TimeRange: input.TimeRange,
		}
		autoMemberCandidateCount := 0
		if input.MemberValue == "" &&
			!input.DimensionResolutionComplete &&
			strings.TrimSpace(input.Question) != "" {
			scoped, err := resolveMetricScopedQuestion(
				ctx, tx, plan.GraphGenerationID, metricNode,
				metricPayload, input.Question, input.DimensionCode,
				minimumConfidence,
			)
			if err != nil {
				return err
			}
			autoMemberCandidateCount = scoped.CandidateCount
			plan.PlanningTrace = append(plan.PlanningTrace, scoped.Trace...)
			if scoped.FailureCode != "" {
				plan.Resolution = append(plan.Resolution, QueryResolutionStep{
					Stage: "DIMENSION_MEMBER", Status: "AMBIGUOUS",
					CandidateCount: scoped.CandidateCount,
					Decision:       "METRIC_SCOPED_EXACT_MATCH",
				})
				plan.Status, plan.FailureCode = "AMBIGUOUS", scoped.FailureCode
				return persistQueryPlan(ctx, tx, actorID, input, &plan)
			}
			if len(scoped.MemberFilters) > 0 {
				input.MemberFilters = mergeQueryMemberFilters(
					input.MemberFilters, scoped.MemberFilters,
				)
			} else if scoped.MemberValue != "" {
				input.DimensionCode = scoped.DimensionCode
				input.MemberValue = scoped.MemberValue
			} else if input.DimensionCode == "" {
				input.DimensionCode = scoped.DimensionCode
			}
			input.MemberFilters, err = normalizeQueryMemberFilters(
				input.DimensionCode, input.MemberValue, input.MemberFilters,
			)
			if err != nil {
				return err
			}
		}
		var metricTimeFieldID, metricTimeFieldType string
		if err := tx.QueryRow(ctx, `SELECT
				COALESCE(
				  metric_version.definition_json#>>'{expression,fieldId}',''
				),
				COALESCE(metric_version.definition_json->>'timeFieldId',''),
				COALESCE(time_field.canonical_type,'')
			FROM platform.metric_versions AS metric_version
			LEFT JOIN platform.dataset_fields AS time_field
			  ON time_field.tenant_id=metric_version.tenant_id
			 AND time_field.dataset_version_id=metric_version.dataset_version_id
			 AND time_field.field_id=
			   COALESCE(metric_version.definition_json->>'timeFieldId','')
			WHERE metric_version.id=$1::uuid
			  AND metric_version.metric_id=$2::uuid
			  AND metric_version.dataset_version_id=$3::uuid
			  AND metric_version.status='PUBLISHED'`,
			metricPayload.MetricVersionID, metricPayload.MetricID,
			metricPayload.DatasetVersionID,
		).Scan(
			&plan.MetricFieldID, &metricTimeFieldID, &metricTimeFieldType,
		); err != nil {
			return err
		}
		if (input.TimeRange != nil || input.TimePreset != "" ||
			input.ComparisonMode != "" || input.ComparisonRange != nil ||
			input.Intent == "TREND" || input.Intent == "COMPARISON") &&
			metricTimeFieldID == "" {
			plan.Status, plan.FailureCode = "GAP", "METRIC_TIME_FIELD_NOT_AVAILABLE"
			return persistQueryPlan(ctx, tx, actorID, input, &plan)
		}
		if input.TimePreset != "" {
			timeRange, err := resolveQueryTimePreset(
				input.TimePreset, input.Timezone, metricTimeFieldType, time.Now(),
			)
			if err != nil {
				plan.Status, plan.FailureCode = "GAP", "TIME_PRESET_NOT_APPLICABLE"
				return persistQueryPlan(ctx, tx, actorID, input, &plan)
			}
			input.TimeRange = &timeRange
		}
		if input.TimeRange != nil {
			_, dateOnly, err := parseQueryBoundary(input.TimeRange.Start)
			if err != nil ||
				(metricTimeFieldType == "DATE") != dateOnly ||
				!oneOf(metricTimeFieldType, "DATE", "DATETIME") {
				plan.Status, plan.FailureCode = "GAP", "TIME_RANGE_TYPE_MISMATCH"
				return persistQueryPlan(ctx, tx, actorID, input, &plan)
			}
		}
		if input.Intent == "COMPARISON" {
			if input.TimeRange == nil {
				plan.Status, plan.FailureCode = "GAP", "COMPARISON_CURRENT_WINDOW_REQUIRED"
				return persistQueryPlan(ctx, tx, actorID, input, &plan)
			}
			if input.ComparisonMode == "" {
				plan.Status, plan.FailureCode = "GAP", "COMPARISON_MODE_REQUIRED"
				return persistQueryPlan(ctx, tx, actorID, input, &plan)
			}
			if input.ComparisonMode != "CUSTOM" {
				comparisonRange, err := deriveQueryComparisonRange(
					*input.TimeRange, input.ComparisonMode, input.TimePreset,
					input.Timezone, metricTimeFieldType,
				)
				if err != nil {
					plan.Status, plan.FailureCode = "GAP", "COMPARISON_WINDOW_NOT_APPLICABLE"
					return persistQueryPlan(ctx, tx, actorID, input, &plan)
				}
				input.ComparisonRange = &comparisonRange
			}
			_, dateOnly, err := parseQueryBoundary(input.ComparisonRange.Start)
			if err != nil ||
				(metricTimeFieldType == "DATE") != dateOnly ||
				!oneOf(metricTimeFieldType, "DATE", "DATETIME") {
				plan.Status, plan.FailureCode = "GAP", "COMPARISON_RANGE_TYPE_MISMATCH"
				return persistQueryPlan(ctx, tx, actorID, input, &plan)
			}
		}

		var memberNode, dimensionNode *resolvedGraphNode
		if input.MemberValue != "" {
			memberCandidates, err := graphMemberNodes(
				ctx, tx, plan.GraphGenerationID,
				strings.ToLower(strings.TrimSpace(input.MemberValue)),
				input.DimensionCode, metricNode.NodeKey,
				metricPayload.DatasetVersionID, minimumConfidence,
			)
			if err != nil {
				return err
			}
			if len(memberCandidates) > 1 {
				plan.Resolution = append(plan.Resolution, QueryResolutionStep{
					Stage: "DIMENSION_MEMBER", Status: "AMBIGUOUS",
					CandidateCount: len(memberCandidates),
					Decision:       "METRIC_SCOPED_EXACT_MATCH",
				})
				plan.Status, plan.FailureCode = "AMBIGUOUS", "MEMBER_AMBIGUOUS"
				return persistQueryPlan(ctx, tx, actorID, input, &plan)
			}
			if len(memberCandidates) == 0 {
				plan.Resolution = append(plan.Resolution, QueryResolutionStep{
					Stage: "DIMENSION_MEMBER", Status: "NOT_FOUND",
					Decision: "METRIC_SCOPED_EXACT_MATCH",
				})
				plan.Status, plan.FailureCode = "GAP", "MEMBER_NOT_FOUND"
				return persistQueryPlan(ctx, tx, actorID, input, &plan)
			}
			memberNode = &memberCandidates[0]
			var payload struct {
				DimensionID string `json:"dimensionId"`
			}
			if err := json.Unmarshal(memberNode.Payload, &payload); err != nil {
				return err
			}
			dimension, err := graphNodeByKey(
				ctx, tx, plan.GraphGenerationID, "dimension:"+payload.DimensionID,
			)
			if err != nil {
				return err
			}
			dimensionNode = &dimension
			plan.SelectedDimensionID = payload.DimensionID
		} else if input.DimensionCode != "" {
			dimensionCandidates, err := graphMetricDimensionNodesByCode(
				ctx, tx, plan.GraphGenerationID, metricNode.NodeKey,
				metricPayload.DatasetVersionID, input.DimensionCode,
				minimumConfidence,
			)
			if err != nil {
				return err
			}
			if len(dimensionCandidates) > 1 {
				plan.Resolution = append(plan.Resolution, QueryResolutionStep{
					Stage: "DIMENSION_MEMBER", Status: "AMBIGUOUS",
					CandidateCount: len(dimensionCandidates),
					SelectedCode:   input.DimensionCode,
					Decision:       "METRIC_SCOPED_DIMENSION",
				})
				plan.Status, plan.FailureCode = "AMBIGUOUS", "DIMENSION_AMBIGUOUS"
				return persistQueryPlan(ctx, tx, actorID, input, &plan)
			}
			if len(dimensionCandidates) == 0 {
				plan.Resolution = append(plan.Resolution, QueryResolutionStep{
					Stage: "DIMENSION_MEMBER", Status: "NOT_FOUND",
					SelectedCode: input.DimensionCode,
					Decision:     "METRIC_SCOPED_DIMENSION",
				})
				plan.Status, plan.FailureCode = "GAP", "DIMENSION_NOT_FOUND"
				return persistQueryPlan(ctx, tx, actorID, input, &plan)
			}
			dimensionNode = &dimensionCandidates[0]
			plan.SelectedDimensionID = dimensionNode.SubjectRef
		}
		type resolvedMemberFilter struct {
			members   []resolvedGraphNode
			dimension resolvedGraphNode
		}
		resolvedMemberFilters := make(
			[]resolvedMemberFilter, 0, len(input.MemberFilters),
		)
		seenFilterDimensions := map[string]bool{}
		if memberNode != nil && dimensionNode != nil {
			seenFilterDimensions[dimensionNode.SubjectRef] = true
		}
		for _, memberFilter := range input.MemberFilters {
			memberValues := memberFilter.MemberValues
			if len(memberValues) == 0 && memberFilter.MemberValue != "" {
				memberValues = []string{memberFilter.MemberValue}
			}
			members := make([]resolvedGraphNode, 0, len(memberValues))
			filterDimensionID := ""
			for _, memberValue := range memberValues {
				memberCandidates, err := graphMemberNodes(
					ctx, tx, plan.GraphGenerationID,
					strings.ToLower(strings.TrimSpace(memberValue)),
					memberFilter.DimensionCode, metricNode.NodeKey,
					metricPayload.DatasetVersionID, minimumConfidence,
				)
				if err != nil {
					return err
				}
				if len(memberCandidates) > 1 {
					plan.Resolution = append(plan.Resolution, QueryResolutionStep{
						Stage: "DIMENSION_MEMBER", Status: "AMBIGUOUS",
						CandidateCount: len(memberCandidates),
						SelectedCode:   memberFilter.DimensionCode,
						Decision:       "METRIC_SCOPED_FILTER",
					})
					plan.Status, plan.FailureCode = "AMBIGUOUS", "FILTER_MEMBER_AMBIGUOUS"
					return persistQueryPlan(ctx, tx, actorID, input, &plan)
				}
				if len(memberCandidates) == 0 {
					plan.Resolution = append(plan.Resolution, QueryResolutionStep{
						Stage: "DIMENSION_MEMBER", Status: "NOT_FOUND",
						SelectedCode: memberFilter.DimensionCode,
						Decision:     "METRIC_SCOPED_FILTER",
					})
					plan.Status, plan.FailureCode = "GAP", "FILTER_MEMBER_NOT_FOUND"
					return persistQueryPlan(ctx, tx, actorID, input, &plan)
				}
				var payload struct {
					DimensionID string `json:"dimensionId"`
				}
				if err := json.Unmarshal(
					memberCandidates[0].Payload, &payload,
				); err != nil {
					return err
				}
				if payload.DimensionID == "" ||
					(filterDimensionID != "" &&
						filterDimensionID != payload.DimensionID) {
					plan.Status, plan.FailureCode =
						"REJECTED", "FILTER_SET_DIMENSION_MISMATCH"
					return persistQueryPlan(ctx, tx, actorID, input, &plan)
				}
				filterDimensionID = payload.DimensionID
				members = append(members, memberCandidates[0])
			}
			if filterDimensionID == "" ||
				seenFilterDimensions[filterDimensionID] {
				plan.Status, plan.FailureCode = "REJECTED", "FILTER_DIMENSION_DUPLICATED"
				return persistQueryPlan(ctx, tx, actorID, input, &plan)
			}
			filterDimension, err := graphNodeByKey(
				ctx, tx, plan.GraphGenerationID,
				"dimension:"+filterDimensionID,
			)
			if err != nil {
				plan.Status, plan.FailureCode = "GAP", "FILTER_DIMENSION_NOT_FOUND"
				return persistQueryPlan(ctx, tx, actorID, input, &plan)
			}
			seenFilterDimensions[filterDimensionID] = true
			resolvedMemberFilters = append(
				resolvedMemberFilters,
				resolvedMemberFilter{
					members: members, dimension: filterDimension,
				},
			)
		}
		if input.TopN > 0 && dimensionNode == nil {
			plan.Status, plan.FailureCode = "GAP", "TOP_N_DIMENSION_REQUIRED"
			return persistQueryPlan(ctx, tx, actorID, input, &plan)
		}
		memberFilterCount := len(resolvedMemberFilters)
		if memberNode != nil {
			memberFilterCount++
		}
		switch {
		case memberFilterCount > 0:
			if autoMemberCandidateCount == 0 {
				autoMemberCandidateCount = memberFilterCount
			}
			plan.Resolution = append(plan.Resolution, QueryResolutionStep{
				Stage: "DIMENSION_MEMBER", Status: "RESOLVED",
				CandidateCount: autoMemberCandidateCount,
				SelectedCode:   input.DimensionCode,
				Decision:       "METRIC_SCOPED_EXACT_MATCH",
			})
		case dimensionNode != nil:
			plan.Resolution = append(plan.Resolution, QueryResolutionStep{
				Stage: "DIMENSION_MEMBER", Status: "RESOLVED",
				CandidateCount: 1, SelectedCode: input.DimensionCode,
				Decision: "METRIC_SCOPED_DIMENSION",
			})
		default:
			plan.Resolution = append(plan.Resolution, QueryResolutionStep{
				Stage: "DIMENSION_MEMBER", Status: "SKIPPED",
				Decision: "NO_DIMENSION_CONSTRAINT",
			})
		}
		if memberNode != nil && dimensionNode != nil {
			clause, err := queryConditionClause(*dimensionNode, *memberNode)
			if err != nil {
				return err
			}
			plan.Conditions.Dimensions = append(plan.Conditions.Dimensions, clause)
		}
		for _, memberFilter := range resolvedMemberFilters {
			clause, err := queryConditionSetClause(
				memberFilter.dimension, memberFilter.members,
			)
			if err != nil {
				return err
			}
			plan.Conditions.Dimensions = append(plan.Conditions.Dimensions, clause)
		}
		sort.Slice(plan.Conditions.Dimensions, func(left, right int) bool {
			return plan.Conditions.Dimensions[left].DimensionCode <
				plan.Conditions.Dimensions[right].DimensionCode
		})
		plan.Conditions.TimeRange = input.TimeRange

		if memberNode != nil {
			plan.Evidence = append(plan.Evidence, QueryEvidence{
				NodeKey: memberNode.NodeKey, SubjectType: "MEMBER",
				SubjectRef: memberNode.SubjectRef, Label: "成员命中（值已脱敏）",
				Authority: "CONTROL_PLANE", Confidence: 1,
				EvidenceHash: memberNode.PayloadHash,
			})
		}
		if dimensionNode != nil {
			evidence, err := graphMetricDimensionEvidence(
				ctx, tx, plan.GraphGenerationID, metricNode.NodeKey,
				*dimensionNode, minimumConfidence,
			)
			if errors.Is(err, ErrNotFound) {
				plan.Status, plan.FailureCode = "REJECTED", "UNPROVEN_DIMENSION_METRIC_PATH"
				return persistQueryPlan(ctx, tx, actorID, input, &plan)
			}
			if err != nil {
				return err
			}
			plan.Evidence = append(plan.Evidence, evidence)
		}
		for _, memberFilter := range resolvedMemberFilters {
			for _, member := range memberFilter.members {
				plan.Evidence = append(plan.Evidence, QueryEvidence{
					NodeKey: member.NodeKey, SubjectType: "MEMBER",
					SubjectRef: member.SubjectRef,
					Label:      "成员命中（值已脱敏）",
					Authority:  "CONTROL_PLANE", Confidence: 1,
					EvidenceHash: member.PayloadHash,
				})
			}
			evidence, err := graphMetricDimensionEvidence(
				ctx, tx, plan.GraphGenerationID, metricNode.NodeKey,
				memberFilter.dimension, minimumConfidence,
			)
			if errors.Is(err, ErrNotFound) {
				plan.Status, plan.FailureCode = "REJECTED", "UNPROVEN_FILTER_METRIC_PATH"
				return persistQueryPlan(ctx, tx, actorID, input, &plan)
			}
			if err != nil {
				return err
			}
			plan.Evidence = append(plan.Evidence, evidence)
		}
		plan.Evidence = append(plan.Evidence, QueryEvidence{
			NodeKey: metricNode.NodeKey, SubjectType: "METRIC",
			SubjectRef: metricNode.SubjectRef, Label: metricNode.Label,
			Authority: "CONTROL_PLANE", Confidence: 1,
			EvidenceHash: metricNode.PayloadHash,
		})
		datasetNode, err := graphNodeByKey(
			ctx, tx, plan.GraphGenerationID,
			"dataset_version:"+metricPayload.DatasetVersionID,
		)
		if err != nil {
			plan.Resolution = append(plan.Resolution, QueryResolutionStep{
				Stage: "DATASET_LOCK", Status: "NOT_FOUND",
				SelectedCode: metricPayload.DatasetVersionID,
				Decision:     "PUBLISHED_METRIC_VERSION_BINDING",
			})
			plan.Status, plan.FailureCode = "GAP", "DATASET_NOT_FOUND"
			return persistQueryPlan(ctx, tx, actorID, input, &plan)
		}
		var datasetEdgeHash, datasetEdgeAuthority string
		var datasetConfidence float64
		err = tx.QueryRow(ctx, `SELECT evidence_hash,authority,confidence::float8
			FROM platform.semantic_graph_edges
			WHERE generation_id=$1::uuid AND from_node_key=$2 AND to_node_key=$3
			  AND relation_type='METRIC_DATASET'`,
			plan.GraphGenerationID, metricNode.NodeKey, datasetNode.NodeKey,
		).Scan(&datasetEdgeHash, &datasetEdgeAuthority, &datasetConfidence)
		if errors.Is(err, pgx.ErrNoRows) {
			plan.Resolution = append(plan.Resolution, QueryResolutionStep{
				Stage: "DATASET_LOCK", Status: "NOT_FOUND",
				SelectedCode: metricPayload.DatasetVersionID,
				Decision:     "METRIC_DATASET_EDGE_NOT_PROVEN",
			})
			plan.Status, plan.FailureCode = "REJECTED", "METRIC_DATASET_NOT_PROVEN"
			return persistQueryPlan(ctx, tx, actorID, input, &plan)
		}
		if err != nil {
			return err
		}
		plan.Evidence = append(plan.Evidence, QueryEvidence{
			NodeKey: datasetNode.NodeKey, RelationType: "METRIC_DATASET",
			SubjectType: "DATASET_VERSION", SubjectRef: datasetNode.SubjectRef,
			Label: datasetNode.Label, Authority: datasetEdgeAuthority,
			Confidence: datasetConfidence, EvidenceHash: datasetEdgeHash,
		})
		materializationNode, materializationEvidence, err := graphMaterializationEvidence(
			ctx, tx, plan.GraphGenerationID, datasetNode.NodeKey,
		)
		if err != nil {
			if errors.Is(err, ErrNotFound) {
				plan.Resolution = append(plan.Resolution, QueryResolutionStep{
					Stage: "DATASET_LOCK", Status: "NOT_FOUND",
					SelectedCode: metricPayload.DatasetVersionID,
					Decision:     "ACTIVE_MATERIALIZATION_NOT_PROVEN",
				})
				plan.Status, plan.FailureCode = "REJECTED", "ACTIVE_MATERIALIZATION_NOT_PROVEN"
				return persistQueryPlan(ctx, tx, actorID, input, &plan)
			}
			return err
		}
		plan.SelectedMaterializationID = materializationNode.SubjectRef
		plan.Evidence = append(plan.Evidence, materializationEvidence)
		plan.Resolution = append(plan.Resolution, QueryResolutionStep{
			Stage: "DATASET_LOCK", Status: "RESOLVED",
			CandidateCount: 1, SelectedCode: metricPayload.DatasetVersionID,
			Decision: "PUBLISHED_METRIC_VERSION_BINDING",
		})
		lineage, err := graphLineageEvidence(
			ctx, tx, plan.GraphGenerationID, datasetNode.NodeKey, maximumHops,
		)
		if err != nil {
			return err
		}
		plan.Evidence = append(plan.Evidence, lineage...)
		if !hasSourceEvidence(plan.Evidence) {
			plan.Status, plan.FailureCode = "REJECTED", "SOURCE_LINEAGE_NOT_PROVEN"
			return persistQueryPlan(ctx, tx, actorID, input, &plan)
		}
		for index := range plan.Evidence {
			plan.Evidence[index].Index = index
		}
		pathHash, err := hashJSON(plan.Evidence)
		if err != nil {
			return err
		}
		plan.PathHash = pathHash
		plan.Status, plan.FailureCode = "READY", ""
		plan.Confidence = 1
		for _, evidence := range plan.Evidence {
			if evidence.Confidence < plan.Confidence {
				plan.Confidence = evidence.Confidence
			}
		}
		return persistQueryPlan(ctx, tx, actorID, input, &plan)
	})
	return plan, err
}

func queryConditionClause(
	dimension resolvedGraphNode,
	member resolvedGraphNode,
) (QueryDimensionClause, error) {
	var dimensionPayload struct {
		DimensionID string `json:"dimensionId"`
		Code        string `json:"code"`
	}
	var memberPayload struct {
		DimensionID string `json:"dimensionId"`
		MemberKey   string `json:"memberKey"`
	}
	if err := json.Unmarshal(dimension.Payload, &dimensionPayload); err != nil {
		return QueryDimensionClause{}, err
	}
	if err := json.Unmarshal(member.Payload, &memberPayload); err != nil {
		return QueryDimensionClause{}, err
	}
	if dimensionPayload.DimensionID == "" ||
		dimensionPayload.DimensionID != memberPayload.DimensionID ||
		dimensionPayload.Code == "" || memberPayload.MemberKey == "" {
		return QueryDimensionClause{}, ErrUnprovenPath
	}
	return QueryDimensionClause{
		DimensionCode: dimensionPayload.Code,
		DimensionID:   dimensionPayload.DimensionID,
		MemberKey:     memberPayload.MemberKey,
	}, nil
}

func queryConditionSetClause(
	dimension resolvedGraphNode,
	members []resolvedGraphNode,
) (QueryDimensionClause, error) {
	if len(members) == 0 {
		return QueryDimensionClause{}, ErrUnprovenPath
	}
	first, err := queryConditionClause(dimension, members[0])
	if err != nil {
		return QueryDimensionClause{}, err
	}
	keys := make([]string, 0, len(members))
	seen := map[string]bool{}
	for _, member := range members {
		clause, err := queryConditionClause(dimension, member)
		if err != nil ||
			clause.DimensionID != first.DimensionID ||
			clause.DimensionCode != first.DimensionCode {
			return QueryDimensionClause{}, ErrUnprovenPath
		}
		if !seen[clause.MemberKey] {
			seen[clause.MemberKey] = true
			keys = append(keys, clause.MemberKey)
		}
	}
	sort.Strings(keys)
	if len(keys) == 1 {
		first.MemberKey = keys[0]
		return first, nil
	}
	first.MemberKey = ""
	first.MemberKeys = keys
	return first, nil
}

func graphMaterializationEvidence(
	ctx context.Context,
	tx pgx.Tx,
	generationID, datasetNodeKey string,
) (resolvedGraphNode, QueryEvidence, error) {
	var node resolvedGraphNode
	var evidence QueryEvidence
	rows, err := tx.Query(ctx, `SELECT materialization.node_key,
			materialization.subject_ref,materialization.label,
			materialization.payload_hash,materialization.payload_json,
			edge.authority,edge.confidence::float8,edge.evidence_hash
		FROM platform.semantic_graph_edges AS edge
		JOIN platform.semantic_graph_nodes AS materialization
		  ON materialization.tenant_id=edge.tenant_id
		 AND materialization.generation_id=edge.generation_id
		 AND materialization.node_key=edge.to_node_key
		 AND materialization.node_type='MATERIALIZATION'
		WHERE edge.generation_id=$1::uuid
		  AND edge.from_node_key=$2
		  AND edge.relation_type='DATASET_MATERIALIZED_AS'
		ORDER BY materialization.node_key
		LIMIT 2`, generationID, datasetNodeKey)
	if err != nil {
		return node, evidence, err
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		count++
		var payload []byte
		if err := rows.Scan(
			&node.NodeKey, &node.SubjectRef, &node.Label, &node.PayloadHash,
			&payload, &evidence.Authority, &evidence.Confidence,
			&evidence.EvidenceHash,
		); err != nil {
			return node, evidence, err
		}
		node.Payload = append(json.RawMessage(nil), payload...)
	}
	if err := rows.Err(); err != nil {
		return node, evidence, err
	}
	if count == 0 {
		return node, evidence, ErrNotFound
	}
	if count != 1 {
		return resolvedGraphNode{}, QueryEvidence{}, ErrUnprovenPath
	}
	evidence.NodeKey = node.NodeKey
	evidence.RelationType = "DATASET_MATERIALIZED_AS"
	evidence.SubjectType = "MATERIALIZATION"
	evidence.SubjectRef = node.SubjectRef
	evidence.Label = node.Label
	return node, evidence, nil
}

func graphNodesByCode(
	ctx context.Context,
	tx pgx.Tx,
	generationID, nodeType, code string,
) ([]resolvedGraphNode, error) {
	rows, err := tx.Query(ctx, `SELECT node_key,subject_ref,label,payload_hash,payload_json
		FROM platform.semantic_graph_nodes
		WHERE generation_id=$1::uuid AND node_type=$2
		  AND lower(payload_json->>'code')=lower($3)
		ORDER BY node_key LIMIT 2`, generationID, nodeType, code)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []resolvedGraphNode{}
	for rows.Next() {
		var node resolvedGraphNode
		var payload []byte
		if err := rows.Scan(
			&node.NodeKey, &node.SubjectRef, &node.Label,
			&node.PayloadHash, &payload,
		); err != nil {
			return nil, err
		}
		node.Payload = append(json.RawMessage(nil), payload...)
		result = append(result, node)
	}
	return result, rows.Err()
}

func graphMetricDimensionNodesByCode(
	ctx context.Context,
	tx pgx.Tx,
	generationID, metricNodeKey, datasetVersionID, code string,
	minimumConfidence float64,
) ([]resolvedGraphNode, error) {
	rows, err := tx.Query(ctx, `SELECT dimension.node_key,
			dimension.subject_ref,dimension.label,
			dimension.payload_hash,dimension.payload_json
		FROM platform.semantic_graph_edges AS compatibility
		JOIN platform.semantic_graph_nodes AS dimension
		  ON dimension.tenant_id=compatibility.tenant_id
		 AND dimension.generation_id=compatibility.generation_id
		 AND dimension.node_key=compatibility.to_node_key
		 AND dimension.node_type='DIMENSION'
		WHERE compatibility.generation_id=$1::uuid
		  AND compatibility.from_node_key=$2
		  AND compatibility.relation_type='METRIC_DIMENSION'
		  AND compatibility.confidence>=$5
		  AND dimension.payload_json->>'datasetVersionId'=$3
		  AND lower(dimension.payload_json->>'code')=lower($4)
		ORDER BY dimension.node_key
		LIMIT 2`,
		generationID, metricNodeKey, datasetVersionID, code, minimumConfidence,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []resolvedGraphNode{}
	for rows.Next() {
		var node resolvedGraphNode
		var payload []byte
		if err := rows.Scan(
			&node.NodeKey, &node.SubjectRef, &node.Label,
			&node.PayloadHash, &payload,
		); err != nil {
			return nil, err
		}
		node.Payload = append(json.RawMessage(nil), payload...)
		result = append(result, node)
	}
	return result, rows.Err()
}

func resolveMetricScopedQuestion(
	ctx context.Context,
	tx pgx.Tx,
	generationID string,
	metricNode resolvedGraphNode,
	metric metricGraphPayload,
	question, requestedDimensionCode string,
	minimumConfidence float64,
) (metricScopedQuestionResolution, error) {
	result := metricScopedQuestionResolution{MemberFilters: []QueryMemberFilterInput{}}
	tokens := memberLookupTokens(question, metricNode.Label)
	if len(tokens) > 0 {
		matches, err := metricScopedMemberMatches(
			ctx, tx, generationID, metricNode.NodeKey,
			metric.DatasetVersionID, requestedDimensionCode,
			minimumConfidence, tokens, question,
		)
		if err != nil {
			return result, err
		}
		result.CandidateCount = len(matches)
		if len(matches) > maxSemanticMemberSetSize {
			result.Trace = buildDimensionValueLookupTrace(matches, nil)
			result.FailureCode = "MEMBER_AMBIGUOUS"
			return result, nil
		}
		selected, ambiguous := selectMetricScopedMemberMatches(matches, question)
		result.Trace = buildDimensionValueLookupTrace(matches, selected)
		if ambiguous {
			result.FailureCode = "MEMBER_AMBIGUOUS"
			return result, nil
		}
		if len(selected) > maxSemanticMemberSetSize {
			result.FailureCode = "MEMBER_AMBIGUOUS"
			return result, nil
		}
		if len(selected) > 0 {
			byDimension := map[string][]scopedMemberMatch{}
			dimensionOrder := []string{}
			for _, item := range selected {
				if _, exists := byDimension[item.DimensionID]; !exists {
					dimensionOrder = append(dimensionOrder, item.DimensionID)
				}
				byDimension[item.DimensionID] = append(
					byDimension[item.DimensionID], item,
				)
			}
			for _, dimensionID := range dimensionOrder {
				items := byDimension[dimensionID]
				values := make([]string, 0, len(items))
				for _, item := range items {
					values = append(values, item.MemberValue)
				}
				result.MemberFilters = append(
					result.MemberFilters,
					QueryMemberFilterInput{
						DimensionCode: items[0].DimensionCode,
						MemberValues:  values,
					},
				)
			}
			return result, nil
		}
	}
	if requestedDimensionCode != "" {
		result.DimensionCode = requestedDimensionCode
		return result, nil
	}
	dimensions, err := metricScopedDimensionsMentioned(
		ctx, tx, generationID, metricNode.NodeKey,
		metric.DatasetVersionID, question, minimumConfidence,
	)
	if err != nil {
		return result, err
	}
	result.CandidateCount = len(dimensions)
	if len(dimensions) > 1 {
		result.FailureCode = "DIMENSION_AMBIGUOUS"
		return result, nil
	}
	if len(dimensions) == 1 {
		var payload struct {
			Code string `json:"code"`
		}
		if err := json.Unmarshal(dimensions[0].Payload, &payload); err != nil {
			return result, err
		}
		result.DimensionCode = payload.Code
	}
	return result, nil
}

func metricScopedMemberMatches(
	ctx context.Context,
	tx pgx.Tx,
	generationID, metricNodeKey, datasetVersionID, dimensionCode string,
	minimumConfidence float64,
	tokens []string,
	question string,
) ([]scopedMemberMatch, error) {
	rows, err := tx.Query(ctx, `WITH tokens(value) AS (
				SELECT unnest($6::text[])
			), compatible_dimensions AS (
			SELECT dimension.id,dimension.code::text AS code,
				dimension.name,dimension.field_id,dimension.description,
				dimension.sensitive
			FROM platform.semantic_dimensions AS dimension
			JOIN platform.semantic_graph_nodes AS graph_dimension
			  ON graph_dimension.tenant_id=dimension.tenant_id
			 AND graph_dimension.generation_id=$1::uuid
			 AND graph_dimension.node_key='dimension:'||dimension.id::text
			JOIN platform.semantic_graph_edges AS compatibility
			  ON compatibility.tenant_id=dimension.tenant_id
			 AND compatibility.generation_id=graph_dimension.generation_id
			 AND compatibility.from_node_key=$2
			 AND compatibility.to_node_key=graph_dimension.node_key
			 AND compatibility.relation_type='METRIC_DIMENSION'
			 AND compatibility.confidence>=$5
			WHERE dimension.status='PUBLISHED'
				  AND dimension.dataset_version_id=$3::uuid
				  AND ($4='' OR lower(dimension.code::text)=lower($4))
			), semantic_tokens AS (
				SELECT DISTINCT matched_value,mapped_value,knowledge_type,set_mapped
				FROM (
				  SELECT tokens.value AS matched_value,
				      lower(btrim(mapped.value)) AS mapped_value,
				      lower(asset.knowledge_type) AS knowledge_type,
				      asset.mapping_value ~ '[,，]' AS set_mapped
				  FROM tokens
				  JOIN platform.semantic_term_assets AS asset
				    ON lower(asset.common_term::text)=tokens.value
				   AND asset.status='ACTIVE'
				  CROSS JOIN LATERAL regexp_split_to_table(
				    asset.mapping_value,'[[:space:]]*[,，][[:space:]]*'
				  ) AS mapped(value)
				  UNION ALL
				  SELECT lower(asset.common_term::text) AS matched_value,
				      lower(btrim(mapped.value)) AS mapped_value,
				      lower(asset.knowledge_type) AS knowledge_type,
				      asset.mapping_value ~ '[,，]' AS set_mapped
				  FROM platform.semantic_term_assets AS asset
				  CROSS JOIN LATERAL regexp_split_to_table(
				    asset.mapping_value,'[[:space:]]*[,，][[:space:]]*'
				  ) AS mapped(value)
				  WHERE asset.status='ACTIVE'
				    AND position(
				      lower(asset.common_term::text) IN lower($7)
				    )>0
				) AS mapped
			), matches AS (
			SELECT member.id,member.normalized_value,dimension.id AS dimension_id,
				dimension.code,dimension.name,dimension.field_id,
				dimension.description,
				tokens.value AS matched_value,
				false AS set_mapped,'EXACT_MEMBER'::text AS match_method,
				dimension.sensitive
			FROM tokens
			JOIN platform.dimension_members AS member
			  ON member.normalized_value=tokens.value
			JOIN compatible_dimensions AS dimension
			  ON dimension.id=member.dimension_id
			WHERE member.status='ACTIVE'
			  AND (member.valid_from IS NULL OR member.valid_from<=now())
			  AND (member.valid_to IS NULL OR member.valid_to>now())
			UNION ALL
			SELECT member.id,member.normalized_value,dimension.id AS dimension_id,
				dimension.code,dimension.name,dimension.field_id,
				dimension.description,
				tokens.value AS matched_value,
				false AS set_mapped,'MEMBER_ALIAS'::text AS match_method,
				dimension.sensitive
			FROM tokens
			JOIN platform.dimension_member_aliases AS alias
			  ON alias.normalized_alias=tokens.value
			 AND (alias.valid_from IS NULL OR alias.valid_from<=now())
			 AND (alias.valid_to IS NULL OR alias.valid_to>now())
			JOIN platform.dimension_members AS member
			  ON member.id=alias.dimension_member_id
			 AND member.dimension_id=alias.dimension_id
			JOIN compatible_dimensions AS dimension
			  ON dimension.id=member.dimension_id
				WHERE member.status='ACTIVE'
				  AND (member.valid_from IS NULL OR member.valid_from<=now())
				  AND (member.valid_to IS NULL OR member.valid_to>now())
				UNION ALL
				SELECT member.id,member.normalized_value,dimension.id AS dimension_id,
					dimension.code,dimension.name,dimension.field_id,
					dimension.description,
					semantic.matched_value,
					semantic.set_mapped,
					CASE WHEN semantic.set_mapped
					  THEN 'SEMANTIC_SET' ELSE 'SEMANTIC_MAPPING' END,
					dimension.sensitive
				FROM semantic_tokens AS semantic
				JOIN compatible_dimensions AS dimension
				  ON semantic.knowledge_type IN (
				       'dimension_member','dimension_value','维度值','维度成员'
				     )
				    OR semantic.knowledge_type=lower(dimension.code)
				    OR semantic.knowledge_type=lower(dimension.name)
				JOIN platform.dimension_members AS member
				  ON member.dimension_id=dimension.id
				 AND (
				   lower(member.normalized_value)=semantic.mapped_value
				   OR lower(member.member_key)=semantic.mapped_value
				   OR lower(member.canonical_label)=semantic.mapped_value
				 )
				WHERE member.status='ACTIVE'
				  AND (member.valid_from IS NULL OR member.valid_from<=now())
				  AND (member.valid_to IS NULL OR member.valid_to>now())
				UNION ALL
				SELECT member.id,member.normalized_value,dimension.id AS dimension_id,
					dimension.code,dimension.name,dimension.field_id,
					dimension.description,
					semantic.matched_value,
					semantic.set_mapped,
					CASE WHEN semantic.set_mapped
					  THEN 'SEMANTIC_SET_ALIAS' ELSE 'SEMANTIC_ALIAS' END,
					dimension.sensitive
				FROM semantic_tokens AS semantic
				JOIN compatible_dimensions AS dimension
				  ON semantic.knowledge_type IN (
				       'dimension_member','dimension_value','维度值','维度成员'
				     )
				    OR semantic.knowledge_type=lower(dimension.code)
				    OR semantic.knowledge_type=lower(dimension.name)
				JOIN platform.dimension_member_aliases AS alias
				  ON lower(alias.normalized_alias)=semantic.mapped_value
				 AND (alias.valid_from IS NULL OR alias.valid_from<=now())
				 AND (alias.valid_to IS NULL OR alias.valid_to>now())
				JOIN platform.dimension_members AS member
				  ON member.id=alias.dimension_member_id
				 AND member.dimension_id=alias.dimension_id
				 AND member.dimension_id=dimension.id
				WHERE member.status='ACTIVE'
				  AND (member.valid_from IS NULL OR member.valid_from<=now())
				  AND (member.valid_to IS NULL OR member.valid_to>now())
				UNION ALL
				SELECT member.id,member.normalized_value,dimension.id AS dimension_id,
					dimension.code,dimension.name,dimension.field_id,
					dimension.description,
					lower(asset.common_term::text) AS matched_value,
					true AS set_mapped,'SEMANTIC_TAG'::text AS match_method,
					dimension.sensitive
				FROM platform.semantic_term_assets AS asset
				JOIN compatible_dimensions AS dimension
				  ON lower(asset.knowledge_type) IN (
				       'dimension_member','dimension_value','维度值','维度成员'
				     )
				    OR lower(asset.knowledge_type)=lower(dimension.code)
				    OR lower(asset.knowledge_type)=lower(dimension.name)
				JOIN platform.dimension_members AS member
				  ON member.dimension_id=dimension.id
				CROSS JOIN LATERAL regexp_split_to_table(
				  member.normalized_value,
				  '[[:space:]]*[,，][[:space:]]*'
				) AS member_tag(value)
				WHERE asset.status='ACTIVE'
				  AND lower(asset.mapping_value) LIKE 'tag:%'
				  AND lower(btrim(member_tag.value))=
				      lower(btrim(substr(asset.mapping_value,5)))
				  AND (
				    EXISTS (
				      SELECT 1 FROM tokens
				      WHERE tokens.value=lower(asset.common_term::text)
				    )
				    OR position(
				      lower(asset.common_term::text) IN lower($7)
				    )>0
				  )
				  AND member.status='ACTIVE'
				  AND (member.valid_from IS NULL OR member.valid_from<=now())
				  AND (member.valid_to IS NULL OR member.valid_to>now())
			)
		SELECT DISTINCT ON(id) normalized_value,dimension_id::text,
			code,name,field_id,description,matched_value,match_method,
			set_mapped,sensitive
		FROM matches
		WHERE set_mapped
		   OR (
		     lower(matched_value)<>lower(name)
		     AND lower(matched_value)<>lower(code)
		   )
		ORDER BY id,set_mapped DESC,char_length(matched_value) DESC,matched_value
		LIMIT 129`,
		generationID, metricNodeKey, datasetVersionID, dimensionCode,
		minimumConfidence, tokens, question,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []scopedMemberMatch{}
	for rows.Next() {
		var item scopedMemberMatch
		if err := rows.Scan(
			&item.MemberValue, &item.DimensionID, &item.DimensionCode,
			&item.DimensionName, &item.DimensionFieldID,
			&item.DimensionDescription, &item.MatchedValue, &item.MatchMethod,
			&item.SetMapped, &item.Sensitive,
		); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func buildDimensionValueLookupTrace(
	matches, selected []scopedMemberMatch,
) []QueryDimensionValueLookupTrace {
	type traceGroup struct {
		item       QueryDimensionValueLookupTrace
		candidates map[string]bool
		selected   map[string]bool
	}
	selectedMatches := make(map[string]bool, len(selected))
	for _, match := range selected {
		selectedMatches[match.DimensionID+"\x00"+match.MemberValue+"\x00"+match.MatchedValue] = true
	}
	groups := map[string]*traceGroup{}
	order := []string{}
	for _, match := range matches {
		key := strings.Join([]string{
			match.MatchedValue, match.DimensionID, match.MatchMethod,
		}, "\x00")
		group, exists := groups[key]
		if !exists {
			group = &traceGroup{
				item: QueryDimensionValueLookupTrace{
					Term:                      match.MatchedValue,
					DimensionID:               match.DimensionID,
					DimensionCode:             match.DimensionCode,
					DimensionName:             match.DimensionName,
					DimensionFieldID:          match.DimensionFieldID,
					DimensionFieldName:        match.DimensionCode,
					DimensionFieldDescription: match.DimensionDescription,
					MatchMethod:               match.MatchMethod,
					Source:                    "CURRENT_TURN",
					Sensitive:                 match.Sensitive,
				},
				candidates: map[string]bool{},
				selected:   map[string]bool{},
			}
			groups[key] = group
			order = append(order, key)
		}
		group.candidates[match.MemberValue] = true
		if selectedMatches[match.DimensionID+"\x00"+match.MemberValue+"\x00"+match.MatchedValue] {
			group.selected[match.MemberValue] = true
		}
	}
	result := make([]QueryDimensionValueLookupTrace, 0, len(groups))
	for _, key := range order {
		group := groups[key]
		group.item.CandidateCount = len(group.candidates)
		group.item.Selected = len(group.selected) > 0
		if !group.item.Sensitive {
			for value := range group.candidates {
				group.item.CandidateMemberKeys = append(
					group.item.CandidateMemberKeys, value,
				)
			}
			for value := range group.selected {
				group.item.SelectedMemberKeys = append(
					group.item.SelectedMemberKeys, value,
				)
			}
			sort.Strings(group.item.CandidateMemberKeys)
			sort.Strings(group.item.SelectedMemberKeys)
		}
		result = append(result, group.item)
	}
	sort.SliceStable(result, func(left, right int) bool {
		if result[left].Term != result[right].Term {
			return result[left].Term < result[right].Term
		}
		if result[left].DimensionCode != result[right].DimensionCode {
			return result[left].DimensionCode < result[right].DimensionCode
		}
		return result[left].MatchMethod < result[right].MatchMethod
	})
	return result
}

func selectMetricScopedMemberMatches(
	matches []scopedMemberMatch,
	question string,
) ([]scopedMemberMatch, bool) {
	if len(matches) == 0 {
		return nil, false
	}
	question = strings.ToLower(question)
	byDimension := map[string][]scopedMemberMatch{}
	for _, match := range matches {
		byDimension[match.DimensionID] = append(
			byDimension[match.DimensionID], match,
		)
	}
	selected := make([]scopedMemberMatch, 0, len(matches))
	for _, candidates := range byDimension {
		sort.Slice(candidates, func(left, right int) bool {
			leftLength := utf8.RuneCountInString(candidates[left].MatchedValue)
			rightLength := utf8.RuneCountInString(candidates[right].MatchedValue)
			if leftLength != rightLength {
				return leftLength > rightLength
			}
			return candidates[left].MemberValue < candidates[right].MemberValue
		})
		longestMatchedValue := candidates[0].MatchedValue
		dimensionSelection := []scopedMemberMatch{}
		for _, candidate := range candidates {
			if candidate.MatchedValue == longestMatchedValue {
				dimensionSelection = append(dimensionSelection, candidate)
				continue
			}
			if candidate.MemberValue != candidates[0].MemberValue &&
				!strings.Contains(longestMatchedValue, candidate.MatchedValue) &&
				!strings.Contains(candidate.MatchedValue, longestMatchedValue) {
				return nil, true
			}
		}
		if len(dimensionSelection) > 1 {
			for _, candidate := range dimensionSelection {
				// One alias accidentally pointing at several members is
				// ambiguous. A comma-separated semantic mapping is the
				// explicit governed contract that authorizes a set.
				if !candidate.SetMapped {
					return nil, true
				}
			}
		}
		selected = append(selected, dimensionSelection...)
	}
	byMatchedValue := map[string][]scopedMemberMatch{}
	for _, match := range selected {
		byMatchedValue[match.MatchedValue] = append(
			byMatchedValue[match.MatchedValue], match,
		)
	}
	filtered := make([]scopedMemberMatch, 0, len(selected))
	for _, candidates := range byMatchedValue {
		dimensionIDs := map[string]bool{}
		for _, candidate := range candidates {
			dimensionIDs[candidate.DimensionID] = true
		}
		if len(dimensionIDs) == 1 {
			filtered = append(filtered, candidates...)
			continue
		}
		namedDimensionID := ""
		for _, candidate := range candidates {
			if strings.Contains(question, strings.ToLower(candidate.DimensionName)) ||
				strings.Contains(question, strings.ToLower(candidate.DimensionCode)) {
				if namedDimensionID != "" &&
					namedDimensionID != candidate.DimensionID {
					return nil, true
				}
				namedDimensionID = candidate.DimensionID
			}
		}
		if namedDimensionID == "" {
			return nil, true
		}
		for _, candidate := range candidates {
			if candidate.DimensionID == namedDimensionID {
				filtered = append(filtered, candidate)
			}
		}
	}
	sort.Slice(filtered, func(left, right int) bool {
		leftCode := strings.ToLower(filtered[left].DimensionCode)
		rightCode := strings.ToLower(filtered[right].DimensionCode)
		if leftCode != rightCode {
			return leftCode < rightCode
		}
		return filtered[left].MemberValue < filtered[right].MemberValue
	})
	return filtered, false
}

func metricScopedDimensionsMentioned(
	ctx context.Context,
	tx pgx.Tx,
	generationID, metricNodeKey, datasetVersionID, question string,
	minimumConfidence float64,
) ([]resolvedGraphNode, error) {
	rows, err := tx.Query(ctx, `SELECT dimension.node_key,
			dimension.subject_ref,dimension.label,
			dimension.payload_hash,dimension.payload_json
		FROM platform.semantic_graph_edges AS compatibility
		JOIN platform.semantic_graph_nodes AS dimension
		  ON dimension.tenant_id=compatibility.tenant_id
		 AND dimension.generation_id=compatibility.generation_id
		 AND dimension.node_key=compatibility.to_node_key
		 AND dimension.node_type='DIMENSION'
		WHERE compatibility.generation_id=$1::uuid
		  AND compatibility.from_node_key=$2
		  AND compatibility.relation_type='METRIC_DIMENSION'
		  AND compatibility.confidence>=$4
		  AND dimension.payload_json->>'datasetVersionId'=$3
		ORDER BY char_length(dimension.label) DESC,dimension.node_key
		LIMIT 32`,
		generationID, metricNodeKey, datasetVersionID, minimumConfidence,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	question = strings.ToLower(question)
	result := []resolvedGraphNode{}
	longest := 0
	for rows.Next() {
		var node resolvedGraphNode
		var payload []byte
		if err := rows.Scan(
			&node.NodeKey, &node.SubjectRef, &node.Label,
			&node.PayloadHash, &payload,
		); err != nil {
			return nil, err
		}
		node.Payload = append(json.RawMessage(nil), payload...)
		var value struct {
			Code string `json:"code"`
		}
		if err := json.Unmarshal(node.Payload, &value); err != nil {
			return nil, err
		}
		matchLength := 0
		if strings.Contains(question, strings.ToLower(node.Label)) {
			matchLength = utf8.RuneCountInString(node.Label)
		}
		if strings.Contains(question, strings.ToLower(value.Code)) {
			if codeLength := utf8.RuneCountInString(value.Code); codeLength > matchLength {
				matchLength = codeLength
			}
		}
		if matchLength == 0 || matchLength < longest {
			continue
		}
		if matchLength > longest {
			longest = matchLength
			result = result[:0]
		}
		result = append(result, node)
	}
	return result, rows.Err()
}

func memberLookupTokens(question, metricLabel string) []string {
	question = strings.ToLower(strings.TrimSpace(question))
	metricLabel = strings.ToLower(strings.TrimSpace(metricLabel))
	if question == "" {
		return nil
	}
	stop := map[string]bool{
		"多少": true, "所有": true, "全部": true, "截至": true,
		"截止": true, "今年": true, "去年": true, "本月": true,
		"上月": true, "数量": true, "总数": true, "趋势": true,
		"排名": true, "同比": true, "环比": true, "是多少": true,
		"是什么": true, "哪些": true, "怎么": true, "所有的": true,
		"有多少": true, "查询": true, "查看": true, "分析": true,
		"请问": true, "给我": true, "按照": true, "根据": true,
	}
	seen := map[string]bool{}
	result := make([]string, 0, 128)
	segments := strings.FieldsFunc(question, func(character rune) bool {
		return unicode.IsSpace(character) || unicode.IsPunct(character) ||
			unicode.IsSymbol(character)
	})
	for _, segment := range segments {
		runes := []rune(segment)
		if len(runes) > 64 {
			runes = runes[:64]
		}
		maximumLength := len(runes)
		if maximumLength > 16 {
			maximumLength = 16
		}
		for length := maximumLength; length >= 2; length-- {
			for start := 0; start+length <= len(runes); start++ {
				token := string(runes[start : start+length])
				if stop[token] || isTimeLookupToken(token) || seen[token] ||
					(metricLabel != "" && strings.Contains(metricLabel, token)) {
					continue
				}
				seen[token] = true
				result = append(result, token)
				if len(result) >= 512 {
					return result
				}
			}
		}
	}
	return result
}

func isTimeLookupToken(value string) bool {
	runes := []rune(value)
	if len(runes) < 2 {
		return false
	}
	unit := runes[len(runes)-1]
	if unit != '年' && unit != '月' && unit != '日' && unit != '号' {
		return false
	}
	for _, character := range runes[:len(runes)-1] {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}

func graphMemberNodes(
	ctx context.Context,
	tx pgx.Tx,
	generationID, normalizedValue, dimensionCode, metricNodeKey,
	datasetVersionID string,
	minimumConfidence float64,
) ([]resolvedGraphNode, error) {
	rows, err := tx.Query(ctx, `SELECT graph_member.node_key,
			graph_member.subject_ref,graph_member.label,
			graph_member.payload_hash,graph_member.payload_json
		FROM platform.dimension_members AS member
		JOIN platform.semantic_dimensions AS dimension
		  ON dimension.tenant_id=member.tenant_id
		 AND dimension.id=member.dimension_id
		 AND dimension.status='PUBLISHED'
		JOIN platform.semantic_graph_nodes AS graph_member
		  ON graph_member.tenant_id=member.tenant_id
		 AND graph_member.generation_id=$1::uuid
		 AND graph_member.node_key='member:'||member.id::text
			JOIN platform.semantic_graph_nodes AS graph_dimension
		  ON graph_dimension.tenant_id=dimension.tenant_id
			 AND graph_dimension.generation_id=graph_member.generation_id
			 AND graph_dimension.node_key='dimension:'||dimension.id::text
			JOIN platform.semantic_graph_edges AS compatibility
			  ON compatibility.tenant_id=dimension.tenant_id
			 AND compatibility.generation_id=graph_dimension.generation_id
			 AND compatibility.from_node_key=$4
			 AND compatibility.to_node_key=graph_dimension.node_key
			 AND compatibility.relation_type='METRIC_DIMENSION'
			 AND compatibility.confidence>=$6
			WHERE member.status='ACTIVE'
		  AND (member.valid_from IS NULL OR member.valid_from<=now())
		  AND (member.valid_to IS NULL OR member.valid_to>now())
		  AND (
		    member.normalized_value=$2
		    OR EXISTS(
		      SELECT 1
		      FROM platform.dimension_member_aliases AS alias
		      WHERE alias.tenant_id=member.tenant_id
		        AND alias.dimension_id=member.dimension_id
		        AND alias.dimension_member_id=member.id
		        AND alias.normalized_alias=$2
		        AND (alias.valid_from IS NULL OR alias.valid_from<=now())
		        AND (alias.valid_to IS NULL OR alias.valid_to>now())
			  )
			)
			  AND ($3='' OR lower(dimension.code::text)=lower($3))
			  AND dimension.dataset_version_id=$5::uuid
			ORDER BY graph_member.node_key LIMIT 2`,
		generationID, normalizedValue, dimensionCode, metricNodeKey,
		datasetVersionID, minimumConfidence)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []resolvedGraphNode{}
	for rows.Next() {
		var node resolvedGraphNode
		var payload []byte
		if err := rows.Scan(
			&node.NodeKey, &node.SubjectRef, &node.Label,
			&node.PayloadHash, &payload,
		); err != nil {
			return nil, err
		}
		node.Payload = append(json.RawMessage(nil), payload...)
		result = append(result, node)
	}
	return result, rows.Err()
}

func graphMetricDimensionEvidence(
	ctx context.Context,
	tx pgx.Tx,
	generationID, metricNodeKey string,
	dimension resolvedGraphNode,
	minimumConfidence float64,
) (QueryEvidence, error) {
	var evidence QueryEvidence
	err := tx.QueryRow(ctx, `SELECT confidence::float8,authority,evidence_hash
		FROM platform.semantic_graph_edges
		WHERE generation_id=$1::uuid
		  AND from_node_key=$2 AND to_node_key=$3
		  AND relation_type='METRIC_DIMENSION'
		  AND confidence>=$4
		ORDER BY confidence DESC,
		  CASE authority
		    WHEN 'CONTROL_PLANE' THEN 1 WHEN 'VERIFIED' THEN 2
		    WHEN 'RULE' THEN 3 ELSE 4
		  END
		LIMIT 1`,
		generationID, metricNodeKey, dimension.NodeKey, minimumConfidence,
	).Scan(&evidence.Confidence, &evidence.Authority, &evidence.EvidenceHash)
	if errors.Is(err, pgx.ErrNoRows) {
		return QueryEvidence{}, ErrNotFound
	}
	if err != nil {
		return QueryEvidence{}, err
	}
	evidence.NodeKey = dimension.NodeKey
	evidence.RelationType = "METRIC_DIMENSION"
	evidence.SubjectType = "DIMENSION"
	evidence.SubjectRef = dimension.SubjectRef
	evidence.Label = dimension.Label
	return evidence, nil
}

func graphNodeByKey(
	ctx context.Context,
	tx pgx.Tx,
	generationID, nodeKey string,
) (node resolvedGraphNode, err error) {
	var payload []byte
	err = tx.QueryRow(ctx, `SELECT node_key,subject_ref,label,payload_hash,payload_json
		FROM platform.semantic_graph_nodes
		WHERE generation_id=$1::uuid AND node_key=$2`,
		generationID, nodeKey,
	).Scan(
		&node.NodeKey, &node.SubjectRef, &node.Label, &node.PayloadHash, &payload,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return node, ErrNotFound
	}
	node.Payload = append(json.RawMessage(nil), payload...)
	return node, err
}

func graphLineageEvidence(
	ctx context.Context,
	tx pgx.Tx,
	generationID, rootNodeKey string,
	maximumHops int,
) ([]QueryEvidence, error) {
	rows, err := tx.Query(ctx, `WITH RECURSIVE walk AS (
			SELECT $2::text AS node_key,0 AS depth,ARRAY[$2::text] AS path,
				''::text AS relation_type,''::text AS authority,
				1.0000::numeric AS confidence,''::text AS evidence_hash
			UNION ALL
			SELECT edge.to_node_key,walk.depth+1,walk.path||edge.to_node_key,
				edge.relation_type,edge.authority,edge.confidence,edge.evidence_hash
			FROM walk
			JOIN platform.semantic_graph_edges AS edge
			  ON edge.generation_id=$1::uuid AND edge.from_node_key=walk.node_key
			 AND edge.relation_type IN ('DATASET_DEPENDS_ON','DATASET_SOURCE')
			WHERE walk.depth<$3 AND NOT edge.to_node_key=ANY(walk.path)
		)
		SELECT walk.depth,walk.relation_type,walk.authority,
			walk.confidence::float8,walk.evidence_hash,
			node.node_key,node.node_type,node.subject_ref,node.label,node.payload_hash
		FROM walk
		JOIN platform.semantic_graph_nodes AS node
		  ON node.generation_id=$1::uuid AND node.node_key=walk.node_key
		WHERE walk.depth>0
		ORDER BY walk.depth,node.node_key`,
		generationID, rootNodeKey, maximumHops)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []QueryEvidence{}
	for rows.Next() {
		var depth int
		var nodePayloadHash string
		var evidence QueryEvidence
		if err := rows.Scan(
			&depth, &evidence.RelationType, &evidence.Authority,
			&evidence.Confidence, &evidence.EvidenceHash,
			&evidence.NodeKey, &evidence.SubjectType,
			&evidence.SubjectRef, &evidence.Label, &nodePayloadHash,
		); err != nil {
			return nil, err
		}
		if evidence.EvidenceHash == "" {
			evidence.EvidenceHash = nodePayloadHash
		}
		result = append(result, evidence)
	}
	return result, rows.Err()
}

func persistQueryPlan(
	ctx context.Context,
	tx pgx.Tx,
	actorID string,
	input QueryPlanInput,
	plan *QueryPlan,
) error {
	reconcilePlanningTrace(plan)
	memberFilterCount := len(input.MemberFilters)
	if input.MemberValue != "" {
		memberFilterCount++
	}
	normalizedRequest := map[string]any{
		"intent": input.Intent, "metricCode": input.MetricCode,
		"dimensionCode":     input.DimensionCode,
		"hasMemberValue":    input.MemberValue != "",
		"memberFilterCount": memberFilterCount,
		"maximumPathHops":   input.MaximumPathHops,
		"topN":              input.TopN,
		"sortDirection":     input.SortDirection,
		"timePreset":        input.TimePreset,
		"timezone":          input.Timezone,
		"comparisonMode":    input.ComparisonMode,
		"resolution":        plan.Resolution,
		"conditions":        plan.Conditions,
		"planningTrace":     plan.PlanningTrace,
	}
	if input.TimeRange != nil {
		normalizedRequest["timeRange"] = input.TimeRange
	}
	if input.ComparisonRange != nil {
		normalizedRequest["comparisonRange"] = input.ComparisonRange
	}
	var createdAt time.Time
	err := tx.QueryRow(ctx, `INSERT INTO platform.semantic_query_plans(
			tenant_id,graph_generation_id,question_hash,intent,
			normalized_request_json,status,confidence,selected_metric_version_id,
			selected_metric_id,selected_dimension_id,selected_dataset_version_id,
			selected_materialization_id,path_hash,failure_code,created_by
		) VALUES(
			platform.current_tenant_id(),$1::uuid,$2,$3,$4,$5,$6,
			NULLIF($7,'')::uuid,NULLIF($8,'')::uuid,NULLIF($9,'')::uuid,
			NULLIF($10,'')::uuid,NULLIF($11,'')::uuid,$12,$13,$14::uuid
		) RETURNING id::text,created_at`,
		plan.GraphGenerationID, plan.QuestionHash, plan.Intent,
		normalizedRequest, plan.Status, plan.Confidence,
		plan.SelectedMetricVersionID, plan.SelectedMetricID,
		plan.SelectedDimensionID, plan.SelectedDatasetVersionID,
		plan.SelectedMaterializationID, plan.PathHash, plan.FailureCode, actorID,
	).Scan(&plan.ID, &createdAt)
	if err != nil {
		return err
	}
	plan.CreatedAt = createdAt.UTC().Format(time.RFC3339Nano)
	for index := range plan.Evidence {
		plan.Evidence[index].Index = index
		evidence := plan.Evidence[index]
		if _, err := tx.Exec(ctx, `INSERT INTO platform.semantic_query_plan_evidence(
				tenant_id,query_plan_id,evidence_index,node_key,relation_type,
				subject_type,subject_ref,authority,confidence,evidence_hash
			) VALUES(
				platform.current_tenant_id(),$1::uuid,$2,$3,$4,$5,$6,$7,$8,$9
			)`, plan.ID, index, evidence.NodeKey, evidence.RelationType,
			evidence.SubjectType, evidence.SubjectRef, evidence.Authority,
			evidence.Confidence, evidence.EvidenceHash,
		); err != nil {
			return err
		}
	}
	if plan.Status == "READY" {
		if err := persistDimensionWhereDecisions(ctx, tx, plan); err != nil {
			return err
		}
	}
	return nil
}

func persistDimensionWhereDecisions(
	ctx context.Context,
	tx pgx.Tx,
	plan *QueryPlan,
) error {
	if plan == nil || plan.ID == "" ||
		plan.SelectedMetricID == "" ||
		plan.SelectedMetricVersionID == "" ||
		plan.SelectedDatasetVersionID == "" ||
		plan.SelectedMaterializationID == "" {
		return nil
	}
	var metricCode, metricName, metricFieldID string
	var tableSchema, tableName string
	if err := tx.QueryRow(ctx, `SELECT metric.code::text,metric.name,
			COALESCE(version.definition_json#>>'{expression,fieldId}',''),
			materialization.published_schema,materialization.published_name
		FROM platform.metric_versions AS version
		JOIN platform.metrics AS metric
		  ON metric.tenant_id=version.tenant_id
		 AND metric.id=version.metric_id
		JOIN platform.dataset_materializations AS materialization
		  ON materialization.tenant_id=version.tenant_id
		 AND materialization.id=$4::uuid
		 AND materialization.dataset_id=version.dataset_id
		 AND materialization.dataset_version_id=version.dataset_version_id
		 AND materialization.status='ACTIVE'
		WHERE version.id=$1::uuid
		  AND version.metric_id=$2::uuid
		  AND version.dataset_version_id=$3::uuid
		  AND version.status='PUBLISHED'`,
		plan.SelectedMetricVersionID, plan.SelectedMetricID,
		plan.SelectedDatasetVersionID, plan.SelectedMaterializationID,
	).Scan(
		&metricCode, &metricName, &metricFieldID, &tableSchema, &tableName,
	); err != nil {
		return err
	}
	dimensionIDs := map[string]string{}
	for _, dimension := range plan.Conditions.Dimensions {
		dimensionIDs[strings.ToLower(dimension.DimensionCode)] =
			dimension.DimensionID
	}
	for index := range plan.PlanningTrace {
		trace := &plan.PlanningTrace[index]
		dimensionID := trace.DimensionID
		if dimensionID == "" {
			dimensionID =
				dimensionIDs[strings.ToLower(trace.DimensionCode)]
		}
		if dimensionID != "" {
			var physicalFieldID, physicalFieldName, description string
			err := tx.QueryRow(ctx, `SELECT dimension.field_id,
					dimension.code::text,dimension.description
				FROM platform.semantic_dimensions AS dimension
				WHERE dimension.id=$1::uuid
				  AND dimension.status='PUBLISHED'`,
				dimensionID,
			).Scan(
				&physicalFieldID, &physicalFieldName, &description,
			)
			if err != nil {
				return err
			}
			trace.DimensionFieldID = physicalFieldID
			trace.DimensionFieldName = physicalFieldName
			trace.DimensionFieldDescription = description
		}
		values := append([]string(nil), trace.SelectedMemberKeys...)
		sort.Strings(values)
		selected := make(map[string]bool, len(values))
		for _, value := range values {
			selected[value] = true
		}
		trace.WhereCondition, trace.CompiledCondition =
			queryLookupWhereConditions(*trace, selected)
		canonicalValue := strings.TrimSpace(trace.CanonicalValue)
		if trace.Sensitive || !trace.Selected ||
			trace.WhereDesignStatus != "SUCCEEDED" ||
			trace.VectorSearchStatus != "SUCCEEDED" ||
			len(trace.VectorEmbedding) != 2560 ||
			strings.TrimSpace(trace.VectorQuery) == "" ||
			strings.TrimSpace(trace.VectorModel) == "" ||
			canonicalValue == "" || len(values) == 0 ||
			dimensionID == "" ||
			strings.TrimSpace(trace.DimensionFieldID) == "" ||
			strings.TrimSpace(trace.DimensionFieldName) == "" ||
			strings.TrimSpace(trace.DimensionFieldDescription) == "" ||
			strings.TrimSpace(trace.WhereCondition) == "" ||
			strings.TrimSpace(trace.CompiledCondition) == "" ||
			strings.TrimSpace(trace.WhereDesignOperator) == "" ||
			strings.TrimSpace(trace.WhereDesignModel) == "" ||
			strings.TrimSpace(trace.WhereDesignReason) == "" ||
			metricFieldID == "" {
			continue
		}
		aliases := make([]string, 0, len(trace.AliasValues)+1)
		for _, alias := range append(
			append([]string(nil), trace.AliasValues...), trace.Term,
		) {
			alias = strings.TrimSpace(alias)
			if alias == "" || len(alias) > 1024 {
				continue
			}
			aliases = appendUniqueString(aliases, alias)
		}
		sort.Strings(aliases)
		if len(aliases) > 64 {
			aliases = aliases[:64]
		}
		var dimensionMemberID any
		if len(values) == 1 {
			var resolvedMemberID string
			memberErr := tx.QueryRow(ctx, `SELECT id::text
				FROM platform.dimension_members
				WHERE dimension_id=$1::uuid
				  AND normalized_value=$2
				  AND status='ACTIVE'`,
				dimensionID, values[0],
			).Scan(&resolvedMemberID)
			if memberErr != nil && !errors.Is(memberErr, pgx.ErrNoRows) {
				return memberErr
			}
			if memberErr == nil {
				dimensionMemberID = resolvedMemberID
			}
		}
		err := tx.QueryRow(ctx, `INSERT INTO platform.dimension_where_decisions(
				tenant_id,vector_key,vector_key_hash,embedding,embedding_model,
				dimension_id,dimension_field_id,dimension_field_name,
				dimension_description,canonical_value,aliases,
				selected_member_set_hash,selected_member_count,
				metric_id,metric_version_id,dataset_version_id,
				metric_code,metric_name,metric_field_id,materialization_id,
				table_schema,table_name,predicate_operator,where_condition,
				compiled_condition,llm_model,llm_prompt_version,llm_reason,
				latest_query_plan_id,dimension_member_id
			) VALUES(
				platform.current_tenant_id(),$1,$2,$3::halfvec,$4,
				$5::uuid,$6,$7,$8,$9,$10,$11,$12,
				$13::uuid,$14::uuid,$15::uuid,$16,$17,$18,$19::uuid,
				$20,$21,$22,$23,$24,$25,
				'semantic-query-where-design-v2',$26,$27::uuid,$28::uuid
			)
			ON CONFLICT(
				tenant_id,dimension_id,selected_member_set_hash,
				metric_version_id,materialization_id
			) DO UPDATE SET
				vector_key=EXCLUDED.vector_key,
				vector_key_hash=EXCLUDED.vector_key_hash,
				embedding=EXCLUDED.embedding,
				embedding_model=EXCLUDED.embedding_model,
				dimension_field_id=EXCLUDED.dimension_field_id,
				dimension_field_name=EXCLUDED.dimension_field_name,
				dimension_description=EXCLUDED.dimension_description,
				canonical_value=EXCLUDED.canonical_value,
				aliases=ARRAY(
					SELECT DISTINCT alias
					FROM unnest(
						platform.dimension_where_decisions.aliases ||
						EXCLUDED.aliases
					) AS expanded(alias)
					WHERE btrim(alias)<>''
					ORDER BY alias
					LIMIT 64
				),
				metric_code=EXCLUDED.metric_code,
				metric_name=EXCLUDED.metric_name,
				metric_field_id=EXCLUDED.metric_field_id,
				table_schema=EXCLUDED.table_schema,
				table_name=EXCLUDED.table_name,
				predicate_operator=EXCLUDED.predicate_operator,
				where_condition=EXCLUDED.where_condition,
				compiled_condition=EXCLUDED.compiled_condition,
				llm_model=EXCLUDED.llm_model,
				llm_prompt_version=EXCLUDED.llm_prompt_version,
				llm_reason=EXCLUDED.llm_reason,
				latest_query_plan_id=EXCLUDED.latest_query_plan_id,
				dimension_member_id=EXCLUDED.dimension_member_id,
				embedding_document_id=NULL,
				source_type='QUERY_OBSERVED',
				source_input_hash='',
				observation_count=
					platform.dimension_where_decisions.observation_count+1,
				last_seen_at=now()
			RETURNING id::text`,
			trace.VectorQuery, hashText(trace.VectorQuery),
			formatVector(trace.VectorEmbedding), trace.VectorModel,
			dimensionID, trace.DimensionFieldID, trace.DimensionFieldName,
			trace.DimensionFieldDescription, canonicalValue, aliases,
			hashText(strings.Join(values, "\x1f")), len(values),
			plan.SelectedMetricID, plan.SelectedMetricVersionID,
			plan.SelectedDatasetVersionID, metricCode, metricName,
			metricFieldID, plan.SelectedMaterializationID,
			tableSchema, tableName, trace.WhereDesignOperator,
			trace.WhereCondition, trace.CompiledCondition,
			trace.WhereDesignModel, trace.WhereDesignReason, plan.ID,
			dimensionMemberID,
		).Scan(&trace.DecisionID)
		if err != nil {
			return err
		}
		trace.MetricName = metricName
		trace.MetricCode = metricCode
		trace.MetricFieldID = metricFieldID
		trace.MetricVersionID = plan.SelectedMetricVersionID
		trace.DatasetVersionID = plan.SelectedDatasetVersionID
		trace.MaterializationID = plan.SelectedMaterializationID
		trace.TableSchema = tableSchema
		trace.TableName = tableName
		trace.DimensionID = dimensionID
	}
	return nil
}

func reconcilePlanningTrace(plan *QueryPlan) {
	if plan == nil || len(plan.PlanningTrace) == 0 {
		return
	}
	selectedByDimension := map[string]map[string]bool{}
	for _, dimension := range plan.Conditions.Dimensions {
		values := append([]string(nil), dimension.MemberKeys...)
		if dimension.MemberKey != "" {
			values = append(values, dimension.MemberKey)
		}
		selected := map[string]bool{}
		for _, value := range values {
			selected[value] = true
		}
		selectedByDimension[strings.ToLower(dimension.DimensionCode)] = selected
	}
	for index := range plan.PlanningTrace {
		trace := &plan.PlanningTrace[index]
		if trace.MetricCode == "" {
			trace.MetricCode = plan.Conditions.MetricCode
		}
		trace.MetricFieldID = plan.MetricFieldID
		trace.MetricVersionID = plan.SelectedMetricVersionID
		trace.DatasetVersionID = plan.SelectedDatasetVersionID
		trace.MaterializationID = plan.SelectedMaterializationID
		if trace.CanonicalValue == "" {
			trace.CanonicalValue = deterministicCanonicalValue(*trace)
		}
		trace.AliasValues = appendUniqueString(
			trace.AliasValues, trace.Term,
		)
		if trace.VectorQuery == "" {
			trace.VectorQuery = dimensionVectorQuery(*trace)
		}
		if trace.Sensitive {
			trace.Selected = len(
				selectedByDimension[strings.ToLower(trace.DimensionCode)],
			) > 0
		} else {
			finalMembers := selectedByDimension[strings.ToLower(trace.DimensionCode)]
			selected := make([]string, 0, len(trace.CandidateMemberKeys))
			for _, member := range trace.CandidateMemberKeys {
				if finalMembers[member] {
					selected = append(selected, member)
				}
			}
			sort.Strings(selected)
			trace.SelectedMemberKeys = selected
			trace.Selected = len(selected) > 0
		}
		acceptedCount := len(trace.SelectedMemberKeys)
		if trace.Sensitive && trace.Selected {
			acceptedCount = len(
				selectedByDimension[strings.ToLower(trace.DimensionCode)],
			)
		}
		inputCount := trace.CandidateCount
		if trace.VectorCandidateCount > inputCount {
			inputCount = trace.VectorCandidateCount
		}
		trace.CandidateFilter = QueryCandidateFilterTrace{
			InputCount: inputCount, AcceptedCount: acceptedCount,
			RejectedCount: max(0, inputCount-acceptedCount),
			Status:        "PASS",
			Rules: []string{
				"METRIC_DIMENSION_COMPATIBLE",
				"ACTIVE_MEMBER_WINDOW",
				"NON_EMPTY_MEMBER_KEY",
				"SEMANTIC_SET_SIZE_WITHIN_LIMIT",
			},
		}
		trace.WhereCondition, trace.CompiledCondition =
			queryLookupWhereConditions(
				*trace,
				selectedByDimension[strings.ToLower(trace.DimensionCode)],
			)
	}
	plan.PlanningTrace = deduplicatePlanningTrace(plan.PlanningTrace)
}

func queryLookupWhereConditions(
	trace QueryDimensionValueLookupTrace,
	selected map[string]bool,
) (string, string) {
	values := make([]string, 0, len(selected))
	for value := range selected {
		values = append(values, value)
	}
	sort.Strings(values)
	if len(values) == 0 {
		return "", ""
	}
	dimensionCode := trace.DimensionCode
	if dimensionCode == "" {
		dimensionCode = trace.DimensionFieldID
	}
	fieldName := trace.DimensionFieldName
	if fieldName == "" {
		fieldName = dimensionCode
	}
	if fieldName == "" {
		fieldName = trace.DimensionFieldID
	}
	compiledField := trace.DimensionFieldID
	if compiledField == "" {
		compiledField = fieldName
	}
	compiled := fmt.Sprintf(
		"%s IN (:%s_1 … :%s_%d)",
		compiledField, dimensionCode, dimensionCode, len(values),
	)
	if len(values) == 1 {
		compiled = fmt.Sprintf(
			"%s = :%s_1", compiledField, dimensionCode,
		)
	}
	if trace.Sensitive {
		return fieldName + " = <受控参数>", compiled
	}
	escape := func(value string) string {
		return strings.ReplaceAll(value, "'", "''")
	}
	if trace.WhereDesignOperator == "CONTAINS" ||
		trace.WhereDesignOperator == "" &&
			trace.MatchMethod == "SEMANTIC_TAG" {
		return fmt.Sprintf(
			"%s LIKE '%%%s%%'", fieldName, escape(trace.Term),
		), compiled
	}
	if trace.WhereDesignOperator == "EQUALS" ||
		trace.WhereDesignOperator == "" && len(values) == 1 {
		return fmt.Sprintf(
			"%s = '%s'", fieldName, escape(values[0]),
		), compiled
	}
	quoted := make([]string, 0, len(values))
	for _, value := range values {
		quoted = append(quoted, "'"+escape(value)+"'")
	}
	return fmt.Sprintf(
		"%s IN (%s)", fieldName, strings.Join(quoted, ", "),
	), compiled
}

func deduplicatePlanningTrace(
	items []QueryDimensionValueLookupTrace,
) []QueryDimensionValueLookupTrace {
	result := make([]QueryDimensionValueLookupTrace, 0, len(items))
	indexByKey := map[string]int{}
	for _, item := range items {
		key := strings.Join([]string{
			strings.ToLower(item.MetricCode),
			strings.ToLower(item.DimensionCode),
			strings.ToLower(item.CanonicalValue),
			hashText(strings.Join(item.SelectedMemberKeys, "\x1f")),
			item.MatchMethod,
		}, "\x00")
		index, exists := indexByKey[key]
		if !exists {
			indexByKey[key] = len(result)
			result = append(result, item)
			continue
		}
		existing := &result[index]
		if item.Source == "CURRENT_TURN" {
			existing.Source = item.Source
		}
		if existing.DimensionFieldName == "" {
			existing.DimensionFieldName = item.DimensionFieldName
		}
		if existing.DimensionFieldDescription == "" {
			existing.DimensionFieldDescription =
				item.DimensionFieldDescription
		}
		if existing.DimensionID == "" {
			existing.DimensionID = item.DimensionID
		}
		if existing.CanonicalValue == "" {
			existing.CanonicalValue = item.CanonicalValue
		}
		for _, alias := range item.AliasValues {
			existing.AliasValues = appendUniqueString(
				existing.AliasValues, alias,
			)
		}
		existing.AliasValues = appendUniqueString(
			existing.AliasValues, item.Term,
		)
		if existing.MetricName == "" {
			existing.MetricName = item.MetricName
		}
		if existing.TableName == "" {
			existing.TableSchema = item.TableSchema
			existing.TableName = item.TableName
		}
		if existing.MaterializationID == "" {
			existing.MaterializationID = item.MaterializationID
		}
		if existing.MetricVersionID == "" {
			existing.MetricVersionID = item.MetricVersionID
		}
		if existing.DatasetVersionID == "" {
			existing.DatasetVersionID = item.DatasetVersionID
		}
		if item.VectorSearchStatus != "" {
			existing.VectorQuery = item.VectorQuery
			existing.VectorModel = item.VectorModel
			existing.VectorDimensions = item.VectorDimensions
			existing.VectorEmbedding = append(
				[]float32(nil), item.VectorEmbedding...,
			)
			existing.VectorSearchStatus = item.VectorSearchStatus
			existing.VectorCandidateCount = item.VectorCandidateCount
			existing.VectorCandidateMemberKeys = append(
				[]string(nil), item.VectorCandidateMemberKeys...,
			)
			existing.VectorTopScore = item.VectorTopScore
		}
		if item.WhereDesignStatus != "" {
			existing.WhereDesignStatus = item.WhereDesignStatus
			existing.WhereDesignOperator = item.WhereDesignOperator
			existing.WhereDesignReason = item.WhereDesignReason
			existing.WhereDesignModel = item.WhereDesignModel
		}
	}
	return result
}

func hasSourceEvidence(evidence []QueryEvidence) bool {
	for _, item := range evidence {
		if item.SubjectType == "SOURCE" {
			return true
		}
	}
	return false
}
