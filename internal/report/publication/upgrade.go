package publication

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"intelligent-report-generation-system/internal/askdata"
	reportmodel "intelligent-report-generation-system/internal/report"
	"intelligent-report-generation-system/internal/report/compiler"
	"intelligent-report-generation-system/internal/report/operation"
	"intelligent-report-generation-system/internal/report/store"
)

var (
	ErrUpgradeInvalid       = errors.New("report semantic upgrade is invalid")
	ErrUpgradeUnavailable   = errors.New("report semantic upgrade dependency is unavailable")
	ErrUpgradeDraftDiverged = errors.New("report draft diverged from the published version")
	ErrUpgradePreviewStale  = errors.New("report upgrade preview is stale")
)

type UpgradeRepository interface {
	GetDraft(context.Context, store.Identity, askdata.ID) (store.Draft, error)
	GetVersion(context.Context, store.Identity, askdata.ID, *int) (store.Version, error)
	SaveDraftWithRevision(context.Context, store.Identity, askdata.ID, store.SaveInput) (store.Draft, store.Revision, error)
}

type ComponentRecompiler interface {
	RecompileComponent(context.Context, store.Identity, askdata.ID, reportmodel.Component) (RecompiledSemantic, error)
}

type SampleComparator interface {
	CompareUpgrade(context.Context, store.Identity, UpgradeComparison) (SampleImpact, error)
}

type ComponentMigrator interface {
	MigrateComponent(reportmodel.Component, string) (reportmodel.Component, error)
}

type UpgradeSpec struct {
	Kind                   compiler.ChangeKind `json:"kind"`
	OldObjectID            string              `json:"oldObjectId"`
	NewObjectID            string              `json:"newObjectId"`
	NewSemanticReleaseID   askdata.ID          `json:"newSemanticReleaseId,omitempty"`
	NewSemanticContentHash askdata.ContentHash `json:"newSemanticContentHash,omitempty"`
}

type ComponentUpgrade struct {
	ComponentID askdata.ID `json:"componentId"`
	BeforeHash  string     `json:"beforeHash"`
	AfterHash   string     `json:"afterHash"`
}

type RecompiledSemantic struct {
	Reference   reportmodel.SemanticQueryRef
	Compilation SemanticCompilation
}

type UpgradeComparison struct {
	ReportID      askdata.ID
	BaseVersionID askdata.ID
	Before        reportmodel.Component
	After         reportmodel.Component
	Compilation   *SemanticCompilation
}

type SampleImpact struct {
	ComponentID    askdata.ID `json:"componentId"`
	Direction      string     `json:"direction"` // INCREASE|DECREASE|UNCHANGED|UNKNOWN
	RelativeChange string     `json:"relativeChange,omitempty"`
	EvidenceHash   string     `json:"evidenceHash,omitempty"`
}

type UpgradePreview struct {
	ReportID            askdata.ID          `json:"reportId"`
	BaseVersionID       askdata.ID          `json:"baseVersionId"`
	BaseDefinitionHash  string              `json:"baseDefinitionHash"`
	BaseDraftRevision   int64               `json:"baseDraftRevision"`
	BaseDraftHash       string              `json:"baseDraftHash"`
	AfterDefinitionHash string              `json:"afterDefinitionHash"`
	AffectedComponents  []ComponentUpgrade  `json:"affectedComponents"`
	SampleImpacts       []SampleImpact      `json:"sampleImpacts"`
	ConfirmationToken   askdata.ContentHash `json:"confirmationToken"`
	definition          reportmodel.ReportDefinition
	compilations        []SemanticCompilation
}

type UpgradeService struct {
	Repository   UpgradeRepository
	Dependencies DependencyValidator
	Recompiler   ComponentRecompiler
	Comparator   SampleComparator
	Components   ComponentMigrator
	Compilations CompilationStore
}

