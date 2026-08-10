package compiler

import "intelligent-report-generation-system/internal/askdata/registry"

func compileBridgeJoin(request JoinCompileRequest) (string, error) {
	if request.Bridge == nil || request.Relationship.JoinType != registry.JoinInner {
		return "", ErrInvalidJoinContract
	}
	condition, _ := decodeRelationshipJoinAST(request.Relationship.JoinAST)
	if condition.LeftFieldID != request.Bridge.LeftSourceColumn ||
		condition.RightFieldID != request.Bridge.RightSourceColumn {
		return "", ErrInvalidJoinContract
	}
	left, err := compileGroupedSource(request.Left, []string{request.Bridge.LeftSourceColumn})
	if err != nil {
		return "", err
	}
	right, err := compileGroupedSource(request.Right, []string{request.Bridge.RightSourceColumn})
	if err != nil {
		return "", err
	}
	bridgeAlias := request.Bridge.Source.Alias
	bridge := "SELECT DISTINCT " +
		qualified(bridgeAlias, request.Bridge.LeftBridgeColumn) + " AS " +
		quoteJoinIdentifier(request.Bridge.LeftBridgeColumn) + ", " +
		qualified(bridgeAlias, request.Bridge.RightBridgeColumn) + " AS " +
		quoteJoinIdentifier(request.Bridge.RightBridgeColumn) + " FROM " +
		sourceRelation(request.Bridge.Source)
	leftCTE := request.Left.Alias + "_pre"
	rightCTE := request.Right.Alias + "_pre"
	bridgeCTE := bridgeAlias + "_dedup"
	if !validJoinIdentifier(leftCTE) || !validJoinIdentifier(rightCTE) ||
		!validJoinIdentifier(bridgeCTE) {
		return "", ErrInvalidJoinContract
	}
	return "WITH " + quoteJoinIdentifier(leftCTE) + " AS (" + left + "), " +
		quoteJoinIdentifier(rightCTE) + " AS (" + right + "), " +
		quoteJoinIdentifier(bridgeCTE) + " AS (" + bridge + ") " +
		"SELECT * FROM " + quoteJoinIdentifier(leftCTE) + " AS " + quoteJoinIdentifier(request.Left.Alias) +
		" INNER JOIN " + quoteJoinIdentifier(bridgeCTE) + " AS " + quoteJoinIdentifier(bridgeAlias) +
		" ON " + qualified(request.Left.Alias, request.Bridge.LeftSourceColumn) + " = " +
		qualified(bridgeAlias, request.Bridge.LeftBridgeColumn) +
		" INNER JOIN " + quoteJoinIdentifier(rightCTE) + " AS " + quoteJoinIdentifier(request.Right.Alias) +
		" ON " + qualified(bridgeAlias, request.Bridge.RightBridgeColumn) + " = " +
		qualified(request.Right.Alias, request.Bridge.RightSourceColumn), nil
}
