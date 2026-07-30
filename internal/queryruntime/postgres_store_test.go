package queryruntime

import (
	"testing"

	"intelligent-report-generation-system/internal/dataset"
)

func TestDWSWarehousePreviewAcceptsDWDOrDIMInputs(t *testing.T) {
	allowed := warehousePreviewInputLayers(dataset.LayerDWS)
	if !allowed[dataset.LayerDWD] || !allowed[dataset.LayerDIM] ||
		allowed[dataset.LayerODS] || allowed[dataset.LayerDWS] {
		t.Fatalf("unexpected DWS preview input layers: %#v", allowed)
	}
	if !hasRequiredWarehousePreviewInput(
		dataset.LayerDWS,
		map[dataset.Layer]bool{dataset.LayerDWD: true},
	) {
		t.Fatal("DWS preview rejected a DWD materialization")
	}
	if !hasRequiredWarehousePreviewInput(
		dataset.LayerDWS,
		map[dataset.Layer]bool{dataset.LayerDIM: true},
	) {
		t.Fatal("factless DWS preview rejected a DIM materialization")
	}
	if hasRequiredWarehousePreviewInput(
		dataset.LayerDWS,
		map[dataset.Layer]bool{dataset.LayerODS: true},
	) {
		t.Fatal("DWS preview accepted a raw ODS materialization")
	}
}

func TestDWDWarehousePreviewStillRequiresFactInput(t *testing.T) {
	if hasRequiredWarehousePreviewInput(
		dataset.LayerDWD,
		map[dataset.Layer]bool{dataset.LayerDIM: true},
	) {
		t.Fatal("DWD preview accepted only dimension inputs")
	}
	if !hasRequiredWarehousePreviewInput(
		dataset.LayerDWD,
		map[dataset.Layer]bool{
			dataset.LayerODS: true,
			dataset.LayerDIM: true,
		},
	) {
		t.Fatal("DWD preview rejected a fact with governed dimensions")
	}
}
