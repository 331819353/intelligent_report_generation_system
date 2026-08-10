package binding

import (
	"errors"

	"intelligent-report-generation-system/internal/askdata"
)

func validateSelectedGraphDomain(scope askdata.PolicyScope, domainID askdata.ID) error {
	if scope.Validate() != nil || domainID.Validate() != nil ||
		len(scope.DomainIDs) != 1 || scope.DomainIDs[0] != domainID {
		return errors.New("graph domain is not the selected session domain")
	}
	return nil
}