func (service *UpgradeService) Preview(
	ctx context.Context, identity store.Identity, reportID askdata.ID, spec UpgradeSpec,
) (UpgradePreview, error) {
	if service == nil || service.Repository == nil || identity.Validate() != nil || reportID.Validate() != nil ||
		validateUpgradeSpec(spec) != nil {
		return UpgradePreview{}, ErrUpgradeInvalid
	}
	version, err := service.Repository.GetVersion(ctx, identity, reportID, nil)
	if err != nil {
		return UpgradePreview{}, err
	}
	draft, err := service.Repository.GetDraft(ctx, identity, reportID)
	if err != nil {
		return UpgradePreview{}, err
	}
	if draft.DefinitionHash != version.DefinitionHash {
		return UpgradePreview{}, ErrUpgradeDraftDiverged
	}

	compilations := []SemanticCompilation{}
	definition, affected, beforeByID, err := applyUpgradeSpec(version.Definition, spec)
	if err != nil {
		return UpgradePreview{}, err
	}
	if spec.Kind == compiler.ChangeComponentTemplate {
		if service.Components == nil {
			return UpgradePreview{}, ErrUpgradeUnavailable
		}
		target := strings.SplitN(spec.NewObjectID, "@", 2)
		for index := range definition.Components {
			original, selected := beforeByID[definition.Components[index].ID]
			if !selected {
				continue
			}
			migrated, migrateErr := service.Components.MigrateComponent(original, target[1])
			if migrateErr != nil {
				return UpgradePreview{}, fmt.Errorf("%w: %v", ErrUpgradeUnavailable, migrateErr)
			}
			definition.Components[index] = migrated
		}
	}
	if requiresSemanticRecompile(spec.Kind) {
		if service.Recompiler == nil {
			return UpgradePreview{}, ErrUpgradeUnavailable
		}
		for index := range definition.Components {
			if _, selected := beforeByID[definition.Components[index].ID]; !selected {
				continue
			}
			if definition.Components[index].DataBinding == nil ||
				definition.Components[index].DataBinding.BindingMode != reportmodel.BindingSemanticIR ||
				definition.Components[index].DataBinding.SemanticQueryRef == nil {
				continue
			}
			recompiled, compileErr := service.Recompiler.RecompileComponent(ctx, identity, reportID, definition.Components[index])
			if compileErr != nil {
				return UpgradePreview{}, fmt.Errorf("%w: %v", ErrUpgradeUnavailable, compileErr)
			}
			definition.Components[index].DataBinding.SemanticQueryRef = &recompiled.Reference
			compilations = append(compilations, recompiled.Compilation)
		}
	}
	canonical, afterHash, err := compiler.Normalize(definition)
	if err != nil {
		return UpgradePreview{}, fmt.Errorf("%w: %v", ErrUpgradeUnavailable, err)
	}
	if err := json.Unmarshal(canonical, &definition); err != nil {
		return UpgradePreview{}, err
	}
	if service.Dependencies != nil {
		if issues := service.Dependencies.ValidateReportDependencies(ctx, identity, definition); len(issues) != 0 {
			return UpgradePreview{}, fmt.Errorf("%w: %v", ErrUpgradeUnavailable, issues)
		}
	}

	upgrades := make([]ComponentUpgrade, 0, len(affected))
	samples := []SampleImpact{}
	for _, componentID := range affected {
		before := beforeByID[componentID]
		after, _ := componentByID(definition, componentID)
		beforeHash := hashComponent(before)
		afterComponentHash := hashComponent(after)
		upgrades = append(upgrades, ComponentUpgrade{ComponentID: componentID, BeforeHash: beforeHash, AfterHash: afterComponentHash})
		if service.Comparator != nil {
			var compilation *SemanticCompilation
			for index := range compilations {
				if compilations[index].ComponentID == componentID {
					compilation = &compilations[index]
					break
				}
			}
			sample, compareErr := service.Comparator.CompareUpgrade(ctx, identity, UpgradeComparison{
				ReportID: reportID, BaseVersionID: version.ID, Before: before, After: after, Compilation: compilation,
			})
			if compareErr != nil {
				return UpgradePreview{}, compareErr
			}
			sample.ComponentID = componentID
			samples = append(samples, sample)
		}
	}
	preview := UpgradePreview{
		ReportID: reportID, BaseVersionID: version.ID, BaseDefinitionHash: version.DefinitionHash,
		BaseDraftRevision: draft.RevisionNo, BaseDraftHash: draft.DefinitionHash,
		AfterDefinitionHash: afterHash, AffectedComponents: upgrades, SampleImpacts: samples,
		definition: definition, compilations: compilations,
	}
	preview.ConfirmationToken = upgradeToken(preview, spec)
	return preview, nil
}

func (service *UpgradeService) Confirm(
	ctx context.Context, identity store.Identity, reportID askdata.ID, spec UpgradeSpec, token askdata.ContentHash,
) (store.Draft, store.Revision, error) {
	if token.Validate() != nil {
		return store.Draft{}, store.Revision{}, ErrUpgradePreviewStale
	}
	preview, err := service.Preview(ctx, identity, reportID, spec)
	if err != nil {
		return store.Draft{}, store.Revision{}, err
	}
	if preview.ConfirmationToken != token {
		return store.Draft{}, store.Revision{}, ErrUpgradePreviewStale
	}
	if len(preview.compilations) != 0 {
		if service.Compilations == nil {
			return store.Draft{}, store.Revision{}, ErrUpgradeUnavailable
		}
		if err := service.Compilations.SaveCompilations(ctx, identity, reportID, preview.compilations); err != nil {
			return store.Draft{}, store.Revision{}, fmt.Errorf("%w: %v", ErrUpgradeUnavailable, err)
		}
	}
	operationPayload := &operation.ReportCreatePayload{Definition: preview.definition}
	draft, revision, err := service.Repository.SaveDraftWithRevision(ctx, identity, reportID, store.SaveInput{
		ExpectedRevision: preview.BaseDraftRevision, Source: string(operation.SourceSystem),
		Operations: []operation.Operation{{Op: operation.ReportCreate, TargetID: reportID, Payload: operationPayload}},
	})
	return draft, revision, err
}

