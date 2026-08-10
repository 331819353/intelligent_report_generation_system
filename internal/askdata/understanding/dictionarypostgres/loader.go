package dictionarypostgres

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/understanding"
	"intelligent-report-generation-system/internal/platform/database"
)

type Loader struct{ pool *pgxpool.Pool }

func NewLoader(pool *pgxpool.Pool) *Loader { return &Loader{pool: pool} }

func (loader *Loader) LoadDictionary(
	ctx context.Context,
	scope askdata.PolicyScope,
	domainID askdata.ID,
) (understanding.DictionarySnapshot, error) {
	if loader == nil || loader.pool == nil || ctx == nil || scope.Validate() != nil ||
		domainID.Validate() != nil || !scopeContainsDomain(scope, domainID) {
		return understanding.DictionarySnapshot{}, understanding.ErrInvalidDictionaryRequest
	}
	access, authenticated := database.AccessContextFromContext(ctx)
	if !authenticated || access.UserID != string(scope.ActorID) {
		return understanding.DictionarySnapshot{}, understanding.ErrInvalidDictionaryRequest
	}
	snapshot := understanding.DictionarySnapshot{
		TenantID: scope.TenantID, DomainID: domainID, Release: scope.Release,
		Terms: []understanding.DictionaryTerm{},
	}
	err := database.WithTenantTx(ctx, loader.pool, string(scope.TenantID), func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT version.id::text,version.target_version_id::text,
			version.target_object_type,version.target_code,identity.term,identity.term_type,version.match_mode,
			COALESCE(version.match_pattern,''),version.priority,version.negative_contexts,
			version.applicable_role_ids::text[],version.valid_from,version.valid_to,
			version.content_hash
		FROM askdata.releases AS release
		JOIN askdata.release_objects AS object
		  ON object.tenant_id=release.tenant_id AND object.domain_id=release.domain_id
		 AND object.release_id=release.id AND object.object_type='BUSINESS_TERM'
		JOIN askdata.business_term_versions AS version
		  ON version.tenant_id=object.tenant_id AND version.domain_id=object.domain_id
		 AND version.id=object.object_version_id AND version.content_hash=object.content_hash
		JOIN askdata.business_terms AS identity
		  ON identity.tenant_id=version.tenant_id AND identity.domain_id=version.domain_id
		 AND identity.id=version.business_term_id
		WHERE release.tenant_id=askdata.current_tenant_id()
		  AND release.id=$1 AND release.content_hash=$2 AND release.domain_id=$3
		  AND release.status IN ('READY','ACTIVE')
		  AND version.status='CERTIFIED' AND version.review_status='APPROVED'
		ORDER BY identity.term,identity.term_type,version.priority DESC,version.id`,
			scope.Release.ReleaseID, scope.Release.ContentHash, domainID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			term := understanding.DictionaryTerm{TenantID: scope.TenantID, DomainID: domainID}
			var roleIDs []string
			var validFrom, validTo *time.Time
			if err := rows.Scan(
				&term.TermVersionID, &term.TargetVersionID, &term.TargetObjectType, &term.TargetCode,
				&term.Term, &term.TermType, &term.MatchMode, &term.MatchPattern,
				&term.Priority, &term.NegativeContexts, &roleIDs, &validFrom, &validTo,
				&term.ContentHash,
			); err != nil {
				return err
			}
			term.ValidFrom, term.ValidTo = validFrom, validTo
			term.ApplicableRoleIDs = make([]askdata.ID, len(roleIDs))
			for index, roleID := range roleIDs {
				term.ApplicableRoleIDs[index] = askdata.ID(roleID)
			}
			snapshot.Terms = append(snapshot.Terms, term)
		}
		return rows.Err()
	})
	if err != nil {
		return understanding.DictionarySnapshot{}, err
	}
	return snapshot, nil
}

func scopeContainsDomain(scope askdata.PolicyScope, domainID askdata.ID) bool {
	for _, candidate := range scope.DomainIDs {
		if candidate == domainID {
			return true
		}
	}
	return false
}

var _ understanding.DictionaryLoader = (*Loader)(nil)
