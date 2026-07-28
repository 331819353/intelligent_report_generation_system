package semanticasset

import (
	"context"
	"errors"
	"testing"
)

type serviceStore struct {
	imported []UpsertInput
}

func (store *serviceStore) List(
	context.Context, string, Filter,
) ([]Asset, int, error) {
	return nil, 0, nil
}

func (store *serviceStore) ListKnowledgeTypes(
	context.Context, string,
) ([]string, error) {
	return nil, nil
}

func (store *serviceStore) Create(
	context.Context, string, string, UpsertInput,
) (Asset, error) {
	return Asset{}, nil
}

func (store *serviceStore) Update(
	context.Context, string, string, string, UpdateInput,
) (Asset, error) {
	return Asset{}, nil
}

func (store *serviceStore) Deprecate(
	context.Context, string, string, string, int64,
) (Asset, error) {
	return Asset{}, nil
}

func (store *serviceStore) Import(
	_ context.Context,
	_ string,
	_ string,
	inputs []UpsertInput,
) (ImportResult, error) {
	store.imported = append([]UpsertInput(nil), inputs...)
	return ImportResult{Inserted: len(inputs), Total: len(inputs)}, nil
}

func TestImportNormalizesAndKeepsMappingValueCommas(t *testing.T) {
	store := &serviceStore{}
	service := NewService(store)
	result, err := service.Import(
		context.Background(),
		"11111111-1111-1111-1111-111111111111",
		"22222222-2222-2222-2222-222222222222",
		ImportInput{Items: []UpsertInput{{
			CommonTerm:    " 80后 ",
			MappingValue:  " 80-85,85-90,90-95,95-00,00后 ",
			KnowledgeType: " 赛道 ",
		}}},
	)
	if err != nil || result.Inserted != 1 || len(store.imported) != 1 {
		t.Fatalf("Import() result/error/items = %#v/%v/%#v", result, err, store.imported)
	}
	item := store.imported[0]
	if item.CommonTerm != "80后" ||
		item.MappingValue != "80-85,85-90,90-95,95-00,00后" ||
		item.KnowledgeType != "赛道" {
		t.Fatalf("normalized input = %#v", item)
	}
}

func TestImportRejectsDuplicateTermWithinKnowledgeType(t *testing.T) {
	service := NewService(&serviceStore{})
	_, err := service.Import(
		context.Background(),
		"11111111-1111-1111-1111-111111111111",
		"22222222-2222-2222-2222-222222222222",
		ImportInput{Items: []UpsertInput{
			{CommonTerm: "集团IT", MappingValue: "集团IT平台", KnowledgeType: "平台"},
			{CommonTerm: "集团it", MappingValue: "另一个值", KnowledgeType: "平台"},
		}},
	)
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("Import() error = %v, want ErrConflict", err)
	}
}
