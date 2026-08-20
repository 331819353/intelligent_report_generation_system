package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/queryruntime"
	"intelligent-report-generation-system/internal/report"
	"intelligent-report-generation-system/internal/report/store"
)

const MaxFilterOptionValues = 100

// FilterOptionRequest identifies one governed logical field. Dataset and
// version IDs are resolved by the HTTP boundary from the current actor's
// catalog; the browser never supplies them directly.
type FilterOptionRequest struct {
	DatasetID        askdata.ID
	DatasetVersionID askdata.ID
	DataContextID    askdata.ID
	Field            string
	Limit            int
}

type FilterOptionResult struct {
	Values      []string `json:"values"`
	Truncated   bool     `json:"truncated"`
	ScannedRows int      `json:"scannedRows"`
}

type FilterOptionLoader interface {
	LoadFilterOptions(context.Context, FilterOptionRequest) (FilterOptionResult, error)
}

// LoadFilterOptions reads the immutable dataset version through the same
// governed query service used by report cards, then returns stable distinct
// values for one allowed field. Current viewer row and column policies are
// reapplied by queryruntime on every call.
func (runner *DatasetVersionRunner) LoadFilterOptions(ctx context.Context, request FilterOptionRequest) (FilterOptionResult, error) {
	identity, ok := ctx.Value(viewerIdentityKey{}).(store.Identity)
	field := strings.TrimSpace(request.Field)
	if runner == nil || runner.service == nil || !ok || identity.Validate() != nil ||
		request.DatasetID.Validate() != nil || request.DatasetVersionID.Validate() != nil ||
		request.DataContextID.Validate() != nil || field == "" ||
		request.Limit < 1 || request.Limit > MaxFilterOptionValues {
		return FilterOptionResult{}, errors.New("report filter option request is invalid")
	}

	preview, err := runner.service.PreviewVersionQuery(
		ctx, string(identity.TenantID), string(identity.ActorID), string(request.DatasetID),
		string(request.DatasetVersionID), queryruntime.VersionQueryInput{
			Fields: []string{field}, MaxRows: request.Limit, Distinct: true,
		},
	)
	if err != nil {
		return FilterOptionResult{}, fmt.Errorf("load governed filter option values: %w", err)
	}
	return distinctFilterOptions(preview.Columns, preview.Rows, field, request.Limit, len(preview.Rows) == request.Limit)
}

func distinctFilterOptions(columns []string, rows [][]any, field string, limit int, sourceTruncated bool) (FilterOptionResult, error) {
	columnIndex := -1
	for index, column := range columns {
		if column == field {
			columnIndex = index
			break
		}
	}
	if columnIndex < 0 {
		return FilterOptionResult{}, errors.New("governed dataset result is missing the filter field")
	}
	seen := make(map[string]struct{}, min(len(rows), limit+1))
	values := make([]string, 0, min(len(rows), limit+1))
	for _, row := range rows {
		if columnIndex >= len(row) {
			return FilterOptionResult{}, errors.New("governed dataset result contains an invalid row")
		}
		value, valid := filterOptionString(row[columnIndex])
		if !valid || len(value) > report.MaxStringLength {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		values = append(values, value)
	}
	sort.Strings(values)
	truncated := len(values) > limit || sourceTruncated && len(values) >= limit
	if len(values) > limit {
		values = values[:limit]
	}
	return FilterOptionResult{Values: values, Truncated: truncated, ScannedRows: len(rows)}, nil
}

func filterOptionString(value any) (string, bool) {
	var formatted string
	switch typed := value.(type) {
	case nil:
		return "", false
	case string:
		formatted = typed
	case []byte:
		formatted = string(typed)
	case time.Time:
		formatted = typed.Format(time.RFC3339Nano)
	case json.Number:
		formatted = typed.String()
	case bool:
		formatted = strconv.FormatBool(typed)
	case int:
		formatted = strconv.Itoa(typed)
	case int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, float32, float64:
		formatted = fmt.Sprint(typed)
	default:
		encoded, err := json.Marshal(typed)
		if err != nil {
			return "", false
		}
		formatted = string(encoded)
	}
	if strings.TrimSpace(formatted) == "" {
		return "", false
	}
	return formatted, true
}

var _ FilterOptionLoader = (*DatasetVersionRunner)(nil)
