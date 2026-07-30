package dataset

import (
	"errors"
	"testing"
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
		{name: "DWS rejects DIM without DWD", kind: LLMTriggerDWSModeling, layer: LayerDIM},
		{name: "ADS accepts DWS", kind: LLMTriggerADSModeling, layer: LayerDWS, valid: true},
		{name: "ADS rejects ADS", kind: LLMTriggerADSModeling, layer: LayerADS},
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

func TestValidateLLMTriggerAssetsAcceptsJointDWSSelection(t *testing.T) {
	selected := []string{
		"0afedb1c-567f-4ed0-96f4-87fddfd4b02c",
		"2b893ed2-55d7-4318-a1f6-a3f2e47688e1",
		"30e3d99a-493b-43d7-9ca9-876ad3031126",
	}
	err := validateLLMTriggerAssets(
		LLMTriggerDWSModeling,
		selected,
		[]llmTriggerAsset{
			{ID: selected[0], Layer: LayerDWD},
			{ID: selected[1], Layer: LayerDWD},
			{ID: selected[2], Layer: LayerDIM},
		},
	)
	if err != nil {
		t.Fatalf("expected DWD + DWD + DIM to be a valid joint DWS scope, got %v", err)
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
		LLMTriggerDWSModeling,
		[]string{"0afedb1c-567f-4ed0-96f4-87fddfd4b02c"},
		nil,
	)
	if !errors.Is(err, ErrLLMTriggerScopeInvalid) {
		t.Fatalf("expected unavailable selection error, got %v", err)
	}
}
