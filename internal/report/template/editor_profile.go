package template

import (
	_ "embed"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"intelligent-report-generation-system/internal/askdata"
)

// EditorProfile is deliberately kept outside the immutable Component Manifest.
// It describes the smallest authoring surface for one exact component version
// and may evolve without changing the renderer contract or its content hash.
type EditorProfile struct {
	ComponentType    string               `json:"componentType"`
	ComponentVersion string               `json:"componentVersion"`
	Example          EditorExample        `json:"example"`
	BindingGroups    []EditorBindingGroup `json:"bindingGroups"`
}

type EditorExample struct {
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Items       []string `json:"items"`
}

type EditorBindingKind string

const (
	EditorBindingDimension EditorBindingKind = "DIMENSION"
	EditorBindingMeasure   EditorBindingKind = "MEASURE"
)

type EditorBindingGroup struct {
	ID           string            `json:"id"`
	Label        string            `json:"label"`
	Description  string            `json:"description"`
	Kind         EditorBindingKind `json:"kind"`
	Roles        []BindingRole     `json:"roles"`
	Min          int               `json:"min"`
	Max          int               `json:"max"`
	AddLabel     string            `json:"addLabel"`
	NestedUnder  string            `json:"nestedUnder,omitempty"`
	MaxPerParent int               `json:"maxPerParent,omitempty"`
}

type editorProfileDocument struct {
	Version  string          `json:"version"`
	Profiles []EditorProfile `json:"profiles"`
}

var editorProfileIDPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,63}$`)

//go:embed editor-profiles.json
var bundledEditorProfiles []byte

func loadBundledEditorProfiles(registry *Registry) (map[string]EditorProfile, error) {
	var document editorProfileDocument
	if err := askdata.DecodeStrictJSON(bundledEditorProfiles, &document); err != nil {
		return nil, fmt.Errorf("decode component editor profiles: %w", err)
	}
	if document.Version != "1.0" || len(document.Profiles) == 0 {
		return nil, errors.New("component editor profile document is invalid")
	}
	profiles := make(map[string]EditorProfile, len(document.Profiles))
	for index, profile := range document.Profiles {
		manifest, exists := registry.Get(profile.ComponentType, profile.ComponentVersion)
		if !exists {
			return nil, fmt.Errorf("editor profiles[%d] references an unknown component", index)
		}
		if err := profile.validate(manifest); err != nil {
			return nil, fmt.Errorf("editor profiles[%d]: %w", index, err)
		}
		key := manifestKey(profile.ComponentType, profile.ComponentVersion)
		if _, duplicated := profiles[key]; duplicated {
			return nil, fmt.Errorf("editor profiles[%d] is duplicated", index)
		}
		profiles[key] = cloneEditorProfile(profile)
	}
	for _, manifest := range registry.List() {
		if _, exists := profiles[manifestKey(manifest.Type, manifest.Version)]; !exists {
			return nil, fmt.Errorf("component %s@%s has no editor profile", manifest.Type, manifest.Version)
		}
	}
	return profiles, nil
}

func (profile EditorProfile) validate(manifest Manifest) error {
	if profile.ComponentType != manifest.Type || profile.ComponentVersion != manifest.Version {
		return errors.New("component reference does not match manifest")
	}
	if strings.TrimSpace(profile.Example.Title) == "" || strings.TrimSpace(profile.Example.Description) == "" ||
		profile.Example.Items == nil || len(profile.Example.Items) > 8 {
		return errors.New("example must have a title, description and at most 8 items")
	}
	for index, item := range profile.Example.Items {
		if strings.TrimSpace(item) == "" || len([]rune(item)) > 80 {
			return fmt.Errorf("example.items[%d] is invalid", index)
		}
	}
	allowedRoles := make(map[BindingRole]struct{}, len(manifest.DataContract.Roles))
	for _, role := range manifest.DataContract.Roles {
		allowedRoles[role] = struct{}{}
	}
	groups := make(map[string]EditorBindingGroup, len(profile.BindingGroups))
	dimensionMin, dimensionMax, measureMin, measureMax := 0, 0, 0, 0
	for index, group := range profile.BindingGroups {
		if !editorProfileIDPattern.MatchString(group.ID) || strings.TrimSpace(group.Label) == "" ||
			strings.TrimSpace(group.Description) == "" {
			return fmt.Errorf("bindingGroups[%d] identity is invalid", index)
		}
		if group.Kind != EditorBindingDimension && group.Kind != EditorBindingMeasure {
			return fmt.Errorf("bindingGroups[%d].kind is invalid", index)
		}
		if group.Roles == nil || len(group.Roles) == 0 || group.Min < 0 || group.Max < group.Min || group.Max > 32 {
			return fmt.Errorf("bindingGroups[%d] cardinality is invalid", index)
		}
		seenRoles := map[BindingRole]struct{}{}
		for roleIndex, role := range group.Roles {
			if _, exists := allowedRoles[role]; !exists {
				return fmt.Errorf("bindingGroups[%d].roles[%d] is not allowed by the manifest", index, roleIndex)
			}
			if _, duplicated := seenRoles[role]; duplicated {
				return fmt.Errorf("bindingGroups[%d].roles[%d] is duplicated", index, roleIndex)
			}
			seenRoles[role] = struct{}{}
		}
		if _, duplicated := groups[group.ID]; duplicated {
			return fmt.Errorf("bindingGroups[%d].id is duplicated", index)
		}
		groups[group.ID] = group
		if group.Kind == EditorBindingDimension {
			dimensionMin += group.Min
			dimensionMax += group.Max
		} else {
			measureMin += group.Min
			measureMax += group.Max
		}
	}
	for index, group := range profile.BindingGroups {
		if group.NestedUnder == "" {
			if group.MaxPerParent != 0 {
				return fmt.Errorf("bindingGroups[%d].maxPerParent requires nestedUnder", index)
			}
			continue
		}
		parent, exists := groups[group.NestedUnder]
		if !exists || parent.NestedUnder != "" || parent.Kind != group.Kind || group.MaxPerParent < 1 ||
			group.Max > parent.Max*group.MaxPerParent {
			return fmt.Errorf("bindingGroups[%d] nested relation is invalid", index)
		}
	}
	if dimensionMin != manifest.DataContract.Dimensions.Min || dimensionMax != manifest.DataContract.Dimensions.Max ||
		measureMin != manifest.DataContract.Measures.Min || measureMax != manifest.DataContract.Measures.Max {
		return fmt.Errorf("binding group totals dimensions %d..%d / measures %d..%d do not match manifest contract",
			dimensionMin, dimensionMax, measureMin, measureMax)
	}
	return nil
}

func cloneEditorProfile(profile EditorProfile) EditorProfile {
	result := profile
	result.Example.Items = append([]string(nil), profile.Example.Items...)
	result.BindingGroups = append([]EditorBindingGroup(nil), profile.BindingGroups...)
	for index := range result.BindingGroups {
		result.BindingGroups[index].Roles = append([]BindingRole(nil), profile.BindingGroups[index].Roles...)
	}
	return result
}
