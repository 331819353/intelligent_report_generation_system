package semanticqa

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

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

		metricCandidates, err := graphNodesByCode(
			ctx, tx, plan.GraphGenerationID, "METRIC", input.MetricCode,
		)
		if err != nil {
			return err
		}
		if len(metricCandidates) > 1 {
			plan.Status, plan.FailureCode = "AMBIGUOUS", "METRIC_AMBIGUOUS"
			return persistQueryPlan(ctx, tx, actorID, input, &plan)
		}
		if len(metricCandidates) == 0 {
			return persistQueryPlan(ctx, tx, actorID, input, &plan)
		}
		metricNode := metricCandidates[0]
		var metricPayload struct {
			MetricID         string `json:"metricId"`
			MetricVersionID  string `json:"metricVersionId"`
			DatasetVersionID string `json:"datasetVersionId"`
		}
		if err := json.Unmarshal(metricNode.Payload, &metricPayload); err != nil {
			return err
		}
		plan.SelectedMetricID = metricPayload.MetricID
		plan.SelectedMetricVersionID = metricPayload.MetricVersionID
		plan.SelectedDatasetVersionID = metricPayload.DatasetVersionID
		var metricTimeFieldID string
		if err := tx.QueryRow(ctx, `SELECT COALESCE(definition_json->>'timeFieldId','')
			FROM platform.metric_versions
			WHERE id=$1::uuid AND metric_id=$2::uuid
			  AND dataset_version_id=$3::uuid AND status='PUBLISHED'`,
			metricPayload.MetricVersionID, metricPayload.MetricID,
			metricPayload.DatasetVersionID,
		).Scan(&metricTimeFieldID); err != nil {
			return err
		}
		if (input.TimeRange != nil || input.Intent == "TREND") &&
			metricTimeFieldID == "" {
			plan.Status, plan.FailureCode = "GAP", "METRIC_TIME_FIELD_NOT_AVAILABLE"
			return persistQueryPlan(ctx, tx, actorID, input, &plan)
		}

		var memberNode, dimensionNode *resolvedGraphNode
		if input.MemberValue != "" {
			memberCandidates, err := graphMemberNodes(
				ctx, tx, plan.GraphGenerationID,
				strings.ToLower(strings.TrimSpace(input.MemberValue)),
				input.DimensionCode,
			)
			if err != nil {
				return err
			}
			if len(memberCandidates) > 1 {
				plan.Status, plan.FailureCode = "AMBIGUOUS", "MEMBER_AMBIGUOUS"
				return persistQueryPlan(ctx, tx, actorID, input, &plan)
			}
			if len(memberCandidates) == 0 {
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
			dimensionCandidates, err := graphNodesByCode(
				ctx, tx, plan.GraphGenerationID, "DIMENSION", input.DimensionCode,
			)
			if err != nil {
				return err
			}
			if len(dimensionCandidates) > 1 {
				plan.Status, plan.FailureCode = "AMBIGUOUS", "DIMENSION_AMBIGUOUS"
				return persistQueryPlan(ctx, tx, actorID, input, &plan)
			}
			if len(dimensionCandidates) == 0 {
				plan.Status, plan.FailureCode = "GAP", "DIMENSION_NOT_FOUND"
				return persistQueryPlan(ctx, tx, actorID, input, &plan)
			}
			dimensionNode = &dimensionCandidates[0]
			plan.SelectedDimensionID = dimensionNode.SubjectRef
		}
		if input.TopN > 0 && dimensionNode == nil {
			plan.Status, plan.FailureCode = "GAP", "TOP_N_DIMENSION_REQUIRED"
			return persistQueryPlan(ctx, tx, actorID, input, &plan)
		}

		if memberNode != nil {
			plan.Evidence = append(plan.Evidence, QueryEvidence{
				NodeKey: memberNode.NodeKey, SubjectType: "MEMBER",
				SubjectRef: memberNode.SubjectRef, Label: "成员命中（值已脱敏）",
				Authority: "CONTROL_PLANE", Confidence: 1,
				EvidenceHash: memberNode.PayloadHash,
			})
		}
		if dimensionNode != nil {
			var confidence float64
			var authority, evidenceHash string
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
				plan.GraphGenerationID, metricNode.NodeKey,
				dimensionNode.NodeKey, minimumConfidence,
			).Scan(&confidence, &authority, &evidenceHash)
			if errors.Is(err, pgx.ErrNoRows) {
				plan.Status, plan.FailureCode = "REJECTED", "UNPROVEN_DIMENSION_METRIC_PATH"
				return persistQueryPlan(ctx, tx, actorID, input, &plan)
			}
			if err != nil {
				return err
			}
			plan.Evidence = append(plan.Evidence, QueryEvidence{
				NodeKey: dimensionNode.NodeKey, RelationType: "METRIC_DIMENSION",
				SubjectType: "DIMENSION", SubjectRef: dimensionNode.SubjectRef,
				Label: dimensionNode.Label, Authority: authority,
				Confidence: confidence, EvidenceHash: evidenceHash,
			})
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
			plan.Status, plan.FailureCode = "GAP", "DATASET_NOT_FOUND"
			return persistQueryPlan(ctx, tx, actorID, input, &plan)
		}
		var datasetEdgeHash, datasetEdgeAuthority string
		var datasetConfidence float64
		if err := tx.QueryRow(ctx, `SELECT evidence_hash,authority,confidence::float8
			FROM platform.semantic_graph_edges
			WHERE generation_id=$1::uuid AND from_node_key=$2 AND to_node_key=$3
			  AND relation_type='METRIC_DATASET'`,
			plan.GraphGenerationID, metricNode.NodeKey, datasetNode.NodeKey,
		).Scan(&datasetEdgeHash, &datasetEdgeAuthority, &datasetConfidence); err != nil {
			return ErrUnprovenPath
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
				plan.Status, plan.FailureCode = "REJECTED", "ACTIVE_MATERIALIZATION_NOT_PROVEN"
				return persistQueryPlan(ctx, tx, actorID, input, &plan)
			}
			return err
		}
		plan.SelectedMaterializationID = materializationNode.SubjectRef
		plan.Evidence = append(plan.Evidence, materializationEvidence)
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

func graphMemberNodes(
	ctx context.Context,
	tx pgx.Tx,
	generationID, normalizedValue, dimensionCode string,
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
		ORDER BY graph_member.node_key LIMIT 2`,
		generationID, normalizedValue, dimensionCode)
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
	normalizedRequest := map[string]any{
		"intent": input.Intent, "metricCode": input.MetricCode,
		"dimensionCode":   input.DimensionCode,
		"hasMemberValue":  input.MemberValue != "",
		"maximumPathHops": input.MaximumPathHops,
		"topN":            input.TopN,
		"sortDirection":   input.SortDirection,
	}
	if input.TimeRange != nil {
		normalizedRequest["timeRange"] = input.TimeRange
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
