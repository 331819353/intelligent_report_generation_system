package operation

import (
	"errors"
	"fmt"
	"strings"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/report"
)

const (
	CodeNotAllowedForAI = "REPORT_OP_NOT_ALLOWED_FOR_AI"
	CodeOutOfScope      = "REPORT_OP_OUT_OF_SCOPE"
)

type GuardError struct {
	Code    string
	Message string
}

func (err *GuardError) Error() string {
	return err.Code + ": " + err.Message
}

func ErrorCode(err error) string {
	var guardError *GuardError
	if errors.As(err, &guardError) {
		return guardError.Code
	}
	return ""
}

func GuardAI(bundle Bundle, current *report.ReportDefinition) error {
	if bundle.Source != SourceAI {
		return nil
	}
	if bundle.Scope == nil {
		return &GuardError{Code: CodeOutOfScope, Message: "AI source requires scope"}
	}
	for _, operation := range bundle.Operations {
		if operation.Op == TemplateApply || operation.Op == PageDelete || operation.Op == SectionDelete {
			return &GuardError{
				Code:    CodeNotAllowedForAI,
				Message: fmt.Sprintf("%s requires explicit human confirmation", operation.Op),
			}
		}
	}
	deleteCount := 0
	for _, operation := range bundle.Operations {
		if strings.HasSuffix(string(operation.Op), "_DELETE") {
			deleteCount++
		}
	}
	if deleteCount > MaxAIDeleteOperations {
		return &GuardError{
			Code:    CodeNotAllowedForAI,
			Message: fmt.Sprintf("AI delete count %d exceeds %d", deleteCount, MaxAIDeleteOperations),
		}
	}
	if current == nil {
		return &GuardError{Code: CodeOutOfScope, Message: "current Report Definition is required to verify AI scope"}
	}
	if current.Metadata.ID != bundle.ReportID {
		return &GuardError{
			Code:    CodeOutOfScope,
			Message: fmt.Sprintf("bundle reportId %q does not match current report %q", bundle.ReportID, current.Metadata.ID),
		}
	}
	index := buildTargetIndex(*current)
	if err := validateScope(*bundle.Scope, index); err != nil {
		return &GuardError{Code: CodeOutOfScope, Message: err.Error()}
	}
	for indexValue, operation := range bundle.Operations {
		paths := index[operation.TargetID]
		if len(paths) == 0 {
			return &GuardError{
				Code:    CodeOutOfScope,
				Message: fmt.Sprintf("operations[%d].targetId %q is not in the current report", indexValue, operation.TargetID),
			}
		}
		for _, path := range paths {
			if !bundle.Scope.contains(path) {
				return &GuardError{
					Code:    CodeOutOfScope,
					Message: fmt.Sprintf("operations[%d].targetId %q is outside the declared scope", indexValue, operation.TargetID),
				}
			}
		}
	}
	return nil
}

type targetPath struct {
	PageID    askdata.ID
	SectionID askdata.ID
	BlockID   askdata.ID
}

func (scope Scope) contains(path targetPath) bool {
	if scope.PageID != nil && path.PageID != *scope.PageID {
		return false
	}
	if scope.SectionID != nil && path.SectionID != *scope.SectionID {
		return false
	}
	if scope.BlockID != nil && path.BlockID != *scope.BlockID {
		return false
	}
	return true
}

func validateScope(scope Scope, index map[askdata.ID][]targetPath) error {
	pagePaths := index[*scope.PageID]
	if len(pagePaths) == 0 {
		return fmt.Errorf("scope.pageId %q does not exist", *scope.PageID)
	}
	if scope.SectionID != nil {
		paths := index[*scope.SectionID]
		if len(paths) == 0 || !containsExactAncestry(paths, *scope.PageID, scope.SectionID, "") {
			return fmt.Errorf("scope.sectionId %q is not under page %q", *scope.SectionID, *scope.PageID)
		}
	}
	if scope.BlockID != nil {
		paths := index[*scope.BlockID]
		if len(paths) == 0 || !containsExactAncestry(paths, *scope.PageID, scope.SectionID, *scope.BlockID) {
			return fmt.Errorf("scope.blockId %q is not under the declared page/section", *scope.BlockID)
		}
	}
	return nil
}

func containsExactAncestry(paths []targetPath, pageID askdata.ID, sectionID *askdata.ID, blockID askdata.ID) bool {
	for _, path := range paths {
		if path.PageID != pageID {
			continue
		}
		if sectionID != nil && path.SectionID != *sectionID {
			continue
		}
		if blockID != "" && path.BlockID != blockID {
			continue
		}
		return true
	}
	return false
}

func buildTargetIndex(definition report.ReportDefinition) map[askdata.ID][]targetPath {
	index := map[askdata.ID][]targetPath{}
	addPath(index, definition.Metadata.ID, targetPath{})
	for _, page := range definition.Pages {
		pagePath := targetPath{PageID: page.ID}
		addPath(index, page.ID, pagePath)
		for _, section := range page.Sections {
			sectionPath := targetPath{PageID: page.ID, SectionID: section.ID}
			addPath(index, section.ID, sectionPath)
			for _, block := range section.Blocks {
				blockPath := targetPath{PageID: page.ID, SectionID: section.ID, BlockID: block.ID}
				addPath(index, block.ID, blockPath)
				for _, zone := range block.Zones {
					addPath(index, zone.ID, blockPath)
					for _, slot := range zone.Slots {
						addPath(index, slot.ID, blockPath)
						if slot.ComponentID != "" {
							addPath(index, slot.ComponentID, blockPath)
						}
					}
				}
			}
		}
	}
	for _, filter := range definition.GlobalFilters {
		paths := pathsForFilter(filter, index)
		for _, path := range paths {
			addPath(index, filter.ID, path)
		}
	}
	for _, interaction := range definition.Interactions {
		for _, path := range index[interaction.SourceComponentID] {
			addPath(index, interaction.ID, path)
		}
	}
	return index
}

func pathsForFilter(filter report.GlobalFilter, index map[askdata.ID][]targetPath) []targetPath {
	if filter.Scope.Type == report.FilterScopeReport {
		return []targetPath{{}}
	}
	var paths []targetPath
	for _, targetID := range filter.Scope.TargetIDs {
		paths = append(paths, index[targetID]...)
	}
	return paths
}

func addPath(index map[askdata.ID][]targetPath, id askdata.ID, path targetPath) {
	if id == "" {
		return
	}
	for _, existing := range index[id] {
		if existing == path {
			return
		}
	}
	index[id] = append(index[id], path)
}
