package reportai

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/report/blueprint"
	"intelligent-report-generation-system/internal/report/cardkind"
	"intelligent-report-generation-system/internal/report/insight"
	"intelligent-report-generation-system/internal/report/template"
)

// BuildBlueprintCatalog assigns stable short references to actor-visible data
// contexts and fields. requested controls dataset order and is never trusted as
// authority: every ID must still be present in candidates.
func BuildBlueprintCatalog(candidates []DataContextCandidate, requested []askdata.ID) (blueprint.Catalog, error) {
	if len(candidates) == 0 || len(candidates) > 30 {
		return blueprint.Catalog{}, errors.New("blueprint data catalog requires 1..30 candidates")
	}
	selected := make([]DataContextCandidate, 0, len(requested))
	if len(requested) == 0 {
		selected = append(selected, candidates...)
	} else {
		seen := map[askdata.ID]bool{}
		for index, id := range requested {
			if seen[id] {
				return blueprint.Catalog{}, fmt.Errorf("requested[%d] is duplicated", index)
			}
			seen[id] = true
			match := -1
			for candidateIndex := range candidates {
				if candidates[candidateIndex].DataContext.ID == id {
					match = candidateIndex
					break
				}
			}
			if match < 0 {
				return blueprint.Catalog{}, fmt.Errorf("requested[%d] is not actor-visible", index)
			}
			selected = append(selected, candidates[match])
		}
	}
	result := blueprint.Catalog{Datasets: make([]blueprint.DatasetCatalog, 0, len(selected))}
	for datasetIndex, candidate := range selected {
		if candidate.DataContext.ID.Validate() != nil || strings.TrimSpace(candidate.Name) == "" || len(candidate.FieldDefinitions) == 0 {
			return blueprint.Catalog{}, fmt.Errorf("candidate %d is incomplete", datasetIndex)
		}
		dataset := blueprint.DatasetCatalog{
			Ref: fmt.Sprintf("d%d", datasetIndex+1), Alias: candidate.DataContext.Alias,
			DataContext: candidate.DataContext, Metrics: []blueprint.Field{}, Dimensions: []blueprint.Field{},
		}
		if strings.TrimSpace(dataset.Alias) == "" {
			dataset.Alias = candidate.Name
		}
		definitions := append([]FieldDefinition(nil), candidate.FieldDefinitions...)
		sort.SliceStable(definitions, func(i, j int) bool { return definitions[i].Code < definitions[j].Code })
		for _, field := range definitions {
			label := strings.TrimSpace(field.Name)
			if label == "" {
				label = field.Code
			}
			if strings.EqualFold(field.Role, "MEASURE") {
				dataset.Metrics = append(dataset.Metrics, blueprint.Field{Ref: fmt.Sprintf("m%d", len(dataset.Metrics)+1), Field: field.Code, Label: label})
			} else {
				dataset.Dimensions = append(dataset.Dimensions, blueprint.Field{Ref: fmt.Sprintf("x%d", len(dataset.Dimensions)+1), Field: field.Code, Label: label})
			}
		}
		result.Datasets = append(result.Datasets, dataset)
	}
	return result, nil
}

func BuildBlueprintContext(intent string, catalog blueprint.Catalog, kinds *cardkind.Registry, methods *insight.Registry) (blueprint.ContextPack, error) {
	intent = strings.TrimSpace(intent)
	if intent == "" || len([]rune(intent)) > 4096 || len(catalog.Datasets) == 0 || kinds == nil || methods == nil {
		return blueprint.ContextPack{}, errors.New("blueprint context request is invalid")
	}
	methodNames := make([]string, 0, len(methods.List()))
	for _, method := range methods.List() {
		methodNames = append(methodNames, string(method.ID()))
	}
	sort.Strings(methodNames)
	return blueprint.ContextPack{
		SchemaVersion: "report-context-pack/1.0", Intent: intent, Datasets: catalog.Datasets,
		Kinds: kinds.List(), Methods: methodNames, Rules: blueprint.Rules{MaxSections: 30, MaxCards: 300},
	}, nil
}

func GenerateBlueprint(ctx context.Context, generator blueprint.Generator, request blueprint.Request, catalog blueprint.Catalog,
	kinds *cardkind.Registry, components *template.Registry, methods *insight.Registry,
) (blueprint.Blueprint, error) {
	if generator == nil || strings.TrimSpace(request.Intent) == "" || request.Intent != request.Context.Intent || request.Context.SchemaVersion != "report-context-pack/1.0" ||
		(request.ReportType != "REPORT" && request.ReportType != "DASHBOARD") {
		return blueprint.Blueprint{}, errors.New("report blueprint request is invalid")
	}
	result, err := generator.GenerateReportBlueprint(ctx, request)
	if err != nil {
		return blueprint.Blueprint{}, err
	}
	if result.ReportType != request.ReportType {
		return blueprint.Blueprint{}, errors.New("report blueprint changed the requested report type")
	}
	if err := result.Validate(catalog, kinds, components, methods); err != nil {
		return blueprint.Blueprint{}, fmt.Errorf("report blueprint was rejected: %w", err)
	}
	return result, nil
}

// RequestedDatasetRefs returns the catalog dataset IDs actually declared by a
// validated blueprint. It is useful for audit summaries without exposing UUIDs
// to the model contract.
func RequestedDatasetRefs(value blueprint.Blueprint) []string {
	refs := make([]string, 0, len(value.Datasets))
	for _, dataset := range value.Datasets {
		if !slices.Contains(refs, dataset.Ref) {
			refs = append(refs, dataset.Ref)
		}
	}
	return refs
}
