package registry

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/platform/database"
)

var ErrBulkCertificationInvalid = errors.New("bulk semantic certification request is invalid")

type CertificationFailure struct {
	ObjectVersionID string                  `json:"objectVersionId"`
	Code            string                  `json:"code"`
	Message         string                  `json:"message"`
	Conflicts       []TermConflictCandidate `json:"conflicts,omitempty"`
}

type BulkCertificationError struct {
	Failures []CertificationFailure `json:"failures"`
}

func (err *BulkCertificationError) Error() string {
	return "one or more semantic versions failed certification"
}

type BulkCertificationResult struct {
	Certified []string `json:"certifiedObjectVersionIds"`
}

type CertificationService struct{ pool *pgxpool.Pool }

func NewCertificationService(pool *pgxpool.Pool) *CertificationService {
	return &CertificationService{pool: pool}
}

type certificationCandidate struct {
	VersionID string
	ObjectID  string
	Type      string
	Table     string
}

func (service *CertificationService) BulkCertify(
	ctx context.Context,
	scope AdminScope,
	domainID string,
	versionIDs []string,
	note string,
) (BulkCertificationResult, error) {
	if service == nil || service.pool == nil || scope.DomainID != domainID ||
		scope.Validate(ctx) != nil || len(versionIDs) == 0 || len(versionIDs) > 1000 ||
		note != strings.TrimSpace(note) || len(note) > 4000 || strings.ContainsAny(note, "\x00\r") {
		return BulkCertificationResult{}, ErrBulkCertificationInvalid
	}
	ids := append([]string(nil), versionIDs...)
	sort.Strings(ids)
	for index, id := range ids {
		parsed, err := uuid.Parse(id)
		if err != nil || parsed.String() != id || index > 0 && id == ids[index-1] {
			return BulkCertificationResult{}, ErrBulkCertificationInvalid
		}
	}
	result := BulkCertificationResult{}
	err := database.WithTenantTx(ctx, service.pool, scope.TenantID, func(tx pgx.Tx) error {
		if err := requireSemanticPermissionTx(ctx, tx, scope, AdminActionRelease, ""); err != nil {
			return err
		}
		candidates := make([]certificationCandidate, 0, len(ids))
		failures := make([]CertificationFailure, 0)
		for _, versionID := range ids {
			candidate, err := resolveCertificationCandidate(ctx, tx, domainID, versionID)
			if err != nil {
				failures = append(failures, CertificationFailure{
					ObjectVersionID: versionID, Code: "CERTIFY_NOT_FOUND_OR_NOT_DRAFT",
					Message: "version is not a DRAFT in the authenticated domain",
				})
				continue
			}
			if err := lockCertificationCandidate(ctx, tx, candidate); err != nil {
				return err
			}
			candidates = append(candidates, candidate)
		}
		ordered, err := orderCertificationCandidates(ctx, tx, candidates)
		if err != nil {
			for _, candidate := range candidates {
				failures = append(failures, CertificationFailure{
					ObjectVersionID: candidate.VersionID, Code: "CERTIFY_DEPENDENCY_CYCLE",
					Message: "selected semantic versions contain a certification dependency cycle",
				})
			}
		}
		if _, err := tx.Exec(ctx, "SAVEPOINT certify_batch_preflight"); err != nil {
			return err
		}
		for index, candidate := range ordered {
			savepoint := fmt.Sprintf("certify_candidate_%d", index)
			if _, err := tx.Exec(ctx, "SAVEPOINT "+savepoint); err != nil {
				return err
			}
			candidateErr := validateCertificationCandidate(ctx, tx, candidate)
			if candidateErr == nil {
				candidateErr = validateCertificationOverride(ctx, tx, candidate, note)
			}
			if candidateErr == nil {
				candidateErr = updateCertificationStatus(ctx, tx, candidate, scope.ActorID)
			}
			if candidateErr != nil {
				if _, err := tx.Exec(ctx, "ROLLBACK TO SAVEPOINT "+savepoint); err != nil {
					return err
				}
				failures = append(failures, CertificationFailure{
					ObjectVersionID: candidate.VersionID, Code: certificationFailureCode(candidateErr),
					Message:   sanitizeCertificationMessage(candidateErr.Error()),
					Conflicts: termConflictCandidates(candidateErr),
				})
			}
			if _, err := tx.Exec(ctx, "RELEASE SAVEPOINT "+savepoint); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, "ROLLBACK TO SAVEPOINT certify_batch_preflight"); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "RELEASE SAVEPOINT certify_batch_preflight"); err != nil {
			return err
		}
		if len(failures) > 0 {
			sort.Slice(failures, func(i, j int) bool { return failures[i].ObjectVersionID < failures[j].ObjectVersionID })
			return &BulkCertificationError{Failures: failures}
		}
		for _, candidate := range ordered {
			if err := validateCertificationCandidate(ctx, tx, candidate); err != nil {
				return err
			}
			if err := validateCertificationOverride(ctx, tx, candidate, note); err != nil {
				return err
			}
			if err := updateCertificationStatus(ctx, tx, candidate, scope.ActorID); err != nil {
				return err
			}
			detail, err := certificationAuditDetail(ctx, tx, candidate, note)
			if err != nil {
				return err
			}
			actionHash := askdata.HashBytes(detail)
			if _, err := tx.Exec(ctx, `INSERT INTO askdata.audit_events(
				id,tenant_id,domain_id,actor_id,event_type,resource_type,
				resource_id,action_hash,detail
			) VALUES($1,$2,$3,$4,'SEMANTIC_VERSION_CERTIFIED',$5,$6,$7,$8)`,
				uuid.NewString(), scope.TenantID, domainID, scope.ActorID,
				candidate.Type, candidate.ObjectID, actionHash, detail); err != nil {
				return err
			}
		}
		result.Certified = ids
		return nil
	})
	return result, normalizeAdminStoreError(err)
}

