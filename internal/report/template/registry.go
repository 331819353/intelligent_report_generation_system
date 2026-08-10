package template

import (
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strings"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/report"
)

//go:embed manifests/*.json
var bundledManifests embed.FS

type Migrator func(options json.RawMessage) (json.RawMessage, error)

type Registry struct {
	manifests     map[string]Manifest
	contentHashes map[string]askdata.ContentHash
	byContentHash map[askdata.ContentHash]Manifest
	migrators     map[string]Migrator
}

func NewDefaultRegistry() (*Registry, error) {
	manifests, err := LoadManifestsFS(bundledManifests, "manifests")
	if err != nil {
		return nil, err
	}
	return NewRegistry(manifests, nil)
}

func NewRegistry(manifests []Manifest, migrators map[string]Migrator) (*Registry, error) {
	registry := &Registry{
		manifests:     make(map[string]Manifest, len(manifests)),
		contentHashes: make(map[string]askdata.ContentHash, len(manifests)),
		byContentHash: make(map[askdata.ContentHash]Manifest, len(manifests)),
		migrators:     make(map[string]Migrator, len(migrators)),
	}
	for id, migrator := range migrators {
		if !migratorIDPattern.MatchString(id) || migrator == nil {
			return nil, fmt.Errorf("migrator %q is invalid", id)
		}
		registry.migrators[id] = migrator
	}
	byType := map[string][]Manifest{}
	for index, manifest := range manifests {
		if err := manifest.Validate(); err != nil {
			return nil, fmt.Errorf("manifests[%d]: %w", index, err)
		}
		key := manifestKey(manifest.Type, manifest.Version)
		if _, exists := registry.manifests[key]; exists {
			return nil, fmt.Errorf("duplicate component manifest %s@%s", manifest.Type, manifest.Version)
		}
		manifestCopy := cloneManifest(manifest)
		raw, err := json.Marshal(manifestCopy)
		if err != nil {
			return nil, fmt.Errorf("encode manifests[%d]: %w", index, err)
		}
		contentHash := askdata.HashBytes(raw)
		if existing, collision := registry.byContentHash[contentHash]; collision &&
			(existing.Type != manifest.Type || existing.Version != manifest.Version) {
			return nil, fmt.Errorf("component manifest content hash collision for %s@%s", manifest.Type, manifest.Version)
		}
		registry.manifests[key] = manifestCopy
		registry.contentHashes[key] = contentHash
		registry.byContentHash[contentHash] = manifestCopy
		byType[manifest.Type] = append(byType[manifest.Type], manifest)
	}
	for componentType, versions := range byType {
		sort.Slice(versions, func(left, right int) bool {
			return compareSemverMust(versions[left].Version, versions[right].Version) < 0
		})
		for index := 1; index < len(versions); index++ {
			if err := CheckUpgrade(versions[index-1], versions[index], registry.migrators); err != nil {
				return nil, fmt.Errorf("component %s upgrade %s -> %s: %w", componentType, versions[index-1].Version, versions[index].Version, err)
			}
		}
	}
	return registry, nil
}

func LoadManifestsFS(fsys fs.FS, directory string) ([]Manifest, error) {
	paths, err := fs.Glob(fsys, strings.TrimSuffix(directory, "/")+"/*.json")
	if err != nil {
		return nil, fmt.Errorf("list component manifests: %w", err)
	}
	if len(paths) == 0 {
		return nil, errors.New("no component manifests found")
	}
	sort.Strings(paths)
	manifests := make([]Manifest, 0, len(paths))
	for _, path := range paths {
		raw, err := fs.ReadFile(fsys, path)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", path, err)
		}
		manifest, err := DecodeManifest(raw)
		if err != nil {
			return nil, fmt.Errorf("decode %s: %w", path, err)
		}
		manifests = append(manifests, manifest)
	}
	return manifests, nil
}

func (registry *Registry) Get(componentType, version string) (Manifest, bool) {
	if registry == nil {
		return Manifest{}, false
	}
	manifest, exists := registry.manifests[manifestKey(componentType, version)]
	if !exists {
		return Manifest{}, false
	}
	return cloneManifest(manifest), true
}

