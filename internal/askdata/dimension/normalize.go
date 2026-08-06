package dimension

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/registry"
)

var ErrReservedMemberValue = errors.New("dimension member is a reserved or sentinel value")

var ErrInvalidMemberLookup = errors.New("dimension member lookup value is invalid")

type ReservedValueCatalog struct {
	Version string
	Values  map[string]string
}

func DefaultReservedValueCatalog() ReservedValueCatalog {
	values := map[string]string{}
	for code, entries := range map[string][]string{
		"UNKNOWN":       {"unknown", "未知", "不详", "未填写", "未提供"},
		"NULL_SENTINEL": {"null", "nil", "none", "n/a", "na", "-", "--"},
		"TEST_VALUE":    {"test", "testing", "测试", "测试数据"},
	} {
		for _, entry := range entries {
			values[normalizeMemberKey(entry)] = code
		}
	}
	return ReservedValueCatalog{Version: "reserved-member-values-v1", Values: values}
}

type NormalizedMember struct {
	DimensionVersionID askdata.ID           `json:"dimensionVersionId"`
	CanonicalValue     string               `json:"canonicalValue"`
	NormalizedValue    string               `json:"normalizedValue"`
	MemberKeyHash      askdata.ContentHash  `json:"memberKeyHash"`
	Aliases            []NormalizedAlias    `json:"aliases"`
	Sensitivity        registry.Sensitivity `json:"sensitivity"`
	EligibleForLLM     bool                 `json:"eligibleForLlm"`
}

type NormalizedAlias struct {
	Alias           string              `json:"alias"`
	NormalizedAlias string              `json:"normalizedAlias"`
	AliasKeyHash    askdata.ContentHash `json:"aliasKeyHash"`
}

type ReservedMember struct {
	Code                string              `json:"code"`
	NormalizedValueHash askdata.ContentHash `json:"normalizedValueHash"`
	CatalogVersion      string              `json:"catalogVersion"`
}

// NormalizeMember keeps canonical values and aliases separate. It performs
// only lossless Unicode/case/space normalization; abbreviations, pinyin and
// fuzzy clusters remain governed alias candidates rather than silent merges.
func NormalizeMember(
	dimensionVersionID askdata.ID,
	canonical string,
	aliases []string,
	sensitivity registry.Sensitivity,
	memberIndexPolicy registry.MemberIndexPolicy,
	highCardinality bool,
	catalog ReservedValueCatalog,
) (NormalizedMember, *ReservedMember, error) {
	if err := dimensionVersionID.Validate(); err != nil {
		return NormalizedMember{}, nil, fmt.Errorf("dimensionVersionId: %w", err)
	}
	if !validSensitivity(sensitivity) {
		return NormalizedMember{}, nil, errors.New("sensitivity is invalid")
	}
	canonicalDisplay, err := normalizeMemberDisplay(canonical)
	if err != nil || canonicalDisplay == "" {
		return NormalizedMember{}, nil, errors.New("canonical member value is invalid")
	}
	normalized := normalizeMemberKey(canonicalDisplay)
	if code, reserved := catalog.Values[normalized]; reserved {
		return NormalizedMember{}, &ReservedMember{
			Code: code, NormalizedValueHash: askdata.HashBytes([]byte(normalized)),
			CatalogVersion: catalog.Version,
		}, ErrReservedMemberValue
	}
	result := NormalizedMember{
		DimensionVersionID: dimensionVersionID,
		CanonicalValue:     canonicalDisplay, NormalizedValue: normalized,
		MemberKeyHash:  askdata.HashBytes([]byte(string(dimensionVersionID) + "\x00" + normalized)),
		Sensitivity:    sensitivity,
		EligibleForLLM: memberLabelsEligibleForLLM(sensitivity, memberIndexPolicy, highCardinality),
	}
	seen := map[string]struct{}{normalized: {}}
	for index, alias := range aliases {
		display, err := normalizeMemberDisplay(alias)
		if err != nil {
			return NormalizedMember{}, nil, fmt.Errorf("aliases[%d]: %w", index, err)
		}
		if display == "" {
			continue
		}
		key := normalizeMemberKey(display)
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		if _, reserved := catalog.Values[key]; reserved {
			continue
		}
		seen[key] = struct{}{}
		result.Aliases = append(result.Aliases, NormalizedAlias{
			Alias: display, NormalizedAlias: key,
			AliasKeyHash: askdata.HashBytes([]byte(string(dimensionVersionID) + "\x00" + key)),
		})
	}
	sort.Slice(result.Aliases, func(i, j int) bool { return result.Aliases[i].NormalizedAlias < result.Aliases[j].NormalizedAlias })
	return result, nil, nil
}

// memberLabelsEligibleForLLM is the single fail-closed policy gate for raw
// member labels. Sensitivity alone is insufficient: only a low-cardinality
// FULL index may expose PUBLIC/INTERNAL labels to an LLM. EXACT_ONLY,
// ON_DEMAND and NONE remain label-free even when their sensitivity is low.
func memberLabelsEligibleForLLM(
	sensitivity registry.Sensitivity,
	memberIndexPolicy registry.MemberIndexPolicy,
	highCardinality bool,
) bool {
	return !highCardinality && memberIndexPolicy == registry.MemberIndexFull &&
		(sensitivity == registry.SensitivityPublic || sensitivity == registry.SensitivityInternal)
}

// MemberLookupKeyHash prepares the only value accepted by the governed
// EXACT_ONLY database lookup. The raw member value is normalized and discarded
// by the caller before SQL is executed; neither labels nor aliases are used as
// SQL parameters. The hash algorithm intentionally matches member_key_hash and
// dimension_member_aliases.alias_key_hash.
func MemberLookupKeyHash(
	dimensionVersionID askdata.ID, value string,
) (askdata.ContentHash, error) {
	if err := dimensionVersionID.Validate(); err != nil {
		return "", ErrInvalidMemberLookup
	}
	display, err := normalizeMemberDisplay(value)
	if err != nil || display == "" {
		return "", ErrInvalidMemberLookup
	}
	normalized := normalizeMemberKey(display)
	if _, reserved := DefaultReservedValueCatalog().Values[normalized]; reserved {
		return "", ErrInvalidMemberLookup
	}
	return askdata.HashBytes([]byte(string(dimensionVersionID) + "\x00" + normalized)), nil
}

func normalizeMemberDisplay(value string) (string, error) {
	if !utf8.ValidString(value) {
		return "", errors.New("member value is not valid UTF-8")
	}
	value = norm.NFKC.String(value)
	value = strings.Join(strings.FieldsFunc(value, unicode.IsSpace), " ")
	if utf8.RuneCountInString(value) > 512 {
		return "", errors.New("member value exceeds 512 characters")
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return "", errors.New("member value contains control characters")
		}
	}
	return value, nil
}

func normalizeMemberKey(value string) string {
	display, _ := normalizeMemberDisplay(value)
	return strings.ToLower(display)
}
