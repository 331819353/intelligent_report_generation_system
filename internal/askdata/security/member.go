// Package security owns cross-stage AskData security boundaries that cannot
// be delegated to an LLM or to a caller-provided policy claim.
package security

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/dimension"
	"intelligent-report-generation-system/internal/askdata/registry"
	"intelligent-report-generation-system/internal/platform/database"
)

const (
	MemberPermissionObjectType         = "ASKDATA_DIMENSION"
	PermissionLookupConfidentialMember = "LOOKUP_CONFIDENTIAL_MEMBER"
	PermissionLookupRestrictedMember   = "LOOKUP_RESTRICTED_MEMBER"
	RedactedMemberLabel                = "[SENSITIVE_MEMBER]"
	memberExactEvidenceVersion         = "member-exact-evidence-v1"
	memberExactPayloadProofVersion     = "member-exact-payload-proof-v1"
	maxSensitiveQuestionRunes          = 4_096
	redactedPromptRune                 = '█'
)

var (
	ErrInvalidMemberPolicy     = errors.New("dimension member policy is invalid")
	ErrInvalidMemberLookup     = errors.New("dimension member lookup is invalid")
	ErrMemberUnavailable       = errors.New("dimension member is unavailable")
	ErrMemberLookupPersistence = errors.New("dimension member lookup persistence failed")
	ErrInvalidMemberRedaction  = errors.New("sensitive member redaction is invalid")
)

// MemberExposureDecision is the single label-exposure matrix for member
// values. EXACT_ONLY is intentionally stricter than FULL even for a
// non-sensitive code: the lookup may bind an ID, but it must not turn the
// matched label into prompt, log, or evidence text.
type MemberExposureDecision struct {
	IndexPolicy         registry.MemberIndexPolicy `json:"indexPolicy"`
	DatabaseExactLookup bool                       `json:"databaseExactLookup"`
	Embedding           bool                       `json:"embedding"`
	LLMContext          bool                       `json:"llmContext"`
	LogLabel            bool                       `json:"logLabel"`
	EvidenceLabel       bool                       `json:"evidenceLabel"`
	RequiresObjectGrant bool                       `json:"requiresObjectGrant"`
}

// DecideMemberExposure fails closed for combinations forbidden by the
// registry database constraints and exposes labels only for non-sensitive,
// low-cardinality FULL members.
func DecideMemberExposure(
	sensitivity registry.Sensitivity,
	policy registry.MemberIndexPolicy,
	highCardinality bool,
) (MemberExposureDecision, error) {
	if !validMemberSensitivity(sensitivity) || !validMemberIndexPolicy(policy) ||
		(highCardinality && policy != registry.MemberIndexOnDemand && policy != registry.MemberIndexNone) ||
		((sensitivity == registry.SensitivityConfidential || sensitivity == registry.SensitivityRestricted) &&
			policy != registry.MemberIndexExactOnly && policy != registry.MemberIndexNone) {
		return MemberExposureDecision{}, ErrInvalidMemberPolicy
	}

	decision := MemberExposureDecision{IndexPolicy: policy}
	switch policy {
	case registry.MemberIndexFull:
		decision.Embedding = true
		decision.LLMContext = true
		decision.LogLabel = true
		decision.EvidenceLabel = true
	case registry.MemberIndexExactOnly:
		decision.DatabaseExactLookup = true
		decision.RequiresObjectGrant = sensitivity == registry.SensitivityConfidential ||
			sensitivity == registry.SensitivityRestricted
	case registry.MemberIndexOnDemand, registry.MemberIndexNone:
		// ON_DEMAND is a separate, dimension-pinned bounded lookup. NONE has
		// no lookup surface. Neither is eligible for label-bearing contexts.
	}
	return decision, nil
}

// ExactMemberLookup contains no raw member value. Its fields are private so a
// caller cannot forge a lookup hash or accidentally serialize it. Formatting
// and JSON marshaling remain redacted even with diagnostic verbs.
type ExactMemberLookup struct {
	scope              askdata.PolicyScope
	dimensionVersionID askdata.ID
	redaction          memberRedaction
}

