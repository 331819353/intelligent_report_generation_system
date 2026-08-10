package suites

import (
	"errors"
	"math"
)

var ErrInvalidAgreement = errors.New("agreement sample is invalid")

type AgreementPair struct {
	Human    bool
	Verifier bool
}

// CohensKappa calculates agreement beyond chance for two binary raters. A
// constant, perfectly agreeing sample is treated as 1 rather than NaN.
func CohensKappa(pairs []AgreementPair) (float64, error) {
	if len(pairs) < 2 {
		return 0, ErrInvalidAgreement
	}
	var agree, humanPositive, verifierPositive int
	for _, pair := range pairs {
		if pair.Human == pair.Verifier {
			agree++
		}
		if pair.Human {
			humanPositive++
		}
		if pair.Verifier {
			verifierPositive++
		}
	}
	n := float64(len(pairs))
	observed := float64(agree) / n
	humanRate := float64(humanPositive) / n
	verifierRate := float64(verifierPositive) / n
	expected := humanRate*verifierRate + (1-humanRate)*(1-verifierRate)
	if math.Abs(1-expected) <= 1e-12 {
		if observed == 1 {
			return 1, nil
		}
		return 0, ErrInvalidAgreement
	}
	return (observed - expected) / (1 - expected), nil
}
