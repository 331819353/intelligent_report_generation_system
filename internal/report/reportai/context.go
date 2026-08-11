package reportai

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/report"
	"intelligent-report-generation-system/internal/report/store"
)

// DataContextCandidate is a governed, actor-visible semantic asset that the
// report model may consider. Raw rows and SQL never cross this boundary.
type DataContextCandidate struct {
	DataContext report.DataContext `json:"dataContext"`
	Name        string             `json:"name"`
	Description string             `json:"description"`
	Fields      []string           `json:"fields"`
}

// DataContextCatalog discovers only semantic assets the current actor can
// read. Implementations must enforce tenant, domain and column policy.
type DataContextCatalog interface {
	Candidates(context.Context, store.Identity, int) ([]DataContextCandidate, error)
}

type DataContextSelectionRequest struct {
	Intent     string                 `json:"intent"`
	Candidates []DataContextCandidate `json:"candidates"`
}

type DataContextSelection struct {
	DataContextID askdata.ID `json:"dataContextId"`
	ReportName    string     `json:"reportName"`
	Rationale     string     `json:"rationale"`
	Confidence    string     `json:"confidence"`
}

type DataContextSelector interface {
	SelectDataContext(context.Context, DataContextSelectionRequest) (DataContextSelection, error)
}

func SelectDataContext(ctx context.Context, selector DataContextSelector, request DataContextSelectionRequest) (DataContextSelection, error) {
	request.Intent = strings.TrimSpace(request.Intent)
	if selector == nil || request.Intent == "" || len(request.Candidates) == 0 || len(request.Candidates) > 30 {
		return DataContextSelection{}, errors.New("report data context selection request is invalid")
	}
	allowed := make([]askdata.ID, 0, len(request.Candidates))
	for index, candidate := range request.Candidates {
		if candidate.DataContext.ID.Validate() != nil || candidate.DataContext.DatasetID.Validate() != nil ||
			candidate.DataContext.DatasetVersionID.Validate() != nil || strings.TrimSpace(candidate.Name) == "" || len(candidate.Fields) == 0 {
			return DataContextSelection{}, fmt.Errorf("candidates[%d] is invalid", index)
		}
		allowed = append(allowed, candidate.DataContext.ID)
	}
	selection, err := selector.SelectDataContext(ctx, request)
	if err != nil {
		return DataContextSelection{}, err
	}
	selection.ReportName = strings.TrimSpace(selection.ReportName)
	selection.Rationale = strings.TrimSpace(selection.Rationale)
	selection.Confidence = strings.ToUpper(strings.TrimSpace(selection.Confidence))
	if !slices.Contains(allowed, selection.DataContextID) || selection.ReportName == "" || selection.Rationale == "" ||
		(selection.Confidence != "HIGH" && selection.Confidence != "MEDIUM" && selection.Confidence != "LOW") {
		return DataContextSelection{}, errors.New("report data context selection was rejected")
	}
	return selection, nil
}