func lockCertificationCandidate(ctx context.Context, tx pgx.Tx, candidate certificationCandidate) error {
	allowed := map[string]struct{}{
		"semantic_models": {}, "measures": {}, "metric_versions": {},
		"metric_dimension_versions": {}, "dimensions": {}, "dimension_members": {},
		"hierarchies": {}, "relationships": {}, "quality_rules": {}, "business_term_versions": {},
		"certified_example_versions": {}, "kpi_bundle_versions": {}, "evaluation_case_versions": {},
		"time_contract_versions": {},
	}
	if _, exists := allowed[candidate.Table]; !exists {
		return ErrBulkCertificationInvalid
	}
	query := fmt.Sprintf("SELECT status FROM askdata.%s WHERE id=$1 FOR UPDATE", candidate.Table)
	var status string
	if err := tx.QueryRow(ctx, query, candidate.VersionID).Scan(&status); err != nil {
		return err
	}
	if status != "DRAFT" {
		return ErrRegistryVersionConflict
	}
	return nil
}

func orderCertificationCandidates(
	ctx context.Context,
	tx pgx.Tx,
	candidates []certificationCandidate,
) ([]certificationCandidate, error) {
	if len(candidates) < 2 {
		return append([]certificationCandidate(nil), candidates...), nil
	}
	ids := make([]string, len(candidates))
	byID := make(map[string]certificationCandidate, len(candidates))
	for index, candidate := range candidates {
		ids[index] = candidate.VersionID
		byID[candidate.VersionID] = candidate
	}
	edges, err := tx.Query(ctx, `WITH edges(dependent,dependency) AS (
		SELECT id::text,time_contract_version_id::text FROM askdata.semantic_models WHERE id::text=ANY($1) AND time_contract_version_id IS NOT NULL
		UNION ALL
		SELECT id::text,semantic_model_version_id::text FROM askdata.measures WHERE id::text=ANY($1)
		UNION ALL SELECT id::text,semantic_model_version_id::text FROM askdata.metric_versions WHERE id::text=ANY($1)
		UNION ALL SELECT metric_version_id::text,measure_version_id::text FROM askdata.metric_version_measures WHERE metric_version_id::text=ANY($1)
		UNION ALL SELECT id::text,metric_version_id::text FROM askdata.metric_dimension_versions WHERE id::text=ANY($1)
		UNION ALL SELECT id::text,dimension_version_id::text FROM askdata.metric_dimension_versions WHERE id::text=ANY($1)
		UNION ALL SELECT id::text,semantic_model_version_id::text FROM askdata.dimensions WHERE id::text=ANY($1)
		UNION ALL SELECT id::text,dimension_version_id::text FROM askdata.dimension_members WHERE id::text=ANY($1)
		UNION ALL SELECT id::text,parent_member_version_id::text FROM askdata.dimension_members WHERE id::text=ANY($1) AND parent_member_version_id IS NOT NULL
		UNION ALL SELECT hierarchy_version_id::text,dimension_version_id::text FROM askdata.hierarchy_levels WHERE hierarchy_version_id::text=ANY($1)
		UNION ALL SELECT id::text,left_model_version_id::text FROM askdata.relationships WHERE id::text=ANY($1)
		UNION ALL SELECT id::text,right_model_version_id::text FROM askdata.relationships WHERE id::text=ANY($1)
		UNION ALL SELECT id::text,bridge_model_version_id::text FROM askdata.relationships WHERE id::text=ANY($1) AND bridge_model_version_id IS NOT NULL
		UNION ALL SELECT id::text,target_version_id::text FROM askdata.quality_rules WHERE id::text=ANY($1)
		UNION ALL SELECT id::text,target_version_id::text FROM askdata.business_term_versions WHERE id::text=ANY($1)
		UNION ALL SELECT example.id::text,dependency::text FROM askdata.certified_example_versions AS example,LATERAL unnest(example.expected_metric_version_ids||example.expected_dimension_version_ids) AS dependency WHERE example.id::text=ANY($1)
		UNION ALL SELECT bundle.id::text,item->>'metricVersionId' FROM askdata.kpi_bundle_versions AS bundle,LATERAL jsonb_array_elements(bundle.items) AS item WHERE bundle.id::text=ANY($1)
		UNION ALL SELECT bundle.id::text,dependency FROM askdata.kpi_bundle_versions AS bundle,LATERAL jsonb_array_elements(bundle.items) AS item,LATERAL jsonb_array_elements_text(COALESCE(item->'groupByDimensionVersionIds','[]'::jsonb)) AS dependency WHERE bundle.id::text=ANY($1)
		UNION ALL SELECT bundle.id::text,dependency::text FROM askdata.kpi_bundle_versions AS bundle,LATERAL unnest(bundle.default_dimension_version_ids) AS dependency WHERE bundle.id::text=ANY($1)
		UNION ALL SELECT evaluation.id::text,dependency::text FROM askdata.evaluation_case_versions AS evaluation,LATERAL unnest(evaluation.expected_metric_version_ids||evaluation.expected_dimension_version_ids) AS dependency WHERE evaluation.id::text=ANY($1)
	)
	SELECT dependent,dependency FROM edges
	WHERE dependent=ANY($1) AND dependency=ANY($1) AND dependent<>dependency
	ORDER BY dependent,dependency`, ids)
	if err != nil {
		return nil, err
	}
	defer edges.Close()
	indegree := make(map[string]int, len(ids))
	dependents := make(map[string][]string, len(ids))
	for _, id := range ids {
		indegree[id] = 0
	}
	seenEdge := map[string]struct{}{}
	for edges.Next() {
		var dependent, dependency string
		if err := edges.Scan(&dependent, &dependency); err != nil {
			return nil, err
		}
		key := dependent + "\x00" + dependency
		if _, exists := seenEdge[key]; exists {
			continue
		}
		seenEdge[key] = struct{}{}
		indegree[dependent]++
		dependents[dependency] = append(dependents[dependency], dependent)
	}
	if err := edges.Err(); err != nil {
		return nil, err
	}
	ready := []string{}
	for _, id := range ids {
		if indegree[id] == 0 {
			ready = append(ready, id)
		}
	}
	sort.Strings(ready)
	ordered := make([]certificationCandidate, 0, len(ids))
	for len(ready) > 0 {
		id := ready[0]
		ready = ready[1:]
		ordered = append(ordered, byID[id])
		for _, dependent := range dependents[id] {
			indegree[dependent]--
			if indegree[dependent] == 0 {
				ready = append(ready, dependent)
				sort.Strings(ready)
			}
		}
	}
	if len(ordered) != len(candidates) {
		return nil, errors.New("certification dependency cycle")
	}
	return ordered, nil
}

