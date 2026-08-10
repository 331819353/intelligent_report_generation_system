package datarequest

import (
	"math"
	"testing"
	"time"
)

func TestComputeOperationalMetrics(t *testing.T) {
	base := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	requests := []Request{
		metricRequest(base, 1*time.Hour, 10*time.Hour, DeliveryExistingReport),
		metricRequest(base, 2*time.Hour, 20*time.Hour, DeliveryNewDataset),
		metricRequest(base, 3*time.Hour, 30*time.Hour, DeliveryOneTimeExport),
	}
	metrics, err := ComputeOperationalMetrics(requests)
	if err != nil {
		t.Fatal(err)
	}
	if metrics.RequestVolume != 3 || metrics.ApprovalDurationP50 != 2*time.Hour ||
		metrics.ApprovalDurationP95 != 2*time.Hour+54*time.Minute ||
		metrics.DeliveryDurationP50 != 20*time.Hour ||
		metrics.DeliveryDurationP95 != 29*time.Hour ||
		math.Abs(metrics.AssetizationConversionRate-2.0/3.0) > 1e-12 {
		t.Fatalf("metrics=%#v", metrics)
	}
}

func TestComputeOperationalMetricsZeroDenominatorIsFinite(t *testing.T) {
	metrics, err := ComputeOperationalMetrics(nil)
	if err != nil || metrics.AssetizationConversionRate != 0 ||
		math.IsNaN(metrics.AssetizationConversionRate) || math.IsInf(metrics.AssetizationConversionRate, 0) {
		t.Fatalf("metrics=%#v err=%v", metrics, err)
	}
}

func metricRequest(
	base time.Time,
	approvalDuration, deliveryDuration time.Duration,
	deliveryType DeliveryType,
) Request {
	submitted := base
	approved := submitted.Add(approvalDuration)
	delivered := approved.Add(deliveryDuration)
	return Request{
		CreatedAt: base.Add(-time.Hour), UpdatedAt: delivered,
		SubmittedAt: &submitted, ApprovedAt: &approved, DeliveredAt: &delivered,
		DeliveryType: deliveryType,
	}
}
