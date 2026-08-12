package registry

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/platform/database"
)

func TestAdminScopeRequiresCanonicalActorBoundContext(t *testing.T) {
	scope := AdminScope{
		TenantID: uuid.NewString(), DomainID: uuid.NewString(), ActorID: uuid.NewString(),
	}
	ctx := database.WithAccessContext(context.Background(), scope.ActorID, scope.DomainID)
	if err := scope.Validate(ctx); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	wrongActor := database.WithAccessContext(context.Background(), uuid.NewString(), scope.DomainID)
	if err := scope.Validate(wrongActor); !errors.Is(err, ErrRegistryPermissionDenied) {
		t.Fatalf("wrong actor error = %v", err)
	}
	invalid := scope
	invalid.DomainID = "NOT-A-UUID"
	if err := invalid.Validate(ctx); !errors.Is(err, ErrRegistryInvalidRequest) {
		t.Fatalf("invalid domain error = %v", err)
	}
}

func TestAdminCommandAndStableIDsAreDeterministic(t *testing.T) {
	requestID := uuid.NewString()
	command := AdminCommand{RequestID: requestID, ActionHash: askdata.HashBytes([]byte("create"))}
	if err := command.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	first := stableAdminID(requestID, string(AdminResourceDimension), "record")
	second := stableAdminID(requestID, string(AdminResourceDimension), "record")
	other := stableAdminID(requestID, string(AdminResourceDimension), "object")
	if first != second || first == other || !canonicalAdminUUID(first) {
		t.Fatalf("stable IDs = %q/%q/%q", first, second, other)
	}
	command.ActionHash = "invalid"
	if err := command.Validate(); !errors.Is(err, ErrRegistryInvalidRequest) {
		t.Fatalf("invalid action hash error = %v", err)
	}
}

func TestSortedAdminIDsPreservesEmptyArrayContract(t *testing.T) {
	values := sortedAdminIDs(nil)
	if values == nil || len(values) != 0 {
		t.Fatalf("sortedAdminIDs(nil) = %#v, want non-nil empty slice", values)
	}
}

func TestDraftContractsNormalizeSetValuedInputsAndMatchReleaseHash(t *testing.T) {
	term := BusinessTerm{
		VersionIdentity: VersionIdentity{ID: uuid.NewString(), TenantID: uuid.NewString(),
			DomainID: uuid.NewString(), ObjectID: uuid.NewString(), VersionNo: 1,
			Status: VersionStatusDraft, ContentHash: askdata.HashBytes([]byte("term")), OwnerID: uuid.NewString()},
		Term: "毛利率", TermType: TermTypeMetric, TargetObjectType: TermTargetMetric,
		TargetVersionID: uuid.NewString(), TargetCode: "gross_margin", MatchMode: TermMatchExact,
		Priority: 100, Source: TermSourceManual, ReviewStatus: TermReviewPending,
		Code: "gross_margin", Name: "毛利率", Definition: "毛利占销售收入比例",
		Aliases: sortedAdminAliases([]string{" 毛利率 ", "GM Rate"}),
	}
	if err := term.Validate(); err != nil {
		t.Fatalf("valid governed term error = %v", err)
	}
	contradictory := term
	contradictory.NegativeContexts = []string{"毛利"}
	if err := contradictory.Validate(); err == nil ||
		!strings.Contains(err.Error(), "negativeContexts") {
		t.Fatalf("contradictory negative context error = %v", err)
	}
	firstHash := businessTermContentHash(term)
	term.Aliases = sortedAdminAliases([]string{"GM Rate", "毛利率"})
	if secondHash := businessTermContentHash(term); firstHash != secondHash {
		t.Fatalf("term hashes differ after alias reordering: %s/%s", firstHash, secondHash)
	}

	metric := MetricVersion{
		VersionIdentity: VersionIdentity{
			ID: uuid.NewString(), ObjectID: uuid.NewString(), VersionNo: 1,
			Status: VersionStatusDraft,
		},
		MetricID: uuid.NewString(), SemanticModelVersionID: uuid.NewString(),
		FormulaAST:        json.RawMessage(`{"type":"MEASURE_REF","id":"measure:sales"}`),
		DefaultFiltersAST: json.RawMessage(`{"type":"TRUE"}`), TimeGrain: "MONTH",
		Additivity: Additive, Unit: "COUNT", NullPolicy: "PRESERVE",
		MeasureVersionIDs: []string{uuid.NewString(), uuid.NewString()},
	}
	metric.ObjectID = metric.MetricID
	metric.MeasureVersionIDs[0], metric.MeasureVersionIDs[1] = metric.MeasureVersionIDs[1], metric.MeasureVersionIDs[0]
	metric.MeasureVersionIDs = sortedAdminIDs(metric.MeasureVersionIDs)
	metric.ContentHash = metricVersionContentHash(metric)
	metric.Status = VersionStatusCertified
	object, err := MetricVersionReleaseObject(metric)
	if err != nil {
		t.Fatalf("MetricVersionReleaseObject() error = %v", err)
	}
	if object.ContentHash != metric.ContentHash {
		t.Fatalf("release hash = %s, draft hash = %s", object.ContentHash, metric.ContentHash)
	}
}

func TestVersionedDraftUpdateRequiresExactTimestampAndImmutableIdentity(t *testing.T) {
	now := time.Now().UTC().Round(time.Microsecond)
	identity := VersionIdentity{ObjectID: uuid.NewString(), VersionNo: 3, UpdatedAt: now}
	if err := validateVersionedUpdate(VersionedDraftInput{ExpectedUpdatedAt: &now}, identity); err != nil {
		t.Fatalf("valid update error = %v", err)
	}
	stale := now.Add(-time.Second)
	if err := validateVersionedUpdate(VersionedDraftInput{ExpectedUpdatedAt: &stale}, identity); !errors.Is(err, ErrRegistryVersionConflict) {
		t.Fatalf("stale update error = %v", err)
	}
	if err := validateVersionedUpdate(VersionedDraftInput{
		ExpectedUpdatedAt: &now, ObjectID: uuid.NewString(),
	}, identity); !errors.Is(err, ErrRegistryInvalidRequest) {
		t.Fatalf("identity mutation error = %v", err)
	}
}
