package registry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/platform/database"
)

// ErrQueryReadInvalid marks a malformed query-time read request. It never
// distinguishes "not found" from "not permitted": candidates the actor cannot
// see must not have their existence leaked through error shapes.
var ErrQueryReadInvalid = errors.New("semantic query read request is invalid")

// QueryReader serves the reads the question runtime performs while answering.
//
// Every read is pinned to the release carried on the PolicyScope and resolved
// through askdata.release_objects, which is the immutable, content-addressed
// manifest of what a release contains. The runtime therefore never observes
// live draft rows: editing or re-certifying a semantic object cannot change the
// meaning of a question that is already running, and a question can only ever
// reference objects that actually passed the release gate.
//
// Tenant isolation comes from RLS via WithTenantTx; domain and actor scoping is
// applied in every predicate. Sensitivity filtering is applied per read.
type QueryReader struct{ pool *pgxpool.Pool }

func NewQueryReader(pool *pgxpool.Pool) *QueryReader { return &QueryReader{pool: pool} }

// ContractRow is the release-pinned contract of one semantic object.
type ContractRow struct {
	ObjectType      string
	ObjectID        string
	ObjectVersionID string
	ContentHash     askdata.ContentHash
	Sensitivity     string
	OwnerID         string
	Status          string
	Contract        json.RawMessage
}

// MemberRow is one governed dimension member value.
type MemberRow struct {
	MemberVersionID string
	DisplayLabel    string
	Aliases         []string
	HierarchyPath   []string
	Sensitive       bool
}

// MemberLookup is the outcome of resolving a mention against a dimension.
type MemberLookup struct {
	DimensionVersionID string
	Members            []MemberRow
	Truncated          bool
}

// ExampleRow is a certified question and the semantic components it is expected
// to bind to.
//
// askdata.certified_example_versions stores expected *components* (metric
// versions, dimension versions, member values, time expression) and never a
// serialised Semantic IR. toolhost.CertifiedExampleSummary carries the same
// components, so this maps across without synthesising a plan: an example is a
// retrieval prior that the binder re-resolves against the release pinned to the
// current run, not a frozen plan to replay.
type ExampleRow struct {
	ExampleVersionID       string
	Question               string
	ExpectedMetricIDs      []string
	ExpectedDimensionIDs   []string
	ExpectedMemberValues   json.RawMessage
	ExpectedTimeExpression string
	ContentHash            askdata.ContentHash
	SimilarityPermillion   int
}

// QualityRuleRow is one governed data-quality rule outcome.
type QualityRuleRow struct {
	Code     string
	Severity string
	Passed   bool
}

// QualityStatus aggregates data-quality state for the models a query touches.
type QualityStatus struct {
	Status string
	Rules  []QualityRuleRow
}

const maxQueryReadIDs = 100

// Contracts returns the release-pinned contracts for the requested object
// versions. IDs that are not part of the pinned release, or that the actor may
// not see, are silently absent rather than reported: the caller learns nothing
// about objects outside its scope.
func (reader *QueryReader) Contracts(
	ctx context.Context,
	scope askdata.PolicyScope,
	domainID askdata.ID,
	objectVersionIDs []string,
) ([]ContractRow, error) {
	if err := reader.validate(scope, domainID); err != nil {
		return nil, err
	}
	ids, err := canonicalIDSet(objectVersionIDs)
	if err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return []ContractRow{}, nil
	}
	rows := []ContractRow{}
	err = reader.read(ctx, scope, func(tx pgx.Tx) error {
		// release_objects carries the contract, hash and sensitivity that the
		// release gate approved. Owner and status come from the source version
		// tables purely for display; they can never widen what is returned.
		result, queryErr := tx.Query(ctx, `SELECT
			object.object_type,object.object_id::text,object.object_version_id::text,
			object.content_hash,object.sensitivity,
			COALESCE(owner.owner_id::text,''),COALESCE(owner.status,''),
			object.contract_json
			FROM askdata.release_objects AS object
			LEFT JOIN LATERAL (
			  SELECT owner_id,status FROM askdata.semantic_models
			   WHERE id=object.object_version_id AND tenant_id=object.tenant_id
			  UNION ALL
			  SELECT owner_id,status FROM askdata.metric_versions
			   WHERE id=object.object_version_id AND tenant_id=object.tenant_id
			  UNION ALL
			  SELECT owner_id,status FROM askdata.dimensions
			   WHERE id=object.object_version_id AND tenant_id=object.tenant_id
			  UNION ALL
			  SELECT owner_id,status FROM askdata.business_term_versions
			   WHERE id=object.object_version_id AND tenant_id=object.tenant_id
			  LIMIT 1
			) AS owner ON true
			WHERE object.release_id=$1 AND object.domain_id=$2
			  AND object.object_version_id::text=ANY($3)
			  AND object.sensitivity<>'RESTRICTED'
			ORDER BY object.object_type,object.object_version_id`,
			string(scope.Release.ReleaseID), string(domainID), ids)
		if queryErr != nil {
			return queryErr
		}
		defer result.Close()
		for result.Next() {
			var row ContractRow
			if scanErr := result.Scan(
				&row.ObjectType, &row.ObjectID, &row.ObjectVersionID, &row.ContentHash,
				&row.Sensitivity, &row.OwnerID, &row.Status, &row.Contract,
			); scanErr != nil {
				return scanErr
			}
			rows = append(rows, row)
		}
		return result.Err()
	})
	if err != nil {
		return nil, err
	}
	return rows, nil
}

