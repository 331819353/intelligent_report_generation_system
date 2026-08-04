package queryruntime

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
	"intelligent-report-generation-system/internal/dataset"
)

const odsSourcePreviewRows = 10

// expandVirtualODSNodesTx resolves published ODS versions as logical mappings,
// not warehouse relations. For DWD preview it also unfolds one-source DIM
// contracts back to the same bounded source-preview plane, preserving the DIM
// field cleaning expressions. This avoids mixing mutable source samples with a
// governed warehouse relation while still allowing the legal ODS + DIM DWD DAG
// to be previewed end to end.
func expandVirtualODSNodesTx(
	ctx context.Context,
	tx pgx.Tx,
	document dataset.Document,
) (dataset.Document, int, map[string]map[string]string, error) {
	odsDocuments := map[string]dataset.Document{}
	dimDocuments := map[string]dataset.Document{}
	dimODSDocuments := map[string]dataset.Document{}
	virtualCount := 0
	for _, node := range document.Nodes {
		if node.Type != "DATASET" || node.DatasetVersionID == "" {
			return dataset.Document{}, 0, nil, dataset.ErrInvalidDocument
		}
		source, datasetID, err := loadPublishedVirtualDocumentTx(
			ctx, tx, node.DatasetVersionID,
		)
		if err != nil {
			return dataset.Document{}, 0, nil, err
		}
		if source.Dataset.Layer != dataset.LayerODS &&
			source.Dataset.Layer != dataset.LayerDIM {
			continue
		}
		if err := dataset.ValidateVersionDependenciesInTx(
			ctx, tx, datasetID, node.DatasetVersionID,
		); err != nil {
			return dataset.Document{}, 0, nil, dataset.ErrPreviewUnsupported
		}
		if source.Dataset.Layer == dataset.LayerODS {
			odsDocuments[node.ID] = source
			virtualCount++
			continue
		}
		if len(source.Nodes) != 1 ||
			source.Nodes[0].Type != "DATASET" ||
			source.Nodes[0].DatasetVersionID == "" {
			return dataset.Document{}, 0, nil, dataset.ErrPreviewUnsupported
		}
		odsDocument, odsDatasetID, err := loadPublishedVirtualDocumentTx(
			ctx, tx, source.Nodes[0].DatasetVersionID,
		)
		if err != nil {
			return dataset.Document{}, 0, nil, err
		}
		if odsDocument.Dataset.Layer != dataset.LayerODS {
			return dataset.Document{}, 0, nil, dataset.ErrPreviewUnsupported
		}
		if err := dataset.ValidateVersionDependenciesInTx(
			ctx, tx, odsDatasetID, source.Nodes[0].DatasetVersionID,
		); err != nil {
			return dataset.Document{}, 0, nil, dataset.ErrPreviewUnsupported
		}
		dimDocuments[node.ID] = source
		dimODSDocuments[node.ID] = odsDocument
		virtualCount++
	}
	if virtualCount == 0 {
		return document, 0, nil, nil
	}
	expanded, typeOverrides, err := expandVirtualODSDocument(
		document, odsDocuments,
	)
	if err != nil {
		return dataset.Document{}, 0, nil, err
	}
	if len(dimDocuments) > 0 {
		var dimOverrides map[string]map[string]string
		expanded, dimOverrides, err = expandVirtualDIMDocument(
			expanded, dimDocuments, dimODSDocuments,
		)
		for nodeID, overrides := range dimOverrides {
			typeOverrides[nodeID] = overrides
		}
	}
	if err == nil {
		normalizeVirtualExecutionType(&expanded)
	}
	return expanded, virtualCount, typeOverrides, err
}

func normalizeVirtualExecutionType(document *dataset.Document) {
	sourceIDs := map[string]bool{}
	for _, node := range document.Nodes {
		if node.Type == "TABLE" && node.DataSourceID != "" {
			sourceIDs[node.DataSourceID] = true
		}
	}
	if len(sourceIDs) > 1 {
		document.Dataset.Type = "CROSS_SOURCE"
		return
	}
	if len(sourceIDs) == 1 {
		document.Dataset.Type = "SINGLE_SOURCE"
	}
}

