package dataset

import (
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestNormalizeLLMTriggerScopeDeduplicatesAndSorts(t *testing.T) {
	scope, err := normalizeLLMTriggerScope(LLMTriggerScope{DatasetIDs: []string{
		"2B893ED2-55D7-4318-A1F6-A3F2E47688E1",
		"0afedb1c-567f-4ed0-96f4-87fddfd4b02c",
		"2b893ed2-55d7-4318-a1f6-a3f2e47688e1",
	}})
	if err != nil {
		t.Fatalf("normalize scope: %v", err)
	}
	if len(scope.DatasetIDs) != 2 ||
		scope.DatasetIDs[0] != "0afedb1c-567f-4ed0-96f4-87fddfd4b02c" ||
		scope.DatasetIDs[1] != "2b893ed2-55d7-4318-a1f6-a3f2e47688e1" {
		t.Fatalf("unexpected normalized scope: %#v", scope.DatasetIDs)
	}
}

func TestValidateLLMTriggerAssetsEnforcesLayerRules(t *testing.T) {
	selected := []string{"0afedb1c-567f-4ed0-96f4-87fddfd4b02c"}
	cases := []struct {
		name  string
		kind  LLMTriggerKind
		layer Layer
		valid bool
	}{
		{name: "DIM accepts ODS", kind: LLMTriggerDIMModeling, layer: LayerODS, valid: true},
		{name: "DIM rejects DIM", kind: LLMTriggerDIMModeling, layer: LayerDIM},
		{name: "DWD accepts ODS", kind: LLMTriggerDWDModeling, layer: LayerODS, valid: true},
		{name: "DWS accepts DWD", kind: LLMTriggerDWSModeling, layer: LayerDWD, valid: true},
		{name: "DWS rejects ODS", kind: LLMTriggerDWSModeling, layer: LayerODS},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			err := validateLLMTriggerAssets(
				testCase.kind,
				selected,
				[]llmTriggerAsset{{ID: selected[0], Layer: testCase.layer}},
			)
			if testCase.valid && err != nil {
				t.Fatalf("expected valid scope, got %v", err)
			}
			if !testCase.valid && !errors.Is(err, ErrLLMTriggerScopeInvalid) {
				t.Fatalf("expected scope error, got %v", err)
			}
		})
	}
}

func TestValidateLLMTriggerAssetsRequiresDWDForDWS(t *testing.T) {
	selected := []string{"0afedb1c-567f-4ed0-96f4-87fddfd4b02c"}
	err := validateLLMTriggerAssets(
		LLMTriggerDWSModeling,
		selected,
		[]llmTriggerAsset{{ID: selected[0], Layer: LayerDIM}},
	)
	if !errors.Is(err, ErrLLMTriggerScopeInvalid) {
		t.Fatalf("expected DWS DWD requirement, got %v", err)
	}
}

func TestValidateLLMTriggerAssetsRequiresODSForDWD(t *testing.T) {
	selected := []string{"0afedb1c-567f-4ed0-96f4-87fddfd4b02c"}
	err := validateLLMTriggerAssets(
		LLMTriggerDWDModeling,
		selected,
		[]llmTriggerAsset{{ID: selected[0], Layer: LayerDIM}},
	)
	if !errors.Is(err, ErrLLMTriggerScopeInvalid) {
		t.Fatalf("expected DWD ODS requirement, got %v", err)
	}
}

func TestValidateLLMTriggerAssetsRejectsUnavailableSelection(t *testing.T) {
	err := validateLLMTriggerAssets(
		LLMTriggerDWDModeling,
		[]string{"0afedb1c-567f-4ed0-96f4-87fddfd4b02c"},
		nil,
	)
	if !errors.Is(err, ErrLLMTriggerScopeInvalid) {
		t.Fatalf("expected unavailable selection error, got %v", err)
	}
}

func TestMapLLMTriggerPostgresErrorMapsDomainAuthorization(t *testing.T) {
	err := mapLLMTriggerPostgresError(&pgconn.PgError{Code: "42501"})
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("expected forbidden error, got %v", err)
	}
}
