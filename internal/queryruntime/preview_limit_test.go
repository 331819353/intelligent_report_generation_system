package queryruntime

import (
	"context"
	"errors"
	"testing"

	"intelligent-report-generation-system/internal/dataset"
)

func TestDraftPreviewLimitIsTenRowsForEveryLayer(t *testing.T) {
	for _, layer := range []dataset.Layer{
		dataset.LayerODS,
		dataset.LayerDIM,
		dataset.LayerDWD,
		dataset.LayerDWS,
		dataset.LayerADS,
	} {
		if got := editablePreviewRowLimit(dataset.Document{
			Dataset: dataset.Descriptor{Layer: layer},
		}); got != 10 {
			t.Fatalf("%s editable preview limit = %d, want 10", layer, got)
		}
	}
}

func TestSavedDraftPreviewRejectsMoreThanTenRowsBeforeLoadingDataset(t *testing.T) {
	service := &Service{}
	_, err := service.Preview(
		context.Background(), "tenant", "actor", "dataset",
		dataset.PreviewInput{MaxRows: 11},
	)
	if !errors.Is(err, dataset.ErrPreviewInvalid) {
		t.Fatalf("preview maxRows=11 error = %v, want ErrPreviewInvalid", err)
	}
}
