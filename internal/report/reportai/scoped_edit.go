package reportai

import (
	"context"
	"encoding/json"
	"errors"
	"slices"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/report"
	"intelligent-report-generation-system/internal/report/compiler"
	"intelligent-report-generation-system/internal/report/operation"
	"intelligent-report-generation-system/internal/report/template"
)

type ScopedContext struct {
	Selection         json.RawMessage     `json:"selection"`
	AllowedOperations []operation.Type    `json:"allowedOperations"`
	Manifests         []template.Manifest `json:"manifests"`
	AllowedFields     []string            `json:"allowedFields"`
	BaseRevision      int64               `json:"baseRevision"`
	AIRunID           askdata.ID          `json:"aiRunId"`
	Intent            string              `json:"intent"`
}

type ScopedEditGenerator interface {
	GenerateScopedOperations(context.Context, ScopedContext) (operation.Bundle, error)
}

type Preview struct {
	BeforeHash         string           `json:"beforeHash"`
	AfterHash          string           `json:"afterHash"`
	AffectedComponents []askdata.ID     `json:"affectedComponents"`
	Bundle             operation.Bundle `json:"bundle"`
}

func BuildScopedContext(definition report.ReportDefinition, scope operation.Scope, revision int64, intent string, fields []string, registry *template.Registry, runIDs ...askdata.ID) (ScopedContext, error) {
	selection, componentTypes, err := selectSubtree(definition, scope)
	if err != nil {
		return ScopedContext{}, err
	}
	manifests := []template.Manifest{}
	for _, manifest := range registry.List() {
		if _, wanted := componentTypes[manifest.Type]; wanted {
			manifests = append(manifests, manifest)
		}
	}
	runID := askdata.ID("")
	if len(runIDs) != 0 {
		runID = runIDs[0]
	}
	return ScopedContext{Selection: selection, AllowedOperations: allowedScopedOperations(), Manifests: manifests,
		AllowedFields: append([]string(nil), fields...), BaseRevision: revision, AIRunID: runID, Intent: intent}, nil
}

func PreviewScopedEdit(ctx context.Context, generator ScopedEditGenerator, definition report.ReportDefinition, reportID askdata.ID, scope operation.Scope, revision int64, intent string, fields []string, registry *template.Registry, runIDs ...askdata.ID) (Preview, error) {
	if generator == nil || registry == nil {
		return Preview{}, errors.New("scoped edit generator is not configured")
	}
	bounded, err := BuildScopedContext(definition, scope, revision, intent, fields, registry, runIDs...)
	if err != nil {
		return Preview{}, err
	}
	bundle, err := generator.GenerateScopedOperations(ctx, bounded)
	if err != nil {
		return Preview{}, err
	}
	return PreviewBundle(definition, reportID, revision, bundle, runIDs...)
}

// PreviewBundle validates a generated bundle and computes its deterministic
// diff without mutating a draft. It is split from generation so an HTTP/job
// boundary can audit the exact rejected operations as well as successful ones.
func PreviewBundle(definition report.ReportDefinition, reportID askdata.ID, revision int64, bundle operation.Bundle, runIDs ...askdata.ID) (Preview, error) {
	if bundle.Source != operation.SourceAI || bundle.ReportID != reportID || bundle.BaseRevision != revision || bundle.Scope == nil {
		return Preview{}, errors.New("AI output is not a scoped operation bundle")
	}
	if len(runIDs) != 0 && (bundle.AIRunID == nil || *bundle.AIRunID != runIDs[0]) {
		return Preview{}, errors.New("AI output does not belong to the audited run")
	}
	if err := bundle.Validate(); err != nil {
		return Preview{}, err
	}
	if err := operation.GuardAI(bundle, &definition); err != nil {
		return Preview{}, err
	}
	_, beforeHash, err := normalizeHash(definition)
	if err != nil {
		return Preview{}, err
	}
	updated, _, afterHash, err := operation.ApplyAndValidate(definition, bundle.Operations)
	if err != nil {
		return Preview{}, err
	}
	return Preview{BeforeHash: beforeHash, AfterHash: afterHash, AffectedComponents: affectedComponents(definition, updated), Bundle: bundle}, nil
}

func selectSubtree(definition report.ReportDefinition, scope operation.Scope) ([]byte, map[string]struct{}, error) {
	for _, page := range definition.Pages {
		if scope.PageID == nil || page.ID != *scope.PageID {
			continue
		}
		selected := page
		if scope.SectionID != nil {
			selected.Sections = nil
			for _, section := range page.Sections {
				if section.ID == *scope.SectionID {
					if scope.BlockID != nil {
						blocks := section.Blocks
						section.Blocks = nil
						for _, block := range blocks {
							if block.ID == *scope.BlockID {
								section.Blocks = append(section.Blocks, block)
							}
						}
					}
					selected.Sections = append(selected.Sections, section)
				}
			}
		}
		if len(selected.Sections) == 0 {
			return nil, nil, errors.New("scope does not exist")
		}
		componentIDs := map[askdata.ID]struct{}{}
		for _, section := range selected.Sections {
			for _, block := range section.Blocks {
				for _, zone := range block.Zones {
					for _, slot := range zone.Slots {
						if slot.ComponentID != "" {
							componentIDs[slot.ComponentID] = struct{}{}
						}
					}
				}
			}
		}
		types := map[string]struct{}{}
		components := []report.Component{}
		for _, component := range definition.Components {
			if _, exists := componentIDs[component.ID]; exists {
				components = append(components, component)
				types[component.TemplateRef.Type] = struct{}{}
			}
		}
		raw, err := json.Marshal(struct {
			Page       report.Page        `json:"page"`
			Components []report.Component `json:"components"`
		}{selected, components})
		return raw, types, err
	}
	return nil, nil, errors.New("scope page does not exist")
}

func allowedScopedOperations() []operation.Type {
	result := []operation.Type{}
	for _, candidate := range operation.Types() {
		if candidate != operation.TemplateApply && candidate != operation.PageDelete && candidate != operation.SectionDelete {
			result = append(result, candidate)
		}
	}
	return result
}

func normalizeHash(definition report.ReportDefinition) ([]byte, string, error) {
	return compiler.Normalize(definition)
}

func affectedComponents(before, after report.ReportDefinition) []askdata.ID {
	left := map[askdata.ID]string{}
	for _, component := range before.Components {
		raw, _ := json.Marshal(component)
		left[component.ID] = string(raw)
	}
	result := []askdata.ID{}
	for _, component := range after.Components {
		raw, _ := json.Marshal(component)
		if left[component.ID] != string(raw) {
			result = append(result, component.ID)
		}
		delete(left, component.ID)
	}
	for id := range left {
		result = append(result, id)
	}
	slices.Sort(result)
	return result
}