func NewExactMemberLookup(
	scope askdata.PolicyScope,
	dimensionVersionID askdata.ID,
	question string,
	span RuneSpan,
) (ExactMemberLookup, error) {
	if err := scope.Validate(); err != nil || dimensionVersionID.Validate() != nil {
		return ExactMemberLookup{}, ErrInvalidMemberLookup
	}
	parsedDimensionID, err := uuid.Parse(string(dimensionVersionID))
	if err != nil || parsedDimensionID.String() != string(dimensionVersionID) {
		return ExactMemberLookup{}, ErrInvalidMemberLookup
	}
	redaction, err := newMemberRedaction(dimensionVersionID, question, span)
	if err != nil {
		return ExactMemberLookup{}, ErrInvalidMemberLookup
	}
	return ExactMemberLookup{
		scope: scope, dimensionVersionID: dimensionVersionID,
		redaction: redaction,
	}, nil
}

func (lookup ExactMemberLookup) Format(state fmt.State, _ rune) {
	_, _ = io.WriteString(state, "ExactMemberLookup{member=[REDACTED]}")
}

func (lookup ExactMemberLookup) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		DimensionVersionID askdata.ID          `json:"dimensionVersionId"`
		Release            askdata.ReleaseRef  `json:"release"`
		QuestionHash       askdata.ContentHash `json:"questionHash"`
		Span               RuneSpan            `json:"span"`
		Member             string              `json:"member"`
	}{
		lookup.dimensionVersionID, lookup.scope.Release,
		lookup.redaction.questionHash, lookup.redaction.span, RedactedMemberLabel,
	})
}

// memberRedaction is issued only by NewExactMemberLookup and copied into a
// successful ExactMemberMatch. It binds an opaque database match to one
// original question span without making either the raw fragment or its
// dictionary-attackable lookup hash serializable.
type memberRedaction struct {
	dimensionVersionID askdata.ID
	questionHash       askdata.ContentHash
	span               RuneSpan
	lookupKeyHash      askdata.ContentHash
}

func newMemberRedaction(
	dimensionVersionID askdata.ID, question string, span RuneSpan,
) (memberRedaction, error) {
	runes, err := sensitiveQuestionRunes(question)
	if err != nil || !validRuneSpan(span, len(runes)) {
		return memberRedaction{}, ErrInvalidMemberLookup
	}
	lookupHash, err := dimension.MemberLookupKeyHash(
		dimensionVersionID, string(runes[span.Start:span.End]),
	)
	if err != nil {
		return memberRedaction{}, ErrInvalidMemberLookup
	}
	return memberRedaction{
		dimensionVersionID: dimensionVersionID,
		questionHash:       askdata.HashBytes([]byte(question)),
		span:               span,
		lookupKeyHash:      lookupHash,
	}, nil
}

func (redaction memberRedaction) validateShape() error {
	parsed, err := uuid.Parse(string(redaction.dimensionVersionID))
	if err != nil || parsed.String() != string(redaction.dimensionVersionID) ||
		redaction.questionHash.Validate() != nil || redaction.lookupKeyHash.Validate() != nil ||
		redaction.span.Start < 0 || redaction.span.End <= redaction.span.Start ||
		redaction.span.End > maxSensitiveQuestionRunes {
		return ErrInvalidMemberRedaction
	}
	return nil
}

func (redaction memberRedaction) validateQuestion(question string) ([]rune, error) {
	if err := redaction.validateShape(); err != nil ||
		askdata.HashBytes([]byte(question)) != redaction.questionHash {
		return nil, ErrInvalidMemberRedaction
	}
	runes, err := sensitiveQuestionRunes(question)
	if err != nil || !validRuneSpan(redaction.span, len(runes)) {
		return nil, ErrInvalidMemberRedaction
	}
	lookupHash, err := dimension.MemberLookupKeyHash(
		redaction.dimensionVersionID,
		string(runes[redaction.span.Start:redaction.span.End]),
	)
	if err != nil || lookupHash != redaction.lookupKeyHash {
		return nil, ErrInvalidMemberRedaction
	}
	return runes, nil
}

