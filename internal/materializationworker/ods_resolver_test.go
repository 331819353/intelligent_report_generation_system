package materializationworker

import (
	"context"
	"testing"

	"intelligent-report-generation-system/internal/datasource"
	"intelligent-report-generation-system/internal/materialization"
)

type resolverStub struct {
	calls int
}

func (resolver *resolverStub) Resolve(
	_ context.Context,
	_ materialization.Claim,
) (ResolvedBuild, error) {
	resolver.calls++
	return ResolvedBuild{}, nil
}

func TestCompositeResolverRoutesSourceBackedLayersToSourceResolver(t *testing.T) {
	for _, claim := range []materialization.Claim{
		{Run: materialization.Run{Layer: materialization.LayerODS}},
		{
			Run: materialization.Run{Layer: materialization.LayerDWS},
			Inputs: []materialization.InputSnapshot{{
				Type: materialization.InputSourceTable,
			}},
		},
	} {
		source := &resolverStub{}
		postgres := &resolverStub{}
		resolver := NewCompositeResolver(source, postgres)
		if _, err := resolver.Resolve(context.Background(), claim); err != nil {
			t.Fatalf("resolve source-backed %s: %v", claim.Layer, err)
		}
		if source.calls != 1 || postgres.calls != 0 {
			t.Fatalf("%s routing calls source=%d postgres=%d, want 1/0", claim.Layer, source.calls, postgres.calls)
		}
	}
}

func TestCompositeResolverKeepsWarehouseDWSOnPostgresResolver(t *testing.T) {
	source := &resolverStub{}
	postgres := &resolverStub{}
	resolver := NewCompositeResolver(source, postgres)
	claim := materialization.Claim{
		Run: materialization.Run{Layer: materialization.LayerDWS},
		Inputs: []materialization.InputSnapshot{{
			Type: materialization.InputMaterialization,
		}},
	}
	if _, err := resolver.Resolve(context.Background(), claim); err != nil {
		t.Fatalf("resolve warehouse DWS: %v", err)
	}
	if source.calls != 0 || postgres.calls != 1 {
		t.Fatalf("warehouse DWS routing calls source=%d postgres=%d, want 0/1", source.calls, postgres.calls)
	}
}

func TestODSMetadataCanonicalTypeUsesDatabaseMetadataForCodes(t *testing.T) {
	for _, sourceType := range []datasource.Type{
		datasource.TypeMySQL,
		datasource.TypeOracle,
	} {
		if got := odsMetadataCanonicalType(
			sourceType, "customer_id", "用户编号", "NUMBER",
		); got != "NUMBER" {
			t.Fatalf("%s code type = %q, want NUMBER", sourceType, got)
		}
	}
}

func TestODSMetadataCanonicalTypeKeepsExcelCodesAsText(t *testing.T) {
	if got := odsMetadataCanonicalType(
		datasource.TypeExcel, "SKU_ID", "商品编码", "NUMBER",
	); got != "TEXT" {
		t.Fatalf("Excel code type = %q, want TEXT", got)
	}
}
