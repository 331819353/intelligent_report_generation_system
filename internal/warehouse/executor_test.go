package warehouse

import (
	"errors"
	"testing"

	"intelligent-report-generation-system/internal/dataset"
)

func TestValidateOutputContractRequiresTypedBusinessTimeWatermark(t *testing.T) {
	for _, canonicalType := range []string{"DATE", "DATETIME"} {
		document := dataset.Document{
			Fields:      []dataset.Field{{Code: "business_date", CanonicalType: canonicalType}},
			OutputGrain: dataset.OutputGrain{TimeField: "business_date"},
		}
		if err := validateOutputContract(document, nil); err != nil {
			t.Fatalf("%s time field: %v", canonicalType, err)
		}
	}

	document := dataset.Document{
		Fields:      []dataset.Field{{Code: "business_date", CanonicalType: "STRING"}},
		OutputGrain: dataset.OutputGrain{TimeField: "business_date"},
	}
	if err := validateOutputContract(document, nil); !errors.Is(err, ErrInvalidBuild) {
		t.Fatalf("STRING time field error = %v", err)
	}
}