// exactMemberMatchPayload is populated only from the database matcher. Keeping
// it private prevents a caller from copying an authorized match, replacing its
// version IDs or content hashes, and presenting the copy as another database
// result.
type exactMemberMatchPayload struct {
	MemberVersionID      askdata.ID          `json:"memberVersionId"`
	DimensionVersionID   askdata.ID          `json:"dimensionVersionId"`
	MemberContentHash    askdata.ContentHash `json:"memberContentHash"`
	DimensionContentHash askdata.ContentHash `json:"dimensionContentHash"`
	Evidence             askdata.EvidenceRef `json:"evidence"`
}

// ExactMemberMatch deliberately has no canonical label, alias, sensitivity,
// query text, or lookup hash. Its database payload and integrity proof are
// private; callers receive only value-returning read-only accessors.
type ExactMemberMatch struct {
	payload      exactMemberMatchPayload
	redaction    memberRedaction
	payloadProof askdata.ContentHash
}

func (match ExactMemberMatch) Format(state fmt.State, _ rune) {
	_, _ = io.WriteString(state, "ExactMemberMatch{member=[REDACTED]}")
}

func (match ExactMemberMatch) MarshalJSON() ([]byte, error) {
	if err := match.Validate(); err != nil {
		return nil, ErrInvalidMemberLookup
	}
	return json.Marshal(match.payload)
}

func (match ExactMemberMatch) MemberVersionID() askdata.ID {
	return match.payload.MemberVersionID
}

func (match ExactMemberMatch) DimensionVersionID() askdata.ID {
	return match.payload.DimensionVersionID
}

func (match ExactMemberMatch) MemberContentHash() askdata.ContentHash {
	return match.payload.MemberContentHash
}

func (match ExactMemberMatch) DimensionContentHash() askdata.ContentHash {
	return match.payload.DimensionContentHash
}

func (match ExactMemberMatch) EvidenceRef() askdata.EvidenceRef {
	return match.payload.Evidence
}

func (match ExactMemberMatch) Validate() error {
	if err := match.payload.MemberVersionID.Validate(); err != nil {
		return ErrInvalidMemberLookup
	}
	if err := match.payload.DimensionVersionID.Validate(); err != nil {
		return ErrInvalidMemberLookup
	}
	if err := match.redaction.validateShape(); err != nil ||
		match.redaction.dimensionVersionID != match.payload.DimensionVersionID {
		return ErrInvalidMemberLookup
	}
	if err := match.payload.MemberContentHash.Validate(); err != nil {
		return ErrInvalidMemberLookup
	}
	if err := match.payload.DimensionContentHash.Validate(); err != nil {
		return ErrInvalidMemberLookup
	}
	if err := match.payload.Evidence.Validate(); err != nil ||
		match.payload.Evidence.Kind != askdata.EvidenceKindExactAlias ||
		match.payload.Evidence.SourceID != match.payload.MemberVersionID ||
		match.payloadProof.Validate() != nil ||
		match.payloadProof != exactMemberPayloadProof(match.payload, match.redaction) {
		return ErrInvalidMemberLookup
	}
	return nil
}

func exactMemberPayloadProof(
	payload exactMemberMatchPayload, redaction memberRedaction,
) askdata.ContentHash {
	proofPayload, err := json.Marshal(struct {
		Version   string                  `json:"version"`
		Payload   exactMemberMatchPayload `json:"payload"`
		Dimension askdata.ID              `json:"dimensionVersionId"`
		Question  askdata.ContentHash     `json:"questionHash"`
		Span      RuneSpan                `json:"span"`
		LookupKey askdata.ContentHash     `json:"lookupKeyHash"`
	}{
		memberExactPayloadProofVersion, payload, redaction.dimensionVersionID,
		redaction.questionHash, redaction.span, redaction.lookupKeyHash,
	})
	if err != nil {
		return ""
	}
	return askdata.HashBytes(proofPayload)
}