// DimensionMembers resolves a business mention against the governed members of
// one dimension. It refuses dimensions whose member policy forbids
// label-bearing recall and never returns members above INTERNAL sensitivity, so
// restricted member labels cannot reach a model, an embedding service or a log.
func (reader *QueryReader) DimensionMembers(
	ctx context.Context,
	scope askdata.PolicyScope,
	domainID askdata.ID,
	dimensionVersionID string,
	mention string,
	limit int,
) (MemberLookup, error) {
	if err := reader.validate(scope, domainID); err != nil {
		return MemberLookup{}, err
	}
	if !canonicalAdminUUID(dimensionVersionID) || limit < 1 || limit > 100 {
		return MemberLookup{}, fmt.Errorf("%w: dimension version and limit are required", ErrQueryReadInvalid)
	}
	mention = strings.TrimSpace(mention)
	if len(mention) > 512 {
		return MemberLookup{}, fmt.Errorf("%w: mention exceeds the safe bound", ErrQueryReadInvalid)
	}
	lookup := MemberLookup{DimensionVersionID: dimensionVersionID, Members: []MemberRow{}}
	err := reader.read(ctx, scope, func(tx pgx.Tx) error {
		// The dimension itself must be inside the pinned release and must allow
		// label-bearing member recall; EXACT_ONLY/NONE and high-cardinality
		// dimensions never expose browsable labels.
		var policy, sensitivity string
		var highCardinality bool
		if err := tx.QueryRow(ctx, `SELECT dimension.member_index_policy,dimension.sensitivity,dimension.high_cardinality
			FROM askdata.dimensions AS dimension
			JOIN askdata.release_objects AS object
			  ON object.object_version_id=dimension.id AND object.tenant_id=dimension.tenant_id
			 AND object.release_id=$1 AND object.object_type='DIMENSION'
			WHERE dimension.id=$2 AND dimension.domain_id=$3`,
			string(scope.Release.ReleaseID), dimensionVersionID, string(domainID),
		).Scan(&policy, &sensitivity, &highCardinality); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil
			}
			return err
		}
		if sensitivity == "RESTRICTED" || sensitivity == "CONFIDENTIAL" ||
			policy == "NONE" || policy == "EXACT_ONLY" || highCardinality {
			return nil
		}
		result, queryErr := tx.Query(ctx, `SELECT
			member.id::text,member.canonical_label,
			COALESCE(array_agg(DISTINCT alias.alias) FILTER(WHERE alias.alias IS NOT NULL),'{}'::text[]),
			COALESCE(member.parent_member_version_id::text,''),
			member.sensitivity
			FROM askdata.dimension_members AS member
			LEFT JOIN askdata.dimension_member_aliases AS alias
			  ON alias.member_version_id=member.id AND alias.tenant_id=member.tenant_id
			 AND alias.status='CERTIFIED'
			WHERE member.dimension_version_id=$1 AND member.domain_id=$2
			  AND member.status='CERTIFIED'
			  AND member.sensitivity IN ('PUBLIC','INTERNAL')
			  AND ($3='' OR member.canonical_label ILIKE '%'||$3||'%' OR alias.alias ILIKE '%'||$3||'%')
			GROUP BY member.id,member.canonical_label,member.parent_member_version_id,member.sensitivity
			ORDER BY member.canonical_label,member.id
			LIMIT $4`,
			dimensionVersionID, string(domainID), mention, limit+1)
		if queryErr != nil {
			return queryErr
		}
		defer result.Close()
		for result.Next() {
			var row MemberRow
			var parent, memberSensitivity string
			if scanErr := result.Scan(
				&row.MemberVersionID, &row.DisplayLabel, &row.Aliases, &parent, &memberSensitivity,
			); scanErr != nil {
				return scanErr
			}
			if parent != "" {
				row.HierarchyPath = []string{parent}
			}
			row.Sensitive = memberSensitivity == "INTERNAL"
			lookup.Members = append(lookup.Members, row)
		}
		return result.Err()
	})
	if err != nil {
		return MemberLookup{}, err
	}
	if len(lookup.Members) > limit {
		lookup.Members, lookup.Truncated = lookup.Members[:limit], true
	}
	return lookup, nil
}

