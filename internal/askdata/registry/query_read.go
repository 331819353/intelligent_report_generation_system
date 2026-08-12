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
	"intelligent-report-generation-system/internal/materialization"
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

// ReleasedVersionRef is the complete immutable identity required by graph
// vertex contracts. Query-time callers provide version IDs only; object IDs and
// version numbers must be resolved from the pinned release, never guessed.
type ReleasedVersionRef struct {
	ObjectID        string
	ObjectVersionID string
	Version         int
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
	// Measurement distinguishes "checked and failed" from "never measured for
	// this snapshot", which Passed alone cannot express.
	Measurement string
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
	err = reader.read(ctx, scope, domainID, func(tx pgx.Tx) error {
		// release_objects carries the contract, hash and sensitivity that the
		// release gate approved. Owner and status come from the source version
		// tables purely for display; they can never widen what is returned.
		result, queryErr := tx.Query(ctx, `SELECT
			object.object_type,object.object_id::text,object.object_version_id::text,
			object.content_hash,object.sensitivity,
			COALESCE(owner.owner_id::text,''),COALESCE(owner.status,''),
			CASE WHEN metric_measure.contract_json IS NOT NULL THEN
			  object.contract_json || jsonb_build_object(
			    'name',COALESCE(metric_measure.contract_json->>'name',''),
			    'definition',COALESCE(metric_measure.contract_json->>'definition',metric_measure.contract_json->>'name','')
			  )
			ELSE object.contract_json END
			FROM askdata.release_objects AS object
			LEFT JOIN LATERAL (
			  SELECT owner_id,status FROM askdata.semantic_models
			   WHERE id=object.object_version_id AND tenant_id=object.tenant_id
			  UNION ALL
			  SELECT owner_id,status FROM askdata.metric_versions
			   WHERE id=object.object_version_id AND tenant_id=object.tenant_id
			  UNION ALL
			  SELECT owner_id,status FROM askdata.measures
			   WHERE id=object.object_version_id AND tenant_id=object.tenant_id
			  UNION ALL
			  SELECT owner_id,status FROM askdata.dimensions
			   WHERE id=object.object_version_id AND tenant_id=object.tenant_id
			  UNION ALL
			  SELECT owner_id,status FROM askdata.business_term_versions
			   WHERE id=object.object_version_id AND tenant_id=object.tenant_id
			  LIMIT 1
			) AS owner ON true
			LEFT JOIN LATERAL (
			  SELECT measure_object.contract_json
			  FROM askdata.metric_versions AS metric
			  JOIN askdata.release_objects AS measure_object
			    ON measure_object.tenant_id=object.tenant_id
			   AND measure_object.release_id=object.release_id
			   AND measure_object.object_type='MEASURE'
			   AND measure_object.object_version_id::text=metric.formula_ast->>'measureVersionId'
			  WHERE object.object_type='METRIC'
			    AND metric.tenant_id=object.tenant_id
			    AND metric.id=object.object_version_id
			    AND metric.formula_ast->>'type'='MEASURE_REF'
			  LIMIT 1
			) AS metric_measure ON true
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

// CanonicalMetricVersions translates historical MEASURE search hits to the
// certified METRIC wrapper in the same pinned release. The search index keeps
// the richer physical-measure label for recall, while every downstream graph
// and compiler contract receives the executable metric version identity.
func (reader *QueryReader) CanonicalMetricVersions(
	ctx context.Context,
	scope askdata.PolicyScope,
	domainID askdata.ID,
	objectVersionIDs []string,
) (map[string]string, error) {
	if err := reader.validate(scope, domainID); err != nil {
		return nil, err
	}
	ids, err := canonicalIDSet(objectVersionIDs)
	if err != nil {
		return nil, err
	}
	result := make(map[string]string, len(ids))
	if len(ids) == 0 {
		return result, nil
	}
	err = reader.read(ctx, scope, domainID, func(tx pgx.Tx) error {
		rows, queryErr := tx.Query(ctx, `SELECT requested.id::text,
			CASE WHEN original.object_type='MEASURE' THEN metric.id::text ELSE original.object_version_id::text END
			FROM unnest($3::uuid[]) AS requested(id)
			JOIN askdata.release_objects AS original
			  ON original.tenant_id=askdata.current_tenant_id()
			 AND original.release_id=$1 AND original.domain_id=$2
			 AND original.object_version_id=requested.id
			 AND original.object_type IN ('METRIC','MEASURE')
			LEFT JOIN LATERAL (
			  SELECT metric.id
			  FROM askdata.metric_versions AS metric
			  JOIN askdata.release_objects AS metric_object
			    ON metric_object.tenant_id=metric.tenant_id
			   AND metric_object.release_id=original.release_id
			   AND metric_object.domain_id=original.domain_id
			   AND metric_object.object_type='METRIC'
			   AND metric_object.object_version_id=metric.id
			  WHERE original.object_type='MEASURE'
			    AND metric.formula_ast->>'type'='MEASURE_REF'
			    AND metric.formula_ast->>'measureVersionId'=original.object_version_id::text
			  ORDER BY metric.id
			  LIMIT 1
			) AS metric ON true
			WHERE original.object_type='METRIC' OR metric.id IS NOT NULL
			ORDER BY requested.id`, scope.Release.ReleaseID, domainID, ids)
		if queryErr != nil {
			return queryErr
		}
		defer rows.Close()
		for rows.Next() {
			var requested, canonical string
			if scanErr := rows.Scan(&requested, &canonical); scanErr != nil {
				return scanErr
			}
			result[requested] = canonical
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// ReleasedVersionRefs resolves complete graph identities from the immutable
// release manifest and the corresponding certified source table.
func (reader *QueryReader) ReleasedVersionRefs(
	ctx context.Context,
	scope askdata.PolicyScope,
	domainID askdata.ID,
	objectType string,
	objectVersionIDs []string,
) ([]ReleasedVersionRef, error) {
	if err := reader.validate(scope, domainID); err != nil {
		return nil, err
	}
	ids, err := canonicalIDSet(objectVersionIDs)
	if err != nil {
		return nil, err
	}
	table, releaseType, objectColumn := "", "", ""
	switch objectType {
	case "MODEL":
		table, releaseType, objectColumn = "semantic_models", "SEMANTIC_MODEL", "model_id"
	case "METRIC":
		table, releaseType, objectColumn = "metric_versions", "METRIC", "metric_id"
	case "DIMENSION":
		table, releaseType, objectColumn = "dimensions", "DIMENSION", "dimension_id"
	case "MEMBER":
		table, releaseType, objectColumn = "dimension_members", "MEMBER", "member_id"
	default:
		return nil, fmt.Errorf("%w: unsupported graph object type", ErrQueryReadInvalid)
	}
	if len(ids) == 0 {
		return []ReleasedVersionRef{}, nil
	}
	refs := []ReleasedVersionRef{}
	err = reader.read(ctx, scope, domainID, func(tx pgx.Tx) error {
		query := fmt.Sprintf(`SELECT source.%s::text,source.id::text,source.version_no
			FROM askdata.release_objects AS object
			JOIN askdata.%s AS source
			  ON source.tenant_id=object.tenant_id AND source.domain_id=object.domain_id
			 AND source.id=object.object_version_id AND source.%s=object.object_id
			WHERE object.release_id=$1 AND object.domain_id=$2
			  AND object.object_type=$3 AND object.object_version_id::text=ANY($4)
			ORDER BY source.id`, objectColumn, table, objectColumn)
		rows, queryErr := tx.Query(ctx, query, scope.Release.ReleaseID, domainID, releaseType, ids)
		if queryErr != nil {
			return queryErr
		}
		defer rows.Close()
		for rows.Next() {
			var ref ReleasedVersionRef
			if scanErr := rows.Scan(&ref.ObjectID, &ref.ObjectVersionID, &ref.Version); scanErr != nil {
				return scanErr
			}
			refs = append(refs, ref)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	if len(refs) != len(ids) {
		return nil, fmt.Errorf("%w: graph identities are outside the pinned release", ErrQueryReadInvalid)
	}
	return refs, nil
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
	err := reader.read(ctx, scope, domainID, func(tx pgx.Tx) error {
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
	err := reader.read(ctx, scope, domainID, func(tx pgx.Tx) error {
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
// touches, resolved against the measurement the materialization pipeline
// actually produced for the snapshot those models are pinned to.
//
// A rule is a binding to an executing check (see quality_rule.go), so the
// outcome here is a real PASSED/FAILED/SKIPPED from platform.data_quality_results
// rather than an assumption. A rule whose bound check produced no measurement
// for the pinned materialization reports as unproven, and unproven is never
// reported as passed.
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
	err = reader.read(ctx, scope, domainID, func(tx pgx.Tx) error {
		// The measurement is looked up through the materialization the semantic
		// model itself pins, so the reported quality belongs to exactly the
		// snapshot the query will read - not to some later rebuild.
		result, queryErr := tx.Query(ctx, `SELECT rule.code,rule.severity,
			COALESCE((
			  SELECT quality.status
			  FROM platform.data_quality_results AS quality
			  WHERE quality.tenant_id=model.tenant_id
			    AND quality.materialization_id=model.materialization_id
			    AND quality.rule_code=rule.rule_ast->>'datasetRuleCode'
			    AND quality.scope=rule.rule_ast->>'scope'
			    AND quality.field_id=COALESCE(rule.rule_ast->>'fieldId','')
			    AND (
			      COALESCE((rule.rule_ast->>'maxAgeHours')::int,0)=0
			      OR quality.measured_at>=now()-make_interval(
			        hours=>COALESCE((rule.rule_ast->>'maxAgeHours')::int,0)
			      )
			    )
			  ORDER BY quality.measured_at DESC,quality.id DESC
			  LIMIT 1
			),'UNMEASURED')
			FROM askdata.quality_rules AS rule
			JOIN askdata.release_objects AS object
			  ON object.object_version_id=rule.id AND object.tenant_id=rule.tenant_id
			 AND object.release_id=$1 AND object.object_type='QUALITY_RULE'
			JOIN askdata.semantic_models AS model
			  ON model.id=rule.target_version_id AND model.tenant_id=rule.tenant_id
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
			var measurement string
			if scanErr := result.Scan(&row.Code, &row.Severity, &measurement); scanErr != nil {
				return scanErr
			}
			// Only an actual PASSED measurement counts as passed. SKIPPED and
			// UNMEASURED are unproven, and unproven must never read as clean.
			row.Passed = measurement == string(materialization.QualityPassed)
			row.Measurement = measurement
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
//
// The absence of rules is UNKNOWN and never PASS: no governed quality rule
// having run is not evidence that the data is good, and the answer layer must
// be able to tell "verified clean" apart from "never checked". A rule that was
// never measured for the pinned snapshot is unproven for the same reason.
//
// The vocabulary is the frozen get_data_quality_status contract
// (PASS/WARNING/FAIL/UNKNOWN); precedence is FAIL > WARNING > UNKNOWN > PASS so
// the worst real finding is never hidden behind a milder one.
func qualityStatusFor(rules []QualityRuleRow) string {
	if len(rules) == 0 {
		return "UNKNOWN"
	}
	failed, warned, unproven := false, false, false
	for _, rule := range rules {
		switch {
		case rule.Passed:
		case rule.Measurement == string(materialization.QualityFailed):
			if rule.Severity == "BLOCKING" {
				failed = true
			} else {
				warned = true
			}
		default:
			unproven = true
		}
	}
	switch {
	case failed:
		return "FAIL"
	case warned:
		return "WARNING"
	case unproven:
		return "UNKNOWN"
	default:
		return "PASS"
	}
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
	domainID askdata.ID,
	run func(pgx.Tx) error,
) error {
	// Preserve the exact domain already validated by each public read. Clearing
	// app.domain_id put the transaction into USER mode without a selected domain,
	// so RLS correctly returned zero contracts; the Tool Host then rejected the
	// empty result as if the release had no governed evidence.
	ctx = database.WithAccessContext(ctx, string(scope.ActorID), string(domainID))
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