// SensitiveSpan returns only a safe location after proving question is the
// exact original question used to construct the database lookup.
func (match ExactMemberMatch) SensitiveSpan(question string) (RuneSpan, error) {
	if err := match.Validate(); err != nil {
		return RuneSpan{}, ErrInvalidMemberRedaction
	}
	if _, err := match.redaction.validateQuestion(question); err != nil {
		return RuneSpan{}, err
	}
	return match.redaction.span, nil
}

// RedactPromptText removes normalized variants of this matched question
// fragment from one model-visible string. Replacement preserves Unicode rune
// length so offsets after the sensitive span do not drift.
func (match ExactMemberMatch) RedactPromptText(question, text string) (string, error) {
	if err := match.Validate(); err != nil {
		return "", ErrInvalidMemberRedaction
	}
	runes, err := match.redaction.validateQuestion(question)
	if err != nil {
		return "", err
	}
	fragment := string(runes[match.redaction.span.Start:match.redaction.span.End])
	return redactNormalizedVariants(text, fragment)
}

type PostgresMemberStore struct{ pool *pgxpool.Pool }

func NewPostgresMemberStore(pool *pgxpool.Pool) *PostgresMemberStore {
	return &PostgresMemberStore{pool: pool}
}

const exactMemberLookupSQL = `SELECT
	member_version_id::text,dimension_version_id::text,
	member_content_hash,dimension_content_hash
	FROM askdata.lookup_exact_dimension_member($1,$2,$3,$4)`

// LookupExact invokes the database-only matcher with a dimension-bound hash.
// Missing, unauthorized, expired, unpublished, and ambiguous values all
// produce the same ErrMemberUnavailable result.
func (store *PostgresMemberStore) LookupExact(
	ctx context.Context, lookup ExactMemberLookup,
) (ExactMemberMatch, error) {
	if store == nil || store.pool == nil {
		return ExactMemberMatch{}, ErrInvalidMemberLookup
	}
	tenantID, releaseID, dimensionVersionID, err := validateExactMemberLookup(ctx, lookup)
	if err != nil {
		return ExactMemberMatch{}, err
	}

	var payload exactMemberMatchPayload
	err = database.WithTenantTx(ctx, store.pool, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(
			ctx, exactMemberLookupSQL, releaseID,
			string(lookup.scope.Release.ContentHash), dimensionVersionID,
			string(lookup.redaction.lookupKeyHash),
		).Scan(
			&payload.MemberVersionID, &payload.DimensionVersionID,
			&payload.MemberContentHash, &payload.DimensionContentHash,
		)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return ExactMemberMatch{}, ErrMemberUnavailable
	}
	if err != nil {
		return ExactMemberMatch{}, errors.Join(ErrMemberLookupPersistence, err)
	}
	if payload.DimensionVersionID != lookup.dimensionVersionID {
		return ExactMemberMatch{}, ErrMemberLookupPersistence
	}
	match := ExactMemberMatch{payload: payload, redaction: lookup.redaction}

	evidence, err := exactMemberEvidence(lookup, match)
	if err != nil {
		return ExactMemberMatch{}, ErrMemberLookupPersistence
	}
	match.payload.Evidence = evidence
	match.payloadProof = exactMemberPayloadProof(match.payload, match.redaction)
	if err := match.Validate(); err != nil {
		return ExactMemberMatch{}, ErrMemberLookupPersistence
	}
	return match, nil
}

func exactMemberEvidence(
	lookup ExactMemberLookup, match ExactMemberMatch,
) (askdata.EvidenceRef, error) {
	payload, err := json.Marshal(struct {
		Version              string              `json:"version"`
		Release              askdata.ReleaseRef  `json:"release"`
		PolicyHash           askdata.ContentHash `json:"policyHash"`
		DimensionVersionID   askdata.ID          `json:"dimensionVersionId"`
		DimensionContentHash askdata.ContentHash `json:"dimensionContentHash"`
		MemberVersionID      askdata.ID          `json:"memberVersionId"`
		MemberContentHash    askdata.ContentHash `json:"memberContentHash"`
	}{
		memberExactEvidenceVersion, lookup.scope.Release, lookup.scope.PolicyHash,
		match.payload.DimensionVersionID, match.payload.DimensionContentHash,
		match.payload.MemberVersionID, match.payload.MemberContentHash,
	})
	if err != nil {
		return askdata.EvidenceRef{}, err
	}
	proofHash := askdata.HashBytes(payload)
	return askdata.EvidenceRef{
		EvidenceID: askdata.ID("member-exact:" + string(proofHash)),
		Kind:       askdata.EvidenceKindExactAlias, SourceID: match.payload.MemberVersionID,
		ContentHash: proofHash,
	}, nil
}

