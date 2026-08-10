package registry

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"

	"intelligent-report-generation-system/internal/askdata"
)

func TestKPIBundleValidateRejectsItemBoundsHeadlineAndComponentType(t *testing.T) {
	bundle := validKPIBundleFixture("overview")
	if err := bundle.Validate(); err != nil {
		t.Fatalf("valid KPI bundle error = %v", err)
	}
	tests := []struct {
		name string
		edit func(*KPIBundle)
		want string
	}{
		{name: "empty", edit: func(value *KPIBundle) {
			value.Items = nil
			value.DefaultDimensionVersionIDs = nil
		}, want: "items"},
		{name: "nine", edit: func(value *KPIBundle) {
			item := value.Items[0]
			value.Items = make([]KPIBundleItem, 9)
			for index := range value.Items {
				value.Items[index] = item
				value.Items[index].MetricVersionID = uuid.NewString()
				value.Items[index].Order = index + 1
			}
		}, want: "items"},
		{name: "headline", edit: func(value *KPIBundle) { value.Items[0].Role = KPIBundleRoleTrend }, want: "items"},
		{name: "chart", edit: func(value *KPIBundle) { value.Items[0].ChartType = "invented-chart" }, want: "items[0].chartType"},
		{name: "order", edit: func(value *KPIBundle) { value.Items[0].Order = 2 }, want: "order"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			invalid := bundle
			invalid.Items = append([]KPIBundleItem(nil), bundle.Items...)
			test.edit(&invalid)
			if err := invalid.Validate(); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Validate() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestKPIBundleReleaseRequiresPinnedMetricAndDimensionDependencies(t *testing.T) {
	bundle := validKPIBundleFixture("release")
	bundle.Status = VersionStatusCertified
	bundle.ContentHash = KPIBundleContentHash(bundle)
	bundleObject, err := KPIBundleReleaseObject(bundle)
	if err != nil {
		t.Fatal(err)
	}
	metricObject := releaseObject(ReleaseObjectMetric, uuid.NewString(), bundle.Items[0].MetricVersionID,
		`{"type":"METRIC","additivity":"FULLY_ADDITIVE","unit":"COUNT"}`)
	dimensionObject := releaseObject(ReleaseObjectDimension, uuid.NewString(), bundle.Items[0].GroupByDimensionVersionIDs[0],
		`{"type":"DIMENSION","name":"区域"}`)
	if _, err := BuildReleaseManifest([]ReleaseObject{bundleObject}); err == nil || !strings.Contains(err.Error(), "KPI_BUNDLE_METRIC_MISSING") {
		t.Fatalf("missing metric error = %v", err)
	}
	if _, err := BuildReleaseManifest([]ReleaseObject{bundleObject, metricObject}); err == nil || !strings.Contains(err.Error(), "KPI_BUNDLE_DIMENSION_MISSING") {
		t.Fatalf("missing dimension error = %v", err)
	}
	first, err := BuildReleaseManifest([]ReleaseObject{bundleObject, metricObject, dimensionObject})
	if err != nil {
		t.Fatalf("complete manifest error = %v", err)
	}
	changed := bundle
	changed.ID = uuid.NewString()
	changed.VersionNo = 2
	changed.DefaultTimeExpression = "PREVIOUS_MONTH"
	changed.ContentHash = KPIBundleContentHash(changed)
	changedObject, err := KPIBundleReleaseObject(changed)
	if err != nil {
		t.Fatal(err)
	}
	second, err := BuildReleaseManifest([]ReleaseObject{changedObject, metricObject, dimensionObject})
	if err != nil || first.ContentHash == second.ContentHash {
		t.Fatalf("version-pinned manifests = %s/%s, err=%v", first.ContentHash, second.ContentHash, err)
	}
}

func TestMatchBundleUniqueLowMarginNoMatchAndReleaseRollback(t *testing.T) {
	tenantID, domainID, actorID := askdata.ID(uuid.NewString()), askdata.ID(uuid.NewString()), askdata.ID(uuid.NewString())
	releaseV1 := askdata.ReleaseRef{ReleaseID: askdata.ID(uuid.NewString()), ContentHash: askdata.HashBytes([]byte("bundle-release-v1"))}
	releaseV2 := askdata.ReleaseRef{ReleaseID: askdata.ID(uuid.NewString()), ContentHash: askdata.HashBytes([]byte("bundle-release-v2"))}
	roleID := askdata.ID(uuid.NewString())
	scopeV1, err := askdata.NewPolicyScope(tenantID, actorID, []askdata.ID{domainID}, []askdata.ID{roleID}, releaseV1)
	if err != nil {
		t.Fatal(err)
	}
	scopeV2, err := askdata.NewPolicyScope(tenantID, actorID, []askdata.ID{domainID}, []askdata.ID{roleID}, releaseV2)
	if err != nil {
		t.Fatal(err)
	}
	first := validKPIBundleFixture("sales_overview_v1")
	first.TenantID, first.DomainID, first.Status = string(tenantID), string(domainID), VersionStatusCertified
	first.ApplicableQuestionPatterns = []string{"经营情况"}
	first.ContentHash = KPIBundleContentHash(first)
	second := validKPIBundleFixture("sales_overview_v2")
	second.TenantID, second.DomainID, second.Status = string(tenantID), string(domainID), VersionStatusCertified
	second.ObjectID, second.VersionNo = first.ObjectID, 2
	second.ApplicableQuestionPatterns = []string{"经营情况"}
	second.ContentHash = KPIBundleContentHash(second)
	competitor := validKPIBundleFixture("finance_overview")
	competitor.TenantID, competitor.DomainID, competitor.Status = string(tenantID), string(domainID), VersionStatusCertified
	competitor.ApplicableQuestionPatterns = []string{"经营情况"}
	competitor.ContentHash = KPIBundleContentHash(competitor)

	document := func(bundle KPIBundle) KPIBundleMatchDocument {
		return KPIBundleMatchDocument{Bundle: bundle, MetricLabels: map[string][]string{
			bundle.Items[0].MetricVersionID: {"销售额", "sales_total"},
		}}
	}
	loader := &fakeKPIBundleMatchLoader{snapshots: map[askdata.ID]KPIBundleMatchSnapshot{
		releaseV1.ReleaseID: {TenantID: tenantID, DomainID: domainID, Release: releaseV1, Bundles: []KPIBundleMatchDocument{document(first)}},
		releaseV2.ReleaseID: {TenantID: tenantID, DomainID: domainID, Release: releaseV2, Bundles: []KPIBundleMatchDocument{document(second)}},
	}}
	matcher, err := NewKPIBundleMatcher(loader, DefaultKPIBundleMatchConfig())
	if err != nil {
		t.Fatal(err)
	}
	unique, err := matcher.MatchBundle(context.Background(), scopeV1, domainID, KPIBundleMatchInput{Question: "这个月经营情况怎么样"})
	if err != nil || unique.Selected == nil || unique.Selected.BundleVersionID != askdata.ID(first.ID) || unique.ClarificationRequired {
		t.Fatalf("unique match = %#v/%v", unique, err)
	}
	rollback, err := matcher.MatchBundle(context.Background(), scopeV2, domainID, KPIBundleMatchInput{Question: "这个月经营情况怎么样"})
	if err != nil || rollback.Selected == nil || rollback.Selected.BundleVersionID != askdata.ID(second.ID) {
		t.Fatalf("release switch match = %#v/%v", rollback, err)
	}

	ambiguousLoader := &fakeKPIBundleMatchLoader{snapshots: map[askdata.ID]KPIBundleMatchSnapshot{
		releaseV1.ReleaseID: {TenantID: tenantID, DomainID: domainID, Release: releaseV1,
			Bundles: []KPIBundleMatchDocument{document(first), document(competitor)}},
	}}
	ambiguousMatcher, _ := NewKPIBundleMatcher(ambiguousLoader, DefaultKPIBundleMatchConfig())
	ambiguous, err := ambiguousMatcher.MatchBundle(context.Background(), scopeV1, domainID,
		KPIBundleMatchInput{Question: "经营情况", MetricMentions: []string{"销售额"}})
	if err != nil || !ambiguous.ClarificationRequired || ambiguous.Selected != nil || len(ambiguous.Candidates) != 2 {
		t.Fatalf("ambiguous match = %#v/%v", ambiguous, err)
	}
	none, err := matcher.MatchBundle(context.Background(), scopeV1, domainID, KPIBundleMatchInput{Question: "天气如何"})
	if err != nil || len(none.Candidates) != 0 || none.Selected != nil || none.ClarificationRequired {
		t.Fatalf("no match = %#v/%v", none, err)
	}
}

func validKPIBundleFixture(code string) KPIBundle {
	metricID, dimensionID := uuid.NewString(), uuid.NewString()
	bundle := KPIBundle{
		VersionIdentity: VersionIdentity{
			ID: uuid.NewString(), TenantID: uuid.NewString(), DomainID: uuid.NewString(),
			ObjectID: uuid.NewString(), VersionNo: 1, Status: VersionStatusDraft,
			OwnerID: uuid.NewString(), ContentHash: askdata.HashBytes([]byte("placeholder")),
		},
		Code: code, Name: "经营概览",
		Items: []KPIBundleItem{{
			MetricVersionID: metricID, Role: KPIBundleRoleHeadline,
			GroupByDimensionVersionIDs: []string{dimensionID}, ChartType: "metric-card", Order: 1,
		}},
		DefaultDimensionVersionIDs: []string{dimensionID}, DefaultTimeExpression: "CURRENT_MONTH",
		DefaultChartTypes: []string{"metric-card"}, RoleMapping: json.RawMessage(`{}`),
		ApplicableQuestionPatterns: []string{"经营情况"},
	}
	bundle.ContentHash = KPIBundleContentHash(bundle)
	return bundle
}

type fakeKPIBundleMatchLoader struct {
	snapshots map[askdata.ID]KPIBundleMatchSnapshot
}

func (loader *fakeKPIBundleMatchLoader) LoadKPIBundleSnapshot(
	_ context.Context,
	scope askdata.PolicyScope,
	_ askdata.ID,
) (KPIBundleMatchSnapshot, error) {
	return loader.snapshots[scope.Release.ReleaseID], nil
}
