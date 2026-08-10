package registry

import "errors"

var ErrInvalidRelationshipCombination = errors.New("relationship cardinality and fanout policy combination is invalid")

// ValidateRelationshipCombination is the single application-side copy of the
// rel_combination_valid and rel_bridge_required database constraints. Empty
// values are never accepted here: NULL exists only as a migration holding
// state and must fail closed before certification, graph use or compilation.
func ValidateRelationshipCombination(
	cardinality Cardinality,
	policy FanoutPolicy,
	bridgeModelVersionID string,
) error {
	valid := false
	switch cardinality {
	case CardinalityOneToOne, CardinalityManyToOne:
		valid = policy == FanoutSafe || policy == FanoutBlock
	case CardinalityOneToMany:
		valid = policy == FanoutPreAggregateRequired || policy == FanoutBlock
	case CardinalityManyToMany:
		valid = policy == FanoutBridgeRequired || policy == FanoutBlock
	}
	if !valid || policy == FanoutBridgeRequired && bridgeModelVersionID == "" {
		return ErrInvalidRelationshipCombination
	}
	return nil
}

func ValidCardinality(value Cardinality) bool {
	switch value {
	case CardinalityOneToOne, CardinalityOneToMany, CardinalityManyToOne, CardinalityManyToMany:
		return true
	default:
		return false
	}
}

func ValidFanoutPolicy(value FanoutPolicy) bool {
	switch value {
	case FanoutSafe, FanoutPreAggregateRequired, FanoutBridgeRequired, FanoutBlock:
		return true
	default:
		return false
	}
}
