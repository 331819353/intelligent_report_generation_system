package publication

import (
	"context"
	"errors"
	"strings"

	"intelligent-report-generation-system/internal/askdata"
	reportmodel "intelligent-report-generation-system/internal/report"
	"intelligent-report-generation-system/internal/report/compiler"
	"intelligent-report-generation-system/internal/report/store"
)

type GateStatus string

const (
	GatePassed  GateStatus = "PASSED"
	GateWarning GateStatus = "WARNING"
	GateBlocked GateStatus = "BLOCKED"
)

type GateCheck struct {
	ID      string                    `json:"id"`
	Label   string                    `json:"label"`
	Status  GateStatus                `json:"status"`
	Summary string                    `json:"summary"`
	Issues  compiler.ValidationIssues `json:"issues"`
}

type PreflightRequest struct {
	ReportID         askdata.ID
	SourceRevisionNo *int64
}

type PreflightResult struct {
	Draft        store.Draft `json:"draft"`
	Checks       []GateCheck `json:"checks"`
	BlockerCodes []string    `json:"blockerCodes"`
	WarningCodes []string    `json:"warningCodes"`
}

// Preflight executes the same deterministic trust boundary used by Publish,
// without writing a version or artifact. The LLM may explain these results but
// cannot downgrade a blocker or invent a passing gate.
func (publisher *Publisher) Preflight(ctx context.Context, identity store.Identity, request PreflightRequest) (PreflightResult, error) {
	if publisher == nil || publisher.Repository == nil || publisher.Authorizer == nil {
		return PreflightResult{}, errors.New("publisher preflight is not configured")
	}
	if err := publisher.Authorizer.CheckReportPublish(ctx, identity, request.ReportID); err != nil {
		return PreflightResult{}, &StepError{Step: 1, Code: "REPORT_PUBLISH_FORBIDDEN", Err: err}
	}
	draft, err := publisher.Repository.GetDraftRevision(ctx, identity, request.ReportID, request.SourceRevisionNo)
	if err != nil {
		return PreflightResult{}, &StepError{Step: 2, Code: "REPORT_DRAFT_NOT_FOUND", Err: err}
	}
	static := publicationDefinitionIssues(draft.Definition)
	domainIssues := compiler.ValidationIssues{}
	if err := validatePublicationDomain(identity, draft.Definition); err != nil {
		domainIssues = append(domainIssues, compiler.ValidationIssue{Code: "REPORT_DOMAIN_INVALID", Path: "dataContexts", Message: err.Error()})
	}
	dependencyIssues := compiler.ValidationIssues{}
	if publisher.Dependencies == nil {
		dependencyIssues = append(dependencyIssues, compiler.ValidationIssue{Code: "REPORT_DEPENDENCY_INVALID", Path: "dataContexts", Message: "dependency validator is unavailable"})
	} else {
		dependencyIssues = publisher.Dependencies.ValidateReportDependencies(ctx, identity, draft.Definition)
	}
	insightIssues := compiler.ValidationIssues{}
	if publisher.Insights == nil {
		insightIssues = append(insightIssues, compiler.ValidationIssue{Code: "REPORT_INSIGHT_INVALID", Path: "components", Message: "insight validator is unavailable"})
	} else {
		insightIssues = publisher.Insights.ValidateReportInsights(ctx, identity, request.ReportID, false)
	}
	normalizeIssues := compiler.ValidationIssues{}
	normalizer := publisher.Normalizer
	if normalizer == nil {
		normalizer = defaultDefinitionNormalizer{}
	}
	if _, _, hash, normalizeErr := normalizer.Normalize(draft.Definition); normalizeErr != nil {
		normalizeIssues = append(normalizeIssues, compiler.ValidationIssue{Code: "REPORT_NORMALIZATION_FAILED", Path: "$", Message: normalizeErr.Error()})
	} else if hash != draft.DefinitionHash {
		normalizeIssues = append(normalizeIssues, compiler.ValidationIssue{Code: "REPORT_SOURCE_HASH_MISMATCH", Path: "$", Message: "draft hash does not match its normalized definition"})
	}

	semanticIssues := appendIssues(static[3], domainIssues, dependencyIssues)
	checks := []GateCheck{
		gate("SEMANTIC", "口径与语义", semanticIssues, "结构、口径与受治理语义依赖已固定"),
		gate("FRESHNESS", "数据新鲜度", freshnessIssues(dependencyIssues), "数据集与语义发布版本当前可用"),
		gate("PERMISSION", "权限泄漏", domainIssues, "当前领域和报告权限边界未发现越权"),
		gate("EXECUTION", "组件可执行性", appendIssues(static[5], static[8], normalizeIssues), "组件、联动与规范化检查通过"),
		gate("RESPONSIVE", "移动端适配", static[7], "桌面、移动与打印布局可生成"),
		gate("FACT", "事实与结论核验", insightIssues, "结论证据与当前草稿一致"),
	}
	result := PreflightResult{Draft: draft, Checks: checks, BlockerCodes: []string{}, WarningCodes: []string{}}
	seenBlockers, seenWarnings := map[string]struct{}{}, map[string]struct{}{}
	for _, check := range checks {
		for _, issue := range check.Issues {
			if issueWarning(issue) {
				if _, exists := seenWarnings[issue.Code]; !exists {
					seenWarnings[issue.Code] = struct{}{}
					result.WarningCodes = append(result.WarningCodes, issue.Code)
				}
				continue
			}
			if _, exists := seenBlockers[issue.Code]; !exists {
				seenBlockers[issue.Code] = struct{}{}
				result.BlockerCodes = append(result.BlockerCodes, issue.Code)
			}
		}
	}
	return result, nil
}

func gate(id, label string, issues compiler.ValidationIssues, passedSummary string) GateCheck {
	status := GatePassed
	summary := passedSummary
	if len(issues) != 0 {
		status = GateWarning
		summary = issues[0].Message
		for _, issue := range issues {
			if !issueWarning(issue) {
				status = GateBlocked
				break
			}
		}
	}
	return GateCheck{ID: id, Label: label, Status: status, Summary: summary, Issues: issues}
}

func appendIssues(groups ...compiler.ValidationIssues) compiler.ValidationIssues {
	result := compiler.ValidationIssues{}
	for _, group := range groups {
		result = append(result, group...)
	}
	return result
}

func freshnessIssues(issues compiler.ValidationIssues) compiler.ValidationIssues {
	result := compiler.ValidationIssues{}
	for _, issue := range issues {
		if strings.Contains(issue.Code, "DATASET") || strings.Contains(issue.Code, "RELEASE") || strings.Contains(issue.Code, "DEPENDENCY") {
			result = append(result, issue)
		}
	}
	return result
}

func issueWarning(issue compiler.ValidationIssue) bool {
	return issue.Code == "REPORT_INSIGHT_STALE" || issue.Code == "REPORT_DATA_SNAPSHOT_STALE"
}

func (result PreflightResult) Definition() reportmodel.ReportDefinition {
	return result.Draft.Definition
}