func resolveCertificationCandidate(
	ctx context.Context,
	tx pgx.Tx,
	domainID, versionID string,
) (certificationCandidate, error) {
	rows, err := tx.Query(ctx, `
		SELECT id::text,model_id::text,'SEMANTIC_MODEL','semantic_models' FROM askdata.semantic_models WHERE id=$1 AND domain_id=$2 AND status='DRAFT'
		UNION ALL SELECT id::text,measure_id::text,'MEASURE','measures' FROM askdata.measures WHERE id=$1 AND domain_id=$2 AND status='DRAFT'
		UNION ALL SELECT id::text,metric_id::text,'METRIC','metric_versions' FROM askdata.metric_versions WHERE id=$1 AND domain_id=$2 AND status='DRAFT'
		UNION ALL SELECT id::text,metric_dimension_id::text,'METRIC_DIMENSION','metric_dimension_versions' FROM askdata.metric_dimension_versions WHERE id=$1 AND domain_id=$2 AND status='DRAFT'
		UNION ALL SELECT id::text,dimension_id::text,'DIMENSION','dimensions' FROM askdata.dimensions WHERE id=$1 AND domain_id=$2 AND status='DRAFT'
		UNION ALL SELECT id::text,member_id::text,'MEMBER','dimension_members' FROM askdata.dimension_members WHERE id=$1 AND domain_id=$2 AND status='DRAFT'
		UNION ALL SELECT id::text,hierarchy_id::text,'HIERARCHY','hierarchies' FROM askdata.hierarchies WHERE id=$1 AND domain_id=$2 AND status='DRAFT'
		UNION ALL SELECT id::text,relationship_id::text,'RELATIONSHIP','relationships' FROM askdata.relationships WHERE id=$1 AND domain_id=$2 AND status='DRAFT'
		UNION ALL SELECT id::text,quality_rule_id::text,'QUALITY_RULE','quality_rules' FROM askdata.quality_rules WHERE id=$1 AND domain_id=$2 AND status='DRAFT'
		UNION ALL SELECT id::text,time_contract_id::text,'TIME_CONTRACT','time_contract_versions' FROM askdata.time_contract_versions WHERE id=$1 AND domain_id=$2 AND status='DRAFT'
		UNION ALL SELECT id::text,business_term_id::text,'BUSINESS_TERM','business_term_versions' FROM askdata.business_term_versions WHERE id=$1 AND domain_id=$2 AND status='DRAFT'
		UNION ALL SELECT id::text,certified_example_id::text,'CERTIFIED_EXAMPLE','certified_example_versions' FROM askdata.certified_example_versions WHERE id=$1 AND domain_id=$2 AND status='DRAFT'
		UNION ALL SELECT id::text,kpi_bundle_id::text,'KPI_BUNDLE','kpi_bundle_versions' FROM askdata.kpi_bundle_versions WHERE id=$1 AND domain_id=$2 AND status='DRAFT'
		UNION ALL SELECT id::text,evaluation_case_asset_id::text,'EVAL_CASE','evaluation_case_versions' FROM askdata.evaluation_case_versions WHERE id=$1 AND domain_id=$2 AND status='DRAFT'`,
		versionID, domainID)
	if err != nil {
		return certificationCandidate{}, err
	}
	defer rows.Close()
	candidates := []certificationCandidate{}
	for rows.Next() {
		var candidate certificationCandidate
		if err := rows.Scan(&candidate.VersionID, &candidate.ObjectID, &candidate.Type, &candidate.Table); err != nil {
			return certificationCandidate{}, err
		}
		candidates = append(candidates, candidate)
	}
	if err := rows.Err(); err != nil {
		return certificationCandidate{}, err
	}
	if len(candidates) != 1 {
		return certificationCandidate{}, pgx.ErrNoRows
	}
	return candidates[0], nil
}

