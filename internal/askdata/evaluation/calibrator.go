package evaluation

import (
	"fmt"

	"intelligent-report-generation-system/internal/askdata/binding"
)

// FitCalibratorFromEvaluation is the production training boundary: the
// EVAL-002 report must replay exactly against its source cases before TRAIN
// and held-out VALIDATION examples can influence a binding calibrator.
func FitCalibratorFromEvaluation(
	report BindingEvaluationReport,
	cases []BindingEvaluationCase,
	config binding.FitConfig,
) (*binding.Calibrator, error) {
	if err := report.ValidateAgainst(cases); err != nil {
		return nil, fmt.Errorf("%w: evaluation replay: %v", binding.ErrInvalidCalibration, err)
	}
	return binding.FitCalibrator(report.Calibration, config)
}