// Has reports whether an exact immutable component manifest is available.
// Runtime callers deliberately do not receive a "latest" fallback: a report
// must render with the version pinned at publication time or fail closed.
func (registry *Registry) Has(componentType, version string) bool {
	_, exists := registry.Get(componentType, version)
	return exists
}

func (registry *Registry) ContentHash(componentType, version string) (askdata.ContentHash, bool) {
	if registry == nil {
		return "", false
	}
	hash, exists := registry.contentHashes[manifestKey(componentType, version)]
	return hash, exists
}

func (registry *Registry) GetByContentHash(contentHash askdata.ContentHash) (Manifest, bool) {
	if registry == nil || contentHash.Validate() != nil {
		return Manifest{}, false
	}
	manifest, exists := registry.byContentHash[contentHash]
	if !exists {
		return Manifest{}, false
	}
	return cloneManifest(manifest), true
}

// MigrateComponent prepares the explicit COMPONENT_REPLACE payload for a
// caller. It only moves forward through registered versions and invokes each
// declared major migrator; it never changes a published definition in place.
func (registry *Registry) MigrateComponent(component report.Component, targetVersion string) (report.Component, error) {
	if registry == nil {
		return report.Component{}, errors.New("component registry is unavailable")
	}
	current, exists := registry.Get(component.TemplateRef.Type, component.TemplateRef.Version)
	if !exists {
		return report.Component{}, fmt.Errorf("component manifest %s@%s is not registered", component.TemplateRef.Type, component.TemplateRef.Version)
	}
	target, exists := registry.Get(component.TemplateRef.Type, targetVersion)
	if !exists {
		return report.Component{}, fmt.Errorf("component manifest %s@%s is not registered", component.TemplateRef.Type, targetVersion)
	}
	if comparison, err := CompareSemver(target.Version, current.Version); err != nil || comparison <= 0 {
		return report.Component{}, errors.New("component migration target must be a newer exact version")
	}
	rawComponent, err := json.Marshal(component)
	if err != nil {
		return report.Component{}, err
	}
	var result report.Component
	if err := askdata.DecodeStrictJSON(rawComponent, &result); err != nil {
		return report.Component{}, err
	}
	options, err := json.Marshal(result.Options)
	if err != nil {
		return report.Component{}, err
	}
	versions := make([]Manifest, 0)
	for _, candidate := range registry.List() {
		if candidate.Type == current.Type && compareSemverMust(candidate.Version, current.Version) > 0 &&
			compareSemverMust(candidate.Version, target.Version) <= 0 {
			versions = append(versions, candidate)
		}
	}
	previous := current
	for _, next := range versions {
		previousVersion, _ := parseSemver(previous.Version)
		nextVersion, _ := parseSemver(next.Version)
		if previousVersion.Major != nextVersion.Major {
			if next.Migration == nil || next.Migration.From != previous.Version {
				return report.Component{}, fmt.Errorf("component migration path %s -> %s is incomplete", previous.Version, next.Version)
			}
			migrator := registry.migrators[next.Migration.MigratorID]
			if migrator == nil {
				return report.Component{}, fmt.Errorf("component migrator %q is unavailable", next.Migration.MigratorID)
			}
			options, err = migrator(append(json.RawMessage(nil), options...))
			if err != nil {
				return report.Component{}, fmt.Errorf("component migrator %q: %w", next.Migration.MigratorID, err)
			}
		}
		if err := next.OptionSchema.ValidateJSON(options); err != nil {
			return report.Component{}, fmt.Errorf("migrated options for %s@%s: %w", next.Type, next.Version, err)
		}
		previous = next
	}
	if previous.Version != target.Version {
		return report.Component{}, errors.New("component migration path is incomplete")
	}
	var migratedOptions report.ComponentOptions
	if err := askdata.DecodeStrictJSON(options, &migratedOptions); err != nil {
		return report.Component{}, fmt.Errorf("decode migrated component options: %w", err)
	}
	result.TemplateRef.Version = target.Version
	result.Options = migratedOptions
	if err := registry.ValidateComponentData(result); err != nil {
		return report.Component{}, err
	}
	return result, nil
}