func updateCertificationStatus(ctx context.Context, tx pgx.Tx, candidate certificationCandidate, actorID string) error {
	allowed := map[string]struct{}{
		"semantic_models": {}, "measures": {}, "metric_versions": {},
		"metric_dimension_versions": {}, "dimensions": {}, "dimension_members": {},
		"hierarchies": {}, "relationships": {}, "quality_rules": {}, "business_term_versions": {},
		"certified_example_versions": {}, "kpi_bundle_versions": {}, "evaluation_case_versions": {},
		"time_contract_versions": {},
	}
	if _, ok := allowed[candidate.Table]; !ok {
		return ErrBulkCertificationInvalid
	}
	query := fmt.Sprintf("UPDATE askdata.%s SET status='CERTIFIED' WHERE id=$1 AND status='DRAFT'", candidate.Table)
	args := []any{candidate.VersionID}
	if candidate.Table == "business_term_versions" {
		query = `UPDATE askdata.business_term_versions SET status='CERTIFIED',
			review_status='APPROVED',reviewed_by=$2,reviewed_at=now()
			WHERE id=$1 AND status='DRAFT'`
		args = append(args, actorID)
	}
	tag, err := tx.Exec(ctx, query, args...)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrRegistryVersionConflict
	}
	if candidate.Table == "metric_versions" {
		if _, err := tx.Exec(ctx, `UPDATE askdata.metrics SET status='ACTIVE'
			WHERE id=$1 AND status='DRAFT'`, candidate.ObjectID); err != nil {
			return err
		}
	}
	if candidate.Table == "dimension_members" {
		if _, err := tx.Exec(ctx, `UPDATE askdata.dimension_member_aliases
			SET status='CERTIFIED' WHERE member_version_id=$1 AND status='DRAFT'`,
			candidate.VersionID); err != nil {
			return err
		}
	}
	return nil
}

