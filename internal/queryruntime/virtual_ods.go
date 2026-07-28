package queryruntime

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
	"intelligent-report-generation-system/internal/dataset"
)

const odsSourcePreviewRows = 100

// expandVirtualODSNodesTx resolves published ODS versions as logical mappings,
// not warehouse relations. Every ODS node is replaced by its immutable source
// TABLE while its field-code references are rewritten to physical source
// columns. Mixing virtual source input and warehouse input in one preview is
// deliberately rejected by the caller because it would cross trust domains.
func expandVirtualODSNodesTx(
	ctx context.Context,
	tx pgx.Tx,
	document dataset.Document,
) (dataset.Document, int, map[string]map[string]string, error) {
	odsDocuments := map[string]dataset.Document{}
	virtualCount := 0
	for _, node := range document.Nodes {
		if node.Type != "DATASET" || node.DatasetVersionID == "" {
			return dataset.Document{}, 0, nil, dataset.ErrInvalidDocument
		}
		var raw json.RawMessage
		var layer dataset.Layer
		var datasetID string
		err := tx.QueryRow(ctx, `SELECT owner.id::text,version.dsl_json,version.layer
			FROM platform.dataset_versions AS version
			JOIN platform.datasets AS owner
			  ON owner.id=version.dataset_id
			 AND owner.tenant_id=version.tenant_id
			WHERE version.id::text=$1
			  AND version.status='PUBLISHED'
			  AND owner.status='PUBLISHED'
			  AND owner.current_published_version_id=version.id
			  AND owner.deleted_at IS NULL
			FOR SHARE OF version,owner`, node.DatasetVersionID).
			Scan(&datasetID, &raw, &layer)
		if errors.Is(err, pgx.ErrNoRows) {
			return dataset.Document{}, 0, nil, dataset.ErrPreviewUnsupported
		}
		if err != nil {
			return dataset.Document{}, 0, nil, err
		}
		if layer != dataset.LayerODS {
			continue
		}
		if err := dataset.ValidateVersionDependenciesInTx(
			ctx, tx, datasetID, node.DatasetVersionID,
		); err != nil {
			return dataset.Document{}, 0, nil, dataset.ErrPreviewUnsupported
		}
		odsDocument, err := dataset.DecodeAndNormalize(raw)
		if err != nil || odsDocument.Dataset.Layer != dataset.LayerODS {
			return dataset.Document{}, 0, nil, dataset.ErrPreviewUnsupported
		}
		odsDocuments[node.ID] = odsDocument
		virtualCount++
	}
	if virtualCount == 0 {
		return document, 0, nil, nil
	}
	expanded, typeOverrides, err := expandVirtualODSDocument(document, odsDocuments)
	return expanded, virtualCount, typeOverrides, err
}