func loadPublishedVirtualDocumentTx(
	ctx context.Context,
	tx pgx.Tx,
	versionID string,
) (dataset.Document, string, error) {
	var raw json.RawMessage
	var datasetID string
	err := tx.QueryRow(ctx, `SELECT owner.id::text,version.dsl_json
		FROM platform.dataset_versions AS version
		JOIN platform.datasets AS owner
		  ON owner.id=version.dataset_id
		 AND owner.tenant_id=version.tenant_id
		WHERE version.id::text=$1
		  AND version.status='PUBLISHED'
		  AND owner.status='PUBLISHED'
		  AND owner.current_published_version_id=version.id
		  AND owner.deleted_at IS NULL
		FOR SHARE OF version,owner`, versionID).
		Scan(&datasetID, &raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return dataset.Document{}, "", dataset.ErrPreviewUnsupported
	}
	if err != nil {
		return dataset.Document{}, "", err
	}
	document, err := dataset.DecodeAndNormalize(raw)
	if err != nil {
		return dataset.Document{}, "", dataset.ErrPreviewUnsupported
	}
	return document, datasetID, nil
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

func expandVirtualDIMDocument(
	document dataset.Document,
	dimDocuments map[string]dataset.Document,
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

	expressionMappings := make(
		map[string]map[string]dataset.Expression, len(dimDocuments),
	)
	simpleMappings := make(map[string]map[string]string, len(dimDocuments))
	typeOverrides := make(map[string]map[string]string, len(dimDocuments))
	hasDistinctDIM := false
	for nodeIndex := range expanded.Nodes {
		parentNode := &expanded.Nodes[nodeIndex]
		dimDocument, virtual := dimDocuments[parentNode.ID]
		if !virtual {
			continue
		}
		odsDocument := odsDocuments[parentNode.ID]
		if len(dimDocument.Nodes) != 1 ||
			dimDocument.Nodes[0].Type != "DATASET" ||
			len(dimDocument.Joins) != 0 ||
			len(dimDocument.PreAggregations) != 0 ||
			len(dimDocument.Filters) != 0 ||
			len(dimDocument.GroupBy) != 0 ||
			len(dimDocument.Having) != 0 ||
			len(odsDocument.Nodes) != 1 ||
			odsDocument.Nodes[0].Type != "TABLE" ||
			len(odsDocument.Joins) != 0 ||
			len(odsDocument.PreAggregations) != 0 ||
			len(odsDocument.Filters) != 0 ||
			len(odsDocument.GroupBy) != 0 ||
			len(odsDocument.Having) != 0 ||
			odsDocument.Distinct {
			return dataset.Document{}, nil, dataset.ErrPreviewUnsupported
		}

		dimSourceNode := dimDocument.Nodes[0]
		odsSourceNode := odsDocument.Nodes[0]
		odsMapping := make(map[string]string, len(odsDocument.Fields))
		overrides := make(map[string]string, len(odsDocument.Fields))
		for _, field := range odsDocument.Fields {
			if field.Code == "" ||
				field.Expression.Type != "FIELD_REF" ||
				field.Expression.NodeID != odsSourceNode.ID ||
				field.Expression.Field == "" {
				return dataset.Document{}, nil, dataset.ErrPreviewUnsupported
			}
			odsMapping[field.Code] = field.Expression.Field
			overrides[field.Expression.Field] = field.CanonicalType
		}

		fieldExpressions := make(
			map[string]dataset.Expression, len(dimDocument.Fields),
		)
		simple := make(map[string]string, len(dimDocument.Fields))
		for _, field := range dimDocument.Fields {
			expression := cloneDatasetExpression(field.Expression)
			rewriteExpression(
				&expression,
				map[string]map[string]string{dimSourceNode.ID: odsMapping},
			)
			rebindExpressionNode(
				&expression, dimSourceNode.ID, parentNode.ID,
			)
			fieldExpressions[field.Code] = expression
			if expression.Type == "FIELD_REF" &&
				expression.NodeID == parentNode.ID {
				simple[field.Code] = expression.Field
			}
		}
		for _, projected := range parentNode.Projection {
			if _, exists := fieldExpressions[projected]; !exists {
				return dataset.Document{}, nil, dataset.ErrInvalidDocument
			}
		}
		typeOverrides[parentNode.ID] = overrides

		sourceFilters := append(
			[]dataset.SourceFilter(nil), odsSourceNode.SourceFilters...,
		)
		for filterIndex := range sourceFilters {
			rebindSourceFilter(
				&sourceFilters[filterIndex],
				odsSourceNode.ID, parentNode.ID, nil,
			)
		}
		dimFilters := append(
			[]dataset.SourceFilter(nil), dimSourceNode.SourceFilters...,
		)
		for filterIndex := range dimFilters {
			rebindSourceFilter(
				&dimFilters[filterIndex],
				dimSourceNode.ID, parentNode.ID, odsMapping,
			)
		}
		parentFilters := append(
			[]dataset.SourceFilter(nil), parentNode.SourceFilters...,
		)
		if dimDocument.Distinct {
			if len(parentFilters) != 0 {
				return dataset.Document{}, nil, dataset.ErrPreviewUnsupported
			}
			joinID, joinSide, found := previewJoinSlot(
				expanded.Joins, parentNode.ID,
			)
			if !found {
				return dataset.Document{}, nil, dataset.ErrPreviewUnsupported
			}
			groups := make(
				[]dataset.PreAggregationGroup, 0, len(parentNode.Projection),
			)
			for _, code := range parentNode.Projection {
				expression := cloneDatasetExpression(fieldExpressions[code])
				groups = append(groups, dataset.PreAggregationGroup{
					Field: code, Expression: &expression,
				})
			}
			expanded.PreAggregations = append(
				expanded.PreAggregations,
				dataset.PreAggregation{
					ID:       "preview_dim_" + parentNode.ID,
					NodeID:   parentNode.ID,
					JoinID:   joinID,
					JoinSide: joinSide,
					GroupBy:  groups,
					// The private count is not projected by the DWD. It only
					// satisfies the canonical pre-join grouping shape while
					// preserving the published DIM's DISTINCT contract.
					Metrics: []dataset.PreAggregationMetric{{
						Field: "preview_row_count", Function: "COUNT",
						CountRows: true,
					}},
				},
			)
			hasDistinctDIM = true
		} else {
			expressionMappings[parentNode.ID] = fieldExpressions
			simpleMappings[parentNode.ID] = simple
			for filterIndex := range parentFilters {
				if err := rewriteVirtualSourceFilter(
					&parentFilters[filterIndex],
					parentNode.ID, fieldExpressions,
				); err != nil {
					return dataset.Document{}, nil, err
				}
			}
		}

		parentID, parentAlias := parentNode.ID, parentNode.Alias
		*parentNode = odsSourceNode
		parentNode.ID = parentID
		parentNode.Alias = parentAlias
		parentNode.SourceFilters = append(
			append(sourceFilters, dimFilters...), parentFilters...,
		)
		parentNode.Projection = append(
			[]string(nil), odsSourceNode.Projection...,
		)
	}
	if hasDistinctDIM {
		// The private marker is not serialized, so persisted DWD documents
		// remain unable to introduce aggregation. It applies only to this
		// server-derived plan that replays a published DIM DISTINCT contract.
		expanded = dataset.AsSourcePreviewExecution(expanded)
	}

	for fieldIndex := range expanded.Fields {
		rewriteExpressionMappings(
			&expanded.Fields[fieldIndex].Expression, expressionMappings,
		)
	}
	for transformIndex := range expanded.Transforms {
		transform := &expanded.Transforms[transformIndex]
		for ruleIndex := range transform.Rules {
			rule := &transform.Rules[ruleIndex]
			for inputIndex, inputKey := range rule.InputKeys {
				rule.InputKeys[inputIndex] = rewriteTransformFieldKey(
					inputKey, simpleMappings,
				)
			}
			rule.ReplaceSourceKey = rewriteTransformFieldKey(
				rule.ReplaceSourceKey, simpleMappings,
			)
			rewriteExpressionMappings(
				&rule.Expression, expressionMappings,
			)
		}
	}
	for filterIndex := range expanded.Filters {
		rewriteExpressionMappings(
			&expanded.Filters[filterIndex].Expression, expressionMappings,
		)
	}
	for filterIndex := range expanded.Having {
		rewriteExpressionMappings(
			&expanded.Having[filterIndex].Expression, expressionMappings,
		)
	}
	for joinIndex := range expanded.Joins {
		join := &expanded.Joins[joinIndex]
		for conditionIndex := range join.Conditions {
			rewriteExpressionMappings(
				&join.Conditions[conditionIndex].LeftExpression,
				expressionMappings,
			)
			rewriteExpressionMappings(
				&join.Conditions[conditionIndex].RightExpression,
				expressionMappings,
			)
		}
		rewriteJoinContracts(join, simpleMappings)
	}
	for preAggregationIndex := range expanded.PreAggregations {
		preAggregation := &expanded.PreAggregations[preAggregationIndex]
		mapping := simpleMappings[preAggregation.NodeID]
		for groupIndex := range preAggregation.GroupBy {
			rewriteStringField(
				&preAggregation.GroupBy[groupIndex].Field, mapping,
			)
			if preAggregation.GroupBy[groupIndex].Expression != nil {
				rewriteExpressionMappings(
					preAggregation.GroupBy[groupIndex].Expression,
					expressionMappings,
				)
			}
		}
		for metricIndex := range preAggregation.Metrics {
			rewriteStringField(
				&preAggregation.Metrics[metricIndex].Field, mapping,
			)
			if preAggregation.Metrics[metricIndex].Expression != nil {
				rewriteExpressionMappings(
					preAggregation.Metrics[metricIndex].Expression,
					expressionMappings,
				)
			}
		}
	}
	return expanded, typeOverrides, nil
}

func previewJoinSlot(
	joins []dataset.Join,
	nodeID string,
) (string, string, bool) {
	for _, join := range joins {
		if join.LeftNodeID == nodeID {
			return join.ID, "LEFT", true
		}
		if join.RightNodeID == nodeID {
			return join.ID, "RIGHT", true
		}
	}
	return "", "", false
}

func rewriteVirtualSourceFilter(
	filter *dataset.SourceFilter,
	nodeID string,
	mapping map[string]dataset.Expression,
) error {
	if filter.Field != "" {
		expression, exists := mapping[filter.Field]
		if !exists ||
			expression.Type != "FIELD_REF" ||
			expression.NodeID != nodeID {
			return dataset.ErrPreviewUnsupported
		}
		filter.Field = expression.Field
	}
	if filter.Expression != nil {
		rewriteExpressionMappings(
			filter.Expression,
			map[string]map[string]dataset.Expression{nodeID: mapping},
		)
	}
	return nil
}

func rewriteExpressionMappings(
	expression *dataset.Expression,
	mappings map[string]map[string]dataset.Expression,
) {
	if expression == nil {
		return
	}
	if expression.Type == "FIELD_REF" {
		if replacement, exists :=
			mappings[expression.NodeID][expression.Field]; exists {
			*expression = cloneDatasetExpression(replacement)
			return
		}
	}
	rewriteExpressionMappings(expression.Argument, mappings)
	for index := range expression.Arguments {
		rewriteExpressionMappings(&expression.Arguments[index], mappings)
	}
	rewriteExpressionMappings(expression.Left, mappings)
	rewriteExpressionMappings(expression.Right, mappings)
	rewriteExpressionMappings(expression.Lower, mappings)
	rewriteExpressionMappings(expression.Upper, mappings)
	for index := range expression.Whens {
		rewriteExpressionMappings(&expression.Whens[index].When, mappings)
		rewriteExpressionMappings(&expression.Whens[index].Then, mappings)
	}
	for index := range expression.PartitionBy {
		rewriteExpressionMappings(&expression.PartitionBy[index], mappings)
	}
	for index := range expression.OrderBy {
		rewriteExpressionMappings(
			&expression.OrderBy[index].Expression, mappings,
		)
	}
	rewriteExpressionMappings(expression.Else, mappings)
}

func cloneDatasetExpression(value dataset.Expression) dataset.Expression {
	clone := value
	if value.Argument != nil {
		argument := cloneDatasetExpression(*value.Argument)
		clone.Argument = &argument
	}
	clone.Arguments = make([]dataset.Expression, len(value.Arguments))
	for index := range value.Arguments {
		clone.Arguments[index] = cloneDatasetExpression(value.Arguments[index])
	}
	clone.Left = cloneDatasetExpressionPointer(value.Left)
	clone.Right = cloneDatasetExpressionPointer(value.Right)
	clone.Lower = cloneDatasetExpressionPointer(value.Lower)
	clone.Upper = cloneDatasetExpressionPointer(value.Upper)
	clone.Whens = append([]dataset.CaseBranch(nil), value.Whens...)
	for index := range clone.Whens {
		clone.Whens[index].When = cloneDatasetExpression(
			value.Whens[index].When,
		)
		clone.Whens[index].Then = cloneDatasetExpression(
			value.Whens[index].Then,
		)
	}
	clone.Else = cloneDatasetExpressionPointer(value.Else)
	clone.PartitionBy = make([]dataset.Expression, len(value.PartitionBy))
	for index := range value.PartitionBy {
		clone.PartitionBy[index] = cloneDatasetExpression(
			value.PartitionBy[index],
		)
	}
	clone.OrderBy = append([]dataset.WindowOrder(nil), value.OrderBy...)
	for index := range clone.OrderBy {
		clone.OrderBy[index].Expression = cloneDatasetExpression(
			value.OrderBy[index].Expression,
		)
	}
	return clone
}

func cloneDatasetExpressionPointer(
	value *dataset.Expression,
) *dataset.Expression {
	if value == nil {
		return nil
	}
	clone := cloneDatasetExpression(*value)
	return &clone
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
