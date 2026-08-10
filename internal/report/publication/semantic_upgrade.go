package publication

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"intelligent-report-generation-system/internal/askdata"
	askcompiler "intelligent-report-generation-system/internal/askdata/compiler"
	"intelligent-report-generation-system/internal/askdata/ir"
	"intelligent-report-generation-system/internal/platform/database"
	reportmodel "intelligent-report-generation-system/internal/report"
	"intelligent-report-generation-system/internal/report/store"
)

var ErrSemanticCompilationInvalid = errors.New("report semantic compilation is invalid")

type SemanticCompilation struct {
	ID                  askdata.ID
	ComponentID         askdata.ID
	SemanticReleaseID   askdata.ID
	SemanticContentHash askdata.ContentHash
	SemanticIRHash      askdata.ContentHash
	SemanticIR          ir.SemanticIR
	Artifact            askcompiler.QueryArtifact
}

func (compilation SemanticCompilation) validate(identity store.Identity) error {
	if compilation.ID.Validate() != nil || compilation.ComponentID.Validate() != nil ||
		compilation.SemanticReleaseID.Validate() != nil || compilation.SemanticContentHash.Validate() != nil ||
		compilation.SemanticIRHash.Validate() != nil || compilation.SemanticIR.Validate() != nil ||
		compilation.Artifact.Validate() != nil ||
		compilation.SemanticIR.SemanticReleaseID != compilation.SemanticReleaseID ||
		compilation.SemanticIR.SemanticContentHash != compilation.SemanticContentHash ||
		compilation.SemanticIR.DomainID != identity.DomainID ||
		compilation.Artifact.DomainID != identity.DomainID ||
		compilation.Artifact.IRHash != compilation.SemanticIRHash ||
		compilation.Artifact.Scope.TenantID != identity.TenantID ||
		compilation.Artifact.Scope.ActorID != identity.ActorID ||
		compilation.Artifact.Scope.Release.ReleaseID != compilation.SemanticReleaseID ||
		compilation.Artifact.Scope.Release.ContentHash != compilation.SemanticContentHash {
		return ErrSemanticCompilationInvalid
	}
	return nil
}

type PinnedSemanticCompiler interface {
	CompilePinnedIR(context.Context, askcompiler.PinnedIRCompileRequest) (askcompiler.QueryArtifact, error)
}

type SemanticScopeResolver interface {
	ResolveViewerScope(context.Context, store.Identity, askdata.ReleaseRef) (askdata.PolicyScope, error)
}

// GovernedComponentRecompiler compiles only the upgraded, release-pinned IR
// under the current actor's scope. It returns an in-memory artifact; Preview
// does not persist it.
type GovernedComponentRecompiler struct {
	Scopes   SemanticScopeResolver
	Compiler PinnedSemanticCompiler
}

func (recompiler GovernedComponentRecompiler) RecompileComponent(
	ctx context.Context,
	identity store.Identity,
	reportID askdata.ID,
	component reportmodel.Component,
) (RecompiledSemantic, error) {
	if recompiler.Scopes == nil || recompiler.Compiler == nil || identity.Validate() != nil ||
		reportID.Validate() != nil || component.ID.Validate() != nil || component.DataBinding == nil ||
		component.DataBinding.BindingMode != reportmodel.BindingSemanticIR ||
		component.DataBinding.SemanticQueryRef == nil {
		return RecompiledSemantic{}, ErrSemanticCompilationInvalid
	}
	reference := *component.DataBinding.SemanticQueryRef
	normalizedIR, _, irHash, err := ir.Canonicalize(reference.SemanticIR)
	if err != nil || normalizedIR.DomainID != identity.DomainID ||
		normalizedIR.SemanticReleaseID != reference.SemanticReleaseID ||
		normalizedIR.SemanticContentHash != reference.SemanticContentHash {
		return RecompiledSemantic{}, ErrSemanticCompilationInvalid
	}
	release := askdata.ReleaseRef{ReleaseID: reference.SemanticReleaseID, ContentHash: reference.SemanticContentHash}
	scope, err := recompiler.Scopes.ResolveViewerScope(ctx, identity, release)
	if err != nil {
		return RecompiledSemantic{}, err
	}
	artifact, err := recompiler.Compiler.CompilePinnedIR(ctx, askcompiler.PinnedIRCompileRequest{
		Scope: scope, SemanticIR: normalizedIR, ResolvedTimeSpec: reference.ResolvedTimeSpec,
	})
	if err != nil {
		return RecompiledSemantic{}, err
	}
	compilationID := askdata.ID(uuid.NewSHA1(uuid.NameSpaceOID, []byte(
		"report-semantic-upgrade-v1\x00"+string(identity.TenantID)+"\x00"+string(reportID)+"\x00"+
			string(component.ID)+"\x00"+string(irHash)+"\x00"+string(artifact.PlanHash),
	)).String())
	reference.SemanticIR = normalizedIR
	reference.QueryPlanHash = artifact.PlanHash
	reference.SourceQuestionRunID = nil
	reference.CompilationArtifactID = &compilationID
	reference.ResolvedTimeSpec = artifact.ResolvedTimeSpec
	reference.EvidenceRefs = nil
	compilation := SemanticCompilation{
		ID: compilationID, ComponentID: component.ID,
		SemanticReleaseID: reference.SemanticReleaseID, SemanticContentHash: reference.SemanticContentHash,
		SemanticIRHash: irHash, SemanticIR: normalizedIR, Artifact: artifact,
	}
	if compilation.validate(identity) != nil {
		return RecompiledSemantic{}, ErrSemanticCompilationInvalid
	}
	return RecompiledSemantic{Reference: reference, Compilation: compilation}, nil
}

