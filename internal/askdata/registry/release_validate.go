package registry

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

type ReleaseAdditivityIssue struct {
	ObjectType      ReleaseObjectType `json:"objectType"`
	ObjectVersionID string            `json:"objectVersionId"`
	Code            string            `json:"code"`
}

type ReleaseAdditivityError struct {
	Issues []ReleaseAdditivityIssue `json:"issues"`
}

func (failure *ReleaseAdditivityError) Error() string {
	parts := make([]string, 0, len(failure.Issues))
	for _, issue := range failure.Issues {
		parts = append(parts, fmt.Sprintf("%s:%s:%s", issue.ObjectType, issue.ObjectVersionID, issue.Code))
	}
	return "release additivity validation failed: " + strings.Join(parts, ",")
}

// ValidateReleaseManifest is the third independent additivity gate. It reads
// only immutable release contracts, so bypassing the Go certification helper
// or a database trigger still cannot admit an unconfirmed metric/measure.
func ValidateReleaseManifest(objects []ReleaseObject) error {
	issues := make([]ReleaseAdditivityIssue, 0)
	for _, object := range objects {
		var err error
		switch object.Type {
		case ReleaseObjectMetric:
			var contract metricContractDocument
			if json.Unmarshal(object.Contract, &contract) != nil {
				err = &AdditivityError{Code: AdditivityMissing, ObjectVersionID: object.ObjectVersionID}
			} else {
				err = validateAdditivityContract(additivityContract{
					id: object.ObjectVersionID, additivity: contract.Additivity,
					semiAdditiveTimeAggregation: contract.SemiAdditiveTimeAggregation,
					aggregationRestriction:      contract.AggregationRestriction,
					unit:                        contract.Unit, currency: contract.Currency,
				})
			}
		case ReleaseObjectMeasure:
			var contract measureContractDocument
			if json.Unmarshal(object.Contract, &contract) != nil {
				err = &AdditivityError{Code: AdditivityMissing, ObjectVersionID: object.ObjectVersionID}
			} else {
				err = validateAdditivityContract(additivityContract{
					id: object.ObjectVersionID, additivity: contract.Additivity,
					semiAdditiveTimeAggregation: contract.SemiAdditiveTimeAggregation,
					aggregationRestriction:      contract.AggregationRestriction,
					unit:                        contract.Unit, currency: contract.Currency,
				})
			}
		default:
			continue
		}
		if err != nil {
			code := AdditivityMissing
			if typed, ok := err.(*AdditivityError); ok {
				code = typed.Code
			}
			issues = append(issues, ReleaseAdditivityIssue{
				ObjectType: object.Type, ObjectVersionID: object.ObjectVersionID, Code: code,
			})
		}
	}
	if len(issues) == 0 {
		return nil
	}
	sort.Slice(issues, func(left, right int) bool {
		if issues[left].ObjectType != issues[right].ObjectType {
			return issues[left].ObjectType < issues[right].ObjectType
		}
		return issues[left].ObjectVersionID < issues[right].ObjectVersionID
	})
	return &ReleaseAdditivityError{Issues: issues}
}