func validateCertificationCandidate(ctx context.Context, tx pgx.Tx, candidate certificationCandidate) error {
	switch candidate.Table {
	case "metric_dimension_versions":
		var valid bool
		err := tx.QueryRow(ctx, `SELECT EXISTS(
			SELECT 1 FROM askdata.metric_dimension_versions AS compatibility
			JOIN askdata.metric_versions AS metric ON metric.id=compatibility.metric_version_id
			JOIN askdata.dimensions AS dimension ON dimension.id=compatibility.dimension_version_id
			WHERE compatibility.id=$1 AND metric.status='CERTIFIED'
			  AND dimension.status='CERTIFIED' AND metric.domain_id=compatibility.domain_id
			  AND dimension.domain_id=compatibility.domain_id
		)`, candidate.VersionID).Scan(&valid)
		if err != nil || !valid {
			return errors.New("CERTIFY_REFERENCE_INVALID: metric and dimension must be certified in the same domain")
		}
	case "hierarchies":
		var valid bool
		if err := tx.QueryRow(ctx, `SELECT count(*) BETWEEN 2 AND 32
			AND min(ordinal)=1 AND max(ordinal)=count(*)
			FROM askdata.hierarchy_levels WHERE hierarchy_version_id=$1`,
			candidate.VersionID).Scan(&valid); err != nil || !valid {
			return errors.New("HIERARCHY_LEVELS_INVALID: hierarchy requires two to thirty-two contiguous levels")
		}
	case "row_access_policies":
		// Certifying a predicate that no longer references the subject would
		// turn a row access control into a filter everyone shares.
		var predicate []byte
		if err := tx.QueryRow(ctx, `SELECT predicate_ast FROM askdata.row_access_policies
			WHERE id=$1`, candidate.VersionID).Scan(&predicate); err != nil {
			return errors.New("ROW_ACCESS_POLICY_INVALID: row access policy was not found")
		}
		if _, err := ParseRowAccessPredicate(predicate); err != nil {
			return errors.New("ROW_ACCESS_POLICY_INVALID: predicate must be boolean and reference at least one subject attribute")
		}
	case "quality_rules":
		// Certifying a rule bound to a check nothing executes would create an
		// object that can only ever report "unproven".
		var ruleAST []byte
		if err := tx.QueryRow(ctx, `SELECT rule_ast FROM askdata.quality_rules
			WHERE id=$1`, candidate.VersionID).Scan(&ruleAST); err != nil {
			return errors.New("QUALITY_RULE_BINDING_INVALID: quality rule was not found")
		}
		if _, err := DecodeQualityRuleBinding(ruleAST); err != nil {
			return errors.New("QUALITY_RULE_BINDING_INVALID: rule must bind to an executing dataset quality check")
		}
	case "relationships":
		var cardinality, fanoutPolicy, bridgeModelVersionID string
		if err := tx.QueryRow(ctx, `SELECT COALESCE(cardinality,''),COALESCE(fanout_policy,''),
			COALESCE(bridge_model_version_id::text,'')
			FROM askdata.relationships WHERE id=$1`, candidate.VersionID).Scan(
			&cardinality, &fanoutPolicy, &bridgeModelVersionID,
		); err != nil || ValidateRelationshipCombination(
			Cardinality(cardinality), FanoutPolicy(fanoutPolicy), bridgeModelVersionID,
		) != nil {
			return errors.New("RELATIONSHIP_FANOUT_INVALID: cardinality and fanout policy combination is unsafe")
		}
	case "business_term_versions":
		return validateTermCertification(ctx, tx, candidate.VersionID)
	case "certified_example_versions":
		return validateCertifiedExampleCertification(ctx, tx, candidate.VersionID)
	case "kpi_bundle_versions":
		return validateKPIBundleCertification(ctx, tx, candidate.VersionID)
	case "evaluation_case_versions":
		return validateEvaluationCaseCertification(ctx, tx, candidate.VersionID)
	}
	return nil
}