// CertifiedExamples returns certified question/IR pairs from the pinned
// release. They are retrieval priors only: the caller must still compile and
// authorise the IR, never replay a stored plan.
func (reader *QueryReader) CertifiedExamples(
	ctx context.Context,
	scope askdata.PolicyScope,
	domainID askdata.ID,
	questionSummary string,
	limit int,
) ([]ExampleRow, error) {
	if err := reader.validate(scope, domainID); err != nil {
		return nil, err
	}
	if limit < 1 || limit > 50 {
		return nil, fmt.Errorf("%w: limit must be between 1 and 50", ErrQueryReadInvalid)
	}
	questionSummary = strings.TrimSpace(questionSummary)
	if len(questionSummary) > 1024 {
		return nil, fmt.Errorf("%w: question summary exceeds the safe bound", ErrQueryReadInvalid)
	}
	rows := []ExampleRow{}
	err := reader.read(ctx, scope, func(tx pgx.Tx) error {
		// Trigram similarity ranks examples against the question; role scoping
		// keeps examples an actor may not use out of the candidate set.
		result, queryErr := tx.Query(ctx, `SELECT
			example.id::text,example.question,
			example.expected_metric_version_ids::text[],
			example.expected_dimension_version_ids::text[],
			example.expected_member_values,example.expected_time_expression,
			example.content_hash,
			CASE WHEN $3='' THEN 0
			     ELSE LEAST(1000000,GREATEST(0,
			       (similarity(example.question,$3)*1000000)::int))
			END AS score
			FROM askdata.certified_example_versions AS example
			JOIN askdata.release_objects AS object
			  ON object.object_version_id=example.id AND object.tenant_id=example.tenant_id
			 AND object.release_id=$1 AND object.object_type='CERTIFIED_EXAMPLE'
			WHERE example.domain_id=$2 AND example.status='CERTIFIED'
			  AND (cardinality(example.applicable_role_ids)=0
			       OR example.applicable_role_ids && $5::uuid[])
			ORDER BY score DESC,example.id
			LIMIT $4`,
			string(scope.Release.ReleaseID), string(domainID), questionSummary, limit,
			stableIDStrings(scope.RoleIDs))
		if queryErr != nil {
			return queryErr
		}
		defer result.Close()
		for result.Next() {
			var row ExampleRow
			if scanErr := result.Scan(
				&row.ExampleVersionID, &row.Question, &row.ExpectedMetricIDs,
				&row.ExpectedDimensionIDs, &row.ExpectedMemberValues,
				&row.ExpectedTimeExpression, &row.ContentHash, &row.SimilarityPermillion,
			); scanErr != nil {
				return scanErr
			}
			rows = append(rows, row)
		}
		return result.Err()
	})
	if err != nil {
		return nil, err
	}
	return rows, nil
}

