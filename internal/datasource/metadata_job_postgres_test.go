package datasource

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

type metadataJobFailureRow struct {
	values []string
	err    error
}

func (r metadataJobFailureRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	for index, value := range r.values {
		*dest[index].(*string) = value
	}
	return nil
}

func TestScanMetadataJobFailureMapsPublicSummary(t *testing.T) {
	failure, err := scanMetadataJobFailure(metadataJobFailureRow{values: []string{
		"catalog", "sales", "orders", "LLM_COMPLETION_FAILED", "LLM 表结构完善失败",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if failure.CatalogName != "catalog" || failure.SchemaName != "sales" || failure.TableName != "orders" ||
		failure.ErrorCode != "LLM_COMPLETION_FAILED" || failure.ErrorMessage != "LLM 表结构完善失败" {
		t.Fatalf("failure=%#v", failure)
	}
	payload, err := json.Marshal(failure)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(payload), "stage") || strings.Contains(string(payload), "rawOutput") {
		t.Fatalf("failure response exposed internal processing details: %s", payload)
	}
	jobPayload, err := json.Marshal(MetadataJob{Failures: []MetadataJobFailure{failure}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(jobPayload), `"failures"`) {
		t.Fatalf("job response omitted failure summaries: %s", jobPayload)
	}
}

func TestMetadataJobFailuresAreOptionalAndScanErrorsPropagate(t *testing.T) {
	payload, err := json.Marshal(MetadataJob{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(payload), "failures") {
		t.Fatalf("empty failures should be omitted: %s", payload)
	}
	emptyFailurePayload, err := json.Marshal(MetadataJobFailure{TableName: "orders"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(emptyFailurePayload), "errorCode") || strings.Contains(string(emptyFailurePayload), "errorMessage") {
		t.Fatalf("empty failure details should be omitted: %s", emptyFailurePayload)
	}
	want := errors.New("scan failed")
	if _, err := scanMetadataJobFailure(metadataJobFailureRow{err: want}); !errors.Is(err, want) {
		t.Fatalf("scan error=%v", err)
	}
}
