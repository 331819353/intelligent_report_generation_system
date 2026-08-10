package datarequest

import (
	"errors"
	"math"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
)

var ErrOperationalMetricsInvalid = errors.New("data request operational metrics are invalid")

type OperationalMetricsRecorder struct {
	requestVolume   *prometheus.GaugeVec
	approvalLatency *prometheus.GaugeVec
	deliveryLatency *prometheus.GaugeVec
	conversionRate  *prometheus.GaugeVec
}

func NewOperationalMetricsRecorder(
	registerer prometheus.Registerer,
) (*OperationalMetricsRecorder, error) {
	if registerer == nil {
		return nil, ErrOperationalMetricsInvalid
	}
	labels := []string{"tenant_id", "domain_id"}
	recorder := &OperationalMetricsRecorder{
		requestVolume: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "data_request_volume_30d", Help: "Detail data requests created in the rolling 30 day window.",
		}, labels),
		approvalLatency: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "data_request_approval_duration_seconds", Help: "Data request approval duration quantiles over the rolling 30 day window.",
		}, append(labels, "quantile")),
		deliveryLatency: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "data_request_delivery_duration_seconds", Help: "Data request delivery duration quantiles over the rolling 30 day window.",
		}, append(labels, "quantile")),
		conversionRate: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "data_request_assetization_conversion_rate", Help: "Delivered requests fulfilled by an existing report or a new governed dataset.",
		}, labels),
	}
	if err := registerer.Register(recorder.requestVolume); err != nil {
		return nil, err
	}
	if err := registerer.Register(recorder.approvalLatency); err != nil {
		return nil, err
	}
	if err := registerer.Register(recorder.deliveryLatency); err != nil {
		return nil, err
	}
	if err := registerer.Register(recorder.conversionRate); err != nil {
		return nil, err
	}
	return recorder, nil
}

func (recorder *OperationalMetricsRecorder) Record(
	tenantID, domainID string, metrics OperationalMetrics,
) error {
	if recorder == nil || uuid.Validate(tenantID) != nil || uuid.Validate(domainID) != nil ||
		metrics.RequestVolume < 0 || metrics.ApprovalDurationP50 < 0 || metrics.ApprovalDurationP95 < 0 ||
		metrics.DeliveryDurationP50 < 0 || metrics.DeliveryDurationP95 < 0 ||
		math.IsNaN(metrics.AssetizationConversionRate) || math.IsInf(metrics.AssetizationConversionRate, 0) ||
		metrics.AssetizationConversionRate < 0 || metrics.AssetizationConversionRate > 1 {
		return ErrOperationalMetricsInvalid
	}
	labels := []string{tenantID, domainID}
	recorder.requestVolume.WithLabelValues(labels...).Set(float64(metrics.RequestVolume))
	recorder.approvalLatency.WithLabelValues(tenantID, domainID, "0.50").Set(metrics.ApprovalDurationP50.Seconds())
	recorder.approvalLatency.WithLabelValues(tenantID, domainID, "0.95").Set(metrics.ApprovalDurationP95.Seconds())
	recorder.deliveryLatency.WithLabelValues(tenantID, domainID, "0.50").Set(metrics.DeliveryDurationP50.Seconds())
	recorder.deliveryLatency.WithLabelValues(tenantID, domainID, "0.95").Set(metrics.DeliveryDurationP95.Seconds())
	recorder.conversionRate.WithLabelValues(labels...).Set(metrics.AssetizationConversionRate)
	return nil
}
