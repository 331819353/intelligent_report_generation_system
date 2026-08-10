package answer

import (
	"errors"
	"math/big"
)

type DerivationName string

const (
	DerivationDifference DerivationName = "DIFFERENCE"
	DerivationRatio      DerivationName = "RATIO"
	DerivationPercentage DerivationName = "PERCENTAGE"
	DerivationShare      DerivationName = "SHARE"
	DerivationYoYGrowth  DerivationName = "YOY_GROWTH"
	// DerivationYearOverYear is the public evidence-contract name.
	DerivationYearOverYear = DerivationYoYGrowth
)

type DerivationRule struct {
	Name  DerivationName
	apply func(left, right *big.Rat) (*big.Rat, error)
}

func fixedDerivationRules() []DerivationRule {
	return []DerivationRule{
		{Name: DerivationDifference, apply: func(left, right *big.Rat) (*big.Rat, error) {
			return new(big.Rat).Sub(left, right), nil
		}},
		{Name: DerivationRatio, apply: divideRationals},
		{Name: DerivationPercentage, apply: divideRationals},
		{Name: DerivationShare, apply: divideRationals},
		{Name: DerivationYoYGrowth, apply: func(left, right *big.Rat) (*big.Rat, error) {
			if right.Sign() == 0 {
				return nil, errors.New("YoY baseline is zero")
			}
			return new(big.Rat).Quo(new(big.Rat).Sub(left, right), right), nil
		}},
	}
}

func divideRationals(left, right *big.Rat) (*big.Rat, error) {
	if right.Sign() == 0 {
		return nil, errors.New("derivation denominator is zero")
	}
	return new(big.Rat).Quo(left, right), nil
}

func derivationOutputKind(name DerivationName, left, right NumericKind) NumericKind {
	switch name {
	case DerivationPercentage, DerivationShare, DerivationYoYGrowth:
		return NumericPercent
	case DerivationDifference:
		if left == NumericPercent && right == NumericPercent {
			return NumericPercentagePoint
		}
		return NumericScalar
	default:
		return NumericScalar
	}
}
