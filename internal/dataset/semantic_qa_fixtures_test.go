package dataset

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSemanticQAFixturesFreezeMarketModelingContracts(t *testing.T) {
	fixtureLayers := map[string]map[string]Layer{
		"dwd-multi-dimension.json": {
			"10000000-0000-4000-8000-000000000001": LayerODS,
			"20000000-0000-4000-8000-000000000001": LayerDIM,
			"20000000-0000-4000-8000-000000000002": LayerDIM,
			"20000000-0000-4000-8000-000000000003": LayerDIM,
		},
		"dwd-order-header-line.json": {
			"10000000-0000-4000-8000-000000000002": LayerODS,
			"10000000-0000-4000-8000-000000000003": LayerODS,
		},
		"dwd-product-category-bridge.json": {
			"10000000-0000-4000-8000-000000000004": LayerODS,
			"20000000-0000-4000-8000-000000000004": LayerDIM,
			"20000000-0000-4000-8000-000000000005": LayerDIM,
		},
		"dwd-scd2-customer-ownership.json": {
			"10000000-0000-4000-8000-000000000005": LayerODS,
			"20000000-0000-4000-8000-000000000006": LayerDIM,
		},
		"dws-monthly-order-trend.json": {
			"30000000-0000-4000-8000-000000000003": LayerDWD,
		},
		"dws-order-refund-multi-fact.json": {
			"30000000-0000-4000-8000-000000000001": LayerDWD,
			"30000000-0000-4000-8000-000000000002": LayerDWD,
		},
		"ads-consumer-contract.json": {
			"40000000-0000-4000-8000-000000000001": LayerDWS,
		},
	}
	for name, versions := range fixtureLayers {
		name, versions := name, versions
		t.Run(name, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "semantic-qa", name))
			if err != nil {
				t.Fatal(err)
			}
			prepared, err := Prepare(raw)
			if err != nil {
				var validation *ValidationError
				if errors.As(err, &validation) {
					t.Fatalf("fixture rejected: %#v", validation.Issues)
				}
				t.Fatal(err)
			}
			if err := ValidateLayerDependencies(
				context.Background(),
				prepared.Document,
				layerResolverStub{layers: versions},
			); err != nil {
				t.Fatalf("layer dependency rejected: %v", err)
			}
		})
	}
}

func TestSemanticQAFanoutCounterexampleFailsClosed(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(
		"..", "..", "testdata", "semantic-qa",
		"invalid-dwd-uncontrolled-fanout.json",
	))
	if err != nil {
		t.Fatal(err)
	}
	_, err = Prepare(raw)
	var validation *ValidationError
	if !errors.As(err, &validation) {
		t.Fatalf("expected validation error, got %v", err)
	}
	found := false
	for _, issue := range validation.Issues {
		if strings.Contains(issue.Reason, "必须显式建模为 BRIDGE") {
			found = true
		}
	}
	if !found {
		t.Fatalf("fanout rejection missing: %#v", validation.Issues)
	}
}
