package federation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"intelligent-report-generation-system/internal/dataset"
	"intelligent-report-generation-system/internal/datasource"
	"intelligent-report-generation-system/internal/filequery"
)

var ErrJoinFanoutLimit = errors.New("federated join fanout exceeds the intermediate row limit")

type joinKeyStats struct {
	frequencies     map[string]int
	nullRows        int
	duplicateKeys   int
	maxMultiplicity int
}

// analyzeJoinRisks 使用已受源端限额约束的节点结果检查声明基数，并沿实际执行
// 顺序估算完整 Join 计划。告警只包含计数，不返回业务键值，避免诊断信息泄露。
func analyzeJoinRisks(ctx context.Context, document dataset.Document, tables map[string]filequery.NodeTableData) ([]datasource.QueryWarning, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	planRows, err := estimateJoinPlanRows(ctx, document, tables)
	if err != nil {
		return nil, err
	}
	warnings := make([]datasource.QueryWarning, 0)
	for _, join := range document.Joins {
		left, leftExists := tables[join.LeftNodeID]
		right, rightExists := tables[join.RightNodeID]
		if !leftExists || !rightExists {
			return nil, ErrInvalidSourceShape
		}
		leftStats, err := collectJoinKeyStats(ctx, left, join.Conditions, true)
		if err != nil {
			return nil, err
		}
		rightStats, err := collectJoinKeyStats(ctx, right, join.Conditions, false)
		if err != nil {
			return nil, err
		}
		_, fanoutKeys := estimateJoinRows(join.JoinType, leftStats, rightStats)
		estimatedRows := planRows[join.ID]
		if !join.ManualConfirmed {
			warnings = append(warnings, datasource.QueryWarning{
				Code: "JOIN_CONFIRMATION_REQUIRED", Message: "该关联尚未人工确认，请核对 Join 字段与基数后再用于正式分析。", JoinID: join.ID, EstimatedRows: estimatedRows,
			})
		}
		violatedSides := make([]string, 0, 2)
		if (join.Cardinality == "ONE_TO_ONE" || join.Cardinality == "ONE_TO_MANY") && leftStats.duplicateKeys > 0 {
			violatedSides = append(violatedSides, "左侧")
		}
		if (join.Cardinality == "ONE_TO_ONE" || join.Cardinality == "MANY_TO_ONE") && rightStats.duplicateKeys > 0 {
			violatedSides = append(violatedSides, "右侧")
		}
		if len(violatedSides) > 0 {
			warnings = append(warnings, datasource.QueryWarning{
				Code: "JOIN_CARDINALITY_MISMATCH", Message: fmt.Sprintf("声明的 %s 基数与预览数据不一致：%s Join 键存在重复。", join.Cardinality, joinSides(violatedSides)), JoinID: join.ID, EstimatedRows: estimatedRows,
			})
		}
		if join.Cardinality == "MANY_TO_MANY" {
			warnings = append(warnings, datasource.QueryWarning{
				Code: "JOIN_MANY_TO_MANY", Message: "多对多关联可能重复累计度量，请确认输出粒度或先聚合再关联。", JoinID: join.ID, EstimatedRows: estimatedRows,
			})
		}
		if fanoutKeys > 0 {
			warnings = append(warnings, datasource.QueryWarning{
				Code: "JOIN_FANOUT_RISK", Message: fmt.Sprintf("检测到 %d 组两侧重复 Join 键，左右单键最大重复数为 %d/%d，关联结果可能发生扇出。", fanoutKeys, leftStats.maxMultiplicity, rightStats.maxMultiplicity), JoinID: join.ID, EstimatedRows: estimatedRows,
			})
		}
	}
	return warnings, nil
}

func collectJoinKeyStats(ctx context.Context, table filequery.NodeTableData, conditions []dataset.JoinCondition, left bool) (joinKeyStats, error) {
	positions := make([]int, 0, len(conditions))
	for _, condition := range conditions {
		expression := condition.RightExpression
		if left {
			expression = condition.LeftExpression
		}
		position := -1
		for index, column := range table.Columns {
			if column == expression.Field {
				position = index
				break
			}
		}
		if position < 0 {
			return joinKeyStats{}, ErrInvalidSourceShape
		}
		positions = append(positions, position)
	}
	stats := joinKeyStats{frequencies: map[string]int{}}
	for rowIndex, row := range table.Rows {
		if err := checkRiskContext(ctx, rowIndex); err != nil {
			return joinKeyStats{}, err
		}
		if len(row) != len(table.Columns) {
			return joinKeyStats{}, ErrInvalidSourceShape
		}
		values := make([]any, 0, len(positions))
		hasNull := false
		for _, position := range positions {
			if row[position] == nil {
				hasNull = true
				break
			}
			values = append(values, row[position])
		}
		if hasNull {
			stats.nullRows++
			continue
		}
		encoded, err := json.Marshal(values)
		if err != nil {
			return joinKeyStats{}, ErrInvalidSourceShape
		}
		key := string(encoded)
		stats.frequencies[key]++
		if stats.frequencies[key] == 2 {
			stats.duplicateKeys++
		}
		stats.maxMultiplicity = max(stats.maxMultiplicity, stats.frequencies[key])
	}
	return stats, nil
}