func validateTermCertification(ctx context.Context, tx pgx.Tx, versionID string) error {
	var domainID, term, termType, matchMode, matchPattern string
	var priority int
	var targetID string
	var negatives []string
	err := tx.QueryRow(ctx, `SELECT version.domain_id::text,identity.term,identity.term_type,version.match_mode,
		COALESCE(version.match_pattern,''),version.priority,version.target_version_id::text,
		version.negative_contexts
		FROM askdata.business_term_versions AS version
		JOIN askdata.business_terms AS identity ON identity.id=version.business_term_id
		WHERE version.id=$1`, versionID).
		Scan(&domainID, &term, &termType, &matchMode, &matchPattern, &priority, &targetID, &negatives)
	if err != nil || termType == "" || priority < 0 || targetID == "" {
		return errors.New("TERM_INVALID: term version is incomplete")
	}
	var targetValid bool
	if err := tx.QueryRow(ctx, `SELECT CASE version.target_object_type
		WHEN 'METRIC' THEN EXISTS(SELECT 1 FROM askdata.metric_versions WHERE id=version.target_version_id AND domain_id=version.domain_id AND status='CERTIFIED')
		WHEN 'DIMENSION' THEN EXISTS(SELECT 1 FROM askdata.dimensions WHERE id=version.target_version_id AND domain_id=version.domain_id AND status='CERTIFIED')
		WHEN 'MEMBER' THEN EXISTS(SELECT 1 FROM askdata.dimension_members WHERE id=version.target_version_id AND domain_id=version.domain_id AND status='CERTIFIED')
		WHEN 'TIME_CONTRACT' THEN EXISTS(SELECT 1 FROM askdata.time_contract_versions WHERE id=version.target_version_id AND domain_id=version.domain_id AND status='CERTIFIED')
		WHEN 'OPERATOR' THEN true
		WHEN 'LEGACY' THEN true
		WHEN 'CONCEPT' THEN true
		ELSE false END
		FROM askdata.business_term_versions AS version WHERE version.id=$1`, versionID).Scan(&targetValid); err != nil || !targetValid {
		return errors.New("TERM_TARGET_INVALID: target must be certified in the same domain")
	}
	conflicts, err := detectTermConflictsTx(ctx, tx, domainID, versionID, true)
	if err != nil {
		return err
	}
	if blocking := blockingTermConflicts(conflicts); len(blocking) > 0 {
		return &TermConflictError{Candidates: blocking}
	}
	for _, negative := range negatives {
		left, right := strings.ToLower(term), strings.ToLower(strings.TrimSpace(negative))
		if right == "" || strings.Contains(left, right) || strings.Contains(right, left) {
			return errors.New("TERM_NEGATIVE_CONTEXT_CONTRADICTION: negative context contradicts the term")
		}
	}
	if matchMode == TermMatchRegexpSafe {
		if _, err := CompileSafeTermRegexp(matchPattern); err != nil {
			return ErrTermRegexUnsafe
		}
	}
	return nil
}