func (registry *Registry) List() []Manifest {
	if registry == nil {
		return nil
	}
	manifests := make([]Manifest, 0, len(registry.manifests))
	for _, manifest := range registry.manifests {
		manifests = append(manifests, cloneManifest(manifest))
	}
	sort.Slice(manifests, func(left, right int) bool {
		if manifests[left].Type != manifests[right].Type {
			return manifests[left].Type < manifests[right].Type
		}
		return compareSemverMust(manifests[left].Version, manifests[right].Version) < 0
	})
	return manifests
}

func (registry *Registry) ValidateOptions(componentType, version string, raw []byte) error {
	manifest, exists := registry.Get(componentType, version)
	if !exists {
		return fmt.Errorf("component manifest %s@%s is not registered", componentType, version)
	}
	return manifest.OptionSchema.ValidateJSON(raw)
}

func (registry *Registry) ValidateComponent(component report.Component, width, height int) error {
	if err := registry.ValidateComponentSchema(component, width, height); err != nil {
		return err
	}
	return registry.ValidateComponentData(component)
}

// ValidateComponentSchema is normalization stage 9: exact manifest version,
// renderable size, and the closed option schema.
func (registry *Registry) ValidateComponentSchema(component report.Component, width, height int) error {
	manifest, exists := registry.Get(component.TemplateRef.Type, component.TemplateRef.Version)
	if !exists {
		return fmt.Errorf("component manifest %s@%s is not registered", component.TemplateRef.Type, component.TemplateRef.Version)
	}
	if width < manifest.MinSize.W || height < manifest.MinSize.H {
		return fmt.Errorf("component %q size %dx%d is below manifest minimum %dx%d", component.ID, width, height, manifest.MinSize.W, manifest.MinSize.H)
	}
	options, err := json.Marshal(component.Options)
	if err != nil {
		return fmt.Errorf("encode component options: %w", err)
	}
	if err := manifest.OptionSchema.ValidateJSON(options); err != nil {
		return fmt.Errorf("component %q options: %w", component.ID, err)
	}
	return nil
}

// ValidateComponentData is normalization stage 10: binding cardinality,
// logical roles, and required time semantics from the manifest data contract.
func (registry *Registry) ValidateComponentData(component report.Component) error {
	manifest, exists := registry.Get(component.TemplateRef.Type, component.TemplateRef.Version)
	if !exists {
		return fmt.Errorf("component manifest %s@%s is not registered", component.TemplateRef.Type, component.TemplateRef.Version)
	}
	if err := validateComponentData(manifest, component); err != nil {
		return fmt.Errorf("component %q data: %w", component.ID, err)
	}
	return nil
}

