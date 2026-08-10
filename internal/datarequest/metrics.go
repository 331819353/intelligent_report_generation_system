package datarequest

import (
	"math"
	"sort"
	"time"
)

type OperationalMetrics struct {
	RequestVolume              int64         `json:"requestVolume"`
	ApprovalDurationP50        time.Duration `json:"approvalDurationP50"`
	ApprovalDurationP95        time.Duration `json:"approvalDurationP95"`
	DeliveryDurationP50        time.Duration `json:"deliveryDurationP50"`
	DeliveryDurationP95        time.Duration `json:"deliveryDurationP95"`
	AssetizationConversionRate float64       `json:"assetizationConversionRate"`
}

// ComputeOperationalMetrics derives M19 request indicators from immutable
// lifecycle timestamps. Delivery latency starts at approval, when fulfilment is
// authorized, and excludes rejected or incomplete requests.
func ComputeOperationalMetrics(requests []Request) (OperationalMetrics, error) {
	if len(requests) > 1_000_000 {
		return OperationalMetrics{}, ErrInvalidRequest
	}
	approvalDurations := []time.Duration{}
	deliveryDurations := []time.Duration{}
	var delivered, assetized int64
	for _, request := range requests {
		if request.CreatedAt.IsZero() || request.UpdatedAt.IsZero() {
			return OperationalMetrics{}, ErrInvalidRequest
		}
		if request.SubmittedAt != nil && request.ApprovedAt != nil {
			duration := request.ApprovedAt.Sub(*request.SubmittedAt)
			if duration < 0 {
				return OperationalMetrics{}, ErrInvalidRequest
			}
			approvalDurations = append(approvalDurations, duration)
		}
		if request.ApprovedAt != nil && request.DeliveredAt != nil {
			duration := request.DeliveredAt.Sub(*request.ApprovedAt)
			if duration < 0 {
				return OperationalMetrics{}, ErrInvalidRequest
			}
			deliveryDurations = append(deliveryDurations, duration)
			delivered++
			if request.DeliveryType == DeliveryExistingReport || request.DeliveryType == DeliveryNewDataset {
				assetized++
			}
		}
	}
	result := OperationalMetrics{RequestVolume: int64(len(requests))}
	result.ApprovalDurationP50 = durationQuantile(approvalDurations, .50)
	result.ApprovalDurationP95 = durationQuantile(approvalDurations, .95)
	result.DeliveryDurationP50 = durationQuantile(deliveryDurations, .50)
	result.DeliveryDurationP95 = durationQuantile(deliveryDurations, .95)
	if delivered > 0 {
		result.AssetizationConversionRate = float64(assetized) / float64(delivered)
	}
	return result, nil
}

func durationQuantile(values []time.Duration, quantile float64) time.Duration {
	if len(values) == 0 {
		return 0
	}
	ordered := append([]time.Duration(nil), values...)
	sort.Slice(ordered, func(left, right int) bool { return ordered[left] < ordered[right] })
	if len(ordered) == 1 {
		return ordered[0]
	}
	position := quantile * float64(len(ordered)-1)
	lower := int(math.Floor(position))
	upper := int(math.Ceil(position))
	if lower == upper {
		return ordered[lower]
	}
	fraction := position - float64(lower)
	return ordered[lower] + time.Duration(math.Round(float64(ordered[upper]-ordered[lower])*fraction))
}
