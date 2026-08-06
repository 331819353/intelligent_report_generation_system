package compiler

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/graph"
	"intelligent-report-generation-system/internal/askdata/ir"
	"intelligent-report-generation-system/internal/askdata/registry"
	"intelligent-report-generation-system/internal/askdata/testfixture"
	"intelligent-report-generation-system/internal/platform/database"
)

type memoryContractStore struct {
	snapshot      ContractSnapshot
	lookups       []ContractLookup
	activeRelease askdata.ReleaseRef
}

func (store *memoryContractStore) LoadContractSnapshot(
	_ context.Context,
	lookup ContractLookup,
) (ContractSnapshot, error) {
	store.lookups = append(store.lookups, lookup)
	return store.snapshot, nil
}

func TestResolverPinsReleaseAndProducesStableResolution(t *testing.T) {
	request, artifact, scope := resolverBuildFixture(t)
	store := &memoryContractStore{
		snapshot:      metricOnlySnapshot(t, scope.Release),
		activeRelease: scope.Release,
	}
	resolver, err := NewResolver(store)
	if err != nil {
		t.Fatal(err)
	}
	ctx := database.WithAccessContext(context.Background(), string(scope.ActorID), "sales")
	first, err := resolver.Resolve(ctx, ResolveRequest{BuildRequest: request, BuildArtifact: artifact})
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Validate(); err != nil {
		t.Fatalf("resolution validation failed: %v", err)
	}
	// The store's mutable notion of a current release is deliberately ignored;
	// ContractLookup continues to carry the run's exact pinned release.
	store.activeRelease = askdata.ReleaseRef{
		ReleaseID: "release-query-v2", ContentHash: askdata.HashBytes([]byte("release-query-v2")),
	}
	second, err := resolver.Resolve(ctx, ResolveRequest{BuildRequest: request, BuildArtifact: artifact})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) || first.ResolutionHash != second.ResolutionHash {
		t.Fatalf("active release change altered pinned resolution:\nfirst=%#v\nsecond=%#v", first, second)
	}
	if len(store.lookups) != 2 || store.lookups[0].Scope.Release != scope.Release ||
		store.lookups[1].Scope.Release != scope.Release ||
		!reflect.DeepEqual(store.lookups[0].MetricVersionIDs, []askdata.ID{"metric-sales-v1"}) {
		t.Fatalf("unexpected exact lookups: %#v", store.lookups)
	}

	tampered := first
	tampered.Metrics[0].FormulaAST = json.RawMessage(`{"type":"FIELD","field":"other"}`)
	if !errors.Is(tampered.Validate(), ErrInvalidResolution) {
		t.Fatal("tampered resolved AST must fail resolution validation")
	}
}

func TestResolverRejectsStaleMaterializationAndAccessMismatch(t *testing.T) {
	request, artifact, scope := resolverBuildFixture(t)
	snapshot := metricOnlySnapshot(t, scope.Release)
	snapshot.Model.Materialization.Status = "RETIRED"
	resolver, err := NewResolver(&memoryContractStore{snapshot: snapshot})
	if err != nil {
		t.Fatal(err)
	}
	ctx := database.WithAccessContext(context.Background(), string(scope.ActorID), "sales")
	if _, err := resolver.Resolve(ctx, ResolveRequest{BuildRequest: request, BuildArtifact: artifact}); !errors.Is(err, ErrMaterializationStale) {
		t.Fatalf("stale materialization error = %v", err)
	}
	wrongActor := database.WithAccessContext(context.Background(), "actor-other", "sales")
	if _, err := resolver.Resolve(wrongActor, ResolveRequest{BuildRequest: request, BuildArtifact: artifact}); !errors.Is(err, ErrInvalidResolveRequest) {
		t.Fatalf("access mismatch error = %v", err)
	}
}