// DataQuality reports the governed quality rules attached to the models a query
// touches.
//
// NOTE: askdata.quality_rules currently has no authoring path — the semantic
// importer does not accept QUALITY_RULE and nothing else writes the table — so
// in practice this returns UNKNOWN with no rules until that gap is closed. The
// read is implemented against the real schema so that closing the authoring gap
// needs no change here.
func (reader *QueryReader) DataQuality(
	ctx context.Context,
	scope askdata.PolicyScope,
	domainID askdata.ID,
	modelVersionIDs []string,
) (QualityStatus, error) {
	if err := reader.validate(scope, domainID); err != nil {
		return QualityStatus{}, err
	}
	ids, err := canonicalIDSet(modelVersionIDs)
	if err != nil {
		return QualityStatus{}, err
	}
	status := QualityStatus{Status: qualityStatusFor(nil), Rules: []QualityRuleRow{}}
	if len(ids) == 0 {
		return status, nil
	}
	err = reader.read(ctx, scope, func(tx pgx.Tx) error {
		result, queryErr := tx.Query(ctx, `SELECT rule.code,rule.severity
			FROM askdata.quality_rules AS rule
			JOIN askdata.release_objects AS object
			  ON object.object_version_id=rule.id AND object.tenant_id=rule.tenant_id
			 AND object.release_id=$1 AND object.object_type='QUALITY_RULE'
			WHERE rule.domain_id=$2 AND rule.target_type='SEMANTIC_MODEL' AND rule.target_version_id::text=ANY($3)
			  AND rule.status='CERTIFIED'
			ORDER BY rule.code`,
			string(scope.Release.ReleaseID), string(domainID), ids)
		if queryErr != nil {
			return queryErr
		}
		defer result.Close()
		for result.Next() {
			var row QualityRuleRow
			if scanErr := result.Scan(&row.Code, &row.Severity); scanErr != nil {
				return scanErr
			}
			// Rule outcomes are produced by materialisation quality runs; until
			// those are linked here a rule is reported as present but unproven.
			row.Passed = false
			status.Rules = append(status.Rules, row)
		}
		return result.Err()
	})
	if err != nil {
		return QualityStatus{}, err
	}
	status.Status = qualityStatusFor(status.Rules)
	return status, nil
}

// qualityStatusFor derives the reported status from the rules that were found.
// The absence of rules is UNKNOWN and never PASSED: no governed quality rule
// having run is not evidence that the data is good, and the answer layer must
// be able to tell "verified clean" apart from "never checked".
func qualityStatusFor(rules []QualityRuleRow) string {
	if len(rules) == 0 {
		return "UNKNOWN"
	}
	for _, rule := range rules {
		if !rule.Passed {
			return "PARTIAL"
		}
	}
	return "PASSED"
}

func (reader *QueryReader) validate(scope askdata.PolicyScope, domainID askdata.ID) error {
	if reader == nil || reader.pool == nil {
		return fmt.Errorf("%w: reader is not configured", ErrQueryReadInvalid)
	}
	if scope.Validate() != nil || scope.Release.Validate() != nil || domainID.Validate() != nil {
		return fmt.Errorf("%w: scope, release and domain are required", ErrQueryReadInvalid)
	}
	for _, allowed := range scope.DomainIDs {
		if allowed == domainID {
			return nil
		}
	}
	return fmt.Errorf("%w: domain is outside the actor policy scope", ErrQueryReadInvalid)
}

func (reader *QueryReader) read(
	ctx context.Context,
	scope askdata.PolicyScope,
	run func(pgx.Tx) error,
) error {
	ctx = database.WithAccessContext(ctx, string(scope.ActorID), "")
	return database.WithTenantTx(ctx, reader.pool, string(scope.TenantID), run)
}

func canonicalIDSet(values []string) ([]string, error) {
	if len(values) > maxQueryReadIDs {
		return nil, fmt.Errorf("%w: too many object identifiers", ErrQueryReadInvalid)
	}
	seen := map[string]bool{}
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if !canonicalAdminUUID(value) {
			return nil, fmt.Errorf("%w: object identifier must be a canonical UUID", ErrQueryReadInvalid)
		}
		if seen[value] {
			continue
		}
		seen[value] = true
		result = append(result, value)
	}
	return result, nil
}

func stableIDStrings(values []askdata.ID) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		result = append(result, string(value))
	}
	return result
}
