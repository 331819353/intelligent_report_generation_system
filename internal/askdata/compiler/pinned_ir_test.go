package compiler

import (
	"context"
	"testing"

	"intelligent-report-generation-system/internal/platform/database"
)

func TestPinnedIRCompilerUsesExactReleaseAndCurrentActor(t *testing.T) {
	_, build, scope := resolverBuildFixture(t)
	store := &memoryContractStore{snapshot: metricOnlySnapshot(t, scope.Release)}
	compiler, err := NewPinnedIRCompiler(store)
	if err != nil {
		t.Fatal(err)
	}
	ctx := database.WithAccessContext(context.Background(), string(scope.ActorID), string(build.DomainID))
	first, err := compiler.CompilePinnedIR(ctx, PinnedIRCompileRequest{
		Scope: scope, SemanticIR: build.IR,
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := compiler.CompilePinnedIR(ctx, PinnedIRCompileRequest{
		Scope: scope, SemanticIR: build.IR,
	})
	if err != nil || first.PlanHash != second.PlanHash || first.IRHash != build.IRHash || len(first.Plans) != 1 {
		t.Fatalf("deterministic pinned compilation = %#v / %#v, %v", first, second, err)
	}
	wrongActor := database.WithAccessContext(context.Background(), "other-actor", string(build.DomainID))
	if _, err := compiler.CompilePinnedIR(wrongActor, PinnedIRCompileRequest{
		Scope: scope, SemanticIR: build.IR,
	}); err == nil {
		t.Fatal("pinned compilation accepted a different actor")
	}
}