func validateComponentData(manifest Manifest, component report.Component) error {
	dimensions := 0
	measures := 0
	hasTime := false
	if component.DataBinding != nil {
		switch component.DataBinding.BindingMode {
		case report.BindingSemanticIR:
			if component.DataBinding.SemanticQueryRef == nil {
				return errors.New("SEMANTIC_IR requires semanticQueryRef")
			}
			dimensions = len(component.DataBinding.SemanticQueryRef.SemanticIR.GroupBy)
			measures = len(component.DataBinding.SemanticQueryRef.SemanticIR.Metrics)
			hasTime = component.DataBinding.SemanticQueryRef.SemanticIR.TimeRange != nil
			for _, group := range component.DataBinding.SemanticQueryRef.SemanticIR.GroupBy {
				hasTime = hasTime || group.Grain != nil
			}
		case report.BindingDatasetField:
			dimensions = len(component.DataBinding.Dimensions)
			measures = len(component.DataBinding.Measures)
			allowedRoles := make(map[report.BindingRole]struct{}, len(manifest.DataContract.Roles))
			for _, role := range manifest.DataContract.Roles {
				allowedRoles[report.BindingRole(role)] = struct{}{}
			}
			for _, binding := range append(append([]report.FieldBinding(nil), component.DataBinding.Dimensions...), component.DataBinding.Measures...) {
				if _, exists := allowedRoles[binding.Role]; !exists {
					return fmt.Errorf("role %s is not supported", binding.Role)
				}
				hasTime = hasTime || binding.Role == report.RoleTime
			}
		default:
			return errors.New("bindingMode is invalid")
		}
	}
	if dimensions < manifest.DataContract.Dimensions.Min || dimensions > manifest.DataContract.Dimensions.Max {
		return fmt.Errorf("dimensions count %d is outside %d..%d", dimensions, manifest.DataContract.Dimensions.Min, manifest.DataContract.Dimensions.Max)
	}
	if measures < manifest.DataContract.Measures.Min || measures > manifest.DataContract.Measures.Max {
		return fmt.Errorf("measures count %d is outside %d..%d", measures, manifest.DataContract.Measures.Min, manifest.DataContract.Measures.Max)
	}
	if manifest.DataContract.TimeField.Required && !hasTime {
		return errors.New("time field is required")
	}
	return nil
}

func CheckUpgrade(previous, next Manifest, migrators map[string]Migrator) error {
	if previous.Type != next.Type {
		return errors.New("component type cannot change")
	}
	if err := previous.Validate(); err != nil {
		return fmt.Errorf("previous manifest: %w", err)
	}
	if err := next.Validate(); err != nil {
		return fmt.Errorf("next manifest: %w", err)
	}
	previousVersion, _ := parseSemver(previous.Version)
	nextVersion, _ := parseSemver(next.Version)
	if compareSemver(previousVersion, nextVersion) >= 0 {
		return errors.New("next version must be greater than previous version")
	}
	if previousVersion.Major != nextVersion.Major {
		if next.Migration == nil || next.Migration.From != previous.Version {
			return errors.New("major upgrade requires migration.from matching the previous version")
		}
		migrator, exists := migrators[next.Migration.MigratorID]
		if !exists || migrator == nil {
			return fmt.Errorf("major upgrade migrator %q is not registered", next.Migration.MigratorID)
		}
		return nil
	}
	if next.Migration != nil {
		return errors.New("patch/minor upgrade must not declare a major migrator")
	}
	if err := checkOptionSchemaCompatibility(previous.OptionSchema, next.OptionSchema); err != nil {
		return err
	}
	if next.MinSize.W > previous.MinSize.W || next.MinSize.H > previous.MinSize.H {
		return errors.New("patch/minor upgrade cannot increase minSize")
	}
	if next.DataContract.Dimensions.Min > previous.DataContract.Dimensions.Min ||
		next.DataContract.Dimensions.Max < previous.DataContract.Dimensions.Max ||
		next.DataContract.Measures.Min > previous.DataContract.Measures.Min ||
		next.DataContract.Measures.Max < previous.DataContract.Measures.Max ||
		(!previous.DataContract.TimeField.Required && next.DataContract.TimeField.Required) {
		return errors.New("patch/minor upgrade cannot narrow dataContract")
	}
	if !isRoleSuperset(next.DataContract.Roles, previous.DataContract.Roles) {
		return errors.New("patch/minor upgrade cannot remove data roles")
	}
	if !isInteractionSuperset(next.SupportedInteractions, previous.SupportedInteractions) {
		return errors.New("patch/minor upgrade cannot remove supported interactions")
	}
	if previous.MobilePolicy.Supported && !next.MobilePolicy.Supported {
		return errors.New("patch/minor upgrade cannot disable mobile support")
	}
	return nil
}