func certificationAuditDetail(
	ctx context.Context,
	tx pgx.Tx,
	candidate certificationCandidate,
	note string,
) ([]byte, error) {
	detail := map[string]any{"objectVersionId": candidate.VersionID, "note": note}
	if candidate.Table == "business_term_versions" {
		var domainID string
		if err := tx.QueryRow(ctx, `SELECT domain_id::text FROM askdata.business_term_versions
			WHERE id=$1`, candidate.VersionID).Scan(&domainID); err != nil {
			return nil, err
		}
		conflicts, err := detectTermConflictsTx(ctx, tx, domainID, candidate.VersionID, false)
		if err != nil {
			return nil, err
		}
		if len(conflicts) > 0 {
			detail["termPriorityShadows"] = conflicts
		}
	}
	return CanonicalValue(detail)
}

func validateCertificationOverride(
	ctx context.Context,
	tx pgx.Tx,
	candidate certificationCandidate,
	note string,
) error {
	if candidate.Table != "business_term_versions" || note != "" {
		return nil
	}
	var domainID string
	if err := tx.QueryRow(ctx, `SELECT domain_id::text FROM askdata.business_term_versions
		WHERE id=$1`, candidate.VersionID).Scan(&domainID); err != nil {
		return err
	}
	conflicts, err := detectTermConflictsTx(ctx, tx, domainID, candidate.VersionID, false)
	if err != nil {
		return err
	}
	if len(conflicts) > 0 {
		return errors.New("TERM_PRIORITY_OVERRIDE_NOTE_REQUIRED: different-priority target requires an explicit owner note")
	}
	return nil
}

