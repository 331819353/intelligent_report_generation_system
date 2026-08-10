package observability

import (
	_ "embed"
	"encoding/json"
	"errors"
	"strings"
)

//go:embed dashboards/quality-cost.json
var qualityCostDashboard []byte

var requiredDashboardMetrics = []string{
	"askdata_e2e_strict_accuracy", "askdata_direct_answer_coverage", "askdata_clarification_rate",
	"askdata_stage_errors_total", "askdata_question_latency_seconds_bucket",
	"askdata_llm_calls_per_question_sum", "askdata_tool_calls_per_question_sum",
	"askdata_cost_per_answer_sum",
	"data_request_volume_30d", "data_request_approval_duration_seconds",
	"data_request_delivery_duration_seconds", "data_request_assetization_conversion_rate",
}

func QualityCostDashboard() ([]byte, error) {
	var document map[string]any
	if err := json.Unmarshal(qualityCostDashboard, &document); err != nil {
		return nil, err
	}
	if document["title"] == "" {
		return nil, errors.New("AskData quality/cost dashboard has no title")
	}
	text := string(qualityCostDashboard)
	for _, metric := range requiredDashboardMetrics {
		if !strings.Contains(text, metric) {
			return nil, errors.New("AskData quality/cost dashboard is missing " + metric)
		}
	}
	return append([]byte(nil), qualityCostDashboard...), nil
}