type estimateRow map[string]any

// estimateJoinPlanRows 按执行器的有向 Join 顺序传播必要键值和实际重复次数，
// 因而能发现任意单边估算都未超限、但多级相乘后才出现的扇出。
func estimateJoinPlanRows(ctx context.Context, document dataset.Document, tables map[string]filequery.NodeTableData) (map[string]int, error) {
	rootID, joins, err := orderJoinPlan(document)
	if err != nil {
		return nil, err
	}
	required := requiredJoinFields(document)
	nodeRows := make(map[string][]estimateRow, len(document.Nodes))
	for _, node := range document.Nodes {
		table, exists := tables[node.ID]
		if !exists {
			return nil, ErrInvalidSourceShape
		}
		rows, err := estimateRowsForNode(ctx, node.ID, table, required[node.ID])
		if err != nil {
			return nil, err
		}
		nodeRows[node.ID] = rows
	}
	combined := cloneEstimateRows(nodeRows[rootID])
	estimates := make(map[string]int, len(joins))
	for _, join := range joins {
		combined, err = estimateJoinStep(ctx, combined, nodeRows[join.RightNodeID], join)
		if err != nil {
			return nil, err
		}
		estimates[join.ID] = len(combined)
	}
	return estimates, nil
}

func orderJoinPlan(document dataset.Document) (string, []dataset.Join, error) {
	incoming := map[string]bool{}
	for _, join := range document.Joins {
		incoming[join.RightNodeID] = true
	}
	rootID := document.Nodes[0].ID
	for _, node := range document.Nodes {
		if !incoming[node.ID] {
			rootID = node.ID
			break
		}
	}
	joined := map[string]bool{rootID: true}
	remaining := append([]dataset.Join(nil), document.Joins...)
	ordered := make([]dataset.Join, 0, len(remaining))
	for len(remaining) > 0 {
		progressed := false
		next := make([]dataset.Join, 0, len(remaining))
		for _, join := range remaining {
			if !joined[join.LeftNodeID] || joined[join.RightNodeID] {
				next = append(next, join)
				continue
			}
			ordered = append(ordered, join)
			joined[join.RightNodeID], progressed = true, true
		}
		if !progressed {
			return "", nil, errors.New("federated join order cannot preserve the declared direction")
		}
		remaining = next
	}
	return rootID, ordered, nil
}

func requiredJoinFields(document dataset.Document) map[string]map[string]bool {
	required := map[string]map[string]bool{}
	for _, node := range document.Nodes {
		required[node.ID] = map[string]bool{}
	}
	for _, join := range document.Joins {
		for _, condition := range join.Conditions {
			required[condition.LeftExpression.NodeID][condition.LeftExpression.Field] = true
			required[condition.RightExpression.NodeID][condition.RightExpression.Field] = true
		}
	}
	return required
}

func estimateRowsForNode(ctx context.Context, nodeID string, table filequery.NodeTableData, required map[string]bool) ([]estimateRow, error) {
	positions := map[int]string{}
	for index, column := range table.Columns {
		if required[column] {
			positions[index] = nodeID + "." + column
		}
	}
	if len(positions) != len(required) {
		return nil, ErrInvalidSourceShape
	}
	rows := make([]estimateRow, len(table.Rows))
	for rowIndex, source := range table.Rows {
		if err := checkRiskContext(ctx, rowIndex); err != nil {
			return nil, err
		}
		if len(source) != len(table.Columns) {
			return nil, ErrInvalidSourceShape
		}
		row := make(estimateRow, len(positions))
		for position, key := range positions {
			row[key] = source[position]
		}
		rows[rowIndex] = row
	}
	return rows, nil
}

