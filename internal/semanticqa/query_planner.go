package semanticqa

import (
	"context"
	"encoding/json"
	"errors"
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
	MemberValue   string
	DimensionID   string
	DimensionCode string
	DimensionName string
	MatchedValue  string
}

type metricScopedQuestionResolution struct {
	DimensionCode  string
	MemberValue    string
	MemberFilters  []QueryMemberFilterInput
	CandidateCount int
	FailureCode    string
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
		if input.MetricMatchMethod == "" {
			input.MetricMatchMethod = "EXPLICIT_CODE"
		}
		intentDecision := "DETERMINISTIC_INTENT"
		switch input.MetricMatchMethod {
		case "CATALOG_RERANK":
			intentDecision = "CATALOG_CONSTRAINED_INTERPRETATION"
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
		if input.MemberValue == "" && len(input.MemberFilters) == 0 &&
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
			if scoped.FailureCode != "" {
				plan.Resolution = append(plan.Resolution, QueryResolutionStep{
					Stage: "DIMENSION_MEMBER", Status: "AMBIGUOUS",
					CandidateCount: scoped.CandidateCount,
					Decision:       "METRIC_SCOPED_EXACT_MATCH",
				})
				plan.Status, plan.FailureCode = "AMBIGUOUS", scoped.FailureCode
				return persistQueryPlan(ctx, tx, actorID, input, &plan)
			}
			if scoped.MemberValue != "" {
				input.DimensionCode = scoped.DimensionCode
				input.MemberValue = scoped.MemberValue
				input.MemberFilters = scoped.MemberFilters
			} else if input.DimensionCode == "" {
				input.DimensionCode = scoped.DimensionCode
			}
		}
		var metricTimeFieldID, metricTimeFieldType string
		if err := tx.QueryRow(ctx, `SELECT
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
		).Scan(&metricTimeFieldID, &metricTimeFieldType); err != nil {
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
			member    resolvedGraphNode
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
			memberCandidates, err := graphMemberNodes(
				ctx, tx, plan.GraphGenerationID,
				strings.ToLower(strings.TrimSpace(memberFilter.MemberValue)),
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
				seenFilterDimensions[payload.DimensionID] {
				plan.Status, plan.FailureCode = "REJECTED", "FILTER_DIMENSION_DUPLICATED"
				return persistQueryPlan(ctx, tx, actorID, input, &plan)
			}
			filterDimension, err := graphNodeByKey(
				ctx, tx, plan.GraphGenerationID,
				"dimension:"+payload.DimensionID,
			)
			if err != nil {
				plan.Status, plan.FailureCode = "GAP", "FILTER_DIMENSION_NOT_FOUND"
				return persistQueryPlan(ctx, tx, actorID, input, &plan)
			}
			seenFilterDimensions[payload.DimensionID] = true
			resolvedMemberFilters = append(
				resolvedMemberFilters,
				resolvedMemberFilter{
					member: memberCandidates[0], dimension: filterDimension,
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
			clause, err := queryConditionClause(
				memberFilter.dimension, memberFilter.member,
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
			plan.Evidence = append(plan.Evidence, QueryEvidence{
				NodeKey: memberFilter.member.NodeKey, SubjectType: "MEMBER",
				SubjectRef: memberFilter.member.SubjectRef,
				Label:      "成员命中（值已脱敏）",
				Authority:  "CONTROL_PLANE", Confidence: 1,
				EvidenceHash: memberFilter.member.PayloadHash,
			})
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
			minimumConfidence, tokens,
		)
		if err != nil {
			return result, err
		}
		result.CandidateCount = len(matches)
		if len(matches) > 33 {
			result.FailureCode = "MEMBER_AMBIGUOUS"
			return result, nil
		}
		selected, ambiguous := selectMetricScopedMemberMatches(matches, question)
		if ambiguous {
			result.FailureCode = "MEMBER_AMBIGUOUS"
			return result, nil
		}
		if len(selected) > 9 {
			result.FailureCode = "MEMBER_AMBIGUOUS"
			return result, nil
		}
		if len(selected) > 0 {
			result.DimensionCode = selected[0].DimensionCode
			result.MemberValue = selected[0].MemberValue
			for _, item := range selected[1:] {
				result.MemberFilters = append(
					result.MemberFilters,
					QueryMemberFilterInput{
						DimensionCode: item.DimensionCode,
						MemberValue:   item.MemberValue,
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
) ([]scopedMemberMatch, error) {
	rows, err := tx.Query(ctx, `WITH tokens(value) AS (
			SELECT unnest($6::text[])
		), compatible_dimensions AS (
			SELECT dimension.id,dimension.code::text AS code,
				dimension.name
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
		), matches AS (
			SELECT member.id,member.normalized_value,dimension.id AS dimension_id,
				dimension.code,dimension.name,tokens.value AS matched_value
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
				dimension.code,dimension.name,tokens.value AS matched_value
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
		)
		SELECT DISTINCT ON(id) normalized_value,dimension_id::text,
			code,name,matched_value
		FROM matches
		WHERE lower(matched_value)<>lower(name)
		  AND lower(matched_value)<>lower(code)
		ORDER BY id,char_length(matched_value) DESC,matched_value
		LIMIT 34`,
		generationID, metricNodeKey, datasetVersionID, dimensionCode,
		minimumConfidence, tokens,
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
			&item.DimensionName, &item.MatchedValue,
		); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
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
	selected := make([]scopedMemberMatch, 0, len(byDimension))
	for _, candidates := range byDimension {
		sort.Slice(candidates, func(left, right int) bool {
			leftLength := utf8.RuneCountInString(candidates[left].MatchedValue)
			rightLength := utf8.RuneCountInString(candidates[right].MatchedValue)
			if leftLength != rightLength {
				return leftLength > rightLength
			}
			return candidates[left].MemberValue < candidates[right].MemberValue
		})
		if len(candidates) > 1 &&
			utf8.RuneCountInString(candidates[0].MatchedValue) ==
				utf8.RuneCountInString(candidates[1].MatchedValue) &&
			candidates[0].MemberValue != candidates[1].MemberValue {
			// Multiple values from one dimension require an explicit set
			// contract. Selecting just one would silently change the question.
			return nil, true
		}
		for _, candidate := range candidates[1:] {
			if candidate.MemberValue != candidates[0].MemberValue &&
				!strings.Contains(
					candidates[0].MatchedValue, candidate.MatchedValue,
				) &&
				!strings.Contains(
					candidate.MatchedValue, candidates[0].MatchedValue,
				) {
				return nil, true
			}
		}
		selected = append(selected, candidates[0])
	}
	byMatchedValue := map[string][]scopedMemberMatch{}
	for _, match := range selected {
		byMatchedValue[match.MatchedValue] = append(
			byMatchedValue[match.MatchedValue], match,
		)
	}
	filtered := make([]scopedMemberMatch, 0, len(selected))
	for _, candidates := range byMatchedValue {
		if len(candidates) == 1 {
			filtered = append(filtered, candidates[0])
			continue
		}
		named := []scopedMemberMatch{}
		for _, candidate := range candidates {
			if strings.Contains(question, strings.ToLower(candidate.DimensionName)) ||
				strings.Contains(question, strings.ToLower(candidate.DimensionCode)) {
				named = append(named, candidate)
			}
		}
		if len(named) != 1 {
			return nil, true
		}
		filtered = append(filtered, named[0])
	}
	sort.Slice(filtered, func(left, right int) bool {
		return strings.ToLower(filtered[left].DimensionCode) <
			strings.ToLower(filtered[right].DimensionCode)
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
	return nil
}

func hasSourceEvidence(evidence []QueryEvidence) bool {
	for _, item := range evidence {
		if item.SubjectType == "SOURCE" {
			return true
		}
	}
	return false
}