func TestResolvedContractsEnforceExactMemberAndTimeOwnership(t *testing.T) {
	request, artifact, scope := resolverBuildFixture(t)
	_ = request
	lookup := ContractLookup{
		Scope: scope, DomainID: "sales", IRHash: artifact.IRHash, ModelVersionID: "model-sales-v1",
		TimeDimensionVersionID: idPointer("dimension-order-date-v1"),
		MetricVersionIDs:       []askdata.ID{"metric-sales-v1"},
		DimensionVersionIDs:    []askdata.ID{"dimension-order-date-v1", "dimension-region-v1"},
		MemberVersionIDs:       []askdata.ID{"member-east-v1"},
		MemberBindings: []MemberBinding{{
			DimensionVersionID: "dimension-region-v1", MemberVersionID: "member-east-v1",
		}},
		RelationshipVersionIDs: nil,
	}
	if err := lookup.Validate(); err != nil {
		t.Fatal(err)
	}
	snapshot := metricOnlySnapshot(t, scope.Release)
	timeField := resolvedField(t, "order_date", "order_date", "TIME", "DATE")
	regionField := resolvedField(t, "region_code", "region_code", "DIMENSION", "STRING")
	snapshot.Model.Fields = append(snapshot.Model.Fields, timeField, regionField)
	snapshot.Model.PrimaryTimeFieldID = idPointer("order_date")
	snapshot.Dimensions = []DimensionContract{
		{
			DimensionVersionID: "dimension-order-date-v1", ModelVersionID: "model-sales-v1",
			LogicalFieldID: "order_date", ContentHash: hash("dimension-order-date-v1"),
			Kind: registry.DimensionTime, Sensitivity: registry.SensitivityInternal,
			MemberIndexPolicy: registry.MemberIndexNone,
		},
		{
			DimensionVersionID: "dimension-region-v1", ModelVersionID: "model-sales-v1",
			LogicalFieldID: "region_code", ContentHash: hash("dimension-region-v1"),
			Kind: registry.DimensionCategorical, Sensitivity: registry.SensitivityInternal,
			MemberIndexPolicy: registry.MemberIndexExactOnly,
		},
	}
	snapshot.Members = []MemberContract{{
		MemberVersionID: "member-east-v1", DimensionVersionID: "dimension-region-v1",
		ContentHash: hash("member-east-v1"), Sensitivity: registry.SensitivityInternal,
	}}
	snapshot.memberParameterValues = map[askdata.ID]string{"member-east-v1": "EAST"}
	if err := validateSnapshot(lookup, nil, snapshot); err != nil {
		t.Fatalf("valid detailed snapshot failed: %v", err)
	}
	serialized, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(serialized), "EAST") || strings.Contains(string(serialized), "memberParameter") {
		t.Fatalf("execution-only member parameter leaked into snapshot JSON: %s", serialized)
	}
	publicResolution, err := finalizeResolution(Resolution{
		Version: ResolutionVersion, Scope: scope, DomainID: "sales", IRHash: artifact.IRHash,
		BuildArtifactHash: hash("build-artifact"), GraphPlanHash: hash("graph-plan"),
		TimeDimensionVersionID: idPointer("dimension-order-date-v1"),
		MemberBindings:         append([]MemberBinding(nil), lookup.MemberBindings...),
		Model:                  snapshot.Model, Metrics: snapshot.Metrics, Dimensions: snapshot.Dimensions,
		Members: snapshot.Members, Relationships: snapshot.Relationships,
		memberParameterValues: cloneMemberParameterValues(snapshot.memberParameterValues),
	})
	if err != nil {
		t.Fatal(err)
	}
	resolutionRaw, err := json.Marshal(publicResolution)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(resolutionRaw), "EAST") {
		t.Fatalf("public Resolution leaked the canonical member key: %s", resolutionRaw)
	}
	var replayed Resolution
	if err := askdata.DecodeStrictJSON(resolutionRaw, &replayed); err != nil {
		t.Fatal(err)
	}
	if err := replayed.Validate(); err != nil {
		t.Fatalf("label-free public Resolution is not replayable: %#v, %v", replayed, err)
	}
	reversed := snapshot
	reversed.Model.Fields = append([]FieldContract(nil), snapshot.Model.Fields...)
	for left, right := 0, len(reversed.Model.Fields)-1; left < right; left, right = left+1, right-1 {
		reversed.Model.Fields[left], reversed.Model.Fields[right] = reversed.Model.Fields[right], reversed.Model.Fields[left]
	}
	reversed.Dimensions = append([]DimensionContract(nil), snapshot.Dimensions...)
	reversed.Dimensions[0], reversed.Dimensions[1] = reversed.Dimensions[1], reversed.Dimensions[0]
	normalized, err := normalizeSnapshot(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	normalizedReversed, err := normalizeSnapshot(reversed)
	if err != nil || !reflect.DeepEqual(normalized, normalizedReversed) {
		t.Fatalf("contract source order changed normalized snapshot: %#v, %v", normalizedReversed, err)
	}

	wrongMember := snapshot
	wrongMember.Members = append([]MemberContract(nil), snapshot.Members...)
	wrongMember.Members[0].DimensionVersionID = "dimension-order-date-v1"
	if err := validateSnapshot(lookup, nil, wrongMember); !errors.Is(err, ErrContractUnavailable) {
		t.Fatalf("wrong member ownership error = %v", err)
	}
	wrongTime := snapshot
	wrongTime.Dimensions = append([]DimensionContract(nil), snapshot.Dimensions...)
	wrongTime.Dimensions[0].LogicalFieldID = "region_code"
	if err := validateSnapshot(lookup, nil, wrongTime); !errors.Is(err, ErrContractUnavailable) {
		t.Fatalf("wrong time field error = %v", err)
	}
	missing := snapshot
	missing.Dimensions = missing.Dimensions[:1]
	if err := validateSnapshot(lookup, nil, missing); !errors.Is(err, ErrContractUnavailable) {
		t.Fatalf("incomplete exact object set error = %v", err)
	}
}