func validateCertifiedExampleCertification(ctx context.Context, tx pgx.Tx, versionID string) error {
	var valid bool
	err := tx.QueryRow(ctx, `SELECT NOT EXISTS(
		SELECT 1 FROM askdata.certified_example_versions AS example,
		LATERAL unnest(example.expected_metric_version_ids) AS metric_id
		LEFT JOIN askdata.metric_versions AS metric ON metric.id=metric_id
		WHERE example.id=$1 AND (metric.id IS NULL OR metric.status<>'CERTIFIED' OR metric.domain_id<>example.domain_id)
	) AND NOT EXISTS(
		SELECT 1 FROM askdata.certified_example_versions AS example,
		LATERAL unnest(example.expected_dimension_version_ids) AS dimension_id
		LEFT JOIN askdata.dimensions AS dimension ON dimension.id=dimension_id
		WHERE example.id=$1 AND (dimension.id IS NULL OR dimension.status<>'CERTIFIED' OR dimension.domain_id<>example.domain_id)
	) AND NOT EXISTS(
		SELECT 1 FROM askdata.certified_example_versions AS example,
		LATERAL unnest(example.applicable_role_ids) AS role_id
		LEFT JOIN platform.roles AS role ON role.id=role_id AND role.tenant_id=example.tenant_id
		WHERE example.id=$1 AND (role.id IS NULL OR role.status<>'ACTIVE' OR role.deleted_at IS NOT NULL)
	)`, versionID).Scan(&valid)
	if err != nil || !valid {
		return errors.New("CERTIFIED_EXAMPLE_REFERENCE_INVALID: expected assets must be certified in the same domain")
	}
	return nil
}

func validateKPIBundleCertification(ctx context.Context, tx pgx.Tx, versionID string) error {
	var bundle KPIBundle
	if err := scanKPIBundle(tx.QueryRow(ctx, kpiBundleAdminSelect+` WHERE version.id=$1`, versionID), &bundle); err != nil {
		return err
	}
	if err := bundle.Validate(); err != nil {
		return err
	}
	if KPIBundleContentHash(bundle) != bundle.ContentHash {
		return errors.New("KPI_BUNDLE_CONTENT_HASH_INVALID: stored hash does not match the governed contract")
	}
	return validateKPIBundleReferencesTx(ctx, tx, bundle)
}

func validateEvaluationCaseCertification(ctx context.Context, tx pgx.Tx, versionID string) error {
	var valid bool
	err := tx.QueryRow(ctx, `SELECT NOT EXISTS(
		SELECT 1 FROM askdata.evaluation_case_versions AS evaluation,
		LATERAL unnest(evaluation.expected_metric_version_ids) AS metric_id
		LEFT JOIN askdata.metric_versions AS metric ON metric.id=metric_id
		WHERE evaluation.id=$1 AND (metric.id IS NULL OR metric.status<>'CERTIFIED' OR metric.domain_id<>evaluation.domain_id)
	) AND NOT EXISTS(
		SELECT 1 FROM askdata.evaluation_case_versions AS evaluation,
		LATERAL unnest(evaluation.expected_dimension_version_ids) AS dimension_id
		LEFT JOIN askdata.dimensions AS dimension ON dimension.id=dimension_id
		WHERE evaluation.id=$1 AND (dimension.id IS NULL OR dimension.status<>'CERTIFIED' OR dimension.domain_id<>evaluation.domain_id)
	) AND EXISTS(
		SELECT 1 FROM askdata.evaluation_case_versions AS evaluation
		JOIN platform.roles AS role ON role.tenant_id=evaluation.tenant_id
		 AND role.code::text=evaluation.actor_role
		WHERE evaluation.id=$1 AND role.status='ACTIVE' AND role.deleted_at IS NULL
	)`, versionID).Scan(&valid)
	if err != nil || !valid {
		return errors.New("EVAL_CASE_REFERENCE_INVALID: expected assets must be certified in the same domain")
	}
	return nil
}

func certificationFailureCode(err error) string {
	message := err.Error()
	if index := strings.Index(message, ":"); index > 0 {
		code := strings.TrimSpace(message[:index])
		if code != "" && len(code) <= 128 {
			return code
		}
	}
	return "CERTIFY_STATIC_VALIDATION_FAILED"
}

func sanitizeCertificationMessage(message string) string {
	message = strings.TrimSpace(strings.Map(func(character rune) rune {
		if character < 0x20 {
			return ' '
		}
		return character
	}, message))
	if len(message) > 1024 {
		message = message[:1024]
	}
	return message
}
