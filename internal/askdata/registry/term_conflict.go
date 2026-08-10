package registry

import (
	"context"
	"errors"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"intelligent-report-generation-system/internal/platform/database"
)

type TermConflictCandidate struct {
	ObjectVersionID string     `json:"objectVersionId"`
	ObjectID        string     `json:"objectId"`
	TargetVersionID string     `json:"targetVersionId"`
	Priority        int        `json:"priority"`
	ValidFrom       *time.Time `json:"validFrom,omitempty"`
	ValidTo         *time.Time `json:"validTo,omitempty"`
	SamePriority    bool       `json:"samePriority"`
}

type TermConflictError struct{ Candidates []TermConflictCandidate }

func (err *TermConflictError) Error() string {
	return "TERM_PRIORITY_CONFLICT: overlapping approved term points at another target"
}

type TermService struct{ pool *pgxpool.Pool }

func NewTermService(pool *pgxpool.Pool) *TermService { return &TermService{pool: pool} }

func (service *TermService) DetectConflicts(
	ctx context.Context,
	scope AdminScope,
	versionID string,
) ([]TermConflictCandidate, error) {
	if service == nil || service.pool == nil || !canonicalAdminUUID(versionID) {
		return nil, ErrRegistryInvalidRequest
	}
	if err := scope.Validate(ctx); err != nil {
		return nil, err
	}
	result := []TermConflictCandidate{}
	err := database.WithTenantTx(ctx, service.pool, scope.TenantID, func(tx pgx.Tx) error {
		if err := requireSemanticPermissionTx(ctx, tx, scope, AdminActionView, ""); err != nil {
			return err
		}
		conflicts, err := detectTermConflictsTx(ctx, tx, scope.DomainID, versionID, false)
		if err != nil {
			return err
		}
		result = conflicts
		return nil
	})
	return result, normalizeAdminStoreError(err)
}

func detectTermConflictsTx(
	ctx context.Context,
	tx pgx.Tx,
	domainID, versionID string,
	lockIdentity bool,
) ([]TermConflictCandidate, error) {
	query := `SELECT version.business_term_id::text,version.target_version_id::text,
		version.priority,version.valid_from,version.valid_to
		FROM askdata.business_term_versions AS version
		JOIN askdata.business_terms AS identity ON identity.id=version.business_term_id
		WHERE version.id=$1 AND version.domain_id=$2`
	if lockIdentity {
		query += ` FOR UPDATE OF identity`
	}
	var objectID, targetVersionID string
	var priority int
	var validFrom, validTo *time.Time
	if err := tx.QueryRow(ctx, query, versionID, domainID).Scan(
		&objectID, &targetVersionID, &priority, &validFrom, &validTo,
	); err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `SELECT other.id::text,other.business_term_id::text,
		other.target_version_id::text,other.priority,other.valid_from,other.valid_to
		FROM askdata.business_term_versions AS other
		WHERE other.domain_id=$1 AND other.business_term_id=$2
		  AND other.status='CERTIFIED' AND other.review_status='APPROVED'
		  AND other.id<>$3 AND other.target_version_id<>$4
		  AND COALESCE(other.valid_from,'-infinity'::timestamptz)<COALESCE($6::timestamptz,'infinity'::timestamptz)
		  AND COALESCE($5::timestamptz,'-infinity'::timestamptz)<COALESCE(other.valid_to,'infinity'::timestamptz)
		ORDER BY other.priority DESC,other.version_no DESC,other.id`,
		domainID, objectID, versionID, targetVersionID, validFrom, validTo)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []TermConflictCandidate{}
	for rows.Next() {
		var candidate TermConflictCandidate
		if err := rows.Scan(
			&candidate.ObjectVersionID, &candidate.ObjectID, &candidate.TargetVersionID,
			&candidate.Priority, &candidate.ValidFrom, &candidate.ValidTo,
		); err != nil {
			return nil, err
		}
		candidate.SamePriority = candidate.Priority == priority
		result = append(result, candidate)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.Slice(result, func(left, right int) bool {
		if result[left].Priority != result[right].Priority {
			return result[left].Priority > result[right].Priority
		}
		return result[left].ObjectVersionID < result[right].ObjectVersionID
	})
	return result, nil
}

func blockingTermConflicts(conflicts []TermConflictCandidate) []TermConflictCandidate {
	result := []TermConflictCandidate{}
	for _, conflict := range conflicts {
		if conflict.SamePriority {
			result = append(result, conflict)
		}
	}
	return result
}

func termConflictCandidates(err error) []TermConflictCandidate {
	var conflict *TermConflictError
	if !errors.As(err, &conflict) {
		return nil
	}
	return conflict.Candidates
}
