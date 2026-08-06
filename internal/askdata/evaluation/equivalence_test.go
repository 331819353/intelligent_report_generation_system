package evaluation

import (
	"database/sql/driver"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/ir"
	"intelligent-report-generation-system/internal/askdata/testfixture"
)

type decimalDriverValue string

func (value decimalDriverValue) Value() (driver.Value, error) { return string(value), nil }

func TestCompareNormalizesSyntheticFixtureColumnRowAndDecimalOrder(t *testing.T) {
	t.Parallel()
	fixture := testfixture.Standard()
	if err := fixture.Validate(); err != nil {
		t.Fatalf("fixture: %v", err)
	}
	expectedIR, expectedResult := directFixtureForTest(t, fixture)
	schema := ResultSchema{Columns: []Column{
		{Name: "net_sales", Type: ScalarDecimal},
		{Name: "stat_month", Type: ScalarString, Key: true},
	}}
	expected := ResultSet{
		Columns: append([]string(nil), expectedResult.Columns...),
		Rows:    stringRowsToAny(expectedResult.Rows),
	}
	actual := ResultSet{
		Columns: []string{"net_sales", "stat_month"},
		Rows: [][]any{
			{"980.0", "2026-02"},
			{json.Number("1.200e3"), "2026-01"},
		},
	}
	report, err := Compare(ComparisonRequest{
		Schema:         schema,
		Expected:       Artifact{IR: expectedIR, Result: expected},
		Actual:         Artifact{IR: ir.Normalize(expectedIR), Result: actual},
		FloatTolerance: DefaultFloatTolerance(),
	})
	if err != nil {
		t.Fatalf("compare: %v", err)
	}
	if !report.Equivalent || !report.IREquivalent || !report.ResultEquivalent ||
		!report.IRHashMatch || !report.ExactResultHashMatch || len(report.Differences) != 0 {
		t.Fatalf("unexpected report: %+v", report)
	}
	if report.ExpectedResultHash.Validate() != nil || report.ExpectedResultHash != report.ActualResultHash {
		t.Fatalf("result hashes were not normalized: %+v", report)
	}
	payload, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(payload), `"rows"`) || strings.Contains(string(payload), "stat_month") || strings.Contains(string(payload), "2026-01") {
		t.Fatalf("comparison report leaked result rows: %s", payload)
	}
}