func expandVirtualODSDocument(
	document dataset.Document,
	odsDocuments map[string]dataset.Document,
) (dataset.Document, map[string]map[string]string, error) {
	raw, err := json.Marshal(document)
	if err != nil {
		return dataset.Document{}, nil, dataset.ErrInvalidDocument
	}
	var expanded dataset.Document
	if err := json.Unmarshal(raw, &expanded); err != nil {
		return dataset.Document{}, nil, dataset.ErrInvalidDocument
	}

	fieldMappings := make(map[string]map[string]string, len(odsDocuments))
	typeOverrides := make(map[string]map[string]string, len(odsDocuments))
	odsSourceFilters := make(map[string][]dataset.SourceFilter, len(odsDocuments))
	for nodeID, odsDocument := range odsDocuments {
		if len(odsDocument.Nodes) != 1 ||
			odsDocument.Nodes[0].Type != "TABLE" ||
			len(odsDocument.Joins) != 0 ||
			len(odsDocument.PreAggregations) != 0 ||
			len(odsDocument.Filters) != 0 ||
			len(odsDocument.GroupBy) != 0 ||
			len(odsDocument.Having) != 0 ||
			odsDocument.Distinct {
			return dataset.Document{}, nil, dataset.ErrPreviewUnsupported
		}
		sourceNode := odsDocument.Nodes[0]
		mapping := make(map[string]string, len(odsDocument.Fields))
		overrides := make(map[string]string, len(odsDocument.Fields))
		for _, field := range odsDocument.Fields {
			if field.Code == "" ||
				field.Expression.Type != "FIELD_REF" ||
				field.Expression.NodeID != sourceNode.ID ||
				field.Expression.Field == "" {
				return dataset.Document{}, nil, dataset.ErrPreviewUnsupported
			}
			mapping[field.Code] = field.Expression.Field
			overrides[field.Expression.Field] = field.CanonicalType
		}
		fieldMappings[nodeID] = mapping
		typeOverrides[nodeID] = overrides
		filters := append([]dataset.SourceFilter(nil), sourceNode.SourceFilters...)
		for index := range filters {
			rebindSourceFilter(&filters[index], sourceNode.ID, nodeID, nil)
		}
		odsSourceFilters[nodeID] = filters
	}

	for index := range expanded.Nodes {
		node := &expanded.Nodes[index]
		odsDocument, virtual := odsDocuments[node.ID]
		if !virtual {
			continue
		}
		sourceNode := odsDocument.Nodes[0]
		mapping := fieldMappings[node.ID]
		projection := make([]string, len(node.Projection))
		for projectionIndex, code := range node.Projection {
			physical, exists := mapping[code]
			if !exists {
				return dataset.Document{}, nil, dataset.ErrInvalidDocument
			}
			projection[projectionIndex] = physical
		}
		for filterIndex := range node.SourceFilters {
			rebindSourceFilter(
				&node.SourceFilters[filterIndex], node.ID, node.ID, mapping,
			)
		}
		node.SourceFilters = append(
			odsSourceFilters[node.ID], node.SourceFilters...,
		)
		node.Type = "TABLE"
		node.DataSourceID = sourceNode.DataSourceID
		node.TableID = sourceNode.TableID
		node.DatasetVersionID = ""
		node.FileVersionID = sourceNode.FileVersionID
		node.Projection = projection
	}

	for fieldIndex := range expanded.Fields {
		rewriteExpression(&expanded.Fields[fieldIndex].Expression, fieldMappings)
	}
	for transformIndex := range expanded.Transforms {
		transform := &expanded.Transforms[transformIndex]
		for ruleIndex := range transform.Rules {
			rule := &transform.Rules[ruleIndex]
			for inputIndex, inputKey := range rule.InputKeys {
				rule.InputKeys[inputIndex] = rewriteTransformFieldKey(
					inputKey, fieldMappings,
				)
			}
			rule.ReplaceSourceKey = rewriteTransformFieldKey(
				rule.ReplaceSourceKey, fieldMappings,
			)
			rewriteExpression(&rule.Expression, fieldMappings)
		}
	}
	for filterIndex := range expanded.Filters {
		rewriteExpression(&expanded.Filters[filterIndex].Expression, fieldMappings)
	}
	for filterIndex := range expanded.Having {
		rewriteExpression(&expanded.Having[filterIndex].Expression, fieldMappings)
	}
	for joinIndex := range expanded.Joins {
		join := &expanded.Joins[joinIndex]
		for conditionIndex := range join.Conditions {
			rewriteExpression(
				&join.Conditions[conditionIndex].LeftExpression, fieldMappings,
			)
			rewriteExpression(
				&join.Conditions[conditionIndex].RightExpression, fieldMappings,
			)
		}
		rewriteJoinContracts(join, fieldMappings)
	}
	for preAggregationIndex := range expanded.PreAggregations {
		preAggregation := &expanded.PreAggregations[preAggregationIndex]
		mapping := fieldMappings[preAggregation.NodeID]
		for groupIndex := range preAggregation.GroupBy {
			if physical, exists := mapping[preAggregation.GroupBy[groupIndex].Field]; exists {
				preAggregation.GroupBy[groupIndex].Field = physical
			}
			if preAggregation.GroupBy[groupIndex].Expression != nil {
				rewriteExpression(
					preAggregation.GroupBy[groupIndex].Expression, fieldMappings,
				)
			}
		}
		for metricIndex := range preAggregation.Metrics {
			if physical, exists := mapping[preAggregation.Metrics[metricIndex].Field]; exists {
				preAggregation.Metrics[metricIndex].Field = physical
			}
			if preAggregation.Metrics[metricIndex].Expression != nil {
				rewriteExpression(
					preAggregation.Metrics[metricIndex].Expression, fieldMappings,
				)
			}
		}
	}
	return expanded, typeOverrides, nil
}