func validateExactMemberLookup(
	ctx context.Context, lookup ExactMemberLookup,
) (string, uuid.UUID, uuid.UUID, error) {
	if err := lookup.scope.Validate(); err != nil ||
		lookup.dimensionVersionID.Validate() != nil ||
		lookup.redaction.validateShape() != nil ||
		lookup.redaction.dimensionVersionID != lookup.dimensionVersionID {
		return "", uuid.Nil, uuid.Nil, ErrInvalidMemberLookup
	}
	access, authenticated := database.AccessContextFromContext(ctx)
	if !authenticated || access.UserID != string(lookup.scope.ActorID) ||
		!containsID(lookup.scope.DomainIDs, askdata.ID(access.DomainID)) {
		return "", uuid.Nil, uuid.Nil, ErrInvalidMemberLookup
	}
	tenantID, err := uuid.Parse(string(lookup.scope.TenantID))
	if err != nil {
		return "", uuid.Nil, uuid.Nil, ErrInvalidMemberLookup
	}
	if _, err := uuid.Parse(string(lookup.scope.ActorID)); err != nil {
		return "", uuid.Nil, uuid.Nil, ErrInvalidMemberLookup
	}
	if _, err := uuid.Parse(access.DomainID); err != nil {
		return "", uuid.Nil, uuid.Nil, ErrInvalidMemberLookup
	}
	releaseID, err := uuid.Parse(string(lookup.scope.Release.ReleaseID))
	if err != nil {
		return "", uuid.Nil, uuid.Nil, ErrInvalidMemberLookup
	}
	dimensionVersionID, err := uuid.Parse(string(lookup.dimensionVersionID))
	if err != nil {
		return "", uuid.Nil, uuid.Nil, ErrInvalidMemberLookup
	}
	return tenantID.String(), releaseID, dimensionVersionID, nil
}

// RuneSpan uses zero-based Unicode code-point offsets and an exclusive end.
type RuneSpan struct {
	Start int `json:"start"`
	End   int `json:"end"`
}

// RedactSensitiveMemberSpans produces the only question/log view that may be
// used after an EXACT_ONLY sensitive value has been located. It neither
// returns nor records the removed fragments.
func RedactSensitiveMemberSpans(text string, spans []RuneSpan) (string, error) {
	if !utf8.ValidString(text) || len(spans) == 0 {
		return "", ErrInvalidMemberRedaction
	}
	runes := []rune(text)
	ordered := append([]RuneSpan(nil), spans...)
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].Start != ordered[j].Start {
			return ordered[i].Start < ordered[j].Start
		}
		return ordered[i].End < ordered[j].End
	})
	previousEnd := 0
	var redacted strings.Builder
	for _, span := range ordered {
		if span.Start < previousEnd || span.Start < 0 || span.End <= span.Start || span.End > len(runes) {
			return "", ErrInvalidMemberRedaction
		}
		redacted.WriteString(string(runes[previousEnd:span.Start]))
		redacted.WriteString(RedactedMemberLabel)
		previousEnd = span.End
	}
	redacted.WriteString(string(runes[previousEnd:]))
	return redacted.String(), nil
}

type normalizedRuneMap struct {
	runes  []rune
	starts []int
	ends   []int
}

var memberCaseFolder = cases.Fold()