func TestNormalizeResultUsesExactDecimalAndStableHashes(t *testing.T) {
	t.Parallel()
	schema := ResultSchema{Columns: []Column{{Name: "amount", Type: ScalarDecimal}, {Name: "id", Type: ScalarInteger, Key: true}}}
	left, err := NormalizeResult(schema, ResultSet{
		Columns: []string{"amount", "id"},
		Rows:    [][]any{{"12345678901234567890.0100", "0002"}, {"12.300", 1}},
	})
	if err != nil {
		t.Fatal(err)
	}
	right, err := NormalizeResult(schema, ResultSet{
		Columns: []string{"id", "amount"},
		Rows:    [][]any{{json.Number("1"), json.Number("1.23e1")}, {2, json.Number("1.234567890123456789001e19")}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if left.ContentHash != right.ContentHash || !reflect.DeepEqual(left, right) {
		t.Fatalf("exact decimal normalization differs:\nleft=%+v\nright=%+v", left, right)
	}
	if got := left.Rows[0][0].Value; got != "123/10" {
		t.Fatalf("canonical decimal = %q, want 123/10", got)
	}
}

func TestCompareAppliesFloatToleranceButStillReportsExactHashDifference(t *testing.T) {
	t.Parallel()
	semanticIR := validIRForTest(t)
	schema := ResultSchema{Columns: []Column{{Name: "id", Type: ScalarString, Key: true}, {Name: "ratio", Type: ScalarFloat}}}
	expected := Artifact{IR: semanticIR, Result: ResultSet{Columns: []string{"id", "ratio"}, Rows: [][]any{{"a", 100.0}}}}
	actual := Artifact{IR: semanticIR, Result: ResultSet{Columns: []string{"ratio", "id"}, Rows: [][]any{{100.00005, "a"}}}}
	report, err := Compare(ComparisonRequest{
		Schema: schema, Expected: expected, Actual: actual,
		FloatTolerance: FloatTolerance{Absolute: 0.0001, Relative: 0},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !report.Equivalent || !report.ResultEquivalent || report.ExactResultHashMatch {
		t.Fatalf("tolerated float result = %+v", report)
	}

	actual.Result.Rows[0][0] = 100.01
	report, err = Compare(ComparisonRequest{
		Schema: schema, Expected: expected, Actual: actual,
		FloatTolerance: FloatTolerance{Absolute: 0.0001, Relative: 0},
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.Equivalent || report.ResultEquivalent || !hasDifference(report.Differences, "RESULT_VALUE_MISMATCH", "result.rows[0].ratio") {
		t.Fatalf("out-of-tolerance float result = %+v", report)
	}
}

func TestNormalizeResultCanonicalizesDateTimeTimezoneDateAndNull(t *testing.T) {
	t.Parallel()
	schema := ResultSchema{Columns: []Column{
		{Name: "day", Type: ScalarDate, Key: true},
		{Name: "event_at", Type: ScalarDateTime, Timezone: "Asia/Shanghai"},
		{Name: "note", Type: ScalarString},
	}}
	expected, err := NormalizeResult(schema, ResultSet{
		Columns: []string{"day", "event_at", "note"},
		Rows:    [][]any{{time.Date(2026, 8, 6, 0, 0, 0, 0, time.UTC), "2026-08-05T16:00:00Z", nil}},
	})
	if err != nil {
		t.Fatal(err)
	}
	actual, err := NormalizeResult(schema, ResultSet{
		Columns: []string{"note", "event_at", "day"},
		Rows:    [][]any{{nil, "2026-08-06 00:00:00", "2026-08-06"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if expected.ContentHash != actual.ContentHash || !reflect.DeepEqual(expected, actual) {
		t.Fatalf("timezone/date/null normalization differs:\nexpected=%+v\nactual=%+v", expected, actual)
	}

	nullResult, err := NormalizeResult(ResultSchema{Columns: []Column{{Name: "value", Type: ScalarString}}}, ResultSet{
		Columns: []string{"value"}, Rows: [][]any{{nil}},
	})
	if err != nil {
		t.Fatal(err)
	}
	textResult, err := NormalizeResult(ResultSchema{Columns: []Column{{Name: "value", Type: ScalarString}}}, ResultSet{
		Columns: []string{"value"}, Rows: [][]any{{"NULL"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if nullResult.ContentHash == textResult.ContentHash || !nullResult.Rows[0][0].Null || textResult.Rows[0][0].Null {
		t.Fatal("NULL was conflated with the string NULL")
	}
}

func TestNormalizeResultCanonicalizesRemainingScalarAndDriverTypes(t *testing.T) {
	t.Parallel()
	result, err := NormalizeResult(ResultSchema{Columns: []Column{
		{Name: "text_value", Type: ScalarString},
		{Name: "integer_value", Type: ScalarInteger},
		{Name: "decimal_value", Type: ScalarDecimal},
		{Name: "float_value", Type: ScalarFloat},
		{Name: "boolean_value", Type: ScalarBoolean},
	}}, ResultSet{
		Columns: []string{"boolean_value", "float_value", "decimal_value", "integer_value", "text_value"},
		Rows:    [][]any{{[]byte("TRUE"), json.Number("1.50"), decimalDriverValue("12.300"), uint16(42), []byte("hello")}},
	})
	if err != nil {
		t.Fatal(err)
	}
	values := map[string]string{}
	for index, column := range result.Columns {
		values[column.Name] = result.Rows[0][index].Value
	}
	want := map[string]string{
		"boolean_value": "true", "float_value": "1.5", "decimal_value": "123/10",
		"integer_value": "42", "text_value": "hello",
	}
	if !reflect.DeepEqual(values, want) {
		t.Fatalf("normalized values = %+v, want %+v", values, want)
	}
}

func TestNormalizeResultRejectsDuplicateKeys(t *testing.T) {
	t.Parallel()
	schema := ResultSchema{Columns: []Column{{Name: "region", Type: ScalarString, Key: true}, {Name: "amount", Type: ScalarDecimal}}}
	_, err := NormalizeResult(schema, ResultSet{
		Columns: []string{"region", "amount"}, Rows: [][]any{{"east", "1.0"}, {"east", "2.0"}},
	})
	if !errors.Is(err, ErrDuplicateResultKey) {
		t.Fatalf("error = %v, want duplicate key", err)
	}
}

func TestCompareRequiresIRAndResultEquivalence(t *testing.T) {
	t.Parallel()
	expectedIR := validIRForTest(t)
	actualIR := ir.Normalize(expectedIR)
	actualIR.Limit--
	result := ResultSet{Columns: []string{"amount"}, Rows: [][]any{{"10.00"}}}
	report, err := Compare(ComparisonRequest{
		Schema:   ResultSchema{Columns: []Column{{Name: "amount", Type: ScalarDecimal}}},
		Expected: Artifact{IR: expectedIR, Result: result}, Actual: Artifact{IR: actualIR, Result: result},
		FloatTolerance: DefaultFloatTolerance(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.Equivalent || report.IREquivalent || !report.ResultEquivalent || report.IRHashMatch ||
		!report.ExactResultHashMatch || !hasDifference(report.Differences, "IR_MISMATCH", "ir.limit") {
		t.Fatalf("unexpected IR mismatch report: %+v", report)
	}
}

func TestCompareUsesRelativeToleranceAndReportsRowCountMismatch(t *testing.T) {
	t.Parallel()
	semanticIR := validIRForTest(t)
	schema := ResultSchema{Columns: []Column{{Name: "ratio", Type: ScalarFloat}}}
	expected := Artifact{IR: semanticIR, Result: ResultSet{Columns: []string{"ratio"}, Rows: [][]any{{1_000_000_000.0}}}}
	actual := Artifact{IR: semanticIR, Result: ResultSet{Columns: []string{"ratio"}, Rows: [][]any{{1_000_000_000.5}}}}
	report, err := Compare(ComparisonRequest{
		Schema: schema, Expected: expected, Actual: actual,
		FloatTolerance: FloatTolerance{Relative: 1e-9},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !report.Equivalent || report.ExactResultHashMatch {
		t.Fatalf("relative tolerance report = %+v", report)
	}

	actual.Result.Rows = append(actual.Result.Rows, []any{2.0})
	report, err = Compare(ComparisonRequest{
		Schema: schema, Expected: expected, Actual: actual,
		FloatTolerance: FloatTolerance{Relative: 1e-9},
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.Equivalent || !hasDifference(report.Differences, "RESULT_ROW_COUNT_MISMATCH", "result.rows") {
		t.Fatalf("row count report = %+v", report)
	}
}

func TestCompareValidatesDeclaredResultHashes(t *testing.T) {
	t.Parallel()
	semanticIR := validIRForTest(t)
	schema := ResultSchema{Columns: []Column{{Name: "amount", Type: ScalarDecimal}}}
	result := ResultSet{Columns: []string{"amount"}, Rows: [][]any{{"10.00"}}}
	tampered := askdata.HashBytes([]byte("not this result"))
	_, err := Compare(ComparisonRequest{
		Schema:         schema,
		Expected:       Artifact{IR: semanticIR, Result: result, DeclaredResultHash: &tampered},
		Actual:         Artifact{IR: semanticIR, Result: result},
		FloatTolerance: DefaultFloatTolerance(),
	})
	if err == nil || !strings.Contains(err.Error(), "declared result hash") {
		t.Fatalf("error = %v, want declared hash rejection", err)
	}
}

func TestNormalizedResultRejectsTampering(t *testing.T) {
	t.Parallel()
	result, err := NormalizeResult(ResultSchema{Columns: []Column{{Name: "id", Type: ScalarInteger, Key: true}}}, ResultSet{
		Columns: []string{"id"}, Rows: [][]any{{2}, {1}},
	})
	if err != nil {
		t.Fatal(err)
	}
	tampered := result
	tampered.Rows = append([][]NormalizedValue(nil), result.Rows...)
	tampered.Rows[0] = append([]NormalizedValue(nil), result.Rows[0]...)
	tampered.Rows[0][0].Value = "3"
	if err := tampered.Validate(); err == nil {
		t.Fatal("expected content hash tampering rejection")
	}

	unsorted := result
	unsorted.Rows = [][]NormalizedValue{result.Rows[1], result.Rows[0]}
	payload, err := normalizedResultPayload(unsorted)
	if err != nil {
		t.Fatal(err)
	}
	unsorted.ContentHash = askdata.HashBytes(payload)
	if err := unsorted.Validate(); err == nil || !strings.Contains(err.Error(), "not sorted") {
		t.Fatalf("error = %v, want row order rejection", err)
	}

	negativeZero, err := NormalizeResult(ResultSchema{Columns: []Column{{Name: "value", Type: ScalarFloat}}}, ResultSet{
		Columns: []string{"value"}, Rows: [][]any{{0.0}},
	})
	if err != nil {
		t.Fatal(err)
	}
	negativeZero.Rows[0][0].Value = "-0"
	payload, err = normalizedResultPayload(negativeZero)
	if err != nil {
		t.Fatal(err)
	}
	negativeZero.ContentHash = askdata.HashBytes(payload)
	if err := negativeZero.Validate(); err == nil || !strings.Contains(err.Error(), "float is not canonical") {
		t.Fatalf("error = %v, want negative zero rejection", err)
	}
}

func TestNormalizeResultFailsClosedOnUnsafeTypesAndSchema(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		schema ResultSchema
		result ResultSet
	}{
		{
			name: "binary float decimal", schema: ResultSchema{Columns: []Column{{Name: "value", Type: ScalarDecimal}}},
			result: ResultSet{Columns: []string{"value"}, Rows: [][]any{{1.25}}},
		},
		{
			name: "non finite float", schema: ResultSchema{Columns: []Column{{Name: "value", Type: ScalarFloat}}},
			result: ResultSet{Columns: []string{"value"}, Rows: [][]any{{math.NaN()}}},
		},
		{
			name: "missing timezone", schema: ResultSchema{Columns: []Column{{Name: "value", Type: ScalarDateTime}}},
			result: ResultSet{Columns: []string{"value"}, Rows: [][]any{{"2026-08-06 00:00:00"}}},
		},
		{
			name: "float key", schema: ResultSchema{Columns: []Column{{Name: "value", Type: ScalarFloat, Key: true}}},
			result: ResultSet{Columns: []string{"value"}, Rows: [][]any{{1.0}}},
		},
		{
			name: "duplicate column", schema: ResultSchema{Columns: []Column{{Name: "value", Type: ScalarString}}},
			result: ResultSet{Columns: []string{"value", "value"}, Rows: [][]any{}},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := NormalizeResult(test.schema, test.result); err == nil {
				t.Fatal("expected normalization rejection")
			}
		})
	}
}

func TestFloatToleranceRejectsUnsafeBounds(t *testing.T) {
	t.Parallel()
	for _, tolerance := range []FloatTolerance{
		{Absolute: -1}, {Absolute: math.Inf(1)}, {Relative: -1}, {Relative: 1.01}, {Relative: math.NaN()},
	} {
		if err := tolerance.Validate(); err == nil {
			t.Fatalf("expected tolerance rejection: %+v", tolerance)
		}
	}
}

func directFixtureForTest(t *testing.T, fixture testfixture.Set) (ir.SemanticIR, testfixture.Result) {
	t.Helper()
	var expectedIR *ir.SemanticIR
	for _, question := range fixture.Questions {
		if question.QuestionID == "question-direct" {
			expectedIR = question.ExpectedIR
			break
		}
	}
	if expectedIR == nil {
		t.Fatal("direct fixture IR not found")
	}
	for _, result := range fixture.Results {
		if result.QuestionID == "question-direct" {
			return ir.Normalize(*expectedIR), result
		}
	}
	t.Fatal("direct fixture result not found")
	return ir.SemanticIR{}, testfixture.Result{}
}

func validIRForTest(t *testing.T) ir.SemanticIR {
	t.Helper()
	fixture := testfixture.Standard()
	if err := fixture.Validate(); err != nil {
		t.Fatal(err)
	}
	semanticIR, _ := directFixtureForTest(t, fixture)
	return semanticIR
}

func stringRowsToAny(rows [][]string) [][]any {
	result := make([][]any, len(rows))
	for rowIndex, row := range rows {
		result[rowIndex] = make([]any, len(row))
		for columnIndex, value := range row {
			result[rowIndex][columnIndex] = value
		}
	}
	return result
}

func hasDifference(values []Difference, code, path string) bool {
	for _, value := range values {
		if value.Code == code && value.Path == path {
			return true
		}
	}
	return false
}