func TestRelationshipPathMustMatchEveryPinnedContract(t *testing.T) {
	pathValue, err := graph.NewJoinPath([]graph.JoinStep{{
		Hop:                   1,
		RelationshipVersionID: "relationship-sales-lines-v1",
		FromModelVersionID:    "model-sales-v1", ToModelVersionID: "model-lines-v1",
		Direction: graph.TraversalForward,
		JoinType:  registry.JoinLeft, Cardinality: registry.CardinalityManyToOne,
		FanoutPolicy: registry.FanoutSafe,
	}})
	if err != nil {
		t.Fatal(err)
	}
	path := &pathValue
	contracts := []RelationshipContract{{
		RelationshipVersionID: "relationship-sales-lines-v1",
		ContentHash:           hash("relationship-sales-lines-v1"),
		LeftModelVersionID:    "model-sales-v1", RightModelVersionID: "model-lines-v1",
		JoinAST:  json.RawMessage(`{"leftFieldId":"line_id","rightFieldId":"line_id","type":"EQUALS"}`),
		JoinType: registry.JoinLeft, Cardinality: registry.CardinalityManyToOne,
		FanoutPolicy: registry.FanoutSafe,
	}}
	if err := validateRelationshipPath(path, contracts); err != nil {
		t.Fatal(err)
	}
	contracts[0].Cardinality = registry.CardinalityOneToMany
	if err := validateRelationshipPath(path, contracts); !errors.Is(err, ErrContractUnavailable) {
		t.Fatalf("relationship mismatch error = %v", err)
	}
}

