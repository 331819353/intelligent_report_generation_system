package reportai

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"intelligent-report-generation-system/internal/report/insight"
	"intelligent-report-generation-system/internal/report/template"
)

type Plan struct {
	ReportTemplateVersion string        `json:"reportTemplateVersion"`
	Sections              []PlanSection `json:"sections"`
}

type PlanSection struct {
	Title   string      `json:"title"`
	Purpose string      `json:"purpose"`
	Blocks  []PlanBlock `json:"blocks"`
}

type PlanBlock struct {
	Purpose              string                   `json:"purpose"`
	RecommendedComponent string                   `json:"recommendedComponent"`
	DataRoles            PlanDataRoles            `json:"dataRoles"`
	AnalysisMethods      []insight.AnalysisMethod `json:"analysisMethods"`
	DesktopHint          DesktopHint              `json:"desktopHint"`
	MobileHint           MobileHint               `json:"mobileHint"`
}

type PlanDataRoles struct {
	Dimensions []string `json:"dimensions"`
	Measures   []string `json:"measures"`
}

type DesktopHint struct {
	W int `json:"w"`
	H int `json:"h"`
}
type MobileHint struct {
	Order int `json:"order"`
}

type PlanGenerator interface {
	GenerateReportPlan(context.Context, PlanRequest) (Plan, error)
}

type PlanRequest struct {
	Intent            string                   `json:"intent"`
	AllowedFieldNames []string                 `json:"allowedFieldNames"`
	AllowedComponents []string                 `json:"allowedComponents"`
	AllowedMethods    []insight.AnalysisMethod `json:"allowedMethods"`
	TemplateVersions  []string                 `json:"templateVersions"`
}

func GeneratePlan(ctx context.Context, generator PlanGenerator, request PlanRequest, componentRegistry *template.Registry, methodRegistry *insight.Registry) (Plan, error) {
	if generator == nil || componentRegistry == nil || methodRegistry == nil || strings.TrimSpace(request.Intent) == "" {
		return Plan{}, errors.New("report plan request is invalid")
	}
	plan, err := generator.GenerateReportPlan(ctx, request)
	if err != nil {
		return Plan{}, err
	}
	return plan, ValidatePlan(plan, request, componentRegistry, methodRegistry)
}

func ValidatePlan(plan Plan, request PlanRequest, componentRegistry *template.Registry, methodRegistry *insight.Registry) error {
	if strings.TrimSpace(plan.ReportTemplateVersion) == "" || len(plan.Sections) == 0 || len(plan.Sections) > 30 {
		return errors.New("plan requires a template version and 1..30 sections")
	}
	if len(request.TemplateVersions) != 0 {
		if _, allowed := stringSet(request.TemplateVersions)[plan.ReportTemplateVersion]; !allowed {
			return fmt.Errorf("report template version %q is not allowed", plan.ReportTemplateVersion)
		}
	}
	allowedFields := stringSet(request.AllowedFieldNames)
	allowedComponents := stringSet(request.AllowedComponents)
	allowedMethods := map[insight.AnalysisMethod]struct{}{}
	for _, method := range request.AllowedMethods {
		allowedMethods[method] = struct{}{}
	}
	totalBlocks := 0
	for sectionIndex, section := range plan.Sections {
		if strings.TrimSpace(section.Title) == "" || strings.TrimSpace(section.Purpose) == "" || len(section.Blocks) == 0 {
			return fmt.Errorf("sections[%d] is incomplete", sectionIndex)
		}
		totalBlocks += len(section.Blocks)
		if totalBlocks > 300 {
			return errors.New("plan exceeds 300 blocks")
		}
		for blockIndex, block := range section.Blocks {
			path := fmt.Sprintf("sections[%d].blocks[%d]", sectionIndex, blockIndex)
			if _, allowed := allowedComponents[block.RecommendedComponent]; !allowed {
				return fmt.Errorf("%s component %q is not allowed", path, block.RecommendedComponent)
			}
			if !componentTypeExists(componentRegistry, block.RecommendedComponent) {
				return fmt.Errorf("%s component %q is not registered", path, block.RecommendedComponent)
			}
			if block.DesktopHint.W < 1 || block.DesktopHint.W > 24 || block.DesktopHint.H < 1 || block.MobileHint.Order < 1 {
				return fmt.Errorf("%s layout hint is invalid", path)
			}
			for _, field := range append(append([]string(nil), block.DataRoles.Dimensions...), block.DataRoles.Measures...) {
				if _, allowed := allowedFields[field]; !allowed {
					return fmt.Errorf("%s field %q is unavailable", path, field)
				}
			}
			for _, method := range block.AnalysisMethods {
				if _, allowed := allowedMethods[method]; !allowed {
					return fmt.Errorf("%s analysis method %q is not allowed", path, method)
				}
				if _, registered := methodRegistry.Get(method); !registered {
					return fmt.Errorf("%s analysis method %q is not registered", path, method)
				}
			}
		}
	}
	return nil
}

func componentTypeExists(registry *template.Registry, componentType string) bool {
	for _, manifest := range registry.List() {
		if manifest.Type == componentType {
			return true
		}
	}
	return false
}

func stringSet(values []string) map[string]struct{} {
	result := make(map[string]struct{}, len(values))
	for _, value := range values {
		result[value] = struct{}{}
	}
	return result
}