func validateUpgradeSpec(spec UpgradeSpec) error {
	if (compiler.ChangeSpec{Kind: spec.Kind, ObjectID: spec.OldObjectID}).Validate() != nil || strings.TrimSpace(spec.NewObjectID) == "" ||
		spec.NewObjectID == spec.OldObjectID {
		return ErrUpgradeInvalid
	}
	if spec.Kind == compiler.ChangeComponentTemplate {
		if (compiler.ChangeSpec{Kind: spec.Kind, ObjectID: spec.NewObjectID}).Validate() != nil {
			return ErrUpgradeInvalid
		}
		oldType := strings.SplitN(spec.OldObjectID, "@", 2)[0]
		newType := strings.SplitN(spec.NewObjectID, "@", 2)[0]
		if oldType != newType {
			return ErrUpgradeInvalid
		}
		return nil
	}
	if askdata.ID(spec.NewObjectID).Validate() != nil {
		return ErrUpgradeInvalid
	}
	if requiresSemanticRecompile(spec.Kind) && (spec.NewSemanticReleaseID.Validate() != nil || spec.NewSemanticContentHash.Validate() != nil) {
		return ErrUpgradeInvalid
	}
	return nil
}

func requiresSemanticRecompile(kind compiler.ChangeKind) bool {
	return kind == compiler.ChangeMetricVersion || kind == compiler.ChangeDimensionVersion ||
		kind == compiler.ChangeMemberVersion || kind == compiler.ChangeDatasetVersion ||
		kind == compiler.ChangeSemanticRelease
}