func checkOptionSchemaCompatibility(previous, next OptionSchema) error {
	previousRequired := stringSet(previous.Required)
	nextRequired := stringSet(next.Required)
	for required := range nextRequired {
		if _, existed := previousRequired[required]; !existed {
			return fmt.Errorf("patch/minor upgrade adds required option %q", required)
		}
	}
	for name, previousProperty := range previous.Properties {
		nextProperty, exists := next.Properties[name]
		if !exists {
			return fmt.Errorf("patch/minor upgrade removes option %q", name)
		}
		if previousProperty.Type != nextProperty.Type {
			return fmt.Errorf("patch/minor upgrade changes type of option %q", name)
		}
		if !constraintIsCompatible(previousProperty.Minimum, nextProperty.Minimum, true) ||
			!constraintIsCompatible(previousProperty.Maximum, nextProperty.Maximum, false) {
			return fmt.Errorf("patch/minor upgrade narrows numeric option %q", name)
		}
		if !enumIsCompatible(previousProperty.Enum, nextProperty.Enum) {
			return fmt.Errorf("patch/minor upgrade narrows enum option %q", name)
		}
	}
	return nil
}

func constraintIsCompatible(previous, next *float64, isMinimum bool) bool {
	if previous == nil {
		return next == nil
	}
	if next == nil {
		return true
	}
	if isMinimum {
		return *next <= *previous
	}
	return *next >= *previous
}

func enumIsCompatible(previous, next []string) bool {
	if len(previous) == 0 {
		return len(next) == 0
	}
	if len(next) == 0 {
		return true
	}
	nextValues := stringSet(next)
	for _, value := range previous {
		if _, exists := nextValues[value]; !exists {
			return false
		}
	}
	return true
}

func isRoleSuperset(next, previous []BindingRole) bool {
	values := map[BindingRole]struct{}{}
	for _, value := range next {
		values[value] = struct{}{}
	}
	for _, value := range previous {
		if _, exists := values[value]; !exists {
			return false
		}
	}
	return true
}

func isInteractionSuperset(next, previous []InteractionKind) bool {
	values := map[InteractionKind]struct{}{}
	for _, value := range next {
		values[value] = struct{}{}
	}
	for _, value := range previous {
		if _, exists := values[value]; !exists {
			return false
		}
	}
	return true
}

type semver struct {
	Major int
	Minor int
	Patch int
}

func parseSemver(raw string) (semver, error) {
	var version semver
	if _, err := fmt.Sscanf(raw, "%d.%d.%d", &version.Major, &version.Minor, &version.Patch); err != nil ||
		version.Major < 0 || version.Minor < 0 || version.Patch < 0 ||
		fmt.Sprintf("%d.%d.%d", version.Major, version.Minor, version.Patch) != raw {
		return semver{}, errors.New("must use MAJOR.MINOR.PATCH without prefixes or leading zeroes")
	}
	return version, nil
}

func compareSemver(left, right semver) int {
	if left.Major != right.Major {
		return left.Major - right.Major
	}
	if left.Minor != right.Minor {
		return left.Minor - right.Minor
	}
	return left.Patch - right.Patch
}

func compareSemverMust(left, right string) int {
	leftVersion, _ := parseSemver(left)
	rightVersion, _ := parseSemver(right)
	return compareSemver(leftVersion, rightVersion)
}

// CompareSemver compares two strict MAJOR.MINOR.PATCH versions.
func CompareSemver(left, right string) (int, error) {
	leftVersion, err := parseSemver(left)
	if err != nil {
		return 0, fmt.Errorf("left version: %w", err)
	}
	rightVersion, err := parseSemver(right)
	if err != nil {
		return 0, fmt.Errorf("right version: %w", err)
	}
	return compareSemver(leftVersion, rightVersion), nil
}

func manifestKey(componentType, version string) string {
	return componentType + "\x00" + version
}

func cloneManifest(manifest Manifest) Manifest {
	raw, err := json.Marshal(manifest)
	if err != nil {
		return Manifest{}
	}
	var clone Manifest
	if err := json.Unmarshal(raw, &clone); err != nil {
		return Manifest{}
	}
	return clone
}

func stringSet(values []string) map[string]struct{} {
	set := make(map[string]struct{}, len(values))
	for _, value := range values {
		set[value] = struct{}{}
	}
	return set
}
