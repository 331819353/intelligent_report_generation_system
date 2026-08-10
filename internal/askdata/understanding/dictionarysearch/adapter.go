package dictionarysearch

import (
	"errors"
	"strings"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/search"
	"intelligent-report-generation-system/internal/askdata/understanding"
)

var ErrInvalidDictionaryEvidence = errors.New("invalid governed dictionary exact evidence")

// ExactHits converts policy-filtered dictionary hits into the independent
// deterministic EXACT lane consumed by semantic retrieval and the Binder.
func ExactHits(
	scope askdata.PolicyScope,
	mention string,
	hits []understanding.DictionaryHit,
) ([]search.RawHit, error) {
	if scope.Validate() != nil || strings.TrimSpace(mention) == "" {
		return nil, ErrInvalidDictionaryEvidence
	}
	normalizedMention, err := understanding.NormalizeQuestion(mention)
	if err != nil {
		return nil, ErrInvalidDictionaryEvidence
	}
	domains := make(map[askdata.ID]struct{}, len(scope.DomainIDs))
	for _, domainID := range scope.DomainIDs {
		domains[domainID] = struct{}{}
	}
	result := []search.RawHit{}
	seen := map[string]struct{}{}
	for _, hit := range hits {
		if err := hit.Validate(); err != nil {
			return nil, ErrInvalidDictionaryEvidence
		}
		if _, allowed := domains[hit.DomainID]; !allowed ||
			hit.NormalizedText != normalizedMention.Normalized {
			continue
		}
		objectType, supported := targetObjectType(hit.TargetObjectType)
		if !supported {
			continue
		}
		key := string(objectType) + "\x00" + string(hit.TargetVersionID)
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, search.RawHit{
			ObjectType: objectType, ObjectVersionID: hit.TargetVersionID,
			InputHash: hit.EvidenceHash, Score: 1,
		})
	}
	return result, nil
}

// ExactMatches projects the same governed hits into the trusted evidence
// boundary consumed before the understanding reviewer. MEMBER targets stay in
// the dedicated sensitive/member resolution path and are not mixed here.
func ExactMatches(hits []understanding.DictionaryHit) ([]understanding.ExactMatch, error) {
	if len(hits) > 128 {
		return nil, ErrInvalidDictionaryEvidence
	}
	result := []understanding.ExactMatch{}
	for _, hit := range hits {
		if err := hit.Validate(); err != nil {
			return nil, ErrInvalidDictionaryEvidence
		}
		objectType, supported := targetObjectType(hit.TargetObjectType)
		if !supported || objectType == search.ObjectMember {
			continue
		}
		evidence := askdata.EvidenceRef{
			EvidenceID: askdata.ID("dictionary:" + string(hit.TermVersionID)),
			Kind:       askdata.EvidenceKindExactAlias, SourceID: hit.TargetVersionID,
			ContentHash: hit.EvidenceHash,
		}
		if evidence.Validate() != nil {
			return nil, ErrInvalidDictionaryEvidence
		}
		canonicalLabel := hit.TargetCode
		if canonicalLabel == "" {
			canonicalLabel = hit.Term
		}
		result = append(result, understanding.ExactMatch{
			ObjectType: objectType, CanonicalLabel: canonicalLabel, Text: hit.OriginalText,
			Span: hit.OriginalSpan, Evidence: evidence,
		})
	}
	return result, nil
}

func targetObjectType(value string) (search.ObjectType, bool) {
	switch value {
	case "METRIC":
		return search.ObjectMetric, true
	case "DIMENSION":
		return search.ObjectDimension, true
	case "MEMBER":
		return search.ObjectMember, true
	default:
		return "", false
	}
}