func applyUpgradeSpec(definition reportmodel.ReportDefinition, spec UpgradeSpec) (
	reportmodel.ReportDefinition, []askdata.ID, map[askdata.ID]reportmodel.Component, error,
) {
	raw, _ := json.Marshal(definition)
	var cloned reportmodel.ReportDefinition
	if err := json.Unmarshal(raw, &cloned); err != nil {
		return definition, nil, nil, err
	}
	definition = cloned
	affected := []askdata.ID{}
	before := map[askdata.ID]reportmodel.Component{}
	changedDataContexts := map[askdata.ID]struct{}{}
	if spec.Kind == compiler.ChangeDatasetVersion {
		for index := range definition.DataContexts {
			if string(definition.DataContexts[index].DatasetVersionID) == spec.OldObjectID {
				definition.DataContexts[index].DatasetVersionID = askdata.ID(spec.NewObjectID)
				changedDataContexts[definition.DataContexts[index].ID] = struct{}{}
			}
		}
	}
	for index := range definition.Components {
		component := &definition.Components[index]
		original, cloneErr := cloneComponent(*component)
		if cloneErr != nil {
			return definition, nil, nil, cloneErr
		}
		changed := false
		switch spec.Kind {
		case compiler.ChangeComponentTemplate:
			oldParts := strings.SplitN(spec.OldObjectID, "@", 2)
			newParts := strings.SplitN(spec.NewObjectID, "@", 2)
			if component.TemplateRef.Type == oldParts[0] && component.TemplateRef.Version == oldParts[1] {
				component.TemplateRef = reportmodel.ComponentTemplateReference{Type: newParts[0], Version: newParts[1]}
				changed = true
			}
		case compiler.ChangeDatasetVersion:
			if component.DataBinding != nil && component.DataBinding.SemanticQueryRef != nil && component.DataBinding.SemanticQueryRef.DatasetVersionID != nil &&
				string(*component.DataBinding.SemanticQueryRef.DatasetVersionID) == spec.OldObjectID {
				value := askdata.ID(spec.NewObjectID)
				component.DataBinding.SemanticQueryRef.DatasetVersionID = &value
				component.DataBinding.SemanticQueryRef.SemanticReleaseID = spec.NewSemanticReleaseID
				component.DataBinding.SemanticQueryRef.SemanticContentHash = spec.NewSemanticContentHash
				component.DataBinding.SemanticQueryRef.SemanticIR.SemanticReleaseID = spec.NewSemanticReleaseID
				component.DataBinding.SemanticQueryRef.SemanticIR.SemanticContentHash = spec.NewSemanticContentHash
				changed = true
			}
			if component.DataBinding != nil && component.DataBinding.DataContextID != nil {
				if _, exists := changedDataContexts[*component.DataBinding.DataContextID]; exists {
					changed = true
				}
			}
		default:
			if component.DataBinding == nil || component.DataBinding.SemanticQueryRef == nil {
				break
			}
			reference := component.DataBinding.SemanticQueryRef
			if spec.Kind == compiler.ChangeSemanticRelease && string(reference.SemanticReleaseID) == spec.OldObjectID {
				changed = true
			}
			for metricIndex := range reference.SemanticIR.Metrics {
				if spec.Kind == compiler.ChangeMetricVersion && string(reference.SemanticIR.Metrics[metricIndex].MetricVersionID) == spec.OldObjectID {
					reference.SemanticIR.Metrics[metricIndex].MetricVersionID = askdata.ID(spec.NewObjectID)
					changed = true
				}
			}
			for groupIndex := range reference.SemanticIR.GroupBy {
				if spec.Kind == compiler.ChangeDimensionVersion && string(reference.SemanticIR.GroupBy[groupIndex].DimensionVersionID) == spec.OldObjectID {
					reference.SemanticIR.GroupBy[groupIndex].DimensionVersionID = askdata.ID(spec.NewObjectID)
					changed = true
				}
			}
			for filterIndex := range reference.SemanticIR.Filters {
				filter := &reference.SemanticIR.Filters[filterIndex]
				if spec.Kind == compiler.ChangeDimensionVersion && string(filter.DimensionVersionID) == spec.OldObjectID {
					filter.DimensionVersionID = askdata.ID(spec.NewObjectID)
					changed = true
				}
				for memberIndex := range filter.MemberVersionIDs {
					if spec.Kind == compiler.ChangeMemberVersion && string(filter.MemberVersionIDs[memberIndex]) == spec.OldObjectID {
						filter.MemberVersionIDs[memberIndex] = askdata.ID(spec.NewObjectID)
						changed = true
					}
				}
			}
			if reference.SemanticIR.TimeRange != nil && spec.Kind == compiler.ChangeDimensionVersion && string(reference.SemanticIR.TimeRange.DimensionVersionID) == spec.OldObjectID {
				reference.SemanticIR.TimeRange.DimensionVersionID = askdata.ID(spec.NewObjectID)
				changed = true
			}
			if changed {
				reference.SemanticReleaseID, reference.SemanticContentHash = spec.NewSemanticReleaseID, spec.NewSemanticContentHash
				reference.SemanticIR.SemanticReleaseID, reference.SemanticIR.SemanticContentHash = spec.NewSemanticReleaseID, spec.NewSemanticContentHash
			}
		}
		if changed {
			before[component.ID] = original
			affected = append(affected, component.ID)
		}
	}
	if len(affected) == 0 {
		return definition, nil, nil, ErrUpgradeUnavailable
	}
	sort.Slice(affected, func(i, j int) bool { return affected[i] < affected[j] })
	return definition, affected, before, nil
}

func componentByID(definition reportmodel.ReportDefinition, id askdata.ID) (reportmodel.Component, bool) {
	for _, component := range definition.Components {
		if component.ID == id {
			return component, true
		}
	}
	return reportmodel.Component{}, false
}

func hashComponent(component reportmodel.Component) string {
	raw, _ := json.Marshal(component)
	return string(askdata.HashBytes(raw))
}

func cloneComponent(component reportmodel.Component) (reportmodel.Component, error) {
	raw, err := json.Marshal(component)
	if err != nil {
		return reportmodel.Component{}, err
	}
	var cloned reportmodel.Component
	if err := json.Unmarshal(raw, &cloned); err != nil {
		return reportmodel.Component{}, err
	}
	return cloned, nil
}

func upgradeToken(preview UpgradePreview, spec UpgradeSpec) askdata.ContentHash {
	raw, _ := json.Marshal(struct {
		ReportID            askdata.ID  `json:"reportId"`
		BaseVersionID       askdata.ID  `json:"baseVersionId"`
		BaseDefinitionHash  string      `json:"baseDefinitionHash"`
		BaseDraftRevision   int64       `json:"baseDraftRevision"`
		BaseDraftHash       string      `json:"baseDraftHash"`
		AfterDefinitionHash string      `json:"afterDefinitionHash"`
		Spec                UpgradeSpec `json:"spec"`
	}{preview.ReportID, preview.BaseVersionID, preview.BaseDefinitionHash, preview.BaseDraftRevision,
		preview.BaseDraftHash, preview.AfterDefinitionHash, spec})
	return askdata.HashBytes(append([]byte("report-upgrade-preview-v1\x00"), raw...))
}