func estimateJoinStep(ctx context.Context, leftRows, rightRows []estimateRow, join dataset.Join) ([]estimateRow, error) {
	index := map[string][]int{}
	for rowIndex, row := range rightRows {
		if err := checkRiskContext(ctx, rowIndex); err != nil {
			return nil, err
		}
		key, ok, err := estimateJoinKey(join.Conditions, row, false)
		if err != nil {
			return nil, err
		}
		if ok {
			index[key] = append(index[key], rowIndex)
		}
	}
	matchedRight := make([]bool, len(rightRows))
	estimated := int64(0)
	for rowIndex, left := range leftRows {
		if err := checkRiskContext(ctx, rowIndex); err != nil {
			return nil, err
		}
		key, ok, err := estimateJoinKey(join.Conditions, left, true)
		if err != nil {
			return nil, err
		}
		matches := index[key]
		if !ok {
			matches = nil
		}
		if len(matches) == 0 {
			if join.JoinType == "LEFT" || join.JoinType == "FULL" {
				estimated++
			}
			continue
		}
		estimated += int64(len(matches))
		for _, rightIndex := range matches {
			matchedRight[rightIndex] = true
		}
	}
	if join.JoinType == "RIGHT" || join.JoinType == "FULL" {
		for index := range rightRows {
			if !matchedRight[index] {
				estimated++
			}
		}
	}
	if estimated > int64(filequery.MaxIntermediateRows) {
		return nil, fmt.Errorf("%w: join %s estimates %d rows", ErrJoinFanoutLimit, join.ID, estimated)
	}
	result := make([]estimateRow, 0, int(estimated))
	matchedRight = make([]bool, len(rightRows))
	for rowIndex, left := range leftRows {
		if err := checkRiskContext(ctx, rowIndex); err != nil {
			return nil, err
		}
		key, ok, err := estimateJoinKey(join.Conditions, left, true)
		if err != nil {
			return nil, err
		}
		matches := index[key]
		if !ok {
			matches = nil
		}
		if len(matches) == 0 && (join.JoinType == "LEFT" || join.JoinType == "FULL") {
			result = append(result, cloneEstimateRow(left))
		}
		for _, rightIndex := range matches {
			matchedRight[rightIndex] = true
			result = append(result, mergeEstimateRows(left, rightRows[rightIndex]))
		}
	}
	if join.JoinType == "RIGHT" || join.JoinType == "FULL" {
		for index, row := range rightRows {
			if !matchedRight[index] {
				result = append(result, cloneEstimateRow(row))
			}
		}
	}
	return result, nil
}

func estimateJoinKey(conditions []dataset.JoinCondition, row estimateRow, left bool) (string, bool, error) {
	values := make([]any, 0, len(conditions))
	for _, condition := range conditions {
		expression := condition.RightExpression
		if left {
			expression = condition.LeftExpression
		}
		value := row[expression.NodeID+"."+expression.Field]
		if value == nil {
			return "", false, nil
		}
		values = append(values, value)
	}
	payload, err := json.Marshal(values)
	return string(payload), true, err
}

func cloneEstimateRows(rows []estimateRow) []estimateRow {
	result := make([]estimateRow, len(rows))
	for index, row := range rows {
		result[index] = cloneEstimateRow(row)
	}
	return result
}

func cloneEstimateRow(row estimateRow) estimateRow {
	result := make(estimateRow, len(row))
	for key, value := range row {
		result[key] = value
	}
	return result
}

func mergeEstimateRows(left, right estimateRow) estimateRow {
	result := make(estimateRow, len(left)+len(right))
	for key, value := range left {
		result[key] = value
	}
	for key, value := range right {
		result[key] = value
	}
	return result
}

func checkRiskContext(ctx context.Context, index int) error {
	if index%1024 != 0 {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}

func estimateJoinRows(joinType string, left, right joinKeyStats) (int, int) {
	estimated, fanoutKeys := 0, 0
	seen := map[string]bool{}
	for key, leftCount := range left.frequencies {
		seen[key] = true
		rightCount := right.frequencies[key]
		if rightCount > 0 {
			estimated += leftCount * rightCount
			if leftCount > 1 && rightCount > 1 {
				fanoutKeys++
			}
		} else if joinType == "LEFT" || joinType == "FULL" {
			estimated += leftCount
		}
	}
	for key, rightCount := range right.frequencies {
		if !seen[key] && (joinType == "RIGHT" || joinType == "FULL") {
			estimated += rightCount
		}
	}
	if joinType == "LEFT" || joinType == "FULL" {
		estimated += left.nullRows
	}
	if joinType == "RIGHT" || joinType == "FULL" {
		estimated += right.nullRows
	}
	return estimated, fanoutKeys
}

func joinSides(values []string) string {
	if len(values) == 1 {
		return values[0]
	}
	return values[0] + "和" + values[1]
}