type CompilationStore interface {
	SaveCompilations(context.Context, store.Identity, askdata.ID, []SemanticCompilation) error
}

type PostgresCompilationStore struct{ pool *pgxpool.Pool }

func NewPostgresCompilationStore(pool *pgxpool.Pool) *PostgresCompilationStore {
	return &PostgresCompilationStore{pool: pool}
}

func (repository *PostgresCompilationStore) SaveCompilations(
	ctx context.Context,
	identity store.Identity,
	reportID askdata.ID,
	compilations []SemanticCompilation,
) error {
	if repository == nil || repository.pool == nil || identity.Validate() != nil || reportID.Validate() != nil ||
		len(compilations) == 0 {
		return ErrSemanticCompilationInvalid
	}
	for _, compilation := range compilations {
		if compilation.validate(identity) != nil {
			return ErrSemanticCompilationInvalid
		}
	}
	ctx = database.WithAccessContext(ctx, string(identity.ActorID), string(identity.DomainID))
	return database.WithTenantTx(ctx, repository.pool, string(identity.TenantID), func(tx pgx.Tx) error {
		for _, compilation := range compilations {
			artifactJSON, err := json.Marshal(compilation.Artifact)
			if err != nil {
				return err
			}
			_, semanticIRJSON, _, err := ir.Canonicalize(compilation.SemanticIR)
			if err != nil {
				return err
			}
			if _, err = tx.Exec(ctx, `INSERT INTO platform.report_semantic_compilations(
				id,tenant_id,domain_id,report_id,component_id,semantic_release_id,
				semantic_content_hash,semantic_ir_hash,semantic_ir_json,query_plan_hash,artifact_json,created_by
			) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
			ON CONFLICT DO NOTHING`,
				compilation.ID, identity.TenantID, identity.DomainID, reportID, compilation.ComponentID,
				compilation.SemanticReleaseID, compilation.SemanticContentHash, compilation.SemanticIRHash,
				semanticIRJSON, compilation.Artifact.PlanHash, artifactJSON, identity.ActorID,
			); err != nil {
				return err
			}
			var exact bool
			if err = tx.QueryRow(ctx, `SELECT semantic_release_id=$4 AND semantic_content_hash=$5 AND
				semantic_ir_hash=$6 AND semantic_ir_json=$7::jsonb AND query_plan_hash=$8 AND
				artifact_json=$9::jsonb AND created_by=$10
				FROM platform.report_semantic_compilations
				WHERE id=$1 AND tenant_id=$2 AND report_id=$3`,
				compilation.ID, identity.TenantID, reportID, compilation.SemanticReleaseID,
				compilation.SemanticContentHash, compilation.SemanticIRHash, semanticIRJSON,
				compilation.Artifact.PlanHash, artifactJSON, identity.ActorID,
			).Scan(&exact); err != nil || !exact {
				return fmt.Errorf("%w: deterministic artifact collision", ErrSemanticCompilationInvalid)
			}
		}
		return nil
	})
}
