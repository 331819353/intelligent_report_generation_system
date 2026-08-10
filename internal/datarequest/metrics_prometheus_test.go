package datarequest

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestOperationalMetricsRecorderPublishesM19Metrics(t *testing.T) {
	registry := prometheus.NewRegistry()
	recorder, err := NewOperationalMetricsRecorder(registry)
	if err != nil {
		t.Fatal(err)
	}
	metrics := OperationalMetrics{
		RequestVolume: 9, ApprovalDurationP50: time.Hour, ApprovalDurationP95: 3 * time.Hour,
		DeliveryDurationP50: 4 * time.Hour, DeliveryDurationP95: 12 * time.Hour,
		AssetizationConversionRate: .4,
	}
	if err := recorder.Record(uuid.NewString(), uuid.NewString(), metrics); err != nil {
		t.Fatal(err)
	}
	if value := testutil.ToFloat64(recorder.requestVolume); value != 9 {
		t.Fatalf("request volume=%v", value)
	}
	if value := testutil.ToFloat64(recorder.conversionRate); value != .4 {
		t.Fatalf("conversion rate=%v", value)
	}
}
