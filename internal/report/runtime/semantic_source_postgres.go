package runtime

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"intelligent-report-generation-system/internal/askdata"
	askcompiler "intelligent-report-generation-system/internal/askdata/compiler"
	"intelligent-report-generation-system/internal/platform/database"
	"intelligent-report-generation-system/internal/report/store"
)

type SemanticArtifactSource interface {
	LoadQueryArtifact(
		context.Context, store.Identity, askdata.ID, askdata.ID, askdata.ContentHash,
	) (askcompiler.QueryArtifact, error)
}

type SemanticCompilationSource interface {
	LoadCompilationArtifact(
		context.Context, store.Identity, askdata.ID, askdata.ID, askdata.ContentHash,
	) (askcompiler.QueryArtifact, error)
}

// PostgresSemanticArtifactSource crosses actor ownership only through the
// SECURITY DEFINER function in migration 000276. That function first proves
// that the current viewer may access the immutable report version and that the
// version references this exact source run and plan hash.
type PostgresSemanticArtifactSource struct{ pool *pgxpool.Pool }

func NewPostgresSemanticArtifactSource(pool *pgxpool.Pool) *PostgresSemanticArtifactSource {
	return &PostgresSemanticArtifactSource{pool: pool}
}

func (source *PostgresSemanticArtifactSource) LoadQueryArtifact(
	ctx context.Context,
	identity store.Identity,
	reportVersionID askdata.ID,
	sourceRunID askdata.ID,
	expectedPlanHash askdata.ContentHash,
) (artifact askcompiler.QueryArtifact, err error) {
	access, ok := database.AccessContextFromContext(ctx)
	if source == nil || source.pool == nil || identity.Validate() != nil ||
		reportVersionID.Validate() != nil || sourceRunID.Validate() != nil ||
		expectedPlanHash.Validate() != nil || !ok || access.UserID != string(identity.ActorID) ||
		access.DomainID != string(identity.DomainID) {
		return askcompiler.QueryArtifact{}, NewError(
			"NO_PERMISSION", "semantic report artifact is unavailable", nil,
		)
	}
	var raw []byte
	err = database.WithTenantTx(ctx, source.pool, string(identity.TenantID), func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT platform.load_report_runtime_query_artifact($1,$2,$3)`,
			reportVersionID, sourceRunID, expectedPlanHash,
		).Scan(&raw)
	})
	if err != nil {
		return askcompiler.QueryArtifact{}, NewError(
			"NO_PERMISSION", "semantic report artifact is unavailable", err,
		)
	}
	if err := askdata.DecodeStrictJSON(raw, &artifact); err != nil ||
		artifact.Validate() != nil || artifact.PlanHash != expectedPlanHash {
		return askcompiler.QueryArtifact{}, NewError(
			"REPORT_SEMANTIC_ARTIFACT_INVALID", "semantic report artifact is invalid", errors.Join(err, artifact.Validate()),
		)
	}
	return artifact, nil
}

func (source *PostgresSemanticArtifactSource) LoadCompilationArtifact(
	ctx context.Context,
	identity store.Identity,
	reportVersionID askdata.ID,
	compilationArtifactID askdata.ID,
	expectedPlanHash askdata.ContentHash,
) (artifact askcompiler.QueryArtifact, err error) {
	access, ok := database.AccessContextFromContext(ctx)
	if source == nil || source.pool == nil || identity.Validate() != nil ||
		reportVersionID.Validate() != nil || compilationArtifactID.Validate() != nil ||
		expectedPlanHash.Validate() != nil || !ok || access.UserID != string(identity.ActorID) ||
		access.DomainID != string(identity.DomainID) {
		return askcompiler.QueryArtifact{}, NewError("NO_PERMISSION", "semantic report compilation is unavailable", nil)
	}
	var raw []byte
	err = database.WithTenantTx(ctx, source.pool, string(identity.TenantID), func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT platform.load_report_runtime_compilation_artifact($1,$2,$3)`,
			reportVersionID, compilationArtifactID, expectedPlanHash,
		).Scan(&raw)
	})
	if err != nil {
		return askcompiler.QueryArtifact{}, NewError("NO_PERMISSION", "semantic report compilation is unavailable", err)
	}
	if err := askdata.DecodeStrictJSON(raw, &artifact); err != nil ||
		artifact.Validate() != nil || artifact.PlanHash != expectedPlanHash {
		return askcompiler.QueryArtifact{}, NewError(
			"REPORT_SEMANTIC_ARTIFACT_INVALID", "semantic report compilation is invalid",
			errors.Join(err, artifact.Validate()),
		)
	}
	return artifact, nil
}