func redactNormalizedVariants(text, fragment string) (string, error) {
	if !utf8.ValidString(text) || !utf8.ValidString(fragment) || fragment == "" {
		return "", ErrInvalidMemberRedaction
	}
	key := normalizedVariantKey(fragment)
	if key == "" {
		return "", ErrInvalidMemberRedaction
	}
	mapped := mapNormalizedRunes(text)
	keyRunes := []rune(key)
	type sourceRange struct{ start, end int }
	ranges := []sourceRange{}
	for start := 0; start+len(keyRunes) <= len(mapped.runes); start++ {
		end := start + len(keyRunes)
		if string(mapped.runes[start:end]) != key {
			continue
		}
		ranges = append(ranges, sourceRange{mapped.starts[start], mapped.ends[end-1]})
	}
	if len(ranges) == 0 {
		// Cross-rune Unicode composition cannot always be represented by the
		// per-rune source map. If full normalization still finds the sensitive
		// value, fail closed instead of emitting a partially redacted fact.
		if strings.Contains(normalizedVariantKey(text), key) {
			return "", ErrInvalidMemberRedaction
		}
		return text, nil
	}
	sort.Slice(ranges, func(i, j int) bool {
		if ranges[i].start != ranges[j].start {
			return ranges[i].start < ranges[j].start
		}
		return ranges[i].end < ranges[j].end
	})
	merged := ranges[:0]
	for _, candidate := range ranges {
		if len(merged) == 0 || candidate.start >= merged[len(merged)-1].end {
			merged = append(merged, candidate)
			continue
		}
		if candidate.end > merged[len(merged)-1].end {
			merged[len(merged)-1].end = candidate.end
		}
	}
	runes := []rune(text)
	var redacted strings.Builder
	previous := 0
	for _, source := range merged {
		redacted.WriteString(string(runes[previous:source.start]))
		redacted.WriteString(strings.Repeat(string(redactedPromptRune), source.end-source.start))
		previous = source.end
	}
	redacted.WriteString(string(runes[previous:]))
	result := redacted.String()
	if strings.Contains(normalizedVariantKey(result), key) {
		return "", ErrInvalidMemberRedaction
	}
	return result, nil
}

func normalizedVariantKey(value string) string {
	value = memberCaseFolder.String(norm.NFKC.String(value))
	return strings.Join(strings.FieldsFunc(value, unicode.IsSpace), " ")
}

func mapNormalizedRunes(value string) normalizedRuneMap {
	result := normalizedRuneMap{}
	for sourceIndex, sourceRune := range []rune(value) {
		piece := memberCaseFolder.String(norm.NFKC.String(string(sourceRune)))
		for _, normalizedRune := range []rune(piece) {
			if unicode.IsSpace(normalizedRune) {
				if len(result.runes) != 0 && result.runes[len(result.runes)-1] == ' ' {
					result.ends[len(result.ends)-1] = sourceIndex + 1
					continue
				}
				normalizedRune = ' '
			}
			result.runes = append(result.runes, normalizedRune)
			result.starts = append(result.starts, sourceIndex)
			result.ends = append(result.ends, sourceIndex+1)
		}
	}
	return result
}

func sensitiveQuestionRunes(question string) ([]rune, error) {
	if !utf8.ValidString(question) || strings.TrimSpace(question) == "" {
		return nil, ErrInvalidMemberLookup
	}
	runes := []rune(question)
	if len(runes) > maxSensitiveQuestionRunes {
		return nil, ErrInvalidMemberLookup
	}
	return runes, nil
}

func validRuneSpan(span RuneSpan, length int) bool {
	return span.Start >= 0 && span.End > span.Start && span.End <= length
}

func validMemberSensitivity(value registry.Sensitivity) bool {
	return value == registry.SensitivityPublic || value == registry.SensitivityInternal ||
		value == registry.SensitivityConfidential || value == registry.SensitivityRestricted
}

func validMemberIndexPolicy(value registry.MemberIndexPolicy) bool {
	return value == registry.MemberIndexFull || value == registry.MemberIndexExactOnly ||
		value == registry.MemberIndexOnDemand || value == registry.MemberIndexNone
}

func containsID(values []askdata.ID, target askdata.ID) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