func rewriteTransformFieldKey(
	key string,
	mappings map[string]map[string]string,
) string {
	separator := strings.IndexByte(key, '.')
	if separator < 1 || separator == len(key)-1 {
		return key
	}
	componentID, field := key[:separator], key[separator+1:]
	if physical, exists := mappings[componentID][field]; exists {
		return componentID + "." + physical
	}
	return key
}

func rebindSourceFilter(
	filter *dataset.SourceFilter,
	fromNodeID, toNodeID string,
	mapping map[string]string,
) {
	if physical, exists := mapping[filter.Field]; exists {
		filter.Field = physical
	}
	if filter.Expression != nil {
		rewriteExpression(
			filter.Expression,
			map[string]map[string]string{fromNodeID: mapping},
		)
		rebindExpressionNode(filter.Expression, fromNodeID, toNodeID)
	}
}

func rebindExpressionNode(expression *dataset.Expression, from, to string) {
	if expression == nil {
		return
	}
	if expression.NodeID == from {
		expression.NodeID = to
	}
	rebindExpressionNode(expression.Argument, from, to)
	for index := range expression.Arguments {
		rebindExpressionNode(&expression.Arguments[index], from, to)
	}
	rebindExpressionNode(expression.Left, from, to)
	rebindExpressionNode(expression.Right, from, to)
	rebindExpressionNode(expression.Lower, from, to)
	rebindExpressionNode(expression.Upper, from, to)
	for index := range expression.Whens {
		rebindExpressionNode(&expression.Whens[index].When, from, to)
		rebindExpressionNode(&expression.Whens[index].Then, from, to)
	}
	for index := range expression.PartitionBy {
		rebindExpressionNode(&expression.PartitionBy[index], from, to)
	}
	for index := range expression.OrderBy {
		rebindExpressionNode(&expression.OrderBy[index].Expression, from, to)
	}
	rebindExpressionNode(expression.Else, from, to)
}

func rewriteExpression(
	expression *dataset.Expression,
	mappings map[string]map[string]string,
) {
	if expression == nil {
		return
	}
	if expression.Type == "FIELD_REF" {
		if physical, exists := mappings[expression.NodeID][expression.Field]; exists {
			expression.Field = physical
		}
	}
	rewriteExpression(expression.Argument, mappings)
	for index := range expression.Arguments {
		rewriteExpression(&expression.Arguments[index], mappings)
	}
	rewriteExpression(expression.Left, mappings)
	rewriteExpression(expression.Right, mappings)
	rewriteExpression(expression.Lower, mappings)
	rewriteExpression(expression.Upper, mappings)
	for index := range expression.Whens {
		rewriteExpression(&expression.Whens[index].When, mappings)
		rewriteExpression(&expression.Whens[index].Then, mappings)
	}
	for index := range expression.PartitionBy {
		rewriteExpression(&expression.PartitionBy[index], mappings)
	}
	for index := range expression.OrderBy {
		rewriteExpression(&expression.OrderBy[index].Expression, mappings)
	}
	rewriteExpression(expression.Else, mappings)
}

func rewriteJoinContracts(
	join *dataset.Join,
	mappings map[string]map[string]string,
) {
	if join.Bridge != nil {
		mapping := mappings[join.Bridge.BridgeNodeID]
		rewriteStringField(&join.Bridge.RelationshipTypeField, mapping)
		rewriteStringField(&join.Bridge.AllocationWeightField, mapping)
		rewriteStringField(&join.Bridge.PrimaryFlagField, mapping)
		rewriteStringField(&join.Bridge.ValidFromField, mapping)
		rewriteStringField(&join.Bridge.ValidToField, mapping)
	}
	if join.Temporal != nil {
		rewriteStringField(
			&join.Temporal.EventTimeField,
			mappings[join.Temporal.EventNodeID],
		)
		rewriteStringField(
			&join.Temporal.ValidFromField,
			mappings[join.Temporal.ValidityNodeID],
		)
		rewriteStringField(
			&join.Temporal.ValidToField,
			mappings[join.Temporal.ValidityNodeID],
		)
	}
}

func rewriteStringField(field *string, mapping map[string]string) {
	if physical, exists := mapping[*field]; exists {
		*field = physical
	}
}
