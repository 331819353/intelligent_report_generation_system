package semanticmanagement

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

const (
	testTenantID  = "11111111-1111-4111-8111-111111111111"
	testActorID   = "22222222-2222-4222-8222-222222222222"
	testTagID     = "33333333-3333-4333-8333-333333333333"
	testAliasID   = "44444444-4444-4444-8444-444444444444"
	testBindingID = "55555555-5555-4555-8555-555555555555"
	testDatasetID = "66666666-6666-4666-8666-666666666666"
	testVersionID = "77777777-7777-4777-8777-777777777777"
)

type fakeStore struct {
	aliasInput       CreateTagAliasInput
	bindingInput     CreateAssetTagBindingInput
	updateAliasInput UpdateTagAliasInput
	tenantID         string
	actorID          string
	err              error
}

func (s *fakeStore) ListTags(context.Context, string, TagFilter) ([]Tag, int, error) {
	return []Tag{}, 0, s.err
}
func (s *fakeStore) CreateTag(context.Context, string, string, CreateTagInput) (Tag, error) {
	return Tag{}, s.err
}
func (s *fakeStore) UpdateTag(context.Context, string, string, string, UpdateTagInput) (Tag, error) {
	return Tag{}, s.err
}
func (s *fakeStore) DeprecateTag(context.Context, string, string, string, int64) (Tag, error) {
	return Tag{}, s.err
}
func (s *fakeStore) ListTagAliases(context.Context, string, AliasFilter) ([]TagAlias, int, error) {
	return []TagAlias{}, 0, s.err
}
func (s *fakeStore) CreateTagAlias(_ context.Context, tenantID, actorID string, input CreateTagAliasInput) (TagAlias, error) {
	s.tenantID, s.actorID, s.aliasInput = tenantID, actorID, input
	return TagAlias{ID: testAliasID, TagID: input.TagID, Alias: input.Alias, AliasType: input.AliasType}, s.err
}
func (s *fakeStore) UpdateTagAlias(_ context.Context, tenantID, actorID, _ string, input UpdateTagAliasInput) (TagAlias, error) {
	s.tenantID, s.actorID, s.updateAliasInput = tenantID, actorID, input
	return TagAlias{ID: testAliasID, TagID: input.TagID, Alias: input.Alias}, s.err
}
func (s *fakeStore) DeleteTagAlias(context.Context, string, string, string, string) error {
	return s.err
}
func (s *fakeStore) ListAssetTagBindings(context.Context, string, BindingFilter) ([]AssetTagBinding, int, error) {
	return []AssetTagBinding{}, 0, s.err
}
func (s *fakeStore) CreateAssetTagBinding(_ context.Context, tenantID, actorID string, input CreateAssetTagBindingInput) (AssetTagBinding, error) {
	s.tenantID, s.actorID, s.bindingInput = tenantID, actorID, input
	return AssetTagBinding{ID: testBindingID, TagID: input.TagID, AssetType: input.AssetType}, s.err
}
func (s *fakeStore) UpdateAssetTagBinding(context.Context, string, string, string, UpdateAssetTagBindingInput) (AssetTagBinding, error) {
	return AssetTagBinding{}, s.err
}
func (s *fakeStore) DeleteAssetTagBinding(context.Context, string, string, string, string) error {
	return s.err
}

func TestServiceAcceptsLegacyAliasWithoutHardcodedMapping(t *testing.T) {
	store := &fakeStore{}
	item, err := NewService(store).CreateTagAlias(context.Background(), testTenantID, testActorID, CreateTagAliasInput{
		TagID: testTagID, Alias: "690", AliasType: "legacy",
	})
	if err != nil {
		t.Fatal(err)
	}
	if item.Alias != "690" || store.aliasInput.Alias != "690" ||
		store.aliasInput.AliasType != "LEGACY" || store.aliasInput.LanguageCode != "zh-CN" {
		t.Fatalf("legacy alias was not preserved as governed data: item=%+v input=%+v", item, store.aliasInput)
	}
	if store.tenantID != testTenantID || store.actorID != testActorID {
		t.Fatalf("tenant/actor not forwarded: tenant=%q actor=%q", store.tenantID, store.actorID)
	}
}

func TestServiceRejectsUncontrolledAliasAndRequiresOptimisticToken(t *testing.T) {
	service := NewService(&fakeStore{})
	_, err := service.CreateTagAlias(context.Background(), testTenantID, testActorID, CreateTagAliasInput{
		TagID: testTagID, Alias: "690", AliasType: "UNTRUSTED",
	})
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("uncontrolled alias type error=%v", err)
	}
	_, err = service.UpdateTagAlias(context.Background(), testTenantID, testActorID, testAliasID, UpdateTagAliasInput{
		TagID: testTagID, Alias: "690", AliasType: "LEGACY", LanguageCode: "zh-CN",
	})
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("missing record version error=%v", err)
	}
}

func TestServiceValidatesBindingSubjectShapeAndEvidence(t *testing.T) {
	store := &fakeStore{}
	confidence := 0.9
	item, err := NewService(store).CreateAssetTagBinding(context.Background(), testTenantID, testActorID, CreateAssetTagBindingInput{
		TagID: testTagID, AssetType: "dataset_version",
		DatasetID: testDatasetID, DatasetVersionID: testVersionID,
		Origin: "llm", Status: "suggested", Confidence: &confidence,
		Evidence: json.RawMessage(`{"reason":"table function"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if item.AssetType != "DATASET_VERSION" || store.bindingInput.Origin != "LLM" ||
		store.bindingInput.Status != "SUGGESTED" {
		t.Fatalf("binding was not normalized: item=%+v input=%+v", item, store.bindingInput)
	}

	_, err = NewService(store).CreateAssetTagBinding(context.Background(), testTenantID, testActorID, CreateAssetTagBindingInput{
		TagID: testTagID, AssetType: "DATASET_FIELD",
		DatasetID: testDatasetID, DatasetVersionID: testVersionID,
		Origin: "USER", Evidence: json.RawMessage(`[]`),
	})
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("invalid target/evidence error=%v", err)
	}
}

func TestServiceRejectsDeprecatedTagAsEditableStatus(t *testing.T) {
	_, err := NewService(&fakeStore{}).UpdateTag(context.Background(), testTenantID, testActorID, testTagID, UpdateTagInput{
		ExpectedVersion: 1, Code: "home_ecosystem", Name: "智家生态圈",
		Category: "BUSINESS_DOMAIN", Governance: "CONTROLLED", Status: "DEPRECATED",
	})
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("deprecation must use the fenced endpoint: %v", err)
	}
}