type ViewerScopeResolver interface {
	ResolveViewerScope(
		context.Context, store.Identity, askdata.ReleaseRef,
	) (askdata.PolicyScope, error)
}

// PostgresViewerScopeResolver rebuilds the policy scope from current active
// roles while retaining the report's exact semantic Release. A publisher's
// historical role set is never reused for a viewer.
type PostgresViewerScopeResolver struct{ pool *pgxpool.Pool }

func NewPostgresViewerScopeResolver(pool *pgxpool.Pool) *PostgresViewerScopeResolver {
	return &PostgresViewerScopeResolver{pool: pool}
}

func (resolver *PostgresViewerScopeResolver) ResolveViewerScope(
	ctx context.Context,
	identity store.Identity,
	release askdata.ReleaseRef,
) (askdata.PolicyScope, error) {
	access, ok := database.AccessContextFromContext(ctx)
	if resolver == nil || resolver.pool == nil || identity.Validate() != nil || release.Validate() != nil ||
		!ok || access.UserID != string(identity.ActorID) || access.DomainID != string(identity.DomainID) {
		return askdata.PolicyScope{}, NewError("NO_PERMISSION", "semantic report access is unavailable", nil)
	}
	roleIDs := []askdata.ID{}
	err := database.WithTenantTx(ctx, resolver.pool, string(identity.TenantID), func(tx pgx.Tx) error {
		var runnable bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(
			SELECT 1 FROM askdata.releases
			WHERE id=$1 AND tenant_id=$2 AND domain_id=$3 AND content_hash=$4
			  AND status IN ('ACTIVE','SUPERSEDED','RETAINED')
		)`, release.ReleaseID, identity.TenantID, identity.DomainID, release.ContentHash).Scan(&runnable); err != nil {
			return err
		}
		if !runnable {
			return errors.New("semantic release is unavailable")
		}
		rows, err := tx.Query(ctx, `SELECT role.id::text
			FROM platform.user_roles AS assignment
			JOIN platform.roles AS role
			  ON role.tenant_id=assignment.tenant_id AND role.id=assignment.role_id
			WHERE assignment.tenant_id=$1 AND assignment.user_id=$2
			  AND role.status='ACTIVE' AND role.deleted_at IS NULL
			ORDER BY role.id LIMIT $3`,
			identity.TenantID, identity.ActorID, askdata.MaxPolicyRoles+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var roleID askdata.ID
			if err := rows.Scan(&roleID); err != nil {
				return err
			}
			roleIDs = append(roleIDs, roleID)
		}
		return rows.Err()
	})
	if err != nil || len(roleIDs) == 0 || len(roleIDs) > askdata.MaxPolicyRoles {
		return askdata.PolicyScope{}, NewError(
			"NO_PERMISSION", "semantic report access is unavailable", err,
		)
	}
	scope, err := askdata.NewPolicyScope(
		identity.TenantID, identity.ActorID, []askdata.ID{identity.DomainID}, roleIDs, release,
	)
	if err != nil {
		return askdata.PolicyScope{}, fmt.Errorf("resolve semantic report policy scope: %w", err)
	}
	return scope, nil
}