func TestMemberManifestContractIsStrictAndLabelFree(t *testing.T) {
	valid := []byte(`{"aliasVersionIds":["00000000-0000-0000-0000-000000000001"],"dimensionVersionId":"00000000-0000-0000-0000-000000000002","schemaVersion":"askdata-member-release-v1","type":"MEMBER"}`)
	if !safeMemberManifestContract(valid, "00000000-0000-0000-0000-000000000002") {
		t.Fatal("safe label-free member manifest was rejected")
	}
	withLabel := []byte(`{"aliasVersionIds":[],"dimensionVersionId":"00000000-0000-0000-0000-000000000002","label":"华东","schemaVersion":"askdata-member-release-v1","type":"MEMBER"}`)
	if safeMemberManifestContract(withLabel, "00000000-0000-0000-0000-000000000002") {
		t.Fatal("label-bearing member manifest must be rejected")
	}
}

func resolverBuildFixture(t *testing.T) (ir.BuildRequest, ir.BuildArtifact, askdata.PolicyScope) {
	t.Helper()
	request, err := testfixture.SemanticMetricBuildRequest()
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := ir.Build(request)
	if err != nil {
		t.Fatal(err)
	}
	return request, artifact, artifact.Scope
}

func metricOnlySnapshot(t *testing.T, release askdata.ReleaseRef) ContractSnapshot {
	t.Helper()
	field := resolvedField(t, "net_sales", "net_sales", "MEASURE", "DECIMAL")
	return ContractSnapshot{
		Release: release, ReleaseStatus: "READY", ReleaseObjectCount: 3,
		Model: ModelContract{
			ModelVersionID: "model-sales-v1", ContentHash: hash("model-sales-v1"),
			DatasetSchemaHash: hash("dataset-schema-v1"), GrainContract: json.RawMessage(`{"keys":["order_id"],"type":"ENTITY"}`),
			Fields: []FieldContract{field},
			Materialization: MaterializationContract{
				MaterializationID: "materialization-sales-v1", DatasetID: "dataset-sales",
				DatasetVersionID: "dataset-sales-v1", Layer: "DWS", Status: "ACTIVE",
				PublishedSchema: "warehouse_published", PublishedName: "dws_sales_orders",
				SchemaHash: hash("dataset-schema-v1"), SnapshotHash: hash("snapshot-v1"), RowCount: 10,
			},
		},
		Metrics: []MetricContract{{
			MetricVersionID: "metric-sales-v1", ModelVersionID: "model-sales-v1",
			ContentHash:      hash("metric-sales-v1"),
			FormulaAST:       json.RawMessage(`{"measureVersionId":"measure-sales-v1","type":"MEASURE_REF"}`),
			DefaultFilterAST: json.RawMessage(`{"type":"TRUE"}`), TimeGrain: "NONE",
			Additivity: registry.Additive, NullPolicy: "PRESERVE",
			Measures: []MeasureContract{{
				MeasureID: "measure-sales", MeasureVersionID: "measure-sales-v1", ModelVersionID: "model-sales-v1",
				ContentHash: hash("measure-sales-v1"), FormulaAST: json.RawMessage(`{"fieldId":"net_sales","type":"FIELD_REF"}`),
				Aggregation: registry.AggregationSum, Additivity: registry.Additive,
				DataType: registry.NumericDecimal, Unit: "CNY",
			}},
		}},
		Dimensions: []DimensionContract{}, Members: []MemberContract{}, Relationships: []RelationshipContract{},
	}
}

func resolvedField(t *testing.T, id, code, role, canonicalType string) FieldContract {
	t.Helper()
	field := FieldContract{
		FieldID: askdata.ID(id), Code: code, Role: role, CanonicalType: canonicalType,
		Nullable: false, Visible: true,
	}
	var err error
	field.ContractHash, err = fieldContractHash(field)
	if err != nil {
		t.Fatal(err)
	}
	return field
}

func hash(value string) askdata.ContentHash { return askdata.HashBytes([]byte(value)) }

func idPointer(value askdata.ID) *askdata.ID { return &value }
